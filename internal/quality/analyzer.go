package quality

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type StreamAnalyzer struct {
	parser         SSEParser
	protocol       string
	streaming      bool
	startedAt      time.Time
	firstTokenAt   time.Time
	firstVisibleAt time.Time
	lastVisibleAt  time.Time
	terminalAt     time.Time
	visibleText    strings.Builder
	visibleRunes   int64
	visibleChunks  int
	aggregateRunes int64
	visibleDelta   bool
	reasoningDelta bool
	hasThinking    bool
	reasoningStart bool
	encryptedBytes int64
	minEncrypted   int64
	bytesPerToken  int64
	retainText     bool
	signals        Signals
}

func NewStreamAnalyzer() *StreamAnalyzer {
	return &StreamAnalyzer{
		streaming:     true,
		retainText:    true,
		startedAt:     time.Now(),
		minEncrypted:  defaultEncryptedBytes,
		bytesPerToken: defaultEncryptedBytesPerToken,
	}
}

func NewStreamAnalyzerWithProtocol(protocol string) *StreamAnalyzer {
	analyzer := NewStreamAnalyzer()
	analyzer.protocol = normalizeQualityProtocol(protocol)
	return analyzer
}

// NewStreamAnalyzerWithProtocolAt keeps the TPS clock anchored to the request
// start instead of when the first host stream chunk becomes readable.
func NewStreamAnalyzerWithProtocolAt(protocol string, startedAt time.Time) *StreamAnalyzer {
	analyzer := NewStreamAnalyzerWithProtocol(protocol)
	if !startedAt.IsZero() {
		analyzer.startedAt = startedAt
	}
	return analyzer
}

func normalizeQualityProtocol(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(normalized, "chat"):
		return qualityProtocolChat
	case strings.Contains(normalized, "response"), strings.Contains(normalized, "codex"):
		return qualityProtocolResponses
	case strings.Contains(normalized, "openai"):
		return qualityProtocolChat
	case strings.Contains(normalized, "message"), strings.Contains(normalized, "anthropic"), strings.Contains(normalized, "claude"):
		return qualityProtocolAnthropic
	default:
		return normalized
	}
}

func (a *StreamAnalyzer) SetStreaming(value bool) {
	if a == nil {
		return
	}
	a.streaming = value
	a.signals.Streaming = value
}

func (a *StreamAnalyzer) SetProtocol(value string) {
	if a == nil {
		return
	}
	a.protocol = normalizeQualityProtocol(value)
}

func (a *StreamAnalyzer) SetRetainText(value bool) {
	if a == nil {
		return
	}
	a.retainText = value
}

func (a *StreamAnalyzer) SetEncryptedConfig(minBytes, bytesPerToken int64) {
	if a == nil {
		return
	}
	if minBytes > 0 {
		a.minEncrypted = minBytes
	}
	if bytesPerToken > 0 {
		a.bytesPerToken = bytesPerToken
	}
}

func (a *StreamAnalyzer) Text() string {
	if a == nil {
		return ""
	}
	return a.visibleText.String()
}

func (a *StreamAnalyzer) Preview() Signals {
	if a == nil {
		return Signals{}
	}
	return a.snapshot(false)
}

func (a *StreamAnalyzer) ObserveChunk(chunk []byte) {
	if a == nil || len(chunk) == 0 {
		return
	}
	a.signals.ChunkCount++
	a.signals.ByteCount += int64(len(chunk))
	for _, ev := range a.parser.Feed(chunk) {
		a.observeEvent(ev, time.Now())
	}
}

func (a *StreamAnalyzer) ObserveJSON(raw []byte) {
	if a == nil || len(raw) == 0 {
		return
	}
	a.observePayload(raw, "", time.Now())
}

