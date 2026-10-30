package session

import (
	"context"
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
