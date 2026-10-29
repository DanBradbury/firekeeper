package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeUndercountWarning(t *testing.T) {
	path := filepath.Join(t.TempDir(), claudeIDA+".jsonl")
	if got := ClaudeUndercountWarningFor(path); got != "" {
		t.Fatalf("no subagents warning = %q", got)
	}
	dir := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := ClaudeUndercountWarningFor(path); got != "" {
		t.Fatalf("empty subagent dir warning = %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-alpha.jsonl"), []byte("synthetic-prompt-marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ClaudeUndercountWarningFor(path); got != ClaudeUndercountWarning || strings.Contains(got, "synthetic-prompt-marker") {
		t.Fatal("warning missing or included file contents")
	}
}

func TestClaudeUndercountWarningLeavesMeasuredTokens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, claudeIDA+".jsonl")
	writeClaudeTranscript(t, path, "/work/demo", "Measured")
	sub := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "agent-alpha.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := (&discoverer{}).readClaudeTranscriptMetadata(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.TokensUsed != 10 || info.Tokens.Total() != 10 {
		t.Fatalf("measured tokens = %d breakdown %d", info.TokensUsed, info.Tokens.Total())
	}
	if info.UndercountWarning != ClaudeUndercountWarning {
		t.Fatalf("warning = %q", info.UndercountWarning)
	}
	meta := Meta{Input: 10, Output: 20, Cache: 3, UndercountWarning: info.UndercountWarning}
	encoded, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got["input"] != float64(10) || got["output"] != float64(20) || got["cache"] != float64(3) {
		t.Fatalf("measured totals changed: %#v", got)
	}
	if got["undercount_warning"] != ClaudeUndercountWarning {
		t.Fatalf("snapshot warning = %#v", got["undercount_warning"])
	}
	if _, ok := got["agent_summary_estimate"]; ok {
		t.Fatal("snapshot still has an estimate")
	}
}
