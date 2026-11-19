// Quality review tests cover stream classification, passive audit and related regressions.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	managementapp "grok-quality-guard/internal/app/management"
	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
	qualitypkg "grok-quality-guard/internal/quality"
)

func TestReviewNonStreamNeverSpeedClassified(t *testing.T) {
	config := appconfig.DefaultQualityConfig()
	config.FailClosed = true
	unit := classifyNonStreamResult(qualitypkg.Signals{
		Terminal:      true,
		FirstTokenMS:  1,
		DurationMS:    1,
		OutputTokens:  300,
		VisibleTokens: 300,
	}, config, false, 8)
	if unit.Class != qualitypkg.ClassNone || unit.Signals.GenerationMS != 0 || unit.Signals.TPS != 0 {
		t.Fatalf("non-stream verdict = %#v", unit)
	}

	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptModelResponse("auth-1", pluginapi.HostModelExecutionResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"` + strings.Repeat("answer ", 80) + `"},"finish_reason":"stop"}],"usage":{"completion_tokens":300,"prompt_tokens":10}}`),
	})
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.TransparentGuard = true
		cfg.PassiveGuard = true
	})
	req := streamRequestForTest()
	req.Stream = false
	req.Model = "grok-composer-2.5-fast"
	req.Format = "openai"
	req.SourceFormat = "openai"
	req.OriginalRequest = []byte(`{"model":"grok-composer-2.5-fast","messages":[{"role":"user","content":"hi"}]}`)
	encoded, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	response, errExecute := plugin.Execute(encoded)
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	var envelope struct {
		OK     bool                       `json:"ok"`
		Result pluginapi.ExecutorResponse `json:"result"`
		Error  pluginabi.Error            `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(response, &envelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !envelope.OK {
		t.Fatalf("non-stream response rejected: %s", response)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State == appstate.StateCooling || account.State == appstate.StateDegraded || account.DisableKind != "" {
		t.Fatalf("non-stream success was penalized: %#v", account)
	}
	if len(host.saves) != 0 {
		t.Fatalf("non-stream success wrote auth: %#v", host.saves)
	}
}

func TestReviewClientDisconnectDoesNotPenalize(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", goodStreamScript()...)
	host.scriptStream("auth-2", goodStreamScript()...)
	host.scriptStreamEmitError(errors.New("stream plugin-stream-disconnect is not open"))
	plugin := newTestPlugin(t, host, enableTransparentForTest)

	plugin.runTransparentStreamBridge(context.Background(), streamRequestForTest(), "plugin-stream-disconnect")

	if len(host.modelCalls) != 1 {
		t.Fatalf("client disconnect used %d upstream executions, want 1", len(host.modelCalls))
	}
	for _, authID := range []string{"auth-1", "auth-2"} {
		account, _ := plugin.stateAccount(authID)
		if account.ErrorStrikes != 0 || account.State == appstate.StateSuspect || account.State == appstate.StateDegraded || account.State == appstate.StateCooling {
			t.Fatalf("client disconnect penalized %s: %#v", authID, account)
		}
	}
	if detail := host.closes["plugin-stream-disconnect"]; detail != nil {
		t.Fatalf("client disconnect closed with an upstream error: %#v", detail)
	}
}

func TestReviewEncryptedStreamNotBurst(t *testing.T) {
	cases := []struct {
		span int64
		want string
	}{
		{span: 0, want: qualitypkg.ClassBufferedBurst},
		{span: 500, want: qualitypkg.ClassNone},
		{span: 1500, want: qualitypkg.ClassNone},
		{span: 3000, want: qualitypkg.ClassNone},
	}
	for _, test := range cases {
		sig := qualitypkg.Signals{
			Terminal:        true,
			HasThinking:     true,
			VisibleTokens:   20,
			OutputTokens:    420,
			ReasoningTokens: 400,
			EncryptedBytes:  2048,
			EncryptedFloor:  256,
			FirstVisible:    true,
			VisibleFlushMS:  test.span,
			VisibleSpanMS:   test.span,
			VisibleChunks:   6,
		}
		verdict := ClassifyQualityHold(sig, 8)
		if test.span >= 200 && verdict.Action != qualitypkg.QualityActionDeliver {
			t.Fatalf("span %d action = %q class = %q, want deliver", test.span, verdict.Action, verdict.Class)
		}
		if test.span >= 200 && verdict.Class != qualitypkg.ClassNone {
			t.Fatalf("span %d class = %q, want none", test.span, verdict.Class)
		}
		if test.span == 0 && verdict.Class != test.want {
			t.Fatalf("span 0 class = %q, want %q", verdict.Class, test.want)
		}
	}
}

func TestReviewHoldExpiryKeepsSustainedEncryptedStream(t *testing.T) {
	sig := qualitypkg.Signals{
		Terminal:        false,
		HoldExpired:     true,
		HasThinking:     true,
		VisibleTokens:   20,
		OutputTokens:    420,
		ReasoningTokens: 400,
		EncryptedBytes:  2048,
		EncryptedFloor:  256,
		FirstVisible:    true,
		VisibleSpanMS:   1000,
		VisibleChunks:   6,
	}
	if verdict := ClassifyQualityHold(sig, 8); verdict.Action != qualitypkg.QualityActionDeliver || verdict.Class != qualitypkg.ClassNone {
		t.Fatalf("sustained stream at hold expiry = %#v, want deliver", verdict)
	}
	dump := sig
	dump.VisibleChunks = 1
	dump.VisibleSpanMS = 0
	dump.VisibleTokens = 4
	if verdict := ClassifyQualityHold(dump, 8); verdict.Class != qualitypkg.ClassBufferedBurst {
		t.Fatalf("single-chunk hold dump = %#v, want buffered burst", verdict)
	}
}

func TestReviewProbeRejectsBufferedBurst(t *testing.T) {
	config := appconfig.DefaultQualityConfig()
	sig := qualitypkg.Signals{
		Terminal:        true,
		FirstTokenMS:    100,
		DurationMS:      400,
		VisibleFlushMS:  150,
		FirstVisible:    true,
		VisibleSpanMS:   150,
		VisibleChunks:   2,
		VisibleTokens:   20,
		OutputTokens:    100,
		ReasoningTokens: 500,
		EncryptedBytes:  2048,
		EncryptedFloor:  2000,
		HasThinking:     true,
	}
	verdict := classifyProbeResult(sig, config, true, true)
	if verdict.Class != qualitypkg.ClassBufferedBurst || verdict.Action != qualitypkg.QualityActionWithhold {
		t.Fatalf("probe verdict = %#v, want buffered_burst withhold", verdict)
	}
}

func TestReviewThinkingTypeDisabledSkipsGate(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "type disabled", body: `{"model":"grok-4.5","thinking":{"type":"disabled"}}`, want: false},
		{name: "type none", body: `{"model":"grok-4.5","thinking":{"type":"none"}}`, want: false},
		{name: "effort none", body: `{"model":"grok-4.5","reasoning":{"effort":"none"}}`, want: false},
		{name: "output config off", body: `{"model":"grok-4.5","output_config":{"effort":"off"}}`, want: false},
		{name: "enabled budget", body: `{"model":"grok-4.5","thinking":{"type":"enabled","budget_tokens":2048}}`, want: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := reasoningRequiredForModel("grok-4.5", []byte(test.body)); got != test.want {
				t.Fatalf("reasoningRequiredForModel = %t, want %t", got, test.want)
			}
		})
	}
}

