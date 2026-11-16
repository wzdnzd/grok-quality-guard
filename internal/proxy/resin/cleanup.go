package resin

import (
	"context"
	"fmt"

	"grok-quality-guard/internal/proxy"
)

type IntegrationSnapshot struct {
	ID         string
	AdminURL   string
	PlatformID string
	AdminToken string
}

type SnapshotResolver func(context.Context, string) (IntegrationSnapshot, error)

type CleanupHandler struct {
	Resolve SnapshotResolver
}

// Delete resolves the immutable integration version recorded by the old
// binding. It never substitutes settings from the currently enabled source.
func (handler CleanupHandler) Delete(ctx context.Context, reference proxy.LeaseRef) error {
	if handler.Resolve == nil || reference.SnapshotID == "" || reference.Account == "" || reference.Provider != "resin" {
		return fmt.Errorf("resin cleanup reference is invalid")
	}
	snapshot, err := handler.Resolve(ctx, reference.SnapshotID)
	if err != nil {
		return err
	}
	if snapshot.ID != reference.SnapshotID || snapshot.PlatformID == "" || reference.PlatformID != "" && reference.PlatformID != snapshot.PlatformID {
		return fmt.Errorf("resin cleanup snapshot does not match the lease reference")
	}
	return DeleteLease(ctx, snapshot.AdminURL, snapshot.PlatformID, snapshot.AdminToken, reference.Account)
}

var _ proxy.CleanupHandler = CleanupHandler{}
