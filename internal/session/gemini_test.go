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
	geminiIDA = "0199c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d"
	geminiIDB = "1a2b3c4d-0000-4000-8000-000000000001"
	geminiIDC = "2b3c4d5e-0000-4000-8000-000000000002"
)

func TestClassifyGeminiProcess(t *testing.T) {
	d := &discoverer{}
	for _, tt := range []struct {
		command, want string
	}{
		{"gemini", "Gemini"},
		{"/opt/homebrew/bin/gemini --resume latest", "Gemini"},
		{"node /usr/local/bin/gemini -m gemini-2.5-pro", "Gemini"},
		// Gemini CLI relaunches itself with node flags.
		{"/usr/local/bin/node --max-old-space-size=8192 --no-warnings=DEP0040 /usr/local/lib/node_modules/@google/gemini-cli/bundle/gemini.js", "Gemini"},
		{"node /home/u/.npm/_npx/1/node_modules/@google/gemini-cli/dist/index.js", "Gemini"},
		{"gemini.exe --session-id abc", "Gemini"},
		// Configuration subcommands are not sessions.
		{"gemini mcp list", ""},
		{"gemini extensions install x", ""},
		// A Gemini model in another harness's flags stays with that harness.
		{"opencode -m google/gemini-2.5-pro", "OpenCode"},
		{"codex --model gemini-3-pro", "Codex"},
		{"vim gemini.md", ""},
		{"node server.js gemini", ""},
		{"/Applications/Gemini.app/Contents/MacOS/gemini", ""},
		{"firekeeper daemon --provider gemini", ""},
	} {
		if got := d.classifyProcess(tt.command); got != tt.want {
			t.Errorf("classifyProcess(%q) = %q, want %q", tt.command, got, tt.want)
		}
	}
}

func TestGeminiSessionIDFromCommand(t *testing.T) {
	d := &discoverer{}
	for _, tt := range []struct {
		command, want string
	}{
		{"gemini --session-id my_session-1", "my_session-1"},
		{"gemini --session-id=" + geminiIDA, geminiIDA},
		{"gemini --resume " + geminiIDA, geminiIDA},
		{"gemini -r " + geminiIDA, geminiIDA},
		{"gemini --resume=" + geminiIDA, geminiIDA},
		// latest and list indexes name no session.
		{"gemini --resume latest", ""},
		{"gemini -r 5", ""},
		{"gemini --resume", ""},
		{"gemini --session-id ../escape", ""},
		{"gemini --session-id a*", ""},
		{"node /usr/local/bin/gemini --session-id abc", "abc"},
		{"codex --session-id abc", ""},
	} {
		got, ok := d.geminiSessionIDFromCommand(tt.command)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("geminiSessionIDFromCommand(%q) = %q, %v; want %q", tt.command, got, ok, tt.want)
		}
	}
}

func geminiLine(t *testing.T, record map[string]any) string {
	t.Helper()
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(line) + "\n"
}

// writeGeminiLog writes a session log for id rooted at cwd with summary as
// its title, followed by extra records.
func writeGeminiLog(t *testing.T, path, id, cwd, summary string, extra ...map[string]any) {
	t.Helper()
	lines := []string{
		geminiLine(t, map[string]any{"sessionId": id, "projectHash": GeminiProjectHash(cwd), "startTime": "2026-10-07T12:00:00.000Z", "lastUpdated": "2026-10-07T12:00:00.000Z", "kind": "main"}),
		geminiLine(t, map[string]any{"id": "u1", "timestamp": "2026-10-07T12:00:01.000Z", "type": "user", "content": []any{map[string]any{"text": "prompt"}}}),
		geminiLine(t, map[string]any{"id": "g1", "timestamp": "2026-10-07T12:00:02.000Z", "type": "gemini", "content": "reply", "model": "gemini-test"}),
		geminiLine(t, map[string]any{"$set": map[string]any{"summary": summary, "lastUpdated": "2026-10-07T12:00:03.000Z"}}),
	}
	for _, record := range extra {
		lines = append(lines, geminiLine(t, record))
	}
	fixtureFile(t, path, strings.Join(lines, ""))
}

