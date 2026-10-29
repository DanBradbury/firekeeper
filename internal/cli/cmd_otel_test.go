package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOtelCommandsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code, handled := RunSubcommand([]string{"otel-listen", "--help"}, &stdout, &stderr); !handled || code != 0 {
		t.Fatalf("help = %d handled %v stderr %s", code, handled, stderr.String())
	}
	if !strings.Contains(stderr.String(), "127.0.0.1") || strings.Contains(stderr.String()+stdout.String(), "SECRET") {
		t.Fatalf("help = %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code, _ := RunSubcommand([]string{"otel-listen", "--port", "99999"}, &stdout, &stderr); code != 2 {
		t.Fatalf("bad port = %d %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code, handled := RunSubcommand([]string{"otel-reconcile"}, &stdout, &stderr); !handled || code != 2 {
		t.Fatalf("reconcile usage = %d handled %v", code, handled)
	}
}

func TestOtelReconcileCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	dir := filepath.Join(home, "projects", "-work")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "22222222-2222-4222-8222-222222222222"
	transcript := `{"type":"user","message":{"content":"SECRET_TRANSCRIPT"}}
{"type":"assistant","message":{"id":"msg_1","model":"claude-haiku","content":"SECRET_TRANSCRIPT","usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}
`
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	jsonl := filepath.Join(t.TempDir(), "otel.jsonl")
	line := `{"ts":"2026-10-29T15:04:05Z","session_id":"` + id + `","model":"claude-haiku","query_source":"agent_summary","input_tokens":40,"output_tokens":2,"cache_read_tokens":0,"cache_creation_tokens":0,"cost_usd":0.25,"request_id":"req_sum","prompt":"SECRET_OTEL_PROMPT"}` + "\n"
	if err := os.WriteFile(jsonl, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runOtelReconcile([]string{jsonl}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d stderr %s", code, stderr.String())
	}
	text := stdout.String() + stderr.String()
	if strings.Contains(text, "SECRET_TRANSCRIPT") || strings.Contains(text, "SECRET_OTEL_PROMPT") {
		t.Fatalf("leaked text:\n%s", text)
	}
	if !strings.Contains(stdout.String(), "gap telemetry-transcript: 42") || !strings.Contains(stdout.String(), "agent_summary requests: 1") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}
