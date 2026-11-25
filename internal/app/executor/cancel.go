package executor

import (
	"context"
	"strings"
	"sync"
)

// hostRequestIDMetadataKey is the host request lifecycle id the executor
// request carries. It matches the id the host reports in request.complete.
const hostRequestIDMetadataKey = "request_id"

// requestIDFromMetadata returns the host request lifecycle id of one execution.
func requestIDFromMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	value, _ := metadata[hostRequestIDMetadataKey].(string)
	return strings.TrimSpace(value)
}

// requestCancels maps a host request lifecycle id to the cancel function of the
// attempt goroutine that serves it. The host detaches cancellation from the
// model stream it owns, so a client disconnect never reaches the plugin's own
// context. The request.complete callback is the only signal the host delivers,
// and without it a canceled request would keep failing over until the account
// pool drained.
type requestCancels struct {
	mu      sync.Mutex
	entries map[string]context.CancelFunc
}

func (r *requestCancels) register(requestID string, cancel context.CancelFunc) bool {
	if r == nil || requestID == "" || cancel == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = make(map[string]context.CancelFunc)
	}
	r.entries[requestID] = cancel
	return true
}

// release drops the registration without canceling. The request finished on
// its own, so its context must stay valid until the caller stops using it.
func (r *requestCancels) release(requestID string) {
	if r == nil || requestID == "" {
		return
	}
	r.mu.Lock()
	delete(r.entries, requestID)
	r.mu.Unlock()
}

// cancel stops the request goroutine once and removes its registration.
func (r *requestCancels) cancelRequest(requestID string) bool {
	if r == nil || requestID == "" {
		return false
	}
	r.mu.Lock()
	cancel := r.entries[requestID]
	delete(r.entries, requestID)
	r.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// RegisterRequestCancel tracks a request so a later completion event can stop
// it. It reports false when the request has no lifecycle id, in which case the
// caller keeps its own fallback timeout.
func (s *Service) RegisterRequestCancel(requestID string, cancel context.CancelFunc) bool {
	if s == nil {
		return false
	}
	return s.cancels.register(requestID, cancel)
}

// ReleaseRequestCancel forgets a request that ended normally.
func (s *Service) ReleaseRequestCancel(requestID string) {
	if s == nil {
		return
	}
	s.cancels.release(requestID)
}

// CancelRequest stops a request the host reported as canceled. It reports
// whether a live request was found, so the caller can tell a disconnect from a
// request that had already finished.
func (s *Service) CancelRequest(requestID string) bool {
	if s == nil {
		return false
	}
	return s.cancels.cancelRequest(requestID)
}
