package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var hostCall func(string, any) (json.RawMessage, error)

const hostCallGrace = 5 * time.Second
const hostAuthListTimeout = 30 * time.Second

type hostCallResult struct {
	raw json.RawMessage
	err error
}

type hostCallFlight struct {
	done chan struct{}
	once sync.Once
	raw  json.RawMessage
	err  error
}

func (flight *hostCallFlight) finish(raw json.RawMessage, err error) {
	if flight == nil {
		return
	}
	flight.once.Do(func() {
		flight.raw = raw
		flight.err = err
		close(flight.done)
	})
}

func invokeHostCall(call func(string, any) (json.RawMessage, error), method string, payload any) (raw json.RawMessage, err error) {
	if call == nil {
		return nil, fmt.Errorf("host callback %s is not configured", method)
	}
	defer func() {
		if recover() != nil {
			raw = nil
			err = fmt.Errorf("host callback %s panicked", method)
		}
	}()
	return call(method, payload)
}

// hostCallWithContext bounds the wait for a host callback. The ABI callback has
// no cancellation handle, so a timed-out callback may still be running in the
// background; callers that own host resources must clean up any late result.
func hostCallWithContext(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	if ctx == nil || ctx.Done() == nil {
		return invokeHostCall(hostCall, method, payload)
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return nil, errCtx
	}
	call := hostCall
	results := make(chan hostCallResult, 1)
	go func() {
		raw, errCall := invokeHostCall(call, method, payload)
		results <- hostCallResult{raw: raw, err: errCall}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-results:
		return result.raw, result.err
	}
}

// SetHostCall installs the ABI-owned host callback used by the application.
func SetHostCall(call func(string, any) (json.RawMessage, error)) {
	hostCall = call
}

type authListResponse struct {
	Files []hostAuthFileEntry `json:"files"`
}

// hostAuthFileEntry intentionally mirrors only fields used by the guard.
// Upstream also serializes recent_requests and other management metadata; those
// values are skipped during decoding instead of being retained in the catalog.
type hostAuthFileEntry struct {
	ID          string    `json:"id,omitempty"`
	AuthIndex   string    `json:"auth_index,omitempty"`
	Name        string    `json:"name"`
	Type        string    `json:"type,omitempty"`
	Provider    string    `json:"provider,omitempty"`
	Label       string    `json:"label,omitempty"`
	Disabled    bool      `json:"disabled,omitempty"`
	Unavailable bool      `json:"unavailable,omitempty"`
	Path        string    `json:"path,omitempty"`
	Size        int64     `json:"size,omitempty"`
	ModTime     time.Time `json:"modtime,omitempty"`
	Email       string    `json:"email,omitempty"`
	Priority    int       `json:"priority,omitempty"`
}

type hostModelExecutionRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func (p *Plugin) sharedHostCall(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	if p == nil {
		return nil, fmt.Errorf("plugin is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return nil, errCtx
	}
	call := hostCall
	p.hostCallsMu.Lock()
	if p.hostCalls == nil {
		p.hostCalls = make(map[string]*hostCallFlight)
	}
	flight := p.hostCalls[method]
	created := flight == nil
	if created {
		flight = &hostCallFlight{done: make(chan struct{})}
		p.hostCalls[method] = flight
		go func() {
			raw, errCall := invokeHostCall(call, method, payload)
			p.hostCallsMu.Lock()
			if p.hostCalls[method] == flight {
				delete(p.hostCalls, method)
			}
			flight.finish(raw, errCall)
			p.hostCallsMu.Unlock()
		}()
	}
	p.hostCallsMu.Unlock()
	if created {
		// The callback itself cannot be cancelled. Keep the flight joinable for
		// later callers, but bound how long a permanently hung callback can
		// poison future requests. A caller timeout must not remove the flight
		// immediately: that would launch a duplicate host call while the first
		// one is still running.
		expiresAt := time.Now().Add(hostAuthListTimeout)
		if deadline, ok := ctx.Deadline(); ok && deadline.Before(expiresAt) {
			expiresAt = deadline
		}
		go p.expireHostCallFlight(method, flight, time.Until(expiresAt))
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-flight.done:
		return flight.raw, flight.err
	}
}

func (p *Plugin) expireHostCallFlight(method string, flight *hostCallFlight, delay time.Duration) {
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-flight.done:
		return
	case <-timer.C:
	}
	p.hostCallsMu.Lock()
	if p.hostCalls[method] == flight {
		delete(p.hostCalls, method)
	}
	p.hostCallsMu.Unlock()
	flight.finish(nil, fmt.Errorf("host callback %s timed out", method))
}

