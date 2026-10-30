package session

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/testenv"
)

func TestValidOpenCodeSessionID(t *testing.T) {
	for id, want := range map[string]bool{
		"ses_ffe9a1b2c3d4Abc123XYZ789":    true,
		"ses-custom_1":                    true,
		"ses":                             false,
		"ses_":                            true,
		"session":                         true,
		"msg_123":                         false,
		"ses_x'1":                         false,
		"ses_a b":                         false,
		"ses_" + strings.Repeat("a", 125): false,
		"":                                false,
	} {
		if got := ValidOpenCodeSessionID(id); got != want {
			t.Errorf("ValidOpenCodeSessionID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestOpenCodeTranscriptPath(t *testing.T) {
	path := OpenCodeTranscriptPath("/data/a#b/opencode.db", "ses_1")
	db, id, ok := SplitOpenCodeTranscriptPath(path)
	if !ok || db != "/data/a#b/opencode.db" || id != "ses_1" {
		t.Errorf("split %q = %q, %q, %v", path, db, id, ok)
	}
	for _, bad := range []string{"", "/data/opencode.db", "/data/x.jsonl#notsession", "#ses_1"} {
		if _, _, ok := SplitOpenCodeTranscriptPath(bad); ok {
			t.Errorf("split %q succeeded", bad)
		}
	}
}

func TestOpenCodeDatabase(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "xdg")
	for name, c := range map[string]struct {
		xdg, db, want string
	}{
		"default":          {"", "", filepath.Join(home, ".local", "share", "opencode", "opencode.db")},
		"xdg":              {data, "", filepath.Join(data, "opencode", "opencode.db")},
		"relative xdg":     {"relative", "", filepath.Join(home, ".local", "share", "opencode", "opencode.db")},
		"absolute db":      {data, filepath.Join(home, "elsewhere", "oc.db"), filepath.Join(home, "elsewhere", "oc.db")},
		"relative db":      {data, "dev.db", filepath.Join(data, "opencode", "dev.db")},
		"in-memory db":     {"", ":memory:", ""},
		"whitespace value": {"", "  ", filepath.Join(home, ".local", "share", "opencode", "opencode.db")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", c.xdg)
			t.Setenv("OPENCODE_DB", c.db)
			got, err := OpenCodeDatabase(home)
			if err != nil || got != c.want {
				t.Errorf("OpenCodeDatabase = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestOpenCodeSessionIDFromCommand(t *testing.T) {
	d := &discoverer{}
	const id = "ses_ffe9a1b2c3d4Abc123XYZ789"
	tests := []struct {
		command string
		want    string
	}{
		{"opencode -s " + id, id},
		{"opencode --session " + id, id},
		{"opencode --session=" + id, id},
		{"/home/u/.opencode/bin/opencode run --session '" + id + "' fix it", id},
		{"opencode mini -s " + id, id},
		// A fork continues into a new session.
		{"opencode -s " + id + " --fork", ""},
		{"opencode --continue", ""},
		{"opencode -s", ""},
		{"opencode -s not-a-session", ""},
		// Helpers do not run a session even when they name one.
		{"opencode session export " + id, ""},
		{"opencode serve --session " + id, ""},
		{"codex -s " + id, ""},
	}
	for _, tt := range tests {
		got, ok := d.openCodeSessionIDFromCommand(tt.command)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("openCodeSessionIDFromCommand(%q) = %q, %v; want %q", tt.command, got, ok, tt.want)
		}
	}
}

func TestOpenCodeState(t *testing.T) {
	for lastType, want := range map[string]State{
		"":               SessionStateWaiting,
		"idle":           SessionStateWaiting,
		"user":           SessionStateActive,
		"assistant":      SessionStateActive,
		"shell":          SessionStateActive,
		"model-switched": SessionStateUnknown,
		"future-type":    SessionStateUnknown,
	} {
		if got := openCodeState(lastType); got != want {
			t.Errorf("openCodeState(%q) = %s, want %s", lastType, got, want)
		}
	}
}

func TestDiscoverOpenCodeSessions(t *testing.T) {
	testenv.NeedTool(t, "sqlite3")
	home := t.TempDir()
	cwd := filepath.Join(home, "work", "demo")
	otherCwd := filepath.Join(home, "work", "other")
	db := filepath.Join(home, "opencode.db")
	base := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC).UnixMilli()
	model := `'{"id":"model-test","providerID":"provider-test"}'`
	statements := []string{
		`CREATE TABLE session_v2 (id text PRIMARY KEY, project_id text NOT NULL, parent_id text,
			slug text NOT NULL, directory text NOT NULL, title text, version text NOT NULL,
			tokens_input integer DEFAULT 0 NOT NULL, tokens_output integer DEFAULT 0 NOT NULL,
			tokens_reasoning integer DEFAULT 0 NOT NULL, tokens_cache_read integer DEFAULT 0 NOT NULL,
			tokens_cache_write integer DEFAULT 0 NOT NULL, model text,
			time_created integer NOT NULL, time_updated integer NOT NULL)`,
		`CREATE TABLE session_message (id text PRIMARY KEY, session_id text NOT NULL, type text NOT NULL,
			seq integer NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL)`,
	}
	session := func(id, parent, dir, title string, updated int64) {
		p := "NULL"
		if parent != "" {
			p = sqlQuote(parent)
		}
		statements = append(statements, fmt.Sprintf(`INSERT INTO session_v2 VALUES (%s, 'prj', %s, 's', %s, %s, '2',
			10, 4, 1, 100, 5, %s, %d, %d)`, sqlQuote(id), p, sqlQuote(dir), sqlQuote(title), model, base, base+updated))
	}
	message := func(sessionID, kind string, seq, updated int64) {
		statements = append(statements, fmt.Sprintf(`INSERT INTO session_message VALUES ('msg_%s_%d', %s, %s, %d, %d, %d, '{}')`,
			sessionID, seq, sqlQuote(sessionID), sqlQuote(kind), seq, base, base+updated))
	}
	// The explicit session is the newest in cwd, but its runtime claims it.
	session("ses_explicit", "", cwd, "Explicit", 5000)
	session("ses_old", "", cwd, "Older", 1000)
	session("ses_new", "", cwd, "Newer", 2000)
	// Subagent sessions have a parent and never map to a runtime.
	session("ses_child", "ses_new", cwd, "Child", 9000)
	session("ses_away", "", otherCwd, "Elsewhere", 8000)
	message("ses_old", "user", 1, 1000)
	message("ses_old", "idle", 2, 1000)
	message("ses_new", "idle", 1, 1500)
	message("ses_new", "assistant", 3, 7000)
	if out, err := exec.Command("sqlite3", db, strings.Join(statements, ";\n")+";").CombinedOutput(); err != nil {
		t.Fatalf("build database: %v: %s", err, out)
	}

	opts := Options{Home: home, OpenCodeDB: db}
	opts.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ps":
			return []byte(strings.Join([]string{
				"50 1 pts/0 00:01 opencode -s ses_explicit",
				"60 1 pts/1 00:02 opencode",
				"70 1 pts/2 00:03 /home/u/.opencode/bin/opencode run fix it",
				"80 1 ? 00:04 opencode serve --port 4096",
				"90 1 pts/3 00:05 opencode",
			}, "\n")), nil
		case "lsof":
			if reflect.DeepEqual(args, []string{"-a", "-d", "cwd", "-Fn", "-p", "60,70,90"}) {
				return []byte("p60\nn" + cwd + "\np70\nn" + cwd + "\np90\nn" + filepath.Join(home, "no-sessions") + "\n"), nil
			}
			return nil, fmt.Errorf("unexpected lsof %v", args)
		case "git":
			return nil, fmt.Errorf("not a repository")
		case "sqlite3":
			return exec.CommandContext(ctx, name, args...).Output()
		}
		t.Fatalf("unexpected command %s", name)
		return nil, nil
	}
	metas, err := Discover(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "OpenCode: metadata unavailable for 1 runtime(s)") {
		t.Fatalf("want a warning for the unmapped runtime, got %v", err)
	}
	byPID := map[int]Meta{}
	for _, m := range metas {
		byPID[m.PID] = m
	}
	if len(metas) != 5 || byPID[80].ID != "" {
		t.Fatalf("got %d runtimes; the background server must not get a session", len(metas))
	}
	tests := []struct {
		pid   int
		id    string
		title string
		state State
	}{
		{50, "ses_explicit", "Explicit", SessionStateWaiting},
		// Higher PIDs are newer processes and take newer sessions.
		{70, "ses_new", "Newer", SessionStateActive},
		{60, "ses_old", "Older", SessionStateWaiting},
		{90, "", "", SessionStateUnknown},
	}
	for _, tt := range tests {
		m := byPID[tt.pid]
		if m.Provider != "opencode" || m.ID != tt.id || m.Title != tt.title || m.State != tt.state {
			t.Errorf("pid %d: provider %q id %q title %q state %s; want id %q title %q state %s",
				tt.pid, m.Provider, m.ID, m.Title, m.State, tt.id, tt.title, tt.state)
		}
		if tt.id == "" {
			continue
		}
		if m.CWD != cwd || m.Model != "model-test" || m.TokensUsed != 120 || m.RolloutPath != OpenCodeTranscriptPath(db, tt.id) {
			t.Errorf("pid %d: metadata %+v", tt.pid, m)
		}
		if m.StartedAt == nil || m.StartedAt.UnixMilli() != base {
			t.Errorf("pid %d: started %v", tt.pid, m.StartedAt)
		}
	}
	// The latest message counts as activity even when the session row is
	// older.
	if got := byPID[70].LastActivityAt; got == nil || got.UnixMilli() != base+7000 {
		t.Errorf("last activity = %v, want the latest message", got)
	}
}

func TestDiscoverOpenCodeWithoutDatabase(t *testing.T) {
	home := t.TempDir()
	opts := Options{Home: home, OpenCodeDB: filepath.Join(home, "missing.db")}
	opts.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ps":
			return []byte("60 1 pts/1 00:02 opencode\n"), nil
		case "lsof", "git":
			return nil, fmt.Errorf("unavailable")
		}
		t.Fatalf("unexpected command %s", name)
		return nil, nil
	}
	metas, err := Discover(context.Background(), opts)
	if err != nil || len(metas) != 1 || metas[0].ID != "" || metas[0].Provider != "opencode" {
		t.Fatalf("Discover = %+v, %v; want one runtime without a session and no warning", metas, err)
	}
}
