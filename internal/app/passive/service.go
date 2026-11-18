package passive

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"grok-quality-guard/internal/abi"
	"grok-quality-guard/internal/app/auth"
	"grok-quality-guard/internal/config"
	"grok-quality-guard/internal/quality"
)

type Environment interface {
	Policy() config.Policy
	IsGrokRouteModel(string) bool
	IsProbeAuth(string) bool
	ApplyVerdict(context.Context, string, quality.QualityVerdict, string, string) error
	RecordInternalError(string, error) error
}

type Observation struct {
	RequestID string
	Body      []byte
	Analyzer  *quality.StreamAnalyzer
	StartedAt time.Time
	Completed bool
	Source    string
	Model     string
}

type Service struct {
	env     Environment
	mu      sync.Mutex
	pending map[string]*Observation
}

func NewService(env Environment) *Service {
	return &Service{env: env, pending: make(map[string]*Observation)}
}

type rpcResponseInterceptRequest struct {
	pluginapi.ResponseInterceptRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcStreamChunkInterceptRequest struct {
	pluginapi.StreamChunkInterceptRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func (s *Service) InterceptResponse(request []byte) []byte {
	raw, _ := abi.OK(pluginapi.ResponseInterceptResponse{})
	var req rpcResponseInterceptRequest
	if len(request) == 0 || json.Unmarshal(request, &req) != nil {
		return raw
	}
	passiveGuard := s.env.Policy().PassiveGuard

	if !passiveGuard || auth.IsProbeHeader(req.RequestHeaders) || !s.env.IsGrokRouteModel(firstNonEmpty(req.RequestedModel, req.Model)) {
		return raw
	}
	if req.RequestID == "" {
		return raw
	}
	analyzer := quality.NewStreamAnalyzer()
	analyzer.SetStreaming(false)
	analyzer.SetRetainText(false)
	analyzer.ObserveJSON(req.Body)
	s.mu.Lock()
	prunePendingLocked(s.pending, time.Now().UTC())
	if existing := s.pending[req.RequestID]; existing != nil {
		s.mu.Unlock()
		return raw
	}
	s.pending[req.RequestID] = &Observation{
		RequestID: req.RequestID,
		Body:      CompactReasoningBody(req.RequestBody),
		Analyzer:  analyzer,
		StartedAt: time.Now(),
		Completed: true,
		Source:    "response",
		Model:     firstNonEmpty(req.RequestedModel, req.Model),
	}
	s.mu.Unlock()
	return raw
}

func (s *Service) InterceptStreamChunk(request []byte) []byte {
	raw, _ := abi.OK(pluginapi.StreamChunkInterceptResponse{})
	var req rpcStreamChunkInterceptRequest
	if len(request) == 0 || json.Unmarshal(request, &req) != nil {
		return raw
	}
	passiveGuard := s.env.Policy().PassiveGuard

	if !passiveGuard || auth.IsProbeHeader(req.RequestHeaders) || !s.env.IsGrokRouteModel(firstNonEmpty(req.RequestedModel, req.Model)) {
		return raw
	}
	if req.RequestID == "" {
		return raw
	}
	s.mu.Lock()
	prunePendingLocked(s.pending, time.Now().UTC())
	pending := s.pending[req.RequestID]
	if req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex {
		if pending != nil && pending.Completed {
			s.mu.Unlock()
			return raw
		}
		analyzer := quality.NewStreamAnalyzer()
		analyzer.SetRetainText(false)
		body := req.OriginalRequest
		if len(body) == 0 {
			body = req.RequestBody
		}
		pending = &Observation{
			RequestID: req.RequestID,
			Body:      CompactReasoningBody(body),
			Analyzer:  analyzer,
			StartedAt: time.Now(),
			Source:    "stream",
			Model:     firstNonEmpty(req.RequestedModel, req.Model),
		}
		s.pending[req.RequestID] = pending
		s.mu.Unlock()
		return raw
	}
	if pending == nil {
		pending = &Observation{
			RequestID: req.RequestID,
			Analyzer:  quality.NewStreamAnalyzer(),
			StartedAt: time.Now(),
			Source:    "stream",
			Model:     firstNonEmpty(req.RequestedModel, req.Model),
		}
		s.pending[req.RequestID] = pending
	}
	pending.Analyzer.ObserveChunk(req.Body)
	s.mu.Unlock()
	return raw
}

func (s *Service) HandleUsage(request []byte) {
	var record pluginapi.UsageRecord
	if len(request) == 0 || json.Unmarshal(request, &record) != nil {
		return
	}
	if record.AuthID == "" {
		return
	}
	policy := s.env.Policy()

	if !policy.PassiveGuard {
		return
	}
	if !s.env.IsGrokRouteModel(record.Model) && !auth.IsGrokProvider(record.Provider) {
		return
	}

	s.mu.Lock()
	pending := s.pending[record.RequestID]
	delete(s.pending, record.RequestID)
	s.mu.Unlock()
	// Transparent executions are audited by the executor before emission. Their
	// usage records have no passive observation and must not be judged twice.
	if pending == nil && policy.TransparentGuard {
		return
	}
	// Probe usage is internal and has no passive observation. Real requests keep
	// their pending observation and must still be recorded during a probe window.
	if pending == nil && s.env.IsProbeAuth(record.AuthID) {
		return
	}
	if record.Failed {
		_ = s.env.ApplyVerdict(context.Background(), record.AuthID, quality.QualityVerdict{Class: quality.ClassIgnored, Reason: "reason.detail.unsuccessful"}, "passive", "")
		return
	}

	reasoningBody := []byte(nil)
	if pending != nil {
		reasoningBody = pending.Body
	}
	reasoningNeeded := UsageReasoningRequired(record, reasoningBody)
	sig := quality.Signals{
		Terminal:        true,
		Streaming:       record.Stream,
		FirstTokenMS:    record.TTFT.Milliseconds(),
		FirstTokenKnown: record.TTFT > 0,
		DurationMS:      record.Latency.Milliseconds(),
		OutputTokens:    record.Detail.OutputTokens,
		ReasoningTokens: record.Detail.ReasoningTokens,
		UsageReported:   true,
	}
	if pending != nil {
		sig = pending.Analyzer.Finalize(true)
		sig.Streaming = record.Stream
		sig.Terminal = true
		sig.UsageReported = true
		if record.TTFT > 0 {
			sig.FirstTokenMS = record.TTFT.Milliseconds()
			sig.FirstTokenKnown = true
		}
		if record.Latency > 0 {
			sig.DurationMS = record.Latency.Milliseconds()
		}
		sig.OutputTokens = record.Detail.OutputTokens
		sig.ReasoningTokens = quality.MaxInt64(0, record.Detail.ReasoningTokens)
		sig.GenerationMS = quality.GenerationWindowMS(sig.DurationMS, sig.FirstTokenMS, sig.ReasoningTokens)
		sig.TPS = quality.TPS(sig.OutputTokens, sig.GenerationMS)
	}
	sig.ReasoningRequired = reasoningNeeded
	verdict := quality.ClassifyPassiveAudit(sig, policy.Quality)

	if errApply := s.env.ApplyVerdict(context.Background(), record.AuthID, verdict, "passive", ""); errApply != nil {
		_ = s.env.RecordInternalError(record.AuthID, errApply)
	}
}

func UsageReasoningRequired(record pluginapi.UsageRecord, body []byte) bool {
	if len(body) > 0 && !quality.ReasoningRequired(body) {
		return false
	}
	if quality.DisabledReasoningValue(record.ReasoningEffort) {
		return false
	}
	for _, model := range []string{record.ResponseModel, record.Model, record.Alias} {
		if strings.TrimSpace(model) == "" {
			continue
		}
		if quality.ReasoningRequiredForModel(model, body) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

const pendingObservationTTL = 10 * time.Minute
const pendingObservationLimit = 1024

// CompactReasoningBody keeps only the request fields used by the passive
// reasoning gate. Pending observations must not retain the full prompt.
func CompactReasoningBody(body []byte) []byte {
	if len(body) == 0 {
		return nil
	}
	var payload map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return nil
	}
	compact := make(map[string]json.RawMessage, 5)
	for _, key := range []string{"model", "reasoning_effort", "reasoning", "output_config", "thinking"} {
		if value, ok := payload[key]; ok && len(value) > 0 && string(value) != "null" {
			compact[key] = value
		}
	}
	if len(compact) == 0 {
		return nil
	}
	encoded, errMarshal := json.Marshal(compact)
	if errMarshal != nil {
		return nil
	}
	return encoded
}

func prunePendingLocked(pending map[string]*Observation, now time.Time) {
	for requestID, observation := range pending {
		if observation == nil || now.Sub(observation.StartedAt) > pendingObservationTTL {
			delete(pending, requestID)
		}
	}
	for len(pending) >= pendingObservationLimit {
		oldestID := ""
		var oldest time.Time
		for requestID, observation := range pending {
			if observation == nil {
				delete(pending, requestID)
				continue
			}
			if oldestID == "" || observation.StartedAt.Before(oldest) {
				oldestID = requestID
				oldest = observation.StartedAt
			}
		}
		if oldestID == "" {
			return
		}
		delete(pending, oldestID)
	}
}
