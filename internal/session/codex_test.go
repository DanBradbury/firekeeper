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
			if len(group.Sessions) != 0 {
				t.Errorf("app-server sessions = %+v, want none", group.Sessions)
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
	if m, ok := byID[sub]; !ok || m.ParentID != parent || m.AgentName != "Galileo" || m.AgentRole != "explorer" {
		t.Errorf("subagent meta = %+v (found %v)", m, ok)
	}
	if m, ok := byID[parent]; !ok || m.ParentID != "" || m.AgentName != "" || m.AgentRole != "" {
		t.Errorf("parent meta = %+v (found %v)", m, ok)
	}
}