func TestReviewRateLimitedDoesNotDisableAccount(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.AutoAction = "cooldown_then_disable"
	})
	before := time.Now().UTC()
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{
		Class:  qualitypkg.ClassRateLimited,
		Reason: "reason.detail.upstream_status_429_rate_limited",
	}, "transparent", ""); errApply != nil {
		t.Fatal(errApply)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.DisableKind != "" || account.ErrorStrikes != 0 || account.SoftStrikes != 0 || account.ThinkingStrikes != 0 {
		t.Fatalf("rate limit became an account failure: %#v", account)
	}
	if account.State != appstate.StateSuspect {
		t.Fatalf("rate limit state = %q, want suspect", account.State)
	}
	if !account.ProbeBackoffUntil.After(before) {
		t.Fatalf("rate limit did not set probe backoff: %#v", account)
	}
	if len(host.saves) != 0 {
		t.Fatalf("rate limit wrote auth: %#v", host.saves)
	}
	if isTransparentSelectionHardFailure(qualitypkg.ClassRateLimited) {
		t.Fatal("rate limit must not break session affinity")
	}
}

func TestReviewPreviewDoesNotCopyVisibleText(t *testing.T) {
	analyzer := NewStreamAnalyzer()
	analyzer.ObserveChunk([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
	if preview := analyzer.Preview(); preview.VisibleText != "" {
		t.Fatalf("preview copied visible text: %q", preview.VisibleText)
	}
	if final := analyzer.Finalize(true); final.VisibleText != "hello" {
		t.Fatalf("final visible text = %q", final.VisibleText)
	}
}

func TestReviewStartBridgeRejectsWhenQuiescing(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	plugin.mu.Lock()
	plugin.quiescing = true
	plugin.mu.Unlock()

	plugin.startTransparentStreamBridge(streamRequestForTest(), "plugin-stream-quiescing")
	if len(host.modelCalls) != 0 {
		t.Fatalf("quiescing plugin started %d model calls", len(host.modelCalls))
	}
	detail := host.closes["plugin-stream-quiescing"]
	if detail == nil || detail.Code != "plugin_shutting_down" || detail.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("unexpected close detail: %#v", detail)
	}
}

func TestReviewTransparentRequestFetchesAuthOnce(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", goodStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	host.resetCatalogCallCounts()
	plugin.runTransparentStreamBridge(context.Background(), streamRequestForTest(), "plugin-stream-auth-get")

	if _, _, gets := host.catalogCallCounts(); gets != 1 {
		t.Fatalf("host.auth.get calls = %d, want 1", gets)
	}
}

func TestReviewObservationWriteIsCoalesced(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	store := plugin.stateStore()
	before, errRead := os.ReadFile(store.Path())
	if errRead != nil {
		t.Fatal(errRead)
	}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{
		Class:   qualitypkg.ClassNone,
		Signals: qualitypkg.Signals{OutputTokens: 10, VisibleTokens: 10},
	}, "passive", ""); errApply != nil {
		t.Fatal(errApply)
	}
	after, errRead := os.ReadFile(store.Path())
	if errRead != nil {
		t.Fatal(errRead)
	}
	if string(before) != string(after) {
		t.Fatal("observation wrote the state file synchronously")
	}
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatal(errFlush)
	}
	flushed, errRead := os.ReadFile(store.Path())
	if errRead != nil {
		t.Fatal(errRead)
	}
	if string(flushed) == string(after) {
		t.Fatal("flushed state did not include the observation")
	}
}

