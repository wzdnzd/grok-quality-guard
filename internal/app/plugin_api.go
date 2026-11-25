package app

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	execapp "grok-quality-guard/internal/app/executor"
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

// canceledRequestID returns the lifecycle id of a request.complete payload that
// reports a cancellation. The host serializes pluginapi.RequestCompletion,
// which carries no JSON tags, so the wire keys are the Go field names
// ("RequestID", "Outcome"); a snake_case decoder never sees the id. The host
// also copies the id into the request metadata, which covers a payload whose
// top-level field is empty.
func canceledRequestID(request []byte) string {
	var completion pluginapi.RequestCompletion
	if len(request) == 0 || json.Unmarshal(request, &completion) != nil {
		return ""
	}
	if completion.Outcome != pluginapi.RequestCompletionCanceled {
		return ""
	}
	if requestID := strings.TrimSpace(completion.RequestID); requestID != "" {
		return requestID
	}
	return execapp.RequestIDFromMetadata(completion.Metadata)
}

// HandleRequestComplete stops the transparent attempt that serves a request the
// host reports as canceled. The host detaches client cancellation from the
// model stream, so without this a disconnected client would keep the failover
// loop running until every account in the pool was tried.
func (p *Plugin) HandleRequestComplete(request []byte) {
	requestID := canceledRequestID(request)
	if requestID == "" || p.executorSvc == nil {
		return
	}
	p.executorSvc.CancelRequest(requestID)
}
