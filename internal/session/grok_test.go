package session

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const grokTestIDA = "0199c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d"
const grokTestIDB = "1a2b3c4d-0000-4000-8000-000000000001"

func grokTestLog(t *testing.T, home, cwd, id, content string) string {
	t.Helper()
	path := filepath.Join(home, "sessions", url.PathEscape(cwd), id, "updates.jsonl")
	fixtureFile(t, path, content)
	return path
}

func grokTestUpdate(id, kind string) string {
	return fmt.Sprintf(`{"timestamp":1793429000,"method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":%q,"content":{"type":"text","text":"synthetic"}}}}`+"\n", id, kind)
}

func TestGrokCommands(t *testing.T) {
	d := &discoverer{}
	for _, tt := range []struct {
		cmd  string
		want bool
	}{
		{"grok", true},
		{"/Users/fixture/.grok/bin/grok --model grok-code", true},
		{`"C:\Program Files\Grok\grok.exe" --resume ` + grokTestIDA, true},
		{"grok agent stdio", true},
		{"grok --model claude-sonnet", true},
		{"grok login", false},
		{"grok update --check", false},
		{"grok sessions list", false},
		{"grok usage", false},
		{"grok wrap codex", false},
		{"grok --help", false},
		{"grok-export", false},
		{"node grok", false},
		{"python grok.py", false},
		{"codex --model grok", false},
		{"grep grok updates.jsonl", false},
	} {
		if got := d.isGrokCommand(tt.cmd); got != tt.want {
			t.Errorf("command classification = %v, want %v", got, tt.want)
		}
	}
	for _, tt := range []struct{ cmd, id string }{
		{"grok --resume " + grokTestIDA, grokTestIDA},
		{"grok --session-id=" + grokTestIDA, grokTestIDA},
		{"grok --load=" + grokTestIDA, grokTestIDA},
		{"grok -r " + grokTestIDA, grokTestIDA},
		{"grok --resume latest", ""},
		{"grok --resume ../escape", ""},
		{"grok --resume", ""},
		{"codex --resume " + grokTestIDA, ""},
	} {
		if got := grokCommandID(tt.cmd); got != tt.id {
			t.Errorf("command id = %q, want %q", got, tt.id)
		}
	}
}

func TestGrokPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", filepath.Join(root, "override"))
	if home, err := GrokHome(); err != nil || home != filepath.Join(root, "override") {
		t.Fatal("GROK_HOME override not honored")
	}
	t.Setenv("GROK_HOME", "")
	if home, err := (&discoverer{opts: Options{Home: root}}).grokHome(); err != nil || home != filepath.Join(root, ".grok") {
		t.Fatal("default home not honored")
	}
	for _, cwd := range []string{"/workspace/a+b space", `C:\Work\project`, `\\server\share\repo`} {
		dir := filepath.Join(root, url.PathEscape(cwd))
		if got := GrokWorkspace(dir); got != cwd {
			t.Errorf("decoded cwd mismatch")
		}
	}
	hashed := filepath.Join(root, "project-0123456789abcdef")
	fixtureFile(t, filepath.Join(hashed, ".cwd"), "/workspace/long/path\n")
	if GrokWorkspace(hashed) != "/workspace/long/path" {
		t.Fatal("hashed .cwd not honored")
	}
	fixtureFile(t, filepath.Join(root, "invalid", ".cwd"), "../relative")
	if GrokWorkspace(filepath.Join(root, "invalid")) != "" {
		t.Fatal("relative cwd accepted")
	}
	path := grokTestLog(t, root, "/workspace/test", grokTestIDA, grokTestUpdate(grokTestIDA, "user_message_chunk"))
	fixtureFile(t, filepath.Join(filepath.Dir(path), "chat_history.jsonl"), "rewritten\n")
	if GrokTranscriptID(path) != grokTestIDA || GrokTranscriptID(filepath.Join(filepath.Dir(path), "chat_history.jsonl")) != "" {
		t.Fatal("primary log validation failed")
	}
	if FindGrokTranscript(root, grokTestIDA) != path || FindGrokTranscript(root, "../escape") != "" {
		t.Fatal("transcript lookup failed")
	}
}

