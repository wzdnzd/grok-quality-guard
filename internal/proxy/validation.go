package proxy

// ValidationError reports a stable code and field location without echoing a
// credential, rendered username, or URL supplied by the caller.
type ValidationError struct {
	Code    string
	Path    string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func invalid(code, path, message string) *ValidationError {
	return &ValidationError{Code: code, Path: path, Message: message}
}
