package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"grok-quality-guard/internal/config"
)

const probePrompt = "先用三点总结为什么天空呈蓝色，最后一行只输出 QUALITY_OK。"
const probeMarker = "QUALITY_OK"
const probeReconcileInterval = 5 * time.Minute

func (s *Service) probeLoop(ctx context.Context) {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	_ = s.env.Reconcile(ctx)
	scheduler := newProbeScheduler(s, ctx)
	defer scheduler.wait()
	scheduler.scanAndDispatch()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	reconcileTicker := time.NewTicker(probeReconcileInterval)
	defer reconcileTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scheduler.scanAndDispatch()
		case <-reconcileTicker.C:
			_ = s.env.Reconcile(ctx)
		case authID := <-scheduler.done:
			scheduler.finish(authID)
			scheduler.dispatch()
		}
	}
}

// probeScheduler bounds pending work while keeping active probe goroutines at
// the configured concurrency
type probeScheduler struct {
	plugin *Service
	ctx    context.Context
	done   chan string

	queue  []string
	queued map[string]struct{}
	active int
	wg     sync.WaitGroup
}

func newProbeScheduler(plugin *Service, ctx context.Context) *probeScheduler {
	if ctx == nil {
		ctx = context.Background()
	}
	return &probeScheduler{
		plugin: plugin,
		ctx:    ctx,
		done:   make(chan string),
		queued: make(map[string]struct{}),
	}
}

func (s *probeScheduler) scanAndDispatch() {
	if s == nil || s.plugin == nil {
		return
	}
	policy := s.plugin.env.Policy()
	if !policy.PassiveGuard && !policy.TransparentGuard {
		return
	}
	accounts := s.plugin.env.CatalogSnapshot()
	store := s.plugin.env.Store()
	if store == nil {
		return
	}
	ids := make([]string, len(accounts))
	for index, account := range accounts {
		ids[index] = account.ID
	}
	values := store.AccountStatesForIDs(ids)
	states := make(map[string]*AccountState, len(accounts))
	for index, account := range accounts {
		if values[index].AuthID == "" {
			continue
		}
		value := values[index]
		states[account.ID] = &value
	}
	s.enqueueDue(accounts, State{Accounts: states}, policy, time.Now().UTC())
	s.dispatch()
}

func (s *probeScheduler) enqueueDue(accounts []Account, state State, policy Policy, now time.Time) int {
	if s == nil {
		return 0
	}
	limit := policy.Probe.QueueSize
	if limit <= 0 {
		limit = config.DefaultProbeQueueSize
	}
	if limit > config.MaxProbeQueueSize {
		limit = config.MaxProbeQueueSize
	}
	added := 0
	for _, account := range accounts {
		if len(s.queue) >= limit {
			break
		}
		if _, exists := s.queued[account.ID]; exists {
			continue
		}
		stateAccount, okState := state.Accounts[account.ID]
		if !probeDueForAccount(stateAccount, okState, account.ID, policy, now) {
			continue
		}
		s.queued[account.ID] = struct{}{}
		s.queue = append(s.queue, account.ID)
		added++
	}
	return added
}

func (s *probeScheduler) dispatch() {
	if s == nil || s.plugin == nil {
		return
	}
	limit := s.plugin.probeConcurrency()
	for s.active < limit && len(s.queue) > 0 {
		authID := s.queue[0]
		copy(s.queue, s.queue[1:])
		s.queue = s.queue[:len(s.queue)-1]
		s.active++
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if errProbe := s.plugin.ProbeAccount(s.ctx, authID, false, ""); errProbe != nil && s.ctx.Err() == nil {
				_ = s.plugin.env.RecordInternalError(authID, errProbe)
			}
			select {
			case s.done <- authID:
			case <-s.ctx.Done():
			}
		}()
	}
}

func (s *probeScheduler) finish(authID string) {
	if s == nil {
		return
	}
	if s.active > 0 {
		s.active--
	}
	delete(s.queued, authID)
}

