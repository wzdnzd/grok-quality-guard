//go:build audit

package app

// The quality audit replays captured Grok responses through the real
// transparent quality gate and records what the plugin decided for each one.
// It lives here only because it reuses this package's test host; the build tag
// keeps it out of every normal build and test run. See tools/audit.
//
//	AUDIT_CAPTURES=a.jsonl,b.jsonl AUDIT_OUT=verdicts.jsonl \
//	AUDIT_POLICY=policy.json AUDIT_SHARD=0/4 \
//	go test -tags audit -run TestQualityGateAudit -timeout 3h ./internal/app/
//
// Streaming captures are replayed at their recorded pace, because the gate's
// hold and speed checks read the wall clock. The host bridge is a package
// global, so parallelism comes from running shards as separate processes.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	execapp "grok-quality-guard/internal/app/executor"
	appconfig "grok-quality-guard/internal/config"
)

type auditRecord struct {
	Channel   string          `json:"channel"`
	Task      string          `json:"task"`
	Prompt    string          `json:"prompt"`
	Model     string          `json:"model"`
	Effort    string          `json:"effort"`
	Rep       int             `json:"rep"`
	Status    json.RawMessage `json:"status"`
	TraceTail string          `json:"trace_tail"`
	AuthIndex string          `json:"auth_index"`
	Stratum   string          `json:"stratum"`
	Chunks    [][]any         `json:"chunks"`
	Body      string          `json:"body"`
	// Request is the body the capture actually sent, so agent tasks replay with
	// the same instructions, tools and history.
	Request map[string]any `json:"request"`
}

// id matches tools/audit/label.py so verdicts join with expected labels.
func (r auditRecord) id() string {
	key := r.TraceTail
	if key == "" {
		key = fmt.Sprintf("%s:%s:%d:%s:%s", r.AuthIndex, r.Task, r.Rep, r.Model, r.Effort)
	}
	return r.Channel + ":" + key
}

func (r auditRecord) ok() bool { return strings.TrimSpace(string(r.Status)) == "200" }

