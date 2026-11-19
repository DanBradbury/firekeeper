package session

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeEffort(t *testing.T) {
	tests := []struct{ in, want string }{
		{"high", "high"},
		{" Medium ", "medium"},
		{"xhigh", "xhigh"},
		{"", ""},
		{"   ", ""},
		{"high\x1b[31m", ""},
		{"two words", ""},
		{strings.Repeat("a", maxEffortLength+1), ""},
	}
	for _, tt := range tests {
		if got := normalizeEffort(tt.in); got != tt.want {
			t.Errorf("normalizeEffort(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func FuzzNormalizeEffort(f *testing.F) {
	for _, seed := range []string{"high", "", "\x1b[0m", "a b", "Ünï"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		got := normalizeEffort(value)
		if len(got) > maxEffortLength {
			t.Fatalf("effort %q is too long", got)
		}
		for _, r := range got {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
				t.Fatalf("effort %q holds %q", got, r)
			}
		}
	})
}

func TestRolloutEffortUsesLatestTurnContext(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"latest wins", []string{
			`{"type":"turn_context","payload":{"effort":"medium"}}`,
			`{"type":"turn_context","payload":{"effort":"high"}}`,
		}, "high"},
		{"turn without effort keeps last", []string{
			`{"type":"turn_context","payload":{"effort":"low"}}`,
			`{"type":"turn_context","payload":{}}`,
		}, "low"},
		{"none recorded", []string{`{"type":"event_msg","payload":{"type":"task_started"}}`}, ""},
		{"invalid value ignored", []string{`{"type":"turn_context","payload":{"effort":"\u001b[31mhigh"}}`}, ""},
		{"non-string value ignored", []string{
			`{"type":"turn_context","payload":{"effort":{"x":1}}}`,
			`not json`,
		}, ""},
		{"non-string value keeps rest of the record", []string{
			`{"type":"turn_context","payload":{"effort":7}}`,
			`{"type":"turn_context","payload":{"effort":"low"}}`,
		}, "low"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, effort, err := (&discoverer{}).scanRolloutStatus(bufio.NewReader(strings.NewReader(strings.Join(tt.lines, "\n"))))
			if err != nil {
				t.Fatal(err)
			}
			if effort != tt.want {
				t.Fatalf("effort = %q, want %q", effort, tt.want)
			}
		})
	}
}

func TestClaudeEffortUsesLatestAssistantRecord(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"latest wins", []string{
			`{"type":"assistant","effort":"medium","message":{"model":"m"}}` + "\n",
			`{"type":"assistant","effort":"high","message":{"model":"m"}}` + "\n",
		}, "high"},
		{"sidechain ignored", []string{
			`{"type":"assistant","effort":"low","message":{"model":"m"}}` + "\n",
			`{"type":"assistant","isSidechain":true,"effort":"max","message":{"model":"m"}}` + "\n",
		}, "low"},
		{"none recorded", []string{`{"type":"assistant","message":{"model":"m"}}` + "\n"}, ""},
		{"invalid value ignored", []string{`{"type":"assistant","effort":"a b","message":{"model":"m"}}` + "\n"}, ""},
		{"non-string value keeps the record", []string{`{"type":"assistant","effort":{"x":1},"message":{"model":"m"}}` + "\n"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, err := (&discoverer{}).scanClaudeTranscript(strings.NewReader(strings.Join(tt.lines, "")), false)
			if err != nil {
				t.Fatal(err)
			}
			if meta.Effort != tt.want {
				t.Fatalf("effort = %q, want %q", meta.Effort, tt.want)
			}
		})
	}
}

func TestCopilotEffortFollowsModelChange(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"set by model change", []string{
			`{"type":"session.model_change","data":{"newModel":"a","reasoningEffort":"medium"}}`,
		}, "medium"},
		{"later change replaces", []string{
			`{"type":"session.model_change","data":{"newModel":"a","reasoningEffort":"medium"}}`,
			`{"type":"session.model_change","data":{"newModel":"a","reasoningEffort":"high"}}`,
		}, "high"},
		{"model without effort clears it", []string{
			`{"type":"session.model_change","data":{"newModel":"a","reasoningEffort":"medium"}}`,
			`{"type":"session.model_change","data":{"newModel":"b","reasoningEffort":null}}`,
		}, ""},
		{"other events do not set it", []string{
			`{"type":"assistant.turn_start","data":{"reasoningEffort":"high"}}`,
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, err := (&discoverer{}).scanCopilotEvents(strings.NewReader(strings.Join(tt.lines, "\n")))
			if err != nil {
				t.Fatal(err)
			}
			if meta.Effort != tt.want {
				t.Fatalf("effort = %q, want %q", meta.Effort, tt.want)
			}
		})
	}
}

func TestNonStringEffortKeepsRecordData(t *testing.T) {
	meta, err := (&discoverer{}).scanClaudeTranscript(strings.NewReader(
		`{"type":"assistant","effort":{"x":1},"message":{"id":"a","model":"m","usage":{"input_tokens":5,"output_tokens":2}}}`+"\n"), false)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Model != "m" || meta.Tokens.Total() != 7 || meta.Effort != "" {
		t.Fatalf("model=%q tokens=%d effort=%q", meta.Model, meta.Tokens.Total(), meta.Effort)
	}
	copilot, err := (&discoverer{}).scanCopilotEvents(strings.NewReader(
		`{"type":"session.model_change","data":{"newModel":"a","reasoningEffort":{"x":1}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if copilot.Model != "a" || copilot.Effort != "" {
		t.Fatalf("model=%q effort=%q", copilot.Model, copilot.Effort)
	}
}

func TestScanCopilotEffortLargeFileUsesHeadAndTail(t *testing.T) {
	dir := t.TempDir()
	const id = "01a1f251-fac4-7d23-81bd-b401e5199391"
	path := filepath.Join(dir, "session-state", id)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	filler := `{"type":"assistant.message","data":{"text":"` + strings.Repeat("x", 1000) + `"}}` + "\n"
	build := func(head, tail string) {
		t.Helper()
		var b strings.Builder
		b.WriteString(head + "\n")
		for int64(b.Len()) < CopilotEffortHeadBytes+CopilotEventTailBytes+(1<<20) {
			b.WriteString(filler)
		}
		b.WriteString(tail)
		if err := os.WriteFile(filepath.Join(path, "events.jsonl"), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	change := func(effort string) string {
		return `{"type":"session.model_change","data":{"newModel":"a","reasoningEffort":"` + effort + `"}}` + "\n"
	}
	build(change("low"), "")
	if got := CopilotSessionEffort(dir, id); got != "low" {
		t.Fatalf("head-only effort = %q, want low", got)
	}
	build(change("low"), change("high"))
	if got := CopilotSessionEffort(dir, id); got != "high" {
		t.Fatalf("tail effort = %q, want high", got)
	}
}

func FuzzScanCopilotEffort(f *testing.F) {
	f.Add(`{"type":"session.model_change","data":{"reasoningEffort":"high"}}`)
	f.Add(`{"type":"session.model_change","data":{"reasoningEffort":{"x":1}}}`)
	f.Add("not json\n\x00")
	f.Fuzz(func(t *testing.T, input string) {
		effort, _ := scanCopilotEffort(strings.NewReader(input))
		if effort != normalizeEffort(effort) {
			t.Fatalf("effort %q is not normalized", effort)
		}
	})
}
