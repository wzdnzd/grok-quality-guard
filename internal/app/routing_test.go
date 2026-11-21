package app

import (
	"context"
	"encoding/json"
	"fmt"
	execapp "grok-quality-guard/internal/app/executor"
	routingapp "grok-quality-guard/internal/app/routing"
	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
	qualitypkg "grok-quality-guard/internal/quality"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func manyGrokAuths(t *testing.T, count int) []pluginapi.HostAuthFileEntry {
	t.Helper()
	entries := make([]pluginapi.HostAuthFileEntry, 0, count)
	for index := 0; index < count; index++ {
		authID := fmt.Sprintf("auth-%02d", index+1)
		entry, _ := singleGrokAuth(t, authID, authID+"@example.com", nil)
		entries = append(entries, entry...)
	}
	return entries
}

func selectionAuthRaw(t *testing.T, entries []pluginapi.HostAuthFileEntry) map[string]json.RawMessage {
	t.Helper()
	auths := make(map[string]json.RawMessage, len(entries))
	for _, entry := range entries {
		auths[entry.ID] = makeAuthJSON(t, map[string]any{
			"type":          "xai",
			"email":         entry.Email,
			"access_token":  "access-" + entry.ID,
			"refresh_token": "refresh-" + entry.ID,
			"disabled":      false,
		})
	}
	return auths
}

func setSelectionStates(t *testing.T, plugin *Plugin, states map[string]string) {
	t.Helper()
	if errUpdate := plugin.stateStore().UpdateIfChanged(func(state *appstate.State) (bool, error) {
		for authID, value := range states {
			plugin.ensureStateAccount(state, authID).State = value
		}
		return true, nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if errRebuild := plugin.rebuildSelectionIndex(); errRebuild != nil {
		t.Fatal(errRebuild)
	}
}

func selectionRequestWithSession(sessionID string) execapp.Request {
	request := streamRequestForTest()
	request.Headers = http.Header{"Content-Type": []string{"application/json"}}
	if sessionID != "" {
		request.Headers.Set("X-Session-Id", sessionID)
	}
	return request
}

func nextSelectionAttempt(t *testing.T, plugin *Plugin, sessionID string) (*routingapp.Selection, routingapp.CandidateAttempt) {
	t.Helper()
	request := selectionRequestWithSession(sessionID)
	selection, errSelection := plugin.newTransparentSelection(request, request.OriginalRequest, request.SourceFormat)
	if errSelection != nil {
		t.Fatal(errSelection)
	}
	attempt, ok := selection.Next(context.Background())
	if !ok {
		t.Fatal("selection returned no candidate")
	}
	return selection, attempt
}

func TestTransparentSelectionRoundRobinDistributesConcurrentIdleAuths(t *testing.T) {
	entries := manyGrokAuths(t, 10)
	host := newMockHostCall(entries, selectionAuthRaw(t, entries))
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.TransparentGuard = true
		cfg.Routing.Strategy.Selection = "round_robin"
		cfg.Routing.Strategy.PreferLastSuccess = false
		cfg.Routing.Affinity.Enabled = false
	})
	states := make(map[string]string, len(entries))
	for _, entry := range entries {
		states[entry.ID] = appstate.StateHealthy
	}
	setSelectionStates(t, plugin, states)

	const requests = 10
	start := make(chan struct{})
	results := make(chan routingapp.CandidateAttempt, requests)
	errs := make(chan error, requests)
	for index := 0; index < requests; index++ {
		go func() {
			<-start
			request := selectionRequestWithSession("")
			selection, errSelection := plugin.newTransparentSelection(request, request.OriginalRequest, request.SourceFormat)
			if errSelection != nil {
				errs <- errSelection
				return
			}
			attempt, ok := selection.Next(context.Background())
			if !ok {
				errs <- fmt.Errorf("selection returned no candidate")
				return
			}
			results <- attempt
		}()
	}
	close(start)

	seen := make(map[string]int, requests)
	attempts := make([]routingapp.CandidateAttempt, 0, requests)
	for len(attempts) < requests {
		select {
		case errSelection := <-errs:
			t.Fatal(errSelection)
		case attempt := <-results:
			seen[attempt.Account.ID]++
			attempts = append(attempts, attempt)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for concurrent selections")
		}
	}
	for _, attempt := range attempts {
		attempt.Release()
	}
	if len(seen) != requests {
		t.Fatalf("concurrent first attempts used %d distinct auths, want %d: %#v", len(seen), requests, seen)
	}
	for authID, count := range seen {
		if count != 1 {
			t.Fatalf("auth %s was selected %d times: %#v", authID, count, seen)
		}
	}
}

func healthyRoutingStats(success float32, ttftMS, tps float64) appstate.RoutingStats {
	nowMS := time.Now().UTC().UnixMilli()
	window := appstate.RoutingSampleWindow{
		Samples:        8,
		SuccessEWMA:    success,
		FailureEWMA:    1 - success,
		LogTTFTEWMA:    float32(math.Log(ttftMS)),
		LogTPSEWMA:     float32(math.Log(tps)),
		LastObservedMS: nowMS,
	}
	return appstate.RoutingStats{Probe: appstate.RoutingSourceStats{Reasoning: window}}
}

func TestTransparentSelectionUsesHighestScoreBucketAndFallsBackByScore(t *testing.T) {
	entries := manyGrokAuths(t, 17)
	host := newMockHostCall(entries, selectionAuthRaw(t, entries))
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.TransparentGuard = true
		cfg.Routing.Strategy.Selection = "fill_first"
		cfg.Routing.Strategy.PreferLastSuccess = false
		cfg.Routing.Affinity.Enabled = false
		cfg.Routing.Scoring.MaxAccountsPerBucket = 16
	})
	if errUpdate := plugin.stateStore().UpdateIfChanged(func(state *appstate.State) (bool, error) {
		for index := 1; index <= 17; index++ {
			authID := fmt.Sprintf("auth-%02d", index)
			plugin.ensureStateAccount(state, authID).State = appstate.StateHealthy
		}
		state.Accounts["auth-01"].RoutingStats = healthyRoutingStats(0.10, 9000, 20)
		state.Accounts["auth-02"].RoutingStats = healthyRoutingStats(0.95, 250, 900)
		for index := 3; index <= 17; index++ {
			authID := fmt.Sprintf("auth-%02d", index)
			success := float32(0.80) - float32(index-3)*0.02
			state.Accounts[authID].RoutingStats = healthyRoutingStats(success, float64(900+index*80), float64(600-index*15))
		}
		return true, nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if errRebuild := plugin.rebuildSelectionIndex(); errRebuild != nil {
		t.Fatal(errRebuild)
	}

	_, firstAttempt := nextSelectionAttempt(t, plugin, "")
	if firstAttempt.Account.ID != "auth-02" {
		t.Fatalf("first auth = %s, want highest-score auth-02", firstAttempt.Account.ID)
	}
	if count := plugin.routingSvc.Runtime().ActiveAttemptCount("auth-02"); count != 1 {
		t.Fatalf("first attempt reservation count = %d, want 1", count)
	}
	attempts := []routingapp.CandidateAttempt{firstAttempt}
	seen := map[string]bool{firstAttempt.Account.ID: true}
	for index := 1; index < 16; index++ {
		_, attempt := nextSelectionAttempt(t, plugin, "")
		if seen[attempt.Account.ID] {
			t.Fatalf("top-bucket attempt %d reused busy auth %s", index, attempt.Account.ID)
		}
		seen[attempt.Account.ID] = true
		attempts = append(attempts, attempt)
	}
	for _, attempt := range attempts {
		if count := plugin.routingSvc.Runtime().ActiveAttemptCount(attempt.Account.ID); count != 1 {
			t.Fatalf("attempt reservation for %s = %d, want 1", attempt.Account.ID, count)
		}
	}
	_, fallback := nextSelectionAttempt(t, plugin, "")
	if fallback.Account.ID != "auth-01" {
		t.Fatalf("busy top bucket did not fall back to the lowest-score bucket: %#v", fallback)
	}
	for _, attempt := range attempts {
		attempt.Release()
	}
	fallback.Release()
}

func TestTransparentSelectionPreferredBusyFallsBackToIdleAuth(t *testing.T) {
	entries := manyGrokAuths(t, 2)
	host := newMockHostCall(entries, selectionAuthRaw(t, entries))
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.TransparentGuard = true
		cfg.Routing.Strategy.Selection = "fill_first"
		cfg.Routing.Strategy.PreferLastSuccess = true
		cfg.Routing.Affinity.Enabled = false
	})
	setSelectionStates(t, plugin, map[string]string{"auth-01": appstate.StateHealthy, "auth-02": appstate.StateHealthy})

	firstSelection, firstAttempt := nextSelectionAttempt(t, plugin, "")
	if firstAttempt.Account.ID != "auth-01" {
		t.Fatalf("first auth = %s, want auth-01", firstAttempt.Account.ID)
	}
	firstSelection.MarkSuccess(firstAttempt.Account.ID, firstAttempt.Source)
	firstAttempt.Release()

	_, secondAttempt := nextSelectionAttempt(t, plugin, "")
	if secondAttempt.Account.ID != "auth-01" || secondAttempt.Source != transparentSourcePreferred {
		t.Fatalf("second attempt = %#v, want idle preferred auth-01", secondAttempt)
	}

	thirdSelection, thirdAttempt := nextSelectionAttempt(t, plugin, "")
	if thirdAttempt.Account.ID != "auth-02" {
		t.Fatalf("busy preferred was reused before idle auth: %#v", thirdAttempt)
	}
	if preferred, _ := plugin.routingSvc.Runtime().Preferred(); preferred != "auth-01" {
		t.Fatalf("busy preferred changed to %q", preferred)
	}
	thirdSelection.MarkSuccess(thirdAttempt.Account.ID, thirdAttempt.Source)
	secondAttempt.Release()
	thirdAttempt.Release()

	fourthSelection, fourthAttempt := nextSelectionAttempt(t, plugin, "")
	if fourthAttempt.Account.ID != "auth-01" || fourthAttempt.Source != transparentSourcePreferred {
		t.Fatalf("idle preferred was not restored: %#v", fourthAttempt)
	}
	fourthSelection.MarkSuccess(fourthAttempt.Account.ID, fourthAttempt.Source)
	fourthAttempt.Release()
}

