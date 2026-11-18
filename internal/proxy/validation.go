package proxy

import "strings"

// ValidationError reports a stable code and field location without echoing a
// credential, rendered username, or URL supplied by the caller.
type ValidationError struct {
	Code    string
	Path    string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// ErrorCode returns the stable localization key suffix for this validation.
func (e *ValidationError) ErrorCode() string { return strings.TrimSpace(e.Code) }

func invalid(code, path, message string) *ValidationError {
	return &ValidationError{Code: code, Path: path, Message: message}
}
