package otelrecv

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReconcileGapAndTranscript(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "projects", "-tmp-work")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"SECRET_TRANSCRIPT"}}`,
		`{"type":"assistant","message":{"id":"msg_1","model":"claude-haiku","content":[{"type":"text","text":"SECRET_TRANSCRIPT"}],"usage":{"input_tokens":80,"output_tokens":20,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		`{"type":"assistant","message":{"id":"msg_1","model":"claude-haiku","content":"SECRET_TRANSCRIPT","usage":{"input_tokens":80,"output_tokens":20,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		`{"type":"assistant","isSidechain":true,"message":{"id":"msg_2","model":"claude-haiku","content":"SECRET_TRANSCRIPT","usage":{"input_tokens":10,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		`{"type":"cost-state","modelUsage":{"claude-haiku":{"inputTokens":300,"outputTokens":50,"cacheReadInputTokens":20,"cacheCreationInputTokens":5}}}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(sessionDir, testSession+".jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}

	seq := int64(4)
	records := []Request{
		{SessionID: testSession, Model: "claude-haiku", QuerySource: "repl_main_thread", InputTokens: 80, OutputTokens: 20, CostUSD: 0.5, RequestID: "req_main"},
		{SessionID: testSession, Model: "claude-haiku", QuerySource: "agent_summary", InputTokens: 200, OutputTokens: 5, CacheReadTokens: 1000, CostUSD: 0.25, RequestID: "req_sum", EventSequence: &seq},
		{SessionID: testSession, Model: "claude-haiku", QuerySource: "agent_summary", InputTokens: 200, OutputTokens: 5, CacheReadTokens: 1000, CostUSD: 0.25, RequestID: "req_sum", EventSequence: &seq},
		{SessionID: testSession, QuerySource: "compact", InputTokens: 1, RequestID: "req_compact"},
	}
	// The duplicate request id is collapsed by Read, which is what the command uses.
	var file bytes.Buffer
	for _, rec := range records {
		if err := encodeRequest(&file, rec); err != nil {
			t.Fatal(err)
		}
	}
	file.WriteString(`{"prompt":"` + testSecret + `"` + "\n")
	gotRecs, skipped, err := Read(&file)
	if err != nil || skipped != 1 || len(gotRecs) != 3 {
		t.Fatalf("read = %d skipped %d err %v", len(gotRecs), skipped, err)
	}

	var out bytes.Buffer
	if err := WriteReconcile(&out, gotRecs, func(id string) (SessionUsage, error) {
		return LoadTranscript(id, dir)
	}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Contains(text, "SECRET_TRANSCRIPT") || strings.Contains(text, testSecret) {
		t.Fatalf("reconcile leaked text:\n%s", text)
	}
	for _, want := range []string{
		"telemetry: 1306 tokens, 3 requests",
		"agent_summary: 1205 tokens, 1 request",
		"repl_main_thread: 100 tokens, 1 request",
		"compact: 1 tokens, 1 request",
		"transcript: 110 tokens, 2 messages, 0 subagent files",
		"gap telemetry-transcript: 1206",
		"untranscribed agent_summary: 1205 tokens",
		"untranscribed compact: 1 tokens",
		"modelUsage: 375 tokens",
		"gap telemetry-modelUsage: 931",
		"agent_summary requests: 1",
		"other non-main requests: 1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
}

func TestReconcileCountsSubagentsAndLeavesHiddenSources(t *testing.T) {
	// Same shape as the Mac run: subagent tokens are on disk, and the gap
	// is only agent_summary, prompt_suggestion, and generate_session_title.
	dir := t.TempDir()
	project := filepath.Join(dir, "projects", "-work")
	sessionDir := filepath.Join(project, testSession)
	if err := os.MkdirAll(filepath.Join(sessionDir, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	main := strings.Join([]string{
		`{"type":"assistant","requestId":"req_main","message":{"id":"msg_main","model":"claude-haiku","usage":{"input_tokens":80,"output_tokens":20,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		`{"type":"assistant","requestId":"req_main","message":{"id":"msg_main","model":"claude-haiku","usage":{"input_tokens":80,"output_tokens":20,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		`{"type":"cost-state","modelUsage":{"claude-haiku":{"inputTokens":371,"outputTokens":0,"cacheReadInputTokens":0,"cacheCreationInputTokens":0}}}`,
		"",
	}, "\n")
	sub := strings.Join([]string{
		`{"type":"assistant","requestId":"req_main","message":{"id":"msg_main","model":"claude-haiku","usage":{"input_tokens":9999,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		`{"type":"assistant","requestId":"req_sub","message":{"id":"msg_sub","model":"claude-haiku","content":"SECRET_TRANSCRIPT","usage":{"input_tokens":150,"output_tokens":30,"cache_read_input_tokens":20,"cache_creation_input_tokens":0}}}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(project, testSession+".jsonl"), []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "subagents", "agent-1.jsonl"), []byte(sub), 0o600); err != nil {
		t.Fatal(err)
	}
	records := []Request{
		{SessionID: testSession, QuerySource: "repl_main_thread", InputTokens: 80, OutputTokens: 20, RequestID: "req_main"},
		{SessionID: testSession, QuerySource: "agent:builtin:general-purpose", InputTokens: 150, OutputTokens: 30, CacheReadTokens: 20, RequestID: "req_sub"},
		{SessionID: testSession, QuerySource: "agent_summary", InputTokens: 40, OutputTokens: 5, RequestID: "req_sum"},
		{SessionID: testSession, QuerySource: "prompt_suggestion", InputTokens: 20, OutputTokens: 4, RequestID: "req_prompt"},
		{SessionID: testSession, QuerySource: "generate_session_title", InputTokens: 1, OutputTokens: 1, RequestID: "req_title"},
	}
	var out bytes.Buffer
	if err := WriteReconcile(&out, records, func(id string) (SessionUsage, error) {
		return LoadTranscript(id, dir)
	}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Contains(text, "SECRET_TRANSCRIPT") {
		t.Fatalf("leaked transcript text:\n%s", text)
	}
	// main 100 + subagent 200 = 300. Hidden 45+24+2 = 71. modelUsage 385.
	for _, want := range []string{
		"telemetry: 371 tokens, 5 requests",
		"transcript: 300 tokens, 2 messages, 1 subagent files",
		"gap telemetry-transcript: 71",
		"untranscribed agent_summary: 45 tokens",
		"untranscribed prompt_suggestion: 24 tokens",
		"untranscribed generate_session_title: 2 tokens",
		"modelUsage: 371 tokens",
		"gap telemetry-modelUsage: 0",
		"agent_summary requests: 1",
		"other non-main requests: 3",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "untranscribed agent:builtin") {
		t.Fatalf("subagent source was treated as untranscribed:\n%s", text)
	}
}

func TestReconcileSkipsNonUUIDWithoutReading(t *testing.T) {
	var out bytes.Buffer
	err := WriteReconcile(&out, []Request{{SessionID: "not-a-session", QuerySource: "agent_summary", InputTokens: 4}}, func(string) (SessionUsage, error) {
		t.Fatal("lookup called for a non-uuid session")
		return SessionUsage{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not read") || !strings.Contains(out.String(), "agent_summary requests: 1") {
		t.Fatalf("output = %s", out.String())
	}
}

func TestTranscriptSkipsOversizedLine(t *testing.T) {
	var body bytes.Buffer
	body.WriteString(`{"type":"assistant","message":{"id":"msg_1","model":"m","usage":{"input_tokens":2,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}` + "\n")
	body.WriteString(`{"type":"user","message":{"content":"`)
	body.Write(bytes.Repeat([]byte("x"), 2000))
	body.WriteString("\"}}\n")
	body.WriteString(`{"type":"assistant","message":{"id":"msg_2","model":"m","usage":{"input_tokens":3,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}` + "\n")
	reader := bufio.NewReader(&body)
	var usage SessionUsage
	var model SessionUsage
	var found bool
	for {
		line, err := readCappedLine(reader, 512)
		if len(line) > 0 {
			trackTranscriptLine(line, &usage, &model, &found)
		}
		if err != nil {
			break
		}
	}
	if usage.Total() != 5 || usage.Messages != 2 {
		t.Fatalf("usage = %+v", usage)
	}
}