func TestTransparentSelectionConcurrentFirstSessionRequestsShareAuth(t *testing.T) {
	entries := manyGrokAuths(t, 2)
	host := newMockHostCall(entries, selectionAuthRaw(t, entries))
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.TransparentGuard = true
		cfg.Routing.Strategy.Selection = "round_robin"
		cfg.Routing.Strategy.PreferLastSuccess = false
		cfg.Routing.Affinity.Enabled = true
	})
	setSelectionStates(t, plugin, map[string]string{"auth-01": appstate.StateHealthy, "auth-02": appstate.StateHealthy})

	const requests = 2
	start := make(chan struct{})
	results := make(chan routingapp.CandidateAttempt, requests)
	for index := 0; index < requests; index++ {
		go func() {
			<-start
			request := selectionRequestWithSession("session-shared")
			selection, errSelection := plugin.newTransparentSelection(request, request.OriginalRequest, request.SourceFormat)
			if errSelection != nil {
				results <- routingapp.CandidateAttempt{}
				return
			}
			attempt, _ := selection.Next(context.Background())
			results <- attempt
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if first.Account.ID == "" || second.Account.ID == "" {
		t.Fatal("session selection returned an empty auth")
	}
	if first.Account.ID != second.Account.ID {
		t.Fatalf("concurrent first session requests used different auths: %s vs %s", first.Account.ID, second.Account.ID)
	}
	first.Release()
	second.Release()
	request := selectionRequestWithSession("session-shared")
	sessionKey := plugin.selectionSessionKey(request, request.OriginalRequest, request.SourceFormat)
	if bound, ok := plugin.routingSvc.Runtime().LookupBinding(sessionKey, time.Now().UTC()); !ok || bound != first.Account.ID {
		t.Fatalf("session binding = %q, %v, want %s", bound, ok, first.Account.ID)
	}
}

func TestTransportFailuresInvalidateSessionAffinity(t *testing.T) {
	for _, class := range []string{qualitypkg.ClassProxyError, qualitypkg.ClassProbeError} {
		t.Run(class, func(t *testing.T) {
			entries := manyGrokAuths(t, 2)
			host := newMockHostCall(entries, selectionAuthRaw(t, entries))
			plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
				cfg.TransparentGuard = true
				cfg.Routing.Strategy.Selection = "round_robin"
				cfg.Routing.Strategy.PreferLastSuccess = false
				cfg.Routing.Affinity.Enabled = true
			})
			setSelectionStates(t, plugin, map[string]string{"auth-01": appstate.StateHealthy, "auth-02": appstate.StateHealthy})

			selection, attempt := nextSelectionAttempt(t, plugin, "session-transport-failure")
			selection.MarkSuccess(attempt.Account.ID, attempt.Source)
			request := selectionRequestWithSession("session-transport-failure")
			sessionKey := plugin.selectionSessionKey(request, request.OriginalRequest, request.SourceFormat)
			if bound, ok := plugin.routingSvc.Runtime().LookupBinding(sessionKey, time.Now().UTC()); !ok || bound != attempt.Account.ID {
				t.Fatalf("session binding = %q, %v, want %s", bound, ok, attempt.Account.ID)
			}

			selection.MarkFailure(attempt.Account.ID, attempt.Source, class)
			if bound, ok := plugin.routingSvc.Runtime().LookupBinding(sessionKey, time.Now().UTC()); ok {
				t.Fatalf("%s failure kept sticky session auth %q", class, bound)
			}
			attempt.Release()
		})
	}
}