func TestGeminiProjectDirs(t *testing.T) {
	runtime := t.TempDir()
	tmp := filepath.Join(runtime, "tmp")
	// Paths are the platform's own: Windows has no /work, and cleans it to
	// \work.
	demo, bad, other := filepath.FromSlash("/work/demo"), filepath.FromSlash("/work/bad"), filepath.FromSlash("/work/other")
	registry, err := json.Marshal(map[string]any{"projects": map[string]string{demo: "demo", bad: "../escape"}})
	if err != nil {
		t.Fatal(err)
	}
	// The registry names the directory.
	fixtureFile(t, filepath.Join(runtime, "projects.json"), string(registry))
	if got, want := GeminiProjectDirs(runtime, demo+string(filepath.Separator)), []string{filepath.Join(tmp, "demo"), filepath.Join(tmp, GeminiProjectHash(demo))}; !reflect.DeepEqual(got, want) {
		t.Errorf("registry: %q, want %q", got, want)
	}
	if got, want := GeminiProjectDirs(runtime, bad), []string{filepath.Join(tmp, GeminiProjectHash(bad))}; !reflect.DeepEqual(got, want) {
		t.Errorf("invalid registry entry: %q, want %q", got, want)
	}
	// Without a registry entry, the .project_root marker does.
	fixtureFile(t, filepath.Join(tmp, "other-1", ".project_root"), other+"\n")
	if got := GeminiProjectDirs(runtime, other); len(got) != 2 || got[0] != filepath.Join(tmp, "other-1") {
		t.Errorf("marker: %q", got)
	}
	if GeminiProjectDirs(runtime, "") != nil {
		t.Error("no cwd, no directories")
	}

	for _, tt := range []struct {
		dir, hash, want string
	}{
		{filepath.Join(tmp, "other-1"), "", other},
		{filepath.Join(tmp, "demo"), "", demo},
		{filepath.Join(tmp, GeminiProjectHash(demo)), "", demo},
		{filepath.Join(tmp, "renamed"), GeminiProjectHash(demo), demo},
		{filepath.Join(tmp, "unknown"), "feed", ""},
	} {
		if got := GeminiProjectRoot(runtime, tt.dir, tt.hash); got != tt.want {
			t.Errorf("GeminiProjectRoot(%s) = %q, want %q", filepath.Base(tt.dir), got, tt.want)
		}
	}
}

func TestReadGeminiTranscriptState(t *testing.T) {
	dir := t.TempDir()
	user := map[string]any{"id": "u2", "timestamp": "2026-10-07T12:00:04.000Z", "type": "user", "content": "more"}
	thinking := map[string]any{"id": "g2", "timestamp": "2026-10-07T12:00:05.000Z", "type": "gemini", "content": []any{map[string]any{"text": "hmm", "thought": true}}, "model": "gemini-next"}
	calls := map[string]any{"id": "g2", "timestamp": "2026-10-07T12:00:05.000Z", "type": "gemini", "content": "Checking.", "toolCalls": []any{map[string]any{"id": "c1", "name": "read_file", "status": "success"}}}
	response := map[string]any{"id": "u3", "timestamp": "2026-10-07T12:00:06.000Z", "type": "user", "content": []any{map[string]any{"functionResponse": map[string]any{"id": "c1", "name": "read_file", "response": map[string]any{"output": "x"}}}}}
	failed := map[string]any{"id": "e1", "timestamp": "2026-10-07T12:00:07.000Z", "type": "error", "content": "API error"}
	info := map[string]any{"id": "i1", "timestamp": "2026-10-07T12:00:08.000Z", "type": "info", "content": "note"}
	rewind := map[string]any{"$rewindTo": "u2"}
	for _, tt := range []struct {
		name  string
		extra []map[string]any
		want  State
	}{
		{"reply", nil, SessionStateWaiting},
		{"prompt", []map[string]any{user}, SessionStateActive},
		{"thinking only", []map[string]any{user, thinking}, SessionStateActive},
		{"tool calls", []map[string]any{user, calls}, SessionStateActive},
		{"tool response", []map[string]any{user, calls, response}, SessionStateActive},
		{"error ends the turn", []map[string]any{user, failed}, SessionStateWaiting},
		{"info keeps the state", []map[string]any{user, info}, SessionStateActive},
		{"rewind", []map[string]any{user, rewind}, SessionStateWaiting},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-")+".jsonl")
			writeGeminiLog(t, path, geminiIDA, "/work/demo", "Title", tt.extra...)
			got, err := ReadGeminiTranscript(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tt.want {
				t.Fatalf("state %s, want %s", got.State, tt.want)
			}
			if got.SessionID != geminiIDA || got.Summary != "Title" || got.Kind != "main" || got.ProjectHash != GeminiProjectHash("/work/demo") {
				t.Fatalf("metadata %+v", got)
			}
			if !got.StartedAt.Equal(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)) || got.UpdatedAt.Before(time.Date(2026, 10, 7, 12, 0, 3, 0, time.UTC)) {
				t.Fatalf("times %v %v", got.StartedAt, got.UpdatedAt)
			}
		})
	}
}

func TestReadGeminiTranscriptTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-x.jsonl")
	filler := map[string]any{"id": "big", "type": "user", "content": strings.Repeat("x", int(GeminiTranscriptTailBytes))}
	last := map[string]any{"id": "g9", "timestamp": "2026-10-07T13:00:00.000Z", "type": "gemini", "content": "", "model": "gemini-tail"}
	writeGeminiLog(t, path, geminiIDA, "/work/demo", "Title", filler, last)
	got, err := ReadGeminiTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	// The filler line is cut by the tail window and skipped.
	if got.SessionID != geminiIDA || got.Model != "gemini-tail" || got.State != SessionStateActive || !got.UpdatedAt.Equal(time.Date(2026, 10, 7, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("tail: %+v", got)
	}
	// Summary and model before the window are not read.
	if got.Summary != "" {
		t.Fatalf("summary %q came from outside the tail", got.Summary)
	}
}

func TestReadGeminiTranscriptErrors(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"empty.jsonl":     "",
		"message.jsonl":   `{"id":"u1","type":"user","content":"x"}` + "\n",
		"bad-id.jsonl":    `{"sessionId":"../x","projectHash":"h"}` + "\n",
		"truncated.json":  `{"sessionId":"abc","messages":[`,
		"no-session.json": `{"messages":[]}`,
	} {
		path := filepath.Join(dir, name)
		fixtureFile(t, path, body)
		if _, err := ReadGeminiTranscript(path); err == nil {
			t.Errorf("%s: want an error", name)
		} else if strings.Contains(err.Error(), dir) {
			t.Errorf("%s: error names the path: %v", name, err)
		}
	}
	legacy := filepath.Join(dir, "legacy.json")
	fixtureFile(t, legacy, `{"sessionId":"`+geminiIDB+`","projectHash":"h","startTime":"2026-03-01T10:00:00Z","lastUpdated":"2026-03-01T10:02:00Z",
  "messages":[{"id":"a","type":"user","content":"x"},{"id":"b","type":"gemini","content":"done","model":"gemini-2.5-pro"}],"summary":"Old"}`)
	got, err := ReadGeminiTranscript(legacy)
	if err != nil || got.SessionID != geminiIDB || got.Summary != "Old" || got.Model != "gemini-2.5-pro" || got.State != SessionStateWaiting {
		t.Fatalf("legacy: %+v, %v", got, err)
	}
}

func TestFindGeminiTranscript(t *testing.T) {
	runtime := t.TempDir()
	chats := filepath.Join(runtime, "tmp", "demo", "chats")
	writeGeminiLog(t, filepath.Join(chats, "session-2026-10-07T12-00-0199c3d4.jsonl"), geminiIDA, "/work/demo", "A")
	// Same eight-character prefix, different session.
	writeGeminiLog(t, filepath.Join(chats, "session-2026-10-07T12-01-0199c3d4.jsonl"), "0199c3d4-0000-4000-8000-000000000000", "/work/demo", "Other")
	if got := FindGeminiTranscript([]string{filepath.Join(runtime, "missing"), runtime}, geminiIDA); filepath.Base(got) != "session-2026-10-07T12-00-0199c3d4.jsonl" {
		t.Fatalf("got %q", got)
	}
	if got := FindGeminiTranscript([]string{runtime}, "0199c3d4*"); got != "" {
		t.Fatalf("glob id matched %q", got)
	}
	short := filepath.Join(chats, "session-2026-10-07T12-02-abc.jsonl")
	writeGeminiLog(t, short, "abc", "/work/demo", "Short")
	if got := FindGeminiTranscript([]string{runtime}, "abc"); got != short {
		t.Fatalf("short id: got %q", got)
	}
}

