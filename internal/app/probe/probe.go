package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"grok-quality-guard/internal/app/i18n"
	"grok-quality-guard/internal/app/redact"
	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/config"
	proxy "grok-quality-guard/internal/proxy"
)

var probePrompt = i18n.T(i18n.DefaultLocale, "probe.prompt", nil)

const probeMarker = "QUALITY_OK"
const (
	probeReconcileInterval = 5 * time.Minute
	probeReconcileTimeout  = 5 * time.Minute
)

func (s *Service) probeLoop(ctx context.Context) {
	scheduler := newProbeScheduler(s, ctx)
	s.setScheduler(scheduler)
	defer func() {
		scheduler.wait()
		s.clearScheduler(scheduler)
	}()

	reconcileRunning := false
	reconcileDone := make(chan struct{}, 1)
	startReconcile := func() {
		if reconcileRunning {
			return
		}
		reconcileRunning = true
		go func() {
			reconcileCtx, cancel := context.WithTimeout(ctx, probeReconcileTimeout)
			defer cancel()
			_ = s.env.Reconcile(reconcileCtx)
			select {
			case reconcileDone <- struct{}{}:
			case <-ctx.Done():
			}
		}()
	}

	// Reconcile repairs persisted guard state, but it must never gate scanning.
	// A stuck auth read here previously left QueueSnapshot empty indefinitely.
	scheduler.scanAndDispatch()
	startReconcile()
	scanTimer := time.NewTimer(s.probeScanInterval())
	defer scanTimer.Stop()
	reconcileTicker := time.NewTicker(probeReconcileInterval)
	defer reconcileTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-scanTimer.C:
			scheduler.scanAndDispatch()
			scanTimer.Reset(s.probeScanInterval())
		case <-reconcileTicker.C:
			startReconcile()
		case <-reconcileDone:
			reconcileRunning = false
		case result := <-scheduler.done:
			scheduler.finish(result)
			scheduler.dispatch()
		}
	}
}

// probeScheduler bounds pending work while keeping active probe goroutines at
// the configured concurrency.
type probeScheduler struct {
	plugin *Service
	ctx    context.Context
	done   chan probeCompletion

	mu            sync.Mutex
	queue         []string
	queued        map[string]struct{}
	queueKeys     map[string]string
	queuedAt      map[string]time.Time
	active        int
	activeIDs     map[string]time.Time
	succeeded     int
	failed        int
	lastScanAt    time.Time
	lastSuccessAt time.Time
	lastError     string
	lastErrorCode string
	wg            sync.WaitGroup
}

type probeCompletion struct {
	authID   string
	queueKey string
	err      error
}

func newProbeScheduler(plugin *Service, ctx context.Context) *probeScheduler {
	if ctx == nil {
		ctx = context.Background()
	}
	return &probeScheduler{
		plugin:    plugin,
		ctx:       ctx,
		done:      make(chan probeCompletion),
		queued:    make(map[string]struct{}),
		queueKeys: make(map[string]string),
		queuedAt:  make(map[string]time.Time),
		activeIDs: make(map[string]time.Time),
	}
}