func (p *Plugin) listAuths(ctx context.Context) ([]pluginapi.HostAuthFileEntry, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, hostAuthListTimeout)
	defer cancel()
	result, errCall := p.sharedHostCall(callCtx, pluginabi.MethodHostAuthList, map[string]any{})
	if errCall != nil {
		return nil, errCall
	}
	var response authListResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host.auth.list result: %w", errUnmarshal)
	}
	files := make([]pluginapi.HostAuthFileEntry, len(response.Files))
	for index, entry := range response.Files {
		files[index] = pluginapi.HostAuthFileEntry{
			ID:          entry.ID,
			AuthIndex:   entry.AuthIndex,
			Name:        entry.Name,
			Type:        entry.Type,
			Provider:    entry.Provider,
			Label:       entry.Label,
			Disabled:    entry.Disabled,
			Unavailable: entry.Unavailable,
			Path:        entry.Path,
			Size:        entry.Size,
			ModTime:     entry.ModTime,
			Email:       entry.Email,
			Priority:    entry.Priority,
		}
	}
	return files, nil
}

func (p *Plugin) listAuthModels(ctx context.Context) ([]pluginapi.HostAuthModels, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, hostAuthListTimeout)
	defer cancel()
	result, errCall := p.sharedHostCall(callCtx, pluginabi.MethodHostAuthModels, pluginapi.HostAuthModelsRequest{})
	if errCall != nil {
		return nil, errCall
	}
	var response pluginapi.HostAuthModelsResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host.auth.models result: %w", errUnmarshal)
	}
	return response.Credentials, nil
}

func (p *Plugin) getAuth(authIndex string) (pluginapi.HostAuthGetResponse, error) {
	result, errCall := hostCall(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	return decodeHostAuthGet(result, errCall)
}

func (p *Plugin) getAuthWithContext(ctx context.Context, authIndex string) (pluginapi.HostAuthGetResponse, error) {
	result, errCall := hostCallWithContext(ctx, pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	return decodeHostAuthGet(result, errCall)
}

func decodeHostAuthGet(result json.RawMessage, errCall error) (pluginapi.HostAuthGetResponse, error) {
	if errCall != nil {
		return pluginapi.HostAuthGetResponse{}, errCall
	}
	var response pluginapi.HostAuthGetResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("decode host.auth.get result: %w", errUnmarshal)
	}
	return response, nil
}

func (p *Plugin) saveAuth(name string, raw json.RawMessage) (pluginapi.HostAuthSaveResponse, error) {
	result, errCall := hostCall(pluginabi.MethodHostAuthSave, pluginapi.HostAuthSaveRequest{Name: name, JSON: raw})
	if errCall != nil {
		return pluginapi.HostAuthSaveResponse{}, errCall
	}
	var response pluginapi.HostAuthSaveResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		return pluginapi.HostAuthSaveResponse{}, fmt.Errorf("decode host.auth.save result: %w", errUnmarshal)
	}
	return response, nil
}

func (p *Plugin) executeModel(_ context.Context, req pluginapi.HostModelExecutionRequest, callbackID string) (pluginapi.HostModelExecutionResponse, error) {
	result, errCall := hostCall(pluginabi.MethodHostModelExecute, hostModelExecutionRequest{HostModelExecutionRequest: req, HostCallbackID: callbackID})
	if errCall != nil {
		return pluginapi.HostModelExecutionResponse{}, errCall
	}
	var response pluginapi.HostModelExecutionResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		return pluginapi.HostModelExecutionResponse{}, fmt.Errorf("decode host.model.execute result: %w", errUnmarshal)
	}
	return response, nil
}

