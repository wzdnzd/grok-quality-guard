package app

import (
	"context"

	execapp "grok-quality-guard/internal/app/executor"
)

func classifyProbeStatus(status int) string { return execapp.ClassifyProbeStatus(status) }
func classifyUpstreamStatus(status int, body []byte) (string, string) {
	return execapp.ClassifyUpstreamStatus(status, body)
}
func classifyTransportError(err error) string { return execapp.ClassifyTransportError(err) }

func ensureTransparentReasoningEvidence(body []byte, sourceFormat string, reasoningRequired bool) []byte {
	return execapp.EnsureTransparentReasoningEvidence(body, sourceFormat, reasoningRequired)
}

func (p *Plugin) runTransparentStreamBridge(ctx context.Context, req execapp.Request, streamID string) {
	p.executorSvc.RunTransparentStreamBridge(ctx, req, streamID)
}

func (p *Plugin) startTransparentStreamBridge(req execapp.Request, streamID string) {
	p.executorSvc.StartTransparentStreamBridge(req, streamID)
}

func (p *Plugin) recordAttemptError(authID, class string, errValue error) error {
	return p.executorSvc.RecordAttemptError(authID, class, errValue)
}
