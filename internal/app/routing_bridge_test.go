package app

import (
	"context"

	execapp "grok-quality-guard/internal/app/executor"
	appmodel "grok-quality-guard/internal/app/model"
	routingapp "grok-quality-guard/internal/app/routing"
)

func (p *Plugin) rebuildSelectionIndex() error { return p.routingSvc.RebuildIndex() }
func (p *Plugin) selectionSessionKey(req execapp.Request, body []byte, sourceFormat string) string {
	return p.routingSvc.SessionKey(req, body, sourceFormat)
}

func (p *Plugin) newTransparentSelection(req execapp.Request, body []byte, sourceFormat string) (*routingapp.Selection, error) {
	return p.routingSvc.NewTransparentSelection(req, body, sourceFormat)
}

func (p *Plugin) transparentCandidates(ctx context.Context) ([]appmodel.Account, error) {
	return p.routingSvc.Candidates(ctx)
}

func isTransparentSelectionHardFailure(class string) bool {
	return routingapp.IsTransparentSelectionHardFailure(class)
}

const (
	transparentSourcePreferred = routingapp.TransparentSourcePreferred
)