func (a *StreamAnalyzer) observeEvent(ev SSEEvent, now time.Time) {
	comment := strings.TrimSpace(strings.ToLower(ev.Comment))
	if comment != "" {
		a.signals.EventCount++
		switch {
		case strings.HasPrefix(comment, strings.TrimPrefix(qualityReasoningSSEComment, ": ")):
			a.reasoningStart = true
			a.signals.ReasoningStarted = true
			a.markFirstToken(now)
		case strings.HasPrefix(comment, strings.TrimPrefix(qualityReasoningEvidencePrefix, ": ")):
			value := strings.TrimSpace(strings.TrimPrefix(ev.Comment, strings.TrimPrefix(qualityReasoningEvidencePrefix, ": ")))
			if count, err := strconv.ParseInt(value, 10, 64); err == nil && count > a.encryptedBytes {
				a.encryptedBytes = count
			}
			a.reasoningStart = true
			a.signals.ReasoningStarted = true
		}
	}
	data := strings.TrimSpace(ev.Data)
	if data == "" {
		return
	}
	a.signals.EventCount++
	if data == "[DONE]" {
		a.signals.Terminal = true
		a.terminalAt = now
		return
	}
	a.observePayload([]byte(data), ev.Event, now)
	if sseEventIsTerminal(ev.Event) {
		a.signals.Terminal = true
		a.terminalAt = now
	}
}

func sseEventIsTerminal(event string) bool {
	switch strings.ToLower(strings.TrimSpace(event)) {
	case "response.completed", "response.done", "response.incomplete", "response.failed", "message_stop":
		return true
	default:
		return false
	}
}

func (a *StreamAnalyzer) observePayload(raw []byte, fallbackEvent string, now time.Time) {
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		a.signals.ParseFailures++
		return
	}
	a.signals.ParsedEvents++
	if fallbackEvent != "" && payload != nil && strings.TrimSpace(asString(payload["type"])) == "" {
		payload["type"] = fallbackEvent
	}
	protocol := a.protocol
	if protocol == "" {
		protocol = detectQualityProtocol(payload)
	} else if !qualityPayloadMatchesProtocol(payload, protocol) {
		detected := detectQualityProtocol(payload)
		if detected == "" {
			a.signals.UnknownEvents++
			return
		}
		protocol = detected
	}
	if protocol == "" {
		a.signals.UnknownEvents++
		return
	}
	switch protocol {
	case qualityProtocolChat:
		a.observeChat(payload, now)
	case qualityProtocolAnthropic:
		a.observeAnthropic(payload, now)
	default:
		a.observeResponses(payload, now)
	}
}

func detectQualityProtocol(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if _, ok := payload["choices"]; ok {
		return qualityProtocolChat
	}
	eventName := strings.ToLower(strings.TrimSpace(asString(payload["type"])))
	if _, ok := payload["response"]; ok {
		return qualityProtocolResponses
	}
	if _, ok := payload["output"]; ok {
		return qualityProtocolResponses
	}
	if _, ok := payload["output_text"]; ok {
		return qualityProtocolResponses
	}
	if eventName == "response" || strings.HasPrefix(eventName, "response.") {
		return qualityProtocolResponses
	}
	if eventName == "message" || strings.HasPrefix(eventName, "content_block_") || strings.HasPrefix(eventName, "message_") {
		return qualityProtocolAnthropic
	}
	if _, ok := payload["content_block"]; ok {
		return qualityProtocolAnthropic
	}
	if _, ok := payload["delta"]; ok {
		return qualityProtocolAnthropic
	}
	if _, ok := payload["content"]; ok {
		if _, hasStop := payload["stop_reason"]; hasStop {
			return qualityProtocolAnthropic
		}
	}
	return ""
}

func qualityPayloadMatchesProtocol(payload map[string]any, protocol string) bool {
	if payload == nil {
		return true
	}
	switch protocol {
	case qualityProtocolChat:
		_, ok := payload["choices"]
		return ok
	case qualityProtocolAnthropic:
		eventName := strings.ToLower(strings.TrimSpace(asString(payload["type"])))
		if eventName == "message" || strings.HasPrefix(eventName, "content_block_") || strings.HasPrefix(eventName, "message_") {
			return true
		}
		if _, ok := payload["content_block"]; ok {
			return true
		}
		if _, ok := payload["delta"]; ok {
			return true
		}
		if _, ok := payload["content"]; ok {
			_, hasStop := payload["stop_reason"]
			return hasStop
		}
		return false
	case qualityProtocolResponses:
		eventName := strings.ToLower(strings.TrimSpace(asString(payload["type"])))
		if eventName == "response" || strings.HasPrefix(eventName, "response.") {
			return true
		}
		if _, ok := payload["response"]; ok {
			return true
		}
		if _, ok := payload["output"]; ok {
			return true
		}
		_, ok := payload["output_text"]
		return ok
	default:
		return false
	}
}
func (a *StreamAnalyzer) observeChat(payload map[string]any, now time.Time) {
	a.observeUsage(payload)
	if choices, ok := payload["choices"].([]any); ok {
		for _, rawChoice := range choices {
			choice, _ := rawChoice.(map[string]any)
			if choice == nil {
				continue
			}
			if message, okMessage := choice["message"].(map[string]any); okMessage && !a.streaming {
				a.observeChatMessage(message, now)
			}
			if delta, okDelta := choice["delta"].(map[string]any); okDelta {
				a.observeChatDelta(delta, now)
			}
			if finish := strings.TrimSpace(asString(choice["finish_reason"])); finish != "" {
				a.signals.Terminal = true
				a.terminalAt = now
			}
		}
	}
}

