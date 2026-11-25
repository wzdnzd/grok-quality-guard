package app

import (
	"encoding/json"
	"strings"
)

// ABI entry points intentionally stay on Plugin: they are the only methods
// invoked by the C ABI and are the composition boundary for the domain
// services.
func (p *Plugin) RouteModel(request []byte) ([]byte, error) {
	return p.executorSvc.RouteModel(request)
}

func (p *Plugin) Execute(request []byte) ([]byte, error) {
	return p.executorSvc.Execute(request)
}

func (p *Plugin) ExecuteStream(request []byte) ([]byte, error) {
	return p.executorSvc.ExecuteStream(request)
}

func (p *Plugin) HandleManagement(request []byte) ([]byte, error) {
	return p.managementSvc.HandleManagement(request)
}

func (p *Plugin) InterceptResponse(request []byte) []byte {
	return p.passiveSvc.InterceptResponse(request)
}

func (p *Plugin) InterceptStreamChunk(request []byte) []byte {
	return p.passiveSvc.InterceptStreamChunk(request)
}

func (p *Plugin) HandleUsage(request []byte) {
	p.passiveSvc.HandleUsage(request)
}

// requestCompletionRequest mirrors the host's request.complete payload. Only
// the fields the guard needs are decoded.
type requestCompletionRequest struct {
	RequestID string `json:"request_id"`
	Outcome   string `json:"outcome"`
}

// HandleRequestComplete stops the transparent attempt that serves a request the
// host reports as canceled. The host detaches client cancellation from the
// model stream, so without this a disconnected client would keep the failover
// loop running until every account in the pool was tried.
func (p *Plugin) HandleRequestComplete(request []byte) {
	var completion requestCompletionRequest
	if len(request) == 0 || json.Unmarshal(request, &completion) != nil {
		return
	}
	if completion.Outcome != "canceled" {
		return
	}
	requestID := strings.TrimSpace(completion.RequestID)
	if requestID == "" {
		return
	}
	if p.executorSvc != nil {
		p.executorSvc.CancelRequest(requestID)
	}
}
