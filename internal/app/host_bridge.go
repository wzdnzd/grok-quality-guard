package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var hostCall func(string, any) (json.RawMessage, error)

const hostCallGrace = 5 * time.Second

type hostCallResult struct {
	raw json.RawMessage
	err error
}

// hostCallWithContext bounds the wait for a host callback. The ABI callback has
// no cancellation handle, so a timed-out callback may still be running in the
// background; callers that own host resources must clean up any late result.
func hostCallWithContext(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	if ctx == nil || ctx.Done() == nil {
		return hostCall(method, payload)
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return nil, errCtx
	}
	call := hostCall
	results := make(chan hostCallResult, 1)
	go func() {
		raw, errCall := call(method, payload)
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
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type hostModelExecutionRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func (p *Plugin) listAuths() ([]pluginapi.HostAuthFileEntry, error) {
	result, errCall := hostCall(pluginabi.MethodHostAuthList, map[string]any{})
	if errCall != nil {
		return nil, errCall
	}
	var response authListResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host.auth.list result: %w", errUnmarshal)
	}
	return response.Files, nil
}

func (p *Plugin) listAuthModels() ([]pluginapi.HostAuthModels, error) {
	result, errCall := hostCall(pluginabi.MethodHostAuthModels, pluginapi.HostAuthModelsRequest{})
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
	ctx, cancel := context.WithTimeout(context.Background(), hostCallGrace)
	defer cancel()
	_, errCall := hostCallWithContext(ctx, pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{StreamID: streamID})
	return errCall
}

func marshalRawMap(value map[string]json.RawMessage) (json.RawMessage, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, fmt.Errorf("encode auth JSON: %w", errMarshal)
	}
	return raw, nil
}