func TestReviewObservationDoesNotCopySelectionIndex(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if errUpdate := plugin.updateStateAccount("auth-1", func(account *appstate.AccountState) error {
		account.State = appstate.StateHealthy
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	before := plugin.routingSvc.Index().Load()
	if before == nil {
		t.Fatal("selection index is not ready")
	}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{
		Class:   qualitypkg.ClassNone,
		Signals: qualitypkg.Signals{OutputTokens: 10},
	}, "passive", ""); errApply != nil {
		t.Fatal(errApply)
	}
	after := plugin.routingSvc.Index().Load()
	if before != after {
		t.Fatal("observation-only update copied the selection index")
	}
}

func BenchmarkReviewPreviewWithoutTextCopy(b *testing.B) {
	analyzer := NewStreamAnalyzer()
	chunk := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("x", 500) + "\"}}]}\n\n")
	for index := 0; index < 2000; index++ {
		analyzer.ObserveChunk(chunk)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = analyzer.Preview()
	}
}

func BenchmarkReviewRecordObservation(b *testing.B) {
	store := appstate.NewStore(filepath.Join(b.TempDir(), "state.json"))
	if errLoad := store.Load(appconfig.PolicyFromConfig(appconfig.DefaultConfig()), "bench"); errLoad != nil {
		b.Fatal(errLoad)
	}
	if errUpdate := store.Update(func(state *appstate.State) error {
		for index := 0; index < 10000; index++ {
			id := "auth-" + strconv.Itoa(index)
			state.Accounts[id] = &appstate.AccountState{AuthID: id, State: appstate.StateHealthy}
		}
		return nil
	}); errUpdate != nil {
		b.Fatal(errUpdate)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if errObserve := store.RecordObservation("auth-0", func(account *appstate.AccountState) (*appstate.Event, error) {
			account.LastObservedAt = time.Now().UTC()
			account.Metrics.OutputTokens++
			return nil, nil
		}); errObserve != nil {
			b.Fatal(errObserve)
		}
	}
	b.StopTimer()
	_ = store.Flush()
}

