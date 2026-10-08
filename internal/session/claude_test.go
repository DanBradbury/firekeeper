package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	claudeIDA = "0199c3d4-e5f6-7a8b-9c0d-000000000001"
	claudeIDB = "0199c3d4-e5f6-7a8b-9c0d-000000000002"
	claudeIDC = "0199c3d4-e5f6-7a8b-9c0d-000000000003"
)

func TestClassifyClaudeCode(t *testing.T) {
	d := &discoverer{}
	tests := []struct {
		command string
		want    string
	}{
		{"claude", "Claude"},
		{"/usr/local/bin/claude --resume " + claudeIDA, "Claude"},
		{"/opt/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe --session-id " + claudeIDA, "Claude"},
		{"node /usr/lib/node_modules/@anthropic-ai/claude-code/cli.js -p hello", "Claude"},
		{"claude --resume /home/u/.claude/projects/-home-u-codex/" + claudeIDA + ".jsonl", "Claude"},
		{"claude bg-pty-host --bg-pty-host /tmp/x.sock", ""},
		{"claude.exe daemon run --origin transient", ""},
		{"claude mcp serve", ""},
		{"/usr/lib/claude-desktop/claude-desktop --type=renderer", ""},
		{"vim /home/u/.claude/settings.json", ""},
		{"node /srv/claude-tools/index.js", ""},
		{"codex", "Codex"},
		{"node /home/u/.claude/hooks/codex-notify.js", "Codex"},
	}
	for _, tt := range tests {
		if got := d.classifyProcess(tt.command); got != tt.want {
			t.Errorf("classifyProcess(%q) = %q, want %q", tt.command, got, tt.want)
		}
	}
}

func TestClaudeSessionIDFromCommand(t *testing.T) {
	d := &discoverer{}
	tests := []struct {
		command string
		want    string
	}{
		{"claude --session-id " + claudeIDA, claudeIDA},
		{"claude --session-id=" + claudeIDA, claudeIDA},
		{"claude --resume " + claudeIDA, claudeIDA},
		{"claude -r " + claudeIDA, claudeIDA},
		{"claude --resume=" + claudeIDA, claudeIDA},
		{"claude --resume /home/u/.claude/projects/-work/" + claudeIDA + ".jsonl", claudeIDA},
		// A fork writes to a new id, so the resumed one is not this session.
		{"claude --fork-session --resume " + claudeIDA, ""},
		{"claude --session-id " + claudeIDB + " --fork-session --resume " + claudeIDA, claudeIDB},
		{"claude --resume", ""},
		{"claude --continue", ""},
		{"claude --resume not-a-uuid", ""},
		{"codex --session-id " + claudeIDA, ""},
	}
	for _, tt := range tests {
		got, ok := d.claudeSessionIDFromCommand(tt.command)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("claudeSessionIDFromCommand(%q) = %q, %v; want %q", tt.command, got, ok, tt.want)
		}
	}
}

func TestClaudeProjectDirName(t *testing.T) {
	d := &discoverer{}
	for cwd, want := range map[string]string{
		"/home/dan/Documents/Github/firekeeper": "-home-dan-Documents-Github-firekeeper",
		"/Users/a.b/my_repo":                    "-Users-a-b-my-repo",
	} {
		if got := d.claudeProjectDirName(cwd); got != want {
			t.Errorf("claudeProjectDirName(%q) = %q, want %q", cwd, got, want)
		}
	}
}

// claudeLine builds one transcript record from a map.
func claudeLine(t *testing.T, record map[string]any) string {
	t.Helper()
	b, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func claudeAssistant(t *testing.T, stop string, content ...map[string]any) string {
	return claudeLine(t, map[string]any{"type": "assistant", "timestamp": "2026-10-07T12:00:02Z", "message": map[string]any{
		"model": "claude-test", "role": "assistant", "stop_reason": stop, "content": content}})
}

func claudeToolUse(id, name string) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}
}