func (p *Plugin) executeModelStream(ctx context.Context, req pluginapi.HostModelExecutionRequest, callbackID string) (pluginapi.HostModelStreamResponse, error) {
	if ctx == nil || ctx.Done() == nil {
		response, errCall := p.executeModelStreamNow(req, callbackID)
		if errCall == nil && response.StreamID != "" {
			p.trackModelStream(response.StreamID)
		}
		return response, errCall
	}
	type streamResult struct {
		response pluginapi.HostModelStreamResponse
		err      error
	}
	// The send is intentionally unbuffered. If the caller times out before
	// accepting the result, the producer closes the late host stream.
	results := make(chan streamResult)
	go func() {
		response, errCall := p.executeModelStreamNow(req, callbackID)
		select {
		case results <- streamResult{response: response, err: errCall}:
		case <-ctx.Done():
			if response.StreamID != "" {
				_ = p.closeModelStream(response.StreamID)
			}
		}
	}()
	select {
	case <-ctx.Done():
		return pluginapi.HostModelStreamResponse{}, ctx.Err()
	case result := <-results:
		if result.err == nil && result.response.StreamID != "" {
			if errCtx := ctx.Err(); errCtx != nil {
				_ = p.closeModelStream(result.response.StreamID)
				return pluginapi.HostModelStreamResponse{}, errCtx
			}
			p.trackModelStream(result.response.StreamID)
		}
		return result.response, result.err
	}
}

func (p *Plugin) executeModelStreamNow(req pluginapi.HostModelExecutionRequest, callbackID string) (pluginapi.HostModelStreamResponse, error) {
	result, errCall := hostCall(pluginabi.MethodHostModelExecuteStream, hostModelExecutionRequest{HostModelExecutionRequest: req, HostCallbackID: callbackID})
	if errCall != nil {
		return pluginapi.HostModelStreamResponse{}, errCall
	}
	var response pluginapi.HostModelStreamResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		return pluginapi.HostModelStreamResponse{}, fmt.Errorf("decode host.model.execute_stream result: %w", errUnmarshal)
	}
	if response.StreamID == "" {
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return pluginapi.HostModelStreamResponse{}, fmt.Errorf("host.model.execute_stream returned an empty stream_id")
		}
	}
	return response, nil
}

func (p *Plugin) readModelStream(ctx context.Context, streamID string) (pluginapi.HostModelStreamReadResponse, error) {
	result, errCall := hostCallWithContext(ctx, pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: streamID})
	if errCall != nil {
		return pluginapi.HostModelStreamReadResponse{}, errCall
	}
	var response pluginapi.HostModelStreamReadResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		return pluginapi.HostModelStreamReadResponse{}, fmt.Errorf("decode host.model.stream_read result: %w", errUnmarshal)
	}
	return response, nil
}

func (p *Plugin) closeModelStream(streamID string) error {
	if streamID == "" {
		return nil
	}
	defer p.untrackModelStream(streamID)
	return p.closeModelStreamNow(streamID)
}

func (p *Plugin) closeModelStreamNow(streamID string) error {
	if streamID == "" {
		return nil
	}
	return p.closeModelStreamNowWithContext(context.Background(), streamID)
}

func (p *Plugin) closeModelStreamNowWithContext(ctx context.Context, streamID string) error {
	if streamID == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return errCtx
	}
	callCtx, cancel := context.WithTimeout(ctx, hostCallGrace)
	defer cancel()
	_, errCall := hostCallWithContext(callCtx, pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{StreamID: streamID})
	return errCall
}

func marshalRawMap(value map[string]json.RawMessage) (json.RawMessage, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, fmt.Errorf("encode auth JSON: %w", errMarshal)
	}
	return raw, nil
}
