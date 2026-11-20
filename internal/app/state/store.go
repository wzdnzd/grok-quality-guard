package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"grok-quality-guard/internal/config"
	proxy "grok-quality-guard/internal/proxy"
)

type Metrics struct {
	FirstTokenMS    int64   `json:"first_token_ms,omitempty"`
	DurationMS      int64   `json:"duration_ms,omitempty"`
	GenerationMS    int64   `json:"generation_ms,omitempty"`
	OutputTokens    int64   `json:"output_tokens,omitempty"`
	ReasoningTokens int64   `json:"reasoning_tokens,omitempty"`
	VisibleTokens   int64   `json:"visible_tokens,omitempty"`
	EncryptedBytes  int64   `json:"encrypted_bytes,omitempty"`
	TPS             float64 `json:"tps,omitempty"`
}

type QualityEvidence struct {
	At                time.Time `json:"at,omitzero"`
	Source            string    `json:"source,omitempty"`
	Action            string    `json:"action,omitempty"`
	Class             string    `json:"class,omitempty"`
	Reason            string    `json:"reason,omitempty"`
	Protocol          string    `json:"protocol,omitempty"`
	Streaming         bool      `json:"streaming,omitempty"`
	Terminal          bool      `json:"terminal,omitempty"`
	HoldExpired       bool      `json:"hold_expired,omitempty"`
	HasThinking       bool      `json:"has_thinking,omitempty"`
	HasReasoningDelta bool      `json:"has_reasoning_delta,omitempty"`
	ReasoningRequired bool      `json:"reasoning_required,omitempty"`
	ReasoningStarted  bool      `json:"reasoning_started,omitempty"`
	SemanticOutput    bool      `json:"semantic_output,omitempty"`
	UsageReported     bool      `json:"usage_reported,omitempty"`
	VisibleRunes      int64     `json:"visible_runes,omitempty"`
	VisibleTokens     int64     `json:"visible_tokens,omitempty"`
	OutputTokens      int64     `json:"output_tokens,omitempty"`
	ReasoningTokens   int64     `json:"reasoning_tokens,omitempty"`
	EncryptedBytes    int64     `json:"encrypted_bytes,omitempty"`
	EncryptedFloor    int64     `json:"encrypted_floor,omitempty"`
	FirstTokenMS      int64     `json:"first_token_ms,omitempty"`
	DurationMS        int64     `json:"duration_ms,omitempty"`
	GenerationMS      int64     `json:"generation_ms,omitempty"`
	VisibleFlushMS    int64     `json:"visible_flush_ms,omitempty"`
	VisibleSpanMS     int64     `json:"visible_span_ms,omitempty"`
	VisibleChunks     int       `json:"visible_chunks,omitempty"`
	TPS               float64   `json:"tps,omitempty"`
	ChunkCount        int       `json:"chunk_count,omitempty"`
	ByteCount         int64     `json:"byte_count,omitempty"`
	EventCount        int       `json:"event_count,omitempty"`
	ParsedEvents      int       `json:"parsed_events,omitempty"`
	UnknownEvents     int       `json:"unknown_events,omitempty"`
	ParseFailures     int       `json:"parse_failures,omitempty"`
}

type AttemptEvidence struct {
	At     time.Time `json:"at,omitzero"`
	Source string    `json:"source,omitempty"`
	Class  string    `json:"class,omitempty"`
	Reason string    `json:"reason,omitempty"`
}
type ResinLeaseDelete struct {
	AuthID        string    `json:"auth_id,omitempty"`
	AdminURL      string    `json:"admin_url,omitempty"`
	PlatformID    string    `json:"platform_id,omitempty"`
	Account       string    `json:"account"`
	SnapshotID    string    `json:"snapshot_id"`
	Paused        bool      `json:"paused,omitempty"`
	Status        string    `json:"status,omitempty"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
	Attempts      int       `json:"attempts,omitempty"`
	NextAttemptAt time.Time `json:"next_attempt_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	LastErrorCode string    `json:"last_error_code,omitempty"`
}

