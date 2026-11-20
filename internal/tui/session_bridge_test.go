package tui

import (
	"reflect"
	"testing"
	"time"

	"github.com/firekeeper-ai/firekeeper/internal/session"
	"github.com/firekeeper-ai/firekeeper/pkg/event"
)

func TestFromProcessGroupCopiesEveryField(t *testing.T) {
	updated := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	in := session.ProcessGroup{
		Provider: event.ProviderCodex,
		Root:     session.ProcessInfo{Provider: event.ProviderCodex, PID: 100, PPID: 1, TTY: "ttys001", Elapsed: "01:00", Command: "codex"},
		Processes: []session.ProcessInfo{
			{Provider: event.ProviderCodex, PID: 100, PPID: 1, TTY: "ttys001", Elapsed: "01:00", Command: "codex"},
			{Provider: event.ProviderCodex, PID: 101, PPID: 100, TTY: "ttys001", Elapsed: "00:30", Command: "codex exec"},
		},
		Sessions: []session.Info{{
			ID: "id-1", Name: "name", State: session.SessionStateNeedsInput, CWD: "/work",
			Model: "gpt", Source: "cli", Repository: "me/repo", GitBranch: "main",
			RolloutPath: "/rollout.jsonl", StartedAt: updated.Add(-time.Hour), UpdatedAt: updated,
			TokensUsed: 42, Tokens: session.TokenBreakdown{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4},
			UndercountWarning: session.ClaudeUndercountWarning,
		}},
	}
	want := processGroup{
		provider: event.ProviderCodex,
		root:     processInfo{provider: event.ProviderCodex, pid: 100, ppid: 1, tty: "ttys001", elapsed: "01:00", command: "codex"},
		processes: []processInfo{
			{provider: event.ProviderCodex, pid: 100, ppid: 1, tty: "ttys001", elapsed: "01:00", command: "codex"},
			{provider: event.ProviderCodex, pid: 101, ppid: 100, tty: "ttys001", elapsed: "00:30", command: "codex exec"},
		},
		// The dashboard has no use for StartedAt, so it is not copied.
		sessions: []sessionInfo{{
			id: "id-1", name: "name", state: sessionStateNeedsInput, cwd: "/work",
			model: "gpt", source: "cli", repository: "me/repo", gitBranch: "main",
			rolloutPath: "/rollout.jsonl", updatedAt: updated,
			tokensUsed: 42, tokens: tokenBreakdown{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4},
			undercountWarning: session.ClaudeUndercountWarning,
		}},
	}
	if got := fromProcessGroup(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("fromProcessGroup =\n%#v\nwant\n%#v", got, want)
	}
}

func TestFromProcessGroupKeepsNilSlicesNil(t *testing.T) {
	got := fromProcessGroup(session.ProcessGroup{Provider: event.ProviderKimi, Root: session.ProcessInfo{PID: 7}})
	if got.processes != nil || got.sessions != nil {
		t.Fatalf("nil slices became %#v and %#v", got.processes, got.sessions)
	}
	if got.provider != event.ProviderKimi || got.root.pid != 7 {
		t.Fatalf("group = %#v", got)
	}
}
