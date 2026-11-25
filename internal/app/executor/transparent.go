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
var errTransparentStreamIncomplete = errors.New("upstream stream ended without a terminal event")
var errTransparentAttemptTimeout = errors.New("transparent attempt exceeded attempt_timeout")
var errTransparentBudgetExhausted = errors.New("transparent verification budget exhausted")

// startTransparentStreamBridge ties the request-lifetime goroutine to the
// plugin lifecycle so Quiesce/Shutdown can cancel and wait for it. The request's
// own context is cancelled by the host completion callback: the host detaches
// cancellation from the model stream it owns, so a client disconnect is only
// observable through request.complete.
func (s *Service) startTransparentStreamBridge(req Request, streamID string) {
	locale := executorRequestLocale(req.Headers)
	ctx, quiescing := s.env.RequestContext()
	if quiescing || !s.env.TaskStarted() {
		s.closePluginErrorStream(streamID, locale, "plugin_shutting_down", http.StatusServiceUnavailable)
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		defer s.env.TaskDone()
		s.runTransparentStreamBridge(ctx, req, streamID)
	}()
}

func (s *Service) transparentExecute(ctx context.Context, req Request) (Result, error) {
	policy := s.env.Policy()
	if !policy.TransparentGuardEnabled() {
		return Result{}, fmt.Errorf("transparent guard is disabled")
	}
	facts, errFacts := s.env.PrepareRequest(req)
	if errFacts != nil {
		return Result{}, errFacts
	}
	selection, errSelection := s.env.NewSelection(req, facts)
	if errSelection != nil {
		return Result{}, errSelection
	}
	run := s.runTransparentAttempts(ctx, req, facts, policy, selection, false, time.Time{}, func(execution pluginapi.HostModelExecutionRequest, info attemptInfo) transparentAttemptOutcome {
		result, verdict, errAttempt := s.executeTransparentOnce(ctx, execution, req.HostCallbackID, policy.Quality, info)
		return transparentAttemptOutcome{
			Class:       verdict.Class,
			Verdict:     verdict,
			Err:         errAttempt,
			PreResponse: result.PreResponse,
			Delivered:   errAttempt == nil,
			Result:      result,
			Available:   result.Available,
			Signals:     result.Signals,
		}
	})
	switch run.Status {
	case transparentRunDelivered:
		return run.Final.Result, nil
	case transparentRunAborted:
		return Result{}, errTransparentAttemptAborted
	default:
		return Result{}, fmt.Errorf("no healthy Grok account produced a valid response")
	}
}

func (s *Service) transparentExecuteBufferedStream(ctx context.Context, req Request) (Result, error) {
	policy := s.env.Policy()
	if !policy.TransparentGuardEnabled() {
		return Result{}, fmt.Errorf("transparent guard is disabled")
	}
	facts, errFacts := s.env.PrepareRequest(req)
	if errFacts != nil {
		return Result{}, errFacts
	}
	selection, errSelection := s.env.NewSelection(req, facts)
	if errSelection != nil {
		return Result{}, errSelection
	}
	run := s.runTransparentAttempts(ctx, req, facts, policy, selection, true, time.Time{}, func(execution pluginapi.HostModelExecutionRequest, info attemptInfo) transparentAttemptOutcome {
		result, verdict, errAttempt := s.executeTransparentBufferedAttempt(ctx, execution, req.HostCallbackID, policy.Quality, info)
		return transparentAttemptOutcome{
			Class:       verdict.Class,
			Verdict:     verdict,
			Err:         errAttempt,
			PreResponse: result.PreResponse,
			Delivered:   errAttempt == nil,
			Result:      result,
			Available:   result.Available,
			Signals:     result.Signals,
		}
	})
	switch run.Status {
	case transparentRunDelivered:
		return run.Final.Result, nil
	case transparentRunAborted:
		return Result{}, errTransparentAttemptAborted
	default:
		return Result{}, fmt.Errorf("no healthy Grok account produced a valid response")
	}
}