func claudeToolResult(t *testing.T, id string) string {
	return claudeLine(t, map[string]any{"type": "user", "message": map[string]any{"role": "user",
		"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "ok"}}}})
}

func claudePrompt(t *testing.T, text string) string {
	return claudeLine(t, map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
}

func TestScanClaudeTranscriptState(t *testing.T) {
	d := &discoverer{}
	text := map[string]any{"type": "text", "text": "done"}
	tests := []struct {
		name  string
		lines []string
		want  SessionState
	}{
		{"empty", nil, SessionStateUnknown},
		{"prompt sent", []string{claudePrompt(t, "go")}, SessionStateActive},
		{"tool running", []string{claudePrompt(t, "go"), claudeAssistant(t, "tool_use", claudeToolUse("a", "Bash"))}, SessionStateActive},
		{"tool finished", []string{claudeAssistant(t, "tool_use", claudeToolUse("a", "Bash")), claudeToolResult(t, "a")}, SessionStateActive},
		{"turn ended", []string{claudePrompt(t, "go"), claudeAssistant(t, "end_turn", text)}, SessionStateWaiting},
		{"turn duration", []string{claudePrompt(t, "go"), claudeLine(t, map[string]any{"type": "system", "subtype": "turn_duration"})}, SessionStateWaiting},
		{"question pending", []string{claudeAssistant(t, "tool_use", claudeToolUse("q", "AskUserQuestion"))}, SessionStateNeedsInput},
		{"question with parallel tool", []string{claudeAssistant(t, "tool_use", claudeToolUse("q", "AskUserQuestion")), claudeAssistant(t, "tool_use", claudeToolUse("b", "Read")), claudeToolResult(t, "b")}, SessionStateNeedsInput},
		{"question answered", []string{claudeAssistant(t, "tool_use", claudeToolUse("q", "ExitPlanMode")), claudeToolResult(t, "q")}, SessionStateActive},
		{"interrupted", []string{claudeAssistant(t, "tool_use", claudeToolUse("a", "Bash")), claudeLine(t, map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "[Request interrupted by user for tool use]"}}}})}, SessionStateWaiting},
		{"injected context ignored", []string{claudeAssistant(t, "end_turn", text), claudeLine(t, map[string]any{"type": "user", "isMeta": true, "message": map[string]any{"role": "user", "content": "caveat"}})}, SessionStateWaiting},
		{"sidechain ignored", []string{claudeAssistant(t, "end_turn", text), claudeLine(t, map[string]any{"type": "user", "isSidechain": true, "message": map[string]any{"role": "user", "content": "sub"}})}, SessionStateWaiting},
		{"malformed lines ignored", []string{claudeAssistant(t, "end_turn", text), "not json\n", `{"type":"assistant","message":{"content":42}}` + "\n"}, SessionStateWaiting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, err := d.scanClaudeTranscript(strings.NewReader(strings.Join(tt.lines, "")), false)
			if err != nil {
				t.Fatal(err)
			}
			if meta.State != tt.want {
				t.Fatalf("state = %s, want %s", meta.State, tt.want)
			}
		})
	}
}

func writeClaudeTranscript(t *testing.T, path, cwd, title string, extra ...string) {
	t.Helper()
	lines := []string{
		claudeLine(t, map[string]any{"type": "user", "timestamp": "2026-10-07T12:00:00Z", "cwd": cwd, "gitBranch": "main", "entrypoint": "cli",
			"sessionId": strings.TrimSuffix(filepath.Base(path), ".jsonl"), "message": map[string]any{"role": "user", "content": "go"}}),
		claudeAssistant(t, "end_turn", map[string]any{"type": "text", "text": "done"}),
		claudeLine(t, map[string]any{"type": "ai-title", "aiTitle": title}),
		claudeLine(t, map[string]any{"type": "cost-state", "modelUsage": map[string]any{"claude-test": map[string]any{
			"inputTokens": 1, "outputTokens": 2, "cacheReadInputTokens": 3, "cacheCreationInputTokens": 4}}}),
	}
	fixtureFile(t, path, strings.Join(append(lines, extra...), ""))
}

