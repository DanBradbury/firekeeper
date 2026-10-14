package tui

import (
	"reflect"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
)

func TestFromProcessGroupCopiesEveryField(t *testing.T) {
	updated := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	in := session.ProcessGroup{
		Tool: "Codex",
		Root: session.ProcessInfo{Tool: "Codex", PID: 100, PPID: 1, TTY: "ttys001", Elapsed: "01:00", Command: "codex"},
		Processes: []session.ProcessInfo{
			{Tool: "Codex", PID: 100, PPID: 1, TTY: "ttys001", Elapsed: "01:00", Command: "codex"},
			{Tool: "Codex", PID: 101, PPID: 100, TTY: "ttys001", Elapsed: "00:30", Command: "codex exec"},
		},
		Sessions: []session.Info{{
			ID: "id-1", Name: "name", State: session.SessionStateNeedsInput, CWD: "/work",
			Model: "gpt", Source: "cli", Repository: "me/repo", GitBranch: "main",
			RolloutPath: "/rollout.jsonl", StartedAt: updated.Add(-time.Hour), UpdatedAt: updated,
			TokensUsed: 42, Tokens: session.TokenBreakdown{Input: 1, Output: 2, Cache: 3},
		}},
	}
	want := processGroup{
		tool: "Codex",
		root: processInfo{tool: "Codex", pid: 100, ppid: 1, tty: "ttys001", elapsed: "01:00", command: "codex"},
		processes: []processInfo{
			{tool: "Codex", pid: 100, ppid: 1, tty: "ttys001", elapsed: "01:00", command: "codex"},
			{tool: "Codex", pid: 101, ppid: 100, tty: "ttys001", elapsed: "00:30", command: "codex exec"},
		},
		// The dashboard has no use for StartedAt, so it is not copied.
		sessions: []sessionInfo{{
			id: "id-1", name: "name", state: sessionStateNeedsInput, cwd: "/work",
			model: "gpt", source: "cli", repository: "me/repo", gitBranch: "main",
			rolloutPath: "/rollout.jsonl", updatedAt: updated,
			tokensUsed: 42, tokens: tokenBreakdown{Input: 1, Output: 2, Cache: 3},
		}},
	}
	if got := fromProcessGroup(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("fromProcessGroup =\n%#v\nwant\n%#v", got, want)
	}
}

func TestFromProcessGroupKeepsNilSlicesNil(t *testing.T) {
	got := fromProcessGroup(session.ProcessGroup{Tool: "Kimi", Root: session.ProcessInfo{PID: 7}})
	if got.processes != nil || got.sessions != nil {
		t.Fatalf("nil slices became %#v and %#v", got.processes, got.sessions)
	}
	if got.tool != "Kimi" || got.root.pid != 7 {
		t.Fatalf("group = %#v", got)
	}
}
