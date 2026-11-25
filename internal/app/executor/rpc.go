package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"grok-quality-guard/internal/app/i18n"
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
	if !policy.TransparentGuardEnabled() {
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
		return localizedErrorEnvelope(i18n.DefaultLocale, "invalid_request", "api.error.invalid_request", http.StatusBadRequest), nil
	}
	ctx, cancel := s.trackedRequestContext(req)
	defer cancel()
	result, errExecute := s.transparentExecute(ctx, req)
	if errExecute != nil {
		if ctx.Err() != nil {
			return localizedErrorEnvelope(executorRequestLocale(req.Headers), "client_canceled", "api.error.client_canceled", 499), nil
		}
		return localizedErrorEnvelope(executorRequestLocale(req.Headers), "quality_degraded", "api.error.quality_degraded", http.StatusServiceUnavailable), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: result.Payload, Headers: result.Headers})
}

func (s *Service) ExecuteStream(request []byte) ([]byte, error) {
	var req Request
	if len(request) == 0 || json.Unmarshal(request, &req) != nil {
		return localizedErrorEnvelope(i18n.DefaultLocale, "invalid_request", "api.error.invalid_request", http.StatusBadRequest), nil
	}
	streamID := strings.TrimSpace(req.StreamID)
	if streamID == "" {
		ctx, cancel := s.trackedRequestContext(req)
		defer cancel()
		result, errExecute := s.transparentExecuteBufferedStream(ctx, req)
		if errExecute != nil {
			if ctx.Err() != nil {
				return localizedErrorEnvelope(executorRequestLocale(req.Headers), "client_canceled", "api.error.client_canceled", 499), nil
			}
			return localizedErrorEnvelope(executorRequestLocale(req.Headers), "quality_degraded", "api.error.quality_degraded", http.StatusServiceUnavailable), nil
		}
		return okEnvelope(StreamResponse{Headers: result.Headers, Chunks: result.Chunks})
	}
	s.startTransparentStreamBridge(req, streamID)
	return okEnvelope(StreamResponse{Headers: sseHeaders()})
}

// trackedRequestContext builds the request budget and, when the host supplied a
// lifecycle id, registers it so a client disconnect can stop the attempt
// instead of failing over to another account.
func (s *Service) trackedRequestContext(req Request) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	requestID := requestIDFromMetadata(req.Metadata)
	if s.RegisterRequestCancel(requestID, cancel) {
		return ctx, func() {
			s.ReleaseRequestCancel(requestID)
			cancel()
		}
	}
	return ctx, cancel
}
