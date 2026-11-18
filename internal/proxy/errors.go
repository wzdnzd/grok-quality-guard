package proxy

import (
	"context"
	"errors"
	"strings"
)

// ManagementError is a domain error carrying the management HTTP status.
type ManagementError struct {
	Code    string
	Status  int
	Message string
}

func (e *ManagementError) Error() string { return e.Message }

func (e *ManagementError) HTTPStatus() int { return e.Status }

// ErrorCode returns the stable localization key suffix for this error.
func (e *ManagementError) ErrorCode() string { return strings.TrimSpace(e.Code) }

func NewError(code string, status int, message string) error {
	return &ManagementError{Code: code, Status: status, Message: message}
}

// CodeOf returns a stable localization code for any error. The code is used by
// management responses instead of exposing implementation English to clients.
func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		if code := strings.TrimSpace(coded.ErrorCode()); code != "" {
			return code
		}
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "operation_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "operation_timeout"
	default:
		return "operation_failed"
	}
}