func (a *StreamAnalyzer) observeChatDelta(delta map[string]any, now time.Time) {
	if text := firstString(delta, "reasoning", "reasoning_content", "thinking_content"); strings.TrimSpace(text) != "" {
		a.addReasoningText(text)
	}
	if content, ok := delta["content"]; ok {
		a.addContentParts(content, now)
	}
	if toolCalls, ok := delta["tool_calls"].([]any); ok && len(toolCalls) > 0 {
		a.signals.SemanticOutput = true
		a.markFirstToken(now)
	}
	if functionCall := delta["function_call"]; functionCall != nil {
		a.signals.SemanticOutput = true
		a.markFirstToken(now)
	}
}

func (a *StreamAnalyzer) observeChatMessage(message map[string]any, now time.Time) {
	if text := firstString(message, "reasoning", "reasoning_content", "thinking_content"); strings.TrimSpace(text) != "" {
		a.addReasoningText(text)
	}
	if content, ok := message["content"]; ok {
		if a.streaming {
			a.observeResponsesMessageContent(content, now)
		} else {
			a.addContentParts(content, now)
		}
	}
	if toolCalls, ok := message["tool_calls"].([]any); ok && len(toolCalls) > 0 {
		a.signals.SemanticOutput = true
		a.markFirstToken(now)
	}
}

