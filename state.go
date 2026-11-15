package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
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

func qualityEvidenceFromVerdict(verdict QualityVerdict, source string, at time.Time) *QualityEvidence {
	sig := verdict.Signals
	return &QualityEvidence{
		At: at, Source: source, Action: verdict.Action, Class: verdict.Class, Reason: verdict.Reason,
		Protocol: sig.Protocol, Streaming: sig.Streaming, Terminal: sig.Terminal, HoldExpired: sig.HoldExpired,
		HasThinking: sig.HasThinking, HasReasoningDelta: sig.HasReasoningDelta, ReasoningRequired: sig.ReasoningRequired, ReasoningStarted: sig.ReasoningStarted,
		SemanticOutput: sig.SemanticOutput, UsageReported: sig.UsageReported,
		VisibleRunes: sig.VisibleRunes, VisibleTokens: sig.VisibleTokens, OutputTokens: sig.OutputTokens,
		ReasoningTokens: sig.ReasoningTokens, EncryptedBytes: sig.EncryptedBytes, EncryptedFloor: sig.EncryptedFloor,
		FirstTokenMS: sig.FirstTokenMS, DurationMS: sig.DurationMS, GenerationMS: sig.GenerationMS,
		VisibleFlushMS: sig.VisibleFlushMS, VisibleSpanMS: sig.VisibleSpanMS, VisibleChunks: sig.VisibleChunks, TPS: sig.TPS,
		ChunkCount: sig.ChunkCount, ByteCount: sig.ByteCount, EventCount: sig.EventCount,
		ParsedEvents: sig.ParsedEvents, UnknownEvents: sig.UnknownEvents, ParseFailures: sig.ParseFailures,
	}
}

type AttemptEvidence struct {
	At     time.Time `json:"at,omitzero"`
	Source string    `json:"source,omitempty"`
	Class  string    `json:"class,omitempty"`
	Reason string    `json:"reason,omitempty"`
}
type ResinState struct {
	Managed    bool   `json:"managed,omitempty"`
	Account    string `json:"account,omitempty"`
	Generation int64  `json:"generation,omitempty"`
}

type ResinLeaseDelete struct {
	AuthID        string    `json:"auth_id,omitempty"`
	AdminURL      string    `json:"admin_url,omitempty"`
	AdminTokenRef string    `json:"admin_token_ref,omitempty"`
	AdminToken    string    `json:"admin_token,omitempty"`
	PlatformID    string    `json:"platform_id,omitempty"`
	Account       string    `json:"account"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
	Attempts      int       `json:"attempts,omitempty"`
	NextAttemptAt time.Time `json:"next_attempt_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
}

type AccountState struct {
	AuthID                       string           `json:"auth_id"`
	State                        string           `json:"state"`
	LastReason                   string           `json:"last_reason,omitempty"`
	LastAttempt                  *AttemptEvidence `json:"last_attempt,omitempty"`
	LastEvidence                 *QualityEvidence `json:"last_evidence,omitempty"`
	FirstSeenAt                  time.Time        `json:"first_seen_at,omitzero"`
	LastObservedAt               time.Time        `json:"last_observed_at,omitzero"`
	LastProbeAt                  time.Time        `json:"last_probe_at,omitzero"`
	CooldownUntil                time.Time        `json:"cooldown_until,omitzero"`
	QualityStrikeMissingThinking bool             `json:"quality_strike_missing_thinking,omitempty"`
	QualityFailureAt             time.Time        `json:"quality_failure_at,omitzero"`
	HealthyStreak                int              `json:"healthy_streak,omitempty"`
	HealthyStreakStartedAt       time.Time        `json:"healthy_streak_started_at,omitzero"`
	SoftStrikes                  int              `json:"soft_strikes,omitempty"`
	ErrorStrikes                 int              `json:"error_strikes,omitempty"`
	ThinkingStrikes              int              `json:"thinking_strikes,omitempty"`
	ProbeBackoffUntil            time.Time        `json:"probe_backoff_until,omitzero"`
	DisableKind                  string           `json:"disable_kind,omitempty"`
	DisableReason                string           `json:"disable_reason,omitempty"`
	PreviousDisabled             bool             `json:"previous_disabled,omitempty"`
	AuthDisabledIntent           *bool            `json:"auth_disabled_intent,omitempty"`
	CatalogMissingSince          time.Time        `json:"catalog_missing_since,omitzero"`
	Resin                        ResinState       `json:"resin"`
	Metrics                      Metrics          `json:"metrics"`
}
type AccountStateSummary struct {
	State             string
	LastReason        string
	DisableReason     string
	LastAttempt       *AttemptEvidence
	LastObservedAt    time.Time
	CooldownUntil     time.Time
	ProbeBackoffUntil time.Time
	Resin             ResinState
	Metrics           Metrics
}