func (s *Service) executeTransparentOnce(ctx context.Context, execution pluginapi.HostModelExecutionRequest, callbackID string, cfg QualityConfig, info attemptInfo) (Result, QualityVerdict, error) {
	execution.Stream = false
	if ctx.Err() != nil {
		return Result{}, QualityVerdict{}, errTransparentAttemptAborted
	}
	startedAt := time.Now()
	response, errExecute := s.env.ExecuteModel(ctx, execution, callbackID)
	if errExecute != nil {
		if isTransparentCancelError(ctx, errExecute) {
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
	result := Result{Payload: response.Body, Headers: response.Headers, Available: true, Signals: sig}
	if verdict.Class == ClassNone {
		s.settleTransparentVerdict(execution.AuthID, verdict, callbackID)
		return result, verdict, nil
	}
	if cfg.ShadowMode {
		s.recordShadowVerdict(execution.AuthID, verdict)
		return result, verdict, nil
	}
	return result, verdict, fmt.Errorf("quality gate rejected response: %s", verdict.Class)
}

func (s *Service) openTransparentModelStream(ctx context.Context, execution pluginapi.HostModelExecutionRequest, callbackID string, cfg QualityConfig) (pluginapi.HostModelStreamResponse, error) {
	openCtx, cancel := context.WithTimeout(ctx, transparentStreamIdleTimeout(cfg))
	defer cancel()
	stream, errExecute := s.env.ExecuteModelStream(openCtx, execution, callbackID)
	if errExecute != nil {
		if isTransparentCancelError(ctx, errExecute) {
			if ctx != nil && ctx.Err() != nil {
				return pluginapi.HostModelStreamResponse{}, ctx.Err()
			}
			return pluginapi.HostModelStreamResponse{}, errTransparentRequestCanceled
		}
		if errors.Is(openCtx.Err(), context.DeadlineExceeded) {
			return pluginapi.HostModelStreamResponse{}, errTransparentStreamIdleTimeout
		}
		return pluginapi.HostModelStreamResponse{}, errExecute
	}
	if ctx != nil && ctx.Err() != nil || errors.Is(openCtx.Err(), context.DeadlineExceeded) {
		if stream.StreamID != "" {
			_ = s.env.CloseModelStream(stream.StreamID)
		}
		if ctx != nil && ctx.Err() != nil {
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
	// attempt_timeout bounds one attempt end to end, covering the wait for the
	// first token as well as the hold. The idle window bounds silence only, so
	// the two are enforced separately.
	attemptDeadline := attemptStartedAt.Add(transparentAttemptTimeout(cfg))
	stream, errExecute := s.openTransparentModelStream(ctx, execution, callbackID, cfg)
	if errExecute != nil {
		if isTransparentCancelError(ctx, errExecute) {
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
	// The idle window measures upstream silence, so it starts now that the
	// stream is open and restarts on every chunk. Control frames such as
	// response.created prove the upstream is working even though they carry
	// no verdict-relevant signal.
	idleTimeout := transparentStreamIdleTimeout(cfg)
	idleDeadline := time.Now().Add(idleTimeout)
	if attemptDeadline.Before(idleDeadline) {
		idleDeadline = attemptDeadline
	}
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
			if isTransparentCancelError(ctx, errRead) {
				return Result{}, QualityVerdict{}, errTransparentAttemptAborted
			}
			if errors.Is(readDeadlineErr, context.DeadlineExceeded) {
				verdict := QualityVerdict{Class: ClassProbeError, Reason: "reason.detail.upstream_stream_idle_timeout"}
				return Result{}, verdict, errTransparentStreamIdleTimeout
			}
			class, reason := classifyTransparentTransportFailure(errRead)
			return Result{}, QualityVerdict{Class: class, Reason: reason}, errRead
		}
		parsedBefore := analyzer.ParsedEventCount()
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
		if analyzer.ParsedEventCount() > parsedBefore {
			idleDeadline = time.Now().Add(idleTimeout)
		}
		if !time.Now().Before(attemptDeadline) {
			// One account exceeded its attempt budget. That is a transport
			// outcome, not a quality one: switch accounts and leave the
			// account state untouched.
			verdict := QualityVerdict{Class: ClassProbeError, Reason: "reason.detail.upstream_attempt_timeout"}
			return Result{PreResponse: true}, verdict, errTransparentAttemptTimeout
		}
		if read.Error != "" {
			errRead := fmt.Errorf("%s", read.Error)
			if isTransparentCancelError(ctx, errRead) {
				return Result{}, QualityVerdict{}, errTransparentAttemptAborted
			}
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
	// A closed channel is not proof the upstream finished. Only a terminal
	// event makes the body complete; without one the stream was truncated and
	// its partial content must not become a quality verdict. An overflowed
	// buffer already bypassed the gate and is delivered unverified instead.
	if !holdOverflow && !analyzer.Preview().Terminal {
		verdict := QualityVerdict{Class: ClassProbeError, Reason: "reason.detail.upstream_stream_incomplete"}
		return Result{}, verdict, fmt.Errorf("upstream stream ended without a terminal event")
	}
	sig := info.decorate(analyzer.Finalize(analyzer.Preview().Terminal))
	verdict := classifyFinal(sig, cfg, info.ReasoningNeeded, cfg.MinHoldOutputTokens, true)
	result := Result{Headers: stream.Headers, Chunks: chunks, Available: true, Signals: sig}
	if holdOverflow {
		// The buffer limit bypassed the hold gate. Deliver the response but do
		// not turn an unverified sample into a healthy account observation.
		return result, QualityVerdict{}, nil
	}
	if verdict.Class == ClassNone {
		s.settleTransparentVerdict(execution.AuthID, verdict, callbackID)
		return result, verdict, nil
	}
	if cfg.ShadowMode {
		s.recordShadowVerdict(execution.AuthID, verdict)
		return result, verdict, nil
	}
	return result, verdict, fmt.Errorf("quality gate rejected stream: %s", verdict.Class)
}

func (s *Service) runTransparentStreamBridge(ctx context.Context, req Request, streamID string) {
	locale := executorRequestLocale(req.Headers)
	runCtx, cancelRun := context.WithTimeout(ctx, 10*time.Minute)
	defer cancelRun()
	// The host detaches client cancellation from the model stream it owns, so
	// the request-scoped context is only stopped by request.complete. Register
	// it here, at the single point every streaming entry funnels through.
	requestID := requestIDFromMetadata(req.Metadata)
	ctx, cancelRequest := context.WithCancel(runCtx)
	if s.RegisterRequestCancel(requestID, cancelRequest) {
		defer s.ReleaseRequestCancel(requestID)
	} else {
		defer cancelRequest()
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			s.closePluginErrorStream(streamID, locale, "plugin_stream_panic", http.StatusInternalServerError)
		}
	}()
	policy := s.env.Policy()
	if !policy.TransparentGuardEnabled() {
		s.closePluginErrorStream(streamID, locale, "transparent_guard_disabled", http.StatusServiceUnavailable)
		return
	}
	facts, errFacts := s.env.PrepareRequest(req)
	if errFacts != nil {
		s.closePluginErrorStream(streamID, locale, "invalid_request", http.StatusBadRequest)
		return
	}
	selection, errSelection := s.env.NewSelection(req, facts)
	if errSelection != nil {
		s.closePluginErrorStream(streamID, locale, "quality_degraded", http.StatusServiceUnavailable)
		return
	}
	// One withhold budget covers every attempt of this request, so a pool of
	// degraded accounts cannot hold the client past its own timeout.
	holdDeadline := time.Now().Add(transparentRequestTimeout(policy.Quality))
	run := s.runTransparentAttempts(ctx, req, facts, policy, selection, true, holdDeadline, func(execution pluginapi.HostModelExecutionRequest, info attemptInfo) transparentAttemptOutcome {
		outcome := s.executeTransparentStreamAttempt(ctx, execution, req.HostCallbackID, policy.Quality, streamID, info, holdDeadline)
		return transparentAttemptOutcome{
			Class:       outcome.Class,
			Verdict:     outcome.Verdict,
			Err:         outcome.Err,
			PreResponse: outcome.PreResponse,
			Emitted:     outcome.Emitted,
			Aborted:     outcome.Aborted,
			Buffered:    outcome.Buffered,
			Available:   outcome.Available,
			Signals:     outcome.Signals,
		}
	})
	switch run.Status {
	case transparentRunDelivered, transparentRunEmitted:
		if run.Final.Err != nil {
			s.closePluginErrorStream(streamID, locale, "upstream_stream_error", http.StatusBadGateway)
			return
		}
		s.closePluginStream(streamID, "", "", 0)
		return
	case transparentRunAborted:
		s.closePluginStream(streamID, "", "", 0)
		return
	}
	last := run.Last
	if policy.Quality.ExhaustedDelivers() && last.Available {
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
	// attempt_timeout caps one attempt end to end; the idle window and the
	// request budget each clip shorter windows inside it.
	attemptDeadline := attemptStartedAt.Add(transparentAttemptTimeout(cfg))
	holdLimit, holdOK := transparentHoldLimit(cfg, holdDeadline)
	if !holdOK {
		return StreamAttempt{Class: ClassInconclusive, Verdict: QualityVerdict{Class: ClassInconclusive, Reason: "reason.detail.budget_exhausted"}, Err: errTransparentBudgetExhausted, PreResponse: true}
	}
	budgetClipped := holdLimit < cfg.HoldTimeout.Duration
	stream, errExecute := s.openTransparentModelStream(ctx, execution, callbackID, cfg)
	if errExecute != nil {
		if isTransparentCancelError(ctx, errExecute) {
			return StreamAttempt{Err: errTransparentAttemptAborted, Aborted: true}
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
	var holdTimer *time.Timer
	var holdC <-chan time.Time
	defer func() {
		if holdTimer != nil {
			holdTimer.Stop()
		}
	}()
	// The idle window bounds upstream silence, not the whole attempt. It starts
	// once the stream is open and restarts on every chunk, so time already
	// spent waiting for the first token cannot shorten it.
	streamIdleTimeout := transparentStreamIdleTimeout(cfg)
	streamIdleTimer := time.NewTimer(time.Until(earlierDeadline(attemptDeadline, time.Now().Add(streamIdleTimeout))))
	defer streamIdleTimer.Stop()
	resetStreamIdle := func() {
		if !streamIdleTimer.Stop() {
			select {
			case <-streamIdleTimer.C:
			default:
			}
		}
		streamIdleTimer.Reset(time.Until(earlierDeadline(attemptDeadline, time.Now().Add(streamIdleTimeout))))
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
			execution, callbackID, cfg, streamID, analyzer, buffer, emitted, holdExpired, overflowed, info,
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
	armEarly := func(firstVisible bool) {
		if reasoningNeeded || emitted || earlyArmed || !firstVisible {
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
	classifyHold := func(preview QualitySignals) QualityVerdict {
		preview = info.decorate(preview)
		preview.HoldExpired = holdExpired
		return ClassifyQualityHold(preview, cfg.MinHoldOutputTokens)
	}
	// applyHold acts on a hold verdict and reports whether the attempt ends.
	applyHold := func(verdict QualityVerdict) (StreamAttempt, bool) {
		switch verdict.Action {
		case QualityActionWithhold:
			if budgetClipped && verdict.Class == ClassMissingThinking {
				return StreamAttempt{Class: ClassInconclusive, Verdict: QualityVerdict{Class: ClassInconclusive, Reason: "reason.detail.budget_exhausted"}, Err: errTransparentBudgetExhausted, Buffered: buffer, Available: true}, true
			}
			if cfg.ShadowMode {
				s.recordShadowVerdict(execution.AuthID, verdict)
			} else if transparentHoldBlocks(cfg, reasoningNeeded, verdict) {
				return StreamAttempt{Class: verdict.Class, Verdict: verdict, Err: fmt.Errorf("quality gate rejected stream: %s", verdict.Class), Buffered: buffer, Available: true}, true
			}
		case QualityActionDeliver:
		default:
			return StreamAttempt{}, false
		}
		if errEmit := emitBuffered(); errEmit != nil {
			return abort(errEmit), true
		}
		return StreamAttempt{}, false
	}

	for {
		select {
		case <-ctx.Done():
			return abort(errTransparentAttemptAborted)
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
				return abort(errTransparentAttemptAborted)
			}
			cancelAttempt()
			closeUpstream()
			class, reason := ClassProbeError, "reason.detail.upstream_stream_idle_timeout"
			errValue := errTransparentStreamIdleTimeout
			if !time.Now().Before(attemptDeadline) {
				reason = "reason.detail.upstream_attempt_timeout"
				errValue = errTransparentAttemptTimeout
			}
			verdict := QualityVerdict{Class: class, Reason: reason}
			return StreamAttempt{Class: class, Verdict: verdict, Err: errValue, Emitted: emitted}
		case <-holdC:
			holdC = nil
			holdExpired = true
			if emitted {
				// Content already reached the client; a late verdict must not
				// fail over and splice another account's answer onto it.
				continue
			}
			if attempt, done := applyHold(classifyHold(analyzer.Preview())); done {
				return attempt
			}
		case event, ok := <-readEvents:
			if !ok {
				if ctx.Err() != nil {
					return abort(errTransparentAttemptAborted)
				}
				if !analyzer.Preview().Terminal {
					verdict := QualityVerdict{Class: ClassProbeError, Reason: "reason.detail.upstream_stream_incomplete"}
					return StreamAttempt{Class: ClassProbeError, Verdict: verdict, Err: errTransparentStreamIncomplete, Emitted: emitted}
				}
				return finish()
			}
			if event.Err != nil {
				if isTransparentCancelError(ctx, event.Err) {
					return abort(errTransparentAttemptAborted)
				}
				class, reason := classifyTransparentTransportFailure(event.Err)
				verdict := QualityVerdict{Class: class, Reason: reason}
				if emitted {
					return StreamAttempt{Class: class, Verdict: verdict, Err: event.Err, Emitted: true}
				}
				return StreamAttempt{Class: class, Verdict: verdict, Err: event.Err}
			}
			// The hold verdict can only move when a chunk changes a signal it
			// depends on. Control frames, keepalive comments and empty item
			// announcements are buffered without reclassifying the stream.
			signalChanged := false
			var preview QualitySignals
			if len(event.Response.Payload) > 0 {
				// A parsed upstream event proves the upstream is working even when
				// it carries no verdict-relevant signal. Comment stubs and
				// unparseable bytes do not count, so a stream that only repeats
				// them still trips the idle timeout.
				parsedBefore := analyzer.ParsedEventCount()
				revisionBefore := analyzer.Revision()
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
				if analyzer.ParsedEventCount() > parsedBefore {
					resetStreamIdle()
				}
				if analyzer.Revision() != revisionBefore {
					signalChanged = true
					preview = analyzer.Preview()
					if preview.FirstVisible {
						armHold()
					}
					if preview.Terminal {
						return finish()
					}
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
				if !overflowed && !analyzer.Preview().Terminal {
					// The channel closed without a terminal event, so the upstream
					// body is truncated. Its partial content must not become quality
					// evidence: report it as transport and let the caller retry.
					verdict := QualityVerdict{Class: ClassProbeError, Reason: "reason.detail.upstream_stream_incomplete"}
					return StreamAttempt{Class: ClassProbeError, Verdict: verdict, Err: errTransparentStreamIncomplete, Emitted: emitted}
				}
				return finish()
			}
			if emitted || !signalChanged {
				continue
			}
			if !reasoningNeeded {
				armEarly(preview.FirstVisible)
				continue
			}
			if attempt, done := applyHold(classifyHold(preview)); done {
				attempt.Signals = preview
				return attempt
			}
		}
	}
}

func (s *Service) finishTransparentStreamAttempt(
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
		return StreamAttempt{Class: ClassNone, Emitted: true, Signals: sig}
	}
	verdict := classifyFinal(sig, cfg, info.ReasoningNeeded, cfg.MinHoldOutputTokens, true)
	if cfg.ShadowMode && verdict.Class != ClassNone {
		// Shadow mode never withholds or penalizes: record the verdict, then
		// release whatever the gate would have held back.
		s.recordShadowVerdict(execution.AuthID, verdict)
		if !emitted {
			if errEmit := s.emitPluginChunks(streamID, buffer); errEmit != nil {
				return StreamAttempt{Err: errEmit, Aborted: true, Available: true, Signals: sig}
			}
		}
		return StreamAttempt{Class: verdict.Class, Verdict: verdict, Emitted: true, Signals: sig}
	}
	if emitted {
		s.settleTransparentVerdict(execution.AuthID, verdict, callbackID)
		return StreamAttempt{Class: verdict.Class, Verdict: verdict, Emitted: true, Signals: sig}
	}
	if verdict.Class == ClassNone {
		if errEmit := s.emitPluginChunks(streamID, buffer); errEmit != nil {
			return StreamAttempt{Err: errEmit, Aborted: true, Available: true, Signals: sig}
		}
		s.settleTransparentVerdict(execution.AuthID, verdict, callbackID)
		return StreamAttempt{Emitted: true, Signals: sig}
	}
	return StreamAttempt{Class: verdict.Class, Verdict: verdict, Err: fmt.Errorf("quality gate rejected stream: %s", verdict.Class), Buffered: buffer, Available: true, Signals: sig}
}
