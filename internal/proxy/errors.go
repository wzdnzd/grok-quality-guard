package proxy

// ManagementError is a domain error carrying the management HTTP status.
type ManagementError struct {
	Code    string
	Status  int
	Message string
}

func (e *ManagementError) Error() string { return e.Message }

func (e *ManagementError) HTTPStatus() int { return e.Status }

func NewError(code string, status int, message string) error {
	return &ManagementError{Code: code, Status: status, Message: message}
}