func TestUnknownToHealthyMovesAuthToHealthyLayerWithoutFullRebuild(t *testing.T) {
	entries := manyGrokAuths(t, 2)
	host := newMockHostCall(entries, selectionAuthRaw(t, entries))
	plugin := newTestPlugin(t, host, nil)
	setSelectionStates(t, plugin, map[string]string{"auth-01": appstate.StateHealthy, "auth-02": appstate.StateUnknown})
	beforeGeneration := plugin.routingSvc.Index().BuildGeneration()

	if errUpdate := plugin.updateStateAccount("auth-02", func(account *appstate.AccountState) error {
		account.State = appstate.StateHealthy
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	layer, ok := plugin.routingSvc.Index().LayerForAuth("auth-02")
	if !ok || layer != "0:1" {
		t.Fatalf("unknown account did not move to healthy layer: %q, %v", layer, ok)
	}
	if generation := plugin.routingSvc.Index().BuildGeneration(); generation != beforeGeneration {
		t.Fatalf("account state move rebuilt the full index: before=%d after=%d", beforeGeneration, generation)
	}
}

func TestRecordAttemptErrorDoesNotInvalidateSelectionIndex(t *testing.T) {
	entries := manyGrokAuths(t, 1)
	host := newMockHostCall(entries, selectionAuthRaw(t, entries))
	plugin := newTestPlugin(t, host, nil)
	setSelectionStates(t, plugin, map[string]string{"auth-01": appstate.StateHealthy})
	beforeGeneration := plugin.routingSvc.Index().BuildGeneration()

	if errEvent := plugin.recordAttemptError("auth-01", qualitypkg.ClassIgnored, fmt.Errorf("temporary upstream capacity")); errEvent != nil {
		t.Fatal(errEvent)
	}
	request := selectionRequestWithSession("")
	if _, errSelection := plugin.newTransparentSelection(request, request.OriginalRequest, request.SourceFormat); errSelection != nil {
		t.Fatal(errSelection)
	}
	if generation := plugin.routingSvc.Index().BuildGeneration(); generation != beforeGeneration {
		t.Fatalf("event write rebuilt the selection index: before=%d after=%d", beforeGeneration, generation)
	}
}

func TestRoutingPolicyUpdateHotReloadsSelectionConfig(t *testing.T) {
	entries := manyGrokAuths(t, 1)
	host := newMockHostCall(entries, selectionAuthRaw(t, entries))
	plugin := newTestPlugin(t, host, nil)
	policy := appconfig.PolicyFromConfig(appconfig.DefaultConfig())
	policy.Routing.Strategy.Selection = "fill_first"
	policy.Routing.Strategy.PreferLastSuccess = false
	policy.Routing.Affinity.Enabled = false
	policy.Routing.Affinity.TTL = appconfig.NewDuration(2 * time.Hour)
	policy.Routing.Affinity.MaxEntries = 77

	_, status, errUpdate := plugin.actionPolicyUpdate(map[string]any{"policy": policy})
	if errUpdate != nil || status != http.StatusOK {
		t.Fatalf("policy update status=%d err=%v", status, errUpdate)
	}
	if plugin.cfg.Routing.Strategy.Selection != "fill_first" || plugin.cfg.Routing.Strategy.PreferLastSuccess || plugin.cfg.Routing.Affinity.Enabled {
		t.Fatalf("routing config was not hot-reloaded: %#v", plugin.cfg.Routing)
	}
	if plugin.cfg.Routing.Affinity.TTL.Duration != 2*time.Hour || plugin.cfg.Routing.Affinity.MaxEntries != 77 {
		t.Fatalf("routing TTL config was not hot-reloaded: %#v", plugin.cfg.Routing)
	}
}

func TestTransparentSelectionRebindsSessionAfterAuthBecomesIneligible(t *testing.T) {
	entries := manyGrokAuths(t, 2)
	host := newMockHostCall(entries, selectionAuthRaw(t, entries))
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.TransparentGuard = true
		cfg.Routing.Strategy.Selection = "fill_first"
		cfg.Routing.Strategy.PreferLastSuccess = false
		cfg.Routing.Affinity.Enabled = true
	})
	setSelectionStates(t, plugin, map[string]string{"auth-01": appstate.StateHealthy, "auth-02": appstate.StateHealthy})

	firstSelection, firstAttempt := nextSelectionAttempt(t, plugin, "session-rebind")
	if firstAttempt.Account.ID != "auth-01" {
		t.Fatalf("first auth = %s, want auth-01", firstAttempt.Account.ID)
	}
	firstSelection.MarkSuccess(firstAttempt.Account.ID, firstAttempt.Source)
	firstAttempt.Release()
	if errUpdate := plugin.updateStateAccount("auth-01", func(account *appstate.AccountState) error {
		account.State = appstate.StateSuspect
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	secondSelection, secondAttempt := nextSelectionAttempt(t, plugin, "session-rebind")
	if secondAttempt.Account.ID != "auth-02" {
		t.Fatalf("session did not rebind after auth invalidation: %#v", secondAttempt)
	}
	secondSelection.MarkSuccess(secondAttempt.Account.ID, secondAttempt.Source)
	secondAttempt.Release()
}

func TestSelectionSessionKeySeparatesCallerScopeAndSession(t *testing.T) {
	first := selectionRequestWithSession("session-a")
	first.Headers.Set("Authorization", "Bearer caller-a")
	second := selectionRequestWithSession("session-a")
	second.Headers.Set("Authorization", "Bearer caller-b")
	third := selectionRequestWithSession("session-b")
	third.Headers.Set("Authorization", "Bearer caller-a")

	firstKey := (&Plugin{}).selectionSessionKey(first, first.OriginalRequest, first.SourceFormat)
	secondKey := (&Plugin{}).selectionSessionKey(second, second.OriginalRequest, second.SourceFormat)
	thirdKey := (&Plugin{}).selectionSessionKey(third, third.OriginalRequest, third.SourceFormat)
	if firstKey == "" || secondKey == "" || thirdKey == "" {
		t.Fatal("session key should not be empty for stable session headers")
	}
	if firstKey == secondKey {
		t.Fatal("different caller scopes reused the same session key")
	}
	if firstKey == thirdKey {
		t.Fatal("different sessions reused the same session key")
	}
}