func TestReviewNonReasoningFastStreamSkipsDumpClasses(t *testing.T) {
	config := appconfig.DefaultQualityConfig()
	sig := qualitypkg.Signals{
		Terminal:        true,
		Streaming:       true,
		FirstTokenMS:    100,
		DurationMS:      500,
		VisibleFlushMS:  100,
		VisibleSpanMS:   100,
		VisibleChunks:   2,
		VisibleTokens:   20,
		OutputTokens:    50,
		ReasoningTokens: 500,
		EncryptedBytes:  2048,
		EncryptedFloor:  2000,
		HasThinking:     true,
		FirstVisible:    true,
	}
	verdict := classifyFinal(sig, config, false, 8, true)
	if verdict.Class != qualitypkg.ClassNone || verdict.Action != qualitypkg.QualityActionDeliver {
		t.Fatalf("non-reasoning fast stream verdict = %#v, want deliver", verdict)
	}
}

func TestReviewNonReasoningProbeSkipsDumpClasses(t *testing.T) {
	config := appconfig.DefaultQualityConfig()
	sig := qualitypkg.Signals{
		Terminal:        true,
		FirstTokenMS:    100,
		DurationMS:      500,
		VisibleFlushMS:  100,
		VisibleSpanMS:   100,
		VisibleChunks:   2,
		VisibleTokens:   20,
		OutputTokens:    50,
		ReasoningTokens: 500,
		EncryptedBytes:  2048,
		EncryptedFloor:  2000,
		HasThinking:     true,
		FirstVisible:    true,
	}
	verdict := classifyProbeResult(sig, config, true, false)
	if verdict.Class != qualitypkg.ClassNone || verdict.Action != qualitypkg.QualityActionDeliver {
		t.Fatalf("non-reasoning probe verdict = %#v, want deliver", verdict)
	}
}