func (s *probeScheduler) scanAndDispatch() {
	if s == nil || s.plugin == nil {
		return
	}
	policy := s.plugin.env.Policy()
	now := time.Now().UTC()
	s.plugin.cleanupProbeSkip(now)
	if !policy.PassiveGuard && !policy.TransparentGuard {
		s.dispatch()
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
	values := store.ProbeStatesForIDs(ids)
	states := make(map[string]ProbeAccountState, len(accounts))
	for index, account := range accounts {
		value := values[index]
		if value.AuthID == "" {
			value.AuthID = account.ID
			value.State = StateUnknown
			value.FirstSeenAt = account.FirstSeenAt
		}
		states[account.ID] = value
	}
	s.enqueueDueProjected(accounts, states, policy, now)
	s.dispatch()
}

func (s *probeScheduler) enqueueDue(accounts []Account, state State, policy Policy, now time.Time) int {
	states := make(map[string]ProbeAccountState, len(state.Accounts))
	for authID, account := range state.Accounts {
		if account == nil {
			continue
		}
		states[authID] = ProbeAccountState{
			AuthID:            authID,
			State:             account.State,
			FirstSeenAt:       account.FirstSeenAt,
			LastProbeAt:       account.LastProbeAt,
			CooldownUntil:     account.CooldownUntil,
			ProbeBackoffUntil: account.ProbeBackoffUntil,
		}
	}
	return s.enqueueDueProjected(accounts, states, policy, now)
}

func (s *probeScheduler) enqueueDueProjected(accounts []Account, states map[string]ProbeAccountState, policy Policy, now time.Time) int {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastScanAt = now
	for _, account := range accounts {
		if len(s.queue) >= limit {
			break
		}
		accountKey := probeQueueKey(account)
		if _, exists := s.queued[accountKey]; exists {
			continue
		}
		if _, exists := s.queueKeys[account.ID]; exists {
			continue
		}
		stateAccount, okState := states[account.ID]
		if !okState || stateAccount.AuthID == "" {
			// A catalog-only disabled auth is externally disabled unless the
			// guard has persisted ownership for it. Do not materialize sparse
			// disabled accounts just to classify them on every scan.
			if account.Disabled {
				continue
			}
			stateAccount = ProbeAccountState{
				AuthID:      account.ID,
				State:       StateUnknown,
				FirstSeenAt: account.FirstSeenAt,
			}
			okState = true
		}
		// Persisted disabled states still need the normal due check. In
		// particular, guard-owned cooldowns become due after CooldownUntil and
		// ProbeAccount temporarily re-enables the auth for recovery probing.
		if !probeDueForProjected(stateAccount, okState, account.ID, policy, now) {
			continue
		}
		s.queued[accountKey] = struct{}{}
		s.queueKeys[account.ID] = accountKey
		s.queuedAt[account.ID] = now
		s.queue = append(s.queue, account.ID)
		added++
	}
	return added
}

func probeQueueKey(account Account) string {
	if oauthID := strings.TrimSpace(account.OAuthID); oauthID != "" {
		return "oauth:" + oauthID
	}
	return "auth:" + strings.TrimSpace(account.ID)
}

func (s *probeScheduler) dispatch() {
	if s == nil || s.plugin == nil {
		return
	}
	limit := s.plugin.probeConcurrency()
	for {
		s.mu.Lock()
		if s.active >= limit || len(s.queue) == 0 {
			s.mu.Unlock()
			return
		}
		authID := s.queue[0]
		queueKey := s.queueKeys[authID]
		copy(s.queue, s.queue[1:])
		s.queue = s.queue[:len(s.queue)-1]
		s.active++
		s.activeIDs[authID] = time.Now().UTC()
		s.mu.Unlock()

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			errProbe := s.plugin.ProbeAccount(s.ctx, authID, false, "")
			if errProbe != nil && s.ctx.Err() == nil {
				_ = s.plugin.env.RecordInternalError(authID, errProbe)
			}
			select {
			case s.done <- probeCompletion{authID: authID, queueKey: queueKey, err: errProbe}:
			case <-s.ctx.Done():
			}
		}()
	}
}

func (s *probeScheduler) finish(result probeCompletion) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active > 0 {
		s.active--
	}
	queueKey := result.queueKey
	if queueKey == "" {
		queueKey = probeQueueKey(Account{ID: result.authID})
	}
	delete(s.queued, queueKey)
	delete(s.queueKeys, result.authID)
	delete(s.queuedAt, result.authID)
	delete(s.activeIDs, result.authID)
	if result.err != nil {
		s.failed++
		s.lastError = redact.SanitizeError(result.err)
		s.lastErrorCode = proxy.CodeOf(result.err)
		return
	}
	s.succeeded++
	s.lastSuccessAt = time.Now().UTC()
	s.lastError = ""
	s.lastErrorCode = ""
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
		s.mu.Lock()
		idle := s.active == 0 && len(s.queue) == 0
		s.mu.Unlock()
		if idle {
			return
		}
		select {
		case result := <-s.done:
			s.finish(result)
		case <-s.ctx.Done():
			s.wg.Wait()
			return
		}
	}
}

