package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageModelLabel(t *testing.T) {
	tests := []struct {
		name    string
		session usageSession
		want    string
	}{
		{"model and effort", usageSession{Model: "gpt-6.1-sol", Effort: "high"}, "gpt-6.1-sol · high"},
		{"model only", usageSession{Model: "gpt-6.1-sol"}, "gpt-6.1-sol"},
		{"effort without model", usageSession{Effort: "high"}, ""},
		{"nothing", usageSession{}, ""},
	}
	for _, tt := range tests {
		if got := usageModelLabel(tt.session); got != tt.want {
			t.Errorf("%s: label = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestUsageSessionDetailShowsEffort(t *testing.T) {
	page := usagePage{Sessions: []usageSession{{ID: "s", Title: "t", Model: "gpt-6.1-sol", Effort: "xhigh"}}}
	text := strings.Join(usageSessionDetailLines(page), "\n")
	if !strings.Contains(text, "gpt-6.1-sol · xhigh") {
		t.Fatalf("detail lacks the effort:\n%s", text)
	}
	page.Sessions[0].Effort = ""
	text = strings.Join(usageSessionDetailLines(page), "\n")
	if strings.Contains(text, "·") {
		t.Fatalf("detail shows a separator without an effort:\n%s", text)
	}
}

func TestCodexHistoryReadsReasoningEffort(t *testing.T) {
	query, err := codexHistoryQuery(map[string]bool{"id": true, "reasoning_effort": true})
	if err != nil || !strings.Contains(query, "NULLIF(reasoning_effort, '')") {
		t.Fatalf("query = %q, err = %v", query, err)
	}
	query, err = codexHistoryQuery(map[string]bool{"id": true})
	if err != nil || strings.Contains(query, "NULLIF(reasoning_effort") {
		t.Fatalf("schema without the column referenced it: %q, err = %v", query, err)
	}
	sessions, err := decodeCodexHistoricalSessions([]byte(`[{"id":"a","effort":"High"},{"id":"b","effort":"\u001b[31mx"},{"id":"c"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].Effort != "high" || sessions[1].Effort != "" || sessions[2].Effort != "" {
		t.Fatalf("efforts = %q %q %q", sessions[0].Effort, sessions[1].Effort, sessions[2].Effort)
	}
}

func TestClaudeUsageLineTracksLatestEffort(t *testing.T) {
	var session claudeHistoricalSession
	summary := &claudeUsageSummary{}
	models, daily, seen := map[string]int64{}, map[string]int64{}, map[string]bool{}
	for _, line := range []string{
		`{"type":"assistant","effort":"medium","message":{"id":"1","model":"m","usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"assistant","effort":{"x":1},"message":{"id":"2","model":"m","usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"assistant","effort":"high","message":{"id":"3","model":"m","usage":{"input_tokens":1,"output_tokens":1}}}`,
	} {
		trackClaudeUsageLine([]byte(line), &session, summary, models, daily, seen)
	}
	if session.Effort != "high" || summary.Messages != 3 {
		t.Fatalf("effort = %q, messages = %d", session.Effort, summary.Messages)
	}
	trackClaudeUsageLine([]byte(`{"type":"assistant","effort":"a b","message":{"id":"4","model":"m","usage":{"input_tokens":1}}}`), &session, summary, models, daily, seen)
	if session.Effort != "high" {
		t.Fatalf("invalid effort replaced a good one: %q", session.Effort)
	}
}

func TestCopilotSessionEffortReadsEventsFile(t *testing.T) {
	home := t.TempDir()
	const id = "01a1f251-fac4-7d23-81bd-b401e5199391"
	dir := filepath.Join(home, "session-state", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(lines ...string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(
		`{"type":"session.model_change","data":{"newModel":"a","reasoningEffort":"medium"}}`,
		`{"type":"user.message","data":{}}`,
		`{"type":"session.model_change","data":{"newModel":"b","reasoningEffort":"high"}}`,
	)
	if got := copilotSessionEffort(home, id); got != "high" {
		t.Fatalf("effort = %q, want high", got)
	}
	write(`{"type":"session.model_change","data":{"newModel":"a","reasoningEffort":"medium"}}`, `{"type":"session.model_change","data":{"newModel":"b","reasoningEffort":null}}`)
	if got := copilotSessionEffort(home, id); got != "" {
		t.Fatalf("model without effort left %q", got)
	}
	write(`{"type":"user.message","data":{}}`, `not json`)
	if got := copilotSessionEffort(home, id); got != "" {
		t.Fatalf("no model change gave %q", got)
	}
	// Not a session id: the path must never be built from it.
	for _, bad := range []string{"", "../" + id, "not-a-uuid"} {
		if got := copilotSessionEffort(home, bad); got != "" {
			t.Fatalf("id %q gave %q", bad, got)
		}
	}
	if got := copilotSessionEffort(t.TempDir(), id); got != "" {
		t.Fatalf("missing file gave %q", got)
	}
}
