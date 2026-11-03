package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Recent Codex builds keep no rollout open in the TUI process; an
// app-server holds the thread-writer locks instead. The CLI must still get
// its session, matched by working directory.
func TestEnrichCodexSessionsMatchesHeldThreadByCWD(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "state_5.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	const (
		threadA = "01a141cf-34d4-7813-9969-ad12c634738d"
		threadB = "01a10dae-2417-7fd1-b90a-9798256f74a6"
		subID   = "01a131d1-68e6-76a1-9a85-536c8a00dc20"
	)
	// The paths go into JSON, where Windows backslashes need escaping.
	rolloutB := jsonPath(filepath.Join(codexHome, "sessions", "rollout-2026-10-05T13-07-58-"+threadB+".jsonl"))
	locks := filepath.Join(codexHome, "thread-writer-locks")
	metadataJSON := `[
	 {"id":"` + threadA + `","display_name":"a","cwd":"/work/a","updated_at_ms":10,"rollout_path":"` + jsonPath(filepath.Join(codexHome, "sessions", "rollout-x-"+threadA+".jsonl")) + `"},
	 {"id":"` + threadB + `","display_name":"b","cwd":"/work/b","source":"vscode","updated_at_ms":20,"rollout_path":"` + rolloutB + `"},
	 {"id":"` + subID + `","display_name":"sub","cwd":"/work/b","source":"{\"subagent\":{}}","updated_at_ms":99,"rollout_path":"` + rolloutB + `"}
	]`

	d := &discoverer{ctx: context.Background(), opts: Options{Home: home, CodexHome: codexHome, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "lsof" && strings.Contains(joined, "-d cwd"):
			return []byte("p100\nn/work/b\n"), nil
		case name == "lsof":
			return []byte("p200\nn" + filepath.Join(locks, threadA+".lock") + "\nn" + filepath.Join(locks, threadB+".lock") + "\nn" + filepath.Join(locks, subID+".lock") + "\n"), nil
		case name == "sqlite3":
			if !strings.Contains(joined, "rollout_path") {
				t.Errorf("thread query does not select rollout_path")
			}
			return []byte(metadataJSON), nil
		}
		return nil, os.ErrNotExist
	}}}

	groups := d.groupProcesses(d.parseProcesses("100 1 ttys001 01:00 codex\n200 1 ?? 02:00 codex app-server --listen unix://"))
	if err := d.enrichCodexSessions(groups); err != nil {
		t.Fatal(err)
	}
	for _, group := range groups {
		switch group.Root.PID {
		case 100:
			if len(group.Sessions) != 1 || group.Sessions[0].ID != threadB {
				t.Errorf("CLI sessions = %+v, want thread %s", group.Sessions, threadB)
			}
		case 200:
			// The server holds thread A's lock and no session group claimed
			// A, so the server keeps it (evidence; invariants 6 and 8). B is
			// the CLI's, so the server doesn't show it too.
			if len(group.Sessions) != 1 || group.Sessions[0].ID != threadA {
				t.Errorf("app-server sessions = %+v, want only the unclaimed thread %s", group.Sessions, threadA)
			}
		}
	}
}