func (s *probeScheduler) detail() QueueDetail {
	if s == nil {
		return QueueDetail{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	detail := QueueDetail{
		Active:  make([]QueueEntry, 0, len(s.activeIDs)),
		Waiting: make([]QueueEntry, 0, len(s.queue)),
	}
	for authID, startedAt := range s.activeIDs {
		detail.Active = append(detail.Active, QueueEntry{
			AuthID:    authID,
			Status:    "running",
			QueuedAt:  s.queuedAt[authID],
			StartedAt: startedAt,
		})
	}
	sort.Slice(detail.Active, func(i, j int) bool {
		if detail.Active[i].StartedAt.Equal(detail.Active[j].StartedAt) {
			return detail.Active[i].AuthID < detail.Active[j].AuthID
		}
		return detail.Active[i].StartedAt.Before(detail.Active[j].StartedAt)
	})
	for _, authID := range s.queue {
		detail.Waiting = append(detail.Waiting, QueueEntry{
			AuthID:   authID,
			Status:   "waiting",
			QueuedAt: s.queuedAt[authID],
		})
	}
	return detail
}
func (s *probeScheduler) snapshot() QueueSnapshot {
	if s == nil {
		return QueueSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return QueueSnapshot{
		Started:       true,
		Waiting:       len(s.queue),
		Active:        s.active,
		Succeeded:     s.succeeded,
		Failed:        s.failed,
		LastScanAt:    s.lastScanAt,
		LastSuccessAt: s.lastSuccessAt,
		LastError:     s.lastError,
		LastErrorCode: s.lastErrorCode,
	}
}

func (s *Service) setScheduler(scheduler *probeScheduler) {
	if s == nil {
		return
	}
	s.schedulerMu.Lock()
	s.scheduler = scheduler
	s.schedulerMu.Unlock()
}

func (s *Service) clearScheduler(scheduler *probeScheduler) {
	if s == nil {
		return
	}
	s.schedulerMu.Lock()
	if s.scheduler == scheduler {
		s.scheduler = nil
	}
	s.schedulerMu.Unlock()
}

// QueueDetail returns the explicit auth IDs currently running or waiting.
func (s *Service) QueueDetail() QueueDetail {
	if s == nil {
		return QueueDetail{}
	}
	s.schedulerMu.RLock()
	scheduler := s.scheduler
	s.schedulerMu.RUnlock()
	if scheduler == nil {
		return QueueDetail{}
	}
	return scheduler.detail()
}

// QueueSnapshot returns a concurrency-safe view of the automatic probe queue.
func (s *Service) QueueSnapshot() QueueSnapshot {
	if s == nil {
		return QueueSnapshot{}
	}
	s.schedulerMu.RLock()
	scheduler := s.scheduler
	s.schedulerMu.RUnlock()
	if scheduler == nil {
		return QueueSnapshot{}
	}
	return scheduler.snapshot()
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
	return probeDueForProjected(ProbeAccountState{
		AuthID:            authID,
		State:             account.State,
		FirstSeenAt:       account.FirstSeenAt,
		LastProbeAt:       account.LastProbeAt,
		CooldownUntil:     account.CooldownUntil,
		ProbeBackoffUntil: account.ProbeBackoffUntil,
	}, true, authID, policy, now)
}

func probeDueForProjected(account ProbeAccountState, okState bool, authID string, policy Policy, now time.Time) bool {
	if !okState || account.AuthID == "" {
		return true
	}
	switch account.State {
	case StateHealthy:
		if policy.Probe.HealthyIntervalHours <= 0 {
			return false
		}
		if !account.ProbeBackoffUntil.IsZero() && now.Before(account.ProbeBackoffUntil) {
			return false
		}
		interval := healthyProbeInterval(policy.Probe.HealthyIntervalHours)
		if account.LastProbeAt.IsZero() {
			return true
		}
		return !now.Before(account.LastProbeAt.Add(interval))
	case StateDisabledByGuard, StateExternallyDisabled, StateCredentialInvalid:
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

func healthyProbeInterval(hours int) time.Duration {
	if hours <= 0 {
		return 0
	}
	const maxDuration = time.Duration(1<<63 - 1)
	maxHours := int64(maxDuration / time.Hour)
	if int64(hours) > maxHours {
		return maxDuration
	}
	return time.Duration(hours) * time.Hour
}

func unknownProbeJitter(authID string, max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(authID))
	return time.Duration(hash.Sum64() % uint64(max))
}
func (s *Service) ProbeAccount(ctx context.Context, authID string, force bool, callbackID string) (errResult error) {
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
	var proxyFailureOutcome VerdictResult
	var proxyFailureVerdict QualityVerdict
	defer func() {
		lock.Unlock()
		if !proxyFailureOutcome.ProxyRotate {
			return
		}
		rotateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if errRotate := s.env.Quality().FinishProxyErrorRotation(rotateCtx, authID, proxyFailureVerdict); errRotate != nil && errResult == nil {
			errResult = errRotate
		}
	}()
	account, response, raw, errResolve := s.resolveAuth(ctx, authID)
	if errResolve != nil {
		return errResolve
	}
	if !isGrokAuth(raw) {
		return nil
	}
	stateAccount, okState := s.stateAccount(authID)
	rawDisabled := authDisabled(raw)
	catalogDisabled := account.Disabled
	guardOwned := okState && stateAccount.DisableKind != "" && !stateAccount.PluginOnlyDisable

	// The catalog can lag behind a host.auth.save that just enabled this auth.
	// When the file is already enabled but the catalog still reports disabled,
	// refresh before classifying the discrepancy as an external disable.
	if catalogDisabled && !rawDisabled && !guardOwned {
		if errRefresh := s.refreshCatalog(ctx); errRefresh != nil {
			return fmt.Errorf("refresh auth catalog before probe: %w", errRefresh)
		}
		account, response, raw, errResolve = s.resolveAuth(ctx, authID)
		if errResolve != nil {
			return errResolve
		}
		stateAccount, okState = s.stateAccount(authID)
		rawDisabled = authDisabled(raw)
		catalogDisabled = account.Disabled
		guardOwned = okState && stateAccount.DisableKind != "" && !stateAccount.PluginOnlyDisable
	}
	if rawDisabled && !guardOwned {
		return s.setStateExternal(authID, ClassProbeError)
	}
	if catalogDisabled && !guardOwned {
		return s.setStateExternal(authID, ClassProbeError)
	}
	wasDisabled := rawDisabled || catalogDisabled
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
	handleFailure := func(verdict QualityVerdict, errProbe error) error {
		outcome, errApply := s.handleProbeFailureResult(authID, raw, response, reEnabled, verdict, errProbe)
		proxyFailureOutcome = outcome
		proxyFailureVerdict = verdict
		return errApply
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
		class, reason := classifyProbeTransportFailure(errStream)
		return handleFailure(QualityVerdict{Class: class, Reason: reason}, errStream)
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
		return handleFailure(QualityVerdict{Class: class, Reason: reason}, fmt.Errorf("probe %s", reason))
	}
	analyzer := NewStreamAnalyzerWithProtocolAt(probeCfg.ExitProtocol, probeStartedAt)
	analyzer.SetEncryptedConfig(qualityCfg.MinEncryptedBytes, qualityCfg.EncryptedBytesPerReasoningToken)
	var bufferBytes int64
	var terminal bool
	reader = s.openModelStreamReader(probeCtx, stream.StreamID)
	for {
		select {
		case <-probeCtx.Done():
			return handleFailure(QualityVerdict{Class: ClassProbeError, Reason: "reason.detail.probe_context_ended"}, probeCtx.Err())
		default:
		}
		read, errRead := reader.Next()
		if errRead != nil {
			class, reason := classifyProbeTransportFailure(errRead)
			return handleFailure(QualityVerdict{Class: class, Reason: reason}, errRead)
		}
		if len(read.Payload) > 0 {
			bufferBytes += int64(len(read.Payload))
			if bufferBytes > qualityCfg.HoldMaxBufferBytes {
				return handleFailure(QualityVerdict{Class: ClassProbeError, Reason: "reason.detail.probe_response_exceeded_buffer_limit"}, fmt.Errorf("probe response exceeded buffer limit"))
			}
			analyzer.ObserveChunk(read.Payload)
		}
		if read.Error != "" {
			readErr := fmt.Errorf("%s", read.Error)
			class, reason := classifyProbeTransportFailure(readErr)
			return handleFailure(QualityVerdict{Class: class, Reason: reason}, readErr)
		}
		if read.Done {
			terminal = true
			break
		}
	}
	sig := analyzer.Finalize(terminal)
	sig.DurationMS = time.Since(probeStartedAt).Milliseconds()
	markerOK := matchProbeMarker(sig.VisibleText, probeMarker)
	probeReasoningRequired := reasoningRequiredForModelWithNonReasoning(probeCfg.Model, body, qualityCfg.NonReasoningModels)
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
	return handleFailure(verdict, fmt.Errorf("probe quality check failed: %s", verdict.Reason))
}

func (s *Service) markProbeSuccess(authID string, sig Signals) error {
	now := time.Now().UTC()
	qualityCfg := s.env.Policy().Quality
	verdict := QualityVerdict{Class: ClassNone, Reason: "reason.detail.probe_passed", Signals: sig}
	errUpdate := s.stateStore().RecordObservationEvents(authID, func(account *AccountState) ([]Event, error) {
		account.State = StateHealthy
		account.LastProbeAt = now
		account.LastObservedAt = now
		account.LastReason = ""
		account.LastAttempt = &AttemptEvidence{At: now, Source: "probe", Class: ClassNone, Reason: "reason.detail.probe_passed"}
		account.LastEvidence = qualityEvidenceFromVerdict(verdict, "probe", now)
		account.CooldownUntil = time.Time{}
		account.ProbeBackoffUntil = time.Time{}
		account.DisableKind = ""
		account.DisableReason = ""
		account.PluginOnlyDisable = false
		account.PreviousDisabled = false
		account.AuthDisabledIntent = nil
		account.SoftStrikes = 0
		account.ErrorStrikes = 0
		account.ThinkingStrikes = 0
		decayEvent := noteHealthyObservation(account, now, qualityCfg)
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
		appstate.ObserveRoutingSample(
			&account.RoutingStats,
			appstate.RoutingSourceProbe,
			sig.ReasoningRequired || sig.ReasoningTokens > 0 || sig.HasReasoningDelta || sig.HasThinking,
			true,
			sig.FirstTokenMS,
			sig.TPS,
			now,
			appstate.RoutingSampleHalfLife(appstate.RoutingSourceProbe, s.env.Policy().Probe.HealthyIntervalHours),
		)
		events := []Event{{
			Time:      time.Now().UTC(),
			AuthID:    authID,
			Action:    "probe",
			Result:    "healthy",
			Detail:    "probe passed",
			DetailKey: "event.detail.probe_passed",
		}}
		if decayEvent != nil {
			events = append(events, *decayEvent)
		}
		return events, nil
	})
	if errUpdate == nil {
		s.refreshSelectionAccount(authID)
	}
	return errUpdate
}

func (s *Service) handleProbeFailure(authID string, raw map[string]json.RawMessage, response pluginapi.HostAuthGetResponse, reEnabled bool, verdict QualityVerdict, errProbe error) error {
	_, errApply := s.handleProbeFailureResult(authID, raw, response, reEnabled, verdict, errProbe)
	return errApply
}

func (s *Service) handleProbeFailureResult(authID string, raw map[string]json.RawMessage, response pluginapi.HostAuthGetResponse, reEnabled bool, verdict QualityVerdict, errProbe error) (VerdictResult, error) {
	// Keep marker and TPS verdicts out of internal error events. Transport
	// failures are useful diagnostics even when they remain non-quality class
	// (for example, a host model cooldown).
	if errProbe != nil {
		switch verdict.Class {
		case ClassProbeError, ClassProxyError, ClassCredentialInvalid, ClassQuotaExhausted, ClassRateLimited, ClassIgnored:
			_ = s.recordInternalError(authID, errProbe)
		}
	}
	return s.applyVerdictLocked(
		context.Background(),
		authID,
		verdict,
		"probe",
		"",
		raw,
		response,
		reEnabled,
	)
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

func (s *Service) probeScanInterval() time.Duration {
	if s == nil {
		return config.DefaultProbeScanInterval
	}
	interval := s.env.Policy().Probe.ScanInterval.Duration
	if interval <= 0 {
		return config.DefaultProbeScanInterval
	}
	return interval
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
