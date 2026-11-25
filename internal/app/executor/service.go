package executor

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"grok-quality-guard/internal/app/model"
	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/config"
	proxy "grok-quality-guard/internal/proxy"
	domain "grok-quality-guard/internal/quality"
)

type RouteRequest struct {
	pluginapi.ModelRouteRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type Request struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type StreamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

type StreamEmitRequest struct {
	StreamID    string           `json:"stream_id"`
	Payload     []byte           `json:"payload,omitempty"`
	Error       string           `json:"error,omitempty"`
	ErrorDetail *pluginabi.Error `json:"error_detail,omitempty"`
}

type StreamCloseRequest struct {
	StreamID    string           `json:"stream_id"`
	Error       string           `json:"error,omitempty"`
	ErrorDetail *pluginabi.Error `json:"error_detail,omitempty"`
}

type Result struct {
	Payload   []byte
	Headers   http.Header
	Chunks    []pluginapi.ExecutorStreamChunk
	Available bool
	// PreResponse marks a transport failure before any upstream response was
	// returned, which makes failover safe even for a non-replayable body.
	PreResponse bool
	// Signals carries the analysed counters of the attempt so the optional
	// diagnostic event can explain why a verdict was chosen.
	Signals QualitySignals
}

type StreamAttempt struct {
	Class     string
	Verdict   domain.QualityVerdict
	Err       error
	Buffered  [][]byte
	Emitted   bool
	Available bool
	Aborted   bool
	Signals   QualitySignals
	// PreResponse marks a transport failure before the model stream was opened.
	// Read failures after this point require a replayable request to fail over.
	PreResponse bool
}

type Facts struct {
	Body              []byte
	SourceFormat      string
	Model             string
	ReasoningRequired bool
	// ReasoningExplicit separates a client request for reasoning from a
	// capability table inference.
	ReasoningExplicit bool
	// EvidenceCapable reports whether the request path can expose reasoning
	// evidence at all.
	EvidenceCapable bool
	Replayable      bool
	SessionSeed     string
}

type Attempt struct {
	Account model.Account
	Raw     map[string]json.RawMessage
	Source  string
	Release func()
}

type Selection interface {
	Next(context.Context) (Attempt, bool)
	MarkSuccess(string, string)
	MarkFailure(string, string, string)
	TotalCandidates() int
	HasRemainingCandidates() bool
}

type Environment interface {
	PluginID() string
	Policy() config.Policy
	Store() *appstate.Store
	RequestContext() (context.Context, bool)
	// TaskStarted registers a request-lifetime goroutine and reports false once
	// the plugin is quiescing, so a refused task never races shutdown's wait.
	TaskStarted() bool
	TaskDone()
	PrepareRequest(Request) (Facts, error)
	NewSelection(Request, Facts) (Selection, error)
	ExecutableAuth(context.Context, string) (model.Account, map[string]json.RawMessage, bool)
	RefreshSelectionAccount(string)
	ExecuteModel(context.Context, pluginapi.HostModelExecutionRequest, string) (pluginapi.HostModelExecutionResponse, error)
	ExecuteModelStream(context.Context, pluginapi.HostModelExecutionRequest, string) (pluginapi.HostModelStreamResponse, error)
	ReadModelStream(context.Context, string) (pluginapi.HostModelStreamReadResponse, error)
	CloseModelStream(string) error
	EmitPluginStreamChunk(string, []byte) error
	ClosePluginStream(string, string, string, int)
	ApplyVerdict(context.Context, string, domain.QualityVerdict, string, string) error
	StateAccount(string) (appstate.AccountState, bool)
	ProxyBindingForAccount(appstate.AccountState) (proxy.Binding, bool)
	RotateProxy(context.Context, string, string) (string, error)
	AutoRotateAfterQualityFailure() bool
	IsGrokRouteModel(string) bool
	HasAvailableXAIAuth([]string) bool
}

type Service struct {
	env     Environment
	cancels requestCancels
}

func NewService(env Environment) *Service { return &Service{env: env} }
