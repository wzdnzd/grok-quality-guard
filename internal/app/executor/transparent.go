package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var errTransparentStreamIdleTimeout = errors.New("upstream stream idle timeout")

// startTransparentStreamBridge ties the request-lifetime goroutine to the
// plugin lifecycle so Quiesce/Shutdown can cancel and wait for it.
func (s *Service) startTransparentStreamBridge(req Request, streamID string) {
	locale := executorRequestLocale(req.Headers)
	ctx, quiescing := s.env.RequestContext()
	if quiescing {
		s.closePluginErrorStream(streamID, locale, "plugin_shutting_down", http.StatusServiceUnavailable)
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.env.TaskStarted()
	go func() {
		defer s.env.TaskDone()
		s.runTransparentStreamBridge(ctx, req, streamID)
	}()
}

func (s *Service) transparentExecute(ctx context.Context, req Request) (Result, error) {
	policy := s.env.Policy()
	if !policy.TransparentGuard {
		return Result{}, fmt.Errorf("transparent guard is disabled")
	}
	facts, errFacts := s.env.PrepareRequest(req)
	if errFacts != nil {
		return Result{}, errFacts
	}
	body := facts.Body
	sourceFormat := facts.SourceFormat
	requestModel := facts.Model
	replayable := facts.Replayable
	selection, errSelection := s.env.NewSelection(req, facts)
	if errSelection != nil {
		return Result{}, errSelection
	}
	maxAttempts := transparentMaxAttempts(policy)
	attempts := 0
	last := Result{}
candidateLoop:
	for {
		attempt, ok := selection.Next(ctx)
		if !ok {
			break
		}
		attemptID := attempt.Account.ID
		source := attempt.Source
		rotated := false
		account := attempt.Account
		raw := attempt.Raw
		for {
			if attempts >= maxAttempts {
				attempt.Release()
				break candidateLoop
			}
			if raw == nil {
				fetchedAccount, fetchedRaw, ok := s.env.ExecutableAuth(ctx, attemptID)
				if !ok {
					attempt.Release()
					s.env.RefreshSelectionAccount(attemptID)
					break
				}
				account, raw = fetchedAccount, fetchedRaw
			}
			attempts++
			execution := buildHostExecutionWithModel(req, body, sourceFormat, requestModel, account.ID, authRawString(raw, "proxy_url"), false)
			raw = nil
			info := s.transparentAttemptInfo(facts, account.ID)
			result, verdict, errAttempt := s.executeTransparentOnce(ctx, execution, req.HostCallbackID, policy.Quality, info)
			if errAttempt == errTransparentAttemptAborted {
				attempt.Release()
				return Result{}, errAttempt
			}
			if errAttempt == nil {
				selection.MarkSuccess(account.ID, source)
				attempt.Release()
				return result, nil
			}
			class := verdict.Class
			if result.Available {
				last = result
			}
			_ = s.recordAttemptError(account.ID, class, errAttempt)
			selection.MarkFailure(account.ID, source, class)
			failureRetryable := attempts < maxAttempts && transparentFailureCanFailover(replayable, false, result.PreResponse, class)
			if failureRetryable && !rotated && shouldRotateTransparentFailure(selection.HasRemainingCandidates(), class) {
				didRotate, errRotate := s.rotateAfterFailure(ctx, account.ID, class)
				if errRotate == nil && didRotate {
					rotated = true
					continue
				}
			}
			s.applyTransparentFailureVerdict(ctx, account.ID, verdict, class, errAttempt, req.HostCallbackID)
			attempt.Release()
			if !failureRetryable {
				break candidateLoop
			}
			break
		}
	}
	if policy.Quality.OnExhausted == "fail_open" && last.Available {
		return last, nil
	}
	return Result{}, fmt.Errorf("no healthy Grok account produced a valid response")
}

func (s *Service) transparentExecuteBufferedStream(ctx context.Context, req Request) (Result, error) {
	policy := s.env.Policy()
	if !policy.TransparentGuard {
		return Result{}, fmt.Errorf("transparent guard is disabled")
	}
	facts, errFacts := s.env.PrepareRequest(req)
	if errFacts != nil {
		return Result{}, errFacts
	}
	body := facts.Body
	sourceFormat := facts.SourceFormat
	requestModel := facts.Model
	replayable := facts.Replayable
	selection, errSelection := s.env.NewSelection(req, facts)
	if errSelection != nil {
		return Result{}, errSelection
	}
	maxAttempts := transparentMaxAttempts(policy)
	attempts := 0
	last := Result{}
candidateLoop:
	for {
		attempt, ok := selection.Next(ctx)
		if !ok {
			break
		}
		attemptID := attempt.Account.ID
		source := attempt.Source
		rotated := false
		account := attempt.Account
		raw := attempt.Raw
		for {
			if attempts >= maxAttempts {
				attempt.Release()
				break candidateLoop
			}
			if raw == nil {
				fetchedAccount, fetchedRaw, ok := s.env.ExecutableAuth(ctx, attemptID)
				if !ok {
					attempt.Release()
					s.env.RefreshSelectionAccount(attemptID)
					break
				}
				account, raw = fetchedAccount, fetchedRaw
			}
			attempts++
			execution := buildHostExecutionWithModel(req, body, sourceFormat, requestModel, account.ID, authRawString(raw, "proxy_url"), true)
			raw = nil
			info := s.transparentAttemptInfo(facts, account.ID)
			result, verdict, errAttempt := s.executeTransparentBufferedAttempt(ctx, execution, req.HostCallbackID, policy.Quality, info)
			if errAttempt == errTransparentAttemptAborted {
				attempt.Release()
				return Result{}, errAttempt
			}
			if errAttempt == nil {
				selection.MarkSuccess(account.ID, source)
				attempt.Release()
				return result, nil
			}
			class := verdict.Class
			if result.Available {
				last = result
			}
			_ = s.recordAttemptError(account.ID, class, errAttempt)
			selection.MarkFailure(account.ID, source, class)
			failureRetryable := attempts < maxAttempts && transparentFailureCanFailover(replayable, false, result.PreResponse, class)
			if failureRetryable && !rotated && shouldRotateTransparentFailure(selection.HasRemainingCandidates(), class) {
				didRotate, errRotate := s.rotateAfterFailure(ctx, account.ID, class)
				if errRotate == nil && didRotate {
					rotated = true
					continue
				}
			}
			s.applyTransparentFailureVerdict(ctx, account.ID, verdict, class, errAttempt, req.HostCallbackID)
			attempt.Release()
			if !failureRetryable {
				break candidateLoop
			}
			break
		}
	}
	if policy.Quality.OnExhausted == "fail_open" && last.Available {
		return last, nil
	}
	return Result{}, fmt.Errorf("no healthy Grok account produced a valid response")
}

func (s *Service) executeTransparentOnce(ctx context.Context, execution pluginapi.HostModelExecutionRequest, callbackID string, cfg QualityConfig, info attemptInfo) (Result, QualityVerdict, error) {
	execution.Stream = false
	if ctx.Err() != nil {
		return Result{}, QualityVerdict{}, errTransparentAttemptAborted
	}
	startedAt := time.Now()
	response, errExecute := s.env.ExecuteModel(ctx, execution, callbackID)
	if errExecute != nil {
		if ctx.Err() != nil {
			return Result{}, QualityVerdict{}, errTransparentAttemptAborted
		}
		class, reason := classifyTransparentTransportFailure(errExecute)
		return Result{PreResponse: true}, QualityVerdict{Class: class, Reason: reason}, errExecute
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		class, reason := classifyUpstreamStatus(response.StatusCode, response.Body)
		return Result{}, QualityVerdict{Class: class, Reason: reason}, fmt.Errorf("%s", reason)
	}
	analyzer := NewStreamAnalyzerWithProtocolAt(execution.ExitProtocol, startedAt)
	analyzer.SetStreaming(false)
	analyzer.SetRetainText(false)
	analyzer.SetEncryptedConfig(cfg.MinEncryptedBytes, cfg.EncryptedBytesPerReasoningToken)
	analyzer.ObserveJSON(response.Body)
	sig := info.decorate(analyzer.Finalize(true))
	sig.DurationMS = time.Since(startedAt).Milliseconds()
	verdict := classifyNonStreamResult(sig, cfg, info.ReasoningNeeded, cfg.MinHoldOutputTokens)
	result := Result{Payload: response.Body, Headers: response.Headers, Available: true}
	if verdict.Class == ClassNone {
		_ = s.env.ApplyVerdict(ctx, execution.AuthID, verdict, "transparent", callbackID)
		return result, verdict, nil
	}
	if cfg.ShadowMode {
		s.recordShadowVerdict(execution.AuthID, verdict)
		return result, verdict, nil
	}
	s.requestWithheldProbe(execution.AuthID, verdict)
	return result, verdict, fmt.Errorf("quality gate rejected response: %s", verdict.Class)
}

func (s *Service) openTransparentModelStream(ctx context.Context, execution pluginapi.HostModelExecutionRequest, callbackID string, cfg QualityConfig) (pluginapi.HostModelStreamResponse, error) {
	openCtx, cancel := context.WithTimeout(ctx, transparentStreamIdleTimeout(cfg))
	defer cancel()
	stream, errExecute := s.env.ExecuteModelStream(openCtx, execution, callbackID)
	if errExecute != nil {
		if ctx.Err() != nil {
			return pluginapi.HostModelStreamResponse{}, ctx.Err()
		}
		if errors.Is(openCtx.Err(), context.DeadlineExceeded) {
			return pluginapi.HostModelStreamResponse{}, errTransparentStreamIdleTimeout
		}
		return pluginapi.HostModelStreamResponse{}, errExecute
	}
	if ctx.Err() != nil || errors.Is(openCtx.Err(), context.DeadlineExceeded) {
		if stream.StreamID != "" {
			_ = s.env.CloseModelStream(stream.StreamID)
		}
		if ctx.Err() != nil {
			return pluginapi.HostModelStreamResponse{}, ctx.Err()
		}
		return pluginapi.HostModelStreamResponse{}, errTransparentStreamIdleTimeout
	}
	return stream, nil
}

func (s *Service) executeTransparentBufferedAttempt(ctx context.Context, execution pluginapi.HostModelExecutionRequest, callbackID string, cfg QualityConfig, info attemptInfo) (Result, QualityVerdict, error) {
	// The host returns the stream only after the first chunk is readable, so the
	// stream-open and TPS clocks must start before the call to include upstream TTFT.
	attemptStartedAt := time.Now()
	stream, errExecute := s.openTransparentModelStream(ctx, execution, callbackID, cfg)
	if errExecute != nil {
		if ctx.Err() != nil {
			return Result{}, QualityVerdict{}, errTransparentAttemptAborted
		}
		if errors.Is(errExecute, errTransparentStreamIdleTimeout) {
			verdict := QualityVerdict{Class: ClassProbeError, Reason: "reason.detail.upstream_stream_idle_timeout"}
			return Result{PreResponse: true}, verdict, errExecute
		}
		class, reason := classifyTransparentTransportFailure(errExecute)
		return Result{PreResponse: true}, QualityVerdict{Class: class, Reason: reason}, errExecute
	}
	var reader *modelStreamReader
	defer func() {
		_ = s.env.CloseModelStream(stream.StreamID)
		if reader != nil {
			reader.Close()
		}
	}()
	if stream.StatusCode < 200 || stream.StatusCode >= 300 {
		class, reason := classifyUpstreamStatus(stream.StatusCode, s.readModelStreamErrorBody(ctx, stream.StreamID))
		return Result{}, QualityVerdict{Class: class, Reason: reason}, fmt.Errorf("%s", reason)
	}
	analyzer := NewStreamAnalyzerWithProtocolAt(execution.ExitProtocol, attemptStartedAt)
	analyzer.SetRetainText(false)
	analyzer.SetEncryptedConfig(cfg.MinEncryptedBytes, cfg.EncryptedBytesPerReasoningToken)
	outputNormalizer := &sseOutputNormalizer{}
	chunks := make([]pluginapi.ExecutorStreamChunk, 0)
	var bufferBytes int64
	holdOverflow := false
	reader = s.openModelStreamReader(ctx, stream.StreamID)
	idleTimeout := transparentStreamIdleTimeout(cfg)
	idleDeadline := attemptStartedAt.Add(idleTimeout)
	for {
		select {
		case <-ctx.Done():
			return Result{}, QualityVerdict{}, errTransparentAttemptAborted
		default:
		}
		readCtx, cancelRead := context.WithDeadline(ctx, idleDeadline)
		read, errRead := reader.NextContext(readCtx)
		readDeadlineErr := readCtx.Err()
		cancelRead()
		if errRead != nil {
			if ctx.Err() != nil {
				return Result{}, QualityVerdict{}, errTransparentAttemptAborted
			}
			if errors.Is(readDeadlineErr, context.DeadlineExceeded) {
				verdict := QualityVerdict{Class: ClassProbeError, Reason: "reason.detail.upstream_stream_idle_timeout"}
				return Result{}, verdict, errTransparentStreamIdleTimeout
			}
			class, reason := classifyTransparentTransportFailure(errRead)
			return Result{}, QualityVerdict{Class: class, Reason: reason}, errRead
		}
		progressBefore := analyzer.Preview().ProgressEvents
		if len(read.Payload) > 0 {
			bufferBytes += int64(len(read.Payload))
			analyzer.ObserveChunk(read.Payload)
			for _, payload := range outputNormalizer.Feed(read.Payload) {
				chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: payload})
			}
			if bufferBytes > cfg.HoldMaxBufferBytes && !holdOverflow {
				holdOverflow = true
				s.recordTransparentOverflow(execution.AuthID, cfg.HoldMaxBufferBytes)
			}
		}
		if analyzer.Preview().ProgressEvents > progressBefore {
			idleDeadline = time.Now().Add(idleTimeout)
		}
		if read.Error != "" {
			if ctx.Err() != nil {
				return Result{}, QualityVerdict{}, errTransparentAttemptAborted
			}
			errRead := fmt.Errorf("%s", read.Error)
			class, reason := classifyTransparentTransportFailure(errRead)
			return Result{}, QualityVerdict{Class: class, Reason: reason}, errRead
		}
		if read.Done {
			break
		}
	}
	for _, payload := range outputNormalizer.Flush() {
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: payload})
	}
	sig := info.decorate(analyzer.Finalize(true))
	verdict := classifyFinal(sig, cfg, info.ReasoningNeeded, cfg.MinHoldOutputTokens, true)
	result := Result{Headers: stream.Headers, Chunks: chunks, Available: true}
	if holdOverflow {
		// The buffer limit bypassed the hold gate. Deliver the response but do
		// not turn an unverified sample into a healthy account observation.
		return result, QualityVerdict{}, nil
	}
	if verdict.Class == ClassNone {
		_ = s.env.ApplyVerdict(ctx, execution.AuthID, verdict, "transparent", callbackID)
		return result, verdict, nil
	}
	if cfg.ShadowMode {
		s.recordShadowVerdict(execution.AuthID, verdict)
		return result, verdict, nil
	}
	s.requestWithheldProbe(execution.AuthID, verdict)
	return result, verdict, fmt.Errorf("quality gate rejected stream: %s", verdict.Class)
}