func (s *probeScheduler) wait() {
	if s != nil {
		s.wg.Wait()
	}
}

func (s *probeScheduler) waitIdle() {
	if s == nil {
		return
	}
	for {
		select {
		case <-s.ctx.Done():
			s.wg.Wait()
			return
		default:
		}
		s.dispatch()
		if s.active == 0 && len(s.queue) == 0 {
			return
		}
		select {
		case authID := <-s.done:
			s.finish(authID)
		case <-s.ctx.Done():
			s.wg.Wait()
			return
		}
	}
}
func (s *Service) probeDue(authID string, force bool) (bool, error) {
	if force {
		return true, nil
	}
	policy := s.env.Policy()
	store := s.stateStore()
	if store == nil {
		return true, nil
	}
	account, okState := store.AccountSnapshot(authID)
	return probeDueForAccount(&account, okState, authID, policy, time.Now().UTC()), nil
}

func probeDueForAccount(account *AccountState, okState bool, authID string, policy Policy, now time.Time) bool {
	if !okState || account == nil {
		return true
	}
	switch account.State {
	case StateHealthy, StateDisabledByGuard, StateExternallyDisabled, StateCredentialInvalid:
		return false
	case StateCooling, StateQuotaExhausted:
		if account.CooldownUntil.IsZero() {
			return true
		}
		return !now.Before(account.CooldownUntil)
	case StateSuspect:
		return account.ProbeBackoffUntil.IsZero() || !now.Before(account.ProbeBackoffUntil)
	case StateUnknown:
		if !account.ProbeBackoffUntil.IsZero() && now.Before(account.ProbeBackoffUntil) {
			return false
		}
		delay := policy.Probe.UnknownDelay.Duration
		if delay <= 0 {
			delay = 30 * time.Second
		}
		delay += unknownProbeJitter(authID, policy.Probe.UnknownJitter.Duration)
		if account.FirstSeenAt.IsZero() {
			return true
		}
		return !now.Before(account.FirstSeenAt.Add(delay))
	default:
		return false
	}
}

