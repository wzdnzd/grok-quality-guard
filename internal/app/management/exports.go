package management

import (
	"context"

	"grok-quality-guard/internal/app/action"
)

type RPCManagementRequest = rpcManagementRequest
type ManagementResponse = managementResponse
type ActionRequest = action.Request
type RestoreBatchItem = restoreBatchItem
type RestoreBatchResult = restoreBatchResult
type RestorePreviewResult = restorePreviewResult
type DisableBatchItem = disableBatchItem
type DisableBatchResult = disableBatchResult
type ProbeBatchItem = probeBatchItem
type ProbeBatchResult = probeBatchResult
type AccountCandidate = accountCandidate
type AccountSummaryView = accountSummaryView
type AccountView = accountView

func (s *Service) HandleAction(ctx context.Context, request ActionRequest) (any, int, error) {
	return s.handleAction(ctx, request)
}
func (s *Service) ActionPolicyGet() (any, int, error) { return s.actionPolicyGet() }
func (s *Service) ActionPolicyUpdate(params map[string]any) (any, int, error) {
	return s.actionPolicyUpdate(params)
}
func (s *Service) ActionPolicyReset() (any, int, error)                 { return s.actionPolicyReset() }
func (s *Service) ActionOverview(ctx context.Context) (any, int, error) { return s.actionOverview(ctx) }
func (s *Service) ActionAccountsList(ctx context.Context, params map[string]any) (any, int, error) {
	return s.actionAccountsList(ctx, params)
}
func (s *Service) ActionAccountGet(ctx context.Context, params map[string]any) (any, int, error) {
	return s.actionAccountGet(ctx, params)
}
func (s *Service) AccountSummary(account Account, stateAccount AccountState, proxyURL string) AccountSummaryView {
	return s.accountSummary(account, stateAccount, proxyURL)
}
func (s *Service) AccountView(ctx context.Context, account Account) (AccountView, error) {
	return s.accountViewForAccount(ctx, account)
}