type auditVerdict struct {
	ID        string `json:"id"`
	Channel   string `json:"channel"`
	Task      string `json:"task"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	AuthIndex string `json:"auth_index"`
	Decision  string `json:"decision"`
	Code      string `json:"code,omitempty"`
	Class     string `json:"class,omitempty"`
	Reason    string `json:"reason,omitempty"`
	State     string `json:"state,omitempty"`
	Metrics   any    `json:"metrics,omitempty"`
	ReplayMS  int64  `json:"replay_ms"`
}

func TestQualityGateAudit(t *testing.T) {
	captures := strings.TrimSpace(os.Getenv("AUDIT_CAPTURES"))
	outPath := strings.TrimSpace(os.Getenv("AUDIT_OUT"))
	if captures == "" || outPath == "" {
		t.Skip("set AUDIT_CAPTURES and AUDIT_OUT")
	}
	shard, shards := auditShard(t)
	records := auditLoad(t, strings.Split(captures, ","))
	policy := auditPolicy(t)
	out, errOpen := os.OpenFile(outPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	defer out.Close()
	for index, record := range records {
		if index%shards != shard {
			continue
		}
		t.Run(fmt.Sprintf("%04d", index), func(t *testing.T) {
			verdict := auditReplay(t, record, policy)
			line, _ := json.Marshal(verdict)
			if _, errWrite := out.Write(append(line, '\n')); errWrite != nil {
				t.Fatal(errWrite)
			}
		})
	}
}

func auditShard(t *testing.T) (int, int) {
	raw := strings.TrimSpace(os.Getenv("AUDIT_SHARD"))
	if raw == "" {
		return 0, 1
	}
	parts := strings.SplitN(raw, "/", 2)
	shard, errShard := strconv.Atoi(parts[0])
	shards, errShards := strconv.Atoi(parts[len(parts)-1])
	if len(parts) != 2 || errShard != nil || errShards != nil || shards <= 0 || shard < 0 || shard >= shards {
		t.Fatalf("AUDIT_SHARD=%q, want i/n", raw)
	}
	return shard, shards
}

func auditLoad(t *testing.T, paths []string) []auditRecord {
	var records []auditRecord
	for _, path := range paths {
		file, errOpen := os.Open(strings.TrimSpace(path))
		if errOpen != nil {
			t.Fatal(errOpen)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for scanner.Scan() {
			var record auditRecord
			if errDecode := json.Unmarshal(scanner.Bytes(), &record); errDecode != nil {
				t.Fatalf("%s: %v", path, errDecode)
			}
			records = append(records, record)
		}
		if errScan := scanner.Err(); errScan != nil {
			t.Fatal(errScan)
		}
		_ = file.Close()
	}
	return records
}

// auditPolicy is the production policy, copied from the plugin state file, so
// the replay judges with the thresholds the live gate uses.
func auditPolicy(t *testing.T) []byte {
	path := strings.TrimSpace(os.Getenv("AUDIT_POLICY"))
	if path == "" {
		return nil
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	return raw
}

// auditRequestModel is what the client asked for. Control responses from a
// weaker model or effort are replayed as answers to this request, which is
// exactly how a degraded account looks to the gate.
func auditRequestModel() (string, string) {
	model := strings.TrimSpace(os.Getenv("AUDIT_REQUEST_MODEL"))
	if model == "" {
		model = "grok-4.7"
	}
	effort, set := os.LookupEnv("AUDIT_REQUEST_EFFORT")
	if !set {
		effort = "xhigh"
	}
	return model, strings.TrimSpace(effort)
}

func auditRequest(record auditRecord, stream bool) execapp.Request {
	model, effort := auditRequestModel()
	body := map[string]any{}
	for key, value := range record.Request {
		if key != "prompt_cache_key" {
			body[key] = value
		}
	}
	if len(body) == 0 {
		body["input"] = []any{map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": record.Prompt}}}}
		body["include"] = []any{"reasoning.encrypted_content"}
	}
	body["model"], body["stream"], body["store"] = model, stream, false
	delete(body, "reasoning")
	if effort != "" {
		body["reasoning"] = map[string]any{"effort": effort}
	}
	raw, _ := json.Marshal(body)
	return execapp.Request{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model: model, Format: "openai-response", SourceFormat: "openai-response", Stream: stream,
			OriginalRequest: raw, Headers: http.Header{"Content-Type": []string{"application/json"}},
		},
		HostCallbackID: "callback-audit",
	}
}

func auditPlugin(t *testing.T, host *mockHostCall, policyJSON []byte) *Plugin {
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.Guard = "transparent" })
	if len(policyJSON) > 0 {
		policy := plugin.Policy()
		if errDecode := json.Unmarshal(policyJSON, &policy); errDecode != nil {
			t.Fatalf("decode policy: %v", errDecode)
		}
		policy.Guard = "transparent"
		plugin.SetPolicy(policy)
	}
	return plugin
}

func auditReplay(t *testing.T, record auditRecord, policyJSON []byte) auditVerdict {
	verdict := auditVerdict{ID: record.id(), Channel: record.Channel, Task: record.Task, Model: record.Model,
		Effort: record.Effort, AuthIndex: record.AuthIndex}
	if !record.ok() {
		verdict.Decision = "skipped"
		return verdict
	}
	started := time.Now()
	entries, auths := singleGrokAuth(t, "auth-1", "lab@example.com", nil)
	host := newMockHostCall(entries, auths)
	var plugin *Plugin
	if record.Channel == "native" {
		plugin = auditReplayStream(t, host, record, policyJSON, &verdict)
	} else {
		plugin = auditReplayBuffered(t, host, record, policyJSON, &verdict)
	}
	if account, ok := plugin.stateAccount("auth-1"); ok {
		verdict.State = account.State
		verdict.Metrics = account.Metrics
		if account.LastAttempt != nil {
			verdict.Class, verdict.Reason = account.LastAttempt.Class, account.LastAttempt.Reason
		}
	}
	verdict.ReplayMS = time.Since(started).Milliseconds()
	return verdict
}

func auditReplayStream(t *testing.T, host *mockHostCall, record auditRecord, policyJSON []byte, verdict *auditVerdict) *Plugin {
	reads := make([]pluginapi.HostModelStreamReadResponse, 0, len(record.Chunks)+1)
	offsets := make([]time.Duration, 0, len(record.Chunks)+1)
	for _, chunk := range record.Chunks {
		if len(chunk) != 2 {
			continue
		}
		offsetMS, _ := chunk[0].(float64)
		text, _ := chunk[1].(string)
		reads = append(reads, streamData(text))
		offsets = append(offsets, time.Duration(offsetMS*float64(time.Millisecond)))
	}
	last := time.Duration(0)
	if len(offsets) > 0 {
		last = offsets[len(offsets)-1]
	}
	reads = append(reads, pluginapi.HostModelStreamReadResponse{Done: true})
	offsets = append(offsets, last)
	host.scriptStream("auth-1", reads...)
	plugin := auditPlugin(t, host, policyJSON)

	// Hold every read until its recorded arrival time, measured from the
	// moment the plugin opened the upstream stream. The host only answers
	// execute_stream once the first payload is readable
	// (receiveInitialModelExecutionChunk), so the open itself waits for the
	// first chunk and the silent thinking time is spent there.
	var mu sync.Mutex
	var opened time.Time
	next := 0
	inner := hostCall
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case pluginabi.MethodHostModelExecuteStream:
			mu.Lock()
			opened, next = time.Now(), 0
			first := opened
			if len(offsets) > 0 {
				first = opened.Add(offsets[0])
			}
			mu.Unlock()
			time.Sleep(time.Until(first))
		case pluginabi.MethodHostModelStreamRead:
			mu.Lock()
			index := next
			next++
			due := opened
			if index < len(offsets) {
				due = opened.Add(offsets[index])
			}
			mu.Unlock()
			time.Sleep(time.Until(due))
		}
		return inner(method, payload)
	}
	t.Cleanup(func() { hostCall = inner })

	streamID := "audit-stream"
	plugin.runTransparentStreamBridge(context.Background(), auditRequest(record, true), streamID)
	host.mu.Lock()
	detail := host.closes[streamID]
	emitted := 0
	for _, payload := range host.emitted[streamID] {
		if !strings.HasPrefix(strings.TrimSpace(string(payload)), ":") {
			emitted++
		}
	}
	host.mu.Unlock()
	switch {
	case detail != nil:
		verdict.Decision, verdict.Code = "withhold", detail.Code
	case emitted > 0:
		verdict.Decision = "deliver"
	default:
		verdict.Decision = "empty"
	}
	return plugin
}

func auditReplayBuffered(t *testing.T, host *mockHostCall, record auditRecord, policyJSON []byte, verdict *auditVerdict) *Plugin {
	host.scriptModelResponse("auth-1", pluginapi.HostModelExecutionResponse{
		StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(record.Body),
	})
	plugin := auditPlugin(t, host, policyJSON)
	encoded, _ := json.Marshal(auditRequest(record, false))
	raw, errExecute := plugin.Execute(encoded)
	plugin.waitSettled()
	if errExecute != nil {
		verdict.Decision, verdict.Code = "error", errExecute.Error()
		return plugin
	}
	var envelope struct {
		OK    bool `json:"ok"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	if envelope.OK {
		verdict.Decision = "deliver"
	} else {
		verdict.Decision = "withhold"
		if envelope.Error != nil {
			verdict.Code = envelope.Error.Code
		}
	}
	return plugin
}