func (a *StreamAnalyzer) observeResponses(payload map[string]any, now time.Time) {
	eventName := strings.ToLower(strings.TrimSpace(asString(payload["type"])))
	if eventName == "" {
		if _, ok := payload["output"]; ok {
			eventName = "response.completed"
		}
	}
	a.observeUsage(payload)
	switch eventName {
	case "response.completed", "response.incomplete", "response.failed", "response.done":
		a.signals.Terminal = true
		a.terminalAt = now
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		a.addReasoningText(asString(payload["delta"]))
	case "response.reasoning_text.done", "response.reasoning_summary_text.done":
		if !a.streaming {
			a.addReasoningText(asString(payload["text"]))
		}
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		if !a.streaming {
			if part, ok := payload["part"].(map[string]any); ok {
				a.addReasoningText(asString(part["text"]))
			}
		}
	case "response.output_text.delta":
		a.addVisibleText(asString(payload["delta"]), now)
	case "response.output_text.done":
		if !a.streaming && !a.visibleDelta {
			runes := a.noteAggregateVisibleText(asString(payload["text"]), now, "")
			a.aggregateRunes = maxInt64(a.aggregateRunes, runes)
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta", "response.mcp_call_arguments.delta":
		if strings.TrimSpace(asString(payload["delta"])) != "" {
			a.signals.SemanticOutput = true
			a.markFirstToken(now)
		}
	case "response.output_item.added", "response.output_item.done":
		a.observeResponsesItem(payload, now)
	}
	// A non-streaming Responses payload has output at the top level. The
	// streaming terminal event carries the same array under response.output.
	if output, ok := payload["output"].([]any); ok {
		a.observeResponsesOutputItems(output, now)
	}
	if !a.streaming {
		if text := asString(payload["output_text"]); strings.TrimSpace(text) != "" {
			runes := a.noteAggregateVisibleText(text, now, "")
			a.aggregateRunes = maxInt64(a.aggregateRunes, runes)
		}
	}
	if response, ok := payload["response"].(map[string]any); ok {
		if output, okOutput := response["output"].([]any); okOutput {
			a.observeResponsesOutputItems(output, now)
		}
		a.observeUsage(response)
	}
}

func (a *StreamAnalyzer) observeResponsesOutputItems(output []any, now time.Time) {
	var aggregateRunes int64
	for _, rawItem := range output {
		item, _ := rawItem.(map[string]any)
		if item != nil {
			aggregateRunes += a.observeResponsesItemMap(item, now)
		}
	}
	a.aggregateRunes = maxInt64(a.aggregateRunes, aggregateRunes)
}

func (a *StreamAnalyzer) observeResponsesItem(payload map[string]any, now time.Time) {
	item, _ := payload["item"].(map[string]any)
	if item == nil {
		return
	}
	runes := a.observeResponsesItemMap(item, now)
	a.aggregateRunes = maxInt64(a.aggregateRunes, runes)
}

func (a *StreamAnalyzer) observeResponsesItemMap(item map[string]any, now time.Time) int64 {
	itemType := strings.ToLower(strings.TrimSpace(asString(item["type"])))
	switch itemType {
	case "reasoning":
		if strings.TrimSpace(asString(item["id"])) != "" {
			a.reasoningStart = true
			a.signals.ReasoningStarted = true
			a.markFirstToken(now)
		}
		if enc := asString(item["encrypted_content"]); strings.TrimSpace(enc) != "" {
			a.addEncrypted(enc)
		}
		if !a.streaming {
			a.observeResponsesReasoningContent(item["summary"])
			a.observeResponsesReasoningContent(item["content"])
		}
		return 0
	case "message":
		return a.observeResponsesMessageContent(item["content"], now)
	case "":
		return 0
	default:
		a.signals.SemanticOutput = true
		a.markFirstToken(now)
		return 0
	}
}

func (a *StreamAnalyzer) observeResponsesReasoningContent(value any) {
	parts, ok := value.([]any)
	if !ok {
		return
	}
	for _, rawPart := range parts {
		part, _ := rawPart.(map[string]any)
		if part == nil {
			continue
		}
		a.addReasoningText(asString(part["text"]))
	}
}

func (a *StreamAnalyzer) observeResponsesMessageContent(value any, now time.Time) int64 {
	parts, ok := value.([]any)
	if !ok {
		if text := asString(value); strings.TrimSpace(text) != "" {
			return a.noteAggregateVisibleText(text, now, "")
		}
		return 0
	}
	var runes int64
	for _, rawPart := range parts {
		part, _ := rawPart.(map[string]any)
		if part == nil {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(asString(part["type"])))
		switch kind {
		case "output_text", "text", "refusal":
			runes += a.noteAggregateVisibleText(asString(part["text"]), now, kind)
		case "reasoning_text", "summary_text":
			a.addReasoningText(asString(part["text"]))
		case "tool_call", "function_call":
			a.signals.SemanticOutput = true
			a.markFirstToken(now)
		default:
			if kind != "" {
				a.signals.SemanticOutput = true
			}
		}
	}
	return runes
}

func (a *StreamAnalyzer) noteAggregateVisibleText(text string, now time.Time, kind string) int64 {
	_ = now
	_ = kind
	if text == "" {
		return 0
	}
	a.signals.SemanticOutput = true
	if !a.visibleDelta && a.retainText {
		a.visibleText.WriteString(text)
	}
	return int64(utf8.RuneCountInString(text))
}

func (a *StreamAnalyzer) observeAnthropic(payload map[string]any, now time.Time) {
	eventName := strings.ToLower(strings.TrimSpace(asString(payload["type"])))
	a.observeUsage(payload)
	switch eventName {
	case "message_stop":
		a.signals.Terminal = true
		a.terminalAt = now
	case "", "message":
		if content, ok := payload["content"]; ok {
			a.observeAnthropicContent(content, now)
		}
		if stopReason := strings.TrimSpace(asString(payload["stop_reason"])); stopReason != "" {
			a.signals.Terminal = true
			a.terminalAt = now
		}
	case "content_block_start":
		block, _ := payload["content_block"].(map[string]any)
		if block == nil {
			return
		}
		switch strings.ToLower(asString(block["type"])) {
		case "thinking":
			a.reasoningStart = true
			a.signals.ReasoningStarted = true
			a.markFirstToken(now)
			if text := asString(block["thinking"]); strings.TrimSpace(text) != "" {
				a.addReasoningText(text)
			}
		case "redacted_thinking":
			a.reasoningStart = true
			a.signals.ReasoningStarted = true
			a.addEncrypted(asString(block["data"]))
		case "text":
			a.addVisibleText(asString(block["text"]), now)
			a.signals.SemanticOutput = true
		case "tool_use", "server_tool_use":
			a.signals.SemanticOutput = true
			a.markFirstToken(now)
		}
	case "content_block_delta":
		delta, _ := payload["delta"].(map[string]any)
		if delta == nil {
			return
		}
		switch strings.ToLower(asString(delta["type"])) {
		case "thinking_delta":
			a.addReasoningText(firstString(delta, "thinking", "text"))
		case "signature_delta":
			a.reasoningStart = true
			a.signals.ReasoningStarted = true
			a.addEncrypted(asString(delta["signature"]))
		case "text_delta":
			a.addVisibleText(asString(delta["text"]), now)
		case "input_json_delta":
			if strings.TrimSpace(asString(delta["partial_json"])) != "" {
				a.signals.SemanticOutput = true
				a.markFirstToken(now)
			}
		}
	}
}
func (a *StreamAnalyzer) observeAnthropicContent(value any, now time.Time) {
	parts, ok := value.([]any)
	if !ok {
		return
	}
	for _, rawPart := range parts {
		part, _ := rawPart.(map[string]any)
		if part == nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(asString(part["type"]))) {
		case "thinking":
			a.reasoningStart = true
			a.signals.ReasoningStarted = true
			a.addReasoningText(firstString(part, "thinking", "text"))
		case "redacted_thinking":
			a.reasoningStart = true
			a.signals.ReasoningStarted = true
			a.addEncrypted(asString(part["data"]))
		case "text":
			a.addVisibleText(asString(part["text"]), now)
			a.signals.SemanticOutput = true
		case "tool_use", "server_tool_use":
			a.signals.SemanticOutput = true
			a.markFirstToken(now)
		}
	}
}

