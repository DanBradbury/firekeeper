package session

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/testenv"
)

const (
	cursorIDA = "0199c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d"
	cursorIDB = "1a2b3c4d-0000-4000-8000-000000000001"
	cursorIDC = "2b3c4d5e-0000-4000-8000-000000000002"
	cursorIDD = "3c4d5e6f-0000-4000-8000-000000000003"
)

const cursorNode = "/home/u/.local/share/cursor-agent/versions/2026.09.02-abc123/node --use-system-ca /home/u/.local/share/cursor-agent/versions/2026.09.02-abc123/index.js"

func TestClassifyCursorProcess(t *testing.T) {
	d := &discoverer{}
	for _, tt := range []struct {
		command, want string
	}{
		{"cursor-agent", "Cursor"},
		{"/home/u/.local/bin/cursor-agent --resume " + cursorIDA, "Cursor"},
		{"bash /home/u/.local/bin/cursor-agent -p fix", "Cursor"},
		// The launcher runs a bundled node on the versioned index.js.
		{cursorNode, "Cursor"},
		{cursorNode + " --resume=" + cursorIDA, "Cursor"},
		{cursorNode + " resume", "Cursor"},
		// A model that names another harness stays with Cursor CLI.
		{"cursor-agent --model gpt-5-codex", "Cursor"},
		{cursorNode + " --model gpt-5-codex", "Cursor"},
		// Account and installation helpers are not sessions.
		{"cursor-agent login", ""},
		{cursorNode + " update", ""},
		{cursorNode + " mcp list", ""},
		// The bare agent name is too generic, and the editor is not the CLI.
		{"agent", ""},
		{"/Applications/Cursor.app/Contents/MacOS/Cursor", ""},
		{"/Applications/Cursor.app/Contents/Resources/app/bin/cursor-agent", ""},
		{"vim ~/.cursor/cli-config.json", ""},
		{"node server.js cursor-agent", ""},
		{"firekeeper daemon --provider cursor", ""},
	} {
		if got := d.classifyProcess(tt.command); got != tt.want {
			t.Errorf("classifyProcess(%q) = %q, want %q", tt.command, got, tt.want)
		}
	}
}

func TestCursorSessionIDFromCommand(t *testing.T) {
	d := &discoverer{}
	for _, tt := range []struct {
		command, want string
	}{
		{"cursor-agent --resume " + cursorIDA, cursorIDA},
		{"cursor-agent --resume=" + cursorIDA, cursorIDA},
		{"cursor-agent --resume='" + cursorIDA + "'", cursorIDA},
		{cursorNode + " --resume " + cursorIDA, cursorIDA},
		// Bare --resume and the resume subcommand pick the latest chat.
		{"cursor-agent --resume", ""},
		{"cursor-agent resume", ""},
		{"cursor-agent --resume ../escape", ""},
		{"cursor-agent --resume *", ""},
		{"claude --resume " + cursorIDA, ""},
	} {
		got, ok := d.cursorSessionIDFromCommand(tt.command)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("cursorSessionIDFromCommand(%q) = %q, %v; want %q", tt.command, got, ok, tt.want)
		}
	}
}

