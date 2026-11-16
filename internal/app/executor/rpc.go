package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var errTransparentAttemptAborted = fmt.Errorf("transparent attempt aborted")

type streamReadEvent struct {
	Response pluginapi.HostModelStreamReadResponse
	Err      error
}

func (s *Service) RouteModel(request []byte) ([]byte, error) {
	var req RouteRequest
	if len(request) == 0 || json.Unmarshal(request, &req) != nil {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false, Reason: "invalid_route_request"})
	}
	policy := s.env.Policy()
	if !policy.TransparentGuard {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false, Reason: "transparent_guard_disabled"})
	}
	if isProbeHeader(req.Headers) {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false, Reason: "internal_probe_request"})
	}
	payload := decodeRequestPayload(req.Body)
	requestedModel := firstNonEmpty(req.RequestedModel, asString(req.Metadata["model"]), asString(payload["model"]))
	if !s.env.IsGrokRouteModel(requestedModel) {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false, Reason: "not_grok_model"})
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(req.Query.Get("alt"))), "compact") || isResponsesCompactionPayload(payload) {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false, Reason: "responses_compaction"})
	}
	// Keep no-auth requests on the host path so logs retain xAI provider attribution
	if !s.routeHasAvailableXAIAuth(req.AvailableProviders) {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false, Reason: "xai_auth_unavailable"})
	}
	return okEnvelope(pluginapi.ModelRouteResponse{
		Handled:    true,
		TargetKind: pluginapi.ModelRouteTargetSelf,
		Target:     s.env.PluginID(),
		Reason:     "grok_quality_guard_transparent",
	})
}

func (s *Service) routeHasAvailableXAIAuth(providers []string) bool {
	return s.env.HasAvailableXAIAuth(providers)
}

func (s *Service) Execute(request []byte) ([]byte, error) {
	var req Request
	if len(request) == 0 || json.Unmarshal(request, &req) != nil {
		return errorEnvelope("invalid_request", "decode executor request", http.StatusBadRequest), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, errExecute := s.transparentExecute(ctx, req)
	if errExecute != nil {
		return errorEnvelope("quality_degraded", "no healthy Grok account produced a valid response", http.StatusServiceUnavailable), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: result.Payload, Headers: result.Headers})
}

func (s *Service) ExecuteStream(request []byte) ([]byte, error) {
	var req Request
	if len(request) == 0 || json.Unmarshal(request, &req) != nil {
		return errorEnvelope("invalid_request", "decode executor request", http.StatusBadRequest), nil
	}
	streamID := strings.TrimSpace(req.StreamID)
	if streamID == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		result, errExecute := s.transparentExecuteBufferedStream(ctx, req)
		if errExecute != nil {
			return errorEnvelope("quality_degraded", "no healthy Grok account produced a valid response", http.StatusServiceUnavailable), nil
		}
		return okEnvelope(StreamResponse{Headers: result.Headers, Chunks: result.Chunks})
	}
	s.startTransparentStreamBridge(req, streamID)
	return okEnvelope(StreamResponse{Headers: sseHeaders()})
}