// jsonPath escapes a path for use inside a JSON string.
func jsonPath(path string) string { return strings.ReplaceAll(path, `\`, `\\`) }

// The app-server daemon behind the Codex app holds the rollout of a CLI's
// thread open, and its own guardian subagent thread too. The CLI in a terminal
// must get its thread (so switching goes to its TTY), with the subagent listed
// under it, and the daemon must show neither.
func TestEnrichCodexSessionsCLITakesThreadHeldByAppServerDaemon(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "state_5.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	const (
		cliThread = "01a1996c-e4af-7af1-851c-69404a420242"
		guardian  = "01a1996c-e645-7781-8069-3e95a35b075d"
	)
	rolloutCLI := filepath.Join(codexHome, "sessions", "rollout-2026-11-01T15-23-32-"+cliThread+".jsonl")
	rolloutGuardian := filepath.Join(codexHome, "sessions", "rollout-2026-11-01T15-23-33-"+guardian+".jsonl")
	metadataJSON := `[
	 {"id":"` + cliThread + `","display_name":"cli","cwd":"/work/repo","source":"vscode","updated_at_ms":20,"rollout_path":"` + jsonPath(rolloutCLI) + `"},
	 {"id":"` + guardian + `","display_name":"Guardian review","cwd":"/work/repo","source":"{\"subagent\":{\"other\":\"guardian\"}}","updated_at_ms":30,"rollout_path":"` + jsonPath(rolloutGuardian) + `"}
	]`
	d := &discoverer{ctx: context.Background(), opts: Options{Home: home, CodexHome: codexHome, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "lsof" && strings.Contains(joined, "-d cwd"):
			return []byte("p100\nn/work/repo\n"), nil
		case name == "lsof":
			return []byte("p200\nn" + rolloutCLI + "\nn" + rolloutGuardian + "\n"), nil
		case name == "sqlite3":
			return []byte(metadataJSON), nil
		}
		return nil, os.ErrNotExist
	}}}

	groups := d.groupProcesses(d.parseProcesses("100 1 ttys006 01:00 codex\n200 1 ?? 02:00 codex app-server --listen unix://"))
	if err := d.enrichCodexSessions(groups); err != nil {
		t.Fatal(err)
	}
	for _, group := range groups {
		switch group.Root.PID {
		case 100:
			if len(group.Sessions) != 2 || group.Sessions[0].ID != cliThread || group.Sessions[0].Subagent ||
				group.Sessions[1].ID != guardian || !group.Sessions[1].Subagent {
				t.Errorf("CLI sessions = %+v, want thread %s then subagent %s", group.Sessions, cliThread, guardian)
			}
		case 200:
			if len(group.Sessions) != 0 {
				t.Errorf("app-server sessions = %+v, want none", group.Sessions)
			}
		}
	}
}

// Real state databases label spawned threads through thread_source ("subagent",
// "guardian_review"), so the raw source column is what names the parent.
func TestEnrichCodexSessionsListsSubagentUnderItsParent(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "state_5.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	const (
		parentA = "01a10000-0000-7000-8000-00000000000a"
		parentB = "01a10000-0000-7000-8000-00000000000b"
		subOfA  = "01a10000-0000-7000-8000-0000000000a1"
	)
	rollout := func(id string) string {
		return filepath.Join(codexHome, "sessions", "rollout-2026-11-01T10-00-00-"+id+".jsonl")
	}
	metadataJSON := `[
	 {"id":"` + parentA + `","display_name":"A","cwd":"/work/a","source":"user","raw_source":"cli","updated_at_ms":10,"rollout_path":"` + jsonPath(rollout(parentA)) + `"},
	 {"id":"` + parentB + `","display_name":"B","cwd":"/work/b","source":"user","raw_source":"cli","updated_at_ms":20,"rollout_path":"` + jsonPath(rollout(parentB)) + `"},
	 {"id":"` + subOfA + `","display_name":"sub","cwd":"/work/b","source":"subagent","raw_source":"{\"subagent\":{\"thread_spawn\":{\"parent_thread_id\":\"` + parentA + `\",\"agent_nickname\":\"Galileo\",\"agent_role\":\"explorer\"}}}","updated_at_ms":30,"rollout_path":"` + jsonPath(rollout(subOfA)) + `"}
	]`
	d := &discoverer{ctx: context.Background(), opts: Options{Home: home, CodexHome: codexHome, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "lsof":
			if strings.Contains(strings.Join(args, " "), "-d cwd") {
				return nil, os.ErrNotExist
			}
			return []byte("p100\nn" + rollout(parentA) + "\np101\nn" + rollout(parentB) + "\np200\nn" + rollout(subOfA) + "\n"), nil
		case "sqlite3":
			return []byte(metadataJSON), nil
		}
		return nil, os.ErrNotExist
	}}}

	groups := d.groupProcesses(d.parseProcesses("100 1 ttys001 01:00 codex\n101 1 ttys002 01:00 codex\n200 1 ?? 02:00 codex app-server --listen unix://"))
	if err := d.enrichCodexSessions(groups); err != nil {
		t.Fatal(err)
	}
	for _, group := range groups {
		switch group.Root.PID {
		case 100:
			if len(group.Sessions) != 2 || group.Sessions[0].ID != parentA || group.Sessions[1].ID != subOfA ||
				group.Sessions[1].ParentID != parentA || group.Sessions[1].AgentName != "Galileo" ||
				group.Sessions[1].AgentRole != "explorer" || group.Sessions[0].ParentID != "" {
				t.Errorf("parent A sessions = %+v", group.Sessions)
			}
		case 101:
			if len(group.Sessions) != 1 || group.Sessions[0].ID != parentB {
				t.Errorf("parent B sessions = %+v, want only its own thread", group.Sessions)
			}
		case 200:
			if len(group.Sessions) != 0 {
				t.Errorf("app-server sessions = %+v, want none", group.Sessions)
			}
		}
	}
}

func TestParseCodexSpawn(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want CodexSpawn
	}{
		{`{"subagent":{"thread_spawn":{"parent_thread_id":"p1","depth":1,"agent_path":null,"agent_nickname":"Galileo","agent_role":"explorer"}}}`, CodexSpawn{"p1", "Galileo", "explorer"}},
		{`{"subagent":{"thread_spawn":{"parent_thread_id":" p1 ","depth":2,"agent_nickname":null,"agent_role":null}}}`, CodexSpawn{ParentID: "p1"}},
		{`{"subagent":{"thread_spawn":{"parent_thread_id":"p1"}}}`, CodexSpawn{ParentID: "p1"}},
		{`{"subagent":{"thread_spawn":{"parent_thread_id":"p1","agent_type":"worker"}}}`, CodexSpawn{ParentID: "p1", AgentRole: "worker"}},
		{`{"subagent":{"thread_spawn":{"parent_thread_id":"p1","agent_role":"explorer","agent_type":"worker"}}}`, CodexSpawn{ParentID: "p1", AgentRole: "explorer"}},
		{`{"subagent":"review"}`, CodexSpawn{}},
		{`{"subagent":{"other":"guardian"}}`, CodexSpawn{}},
		{`"cli"`, CodexSpawn{}},
		{`cli`, CodexSpawn{}},
		{``, CodexSpawn{}},
		{`{"subagent":{"thread_spawn":{"parent_thread_id":7}}}`, CodexSpawn{}},
	} {
		if got := ParseCodexSpawn([]byte(tc.raw)); got != tc.want {
			t.Errorf("ParseCodexSpawn(%s) = %+v, want %+v", tc.raw, got, tc.want)
		}
	}
}

// TestDiscoverCodexSubagentMeta checks that Discover hands a Codex subagent's
// parent, name and role to session.Meta, which the reporter uploads, and
// leaves them empty on the top-level thread.
func TestDiscoverCodexSubagentMeta(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "state_5.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	const (
		parent = "01a10000-0000-7000-8000-00000000000a"
		sub    = "01a10000-0000-7000-8000-0000000000a1"
	)
	rollout := func(id string) string {
		return filepath.Join(codexHome, "sessions", "rollout-2026-11-01T10-00-00-"+id+".jsonl")
	}
	// sub was spawned with parent's context: its own session_meta names the
	// fork source, and the copied one after it names another. parent's
	// rollout only has a copied session_meta naming a fork source.
	writeRollout := func(id, lines string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(rollout(id)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rollout(id), []byte(lines), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const other = "01a10000-0000-7000-8000-0000000000ff"
	writeRollout(sub, `{"type":"session_meta","payload":{"id":"`+sub+`","forked_from_id":"`+parent+`","source":"cli"}}`+"\n"+
		`{"type":"session_meta","payload":{"id":"`+parent+`","forked_from_id":"`+other+`"}}`+"\n")
	writeRollout(parent, `{"type":"session_meta","payload":{"id":"`+parent+`","source":"cli"}}`+"\n"+
		`{"type":"session_meta","payload":{"id":"`+other+`","forked_from_id":"`+sub+`"}}`+"\n")
	metadataJSON := `[
	 {"id":"` + parent + `","display_name":"A","cwd":"/work/a","source":"user","raw_source":"cli","updated_at_ms":10,"rollout_path":"` + jsonPath(rollout(parent)) + `"},
	 {"id":"` + sub + `","display_name":"sub","cwd":"/work/a","source":"subagent","raw_source":"{\"subagent\":{\"thread_spawn\":{\"parent_thread_id\":\"` + parent + `\",\"depth\":1,\"agent_nickname\":\"Galileo\",\"agent_role\":\"explorer\"}}}","updated_at_ms":30,"rollout_path":"` + jsonPath(rollout(sub)) + `"}
	]`
	opts := Options{Home: home, CodexHome: codexHome, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ps":
			return []byte("100 1 ttys001 01:00 codex\n"), nil
		case "lsof":
			if strings.Contains(strings.Join(args, " "), "-d cwd") {
				return nil, os.ErrNotExist
			}
			return []byte("p100\nn" + rollout(parent) + "\nn" + rollout(sub) + "\n"), nil
		case "sqlite3":
			return []byte(metadataJSON), nil
		}
		return nil, os.ErrNotExist
	}}
	metas, err := Discover(context.Background(), opts)
	if err != nil {
		var warning *Warning
		if !errors.As(err, &warning) {
			t.Fatal(err)
		}
	}
	byID := map[string]Meta{}
	for _, m := range metas {
		byID[m.ID] = m
	}
	if m, ok := byID[sub]; !ok || m.ParentID != parent || m.AgentName != "Galileo" || m.AgentRole != "explorer" || m.ForkedFromID != parent {
		t.Errorf("subagent meta = %+v (found %v)", m, ok)
	}
	if m, ok := byID[parent]; !ok || m.ParentID != "" || m.AgentName != "" || m.AgentRole != "" || m.ForkedFromID != "" {
		t.Errorf("parent meta = %+v (found %v)", m, ok)
	}
}

// TestParseCodexSessionMeta checks the session_meta fields Firekeeper reads:
// forked_from_id, and the top-level parent_thread_id, agent_nickname and
// agent_role (or agent_type) that newer Codex writes, used only to fill what
// a spawned subagent's source leaves empty.
func TestParseCodexSessionMeta(t *testing.T) {
	for _, tc := range []struct {
		payload string
		want    CodexSessionMeta
	}{
		{`{"id":"t","source":"cli"}`, CodexSessionMeta{ID: "t"}},
		{`{"id":"t","forked_from_id":"f","source":"cli"}`, CodexSessionMeta{ID: "t", ForkedFromID: "f"}},
		// The source wins; top-level fields fill the gaps.
		{`{"id":"t","parent_thread_id":"x","agent_nickname":"Other","agent_role":"r2","source":{"subagent":{"thread_spawn":{"parent_thread_id":"p","agent_nickname":"Galileo"}}}}`,
			CodexSessionMeta{ID: "t", Spawn: CodexSpawn{"p", "Galileo", "r2"}}},
		{`{"id":"t","parent_thread_id":"p","agent_nickname":"Galileo","agent_type":"worker","forked_from_id":"p","source":{"subagent":{"thread_spawn":{}}}}`,
			CodexSessionMeta{ID: "t", ForkedFromID: "p", Spawn: CodexSpawn{"p", "Galileo", "worker"}}},
		// A subagent source of a shape this version does not know.
		{`{"id":"t","parent_thread_id":"p","agent_nickname":"Galileo","agent_role":"explorer","source":{"subagent":{"agent_spawn_v2":{}}}}`,
			CodexSessionMeta{ID: "t", Spawn: CodexSpawn{"p", "Galileo", "explorer"}}},
		// Not spawns: a CLI thread, a review, and other internal subagents
		// never take a parent from the top-level fields.
		{`{"id":"t","parent_thread_id":"p","source":"cli"}`, CodexSessionMeta{ID: "t"}},
		{`{"id":"t","parent_thread_id":"p","source":{"subagent":"review"}}`, CodexSessionMeta{ID: "t"}},
		{`{"id":"t","parent_thread_id":"p","source":{"subagent":{"other":"guardian"}}}`, CodexSessionMeta{ID: "t"}},
		{`not json`, CodexSessionMeta{}},
	} {
		if got := ParseCodexSessionMeta([]byte(tc.payload)); got != tc.want {
			t.Errorf("ParseCodexSessionMeta(%s) = %+v, want %+v", tc.payload, got, tc.want)
		}
	}
}

// TestReadCodexSessionMeta checks that only the rollout's first line, the
// thread's own session_meta, is read: a forked rollout's copied headers
// never count.
func TestReadCodexSessionMeta(t *testing.T) {
	copied := `{"type":"session_meta","payload":{"id":"src","forked_from_id":"older"}}` + "\n"
	for _, tc := range []struct {
		name, rollout, fork string
		ok                  bool
	}{
		{"own fork", `{"type":"session_meta","payload":{"id":"T","forked_from_id":"src"}}` + "\n" + copied, "src", true},
		{"not a fork", `{"type":"session_meta","payload":{"id":"t"}}` + "\n" + copied, "", true},
		{"no id", `{"type":"session_meta","payload":{"forked_from_id":"src"}}`, "src", true},
		{"other id first", copied, "", false},
		{"no session_meta first", `{"type":"turn_context","payload":{}}` + "\n" + copied, "", false},
		{"empty", ``, "", false},
	} {
		got, ok := ReadCodexSessionMeta(strings.NewReader(tc.rollout), "t")
		if ok != tc.ok || got.ForkedFromID != tc.fork {
			t.Errorf("%s: %+v %v, want fork %q ok %v", tc.name, got, ok, tc.fork, tc.ok)
		}
	}
}