func TestCursorProjectKey(t *testing.T) {
	for in, want := range map[string]string{
		"/Users/me/Repository/Codex-History": "users-me-repository-codex-history",
		"Users-me-Repository-Codex-History":  "users-me-repository-codex-history",
		"/home/me/my_app/.config":            "home-me-my-app-config",
		"home-me-my-app--config":             "home-me-my-app-config",
		"/":                                  "",
		"":                                   "",
	} {
		if got := CursorProjectKey(in); got != want {
			t.Errorf("CursorProjectKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCursorWorkspace(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"work/my-app", "work/my", "work/other.repo/sub", "work/file-dir"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fixtureFile(t, filepath.Join(root, "work", "plain"), "")
	for _, tt := range []struct {
		name, want string
	}{
		// A hyphen in a directory name survives when that directory exists.
		{"work-my-app", filepath.Join(root, "work", "my-app")},
		{"work-my", filepath.Join(root, "work", "my")},
		{"work-other-repo-sub", filepath.Join(root, "work", "other.repo", "sub")},
		{"Work-Other-Repo", filepath.Join(root, "work", "other.repo")},
		// Nothing is guessed: a missing directory or a file gives "".
		{"work-gone", ""},
		{"work-plain", ""},
		{"", ""},
	} {
		if got := cursorWorkspaceFrom(root, tt.name); got != tt.want {
			t.Errorf("cursorWorkspaceFrom(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
	// From the filesystem root, the real encoding of a temporary directory
	// resolves.
	// Cursor writes no colon in the name, which no directory could hold.
	name := strings.ReplaceAll(strings.TrimLeft(strings.ReplaceAll(filepath.ToSlash(filepath.Join(root, "work", "my-app")), "/", "-"), "-"), ":", "")
	if got := CursorWorkspace(name); got != filepath.Join(root, "work", "my-app") {
		t.Errorf("CursorWorkspace(%q) = %q", name, got)
	}
}

func TestCursorTranscriptFiles(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "projects", "home-me-demo")
	transcripts := filepath.Join(project, "agent-transcripts")
	nested := filepath.Join(transcripts, cursorIDA, cursorIDA+".jsonl")
	flat := filepath.Join(transcripts, cursorIDB+".jsonl")
	for _, path := range []string{
		nested, flat,
		filepath.Join(transcripts, cursorIDA, "subagents", cursorIDC+".jsonl"),
		filepath.Join(transcripts, cursorIDD, cursorIDC+".jsonl"),
		filepath.Join(transcripts, "notes.jsonl"),
		filepath.Join(transcripts, cursorIDD+".txt"),
	} {
		fixtureFile(t, path, "{}\n")
	}
	if got := CursorSessionFiles(project); !reflect.DeepEqual(got, []string{nested, flat}) && !reflect.DeepEqual(got, []string{flat, nested}) {
		t.Errorf("CursorSessionFiles = %q", got)
	}
	for id, want := range map[string]string{cursorIDA: nested, cursorIDB: flat, cursorIDC: "", "*": "", "../x": ""} {
		if got := FindCursorTranscript(dir, id); got != want {
			t.Errorf("FindCursorTranscript(%s) = %q, want %q", id, got, want)
		}
	}
	if got := CursorProjectDirs(dir, "/home/me/demo"); !reflect.DeepEqual(got, []string{project}) {
		t.Errorf("CursorProjectDirs = %q", got)
	}
	if CursorProjectDirs(dir, "") != nil || CursorProjectDirs(dir, "/home/me/other") != nil {
		t.Error("unrelated working directories matched a project")
	}
}

func cursorLines(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func TestReadCursorTranscriptState(t *testing.T) {
	const (
		prompt   = `{"role":"user","message":{"content":[{"type":"text","text":"<user_query>go</user_query>"}]}}`
		thinking = `{"role":"assistant","message":{"content":[{"type":"thinking","thinking":"hmm"}]}}`
		call     = `{"role":"assistant","message":{"content":[{"type":"text","text":"Running."},{"type":"tool_use","id":"t1","name":"Shell","input":{}}]}}`
		hyphen   = `{"role":"assistant","message":{"content":[{"type":"tool-call","name":"read_file"}]}}`
		result   = `{"role":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`
		reply    = `{"role":"assistant","model":"cursor-test","message":{"content":[{"type":"text","text":"Done."}]}}`
		plain    = `{"role":"assistant","message":{"content":"Done."}}`
		ended    = `{"type":"turn_ended","status":"success"}`
		failed   = `{"type":"turn_ended","status":"error","error":"boom"}`
	)
	dir := t.TempDir()
	for _, tt := range []struct {
		name  string
		body  string
		state State
	}{
		{"prompt", cursorLines(prompt), SessionStateActive},
		{"thinking", cursorLines(prompt, thinking), SessionStateActive},
		{"tool call", cursorLines(prompt, call), SessionStateActive},
		{"hyphenated tool call", cursorLines(prompt, hyphen), SessionStateActive},
		{"tool result", cursorLines(prompt, call, result), SessionStateActive},
		{"reply", cursorLines(prompt, call, result, reply), SessionStateWaiting},
		{"plain reply", cursorLines(prompt, plain), SessionStateWaiting},
		{"turn ended", cursorLines(prompt, call, ended), SessionStateWaiting},
		{"turn failed", cursorLines(prompt, failed), SessionStateWaiting},
		{"new prompt", cursorLines(prompt, reply, prompt), SessionStateActive},
		{"partial line", cursorLines(prompt, reply) + prompt[:20], SessionStateWaiting},
		{"malformed", cursorLines("not json", "{}", "[1]"), SessionStateUnknown},
		{"empty", "", SessionStateUnknown},
	} {
		path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-")+".jsonl")
		fixtureFile(t, path, tt.body)
		got, err := ReadCursorTranscript(path)
		if err != nil || got.State != tt.state {
			t.Errorf("%s: state %s, %v; want %s", tt.name, got.State, err, tt.state)
		}
		if info, _ := os.Stat(path); !got.UpdatedAt.Equal(info.ModTime()) {
			t.Errorf("%s: UpdatedAt is not the file's modification time", tt.name)
		}
	}
	path := filepath.Join(dir, "model.jsonl")
	fixtureFile(t, path, cursorLines(prompt, reply))
	if got, _ := ReadCursorTranscript(path); got.Model != "cursor-test" {
		t.Errorf("model = %q", got.Model)
	}
	if _, err := ReadCursorTranscript(filepath.Join(dir, "missing.jsonl")); err == nil || strings.Contains(err.Error(), dir) {
		t.Errorf("missing transcript: %v", err)
	}
}

func TestParseCursorChatMeta(t *testing.T) {
	encode := func(s string) string { return hex.EncodeToString([]byte(s)) }
	full := `{"agentId":"` + cursorIDA + `","name":" Fix it ","createdAt":1788220800000,"mode":"default","lastUsedModel":"gpt-5"}`
	got, err := parseCursorChatMeta(encode(full))
	if err != nil || got.ID != cursorIDA || got.Name != "Fix it" || got.Model != "gpt-5" || !got.CreatedAt.Equal(time.UnixMilli(1788220800000)) {
		t.Fatalf("hex metadata: %+v, %v", got, err)
	}
	// Plain JSON is accepted too, and the automatic model is left out.
	got, err = parseCursorChatMeta(`{"agentId":"` + cursorIDA + `","lastUsedModel":"default"}`)
	if err != nil || got.Model != "" || !got.CreatedAt.IsZero() {
		t.Fatalf("plain metadata: %+v, %v", got, err)
	}
	for name, value := range map[string]string{
		"empty":     "",
		"not json":  encode("nope"),
		"no id":     encode(`{"name":"x"}`),
		"bad id":    encode(`{"agentId":"../x"}`),
		"too large": strings.Repeat("0", cursorChatMetaMaxBytes+2),
	} {
		if _, err := parseCursorChatMeta(value); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// writeCursorChat creates a chat database with sqlite3, holding meta as
// Cursor stores it.
func writeCursorChat(t *testing.T, path, meta string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	sql := "CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT); CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB);" +
		"INSERT INTO meta VALUES ('0', '" + hex.EncodeToString([]byte(meta)) + "');"
	if out, err := exec.Command("sqlite3", path, sql).CombinedOutput(); err != nil {
		t.Fatalf("create chat database: %v: %s", err, out)
	}
}

func TestReadCursorChat(t *testing.T) {
	testenv.NeedTool(t, "sqlite3")
	path := filepath.Join(t.TempDir(), "store.db")
	writeCursorChat(t, path, `{"agentId":"`+cursorIDA+`","name":"Chat","lastUsedModel":"gpt-5"}`)
	got, err := ReadCursorChat(context.Background(), path)
	if err != nil || got.ID != cursorIDA || got.Name != "Chat" || got.Model != "gpt-5" {
		t.Fatalf("got %+v, %v", got, err)
	}
	empty := filepath.Join(t.TempDir(), "empty.db")
	writeCursorChat(t, empty, `{}`)
	if _, err := ReadCursorChat(context.Background(), empty); err == nil {
		t.Fatal("chat without an agent id accepted")
	}
	if _, err := ReadCursorChat(context.Background(), filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("missing database accepted")
	}
}

func TestDiscoverCursorSessions(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "cursor-data")
	cwd := filepath.Join(home, "work", "demo")
	otherCwd := filepath.Join(home, "work", "other")
	transcripts := filepath.Join(dir, "projects", CursorProjectKey(cwd), "agent-transcripts")
	explicit := filepath.Join(dir, "projects", CursorProjectKey(otherCwd), "agent-transcripts", cursorIDA, cursorIDA+".jsonl")
	older := filepath.Join(transcripts, cursorIDB, cursorIDB+".jsonl")
	newer := filepath.Join(transcripts, cursorIDC, cursorIDC+".jsonl")
	const prompt = `{"role":"user","message":{"content":[{"type":"text","text":"<user_query>go</user_query>"}]}}`
	const reply = `{"role":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}`
	fixtureFile(t, explicit, cursorLines(prompt, reply))
	fixtureFile(t, older, cursorLines(prompt, reply))
	fixtureFile(t, newer, cursorLines(prompt))
	// A subagent transcript is never a candidate.
	fixtureFile(t, filepath.Join(transcripts, cursorIDC, "subagents", cursorIDA+".jsonl"), cursorLines(prompt))
	// The newest chat has a database but no transcript yet.
	chats := CursorChatsDir(dir, cwd)
	for _, id := range []string{cursorIDB, cursorIDD} {
		fixtureFile(t, filepath.Join(chats, id, "store.db"), "")
	}
	base := time.Now().Add(-time.Hour)
	for i, path := range []string{older, filepath.Join(chats, cursorIDB, "store.db"), newer, explicit, filepath.Join(chats, cursorIDD, "store.db")} {
		stamp := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	opts := Options{Home: home, CursorDir: dir}
	opts.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ps":
			return []byte(strings.Join([]string{
				"50 1 pts/0 00:01 bash /home/u/.local/bin/cursor-agent --resume " + cursorIDA,
				"51 50 pts/0 00:01 " + cursorNode + " --resume " + cursorIDA,
				"60 1 pts/1 00:02 cursor-agent",
				"70 1 pts/2 00:03 cursor-agent --model gpt-5-codex",
				"75 1 pts/4 00:03 cursor-agent",
				"80 1 ? 00:04 cursor-agent status",
				"90 1 pts/3 00:05 cursor-agent",
			}, "\n")), nil
		case "lsof":
			if reflect.DeepEqual(args, []string{"-a", "-d", "cwd", "-Fn", "-p", "50,60,70,75,90"}) {
				return []byte("p50\nn" + otherCwd + "\np60\nn" + cwd + "\np70\nn" + cwd + "\np75\nn" + cwd + "\np90\nn" + filepath.Join(home, "no-sessions") + "\n"), nil
			}
			if reflect.DeepEqual(args, []string{"-a", "-d", "cwd", "-Fn", "-p", "90"}) {
				return []byte("p90\nn" + filepath.Join(home, "no-sessions") + "\n"), nil
			}
			return nil, fmt.Errorf("unexpected lsof %v", args)
		case "sqlite3":
			// Only the chat with a transcript has readable metadata.
			if len(args) == 4 && args[2] == filepath.Join(chats, cursorIDB, "store.db") {
				meta := hex.EncodeToString([]byte(`{"agentId":"` + cursorIDB + `","name":"Older","createdAt":1788220800000,"lastUsedModel":"gpt-5"}`))
				return []byte(`[{"value":"` + meta + `"}]`), nil
			}
			return nil, fmt.Errorf("no such table: meta")
		case "git":
			return nil, fmt.Errorf("not a repository")
		}
		t.Fatalf("unexpected command %s", name)
		return nil, nil
	}
	metas, err := Discover(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "Cursor: metadata unavailable for 1 runtime(s)") {
		t.Fatalf("want a warning for the unmapped runtime, got %v", err)
	}
	byPID := map[int]Meta{}
	for _, m := range metas {
		byPID[m.PID] = m
	}
	if len(metas) != 5 || byPID[80].PID != 0 {
		t.Fatalf("got %d runtimes; helpers must not count as sessions", len(metas))
	}
	if len(byPID[50].Runtime.Processes) != 2 {
		t.Error("node child not grouped under its launcher")
	}
	for _, tt := range []struct {
		pid   int
		id    string
		title string
		model string
		state State
		cwd   string
		path  string
	}{
		{50, cursorIDA, CursorSource, "", SessionStateWaiting, otherCwd, explicit},
		// Higher PIDs are newer processes and take newer chats.
		{75, cursorIDD, CursorSource, "", SessionStateUnknown, cwd, ""},
		{70, cursorIDC, CursorSource, "", SessionStateActive, cwd, newer},
		{60, cursorIDB, "Older", "gpt-5", SessionStateWaiting, cwd, older},
		{90, "", "", "", SessionStateUnknown, filepath.Join(home, "no-sessions"), ""},
	} {
		m := byPID[tt.pid]
		if m.Provider != "cursor" || m.ID != tt.id || m.Title != tt.title || m.Model != tt.model || m.State != tt.state || m.CWD != tt.cwd || m.RolloutPath != tt.path {
			t.Errorf("pid %d: provider %q id %q title %q model %q state %s cwd %q path %q; want id %q title %q model %q state %s cwd %q path %q",
				tt.pid, m.Provider, m.ID, m.Title, m.Model, m.State, m.CWD, m.RolloutPath, tt.id, tt.title, tt.model, tt.state, tt.cwd, tt.path)
		}
		if tt.id != "" && (m.Source != CursorSource || m.LastActivityAt == nil) {
			t.Errorf("pid %d: metadata %+v", tt.pid, m)
		}
	}
	if m := byPID[60]; m.StartedAt == nil || !m.StartedAt.Equal(time.UnixMilli(1788220800000)) {
		t.Error("chat creation time not used as the start time")
	}
}

func TestCursorDirHonorsEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CURSOR_CONFIG_DIR", filepath.Join(home, "custom"))
	if dir, err := CursorDir(); err != nil || dir != filepath.Join(home, "custom") {
		t.Fatalf("CursorDir = %q, %v", dir, err)
	}
	t.Setenv("CURSOR_CONFIG_DIR", "")
	d := &discoverer{opts: Options{Home: home}}
	if dir, _ := d.cursorDir(); dir != filepath.Join(home, ".cursor") {
		t.Fatalf("default dir %q", dir)
	}
	d.opts.CursorDir = "/override"
	if dir, _ := d.cursorDir(); dir != "/override" {
		t.Fatalf("override dir %q", dir)
	}
}
