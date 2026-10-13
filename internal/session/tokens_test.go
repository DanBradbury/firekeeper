package session

import (
	"bufio"
	"strings"
	"testing"
)

func TestScanRolloutStatusTokens(t *testing.T) {
	lines := strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"task_started"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":null}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1200,"cached_input_tokens":1000,"output_tokens":40,"total_tokens":1240}}}}`,
		`not json`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1500,"cached_input_tokens":1200,"output_tokens":65,"total_tokens":1565}}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":9}}}}`,
		`{"type":"event_msg","payload":{"type":"task_complete"}}`,
	}, "\n")
	state, tokens, err := (&discoverer{}).scanRolloutStatus(bufio.NewReader(strings.NewReader(lines)))
	if err != nil {
		t.Fatal(err)
	}
	if state != SessionStateWaiting {
		t.Fatalf("state = %s", state)
	}
	if want := (TokenBreakdown{Input: 300, Output: 65, Cache: 1200}); tokens != want {
		t.Fatalf("tokens = %+v, want %+v", tokens, want)
	}
	if tokens.Total() != 1565 {
		t.Fatalf("total = %d, want the rollout's total_tokens", tokens.Total())
	}
}

func TestScanCopilotEventsTokens(t *testing.T) {
	lines := strings.Join([]string{
		`{"type":"session.start","data":{}}`,
		`{"type":"model.model_call_success","data":{"responseChunk":{"usage":{"prompt_tokens":1000,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":800}}}}}`,
		`{"type":"model.model_call_success","data":{"responseChunk":{"usage":{"prompt_tokens":200,"completion_tokens":5}}}}`,
		`{"type":"model.model_call_success","data":{"responseChunk":{}}}`,
		`{"type":"assistant.message","data":{"outputTokens":55,"responseChunk":{"usage":{"prompt_tokens":999}}}}`,
	}, "\n")
	meta, err := (&discoverer{}).scanCopilotEvents(strings.NewReader(lines))
	if err != nil {
		t.Fatal(err)
	}
	if want := (TokenBreakdown{Input: 400, Output: 55, Cache: 800}); meta.Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", meta.Tokens, want)
	}
}

func TestScanClaudeTranscriptTokenBreakdown(t *testing.T) {
	lines := strings.Join([]string{
		`{"type":"assistant","message":{"id":"msg_1","usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}}`,
		`{"type":"assistant","message":{"id":"msg_1","usage":{"input_tokens":10,"output_tokens":25,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}}`,
		`{"type":"cost-state","modelUsage":{"a":{"inputTokens":100,"outputTokens":200,"cacheReadInputTokens":300,"cacheCreationInputTokens":400}}}`,
		`{"type":"assistant","message":{"id":"msg_2","usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}}`,
	}, "\n") + "\n"
	meta, err := (&discoverer{}).scanClaudeTranscript(strings.NewReader(lines), false)
	if err != nil {
		t.Fatal(err)
	}
	if want := (TokenBreakdown{Input: 101, Output: 202, Cache: 707}); meta.Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", meta.Tokens, want)
	}
	if meta.TokensUsed != meta.Tokens.Total() {
		t.Fatalf("TokensUsed = %d, want %d", meta.TokensUsed, meta.Tokens.Total())
	}
}
