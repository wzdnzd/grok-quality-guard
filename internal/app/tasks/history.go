package tasks

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HistoryConfig controls the on-disk task archive. Details are loaded only when
// a user opens a task; the list view reads summaries from the compact index.
type HistoryConfig struct {
	Enabled         bool  `json:"enabled"`
	RetentionDays   int   `json:"retention_days"`
	MaxRecords      int   `json:"max_records"`
	MaxBytes        int64 `json:"max_bytes"`
	MaxItemsPerTask int   `json:"max_items_per_task"`
}

func DefaultHistoryConfig() HistoryConfig {
	return HistoryConfig{
		Enabled:         true,
		RetentionDays:   3,
		MaxRecords:      500,
		MaxBytes:        128 << 20,
		MaxItemsPerTask: 10000,
	}
}

func normalizeHistoryConfig(config HistoryConfig) HistoryConfig {
	defaults := DefaultHistoryConfig()
	if config.RetentionDays <= 0 {
		config.RetentionDays = defaults.RetentionDays
	}
	if config.MaxRecords <= 0 {
		config.MaxRecords = defaults.MaxRecords
	}
	if config.MaxBytes <= 0 {
		config.MaxBytes = defaults.MaxBytes
	}
	if config.MaxItemsPerTask <= 0 {
		config.MaxItemsPerTask = defaults.MaxItemsPerTask
	}
	return config
}

type historyEntry struct {
	Task         Task   `json:"task"`
	Archive      string `json:"archive"`
	ArchiveBytes int64  `json:"archive_bytes"`
}

type activeHistory struct {
	BaseDir     string
	MetaPath    string
	ItemsPath   string
	itemsFile   *os.File
	itemsWriter *bufio.Writer
	itemsStored int
	discarded   bool
}

type HistoryStats struct {
	Enabled         bool  `json:"enabled"`
	RetentionDays   int   `json:"retention_days"`
	MaxRecords      int   `json:"max_records"`
	MaxBytes        int64 `json:"max_bytes"`
	MaxItemsPerTask int   `json:"max_items_per_task"`
	RecordCount     int   `json:"record_count"`
	ArchiveBytes    int64 `json:"archive_bytes"`
}

type TaskCenterFilter struct {
	Page     int
	PageSize int
	Category string
	Status   string
	Query    string
}

