package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var hostCall func(string, any) (json.RawMessage, error)

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

func (p *Plugin) executeModelStream(_ context.Context, req pluginapi.HostModelExecutionRequest, callbackID string) (pluginapi.HostModelStreamResponse, error) {
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
		return response, nil
	}
	p.trackModelStream(response.StreamID)
	return response, nil
}

func (p *Plugin) readModelStream(_ context.Context, streamID string) (pluginapi.HostModelStreamReadResponse, error) {
	result, errCall := hostCall(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: streamID})
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
	_, errCall := hostCall(pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{StreamID: streamID})
	return errCall
}

func marshalRawMap(value map[string]json.RawMessage) (json.RawMessage, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, fmt.Errorf("encode auth JSON: %w", errMarshal)
	}
	return raw, nil
}
