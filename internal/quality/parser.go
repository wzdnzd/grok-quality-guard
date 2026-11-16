package quality

import (
	"bytes"
	"encoding/json"
	"strings"
)

type SSEEvent struct {
	Event   string
	Data    string
	Comment string
}

type SSEParser struct {
	buffer []byte
	event  string
}

// Feed consumes host model stream chunks. CPA forwards each executor SSE line
// without its trailing newline, so complete field lines must be handled as soon
// as they arrive instead of waiting for a newline that never comes.
func (p *SSEParser) Feed(chunk []byte) []SSEEvent {
	if p == nil || len(chunk) == 0 {
		return nil
	}
	p.buffer = append(p.buffer, chunk...)
	var events []SSEEvent
	for {
		idx := bytes.IndexByte(p.buffer, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSuffix(string(p.buffer[:idx]), "\r")
		p.buffer = p.buffer[idx+1:]
		if ev, ok := p.processLine(line); ok {
			events = append(events, ev)
		}
	}
	if remainder := strings.TrimSuffix(string(p.buffer), "\r"); remainder != "" && sseLineComplete(remainder) {
		p.buffer = nil
		if ev, ok := p.processLine(remainder); ok {
			events = append(events, ev)
		}
	}
	return events
}

func (p *SSEParser) Flush() []SSEEvent {
	if p == nil {
		return nil
	}
	var events []SSEEvent
	if len(p.buffer) > 0 {
		line := strings.TrimSuffix(string(p.buffer), "\r")
		p.buffer = nil
		if ev, ok := p.processLine(line); ok {
			events = append(events, ev)
		}
	}
	p.event = ""
	return events
}

// sseLineComplete reports whether an unterminated chunk is already a complete
// SSE field line. JSON is accepted only when it parses, so a payload split
// across chunks stays buffered.
func sseLineComplete(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	if strings.HasPrefix(line, "event:") || strings.HasPrefix(line, ":") {
		return true
	}
	if data, ok := strings.CutPrefix(line, "data:"); ok {
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			return true
		}
		if !jsonCandidateComplete(data) {
			return false
		}
		return json.Valid([]byte(data))
	}
	if line == "[DONE]" {
		return true
	}
	if line[0] == '{' || line[0] == '[' {
		if !jsonCandidateComplete(line) {
			return false
		}
		return json.Valid([]byte(line))
	}
	return false
}

// jsonCandidateComplete cheaply rejects obviously partial JSON before the
// full json.Valid scan. It avoids re-validating a growing buffer for every
// chunk of a large SSE data line.
func jsonCandidateComplete(value string) bool {
	if value == "" {
		return false
	}
	last := value[len(value)-1]
	return last == '}' || last == ']'
}

func (p *SSEParser) processLine(line string) (SSEEvent, bool) {
	if line == "" {
		// A blank line terminates an SSE frame without carrying data.
		p.event = ""
		return SSEEvent{}, false
	}
	if strings.HasPrefix(line, ":") {
		return SSEEvent{Comment: strings.TrimSpace(strings.TrimPrefix(line, ":"))}, true
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "[DONE]" || ((strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(trimmed))) {
		ev := SSEEvent{Event: p.event, Data: trimmed}
		p.event = ""
		return ev, true
	}
	field, value, _ := strings.Cut(line, ":")
	value = strings.TrimPrefix(value, " ")
	if field == "event" {
		p.event = value
		return SSEEvent{}, false
	}
	if field != "data" {
		return SSEEvent{}, false
	}
	data := strings.TrimSpace(value)
	if data == "" {
		return SSEEvent{}, false
	}
	ev := SSEEvent{Event: p.event, Data: data}
	p.event = ""
	return ev, true
}
