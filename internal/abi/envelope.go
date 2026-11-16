package abi

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// Envelope is the JSON response shape shared by the plugin ABI and host calls.
type Envelope struct {
	OK     bool             `json:"ok"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *pluginabi.Error `json:"error,omitempty"`
}

// OK marshals a successful result envelope.
func OK(value any) ([]byte, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(Envelope{OK: true, Result: raw})
}

// Error marshals a failed result envelope.
func Error(code, message string, status int) []byte {
	raw, _ := json.Marshal(Envelope{
		OK: false,
		Error: &pluginabi.Error{
			Code:       code,
			Message:    message,
			HTTPStatus: status,
		},
	})
	return raw
}