func (a *StreamAnalyzer) addContentParts(value any, now time.Time) {
	switch typed := value.(type) {
	case string:
		a.addVisibleText(typed, now)
	case []any:
		for _, rawPart := range typed {
			part, _ := rawPart.(map[string]any)
			if part == nil {
				continue
			}
			kind := strings.ToLower(asString(part["type"]))
			switch kind {
			case "output_text", "text":
				a.addVisibleText(asString(part["text"]), now)
			case "reasoning_text", "summary_text":
				a.addReasoningText(asString(part["text"]))
			case "tool_call", "function_call":
				a.signals.SemanticOutput = true
				a.markFirstToken(now)
			case "refusal":
				a.addVisibleText(asString(part["text"]), now)
				a.signals.SemanticOutput = true
			}
		}
	}
}

func (a *StreamAnalyzer) addReasoningText(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	a.hasThinking = true
	a.reasoningDelta = true
	a.signals.HasReasoningDelta = true
	a.signals.ReasoningStarted = true
	a.markFirstToken(time.Now())
}

func (a *StreamAnalyzer) addVisibleText(text string, now time.Time) {
	if text == "" {
		return
	}
	a.visibleDelta = true
	if a.firstVisibleAt.IsZero() {
		a.firstVisibleAt = now
		a.signals.FirstVisible = true
	}
	a.lastVisibleAt = now
	a.visibleChunks++
	if a.retainText {
		a.visibleText.WriteString(text)
	}
	a.visibleRunes += int64(utf8.RuneCountInString(text))
	a.signals.VisibleRunes = maxInt64(a.signals.VisibleRunes, a.visibleRunes)
	a.signals.VisibleTokens = (a.signals.VisibleRunes + 3) / 4
	a.markFirstToken(now)
}

func (a *StreamAnalyzer) addEncrypted(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	a.markFirstToken(time.Now())
	if size := int64(len([]byte(value))); size > a.encryptedBytes {
		a.encryptedBytes = size
	}
	a.signals.EncryptedBytes = a.encryptedBytes
}

