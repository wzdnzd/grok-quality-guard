package executor

import (
	"context"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// transparentAttemptOutcome normalizes one upstream attempt across the
// non-stream, buffered-stream and live-stream entry points. Keeping the shape
// identical lets a single runner own budget accounting, account rotation and
// verdict settlement instead of three copies drifting apart.
type transparentAttemptOutcome struct {
	Class       string
	Verdict     QualityVerdict
	Err         error
	PreResponse bool
	Emitted     bool
	Aborted     bool
	// Signals keeps the analysed counters so the optional diagnostic event can
	// explain why the class was chosen.
	Signals QualitySignals
	// Delivered marks a complete response the caller should return as-is.
	Delivered bool
	Result    Result
	// Buffered keeps a withheld stream so the exhaustion policy can release it.
	Buffered  [][]byte
	Available bool
}

type transparentRunStatus int

const (
	// transparentRunExhausted means no candidate produced a deliverable
	// response; the caller applies the configured exhaustion policy.
	transparentRunExhausted transparentRunStatus = iota
	// transparentRunDelivered means an attempt returned a complete result.
	transparentRunDelivered
	// transparentRunEmitted means content already reached the client, so the
	// request must end without failover even if the attempt then failed.
	transparentRunEmitted
	// transparentRunAborted means the client went away mid-attempt.
	transparentRunAborted
)

type transparentRun struct {
	Status transparentRunStatus
	Final  transparentAttemptOutcome
	// Last is the most recent withheld attempt that holds buffered content.
	Last transparentAttemptOutcome
}

// transparentAttemptFunc runs exactly one attempt against the given execution.
type transparentAttemptFunc func(execution pluginapi.HostModelExecutionRequest, info attemptInfo) transparentAttemptOutcome

// runTransparentAttempts walks the selection once for a whole request. The
// deadline, when non-zero, is the shared request budget; a hold that cannot
// last minVerifyWindow stops the request instead of misjudging the stream.
func (s *Service) runTransparentAttempts(
	ctx context.Context,
	req Request,
	facts Facts,
	policy Policy,
	selection Selection,
	stream bool,
	deadline time.Time,
	attemptFn transparentAttemptFunc,
) transparentRun {
	maxAttempts := transparentMaxAttempts(policy)
	attempts := 0
	run := transparentRun{Status: transparentRunExhausted}
candidateLoop:
	for {
		if !deadline.IsZero() && time.Until(deadline) < minVerifyWindow(policy.Quality) {
			break
		}
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
			execution := buildHostExecutionWithModel(req, facts.Body, facts.SourceFormat, facts.Model, account.ID, authRawString(raw, "proxy_url"), stream)
			raw = nil
			info := s.transparentAttemptInfo(facts, account.ID)
			outcome := attemptFn(execution, info)

			switch {
			case outcome.Aborted:
				attempt.Release()
				run.Status, run.Final = transparentRunAborted, outcome
				return run
			case outcome.Class == ClassInconclusive:
				if outcome.Available {
					run.Last = outcome
				}
				// Keep the reason so the caller can tell a thinking timeout from
				// an exhausted verification budget.
				run.Final = outcome
				attempt.Release()
				break candidateLoop
			case outcome.Delivered:
				s.recordDiagnosticVerdict(account.ID, source, policy.Quality, outcome.Verdict, outcome.Signals, map[string]any{
					"attempt": attempts,
					"max":     maxAttempts,
				})
				selection.MarkSuccess(account.ID, source)
				attempt.Release()
				run.Status, run.Final = transparentRunDelivered, outcome
				return run
			case outcome.Emitted:
				if outcome.Err != nil {
					selection.MarkFailure(account.ID, source, outcome.Class)
				} else {
					selection.MarkSuccess(account.ID, source)
				}
				attempt.Release()
				run.Status, run.Final = transparentRunEmitted, outcome
				return run
			case outcome.Err == nil:
				selection.MarkSuccess(account.ID, source)
				attempt.Release()
				run.Status, run.Final = transparentRunEmitted, outcome
				return run
			}
			if ctx.Err() != nil {
				attempt.Release()
				run.Status, run.Final = transparentRunAborted, outcome
				return run
			}
			class := outcome.Class
			if outcome.Verdict.Class != "" {
				class = outcome.Verdict.Class
			}
			s.recordDiagnosticVerdict(account.ID, source, policy.Quality, outcome.Verdict, outcome.Signals, map[string]any{
				"attempt": attempts,
				"max":     maxAttempts,
			})
			selection.MarkFailure(account.ID, source, class)
			retryable := attempts < maxAttempts && transparentFailureCanFailover(facts.Replayable, outcome.Emitted, outcome.PreResponse, class)
			if retryable && !rotated && shouldRotateTransparentFailure(selection.HasRemainingCandidates(), class) {
				didRotate, errRotate := s.rotateAfterFailure(ctx, account.ID, class)
				if errRotate == nil && didRotate {
					rotated = true
					s.settleTransparentFailure(account.ID, outcome.Verdict, class, outcome.Err, req.HostCallbackID, false)
					continue
				}
			}
			s.settleTransparentFailure(account.ID, outcome.Verdict, class, outcome.Err, req.HostCallbackID, true)
			attempt.Release()
			if !retryable {
				break candidateLoop
			}
			break
		}
	}
	return run
}