func TestDiscoverGeminiSessions(t *testing.T) {
	home := t.TempDir()
	runtime := filepath.Join(home, "gemini-runtime")
	cwd := filepath.Join(home, "work", "demo")
	otherCwd := filepath.Join(home, "work", "other")
	fixtureFile(t, filepath.Join(runtime, "projects.json"), fmt.Sprintf(`{"projects":{%q:"demo"}}`, cwd))
	chats := filepath.Join(runtime, "tmp", "demo", "chats")
	explicit := filepath.Join(runtime, "tmp", "other", "chats", "session-2026-10-07T11-00-0199c3d4.jsonl")
	older := filepath.Join(chats, "session-2026-10-07T12-00-1a2b3c4d.jsonl")
	newer := filepath.Join(chats, "session-2026-10-07T12-05-2b3c4d5e.jsonl")
	writeGeminiLog(t, explicit, geminiIDA, otherCwd, "Explicit")
	writeGeminiLog(t, older, geminiIDB, cwd, "Older")
	writeGeminiLog(t, newer, geminiIDC, cwd, "Newer", map[string]any{"id": "u2", "type": "user", "content": "again"})
	// Subagent logs nest under their parent and are never candidates.
	writeGeminiLog(t, filepath.Join(chats, geminiIDC, "sub.jsonl"), "sub", cwd, "Sub")
	base := time.Now().Add(-time.Hour)
	for i, path := range []string{older, newer, explicit} {
		stamp := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	opts := Options{Home: home, GeminiDir: runtime}
	opts.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ps":
			return []byte(strings.Join([]string{
				"50 1 pts/0 00:01 node /usr/local/bin/gemini --resume " + geminiIDA,
				"51 50 pts/0 00:01 /usr/local/bin/node --max-old-space-size=8192 /usr/local/bin/gemini --resume " + geminiIDA,
				"60 1 pts/1 00:02 gemini",
				"70 1 pts/2 00:03 gemini -y",
				"80 1 ? 00:04 gemini mcp list",
				"90 1 pts/3 00:05 gemini",
			}, "\n")), nil
		case "lsof":
			switch {
			case reflect.DeepEqual(args, []string{"-a", "-d", "cwd", "-Fn", "-p", "60,70,90"}):
				return []byte("p60\nn" + cwd + "\np70\nn" + cwd + "\np90\nn" + filepath.Join(home, "no-sessions") + "\n"), nil
			case reflect.DeepEqual(args, []string{"-a", "-d", "cwd", "-Fn", "-p", "50"}):
				return []byte("p50\nn" + otherCwd + "\n"), nil
			case reflect.DeepEqual(args, []string{"-a", "-d", "cwd", "-Fn", "-p", "90"}):
				return []byte("p90\nn" + filepath.Join(home, "no-sessions") + "\n"), nil
			}
			return nil, fmt.Errorf("unexpected lsof %v", args)
		case "git":
			return nil, fmt.Errorf("not a repository")
		}
		t.Fatalf("unexpected command %s", name)
		return nil, nil
	}
	metas, err := Discover(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "Gemini: metadata unavailable for 1 runtime(s)") {
		t.Fatalf("want a warning for the unmapped runtime, got %v", err)
	}
	byPID := map[int]Meta{}
	for _, m := range metas {
		byPID[m.PID] = m
	}
	if len(metas) != 4 || byPID[80].PID != 0 {
		t.Fatalf("got %d runtimes; helpers must not count as sessions", len(metas))
	}
	if len(byPID[50].Runtime.Processes) != 2 {
		t.Error("relaunched Gemini CLI child not grouped under its root")
	}
	for _, tt := range []struct {
		pid   int
		id    string
		title string
		state State
		cwd   string
	}{
		{50, geminiIDA, "Explicit", SessionStateWaiting, otherCwd},
		// Higher PIDs are newer processes and take newer logs.
		{70, geminiIDC, "Newer", SessionStateActive, cwd},
		{60, geminiIDB, "Older", SessionStateWaiting, cwd},
		{90, "", "", SessionStateUnknown, filepath.Join(home, "no-sessions")},
	} {
		m := byPID[tt.pid]
		if m.Provider != "gemini" || m.ID != tt.id || m.Title != tt.title || m.State != tt.state || m.CWD != tt.cwd {
			t.Errorf("pid %d: provider %q id %q title %q state %s cwd %q; want id %q title %q state %s cwd %q",
				tt.pid, m.Provider, m.ID, m.Title, m.State, m.CWD, tt.id, tt.title, tt.state, tt.cwd)
		}
		if tt.id == "" {
			continue
		}
		if m.Model != "gemini-test" || m.Source != GeminiSource || m.RolloutPath == "" || m.StartedAt == nil {
			t.Errorf("pid %d: metadata %+v", tt.pid, m)
		}
	}
}

func TestGeminiDirsHonorEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GEMINI_CLI_HOME", home)
	dirs, err := GeminiDirs()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(home, ".gemini"), filepath.Join(home, ".cache", ".gemini")}; !reflect.DeepEqual(dirs, want) {
		t.Fatalf("dirs %q, want %q", dirs, want)
	}
	t.Setenv("GEMINI_CLI_HOME", "")
	d := &discoverer{opts: Options{Home: home}}
	if dirs, _ := d.geminiDirs(); dirs[0] != filepath.Join(home, ".gemini") {
		t.Fatalf("default dirs %q", dirs)
	}
	d.opts.GeminiDir = "/override"
	if dirs, _ := d.geminiDirs(); !reflect.DeepEqual(dirs, []string{"/override"}) {
		t.Fatalf("override dirs %q", dirs)
	}
}