func TestReviewEncryptedStreamFastDeltasNotDump(t *testing.T) {
	config := appconfig.DefaultQualityConfig()
	analyzer := NewStreamAnalyzerWithProtocolAt("openai-response", time.Now())
	analyzer.SetRetainText(false)
	analyzer.SetEncryptedConfig(config.MinEncryptedBytes, config.EncryptedBytesPerReasoningToken)
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"rs_fast\",\"type\":\"reasoning\",\"encrypted_content\":\"" + strings.Repeat("A", 2048) + "\"}}\n\n"))
	for index := 0; index < 12; index++ {
		time.Sleep(20 * time.Millisecond)
		analyzer.ObserveChunk([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"word \"}\n\n"))
		preview := analyzer.Preview()
		preview.ReasoningRequired = true
		if verdict := ClassifyQualityHold(preview, config.MinHoldOutputTokens); verdict.Action == qualitypkg.QualityActionWithhold {
			t.Fatalf("healthy fast stream withheld at delta %d: %#v", index+1, verdict)
		}
	}
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":420,\"output_tokens_details\":{\"reasoning_tokens\":400}}}}\n\n"))
	sig := analyzer.Finalize(true)
	if sig.VisibleChunks != 12 || sig.VisibleSpanMS < 100 {
		t.Fatalf("unexpected visible stream signals: %#v", sig)
	}
	if verdict := classifyFinal(sig, config, true, config.MinHoldOutputTokens, true); verdict.Action != qualitypkg.QualityActionDeliver {
		t.Fatalf("healthy fast stream final verdict = %#v, want deliver", verdict)
	}
}

func TestReviewProbeSingleChunkDumpRejects(t *testing.T) {
	config := appconfig.DefaultQualityConfig()
	sig := qualitypkg.Signals{
		Terminal:        true,
		HasThinking:     true,
		VisibleTokens:   20,
		OutputTokens:    420,
		ReasoningTokens: 400,
		EncryptedBytes:  2048,
		EncryptedFloor:  256,
		FirstVisible:    true,
		VisibleChunks:   1,
		VisibleSpanMS:   0,
	}
	if verdict := classifyProbeResult(sig, config, true, true); !isDumpClass(verdict.Class) {
		t.Fatalf("probe accepted a single-chunk dump: %#v", verdict)
	}
}

// Passive audit and classification regressions.

func TestIgnoredAndProbeErrorPreserveQualityEvidence(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(config *appconfig.Config) { config.AutoAction = "observe" })
	initial := qualitypkg.QualityVerdict{
		Class:  qualitypkg.ClassMissingThinking,
		Reason: "reason.detail.no_real_reasoning_evidence",
		Signals: qualitypkg.Signals{
			OutputTokens: 100, VisibleTokens: 100, FirstTokenMS: 100, DurationMS: 1000,
			GenerationMS: 900, TPS: 111.1, Terminal: true, Streaming: true,
		},
	}
	if err := plugin.ApplyVerdict(context.Background(), "auth-1", initial, "passive", ""); err != nil {
		t.Fatal(err)
	}
	before, ok := plugin.stateAccount("auth-1")
	if !ok || before.LastEvidence == nil {
		t.Fatalf("initial quality evidence missing: %#v", before)
	}
	if err := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{Class: qualitypkg.ClassIgnored, Reason: "reason.detail.unsuccessful"}, "passive", ""); err != nil {
		t.Fatal(err)
	}
	afterIgnored, _ := plugin.stateAccount("auth-1")
	if afterIgnored.LastAttempt == nil || afterIgnored.LastAttempt.Class != qualitypkg.ClassIgnored {
		t.Fatalf("ignored attempt was not recorded: %#v", afterIgnored.LastAttempt)
	}
	if !reflect.DeepEqual(afterIgnored.LastEvidence, before.LastEvidence) || !reflect.DeepEqual(afterIgnored.Metrics, before.Metrics) {
		t.Fatalf("ignored attempt overwrote quality evidence: before=%#v after=%#v", before, afterIgnored)
	}
	if err := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{Class: qualitypkg.ClassProbeError, Reason: "reason.detail.probe_transport_error"}, "probe", ""); err != nil {
		t.Fatal(err)
	}
	afterError, _ := plugin.stateAccount("auth-1")
	if afterError.LastAttempt == nil || afterError.LastAttempt.Class != qualitypkg.ClassProbeError {
		t.Fatalf("probe error attempt was not recorded: %#v", afterError.LastAttempt)
	}
	if !reflect.DeepEqual(afterError.LastEvidence, before.LastEvidence) || !reflect.DeepEqual(afterError.Metrics, before.Metrics) {
		t.Fatalf("probe error overwrote quality evidence: before=%#v after=%#v", before, afterError)
	}
	if afterError.LastReason != qualitypkg.ClassMissingThinking {
		t.Fatalf("last quality reason changed to %q", afterError.LastReason)
	}
}

