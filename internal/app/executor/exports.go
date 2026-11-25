package executor

import (
	"context"
)

func ClassifyProbeStatus(status int) string { return classifyProbeStatus(status) }
func ClassifyUpstreamStatus(status int, body []byte) (string, string) {
	return classifyUpstreamStatus(status, body)
}
func ClassifyTransportError(err error) string { return classifyTransportError(err) }

// RequestIDFromMetadata returns the host request lifecycle id stored in
// execution or completion metadata.
func RequestIDFromMetadata(metadata map[string]any) string { return requestIDFromMetadata(metadata) }

func ExecutorRequestInput(req Request) ([]byte, string, error) { return executorRequestInput(req) }
func ExecutionModel(req Request, body []byte) string           { return executionModel(req, body) }
func EnsureTransparentReasoningEvidence(body []byte, sourceFormat string, required bool) []byte {
	return ensureTransparentReasoningEvidence(body, sourceFormat, required)
}

func (s *Service) ReadModelStreamErrorBody(ctx context.Context, streamID string) []byte {
	return s.readModelStreamErrorBody(ctx, streamID)
}

var ErrTransparentAttemptAborted = errTransparentAttemptAborted

func SafeReplayPayload(payload map[string]any, sourceFormat string) bool {
	return safeReplayPayload(payload, sourceFormat)
}

func ClassifyTransportFailure(err error) (string, string) { return classifyTransportFailure(err) }
func (s *Service) RunTransparentStreamBridge(ctx context.Context, req Request, streamID string) {
	s.runTransparentStreamBridge(ctx, req, streamID)
}

func (s *Service) StartTransparentStreamBridge(req Request, streamID string) {
	s.startTransparentStreamBridge(req, streamID)
}
func (s *Service) RotateAfterFailure(ctx context.Context, authID, class string) (bool, error) {
	return s.rotateAfterFailure(ctx, authID, class)
}
func (s *Service) RecordAttemptError(authID, class string, errValue error) error {
	return s.recordAttemptError(authID, class, errValue)
}