type Event struct {
	Time   time.Time `json:"time"`
	AuthID string    `json:"auth_id,omitempty"`
	Class  string    `json:"class,omitempty"`
	Action string    `json:"action,omitempty"`
	Result string    `json:"result,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

type State struct {
	SchemaVersion      int                      `json:"schema_version"`
	UpdatedAt          time.Time                `json:"updated_at"`
	Policy             Policy                   `json:"policy"`
	PolicyYAMLRevision string                   `json:"policy_yaml_revision,omitempty"`
	Resin              ResinSettings            `json:"resin,omitempty"`
	Accounts           map[string]*AccountState `json:"accounts"`
	Events             []Event                  `json:"events"`
	ResinLeaseDeletes  []ResinLeaseDelete       `json:"resin_lease_deletes,omitempty"`
	ResinAdminTokens   map[string]string        `json:"resin_admin_tokens,omitempty"`
}

type StateStore struct {
	mu    sync.RWMutex
	path  string
	state State

	flushMu    sync.Mutex
	flushTimer *time.Timer
	dirty      bool
}

const stateFlushInterval = time.Second

func NewStateStore(path string) *StateStore {
	if path == "" {
		path = defaultStatePath()
	}
	return &StateStore{path: path}
}

func (s *StateStore) Path() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

func (s *StateStore) PolicySnapshot() Policy {
	if s == nil {
		return Policy{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	policy := s.state.Policy
	if s.state.Policy.ModelPrefixes != nil {
		policy.ModelPrefixes = append([]string(nil), s.state.Policy.ModelPrefixes...)
	}
	return policy
}

func (s *StateStore) ResinAccounts() map[string]struct{} {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	accounts := make(map[string]struct{})
	for _, account := range s.state.Accounts {
		if account != nil && account.Resin.Account != "" {
			accounts[account.Resin.Account] = struct{}{}
		}
	}
	return accounts
}

func (s *StateStore) UpdatedAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.UpdatedAt
}

// SetPath moves the live state to a new file without losing concurrent updates.
// It refuses to overwrite an existing file unless the caller explicitly restores
// a previously active path after a failed pointer update.
func (s *StateStore) SetPath(path string) error {
	return s.setPath(path, false)
}

func (s *StateStore) setPath(path string, overwrite bool) error {
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
	s.path = path
	s.state.UpdatedAt = time.Now().UTC()
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
	return defaultStatePath()
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
	if errRename := os.Rename(tempName, pointerPath); errRename != nil {
		if errRemove := os.Remove(pointerPath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			return fmt.Errorf("replace state path pointer: %w", errRename)
		}
		if errRetry := os.Rename(tempName, pointerPath); errRetry != nil {
			return fmt.Errorf("replace state path pointer: %w", errRetry)
		}
	}
	return nil
}

func (s *StateStore) Load(defaultPolicy Policy, revision string) error {
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
		s.state = State{
			SchemaVersion:      stateSchemaVersion,
			UpdatedAt:          time.Now().UTC(),
			Policy:             defaultPolicy,
			PolicyYAMLRevision: revision,
			Accounts:           make(map[string]*AccountState),
			Events:             []Event{},
		}
		return s.saveLocked()
	}
	var state State
	if errUnmarshal := json.Unmarshal(raw, &state); errUnmarshal != nil {
		return fmt.Errorf("decode state file: %w", errUnmarshal)
	}
	if state.SchemaVersion != stateSchemaVersion {
		return fmt.Errorf("unsupported state schema version %d", state.SchemaVersion)
	}
	if state.Accounts == nil {
		state.Accounts = make(map[string]*AccountState)
	}
	if len(state.Policy.ModelPrefixes) == 0 && state.Policy.EventsLimit == 0 {
		state.Policy = defaultPolicy
		state.PolicyYAMLRevision = revision
	} else if normalized, errNormalize := normalizePolicy(state.Policy); errNormalize != nil {
		return fmt.Errorf("state policy invalid: %w", errNormalize)
	} else {
		state.Policy = normalized
	}
	if state.PolicyYAMLRevision == "" {
		state.PolicyYAMLRevision = revision
	}
	migrateResinAdminTokens(&state)
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
	return nil
}

func (s *StateStore) ResinLeaseDeletesSnapshot() []ResinLeaseDelete {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]ResinLeaseDelete(nil), s.state.ResinLeaseDeletes...)
}

func (s *StateStore) ResinAdminToken(ref string) string {
	if s == nil || ref == "" {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.ResinAdminTokens[ref]
}

func (s *StateStore) Snapshot() State {
	if s == nil {
		return State{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneState(s.state)
}

func (s *StateStore) AccountSnapshot(authID string) (AccountState, bool) {
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

func (s *StateStore) AccountStateValuesForIDs(authIDs []string) []string {
	states := make([]string, len(authIDs))
	if s == nil {
		return states
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for index, authID := range authIDs {
		if account := s.state.Accounts[authID]; account != nil {
			states[index] = account.State
		}
	}
	return states
}

func (s *StateStore) AccountResinStatesForIDs(authIDs []string) []ResinState {
	states := make([]ResinState, len(authIDs))
	if s == nil {
		return states
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for index, authID := range authIDs {
		if account := s.state.Accounts[authID]; account != nil {
			states[index] = account.Resin
		}
	}
	return states
}

func (s *StateStore) AccountStateSummariesForIDs(authIDs []string) []AccountStateSummary {
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
			Resin:             account.Resin,
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

func (s *StateStore) AccountStatesForIDs(authIDs []string) []AccountState {
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

func (s *StateStore) AccountsSnapshot() map[string]*AccountState {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	accounts := make(map[string]*AccountState, len(s.state.Accounts))
	for authID, account := range s.state.Accounts {
		if account == nil {
			continue
		}
		accounts[authID] = cloneAccountState(account)
	}
	return accounts
}

func (s *StateStore) StateCounts() map[string]int {
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

func (s *StateStore) EventsSnapshot() []Event {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Event(nil), s.state.Events...)
}

func (s *StateStore) RecentEventCount(since time.Time) int {
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

func (s *StateStore) ResinSettings() (ResinSettings, bool) {
	if s == nil {
		return ResinSettings{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Resin, s.state.Resin.Configured
}

func (s *StateStore) CatalogMatches(accounts []Account) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.state.Accounts) != len(accounts) {
		return false
	}
	for _, account := range accounts {
		if _, exists := s.state.Accounts[account.ID]; !exists {
			return false
		}
	}
	return true
}

func (s *StateStore) UpdateAccount(authID string, fn func(*AccountState) (*Event, error)) error {
	return s.updateAccount(authID, fn, true)
}

// RecordObservation applies an observation-only mutation in memory and
// coalesces the durable write. Use UpdateAccount for guard, probe, admin, and
// Resin mutations that must be durable before the caller continues.
func (s *StateStore) RecordObservation(authID string, fn func(*AccountState) (*Event, error)) error {
	return s.updateAccount(authID, fn, false)
}

func (s *StateStore) updateAccount(authID string, fn func(*AccountState) (*Event, error), persist bool) error {
	if s == nil {
		return errors.New("state store is nil")
	}
	s.mu.Lock()
	previousUpdatedAt := s.state.UpdatedAt
	previousEvents := s.state.Events
	previousAccount, existed := s.state.Accounts[authID]
	var previousCopy *AccountState
	if previousAccount != nil {
		previousCopy = cloneAccountState(previousAccount)
	}
	if s.state.Accounts == nil {
		s.state.Accounts = make(map[string]*AccountState)
	}
	account := s.state.Accounts[authID]
	if account == nil {
		account = &AccountState{AuthID: authID, State: StateUnknown, FirstSeenAt: time.Now().UTC()}
		s.state.Accounts[authID] = account
	}
	event, errFn := fn(account)
	if errFn != nil {
		if existed && previousCopy != nil {
			s.state.Accounts[authID] = previousCopy
		} else {
			delete(s.state.Accounts, authID)
		}
		s.state.Events = previousEvents
		s.state.UpdatedAt = previousUpdatedAt
		s.mu.Unlock()
		return errFn
	}
	if event != nil {
		if event.AuthID == "" {
			event.AuthID = authID
		}
		if event.Time.IsZero() {
			event.Time = time.Now().UTC()
		}
		appendEvent(&s.state, *event)
	} else if existed && previousCopy != nil && reflect.DeepEqual(previousCopy, account) {
		s.mu.Unlock()
		return nil
	}
	s.state.UpdatedAt = time.Now().UTC()
	if persist {
		if errSave := s.saveLocked(); errSave != nil {
			if existed && previousCopy != nil {
				s.state.Accounts[authID] = previousCopy
			} else {
				delete(s.state.Accounts, authID)
			}
			s.state.Events = previousEvents
			s.state.UpdatedAt = previousUpdatedAt
			s.mu.Unlock()
			return errSave
		}
	} else {
		s.dirty = true
	}
	s.mu.Unlock()
	if !persist {
		s.scheduleFlush()
	}
	return nil
}

func (s *StateStore) scheduleFlush() {
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

func (s *StateStore) AppendEvent(event Event) error {
	if s == nil {
		return errors.New("state store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previousUpdatedAt := s.state.UpdatedAt
	previousEvents := s.state.Events
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	appendEvent(&s.state, event)
	s.state.UpdatedAt = time.Now().UTC()
	if errSave := s.saveLocked(); errSave != nil {
		s.state.Events = previousEvents
		s.state.UpdatedAt = previousUpdatedAt
		return errSave
	}
	return nil
}

func (s *StateStore) Update(fn func(*State) error) error {
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
	if errSave := s.saveLocked(); errSave != nil {
		s.state = previous
		return errSave
	}
	return nil
}

// UpdateIfChanged applies a periodic catalog or Resin sync mutation. The
// callback must report whether it changed the state. Callers validate before
// mutating; a save failure leaves the in-memory state authoritative so the next
// flush can retry instead of paying for a full clone on every invocation.
func (s *StateStore) UpdateIfChanged(fn func(*State) (bool, error)) error {
	if s == nil {
		return errors.New("state store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed, errFn := fn(&s.state)
	if errFn != nil {
		return errFn
	}
	if !changed {
		return nil
	}
	s.state.UpdatedAt = time.Now().UTC()
	s.dirty = true
	return s.saveLocked()
}

func (s *StateStore) Flush() error {
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
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	return s.saveLocked()
}

func (s *StateStore) saveLocked() error {
	if s.path == "" {
		return errors.New("state path is empty")
	}
	if s.state.SchemaVersion == 0 {
		s.state.SchemaVersion = stateSchemaVersion
	}
	if s.state.Accounts == nil {
		s.state.Accounts = make(map[string]*AccountState)
	}
	if s.state.Events == nil {
		s.state.Events = []Event{}
	}
	trimEvents(&s.state, s.state.Policy.EventsLimit)
	dir := filepath.Dir(s.path)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create state directory: %w", errMkdir)
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
	if errEncode := json.NewEncoder(temp).Encode(s.state); errEncode != nil {
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
	if errRename := os.Rename(tempName, s.path); errRename != nil {
		if errRemove := os.Remove(s.path); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			return fmt.Errorf("replace state file: %w", errRename)
		}
		if errRetry := os.Rename(tempName, s.path); errRetry != nil {
			return fmt.Errorf("replace state file: %w", errRetry)
		}
	}
	s.dirty = false
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
	if state.Events != nil {
		clone.Events = append([]Event{}, state.Events...)
	}
	if state.ResinLeaseDeletes != nil {
		clone.ResinLeaseDeletes = append([]ResinLeaseDelete{}, state.ResinLeaseDeletes...)
	}
	if state.ResinAdminTokens != nil {
		clone.ResinAdminTokens = make(map[string]string, len(state.ResinAdminTokens))
		for ref, token := range state.ResinAdminTokens {
			clone.ResinAdminTokens[ref] = token
		}
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
	return &copied
}

func trimEvents(state *State, limit int) {
	if state == nil {
		return
	}
	if limit <= 0 {
		limit = defaultEventsLimit
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