func (s *Service) runTransparentStreamBridge(ctx context.Context, req Request, streamID string) {
	locale := executorRequestLocale(req.Headers)
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	ctx = runCtx
	defer func() {
		if recovered := recover(); recovered != nil {
			s.closePluginErrorStream(streamID, locale, "plugin_stream_panic", http.StatusInternalServerError)
		}
	}()
	policy := s.env.Policy()
	if !policy.TransparentGuard {
		s.closePluginErrorStream(streamID, locale, "transparent_guard_disabled", http.StatusServiceUnavailable)
		return
	}
	facts, errFacts := s.env.PrepareRequest(req)
	if errFacts != nil {
		s.closePluginErrorStream(streamID, locale, "invalid_request", http.StatusBadRequest)
		return
	}
	body := facts.Body
	sourceFormat := facts.SourceFormat
	requestModel := facts.Model
	replayable := facts.Replayable
	selection, errSelection := s.env.NewSelection(req, facts)
	if errSelection != nil {
		s.closePluginErrorStream(streamID, locale, "quality_degraded", http.StatusServiceUnavailable)
		return
	}
	// One withhold budget covers every attempt of this request, so a pool of
	// degraded accounts cannot hold the client past its own timeout.
	holdDeadline := time.Now().Add(policy.Quality.MaxHoldTotal.Duration)
	maxAttempts := transparentMaxAttempts(policy)
	attempts := 0
	last := StreamAttempt{}
candidateLoop:
	for {
		attempt, ok := selection.Next(ctx)
		if !ok {
			break
		}
		attemptID := attempt.Account.ID
		source := attempt.Source
		rotated := false
		account := attempt.Account
		raw := attempt.Raw
		for {
			if attempts >= maxAttempts {
				attempt.Release()
				break candidateLoop
			}
			if raw == nil {
				fetchedAccount, fetchedRaw, ok := s.env.ExecutableAuth(ctx, attemptID)
				if !ok {
					attempt.Release()
					s.env.RefreshSelectionAccount(attemptID)
					break
				}
				account, raw = fetchedAccount, fetchedRaw
			}
			attempts++
			execution := buildHostExecutionWithModel(req, body, sourceFormat, requestModel, account.ID, authRawString(raw, "proxy_url"), true)
			raw = nil
			info := s.transparentAttemptInfo(facts, account.ID)
			outcome := s.executeTransparentStreamAttempt(ctx, execution, req.HostCallbackID, policy.Quality, streamID, info, holdDeadline)
			if outcome.Aborted {
				attempt.Release()
				s.closePluginStream(streamID, "", "", 0)
				return
			}
			if outcome.Emitted {
				if outcome.Err != nil {
					selection.MarkFailure(account.ID, source, outcome.Class)
					attempt.Release()
					s.closePluginErrorStream(streamID, locale, "upstream_stream_error", http.StatusBadGateway)
					return
				}
				selection.MarkSuccess(account.ID, source)
				attempt.Release()
				s.closePluginStream(streamID, "", "", 0)
				return
			}
			if outcome.Err == nil {
				selection.MarkSuccess(account.ID, source)
				attempt.Release()
				s.closePluginStream(streamID, "", "", 0)
				return
			}
			if ctx.Err() != nil {
				attempt.Release()
				s.closePluginStream(streamID, "", "", 0)
				return
			}
			class := outcome.Class
			if outcome.Verdict.Class != "" {
				class = outcome.Verdict.Class
			}
			if outcome.Available {
				last = outcome
			}
			_ = s.recordAttemptError(account.ID, class, outcome.Err)
			selection.MarkFailure(account.ID, source, class)
			failureRetryable := attempts < maxAttempts && transparentFailureCanFailover(replayable, outcome.Emitted, outcome.PreResponse, class)
			if failureRetryable && !rotated && shouldRotateTransparentFailure(selection.HasRemainingCandidates(), class) {
				didRotate, errRotate := s.rotateAfterFailure(ctx, account.ID, class)
				if errRotate == nil && didRotate {
					rotated = true
					continue
				}
			}
			s.applyTransparentFailureVerdict(ctx, account.ID, outcome.Verdict, class, outcome.Err, req.HostCallbackID)
			attempt.Release()
			if !failureRetryable {
				break candidateLoop
			}
			break
		}
	}
	if policy.Quality.OnExhausted == "fail_open" && last.Available {
		for _, payload := range last.Buffered {
			if errEmit := s.emitPluginStreamChunk(streamID, payload); errEmit != nil {
				return
			}
		}
		s.closePluginStream(streamID, "", "", 0)
		return
	}
	s.closePluginErrorStream(streamID, locale, "quality_degraded", http.StatusServiceUnavailable)
}