type TaskCenterResult struct {
	Items    []Task         `json:"items"`
	Total    int            `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Counts   map[string]int `json:"counts"`
	History  HistoryStats   `json:"history"`
}

type TaskDetail struct {
	Task           Task   `json:"task"`
	Items          []any  `json:"items"`
	Total          int    `json:"total"`
	ItemTotal      int    `json:"item_total"`
	Page           int    `json:"page"`
	PageSize       int    `json:"page_size"`
	ItemsTruncated bool   `json:"items_truncated"`
	Source         string `json:"source"`
}

// ConfigureHistory attaches a durable history directory to the manager. Active
// task files are scanned once so a restart can mark interrupted work explicitly.
func (m *Manager) ConfigureHistory(baseDir string, config HistoryConfig) error {
	if m == nil {
		return nil
	}
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return errors.New("task history base directory is empty")
	}
	dir := filepath.Join(baseDir, "task-center")
	config = normalizeHistoryConfig(config)
	m.historyMu.Lock()
	wasConfigured := m.historyConfigured
	previousDir := m.historyDir
	previousConfig := m.historyConfig
	changed := !wasConfigured || previousDir != dir || previousConfig != config
	recover := config.Enabled && (!wasConfigured || previousDir != dir || !previousConfig.Enabled)
	m.historyDir = dir
	m.historyConfig = config
	m.historyConfigured = true
	if changed {
		m.historyLoaded = false
		m.historyIndex = nil
		m.historyCleanupAt = time.Time{}
	}
	if !config.Enabled {
		m.historyLoaded = true
	}
	m.historyMu.Unlock()
	if recover {
		m.recoverActiveTasks()
	}
	return nil
}

func (m *Manager) historyMaxItems() int {
	m.historyMu.Lock()
	defer m.historyMu.Unlock()
	config := normalizeHistoryConfig(m.historyConfig)
	return config.MaxItemsPerTask
}

func (m *Manager) activeHistoryConfig() (string, HistoryConfig, bool) {
	m.historyMu.Lock()
	defer m.historyMu.Unlock()
	config := normalizeHistoryConfig(m.historyConfig)
	return m.historyDir, config, m.historyConfigured && config.Enabled && m.historyDir != ""
}

func (m *Manager) ensureHistoryLayout(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "active"), 0o700); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(dir, "archive"), 0o700)
}

func (m *Manager) openActiveHistoryLocked(task Task) (*activeHistory, error) {
	dir, config, enabled := m.activeHistoryConfig()
	if !enabled {
		return nil, errors.New("task history is disabled")
	}
	if err := m.ensureHistoryLayout(dir); err != nil {
		return nil, err
	}
	base := filepath.Base(task.ID)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return nil, errors.New("task id is invalid")
	}
	activeDir := filepath.Join(dir, "active")
	entry := &activeHistory{
		BaseDir:   dir,
		MetaPath:  filepath.Join(activeDir, base+".json"),
		ItemsPath: filepath.Join(activeDir, base+".items.jsonl"),
	}
	if err := writeJSONFile(entry.MetaPath, task); err != nil {
		return nil, err
	}
	itemsFile, errOpen := os.OpenFile(entry.ItemsPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if errOpen != nil {
		_ = os.Remove(entry.MetaPath)
		return nil, errOpen
	}
	entry.itemsFile = itemsFile
	entry.itemsWriter = bufio.NewWriterSize(itemsFile, 64*1024)
	_ = config
	return entry, nil
}

func (m *Manager) persistActiveMetadataLocked(entry *record, force bool) {
	if entry == nil || entry.history == nil {
		return
	}
	now := time.Now().UTC()
	if !force && !entry.lastMetaWrite.IsZero() && now.Sub(entry.lastMetaWrite) < time.Second {
		return
	}
	if writeJSONFile(entry.history.MetaPath, entry.task) == nil {
		entry.lastMetaWrite = now
	}
}

func writeJSONFile(path string, value any) error {
	raw, errMarshal := json.MarshalIndent(value, "", "  ")
	if errMarshal != nil {
		return errMarshal
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func appendActiveItem(history *activeHistory, raw json.RawMessage, maxItems int) bool {
	if history == nil || history.itemsWriter == nil || len(raw) == 0 {
		return false
	}
	if maxItems <= 0 {
		maxItems = DefaultHistoryConfig().MaxItemsPerTask
	}
	if history.itemsStored >= maxItems {
		return false
	}
	if _, errWrite := history.itemsWriter.Write(append(raw, '\n')); errWrite != nil {
		return false
	}
	history.itemsStored++
	return true
}

func writeFinalResultItems(history *activeHistory, task *Task, maxItems int) {
	if history == nil || task == nil || history.itemsWriter == nil {
		return
	}
	items := extractResultItems(task.Result)
	if len(items) == 0 {
		task.ItemCount = maxInt(task.ItemCount, history.itemsStored)
		task.ItemsStored = history.itemsStored
		if task.ItemCount > history.itemsStored {
			task.ItemsTruncated = true
		}
		return
	}
	if maxItems <= 0 {
		maxItems = DefaultHistoryConfig().MaxItemsPerTask
	}
	task.ItemCount = maxInt(task.ItemCount, len(items))
	limit := minInt(len(items), maxItems)
	// ReportItem streams every processed item during execution. Batch results may
	// intentionally contain only a bounded sample, so never replace a more
	// complete active item log with a shorter final result.
	if history.itemsStored >= limit {
		task.ItemsStored = history.itemsStored
		if task.ItemCount > history.itemsStored {
			task.ItemsTruncated = true
		}
		return
	}
	if limit < len(items) {
		task.ItemsTruncated = true
	}
	_ = history.itemsWriter.Flush()
	if history.itemsFile != nil {
		_ = history.itemsFile.Truncate(0)
		_, _ = history.itemsFile.Seek(0, io.SeekStart)
	}
	history.itemsWriter.Reset(history.itemsFile)
	history.itemsStored = 0
	for _, item := range items[:limit] {
		line, errMarshal := json.Marshal(item)
		if errMarshal != nil {
			continue
		}
		if _, errWrite := history.itemsWriter.Write(append(line, '\n')); errWrite != nil {
			return
		}
		history.itemsStored++
	}
	task.ItemsStored = history.itemsStored
	if task.ItemCount > history.itemsStored {
		task.ItemsTruncated = true
	}
}
func extractResultItems(result any) []json.RawMessage {
	if result == nil {
		return nil
	}
	raw, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		return nil
	}
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return nil
	}
	return resultItems(root)
}

func resultItems(root map[string]any) []json.RawMessage {
	if root == nil {
		return nil
	}
	for _, key := range []string{"items", "results", "samples"} {
		values, ok := root[key].([]any)
		if !ok || len(values) == 0 {
			continue
		}
		items := make([]json.RawMessage, 0, len(values))
		for _, value := range values {
			raw, errMarshal := json.Marshal(value)
			if errMarshal == nil {
				items = append(items, json.RawMessage(raw))
			}
		}
		if len(items) > 0 {
			return items
		}
	}
	return nil
}

func summaryResult(result any) any {
	if result == nil {
		return nil
	}
	raw, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		return result
	}
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return result
	}
	for _, key := range []string{"items", "results", "samples"} {
		delete(root, key)
	}
	return root
}

func flushActiveHistory(history *activeHistory) {
	if history == nil {
		return
	}
	if history.itemsWriter != nil {
		_ = history.itemsWriter.Flush()
	}
	if history.itemsFile != nil {
		_ = history.itemsFile.Sync()
	}
}

func closeActiveHistory(history *activeHistory) {
	if history == nil {
		return
	}
	flushActiveHistory(history)
	if history.itemsFile != nil {
		_ = history.itemsFile.Close()
		history.itemsFile = nil
	}
	history.itemsWriter = nil
}

func (m *Manager) finalizeHistory(task Task, history *activeHistory) bool {
	if history == nil || strings.TrimSpace(history.BaseDir) == "" {
		return false
	}
	closeActiveHistory(history)
	if _, _, enabled := m.activeHistoryConfig(); !enabled {
		// A task can start while history is enabled and finish after the user
		// turns it off. Keep the final result only in the in-memory waiter cache.
		history.discarded = true
		_ = os.Remove(history.MetaPath)
		_ = os.Remove(history.ItemsPath)
		return false
	}
	archiveDir := filepath.Join(history.BaseDir, "archive", task.EndedAt.UTC().Format("2006-01"))
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		return false
	}
	archivePath := filepath.Join(archiveDir, filepath.Base(task.ID)+".json.gz")
	if err := writeTaskArchive(archivePath, task, history.ItemsPath); err != nil {
		return false
	}
	info, errStat := os.Stat(archivePath)
	if errStat != nil {
		return false
	}

	m.historyWriteMu.Lock()
	defer m.historyWriteMu.Unlock()
	if err := appendHistoryIndex(history.BaseDir, historyEntry{Task: HistorySummary(task), Archive: archivePath, ArchiveBytes: info.Size()}); err != nil {
		return false
	}
	_ = os.Remove(history.MetaPath)
	_ = os.Remove(history.ItemsPath)

	m.historyMu.Lock()
	if m.historyDir == history.BaseDir {
		m.historyLoaded = false
		m.historyIndex = nil
	}
	m.historyMu.Unlock()
	_, _ = m.loadHistory()
	return true
}

func HistorySummary(task Task) Task {
	task.Result = summaryResult(task.Result)
	task.AuthIDs = append([]string(nil), task.AuthIDs...)
	if len(task.AuthIDs) > 20 {
		task.AuthIDs = task.AuthIDs[:20]
	}
	return task
}

func writeTaskArchive(path string, task Task, itemsPath string) error {
	temp, errTemp := os.CreateTemp(filepath.Dir(path), ".task-*.tmp")
	if errTemp != nil {
		return errTemp
	}
	tempName := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempName)
		}
	}()
	gz := gzip.NewWriter(temp)
	header, errMarshal := json.Marshal(HistorySummary(task))
	if errMarshal != nil {
		return errMarshal
	}
	if _, errWrite := gz.Write(append(header, '\n')); errWrite != nil {
		return errWrite
	}
	items, errOpen := os.Open(itemsPath)
	if errOpen == nil {
		_, errCopy := io.Copy(gz, items)
		_ = items.Close()
		if errCopy != nil {
			return errCopy
		}
	} else if !errors.Is(errOpen, os.ErrNotExist) {
		return errOpen
	}
	if errClose := gz.Close(); errClose != nil {
		return errClose
	}
	if errClose := temp.Close(); errClose != nil {
		return errClose
	}
	if errRename := os.Rename(tempName, path); errRename != nil {
		return errRename
	}
	committed = true
	return nil
}

func appendHistoryIndex(dir string, entry historyEntry) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, errMarshal := json.Marshal(entry)
	if errMarshal != nil {
		return errMarshal
	}
	file, errOpen := os.OpenFile(filepath.Join(dir, "index.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if errOpen != nil {
		return errOpen
	}
	defer file.Close()
	_, errWrite := file.Write(append(raw, '\n'))
	return errWrite
}

func (m *Manager) loadHistory() ([]historyEntry, error) {
	m.historyMu.Lock()
	defer m.historyMu.Unlock()
	return m.loadHistoryLocked()
}

func (m *Manager) loadHistoryLocked() ([]historyEntry, error) {
	if m.historyLoaded {
		return append([]historyEntry(nil), m.historyIndex...), nil
	}
	m.historyLoaded = true
	m.historyIndex = nil
	if m.historyDir == "" || !m.historyConfig.Enabled {
		return nil, nil
	}
	file, errOpen := os.Open(filepath.Join(m.historyDir, "index.jsonl"))
	if errors.Is(errOpen, os.ErrNotExist) {
		return nil, nil
	}
	if errOpen != nil {
		m.historyLoaded = false
		return nil, errOpen
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	byID := make(map[string]historyEntry)
	for scanner.Scan() {
		var entry historyEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || strings.TrimSpace(entry.Task.ID) == "" {
			continue
		}
		byID[entry.Task.ID] = entry
	}
	if errScan := scanner.Err(); errScan != nil {
		_ = file.Close()
		return nil, errScan
	}
	if errClose := file.Close(); errClose != nil {
		m.historyLoaded = false
		return nil, errClose
	}
	for _, entry := range byID {
		if entry.ArchiveBytes <= 0 {
			if info, errStat := os.Stat(entry.Archive); errStat == nil {
				entry.ArchiveBytes = info.Size()
			}
		}
		m.historyIndex = append(m.historyIndex, entry)
	}
	now := time.Now().UTC()
	m.cleanupHistoryLocked(now)
	m.historyCleanupAt = now
	return append([]historyEntry(nil), m.historyIndex...), nil
}
func (m *Manager) cleanupHistoryIfDue(now time.Time) {
	if m == nil {
		return
	}
	m.historyMu.Lock()
	defer m.historyMu.Unlock()
	if !m.historyLoaded || m.historyDir == "" || !m.historyConfig.Enabled {
		return
	}
	if !m.historyCleanupAt.IsZero() && now.Sub(m.historyCleanupAt) < time.Minute {
		return
	}
	m.cleanupHistoryLocked(now)
	m.historyCleanupAt = now
}

func (m *Manager) cleanupHistoryLocked(now time.Time) {
	if m.historyDir == "" {
		return
	}
	config := normalizeHistoryConfig(m.historyConfig)
	sort.Slice(m.historyIndex, func(i, j int) bool {
		return historyTime(m.historyIndex[i].Task).After(historyTime(m.historyIndex[j].Task))
	})
	cutoff := now.Add(-time.Duration(config.RetentionDays) * 24 * time.Hour)
	kept := make([]historyEntry, 0, len(m.historyIndex))
	changed := false
	for _, entry := range m.historyIndex {
		if historyTime(entry.Task).Before(cutoff) {
			if removeHistoryArchive(entry.Archive) {
				changed = true
				continue
			}
			kept = append(kept, entry)
			continue
		}
		if entry.ArchiveBytes <= 0 {
			if info, errStat := os.Stat(entry.Archive); errStat == nil {
				entry.ArchiveBytes = info.Size()
			} else if errors.Is(errStat, os.ErrNotExist) {
				changed = true
				continue
			} else {
				kept = append(kept, entry)
				continue
			}
		}
		kept = append(kept, entry)
	}
	if len(kept) > config.MaxRecords {
		trimmed := make([]historyEntry, 0, len(kept))
		for index, entry := range kept {
			if index < config.MaxRecords {
				trimmed = append(trimmed, entry)
				continue
			}
			if removeHistoryArchive(entry.Archive) {
				changed = true
				continue
			}
			trimmed = append(trimmed, entry)
		}
		kept = trimmed
	}
	var total int64
	withinBytes := make([]historyEntry, 0, len(kept))
	for _, entry := range kept {
		if total+entry.ArchiveBytes > config.MaxBytes && removeHistoryArchive(entry.Archive) {
			changed = true
			continue
		}
		total += entry.ArchiveBytes
		withinBytes = append(withinBytes, entry)
	}
	kept = withinBytes
	m.historyIndex = kept
	if changed {
		_ = rewriteHistoryIndex(m.historyDir, kept)
	}
}

func (m *Manager) historyBaseDir() string {
	if m == nil {
		return ""
	}
	m.historyMu.Lock()
	defer m.historyMu.Unlock()
	return m.historyDir
}

func removeActiveTaskFiles(dir, taskID string) error {
	dir = strings.TrimSpace(dir)
	base := filepath.Base(strings.TrimSpace(taskID))
	if dir == "" || base == "" || base == "." || base == string(filepath.Separator) {
		return nil
	}
	for _, suffix := range []string{".json", ".items.jsonl"} {
		path := filepath.Join(dir, "active", base+suffix)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (m *Manager) deleteArchivedTask(taskID string) (bool, error) {
	if m == nil {
		return false, nil
	}
	m.historyWriteMu.Lock()
	defer m.historyWriteMu.Unlock()
	m.historyMu.Lock()
	dir := m.historyDir
	enabled := m.historyConfig.Enabled
	m.historyMu.Unlock()
	if dir == "" || !enabled {
		return false, nil
	}
	entries, errLoad := m.loadHistory()
	if errLoad != nil {
		return false, errLoad
	}
	found := false
	kept := make([]historyEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Task.ID != taskID {
			kept = append(kept, entry)
			continue
		}
		found = true
		if errRemove := os.Remove(entry.Archive); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			return false, errRemove
		}
	}
	if !found {
		return false, nil
	}
	if errRewrite := rewriteHistoryIndex(dir, kept); errRewrite != nil {
		return false, errRewrite
	}
	m.historyMu.Lock()
	if m.historyDir == dir {
		m.historyLoaded = false
		m.historyIndex = nil
	}
	m.historyMu.Unlock()
	_, _ = m.loadHistory()
	return true, nil
}

func removeHistoryArchive(path string) bool {
	if strings.TrimSpace(path) == "" {
		return true
	}
	err := os.Remove(path)
	return err == nil || errors.Is(err, os.ErrNotExist)
}

func rewriteHistoryIndex(dir string, entries []historyEntry) error {
	var builder strings.Builder
	for _, entry := range entries {
		raw, errMarshal := json.Marshal(entry)
		if errMarshal == nil {
			builder.Write(raw)
			builder.WriteByte('\n')
		}
	}
	temp, errTemp := os.CreateTemp(dir, ".index-*.tmp")
	if errTemp != nil {
		return errTemp
	}
	tempName := temp.Name()
	if _, errWrite := temp.WriteString(builder.String()); errWrite != nil {
		_ = temp.Close()
		_ = os.Remove(tempName)
		return errWrite
	}
	if errSync := temp.Sync(); errSync != nil {
		_ = temp.Close()
		_ = os.Remove(tempName)
		return errSync
	}
	if errClose := temp.Close(); errClose != nil {
		_ = os.Remove(tempName)
		return errClose
	}
	indexPath := filepath.Join(dir, "index.jsonl")
	if errRename := os.Rename(tempName, indexPath); errRename != nil {
		// Windows does not replace an existing destination with os.Rename.
		// The old index is already represented by the temporary file, so a
		// short remove-and-rename window is preferable to leaving stale limits.
		if errRemove := os.Remove(indexPath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			_ = os.Remove(tempName)
			return errRemove
		}
		if errRetry := os.Rename(tempName, indexPath); errRetry != nil {
			_ = os.Remove(tempName)
			return errRetry
		}
	}
	return nil
}

func historyTime(task Task) time.Time {
	if !task.EndedAt.IsZero() {
		return task.EndedAt
	}
	return task.CreatedAt
}

func (m *Manager) historyTask(taskID string) (Task, bool) {
	entries, errLoad := m.loadHistory()
	if errLoad != nil {
		return Task{}, false
	}
	for _, entry := range entries {
		if entry.Task.ID != taskID {
			continue
		}
		task, items, errRead := readTaskArchive(entry.Archive)
		if errRead != nil {
			return Task{}, false
		}
		return restoreArchivedItems(task, items), true
	}
	return Task{}, false
}

func restoreArchivedItems(task Task, items []json.RawMessage) Task {
	if len(items) == 0 {
		return task
	}
	result := make(map[string]any)
	if existing, ok := task.Result.(map[string]any); ok {
		for key, value := range existing {
			result[key] = value
		}
	}
	values := make([]any, 0, len(items))
	for _, raw := range items {
		var value any
		if json.Unmarshal(raw, &value) == nil {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return task
	}
	result["items"] = values
	task.Result = result
	return task
}

func scanTaskArchiveItems(path string, onItem func(json.RawMessage) error) (Task, error) {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return Task{}, errOpen
	}
	defer file.Close()
	gz, errGzip := gzip.NewReader(file)
	if errGzip != nil {
		return Task{}, errGzip
	}
	defer gz.Close()
	reader := bufio.NewReaderSize(gz, 64*1024)
	header, errHeader := reader.ReadBytes('\n')
	if errHeader != nil && !errors.Is(errHeader, io.EOF) {
		return Task{}, errHeader
	}
	var task Task
	if json.Unmarshal(header, &task) != nil {
		return Task{}, errors.New("task archive header is invalid")
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		line = []byte(strings.TrimSpace(string(line)))
		if len(line) > 0 {
			if errItem := onItem(json.RawMessage(line)); errItem != nil {
				return Task{}, errItem
			}
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return Task{}, errScan
	}
	return task, nil
}

func readTaskArchive(path string) (Task, []json.RawMessage, error) {
	var items []json.RawMessage
	task, errScan := scanTaskArchiveItems(path, func(item json.RawMessage) error {
		items = append(items, append(json.RawMessage(nil), item...))
		return nil
	})
	return task, items, errScan
}

func (m *Manager) ListCenter(filter TaskCenterFilter) (TaskCenterResult, error) {
	return m.ListCenterContext(context.Background(), filter)
}

// ListCenterContext is the cancellable form used by the management handler's
// bounded heavy-action budget. The on-disk history load itself is not
// cancellable, but merging, counting, and filtering check between entries.
func (m *Manager) ListCenterContext(ctx context.Context, filter TaskCenterFilter) (TaskCenterResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return TaskCenterResult{}, err
	}
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 20
	}
	if filter.PageSize > 200 {
		filter.PageSize = 200
	}
	entries, stats, errHistory := m.historyEntriesForCenter(time.Now().UTC())
	if errHistory != nil {
		return TaskCenterResult{}, errHistory
	}
	m.mu.Lock()
	memory := make([]Task, 0, len(m.tasks))
	for _, entry := range m.tasks {
		if entry != nil {
			memory = append(memory, HistorySummary(entry.task))
		}
	}
	m.mu.Unlock()
	sort.Slice(memory, func(i, j int) bool { return memory[i].CreatedAt.After(memory[j].CreatedAt) })
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Task.CreatedAt.After(entries[j].Task.CreatedAt)
	})
	if err := ctx.Err(); err != nil {
		return TaskCenterResult{}, err
	}
	category := strings.TrimSpace(filter.Category)
	status := strings.TrimSpace(filter.Status)
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	counts := emptyTaskCenterCounts()
	total := 0
	if err := forEachMergedTaskContext(ctx, memory, entries, func(task Task) {
		addTaskCenterCounts(counts, task)
		if taskCenterMatchesFilter(task, category, status, query) {
			total++
		}
	}); err != nil {
		return TaskCenterResult{}, err
	}
	pages := maxInt(1, (total+filter.PageSize-1)/filter.PageSize)
	if filter.Page > pages {
		filter.Page = pages
	}
	start := (filter.Page - 1) * filter.PageSize
	items := make([]Task, 0, minInt(filter.PageSize, maxInt(0, total-start)))
	matched := 0
	if err := forEachMergedTaskContext(ctx, memory, entries, func(task Task) {
		if !taskCenterMatchesFilter(task, category, status, query) {
			return
		}
		if matched >= start && len(items) < filter.PageSize {
			items = append(items, task)
		}
		matched++
	}); err != nil {
		return TaskCenterResult{}, err
	}
	return TaskCenterResult{Items: items, Total: total, Page: filter.Page, PageSize: filter.PageSize, Counts: counts, History: stats}, nil
}

func (m *Manager) historyEntriesForCenter(now time.Time) ([]historyEntry, HistoryStats, error) {
	m.historyMu.Lock()
	defer m.historyMu.Unlock()
	if !m.historyLoaded {
		if _, err := m.loadHistoryLocked(); err != nil {
			return nil, HistoryStats{}, err
		}
	} else if !m.historyCleanupAt.IsZero() && now.Sub(m.historyCleanupAt) >= time.Minute {
		m.cleanupHistoryLocked(now)
		m.historyCleanupAt = now
	}
	config := normalizeHistoryConfig(m.historyConfig)
	var bytes int64
	for _, entry := range m.historyIndex {
		bytes += entry.ArchiveBytes
	}
	stats := HistoryStats{
		Enabled:         config.Enabled,
		RetentionDays:   config.RetentionDays,
		MaxRecords:      config.MaxRecords,
		MaxBytes:        config.MaxBytes,
		MaxItemsPerTask: config.MaxItemsPerTask,
		RecordCount:     len(m.historyIndex),
		ArchiveBytes:    bytes,
	}
	return append([]historyEntry(nil), m.historyIndex...), stats, nil
}

func emptyTaskCenterCounts() map[string]int {
	return map[string]int{
		"active": 0, "queued": 0, "running": 0, "succeeded": 0, "failed": 0,
		"cancelled": 0, "interrupted": 0, "total": 0, "running_items": 0,
		"waiting_items": 0, "succeeded_items": 0, "failed_items": 0, "skipped_items": 0,
	}
}

func addTaskCenterCounts(counts map[string]int, task Task) {
	counts["total"]++
	counts["running_items"] += task.Running
	counts["waiting_items"] += task.Waiting
	counts["succeeded_items"] += task.Succeeded
	counts["failed_items"] += task.Failed
	counts["skipped_items"] += task.Skipped
	if task.Status == taskStatusQueued || task.Status == taskStatusRunning {
		counts["active"]++
	}
	if _, ok := counts[task.Status]; ok {
		counts[task.Status]++
	}
}

func taskCenterMatchesFilter(task Task, category, status, query string) bool {
	if category != "" && category != "all" && task.Category != category {
		return false
	}
	if status != "" && status != "all" {
		if status == "active" {
			if task.Status != taskStatusQueued && task.Status != taskStatusRunning {
				return false
			}
		} else if status == "finished" {
			if task.Status == taskStatusQueued || task.Status == taskStatusRunning {
				return false
			}
		} else if task.Status != status {
			return false
		}
	}
	if query == "" {
		return true
	}
	text := strings.ToLower(strings.Join([]string{task.ID, task.Kind, task.Label, task.Category, strings.Join(task.AuthIDs, " "), task.Error}, " "))
	return strings.Contains(text, query)
}

func forEachMergedTaskContext(ctx context.Context, memory []Task, entries []historyEntry, visit func(Task)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	memoryIDs := make(map[string]struct{}, len(memory))
	for _, task := range memory {
		memoryIDs[task.ID] = struct{}{}
	}
	memoryIndex, historyIndex := 0, 0
	visited := 0
	for memoryIndex < len(memory) || historyIndex < len(entries) {
		if visited%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		var task Task
		if historyIndex >= len(entries) || (memoryIndex < len(memory) && !memory[memoryIndex].CreatedAt.Before(entries[historyIndex].Task.CreatedAt)) {
			task = memory[memoryIndex]
			memoryIndex++
		} else {
			task = entries[historyIndex].Task
			historyIndex++
			if _, exists := memoryIDs[task.ID]; exists {
				continue
			}
		}
		visit(task)
		visited++
	}
	return nil
}
func (m *Manager) TaskDetail(taskID string, page, pageSize int, statusFilter string) (TaskDetail, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return TaskDetail{}, ErrTaskNotFound
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	m.mu.Lock()
	entry := m.tasks[taskID]
	if entry != nil {
		if entry.history != nil {
			flushActiveHistory(entry.history)
		}
		task := Clone(entry.task)
		var itemsPath string
		if entry.history != nil {
			itemsPath = entry.history.ItemsPath
		}
		m.mu.Unlock()
		if strings.TrimSpace(itemsPath) != "" {
			detail, scanned, errDetail := taskDetailFromItemFile(task, itemsPath, page, pageSize, statusFilter, "active")
			if errDetail != nil && !errors.Is(errDetail, os.ErrNotExist) {
				return TaskDetail{}, errDetail
			}
			if scanned > 0 {
				return detail, nil
			}
		}
		items := extractResultItems(task.Result)
		return buildTaskDetail(task, items, page, pageSize, statusFilter, "active")
	}
	m.mu.Unlock()
	entries, errLoad := m.loadHistory()
	if errLoad != nil {
		return TaskDetail{}, errLoad
	}
	for _, entry := range entries {
		if entry.Task.ID != taskID {
			continue
		}
		detail, errRead := taskDetailFromArchive(entry.Archive, page, pageSize, statusFilter)
		if errRead != nil {
			return TaskDetail{}, errRead
		}
		return detail, nil
	}
	return TaskDetail{}, ErrTaskNotFound
}

func taskDetailFromItemFile(task Task, path string, page, pageSize int, statusFilter, source string) (TaskDetail, int, error) {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return TaskDetail{}, 0, errOpen
	}
	defer file.Close()
	return paginateTaskItems(task, func(onItem func(json.RawMessage) error) error {
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			if len(line) == 0 {
				continue
			}
			if errItem := onItem(json.RawMessage(line)); errItem != nil {
				return errItem
			}
		}
		return scanner.Err()
	}, page, pageSize, statusFilter, source)
}

func taskDetailFromArchive(path string, page, pageSize int, statusFilter string) (TaskDetail, error) {
	for attempt := 0; attempt < 2; attempt++ {
		detail, scanned, errPage := taskDetailFromArchivePage(path, page, pageSize, statusFilter)
		if errPage != nil {
			return TaskDetail{}, errPage
		}
		if scanned == 0 || detail.Total > 0 || page == 1 {
			return detail, nil
		}
		page = 1
	}
	detail, _, errPage := taskDetailFromArchivePage(path, 1, pageSize, statusFilter)
	return detail, errPage
}

func taskDetailFromArchivePage(path string, page, pageSize int, statusFilter string) (TaskDetail, int, error) {
	var detail TaskDetail
	var scanned int
	task, errScan := scanTaskArchiveItems(path, func(item json.RawMessage) error {
		return nil
	})
	if errScan != nil {
		return TaskDetail{}, 0, errScan
	}
	detail, scanned, errScan = paginateTaskItems(task, func(onItem func(json.RawMessage) error) error {
		_, err := scanTaskArchiveItems(path, func(item json.RawMessage) error { return onItem(item) })
		return err
	}, page, pageSize, statusFilter, "history")
	return detail, scanned, errScan
}

func paginateTaskItems(task Task, scan func(func(json.RawMessage) error) error, page, pageSize int, statusFilter, source string) (TaskDetail, int, error) {
	statusFilter = strings.ToLower(strings.TrimSpace(statusFilter))
	if task.ItemsStored > 0 {
		pages := maxInt(1, (task.ItemsStored+pageSize-1)/pageSize)
		if page > pages {
			page = pages
		}
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	matched := 0
	scanned := 0
	values := make([]any, 0, pageSize)
	errScan := scan(func(raw json.RawMessage) error {
		scanned++
		var value any
		if json.Unmarshal(raw, &value) != nil {
			return nil
		}
		if statusFilter != "" && statusFilter != "all" {
			valueMap, _ := value.(map[string]any)
			if valueMap == nil {
				return nil
			}
			itemStatus := stringFromAny(valueMap["status"])
			outcome := itemOutcomeFromMap(valueMap, itemStatus)
			if statusFilter == "succeeded" || statusFilter == "failed" || statusFilter == "skipped" {
				if outcome != statusFilter {
					return nil
				}
			} else if strings.ToLower(itemStatus) != statusFilter {
				return nil
			}
		}
		if matched >= start && matched < end {
			values = append(values, value)
		}
		matched++
		return nil
	})
	if errScan != nil {
		return TaskDetail{}, scanned, errScan
	}
	pages := maxInt(1, (matched+pageSize-1)/pageSize)
	if page > pages {
		return TaskDetail{Task: task, Total: 0, ItemTotal: scanned, Page: page, PageSize: pageSize, ItemsTruncated: task.ItemsTruncated, Source: source}, scanned, nil
	}
	return TaskDetail{Task: task, Items: values, Total: matched, ItemTotal: scanned, Page: page, PageSize: pageSize, ItemsTruncated: task.ItemsTruncated, Source: source}, scanned, nil
}

func countItemsFile(path string) (int, error) {
	if strings.TrimSpace(path) == "" {
		return 0, nil
	}
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return 0, errOpen
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	count := 0
	for scanner.Scan() {
		if len(strings.TrimSpace(string(scanner.Bytes()))) > 0 {
			count++
		}
	}
	return count, scanner.Err()
}
func buildTaskDetail(task Task, items []json.RawMessage, page, pageSize int, statusFilter, source string) (TaskDetail, error) {
	statusFilter = strings.ToLower(strings.TrimSpace(statusFilter))
	all := make([]any, 0, len(items))
	for _, raw := range items {
		var value any
		if json.Unmarshal(raw, &value) != nil {
			continue
		}
		if statusFilter != "" && statusFilter != "all" {
			itemStatus := ""
			itemError := false
			valueMap, _ := value.(map[string]any)
			if valueMap != nil {
				itemStatus = stringFromAny(valueMap["status"])
				itemError = stringFromAny(valueMap["error"]) != ""
			}
			outcome := itemOutcomeFromMap(valueMap, itemStatus)
			if itemError {
				outcome = "failed"
			}
			if statusFilter == "succeeded" || statusFilter == "failed" || statusFilter == "skipped" {
				if outcome != statusFilter {
					continue
				}
			} else if strings.ToLower(itemStatus) != statusFilter {
				continue
			}
		}
		all = append(all, value)
	}
	total := len(all)
	pages := maxInt(1, (total+pageSize-1)/pageSize)
	if page > pages {
		page = pages
	}
	start := (page - 1) * pageSize
	end := minInt(total, start+pageSize)
	if start > total {
		start = total
	}
	return TaskDetail{
		Task:           task,
		Items:          append([]any(nil), all[start:end]...),
		Total:          total,
		ItemTotal:      len(items),
		Page:           page,
		PageSize:       pageSize,
		ItemsTruncated: task.ItemsTruncated,
		Source:         source,
	}, nil
}

func (m *Manager) recoverActiveTasks() {
	dir, config, enabled := m.activeHistoryConfig()
	if !enabled || dir == "" {
		return
	}
	activeDir := filepath.Join(dir, "active")
	files, errRead := os.ReadDir(activeDir)
	if errRead != nil {
		return
	}
	now := time.Now().UTC()
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		metaPath := filepath.Join(activeDir, file.Name())
		raw, errReadFile := os.ReadFile(metaPath)
		if errReadFile != nil {
			continue
		}
		var task Task
		if json.Unmarshal(raw, &task) != nil || strings.TrimSpace(task.ID) == "" {
			continue
		}
		m.mu.Lock()
		_, running := m.tasks[task.ID]
		m.mu.Unlock()
		if running {
			continue
		}
		id := strings.TrimSuffix(file.Name(), ".json")
		task.Status = taskStatusInterrupted
		task.CurrentStage = "task.stage.interrupted"
		task.Progress = ""
		task.Error = "task.error.interrupted"
		task.Waiting = 0
		task.Running = 0
		task.UpdatedAt = now
		task.EndedAt = now
		if !task.StartedAt.IsZero() {
			task.DurationMS = maxInt64(0, now.Sub(task.StartedAt).Milliseconds())
		}
		history := &activeHistory{
			BaseDir:   dir,
			MetaPath:  metaPath,
			ItemsPath: filepath.Join(activeDir, id+".items.jsonl"),
		}
		if count, errCount := countItemsFile(history.ItemsPath); errCount == nil && count > 0 {
			task.ItemCount = count
			task.ItemsStored = count
			if count > config.MaxItemsPerTask {
				task.ItemsTruncated = true
			}
		}
		_ = m.finalizeHistory(task, history)
	}
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func numberFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := strconv.Atoi(typed.String())
		return parsed
	default:
		return 0
	}
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