func TestDiscoverClaudeSessions(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, "claude-config")
	cwd := filepath.Join(home, "work", "demo")
	otherCwd := filepath.Join(home, "work", "other")
	project := filepath.Join(claudeDir, "projects", (&discoverer{}).claudeProjectDirName(cwd))
	explicit := filepath.Join(claudeDir, "projects", "-elsewhere", claudeIDA+".jsonl")
	older := filepath.Join(project, claudeIDB+".jsonl")
	newer := filepath.Join(project, claudeIDC+".jsonl")
	writeClaudeTranscript(t, explicit, otherCwd, "Explicit")
	writeClaudeTranscript(t, older, cwd, "Older")
	writeClaudeTranscript(t, newer, cwd, "Newer", claudePrompt(t, "again"))
	// The explicit session lives in this directory too, but is claimed.
	fixtureFile(t, filepath.Join(project, claudeIDA+".jsonl"), "")
	fixtureFile(t, filepath.Join(project, "notes.jsonl"), "")
	base := time.Now().Add(-time.Hour)
	for i, path := range []string{older, newer, filepath.Join(project, claudeIDA+".jsonl")} {
		stamp := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	opts := Options{Home: home, ClaudeHome: claudeDir}
	opts.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ps":
			return []byte(strings.Join([]string{
				"50 1 pts/0 00:01 claude --session-id " + claudeIDA,
				"60 1 pts/1 00:02 claude",
				"70 1 pts/2 00:03 /usr/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe --continue",
				"71 70 pts/2 00:03 /usr/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe --continue",
				"80 1 ? 00:04 claude bg-spare --bg-spare /tmp/x.sock",
				"90 1 pts/3 00:05 claude",
			}, "\n")), nil
		case "lsof":
			if reflect.DeepEqual(args, []string{"-a", "-d", "cwd", "-Fn", "-p", "60,70,90"}) {
				return []byte("p60\nn" + cwd + "\np70\nn" + cwd + "\np90\nn" + filepath.Join(home, "no-sessions") + "\n"), nil
			}
			return nil, fmt.Errorf("unexpected lsof %v", args)
		case "git":
			return nil, fmt.Errorf("not a repository")
		}
		t.Fatalf("unexpected command %s", name)
		return nil, nil
	}
	metas, err := Discover(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "Claude: metadata unavailable for 1 runtime(s)") {
		t.Fatalf("want a warning for the unmapped runtime, got %v", err)
	}
	byPID := map[int]Meta{}
	for _, m := range metas {
		byPID[m.PID] = m
	}
	if len(metas) != 4 || byPID[80].PID != 0 {
		t.Fatalf("got %d runtimes; helpers must not count as sessions", len(metas))
	}
	if len(byPID[70].Runtime.Processes) != 2 {
		t.Error("child Claude Code process not grouped under its root")
	}
	tests := []struct {
		pid   int
		id    string
		title string
		state SessionState
		cwd   string
	}{
		{50, claudeIDA, "Explicit", SessionStateWaiting, otherCwd},
		// Higher PIDs are newer processes and take newer transcripts.
		{70, claudeIDC, "Newer", SessionStateActive, cwd},
		{60, claudeIDB, "Older", SessionStateWaiting, cwd},
		{90, "", "", SessionStateUnknown, ""},
	}
	for _, tt := range tests {
		m := byPID[tt.pid]
		if m.Provider != "claude" || m.ID != tt.id || m.Title != tt.title || m.State != tt.state {
			t.Errorf("pid %d: provider %q id %q title %q state %s; want id %q title %q state %s",
				tt.pid, m.Provider, m.ID, m.Title, m.State, tt.id, tt.title, tt.state)
		}
		if tt.id == "" {
			continue
		}
		if m.CWD != tt.cwd || m.Model != "claude-test" || m.Branch != "main" || m.Source != "cli" || m.TokensUsed != 10 {
			t.Errorf("pid %d: metadata %+v", tt.pid, m)
		}
		if m.StartedAt == nil || !m.StartedAt.Equal(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)) || m.RolloutPath == "" {
			t.Errorf("pid %d: started %v path set %v", tt.pid, m.StartedAt, m.RolloutPath != "")
		}
	}
}

func TestClaudeConfigDirHonorsEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	got, err := (&discoverer{opts: Options{Home: t.TempDir()}}).claudeConfigDir()
	if err != nil || got != dir {
		t.Fatalf("claudeConfigDir = %q, %v", got, err)
	}
	got, _ = (&discoverer{opts: Options{ClaudeHome: "/override"}}).claudeConfigDir()
	if got != "/override" {
		t.Fatalf("Options.ClaudeHome ignored: %q", got)
	}
}

// Large transcripts are read from a bounded tail, with the start time taken
// from the head.
func TestReadClaudeTranscriptMetadataLargeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), claudeIDA+".jsonl")
	head := claudeLine(t, map[string]any{"type": "user", "timestamp": "2026-10-01T00:00:00Z", "message": map[string]any{"role": "user", "content": "start"}})
	filler := claudeLine(t, map[string]any{"type": "attachment", "attachment": map[string]any{"pad": strings.Repeat("x", 1<<20)}})
	tail := claudeAssistant(t, "tool_use", claudeToolUse("q", "AskUserQuestion"))
	fixtureFile(t, path, head+strings.Repeat(filler, int(ClaudeTranscriptTailBytes>>20)+1)+tail)
	info, err := (&discoverer{}).readClaudeTranscriptMetadata(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != claudeIDA || info.State != SessionStateNeedsInput || info.Name != "Claude Code" ||
		!info.StartedAt.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("metadata %+v", info)
	}
}