type AccountState struct {
	AuthID                       string                     `json:"auth_id"`
	State                        string                     `json:"state"`
	LastReason                   string                     `json:"last_reason,omitempty"`
	LastAttempt                  *AttemptEvidence           `json:"last_attempt,omitempty"`
	LastEvidence                 *QualityEvidence           `json:"last_evidence,omitempty"`
	FirstSeenAt                  time.Time                  `json:"first_seen_at,omitzero"`
	LastObservedAt               time.Time                  `json:"last_observed_at,omitzero"`
	LastProbeAt                  time.Time                  `json:"last_probe_at,omitzero"`
	CooldownUntil                time.Time                  `json:"cooldown_until,omitzero"`
	QualityStrikeMissingThinking bool                       `json:"quality_strike_missing_thinking,omitempty"`
	QualityFailureAt             time.Time                  `json:"quality_failure_at,omitzero"`
	HealthyStreak                int                        `json:"healthy_streak,omitempty"`
	HealthyStreakStartedAt       time.Time                  `json:"healthy_streak_started_at,omitzero"`
	SoftStrikes                  int                        `json:"soft_strikes,omitempty"`
	ErrorStrikes                 int                        `json:"error_strikes,omitempty"`
	ThinkingStrikes              int                        `json:"thinking_strikes,omitempty"`
	ProbeBackoffUntil            time.Time                  `json:"probe_backoff_until,omitzero"`
	DisableKind                  string                     `json:"disable_kind,omitempty"`
	DisableReason                string                     `json:"disable_reason,omitempty"`
	PluginOnlyDisable            bool                       `json:"plugin_only_disable,omitempty"`
	PreviousDisabled             bool                       `json:"previous_disabled,omitempty"`
	AuthDisabledIntent           *bool                      `json:"auth_disabled_intent,omitempty"`
	CatalogMissingSince          time.Time                  `json:"catalog_missing_since,omitzero"`
	Proxy                        proxy.Binding              `json:"proxy_binding,omitempty"`
	AutoBind                     proxy.AutoBindAccountState `json:"auto_bind,omitempty"`
	Metrics                      Metrics                    `json:"metrics"`
}

type AccountStateSummary struct {
	State             string
	LastReason        string
	DisableReason     string
	LastAttempt       *AttemptEvidence
	LastObservedAt    time.Time
	CooldownUntil     time.Time
	ProbeBackoffUntil time.Time
	Proxy             proxy.Binding
	AutoBind          proxy.AutoBindAccountState
	Metrics           Metrics
}

// ProbeAccountState is the small per-account projection needed by the
// automatic probe scheduler. It deliberately excludes evidence, metrics,
// proxy bindings, and event history.
type ProbeAccountState struct {
	AuthID            string
	State             string
	FirstSeenAt       time.Time
	CooldownUntil     time.Time
	ProbeBackoffUntil time.Time
}

// AccountReconcileCandidate is the persisted metadata required to decide
// whether a full auth read is needed during quality reconciliation.
type AccountReconcileCandidate struct {
	AuthID                string
	State                 string
	HasAuthDisabledIntent bool
}

type Event struct {
	Time       time.Time      `json:"time"`
	AuthID     string         `json:"auth_id,omitempty"`
	Class      string         `json:"class,omitempty"`
	Action     string         `json:"action,omitempty"`
	Result     string         `json:"result,omitempty"`
	Detail     string         `json:"detail,omitempty"`
	DetailKey  string         `json:"detail_key,omitempty"`
	DetailArgs map[string]any `json:"detail_args,omitempty"`
}

type State struct {
	SchemaVersion      int                            `json:"schema_version"`
	UpdatedAt          time.Time                      `json:"updated_at"`
	Policy             config.Policy                  `json:"policy"`
	PolicyYAMLRevision string                         `json:"policy_yaml_revision,omitempty"`
	Accounts           map[string]*AccountState       `json:"accounts"`
	Events             []Event                        `json:"events"`
	ResinLeaseDeletes  []ResinLeaseDelete             `json:"resin_lease_deletes,omitempty"`
	Proxy              proxy.ProxyState               `json:"proxy,omitempty"`
	ResinSnapshots     map[string]proxy.ResinSnapshot `json:"resin_snapshots,omitempty"`
}

type Store struct {
	mu    sync.RWMutex
	path  string
	state State

	flushMu               sync.Mutex
	flushTimer            *time.Timer
	persistMu             sync.Mutex
	dirty                 bool
	dirtySeq              uint64
	overviewSeq           uint64
	proxyCounts           map[string]int
	proxyBySource         map[string]map[string]struct{}
	proxyPending          map[string]string
	proxySourcesEpoch     uint64
	proxyChangeIndex      map[string]map[string]int
	proxyLeaseAccounts    map[string]int
	proxyPendingCounts    map[string]int
	proxyPendingReserved  map[string]string
	proxyOperationLeases  map[string]string
	proxyTerminalOps      map[string]struct{}
	proxyCleanupIndex     map[string]int
	proxyLastGC           time.Time
	proxyRecoveredChanges []string
}

func (s *Store) markDirtyLocked() {
	s.dirty = true
	s.dirtySeq++
}

func (s *Store) markOverviewChangedLocked() {
	s.overviewSeq++
}

const stateFlushInterval = time.Second