func transparentHoldBlocks(cfg QualityConfig, reasoningNeeded bool, verdict QualityVerdict) bool {
	if verdict.Class == ClassMissingThinking && (!cfg.ThinkingGuard || !reasoningNeeded) {
		return false
	}
	return true
}

func (s *Service) executeTransparentStreamAttempt(ctx context.Context, execution pluginapi.HostModelExecutionRequest, callbackID string, cfg QualityConfig, streamID string, info attemptInfo, holdDeadline time.Time) StreamAttempt {
	reasoningNeeded := info.ReasoningNeeded
	// The host returns the stream only after the first chunk is readable, so the
	// stream-open and TPS clocks must start before the call to include upstream TTFT.
	attemptStartedAt := time.Now()
	stream, errExecute := s.openTransparentModelStream(ctx, execution, callbackID, cfg)
	if errExecute != nil {
		if ctx.Err() != nil {
			return StreamAttempt{Err: ctx.Err(), Aborted: true}
		}
		if errors.Is(errExecute, errTransparentStreamIdleTimeout) {
			verdict := QualityVerdict{Class: ClassProbeError, Reason: "reason.detail.upstream_stream_idle_timeout"}
			return StreamAttempt{Class: ClassProbeError, Verdict: verdict, Err: errExecute, PreResponse: true}
		}
		class, reason := classifyTransparentTransportFailure(errExecute)
		return StreamAttempt{Class: class, Verdict: QualityVerdict{Class: class, Reason: reason}, Err: errExecute, PreResponse: true}
	}
	attemptCtx, cancelAttempt := context.WithCancel(ctx)
	if stream.StatusCode < 200 || stream.StatusCode >= 300 {
		class, reason := classifyUpstreamStatus(stream.StatusCode, s.readModelStreamErrorBody(attemptCtx, stream.StreamID))
		cancelAttempt()
		_ = s.env.CloseModelStream(stream.StreamID)
		return StreamAttempt{Class: class, Verdict: QualityVerdict{Class: class, Reason: reason}, Err: fmt.Errorf("%s", reason)}
	}
	analyzer := NewStreamAnalyzerWithProtocolAt(execution.ExitProtocol, attemptStartedAt)
	analyzer.SetRetainText(false)
	analyzer.SetEncryptedConfig(cfg.MinEncryptedBytes, cfg.EncryptedBytesPerReasoningToken)
	readEvents := make(chan streamReadEvent, 1)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer close(readEvents)
		for {
			read, errRead := s.env.ReadModelStream(attemptCtx, stream.StreamID)
			event := streamReadEvent{Response: read, Err: errRead}
			select {
			case readEvents <- event:
			case <-attemptCtx.Done():
				return
			}
			if errRead != nil || read.Done {
				return
			}
		}
	}()
	streamClosed := false
	closeUpstream := func() {
		if streamClosed {
			return
		}
		streamClosed = true
		_ = s.env.CloseModelStream(stream.StreamID)
	}
	defer func() {
		cancelAttempt()
		closeUpstream()
		select {
		case <-readDone:
		case <-time.After(modelStreamCloseGrace):
		}
	}()

	// The visible hold starts with the first visible chunk, so time spent
	// thinking cannot consume the window before the answer even begins.
	holdLimit := transparentHoldLimit(cfg, holdDeadline)
	var holdTimer *time.Timer
	var holdC <-chan time.Time
	defer func() {
		if holdTimer != nil {
			holdTimer.Stop()
		}
	}()
	streamIdleTimeout := transparentStreamIdleTimeout(cfg)
	streamIdleTimer := time.NewTimer(time.Until(attemptStartedAt.Add(streamIdleTimeout)))
	defer streamIdleTimer.Stop()
	resetStreamIdle := func() {
		if !streamIdleTimer.Stop() {
			select {
			case <-streamIdleTimer.C:
			default:
			}
		}
		streamIdleTimer.Reset(streamIdleTimeout)
	}
	holdExpired := false
	emitted := false
	overflowed := false
	buffer := make([][]byte, 0)
	var bufferBytes int64
	outputNormalizer := &sseOutputNormalizer{}
	appendOutput := func(payloads [][]byte) {
		for _, payload := range payloads {
			if len(payload) == 0 {
				continue
			}
			buffer = append(buffer, payload)
			bufferBytes += int64(len(payload))
		}
	}
	emitBuffered := func() error {
		if len(buffer) == 0 {
			emitted = true
			return nil
		}
		if errEmit := s.emitPluginChunks(streamID, buffer); errEmit != nil {
			return errEmit
		}
		emitted = true
		buffer = nil
		bufferBytes = 0
		return nil
	}
	abort := func(errValue error) StreamAttempt {
		return StreamAttempt{Err: errValue, Aborted: true, Emitted: emitted}
	}
	finish := func() StreamAttempt {
		appendOutput(outputNormalizer.Flush())
		if emitted && len(buffer) > 0 {
			if errEmit := s.emitPluginChunks(streamID, buffer); errEmit != nil {
				return abort(errEmit)
			}
			buffer = nil
			bufferBytes = 0
		}
		return s.finishTransparentStreamAttempt(
			ctx, execution, callbackID, cfg, streamID, analyzer, buffer, emitted, holdExpired, overflowed, info,
		)
	}
	var earlyTimer *time.Timer
	var earlyC <-chan time.Time
	earlyArmed := false
	defer func() {
		if earlyTimer != nil {
			earlyTimer.Stop()
		}
	}()
	armEarly := func() {
		if reasoningNeeded || emitted || earlyArmed {
			return
		}
		if !analyzer.Preview().FirstVisible {
			return
		}
		earlyTimer = time.NewTimer(200 * time.Millisecond)
		earlyC = earlyTimer.C
		earlyArmed = true
	}

	keepaliveEvery := cfg.HoldKeepalive.Duration
	var keepaliveC <-chan time.Time
	if keepaliveEvery > 0 {
		keepaliveTicker := time.NewTicker(keepaliveEvery)
		defer keepaliveTicker.Stop()
		keepaliveC = keepaliveTicker.C
	}
	armHold := func() {
		if holdTimer != nil || emitted || holdExpired || !reasoningNeeded {
			return
		}
		holdTimer = time.NewTimer(holdLimit)
		holdC = holdTimer.C
	}

	for {
		select {
		case <-ctx.Done():
			return abort(ctx.Err())
		case <-keepaliveC:
			// Keep the client connection alive while the answer is held. The
			// comment frame carries no content and does not end the hold.
			if emitted {
				continue
			}
			if errEmit := s.emitPluginStreamChunk(streamID, []byte(": quality-hold\n\n")); errEmit != nil {
				return abort(errEmit)
			}
		case <-earlyC:
			earlyC = nil
			earlyArmed = false
			if !emitted {
				if errEmit := emitBuffered(); errEmit != nil {
					return abort(errEmit)
				}
			}
		case <-streamIdleTimer.C:
			if ctx.Err() != nil {
				return abort(ctx.Err())
			}
			cancelAttempt()
			closeUpstream()
			verdict := QualityVerdict{Class: ClassProbeError, Reason: "reason.detail.upstream_stream_idle_timeout"}
			return StreamAttempt{Class: ClassProbeError, Verdict: verdict, Err: errTransparentStreamIdleTimeout, Emitted: emitted}
		case <-holdC:
			holdC = nil
			holdExpired = true
			preview := info.decorate(analyzer.Preview())
			preview.HoldExpired = true
			verdict := ClassifyQualityHold(preview, cfg.MinHoldOutputTokens)
			if verdict.Action == QualityActionWithhold {
				if cfg.ShadowMode {
					s.recordShadowVerdict(execution.AuthID, verdict)
					if errEmit := emitBuffered(); errEmit != nil {
						return abort(errEmit)
					}
					continue
				}
				if transparentHoldBlocks(cfg, reasoningNeeded, verdict) {
					s.requestWithheldProbe(execution.AuthID, verdict)
					return StreamAttempt{Class: verdict.Class, Verdict: verdict, Err: fmt.Errorf("quality gate rejected stream: %s", verdict.Class), Buffered: buffer, Available: true}
				}
				if errEmit := emitBuffered(); errEmit != nil {
					return abort(errEmit)
				}
				continue
			}
			if verdict.Action == QualityActionDeliver {
				if errEmit := emitBuffered(); errEmit != nil {
					return abort(errEmit)
				}
			}
		case event, ok := <-readEvents:
			if !ok {
				return finish()
			}
			if event.Err != nil {
				class, reason := classifyTransparentTransportFailure(event.Err)
				verdict := QualityVerdict{Class: class, Reason: reason}
				if emitted {
					return StreamAttempt{Class: class, Verdict: verdict, Err: event.Err, Emitted: true}
				}
				return StreamAttempt{Class: class, Verdict: verdict, Err: event.Err}
			}
			if len(event.Response.Payload) > 0 {
				progressBefore := analyzer.Preview().ProgressEvents
				payload := append([]byte(nil), event.Response.Payload...)
				framedPayloads := outputNormalizer.Feed(payload)
				if emitted {
					if errEmit := s.emitPluginChunks(streamID, framedPayloads); errEmit != nil {
						return abort(errEmit)
					}
				} else {
					appendOutput(framedPayloads)
					if bufferBytes > cfg.HoldMaxBufferBytes {
						if errEmit := emitBuffered(); errEmit != nil {
							return abort(errEmit)
						}
						overflowed = true
						s.recordTransparentOverflow(execution.AuthID, cfg.HoldMaxBufferBytes)
					}
				}
				analyzer.ObserveChunk(payload)
				previewNow := analyzer.Preview()
				if previewNow.ProgressEvents > progressBefore {
					resetStreamIdle()
				}
				if previewNow.FirstVisible {
					armHold()
				}
				if previewNow.Terminal {
					return finish()
				}
			}
			if event.Response.Error != "" {
				errRead := fmt.Errorf("%s", event.Response.Error)
				class, reason := classifyTransparentTransportFailure(errRead)
				verdict := QualityVerdict{Class: class, Reason: reason}
				if emitted {
					return StreamAttempt{Class: class, Verdict: verdict, Err: errRead, Emitted: true}
				}
				return StreamAttempt{Class: class, Verdict: verdict, Err: errRead}
			}
			if event.Response.Done {
				return finish()
			}
			if emitted {
				continue
			}
			if !reasoningNeeded {
				armEarly()
				continue
			}
			preview := info.decorate(analyzer.Preview())
			preview.HoldExpired = holdExpired
			holdVerdict := ClassifyQualityHold(preview, cfg.MinHoldOutputTokens)
			if holdVerdict.Action == QualityActionWithhold {
				if cfg.ShadowMode {
					s.recordShadowVerdict(execution.AuthID, holdVerdict)
					if errEmit := emitBuffered(); errEmit != nil {
						return abort(errEmit)
					}
					continue
				}
				if transparentHoldBlocks(cfg, reasoningNeeded, holdVerdict) {
					s.requestWithheldProbe(execution.AuthID, holdVerdict)
					return StreamAttempt{Class: holdVerdict.Class, Verdict: holdVerdict, Err: fmt.Errorf("quality gate rejected stream: %s", holdVerdict.Class), Buffered: buffer, Available: true}
				}
				if errEmit := emitBuffered(); errEmit != nil {
					return abort(errEmit)
				}
				continue
			}
			if holdVerdict.Action == QualityActionDeliver {
				if errEmit := emitBuffered(); errEmit != nil {
					return abort(errEmit)
				}
			}
		}
	}
}

