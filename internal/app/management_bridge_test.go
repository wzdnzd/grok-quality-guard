package app

import (
	"net/http"

	managementapp "grok-quality-guard/internal/app/management"
	appmodel "grok-quality-guard/internal/app/model"
	appstate "grok-quality-guard/internal/app/state"
)

func (p *Plugin) actionPolicyUpdate(params map[string]any) (any, int, error) {
	return p.managementSvc.ActionPolicyUpdate(params)
}

func (p *Plugin) accountSummary(account appmodel.Account, stateAccount appstate.AccountState, resinCfg appstate.ResinSettings, proxyURL string) managementapp.AccountSummaryView {
	return p.managementSvc.AccountSummary(account, stateAccount, resinCfg, proxyURL)
}

func jsonHeaders() http.Header {
	return http.Header{"Content-Type": []string{"application/json; charset=utf-8"}, "Cache-Control": []string{"no-store"}}
}
func actionErrorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return "upstream_error"
	default:
		return "internal_error"
	}
}