func NewStore(path string) *Store {
	if path == "" {
		path = config.DefaultStatePath()
	}
	return &Store{path: path}
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

func (s *Store) PolicySnapshot() config.Policy {
	if s == nil {
		return config.Policy{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	policy := s.state.Policy
	if s.state.Policy.ModelPrefixes != nil {
		policy.ModelPrefixes = append([]string(nil), s.state.Policy.ModelPrefixes...)
	}
	return policy
}

func (s *Store) UpdatedAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.UpdatedAt
}

// OverviewRevision advances only when a change can affect dashboard aggregate
// counts. Observation timestamps and proxy-only mutations do not invalidate
// the small state-count cache used by the overview endpoint.
func (s *Store) OverviewRevision() uint64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.overviewSeq
}

// SetPath moves the live state to a new file without losing concurrent updates.
// It refuses to overwrite an existing file unless the caller explicitly restores
// a previously active path after a failed pointer update.
func (s *Store) SetPath(path string) error {
	return s.setPath(path, false)
}

func (s *Store) setPath(path string, overwrite bool) error {
	if s == nil {
		return errors.New("state store is nil")
	}
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return fmt.Errorf("%w: state path is empty", os.ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if path == s.path {
		return nil
	}
	if info, errStat := os.Stat(path); errStat == nil {
		if info.IsDir() {
			return fmt.Errorf("%w: state path is a directory", os.ErrInvalid)
		}
		if !overwrite {
			return fmt.Errorf("%w: state file already exists: %s", os.ErrExist, path)
		}
	} else if !errors.Is(errStat, os.ErrNotExist) {
		return fmt.Errorf("inspect state file: %w", errStat)
	}
	oldPath := s.path
	oldUpdatedAt := s.state.UpdatedAt
	if !overwrite {
		if _, err := os.Stat(path + ".proxy-journal"); err == nil {
			return fmt.Errorf("%w: target proxy journal already exists", os.ErrExist)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if s.dirty {
		if err := s.saveLocked(); err != nil {
			return err
		}
	}
	s.path = path
	s.state.UpdatedAt = time.Now().UTC()
	s.markDirtyLocked()
	if errSave := s.saveLocked(); errSave != nil {
		s.path = oldPath
		s.state.UpdatedAt = oldUpdatedAt
		return errSave
	}
	return nil
}

func configuredStatePath(value string) string {
	if path := strings.TrimSpace(value); path != "" {
		return filepath.Clean(path)
	}
	return config.DefaultStatePath()
}

func normalizeStatePath(value string) (string, error) {
	path := strings.TrimSpace(value)
	if path == "" {
		return "", fmt.Errorf("%w: state_file is required", os.ErrInvalid)
	}
	absolute, errAbsolute := filepath.Abs(path)
	if errAbsolute != nil {
		return "", fmt.Errorf("%w: invalid state_file: %v", os.ErrInvalid, errAbsolute)
	}
	absolute = filepath.Clean(absolute)
	if info, errStat := os.Stat(absolute); errStat == nil && info.IsDir() {
		return "", fmt.Errorf("%w: state_file must point to a file", os.ErrInvalid)
	} else if errStat != nil && !errors.Is(errStat, os.ErrNotExist) {
		return "", fmt.Errorf("inspect state_file: %w", errStat)
	}
	return absolute, nil
}

func statePathPointerPath(bootstrapPath string) string {
	return filepath.Clean(bootstrapPath) + ".location"
}

func readStatePathPointer(bootstrapPath string) (string, error) {
	pointerPath := statePathPointerPath(bootstrapPath)
	raw, errRead := os.ReadFile(pointerPath)
	if errRead != nil {
		if errors.Is(errRead, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read state path pointer: %w", errRead)
	}
	return strings.TrimSpace(string(raw)), nil
}

func writeStatePathPointer(bootstrapPath, activePath string) error {
	pointerPath := statePathPointerPath(bootstrapPath)
	if filepath.Clean(bootstrapPath) == filepath.Clean(activePath) {
		if errRemove := os.Remove(pointerPath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			return fmt.Errorf("remove state path pointer: %w", errRemove)
		}
		return nil
	}
	dir := filepath.Dir(pointerPath)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create state path pointer directory: %w", errMkdir)
	}
	temp, errTemp := os.CreateTemp(dir, ".state-location-*.tmp")
	if errTemp != nil {
		return fmt.Errorf("create state path pointer temp file: %w", errTemp)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, errWrite := temp.WriteString(filepath.Clean(activePath) + "\n"); errWrite != nil {
		_ = temp.Close()
		return fmt.Errorf("write state path pointer: %w", errWrite)
	}
	if errSync := temp.Sync(); errSync != nil {
		_ = temp.Close()
		return fmt.Errorf("sync state path pointer: %w", errSync)
	}
	if errClose := temp.Close(); errClose != nil {
		return fmt.Errorf("close state path pointer: %w", errClose)
	}
	if errReplace := replaceStateFile(tempName, pointerPath); errReplace != nil {
		return fmt.Errorf("replace state path pointer: %w", errReplace)
	}
	return nil
}

func (s *Store) Load(defaultPolicy config.Policy, revision string) error {
	if s == nil {
		return errors.New("state store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, errRead := os.ReadFile(s.path)
	if errRead != nil {
		if !errors.Is(errRead, os.ErrNotExist) {
			return fmt.Errorf("read state file: %w", errRead)
		}
		if journal, err := os.Stat(s.path + ".proxy-journal"); err == nil && journal.Size() > 0 {
			return errors.New("state checkpoint is missing while a proxy journal exists; restore the matching checkpoint")
		}
		s.state = State{
			SchemaVersion:      stateSchemaVersion,
			UpdatedAt:          time.Now().UTC(),
			Policy:             defaultPolicy,
			PolicyYAMLRevision: revision,
			Accounts:           make(map[string]*AccountState),
			Events:             []Event{},
		}
		s.rebuildProxyIndexesLocked()
		if errJournal := s.replayProxyJournalLocked(); errJournal != nil {
			return errJournal
		}
		s.rebuildProxyIndexesLocked()
		s.markDirtyLocked()
		return s.saveLocked()
	}
	var state State
	if errUnmarshal := decodeStrictJSON(raw, &state); errUnmarshal != nil {
		return fmt.Errorf("decode state file: %w", errUnmarshal)
	}
	if state.SchemaVersion != stateSchemaVersion {
		return fmt.Errorf("unsupported state schema version %d; create a fresh schema v%d state file", state.SchemaVersion, stateSchemaVersion)
	}
	if errValidate := validateLoadedState(&state); errValidate != nil {
		return errValidate
	}

	if state.Accounts == nil {
		state.Accounts = make(map[string]*AccountState)
	}
	if len(state.Policy.ModelPrefixes) == 0 && state.Policy.EventsLimit == 0 {
		state.Policy = defaultPolicy
		state.PolicyYAMLRevision = revision
	} else {
		// States written before task history existed must adopt the new default
		// instead of inheriting bool's zero value and silently disabling history.
		if !rawPolicyHasTaskHistory(raw) {
			state.Policy.TaskHistory = config.DefaultTaskHistoryConfig()
		}
		if normalized, errNormalize := config.NormalizePolicy(state.Policy); errNormalize != nil {
			return fmt.Errorf("state policy invalid: %w", errNormalize)
		} else {
			state.Policy = normalized
		}
	}
	if state.PolicyYAMLRevision == "" {
		state.PolicyYAMLRevision = revision
	}
	for authID, account := range state.Accounts {
		if account == nil {
			delete(state.Accounts, authID)
			continue
		}
		if account.AuthID == "" {
			account.AuthID = authID
		}
		if account.State == "" {
			account.State = StateUnknown
		}
		if account.FirstSeenAt.IsZero() {
			account.FirstSeenAt = state.UpdatedAt
			if account.FirstSeenAt.IsZero() {
				account.FirstSeenAt = time.Now().UTC()
			}
		}
	}
	s.state = state
	s.rebuildProxyIndexesLocked()
	s.markOverviewChangedLocked()
	if errJournal := s.replayProxyJournalLocked(); errJournal != nil {
		return errJournal
	}
	s.rebuildProxyIndexesLocked()
	for _, change := range s.state.Proxy.Changes {
		if change.Status == "pending" || change.Status == "running" {
			s.proxyRecoveredChanges = append(s.proxyRecoveredChanges, change.ID)
		}
	}
	return nil
}

func rawPolicyHasTaskHistory(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, errToken := decoder.Token()
	if errToken != nil || token != json.Delim('{') {
		return false
	}
	for decoder.More() {
		keyToken, errKey := decoder.Token()
		if errKey != nil {
			return false
		}
		key, _ := keyToken.(string)
		if key != "policy" {
			// Walk tokens without materializing large account, event, or proxy
			// values. Load already decodes the state once; this probe must not
			// allocate a second copy of those fields just to inspect policy.
			if skipJSONValue(decoder) != nil {
				return false
			}
			continue
		}
		policyToken, errPolicy := decoder.Token()
		if errPolicy != nil || policyToken != json.Delim('{') {
			return false
		}
		for decoder.More() {
			policyKeyToken, errPolicyKey := decoder.Token()
			if errPolicyKey != nil {
				return false
			}
			policyKey, _ := policyKeyToken.(string)
			if policyKey == "task_history" {
				return true
			}
			if skipJSONValue(decoder) != nil {
				return false
			}
		}
		return false
	}
	return false
}

// skipJSONValue consumes exactly one JSON value using token boundaries. Strings
// are transient while scanning; unlike json.RawMessage, large values are not
// retained as a second in-memory copy.
func skipJSONValue(decoder *json.Decoder) error {
	token, errToken := decoder.Token()
	if errToken != nil {
		return errToken
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delim {
	case '{':
		for decoder.More() {
			if _, errKey := decoder.Token(); errKey != nil {
				return errKey
			}
			if errValue := skipJSONValue(decoder); errValue != nil {
				return errValue
			}
		}
	case '[':
		for decoder.More() {
			if errValue := skipJSONValue(decoder); errValue != nil {
				return errValue
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	_, errClose := decoder.Token()
	return errClose
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func validateLoadedState(state *State) error {
	if state == nil {
		return errors.New("state is nil")
	}
	for id, snapshot := range state.ResinSnapshots {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(snapshot.AdminURL) == "" || strings.TrimSpace(snapshot.PlatformID) == "" || strings.TrimSpace(snapshot.AdminToken) == "" {
			return fmt.Errorf("resin snapshot %q is incomplete; delete the old state file and recreate it", id)
		}
	}
	for _, job := range state.ResinLeaseDeletes {
		if strings.TrimSpace(job.Account) == "" || strings.TrimSpace(job.AdminURL) == "" || strings.TrimSpace(job.PlatformID) == "" || strings.TrimSpace(job.SnapshotID) == "" {
			return fmt.Errorf("resin cleanup job for account %q is incomplete; delete the old state file and recreate it", job.Account)
		}
		if _, ok := state.ResinSnapshots[job.SnapshotID]; !ok {
			return fmt.Errorf("resin cleanup job references missing snapshot %q; delete the old state file and recreate it", job.SnapshotID)
		}
	}
	for authID, account := range state.Accounts {
		if account == nil || account.Proxy.Lease == nil {
			continue
		}
		lease := account.Proxy.Lease
		if lease.Provider == "resin" && strings.TrimSpace(lease.SnapshotID) == "" {
			return fmt.Errorf("account %q has a Resin lease without a snapshot; delete the old state file and recreate it", authID)
		}
		if strings.TrimSpace(lease.SnapshotID) != "" {
			if _, ok := state.ResinSnapshots[lease.SnapshotID]; !ok {
				return fmt.Errorf("account %q references missing Resin snapshot %q; delete the old state file and recreate it", authID, lease.SnapshotID)
			}
		}
	}
	return nil
}

func (s *Store) ResinLeaseDeletesSnapshot() []ResinLeaseDelete {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]ResinLeaseDelete(nil), s.state.ResinLeaseDeletes...)
}

type ResinCleanupTotals struct {
	Total         int
	Pending       int
	Failed        int
	Paused        int
	UpdatedAt     time.Time
	LastError     string
	LastErrorCode string
}

func (s *Store) ResinCleanupTotals() ResinCleanupTotals {
	if s == nil {
		return ResinCleanupTotals{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	totals := ResinCleanupTotals{Total: len(s.state.ResinLeaseDeletes)}
	for _, job := range s.state.ResinLeaseDeletes {
		if job.Paused {
			totals.Paused++
		} else if job.LastError != "" || job.Status == "permanent_failure" || job.Status == "credential_blocked" {
			totals.Failed++
		} else {
			totals.Pending++
		}
		if job.LastError != "" && totals.LastError == "" {
			totals.LastError = job.LastError
			totals.LastErrorCode = job.LastErrorCode
		}
		if job.CreatedAt.After(totals.UpdatedAt) {
			totals.UpdatedAt = job.CreatedAt
		}
	}
	return totals
}

func (s *Store) Snapshot() State {
	if s == nil {
		return State{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneState(s.state)
}

func (s *Store) AccountSnapshot(authID string) (AccountState, bool) {
	if s == nil {
		return AccountState{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	account, ok := s.state.Accounts[authID]
	if !ok || account == nil {
		return AccountState{}, false
	}
	return *cloneAccountState(account), true
}

// AccountStateValues returns only the routing-facing state string for each
// persisted account. Missing entries intentionally fall back to unknown in the
// routing index instead of being materialized as default AccountState values.
func (s *Store) AccountStateValues() map[string]string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make(map[string]string, len(s.state.Accounts))
	for authID, account := range s.state.Accounts {
		if account != nil {
			values[authID] = account.State
		}
	}
	return values
}

// FillRoutingStates copies routing state strings for a bounded ID chunk into
// caller-owned storage. It avoids materializing a map proportional to the
// entire persisted account set on every list request.
func (s *Store) FillRoutingStates(authIDs []string, dst []string) {
	if len(dst) < len(authIDs) {
		return
	}
	if s == nil {
		clear(dst[:len(authIDs)])
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for index, authID := range authIDs {
		account := s.state.Accounts[authID]
		if account == nil {
			dst[index] = ""
		} else {
			dst[index] = account.State
		}
	}
}

// AccountFirstSeenValues lets the catalog preserve probe aging for existing
// account state records.
func (s *Store) AccountFirstSeenValues() map[string]time.Time {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make(map[string]time.Time, len(s.state.Accounts))
	for authID, account := range s.state.Accounts {
		if account != nil && !account.FirstSeenAt.IsZero() {
			values[authID] = account.FirstSeenAt
		}
	}
	return values
}

// ProbeStatesForIDs avoids cloning the complete AccountState for every account
// during the periodic probe scan.
func (s *Store) ProbeStatesForIDs(authIDs []string) []ProbeAccountState {
	states := make([]ProbeAccountState, len(authIDs))
	if s == nil {
		return states
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for index, authID := range authIDs {
		account := s.state.Accounts[authID]
		if account == nil {
			continue
		}
		states[index] = ProbeAccountState{
			AuthID:            authID,
			State:             account.State,
			FirstSeenAt:       account.FirstSeenAt,
			CooldownUntil:     account.CooldownUntil,
			ProbeBackoffUntil: account.ProbeBackoffUntil,
		}
	}
	return states
}

// AccountReconcileCandidates copies only fields used for candidate selection.
// The full state is fetched lazily for the small set that actually needs repair.
func (s *Store) AccountReconcileCandidates() []AccountReconcileCandidate {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	candidates := make([]AccountReconcileCandidate, 0, len(s.state.Accounts))
	for authID, account := range s.state.Accounts {
		if account == nil {
			continue
		}
		candidates = append(candidates, AccountReconcileCandidate{
			AuthID:                authID,
			State:                 account.State,
			HasAuthDisabledIntent: account.AuthDisabledIntent != nil,
		})
	}
	return candidates
}

func (s *Store) AccountStateSummariesForIDs(authIDs []string) []AccountStateSummary {
	summaries := make([]AccountStateSummary, len(authIDs))
	if s == nil {
		return summaries
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for index, authID := range authIDs {
		account := s.state.Accounts[authID]
		if account == nil {
			continue
		}
		summary := AccountStateSummary{
			State:             account.State,
			LastReason:        account.LastReason,
			DisableReason:     account.DisableReason,
			LastObservedAt:    account.LastObservedAt,
			CooldownUntil:     account.CooldownUntil,
			ProbeBackoffUntil: account.ProbeBackoffUntil,
			Proxy:             proxy.CloneBinding(account.Proxy),
			AutoBind:          account.AutoBind,
			Metrics:           account.Metrics,
		}
		if account.LastAttempt != nil {
			attempt := *account.LastAttempt
			summary.LastAttempt = &attempt
		}
		summaries[index] = summary
	}
	return summaries
}

func (s *Store) AccountStatesForIDs(authIDs []string) []AccountState {
	states := make([]AccountState, len(authIDs))
	if s == nil {
		return states
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for index, authID := range authIDs {
		account := s.state.Accounts[authID]
		if account == nil {
			continue
		}
		states[index] = *cloneAccountState(account)
	}
	return states
}

func (s *Store) StateCounts() map[string]int {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := make(map[string]int, 8)
	for _, account := range s.state.Accounts {
		if account != nil {
			counts[account.State]++
		}
	}
	return counts
}

func (s *Store) EventsSnapshot() []Event {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Event(nil), s.state.Events...)
}

func (s *Store) RecentEventCount(since time.Time) int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, event := range s.state.Events {
		if event.Time.After(since) && event.Class != "" {
			count++
		}
	}
	return count
}

func (s *Store) UpdateAccount(authID string, fn func(*AccountState) (*Event, error)) error {
	return s.updateAccountEvents(authID, func(account *AccountState) ([]Event, error) {
		event, err := fn(account)
		if event == nil {
			return nil, err
		}
		return []Event{*event}, err
	}, true)
}

// RecordObservation applies an observation-only mutation in memory and
// coalesces the durable write. Use UpdateAccount for guard, probe, admin, and
// proxy mutations that must be durable before the caller continues.
func (s *Store) RecordObservation(authID string, fn func(*AccountState) (*Event, error)) error {
	return s.updateAccountEvents(authID, func(account *AccountState) ([]Event, error) {
		event, err := fn(account)
		if event == nil {
			return nil, err
		}
		return []Event{*event}, err
	}, false)
}

// RecordObservationEvents coalesces one observation update and all of its
// dependent events into a single scheduled checkpoint write.
func (s *Store) RecordObservationEvents(authID string, fn func(*AccountState) ([]Event, error)) error {
	return s.updateAccountEvents(authID, fn, false)
}

func (s *Store) updateAccountEvents(authID string, fn func(*AccountState) ([]Event, error), persist bool) error {
	if s == nil {
		return errors.New("state store is nil")
	}
	s.mu.Lock()
	previousUpdatedAt := s.state.UpdatedAt
	previousEvents := s.state.Events
	previousAccount, existed := s.state.Accounts[authID]
	previousState := ""
	previousSource := ""
	previousLease := ""
	if existed && previousAccount != nil {
		previousState = previousAccount.State
		previousSource, _ = proxyBindingSource(*previousAccount)
		previousLease = proxyLeaseIdentity(*previousAccount)
	}
	if s.state.Accounts == nil {
		s.state.Accounts = make(map[string]*AccountState)
	}
	account := s.state.Accounts[authID]
	if account == nil {
		account = &AccountState{AuthID: authID, State: StateUnknown, FirstSeenAt: time.Now().UTC()}
		s.state.Accounts[authID] = account
	}
	var rollback *AccountState
	var target *AccountState
	if persist {
		rollback = cloneAccountState(account)
		target = account
	} else {
		working := *account
		target = &working
	}
	events, errFn := fn(target)
	if errFn != nil {
		if persist {
			if existed && rollback != nil {
				s.state.Accounts[authID] = rollback
			} else {
				delete(s.state.Accounts, authID)
			}
		} else if existed && previousAccount != nil {
			s.state.Accounts[authID] = previousAccount
		} else {
			delete(s.state.Accounts, authID)
		}
		s.state.Events = previousEvents
		s.state.UpdatedAt = previousUpdatedAt
		s.mu.Unlock()
		return errFn
	}
	if !persist {
		*account = *target
	}
	if len(events) > 0 {
		for _, event := range events {
			if event.AuthID == "" {
				event.AuthID = authID
			}
			if event.Time.IsZero() {
				event.Time = time.Now().UTC()
			}
			appendEvent(&s.state, event)
		}
	}
	s.state.UpdatedAt = time.Now().UTC()
	currentSource, _ := proxyBindingSource(*account)
	currentLease := proxyLeaseIdentity(*account)
	if !existed || previousSource != currentSource || previousLease != currentLease {
		s.indexProxyAccountIdentitiesLocked(authID, previousSource, currentSource, previousLease, currentLease)
	}
	if !existed || previousState != account.State || eventsContainClass(events) {
		s.markOverviewChangedLocked()
	}
	if persist {
		s.markDirtyLocked()
		if errSave := s.saveLocked(); errSave != nil {
			s.scheduleFlush()
			// A concurrent observation may have changed this account while the
			// checkpoint was being written. Keep the in-memory mutation and let
			// the next flush retry rather than rolling back newer state.
			s.mu.Unlock()
			return errSave
		}
	} else {
		s.markDirtyLocked()
	}
	s.mu.Unlock()
	if !persist {
		s.scheduleFlush()
	}
	return nil
}

func eventsContainClass(events []Event) bool {
	for _, event := range events {
		if event.Class != "" {
			return true
		}
	}
	return false
}

func (s *Store) scheduleFlush() {
	if s == nil {
		return
	}
	s.flushMu.Lock()
	if s.flushTimer != nil {
		s.flushMu.Unlock()
		return
	}
	s.flushTimer = time.AfterFunc(stateFlushInterval, func() {
		s.flushMu.Lock()
		s.flushTimer = nil
		s.flushMu.Unlock()
		_ = s.Flush()
	})
	s.flushMu.Unlock()
}

func (s *Store) AppendEvent(event Event) error {
	if s == nil {
		return errors.New("state store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	appendEvent(&s.state, event)
	s.state.UpdatedAt = time.Now().UTC()
	if event.Class != "" {
		s.markOverviewChangedLocked()
	}
	s.markDirtyLocked()
	if errSave := s.saveLocked(); errSave != nil {
		s.scheduleFlush()
		// Leave the event in memory; dirty remains set and a later flush retries.
		return errSave
	}
	return nil
}

func (s *Store) Update(fn func(*State) error) error {
	if s == nil {
		return errors.New("state store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := cloneState(s.state)
	if errFn := fn(&s.state); errFn != nil {
		s.state = previous
		return errFn
	}
	s.state.UpdatedAt = time.Now().UTC()
	s.markOverviewChangedLocked()
	s.markDirtyLocked()
	s.rebuildProxyIndexesLocked()
	if errSave := s.saveLocked(); errSave != nil {
		s.scheduleFlush()
		return errSave
	}
	return nil
}

func (s *Store) UpdateIfChanged(fn func(*State) (bool, error)) error {
	if s == nil {
		return errors.New("state store is nil")
	}
	s.mu.Lock()
	changed, errFn := fn(&s.state)
	if errFn != nil {
		s.mu.Unlock()
		return errFn
	}
	if !changed {
		s.mu.Unlock()
		return nil
	}
	s.state.UpdatedAt = time.Now().UTC()
	s.markOverviewChangedLocked()
	s.markDirtyLocked()
	s.rebuildProxyIndexesLocked()
	s.mu.Unlock()
	s.scheduleFlush()
	return nil
}

func (s *Store) Flush() error {
	if s == nil {
		return errors.New("state store is nil")
	}
	s.flushMu.Lock()
	if s.flushTimer != nil {
		s.flushTimer.Stop()
		s.flushTimer = nil
	}
	s.flushMu.Unlock()
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	if err := s.prepareStateLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	snapshot := cloneState(s.state)
	seq := s.dirtySeq
	path := s.path
	// Lock order is state.mu -> persistMu. The writer releases persistMu
	// before reacquiring state.mu, so a concurrent snapshotter cannot deadlock
	// while serializing a newer checkpoint behind this one.
	s.persistMu.Lock()
	s.mu.Unlock()
	errWrite := writeStateSnapshot(path, &snapshot)
	s.persistMu.Unlock()
	if errWrite != nil {
		s.scheduleFlush()
		return errWrite
	}
	s.mu.Lock()
	retry := s.dirty
	if s.dirtySeq == seq {
		s.dirty = false
		s.checkpointProxyJournalLocked()
		retry = false
	}
	s.mu.Unlock()
	if retry {
		s.scheduleFlush()
	}
	return nil
}

// saveLocked is called by callers that already hold s.mu. It deliberately
// releases s.mu while encoding, syncing, and replacing the file; callers must
// therefore treat the returned error as a persistence failure and leave the
// in-memory mutation authoritative for the next flush.
func (s *Store) saveLocked() error {
	if err := s.prepareStateLocked(); err != nil {
		return err
	}
	snapshot := cloneState(s.state)
	seq := s.dirtySeq
	path := s.path
	s.persistMu.Lock()
	s.mu.Unlock()
	errWrite := writeStateSnapshot(path, &snapshot)
	s.persistMu.Unlock()
	s.mu.Lock()
	if errWrite == nil && s.dirtySeq == seq {
		s.dirty = false
		s.checkpointProxyJournalLocked()
	}
	return errWrite
}

func (s *Store) prepareStateLocked() error {
	if s.path == "" {
		return errors.New("state path is empty")
	}
	if s.state.SchemaVersion != stateSchemaVersion {
		return fmt.Errorf("state schema version %d is not writable; expected %d", s.state.SchemaVersion, stateSchemaVersion)
	}
	if s.state.Accounts == nil {
		s.state.Accounts = make(map[string]*AccountState)
	}
	if s.state.Events == nil {
		s.state.Events = []Event{}
	}
	trimEvents(&s.state, s.state.Policy.EventsLimit)
	s.maybeGCProxyHistoryLocked()
	return nil
}

func writeStateSnapshot(path string, snapshot *State) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	_ = os.Chmod(dir, 0o700)
	temp, errTemp := os.CreateTemp(dir, ".state-*.tmp")
	if errTemp != nil {
		return fmt.Errorf("create state temp file: %w", errTemp)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if errChmod := temp.Chmod(0o600); errChmod != nil {
		_ = temp.Close()
		return fmt.Errorf("set state file permissions: %w", errChmod)
	}
	if errEncode := json.NewEncoder(temp).Encode(snapshot); errEncode != nil {
		_ = temp.Close()
		return fmt.Errorf("encode state: %w", errEncode)
	}
	if errSync := temp.Sync(); errSync != nil {
		_ = temp.Close()
		return fmt.Errorf("sync state temp file: %w", errSync)
	}
	if errClose := temp.Close(); errClose != nil {
		return fmt.Errorf("close state temp file: %w", errClose)
	}
	if errReplace := replaceStateFile(tempName, path); errReplace != nil {
		return fmt.Errorf("replace state file: %w", errReplace)
	}
	return nil
}

func cloneState(state State) State {
	clone := state
	if state.Policy.ModelPrefixes != nil {
		clone.Policy.ModelPrefixes = append([]string{}, state.Policy.ModelPrefixes...)
	}
	clone.Accounts = make(map[string]*AccountState, len(state.Accounts))
	for key, account := range state.Accounts {
		if account == nil {
			continue
		}
		clone.Accounts[key] = cloneAccountState(account)
	}
	clone.Proxy = proxy.CloneProxyState(state.Proxy)
	if state.ResinSnapshots != nil {
		clone.ResinSnapshots = make(map[string]proxy.ResinSnapshot, len(state.ResinSnapshots))
		for id, snapshot := range state.ResinSnapshots {
			clone.ResinSnapshots[id] = snapshot
		}
	}
	if state.Events != nil {
		clone.Events = append([]Event{}, state.Events...)
	}
	if state.ResinLeaseDeletes != nil {
		clone.ResinLeaseDeletes = append([]ResinLeaseDelete{}, state.ResinLeaseDeletes...)
	}

	return clone
}

func cloneAccountState(account *AccountState) *AccountState {
	if account == nil {
		return nil
	}
	copied := *account
	if account.LastAttempt != nil {
		attempt := *account.LastAttempt
		copied.LastAttempt = &attempt
	}
	if account.LastEvidence != nil {
		evidence := *account.LastEvidence
		copied.LastEvidence = &evidence
	}
	if account.AuthDisabledIntent != nil {
		intent := *account.AuthDisabledIntent
		copied.AuthDisabledIntent = &intent
	}
	copied.Proxy = proxy.CloneBinding(account.Proxy)
	return &copied
}

func trimEvents(state *State, limit int) {
	if state == nil {
		return
	}
	if limit <= 0 {
		limit = config.DefaultEventsLimit
	}
	if len(state.Events) <= limit {
		return
	}
	state.Events = append([]Event(nil), state.Events[len(state.Events)-limit:]...)
}

func appendEvent(state *State, ev Event) {
	if state == nil {
		return
	}
	ev.Time = ev.Time.UTC()
	state.Events = append(state.Events, ev)
	trimEvents(state, state.Policy.EventsLimit)
}
