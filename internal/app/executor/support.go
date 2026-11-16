package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func transparentMaxAttempts(policy Policy, replayable bool) int {
	if !replayable {
		return 1
	}
	maxAttempts := policy.Quality.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 6
	}
	return maxAttempts
}

func shouldRotateTransparentFailure(candidateCount int, class string) bool {
	// Missing thinking is account-level evidence. Switch accounts first when an
	// alternative exists; keep proxy rotation as the single-account fallback.
	return class != ClassMissingThinking || candidateCount <= 1
}

func executorRequestInput(req Request) ([]byte, string, error) {
	if len(req.Payload) > 0 {
		sourceFormat := firstNonEmpty(req.SourceFormat, req.Format, "codex")
		return req.Payload, sourceFormat, nil
	}
	body := req.OriginalRequest
	if len(body) == 0 {
		return nil, "", fmt.Errorf("request body is empty")
	}
	sourceFormat := firstNonEmpty(req.SourceFormat, req.Format, "codex")
	return body, sourceFormat, nil
}

func executionModel(req Request, body []byte) string {
	return firstNonEmpty(req.Model, asString(req.Metadata["model"]), modelFromRequestBody(body), req.Alt)
}

// ensureTransparentReasoningEvidence mirrors the grok2api Build/Console request
// boundary. Codex and Responses clients may omit encrypted reasoning from the
// include list, which makes a healthy stream indistinguishable from a degraded
// one. Add the include before the quality gate observes the upstream stream.
func ensureTransparentReasoningEvidence(body []byte, sourceFormat string, reasoningRequired bool) []byte {
	if len(body) == 0 {
		return body
	}
	format := strings.ToLower(strings.TrimSpace(sourceFormat))
	if !strings.Contains(format, "codex") && !strings.Contains(format, "response") {
		return body
	}
	if !reasoningRequired {
		return body
	}
	var payload map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil || payload == nil {
		return body
	}
	if _, ok := payload["input"]; !ok {
		return body
	}
	includes := make([]string, 0)
	if raw, exists := payload["include"]; exists && len(raw) > 0 && string(raw) != "null" {
		if errUnmarshal := json.Unmarshal(raw, &includes); errUnmarshal != nil {
			return body
		}
		for _, name := range includes {
			if strings.TrimSpace(name) == "" {
				continue
			}
			if name == "reasoning.encrypted_content" {
				return body
			}
		}
	}
	encodedInclude, errMarshalInclude := json.Marshal(append(includes, "reasoning.encrypted_content"))
	if errMarshalInclude != nil {
		return body
	}
	payload["include"] = encodedInclude
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return body
	}
	return encoded
}

func buildHostExecution(req Request, body []byte, sourceFormat, authID, proxyURL string, stream bool) pluginapi.HostModelExecutionRequest {
	return buildHostExecutionWithModel(req, body, sourceFormat, executionModel(req, body), authID, proxyURL, stream)
}

func buildHostExecutionWithModel(req Request, body []byte, sourceFormat, model, authID, proxyURL string, stream bool) pluginapi.HostModelExecutionRequest {
	return pluginapi.HostModelExecutionRequest{
		EntryProtocol:  sourceFormat,
		ExitProtocol:   firstNonEmpty(req.Format, sourceFormat),
		Model:          model,
		Stream:         stream,
		Body:           body,
		Headers:        cloneHeader(req.Headers),
		Query:          cloneValues(req.Query),
		Alt:            req.Alt,
		ForcedProvider: "xai",
		AuthID:         authID,
		ProxyURL:       strings.TrimSpace(proxyURL),
	}
}

func (s *Service) emitPluginChunks(streamID string, payloads [][]byte) error {
	for _, payload := range payloads {
		if errEmit := s.emitPluginStreamChunk(streamID, payload); errEmit != nil {
			return errEmit
		}
	}
	return nil
}

func (s *Service) emitPluginStreamChunk(streamID string, payload []byte) error {
	if strings.TrimSpace(streamID) == "" {
		return fmt.Errorf("plugin stream id is required")
	}
	return s.env.EmitPluginStreamChunk(streamID, append([]byte(nil), payload...))
}

func (s *Service) closePluginStream(streamID, code, message string, status int) {
	s.env.ClosePluginStream(streamID, code, message, status)
}

func (s *Service) recordTransparentOverflow(authID string, limit int64) {
	if s == nil || s.env == nil || s.env.Store() == nil {
		return
	}
	_ = s.env.Store().AppendEvent(Event{
		Time:   time.Now().UTC(),
		AuthID: authID,
		Action: "transparent_overflow",
		Result: "delivered",
		Detail: fmt.Sprintf("hold buffer exceeded %d bytes; quality gate bypassed", limit),
	})
}

func (s *Service) recordAttemptError(authID, class string, errValue error) error {
	if s.env.Store() == nil {
		return errValue
	}
	return s.env.Store().AppendEvent(Event{
		Time:   time.Now().UTC(),
		AuthID: authID,
		Class:  class,
		Action: "transparent_attempt",
		Result: "failed",
		Detail: sanitizeError(errValue),
	})
}

func (s *Service) applyTransparentFailureVerdict(ctx context.Context, authID string, verdict QualityVerdict, class string, errValue error, callbackID string) {
	if verdict.Class == "" {
		verdict.Class = class
	}
	if verdict.Class == "" || verdict.Class == ClassIgnored {
		return
	}
	if verdict.Reason == "" && errValue != nil {
		verdict.Reason = sanitizeError(errValue)
	}
	_ = s.env.ApplyVerdict(ctx, authID, verdict, "transparent", callbackID)
}

// rotateAfterFailure runs before the verdict is applied so the same auth can retry on the new proxy
func (s *Service) rotateAfterFailure(ctx context.Context, authID, class string) (bool, error) {
	if !s.env.AutoRotateAfterQualityFailure() {
		return false, nil
	}
	if class != ClassMissingThinking && class != ClassCipherDrool && class != ClassFakeEncryptedDump && class != ClassBufferedBurst {
		return false, nil
	}
	stateAccount, _ := s.env.StateAccount(authID)
	if isIsolatedState(stateAccount.State) {
		return false, nil
	}
	binding, managed := s.env.ProxyBindingForAccount(stateAccount)
	if !managed {
		return false, nil
	}
	if _, _, ok := s.env.ExecutableAuth(ctx, authID); !ok {
		errResolve := fmt.Errorf("auth is not executable")
		_ = s.recordAttemptError(authID, ClassProxyError, errResolve)
		return false, errResolve
	}
	status, errRotate := s.env.RotateProxy(ctx, authID, string(binding.SourceKind))
	if errRotate != nil {
		_ = s.recordAttemptError(authID, ClassProxyError, errRotate)
		if status == "rotation_unavailable" {
			return false, nil
		}
		return false, errRotate
	}
	return status == "rotated" || status == "succeeded", nil
}