func TestPassiveUsageFailureFiltering(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)

	nonGrok, errMarshal := json.Marshal(pluginapi.UsageRecord{
		AuthID: "auth-1", RequestID: "non-grok", Provider: "openai", Model: "gpt-5", Failed: true,
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	plugin.HandleUsage(nonGrok)
	account, _ := plugin.stateAccount("auth-1")
	if account.LastAttempt != nil {
		t.Fatalf("non-Grok failure was recorded as an attempt: %#v", account.LastAttempt)
	}

	grokFailure, errMarshal := json.Marshal(pluginapi.UsageRecord{
		AuthID: "auth-1", RequestID: "grok-failure", Provider: "xai", Model: "grok-4.5", Failed: true,
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	plugin.HandleUsage(grokFailure)
	account, _ = plugin.stateAccount("auth-1")
	if account.LastAttempt == nil || account.LastAttempt.Class != qualitypkg.ClassIgnored || account.LastAttempt.Reason != "reason.detail.unsuccessful" {
		t.Fatalf("Grok failure was not recorded as ignored: %#v", account.LastAttempt)
	}
	if account.ErrorStrikes != 0 || account.LastEvidence != nil {
		t.Fatalf("Grok failure changed quality evidence: %#v", account)
	}
}

func TestAccountViewExposesQualityEvidence(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(config *appconfig.Config) { config.AutoAction = "observe" })
	verdict := qualitypkg.QualityVerdict{
		Class:  qualitypkg.ClassMissingThinking,
		Reason: "reason.detail.no_real_reasoning_evidence",
		Signals: qualitypkg.Signals{
			OutputTokens: 100, VisibleTokens: 100, ReasoningTokens: 20, EncryptedBytes: 200,
			EncryptedFloor: 256, FirstTokenMS: 100, DurationMS: 1000, GenerationMS: 900,
			TPS: 111.1, Terminal: true, Streaming: true, Protocol: qualitypkg.ProtocolResponses,
		},
	}
	if err := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "passive", ""); err != nil {
		t.Fatal(err)
	}
	response := actionManagementForTest(t, plugin, "accounts.list", map[string]any{"page_size": 10})
	if response.StatusCode != 200 {
		t.Fatalf("accounts.list status = %d body=%s", response.StatusCode, response.Body)
	}
	var decoded struct {
		Data struct {
			Items []managementapp.AccountView `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Data.Items) != 1 {
		t.Fatalf("unexpected account count: %d", len(decoded.Data.Items))
	}
	item := decoded.Data.Items[0]
	if item.LastAttempt == nil || item.LastAttempt.Class != qualitypkg.ClassMissingThinking {
		t.Fatalf("last attempt missing from account view: %#v", item.LastAttempt)
	}
	if item.Evidence == nil || item.Evidence.Class != qualitypkg.ClassMissingThinking || item.Evidence.Protocol != qualitypkg.ProtocolResponses {
		t.Fatalf("quality evidence missing from account view: %#v", item.Evidence)
	}
	if strings.TrimSpace(item.Evidence.Reason) == "" || item.Evidence.OutputTokens != 100 || item.Evidence.EncryptedFloor != 256 {
		t.Fatalf("quality evidence fields incomplete: %#v", item.Evidence)
	}
}

func TestHoldExpiredBurstRequiresReasoningBill(t *testing.T) {
	cipher := qualitypkg.Signals{
		HoldExpired: true, FirstVisible: true, VisibleFlushMS: 1500, VisibleTokens: 4,
		EncryptedBytes: 300, EncryptedFloor: 256,
	}
	if qualityIsBurstDump(cipher, 8) {
		t.Fatalf("cipher floor alone must not satisfy hold-expired burst reasoning bill: %#v", cipher)
	}
	cipher.ReasoningTokens = 80
	if !qualityIsBurstDump(cipher, 8) {
		t.Fatalf("reasoning bill at hold expiry should trigger burst: %#v", cipher)
	}
}

func TestPassiveAuditFlagsMissingThinkingForReasoningRequest(t *testing.T) {
	config := appconfig.DefaultQualityConfig()
	signals := qualitypkg.Signals{
		Protocol:          qualitypkg.ProtocolResponses,
		Streaming:         true,
		Terminal:          true,
		FirstTokenKnown:   true,
		FirstTokenMS:      1700,
		DurationMS:        3554,
		OutputTokens:      99,
		ReasoningTokens:   0,
		VisibleTokens:     0,
		UsageReported:     true,
		ReasoningRequired: true,
	}
	verdict := classifyPassiveAudit(signals, config)
	if verdict.Class != qualitypkg.ClassMissingThinking {
		t.Fatalf("class = %q, want %q (%#v)", verdict.Class, qualitypkg.ClassMissingThinking, verdict.Signals)
	}
}

func TestProbeWindowDoesNotDropRealPassiveUsage(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)

	initRequest, errMarshal := json.Marshal(streamChunkInterceptRequestForTest{
		StreamChunkInterceptRequest: pluginapi.StreamChunkInterceptRequest{
			RequestID:       "real-request",
			RequestedModel:  "grok-4.7",
			OriginalRequest: []byte(`{"model":"grok-4.7","reasoning":{"effort":"xhigh"}}`),
			ChunkIndex:      pluginapi.StreamChunkHeaderInitIndex,
		},
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	plugin.InterceptStreamChunk(initRequest)
	chunkRequest, errMarshal := json.Marshal(streamChunkInterceptRequestForTest{
		StreamChunkInterceptRequest: pluginapi.StreamChunkInterceptRequest{
			RequestID:      "real-request",
			RequestedModel: "grok-4.7",
			Body:           []byte(`data: {"type":"response.output_text.delta","delta":"` + strings.Repeat("visible ", 64) + `"}` + "\n\n"),
			ChunkIndex:     0,
		},
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	plugin.InterceptStreamChunk(chunkRequest)
	plugin.markProbeAuth("auth-1", 3*time.Minute)

	record := pluginapi.UsageRecord{
		AuthID:          "auth-1",
		RequestID:       "real-request",
		Provider:        "xai",
		Model:           "grok-4.7",
		ReasoningEffort: "xhigh",
		Stream:          true,
		TTFT:            1700 * time.Millisecond,
		Latency:         3554 * time.Millisecond,
		Detail: pluginapi.UsageDetail{
			OutputTokens:    99,
			ReasoningTokens: 0,
		},
	}
	raw, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	plugin.HandleUsage(raw)

	account, ok := plugin.stateAccount("auth-1")
	if !ok || account.LastAttempt == nil {
		t.Fatalf("real usage during probe window was not recorded: %#v", account)
	}
	if account.LastAttempt.Class != qualitypkg.ClassMissingThinking {
		t.Fatalf("real usage class = %q, want %q", account.LastAttempt.Class, qualitypkg.ClassMissingThinking)
	}
}

func TestPassiveUsageFlagsReasoningRequestWithoutThinking(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)

	record := pluginapi.UsageRecord{
		AuthID:          "auth-1",
		RequestID:       "passive-responses-missing-thinking",
		Provider:        "xai",
		Model:           "grok-4.7",
		ReasoningEffort: "xhigh",
		Stream:          true,
		TTFT:            1700 * time.Millisecond,
		Latency:         3554 * time.Millisecond,
		Detail: pluginapi.UsageDetail{
			OutputTokens:    99,
			ReasoningTokens: 0,
		},
	}
	raw, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	plugin.HandleUsage(raw)

	account, ok := plugin.stateAccount("auth-1")
	if !ok || account.LastAttempt == nil {
		t.Fatalf("passive usage was not recorded: %#v", account)
	}
	if account.LastAttempt.Class != qualitypkg.ClassMissingThinking {
		t.Fatalf("passive class = %q, want %q", account.LastAttempt.Class, qualitypkg.ClassMissingThinking)
	}
	if account.LastEvidence == nil || !account.LastEvidence.ReasoningRequired {
		t.Fatalf("passive evidence did not preserve reasoning requirement: %#v", account.LastEvidence)
	}
}

func TestStreamingSemanticOnlyOutputBypassesMissingThinking(t *testing.T) {
	config := appconfig.DefaultQualityConfig()
	config.SoftTPS = 1e9
	config.HardTPS = 2e9
	analyzer := NewStreamAnalyzerWithProtocol(qualitypkg.ProtocolResponses)
	encrypted := strings.Repeat("A", 1331)
	analyzer.ObserveChunk([]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","encrypted_content":"` + encrypted + `"}}` + "\n\n"))
	analyzer.ObserveChunk([]byte(`data: {"type":"response.output_item.done","item":{"id":"call_1","type":"function_call","call_id":"call_1","name":"exec","arguments":"{}"}}` + "\n\n"))
	analyzer.ObserveChunk([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"output_tokens":421,"output_tokens_details":{"reasoning_tokens":355}}}}` + "\n\n"))
	signals := analyzer.Finalize(true)
	if signals.HasThinking || !signals.SemanticOutput || signals.VisibleTokens != 0 {
		t.Fatalf("unexpected semantic-only signals: %#v", signals)
	}
	verdict := classifyFinal(signals, config, true, 8, true)
	if verdict.Class != qualitypkg.ClassNone || verdict.Action != qualitypkg.QualityActionDeliver {
		t.Fatalf("semantic-only response was rejected: %#v", verdict)
	}
	if !verdict.Signals.ReasoningRequired {
		t.Fatalf("reasoning requirement was not preserved in evidence: %#v", verdict.Signals)
	}
	if verdict.Signals.EncryptedFloor != 1420 {
		t.Fatalf("encrypted floor = %d, want 1420", verdict.Signals.EncryptedFloor)
	}
}

func TestThinkingGuardDisabledBypassesMissingThinkingInAllQualityPaths(t *testing.T) {
	config := appconfig.DefaultQualityConfig()
	config.ThinkingGuard = false
	config.SoftTPS = 1e9
	config.HardTPS = 2e9
	signals := qualitypkg.Signals{
		Streaming:       true,
		Terminal:        true,
		FirstTokenKnown: true,
		FirstTokenMS:    100,
		DurationMS:      2000,
		OutputTokens:    100,
		VisibleTokens:   100,
	}
	if verdict := classifyFinal(signals, config, true, 8, true); verdict.Class != qualitypkg.ClassNone {
		t.Fatalf("stream thinking_guard=false class = %q, want none (%#v)", verdict.Class, verdict)
	}
	if verdict := classifyNonStreamResult(signals, config, true, 8); verdict.Class != qualitypkg.ClassNone {
		t.Fatalf("non-stream thinking_guard=false class = %q, want none (%#v)", verdict.Class, verdict)
	}
	passive := signals
	passive.ReasoningRequired = true
	passive.FirstTokenMS = 100
	passive.DurationMS = 2000
	if verdict := classifyPassiveAudit(passive, config); verdict.Class != qualitypkg.ClassNone {
		t.Fatalf("passive thinking_guard=false class = %q, want none (%#v)", verdict.Class, verdict)
	}
}
