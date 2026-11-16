package proxy

import "context"

// CleanupHandler is invoked only by the persistent outbox worker, after the
// replacement or clear has actually committed. Generation never calls Delete.
type CleanupHandler interface {
	Delete(context.Context, LeaseRef) error
}