func (a *StreamAnalyzer) markFirstToken(now time.Time) {
	if a.firstTokenAt.IsZero() {
		a.firstTokenAt = now
	}
}

func (a *StreamAnalyzer) observeUsage(payload map[string]any) {
	if payload == nil {
		return
	}
	usage, _ := payload["usage"].(map[string]any)
	if usage == nil {
		if response, ok := payload["response"].(map[string]any); ok {
			usage, _ = response["usage"].(map[string]any)
		}
	}
	if usage == nil {
		return
	}
	a.signals.UsageReported = true
	if value := firstInt(usage, "completion_tokens", "output_tokens"); value > a.signals.OutputTokens {
		a.signals.OutputTokens = value
	}
	for _, key := range []string{"completion_tokens_details", "output_tokens_details"} {
		if detail, ok := usage[key].(map[string]any); ok {
			if value := firstInt(detail, "reasoning_tokens", "thinking_tokens"); value > a.signals.ReasoningTokens {
				a.signals.ReasoningTokens = value
			}
		}
	}
	if value := firstInt(usage, "reasoning_tokens", "thinking_tokens"); value > a.signals.ReasoningTokens {
		a.signals.ReasoningTokens = value
	}
}

func (a *StreamAnalyzer) snapshot(final bool) Signals {
	if a == nil {
		return Signals{}
	}
	sig := a.signals
	sig.Protocol = a.protocol
	sig.Streaming = a.streaming
	sig.EncryptedBytes = a.encryptedBytes
	sig.ReasoningStarted = a.reasoningStart || sig.ReasoningStarted || sig.HasThinking
	floor := encryptedFloorWithConfig(sig.ReasoningTokens, a.minEncrypted, a.bytesPerToken)
	sig.EncryptedFloor = floor
	sig.HasThinking = a.reasoningDelta || sig.HasReasoningDelta || (sig.EncryptedBytes > 0 && sig.EncryptedBytes >= floor)
	sig.VisibleRunes = maxInt64(sig.VisibleRunes, a.aggregateRunes)
	sig.VisibleTokens = (sig.VisibleRunes + 3) / 4
	if sig.OutputTokens <= 0 && final {
		sig.OutputTokens = sig.VisibleTokens
	}
	if a.firstTokenAt.IsZero() {
		sig.FirstTokenMS = 0
		sig.FirstTokenKnown = false
	} else {
		sig.FirstTokenMS = durationMS(a.startedAt, a.firstTokenAt)
		sig.FirstTokenKnown = true
	}
	end := a.terminalAt
	if end.IsZero() {
		end = time.Now()
	}
	if !a.firstVisibleAt.IsZero() {
		sig.FirstVisible = true
		sig.VisibleFlushMS = durationMS(a.firstVisibleAt, end)
		if sig.VisibleFlushMS < 0 {
			sig.VisibleFlushMS = 0
		}
		sig.VisibleSpanMS = durationMS(a.firstVisibleAt, a.lastVisibleAt)
		sig.VisibleChunks = a.visibleChunks
	}
	sig.DurationMS = durationMS(a.startedAt, end)
	sig.GenerationMS = generationWindowMS(sig.DurationMS, sig.FirstTokenMS, sig.ReasoningTokens)
	sig.TPS = tps(sig.OutputTokens, sig.GenerationMS)
	if final && a.retainText {
		sig.VisibleText = a.visibleText.String()
	}
	sig.ChunkCount = a.signals.ChunkCount
	sig.ByteCount = a.signals.ByteCount
	sig.EventCount = a.signals.EventCount
	sig.ParsedEvents = a.signals.ParsedEvents
	sig.UnknownEvents = a.signals.UnknownEvents
	sig.ParseFailures = a.signals.ParseFailures
	return sig
}

func (a *StreamAnalyzer) Finalize(ended bool) Signals {
	if a == nil {
		return Signals{}
	}
	for _, ev := range a.parser.Flush() {
		a.observeEvent(ev, time.Now())
	}
	if ended {
		a.signals.Terminal = true
		if a.terminalAt.IsZero() {
			a.terminalAt = time.Now()
		}
	}
	return a.snapshot(true)
}