func TestGrokMetadata(t *testing.T) {
	for _, tt := range []struct {
		kind  string
		state State
	}{
		{"user_message_chunk", SessionStateActive},
		{"tool_call", SessionStateActive},
		{"tool_call_update", SessionStateActive},
		{"agent_thought_chunk", SessionStateActive},
		{"agent_message_chunk", SessionStateWaiting},
		{"future_update", SessionStateUnknown},
	} {
		t.Run(tt.kind, func(t *testing.T) {
			root := t.TempDir()
			path := grokTestLog(t, root, "/workspace/test", grokTestIDA, grokTestUpdate(grokTestIDA, tt.kind))
			got, err := ReadGrokTranscript(path)
			if err != nil || got.State != tt.state {
				t.Fatalf("state = %v, error = %v", got.State, err)
			}
		})
	}
	root := t.TempDir()
	path := grokTestLog(t, root, "/workspace/test", grokTestIDA, grokTestUpdate(grokTestIDA, "agent_message_chunk"))
	fixtureFile(t, filepath.Join(filepath.Dir(path), "summary.json"), fmt.Sprintf(`{"info":{"id":%q,"cwd":"/workspace/test"},"session_summary":"fallback","generated_title":"title","current_model_id":"grok-code","head_branch":"main","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-02T00:00:00Z"}`, grokTestIDA))
	fixtureFile(t, filepath.Join(filepath.Dir(path), "usage.json"), fmt.Sprintf(`{"sessionId":%q,"session":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":30,"cacheCreationTokens":10}}`, grokTestIDA))
	fixtureFile(t, filepath.Join(filepath.Dir(path), "signals.json"), `{"turnCount":4,"assistantMessageCount":8,"toolCallCount":9}`)
	got, err := ReadGrokTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "title" || got.Model != "grok-code" || got.Branch != "main" || got.StartedAt.Format(time.DateOnly) != "2026-10-01" {
		t.Fatal("summary metadata not projected")
	}
	if got.Tokens != (TokenBreakdown{Input: 60, Output: 20, CacheRead: 30, CacheWrite: 10}) {
		t.Fatal("billed token projection mismatch")
	}
	fixtureFile(t, filepath.Join(filepath.Dir(path), "summary.json"), `{"info":{"id":"mismatch"},"generated_title":"wrong"}`)
	fixtureFile(t, filepath.Join(filepath.Dir(path), "usage.json"), `{"sessionId":"mismatch","session":{"inputTokens":999}}`)
	got, err = ReadGrokTranscript(path)
	if err != nil || got.Title != "" || got.Tokens.Total() != 0 {
		t.Fatal("mismatched sidecars contaminated metadata")
	}
	fixtureFile(t, path, "{invalid}\n")
	_, err = ReadGrokTranscript(path)
	if err == nil || strings.Contains(err.Error(), root) {
		t.Fatal("corrupt log must have sanitized error")
	}
}

func TestGrokBoundedTailAndPartial(t *testing.T) {
	root := t.TempDir()
	first := grokTestUpdate(grokTestIDA, "user_message_chunk")
	unknown := fmt.Sprintf(`{"sessionId":%q,"update":{"sessionUpdate":"future_update","padding":%q}}`+"\n", grokTestIDA, strings.Repeat("x", 4000))
	path := grokTestLog(t, root, "/workspace/test", grokTestIDA, first+strings.Repeat(unknown, 1400)+grokTestUpdate(grokTestIDA, "agent_message_chunk")+`{"timestamp":`)
	got, err := ReadGrokTranscript(path)
	if err != nil || got.State != SessionStateWaiting || got.StartedAt.IsZero() {
		t.Fatal("bounded head/tail or partial record handling failed")
	}
}

func TestGrokEnrichCorrelation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := "/workspace/project"
	newest := grokTestLog(t, root, cwd, grokTestIDA, grokTestUpdate(grokTestIDA, "user_message_chunk"))
	held := grokTestLog(t, root, cwd, grokTestIDB, grokTestUpdate(grokTestIDB, "agent_message_chunk"))
	older := time.Now().Add(-time.Hour)
	if err := os.Chtimes(held, older, older); err != nil {
		t.Fatal(err)
	}
	d := &discoverer{ctx: context.Background(), opts: Options{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "lsof" {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "-d cwd") {
				return []byte("p100\nn" + cwd + "\np200\nn" + cwd + "\np300\nn/workspace/other\n"), nil
			}
			if args[len(args)-1] == "100" {
				return []byte("p100\nn" + held + "\n"), nil
			}
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected command")
	}}}
	groups := []ProcessGroup{
		{Tool: "Grok", Root: ProcessInfo{PID: 100, Command: "grok"}},
		{Tool: "Grok", Root: ProcessInfo{PID: 200, Command: "grok"}},
		{Tool: "Grok", Root: ProcessInfo{PID: 300, Command: "grok"}},
	}
	if err := d.enrichGrokSessions(groups); err == nil {
		t.Fatal("unmatched runtime should warn")
	}
	// On Windows open-file mapping is unavailable; higher PID gets newer log.
	if openFilesListable && (len(groups[0].Sessions) != 1 || groups[0].Sessions[0].RolloutPath != held) {
		t.Fatal("held file did not beat cwd heuristic")
	}
	if len(groups[1].Sessions) != 1 || groups[1].Sessions[0].RolloutPath != newest || len(groups[2].Sessions) != 0 {
		t.Fatal("cwd heuristic claimed wrong workspace or session")
	}
	groups = []ProcessGroup{{Tool: "Grok", Root: ProcessInfo{PID: 100, Command: "grok --resume " + grokTestIDA}}}
	if err := d.enrichGrokSessions(groups); err != nil || len(groups[0].Sessions) != 1 || groups[0].Sessions[0].ID != grokTestIDA {
		t.Fatal("explicit id did not beat held file")
	}
}

func TestDecodeGrokUpdate(t *testing.T) {
	for _, line := range []string{
		grokTestUpdate(grokTestIDA, "user_message_chunk"),
		fmt.Sprintf(`{"sessionId":%q,"update":{"sessionUpdate":"plan"}}`, grokTestIDA),
	} {
		u, err := DecodeGrokUpdate([]byte(line))
		if err != nil || u.SessionID != grokTestIDA || !json.Valid(u.Update) {
			t.Fatal("valid update not decoded")
		}
	}
	for _, line := range []string{
		"{invalid}", `null`, `{"method":"unknown","params":{}}`, `{"sessionId":"../escape","update":{}}`,
		fmt.Sprintf(`{"sessionId":%q,"update":null}`, grokTestIDA),
		fmt.Sprintf(`{"sessionId":%q,"update":{}}`, grokTestIDA),
	} {
		if _, err := DecodeGrokUpdate([]byte(line)); err == nil {
			t.Fatal("malformed notification accepted")
		}
	}
}
