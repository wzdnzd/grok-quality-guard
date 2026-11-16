package action

// Request is the management action envelope shared by the management API and
// domain services that consume action parameters.
type Request struct {
	Action string         `json:"action"`
	Params map[string]any `json:"params"`
}