func unknownProbeJitter(authID string, max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(authID))
	return time.Duration(hash.Sum64() % uint64(max))
}
func (s *Service) ProbeAccount(ctx context.Context, authID string, force bool, callbackID string) error {
	if strings.TrimSpace(authID) == "" {
		return fmt.Errorf("auth_id is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return errCtx
	}
	due, errDue := s.probeDue(authID, force)
	if errDue != nil || !due {
		return errDue
	}
	// Reserve the account before taking a concurrency slot so a second probe for
	// the same account waits without occupying another slot
	if errBegin := s.beginAccountProbe(ctx, authID); errBegin != nil {
		return errBegin
	}
	defer s.endAccountProbe(authID)
	if errSlot := s.acquireProbeSlot(ctx, force); errSlot != nil {
		return errSlot
	}
	defer s.releaseProbeSlot()

	lock := s.accountLock(authID)
	lock.Lock()
	defer lock.Unlock()

	account, response, raw, errResolve := s.resolveAuth(ctx, authID)
	if errResolve != nil {
		return errResolve
	}
	if !isGrokAuth(raw) {
		return nil
	}
	stateAccount, okState := s.stateAccount(authID)
	wasDisabled := authDisabled(raw)
	if wasDisabled && (!okState || stateAccount.DisableKind == "") {
		return s.setStateExternal(authID, ClassProbeError)
	}
	if wasDisabled && (stateAccount.DisableKind == DisableKindPermanent || stateAccount.DisableKind == DisableKindCredentialInvalid) {
		return nil
	}
	if wasDisabled && !force && isCooldownState(stateAccount.State) {
		if !stateAccount.CooldownUntil.IsZero() && time.Now().UTC().Before(stateAccount.CooldownUntil) {
			return nil
		}
	}
	reEnabled := false
	if wasDisabled {
		if errEnable := s.saveAuthDisabled(response.Name, raw, false); errEnable != nil {
			return errEnable
		}
		reEnabled = true
	}
	s.MarkProbeAuth(account.ID, 3*time.Minute)
	policy := s.env.Policy()
	probeCfg := policy.Probe
	qualityCfg := policy.Quality
	timeout := probeCfg.Timeout.Duration
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body := probeBody(probeCfg)
	probeStartedAt := time.Now()
	stream, errStream := s.executeModelStream(probeCtx, pluginapi.HostModelExecutionRequest{
		EntryProtocol:  probeCfg.EntryProtocol,
		ExitProtocol:   probeCfg.ExitProtocol,
		Model:          probeCfg.Model,
		Stream:         true,
		Body:           body,
		Headers:        http.Header{"Content-Type": []string{"application/json"}, probeHeader: []string{"1"}},
		ForcedProvider: "xai",
		AuthID:         account.ID,
		ProxyURL:       strings.TrimSpace(authRawString(raw, "proxy_url")),
	}, callbackID)
	if errStream != nil {
		class, reason := classifyTransportFailure(errStream)
		return s.handleProbeFailure(authID, raw, response, reEnabled, QualityVerdict{Class: class, Reason: reason}, errStream)
	}
	var reader *modelStreamReader
	defer func() {
		_ = s.closeModelStream(stream.StreamID)
		if reader != nil {
			reader.Close()
		}
	}()
	if stream.StatusCode < 200 || stream.StatusCode >= 300 {
		errorBody := s.readModelStreamErrorBody(probeCtx, stream.StreamID)
		class, reason := classifyUpstreamStatus(stream.StatusCode, errorBody)
		return s.handleProbeFailure(authID, raw, response, reEnabled, QualityVerdict{Class: class, Reason: reason}, fmt.Errorf("probe %s", reason))
	}
	analyzer := NewStreamAnalyzerWithProtocolAt(probeCfg.ExitProtocol, probeStartedAt)
	analyzer.SetEncryptedConfig(qualityCfg.MinEncryptedBytes, qualityCfg.EncryptedBytesPerReasoningToken)
	var bufferBytes int64
	var terminal bool
	reader = s.openModelStreamReader(probeCtx, stream.StreamID)
	for {
		select {
		case <-probeCtx.Done():
			return s.handleProbeFailure(authID, raw, response, reEnabled, QualityVerdict{Class: ClassProbeError, Reason: "probe context ended"}, probeCtx.Err())
		default:
		}
		read, errRead := reader.Next()
		if errRead != nil {
			class, reason := classifyTransportFailure(errRead)
			return s.handleProbeFailure(authID, raw, response, reEnabled, QualityVerdict{Class: class, Reason: reason}, errRead)
		}
		if len(read.Payload) > 0 {
			bufferBytes += int64(len(read.Payload))
			if bufferBytes > qualityCfg.HoldMaxBufferBytes {
				return s.handleProbeFailure(authID, raw, response, reEnabled, QualityVerdict{Class: ClassProbeError, Reason: "probe response exceeded buffer limit"}, fmt.Errorf("probe response exceeded buffer limit"))
			}
			analyzer.ObserveChunk(read.Payload)
		}
		if read.Error != "" {
			readErr := fmt.Errorf("%s", read.Error)
			class, reason := classifyTransportFailure(readErr)
			return s.handleProbeFailure(authID, raw, response, reEnabled, QualityVerdict{Class: class, Reason: reason}, readErr)
		}
		if read.Done {
			terminal = true
			break
		}
	}
	sig := analyzer.Finalize(terminal)
	sig.DurationMS = time.Since(probeStartedAt).Milliseconds()
	markerOK := matchProbeMarker(sig.VisibleText, probeMarker)
	probeReasoningRequired := reasoningRequiredForModel(probeCfg.Model, body)
	verdict := classifyProbeResult(sig, qualityCfg, markerOK, probeReasoningRequired)
	if verdict.Class == ClassNone {
		if errSuccess := s.markProbeSuccess(authID, verdict.Signals); errSuccess != nil {
			return errSuccess
		}
		return nil
	}
	class := verdict.Class
	if class == ClassNone {
		class = ClassProbeError
	}
	return s.handleProbeFailure(authID, raw, response, reEnabled, verdict, fmt.Errorf("probe quality check failed: %s", verdict.Reason))
}

func (s *Service) markProbeSuccess(authID string, sig Signals) error {
	now := time.Now().UTC()
	qualityCfg := s.env.Policy().Quality
	verdict := QualityVerdict{Class: ClassNone, Reason: "probe passed", Signals: sig}
	var decayEvent *Event
	errUpdate := s.stateStore().UpdateAccount(authID, func(account *AccountState) (*Event, error) {
		account.State = StateHealthy
		account.LastProbeAt = now
		account.LastObservedAt = now
		account.LastReason = ""
		account.LastAttempt = &AttemptEvidence{At: now, Source: "probe", Class: ClassNone, Reason: "probe passed"}
		account.LastEvidence = qualityEvidenceFromVerdict(verdict, "probe", now)
		account.CooldownUntil = time.Time{}
		account.ProbeBackoffUntil = time.Time{}
		account.DisableKind = ""
		account.DisableReason = ""
		account.PreviousDisabled = false
		account.AuthDisabledIntent = nil
		account.SoftStrikes = 0
		account.ErrorStrikes = 0
		account.ThinkingStrikes = 0
		decayEvent = noteHealthyObservation(account, now, qualityCfg)
		account.Metrics = Metrics{
			FirstTokenMS:    sig.FirstTokenMS,
			DurationMS:      sig.DurationMS,
			GenerationMS:    sig.GenerationMS,
			OutputTokens:    sig.OutputTokens,
			ReasoningTokens: sig.ReasoningTokens,
			VisibleTokens:   sig.VisibleTokens,
			EncryptedBytes:  sig.EncryptedBytes,
			TPS:             sig.TPS,
		}
		return &Event{
			Time:   time.Now().UTC(),
			AuthID: authID,
			Action: "probe",
			Result: "healthy",
			Detail: "probe passed",
		}, nil
	})
	if errUpdate == nil {
		s.refreshSelectionAccount(authID)
		if decayEvent != nil {
			_ = s.stateStore().AppendEvent(*decayEvent)
		}
	}
	return errUpdate
}

func (s *Service) handleProbeFailure(authID string, raw map[string]json.RawMessage, response pluginapi.HostAuthGetResponse, reEnabled bool, verdict QualityVerdict, errProbe error) error {
	// Keep marker and TPS verdicts out of internal error events
	if errProbe != nil && verdict.Class == ClassProbeError {
		_ = s.recordInternalError(authID, errProbe)
	}
	_, errApply := s.applyVerdictLocked(
		context.Background(),
		authID,
		verdict,
		"probe",
		"",
		raw,
		response,
		reEnabled,
	)
	return errApply
}
func (s *Service) saveAuthDisabled(name string, raw map[string]json.RawMessage, disabled bool) error {
	setRawBool(raw, "disabled", disabled)
	encoded, errMarshal := marshalRawMap(raw)
	if errMarshal != nil {
		return errMarshal
	}
	_, errSave := s.saveAuth(name, encoded)
	return errSave
}

func matchProbeMarker(text, expected string) bool {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return true
	}
	text = strings.TrimRightFunc(text, unicode.IsSpace)
	lines := strings.Split(text, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line == "" {
			continue
		}
		return strings.EqualFold(line, expected)
	}
	return false
}
func probeBody(cfg ProbeConfig) []byte {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "grok-4.5"
	}
	body := map[string]any{
		"model":             model,
		"stream":            true,
		"max_output_tokens": cfg.MaxOutputTokens,
		"input": []any{map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": probePrompt,
			}},
		}},
	}
	raw, _ := json.Marshal(body)
	return raw
}
func nextProbeBackoff(cfg ProbeConfig, previous AccountState) time.Duration {
	base := cfg.SuspectBackoff.Duration
	if base <= 0 {
		base = 15 * time.Minute
	}
	maxBackoff := cfg.MaxBackoff.Duration
	if maxBackoff <= 0 {
		maxBackoff = time.Hour
	}
	if maxBackoff < base {
		maxBackoff = base
	}
	backoff := base
	if !previous.LastProbeAt.IsZero() && !previous.ProbeBackoffUntil.IsZero() {
		previousWindow := previous.ProbeBackoffUntil.Sub(previous.LastProbeAt)
		if previousWindow > 0 {
			backoff = previousWindow * 2
		}
	}
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	return backoff
}