func (s *Service) finishTransparentStreamAttempt(
	ctx context.Context,
	execution pluginapi.HostModelExecutionRequest,
	callbackID string,
	cfg QualityConfig,
	streamID string,
	analyzer *StreamAnalyzer,
	buffer [][]byte,
	emitted bool,
	holdExpired bool,
	holdOverflow bool,
	info attemptInfo,
) StreamAttempt {
	sig := info.decorate(analyzer.Finalize(true))
	sig.HoldExpired = holdExpired
	if holdOverflow {
		if !emitted && len(buffer) > 0 {
			if errEmit := s.emitPluginChunks(streamID, buffer); errEmit != nil {
				return StreamAttempt{Err: errEmit, Aborted: true}
			}
		}
		return StreamAttempt{Class: ClassNone, Emitted: true}
	}
	verdict := classifyFinal(sig, cfg, info.ReasoningNeeded, cfg.MinHoldOutputTokens, true)
	if cfg.ShadowMode && verdict.Class != ClassNone {
		// Shadow mode never withholds or penalizes: record the verdict, then
		// release whatever the gate would have held back.
		s.recordShadowVerdict(execution.AuthID, verdict)
		if !emitted {
			if errEmit := s.emitPluginChunks(streamID, buffer); errEmit != nil {
				return StreamAttempt{Err: errEmit, Aborted: true, Available: true}
			}
		}
		return StreamAttempt{Class: verdict.Class, Verdict: verdict, Emitted: true}
	}
	if emitted {
		_ = s.env.ApplyVerdict(ctx, execution.AuthID, verdict, "transparent", callbackID)
		return StreamAttempt{Class: verdict.Class, Verdict: verdict, Emitted: true}
	}
	if verdict.Class == ClassNone {
		if errEmit := s.emitPluginChunks(streamID, buffer); errEmit != nil {
			return StreamAttempt{Err: errEmit, Aborted: true, Available: true}
		}
		_ = s.env.ApplyVerdict(ctx, execution.AuthID, verdict, "transparent", callbackID)
		return StreamAttempt{Emitted: true}
	}
	s.requestWithheldProbe(execution.AuthID, verdict)
	return StreamAttempt{Class: verdict.Class, Verdict: verdict, Err: fmt.Errorf("quality gate rejected stream: %s", verdict.Class), Buffered: buffer, Available: true}
}
