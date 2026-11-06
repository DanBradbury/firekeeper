package otelrecv

import (
	"bytes"
	"encoding/json"
	"unicode"
	"unicode/utf8"
)

// maxAttrLen is the longest attribute value copied off an OTLP record.
// Model names, query sources, session ids, and request ids are short.
// Anything longer is dropped so a mis-labeled prompt cannot land in the file.
const maxAttrLen = 256

// Request is one Claude Code api_request, reduced to usage fields.
// Prompt text, tool input, and response text are not fields of this type.
type Request struct {
	TS                  string  `json:"ts,omitempty"`
	SessionID           string  `json:"session_id,omitempty"`
	Model               string  `json:"model,omitempty"`
	QuerySource         string  `json:"query_source,omitempty"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CostUSD             float64 `json:"cost_usd"`
	RequestID           string  `json:"request_id,omitempty"`
	EventSequence       *int64  `json:"event_sequence,omitempty"`
}

// Tokens is input + output + cache read + cache creation. Claude's
// input_tokens excludes cached tokens, matching Firekeeper's transcript totals.
func (r Request) Tokens() int64 {
	return r.InputTokens + r.OutputTokens + r.CacheReadTokens + r.CacheCreationTokens
}

// dedupeKey collapses a retried export of the same API request.
// request_id is unique per successful response. Without one, an identical
// record is treated as the retry.
func (r Request) dedupeKey() string {
	if r.RequestID != "" {
		return "id\x00" + r.SessionID + "\x00" + r.RequestID
	}
	body, err := json.Marshal(r)
	if err != nil {
		return "rec\x00"
	}
	return "rec\x00" + string(body)
}

func (r Request) clean() Request {
	r.TS = cleanString(r.TS)
	r.SessionID = cleanString(r.SessionID)
	r.Model = cleanString(r.Model)
	r.QuerySource = cleanString(r.QuerySource)
	r.RequestID = cleanString(r.RequestID)
	if r.InputTokens < 0 {
		r.InputTokens = 0
	}
	if r.OutputTokens < 0 {
		r.OutputTokens = 0
	}
	if r.CacheReadTokens < 0 {
		r.CacheReadTokens = 0
	}
	if r.CacheCreationTokens < 0 {
		r.CacheCreationTokens = 0
	}
	if r.CostUSD < 0 {
		r.CostUSD = 0
	}
	return r
}

// cleanString keeps value only when it is short, valid UTF-8, and entirely
// printable. Anything POSTed to the listener can land here, and these values
// are shown in the dashboard and printed by otel-listen, so a control
// character (an escape sequence that sets the clipboard or the window title,
// for example) or an invisible format character drops the whole value.
func cleanString(value string) string {
	if len(value) > maxAttrLen || !utf8.ValidString(value) {
		return ""
	}
	for _, r := range value {
		if !unicode.IsPrint(r) {
			return ""
		}
	}
	return value
}

// isMainQuerySource reports the query sources that are the main conversation.
// Log events use repl_main_thread. The token counter's coarser label is main.
// agent_summary, compact, and subagent names are not main.
func isMainQuerySource(source string) bool {
	switch source {
	case "repl_main_thread", "main":
		return true
	default:
		return false
	}
}

func decodeRequestLine(line []byte) (Request, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || len(line) > 1<<20 {
		return Request{}, false
	}
	var rec Request
	if err := json.Unmarshal(line, &rec); err != nil {
		return Request{}, false
	}
	return rec.clean(), true
}