func (s *Service) probeConcurrency() int {
	if s == nil {
		return 1
	}
	limit := s.env.Policy().Probe.Concurrency
	if limit <= 0 {
		return 1
	}
	return limit
}

func (s *Service) probeQueueSize() int {
	if s == nil {
		return config.DefaultProbeQueueSize
	}
	limit := s.env.Policy().Probe.QueueSize
	if limit <= 0 {
		return config.DefaultProbeQueueSize
	}
	if limit > config.MaxProbeQueueSize {
		return config.MaxProbeQueueSize
	}
	return limit
}

func (s *Service) beginAccountProbe(ctx context.Context, authID string) error {
	s.probeGate.Lock()
	defer s.probeGate.Unlock()
	for {
		if _, busy := s.probing[authID]; !busy {
			if s.probing == nil {
				s.probing = make(map[string]struct{})
			}
			s.probing[authID] = struct{}{}
			return nil
		}
		if errWait := s.waitProbeLocked(ctx); errWait != nil {
			return errWait
		}
	}
}

func (s *Service) endAccountProbe(authID string) {
	if s == nil {
		return
	}
	s.probeGate.Lock()
	delete(s.probing, authID)
	s.probeCond.Broadcast()
	s.probeGate.Unlock()
}

func (s *Service) acquireProbeSlot(ctx context.Context, priority bool) error {
	if priority {
		s.probeGate.Lock()
		s.probePriorityWaiters++
		s.probeGate.Unlock()
		defer func() {
			s.probeGate.Lock()
			s.probePriorityWaiters--
			s.probeCond.Broadcast()
			s.probeGate.Unlock()
		}()
	}
	for {
		limit := s.probeConcurrency()
		s.probeGate.Lock()
		if s.probeActive < limit && (priority || s.probePriorityWaiters == 0) {
			if errCtx := ctx.Err(); errCtx != nil {
				s.probeGate.Unlock()
				return errCtx
			}
			s.probeActive++
			s.probeGate.Unlock()
			return nil
		}
		errWait := s.waitProbeLocked(ctx)
		s.probeGate.Unlock()
		if errWait != nil {
			return errWait
		}
	}
}

func (s *Service) releaseProbeSlot() {
	if s == nil {
		return
	}
	s.probeGate.Lock()
	if s.probeActive > 0 {
		s.probeActive--
	}
	s.probeCond.Broadcast()
	s.probeGate.Unlock()
}

func (s *Service) wakeProbeWaiters() {
	if s == nil || s.probeCond == nil {
		return
	}
	s.probeGate.Lock()
	s.probeCond.Broadcast()
	s.probeGate.Unlock()
}

// waitProbeLocked waits until a probe slot or account reservation changes.
// Caller must hold probeGate.
func (s *Service) waitProbeLocked(ctx context.Context) error {
	if ctx == nil {
		s.probeCond.Wait()
		return nil
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return errCtx
	}
	stop := context.AfterFunc(ctx, func() {
		s.probeGate.Lock()
		s.probeCond.Broadcast()
		s.probeGate.Unlock()
	})
	s.probeCond.Wait()
	stop()
	return ctx.Err()
}
