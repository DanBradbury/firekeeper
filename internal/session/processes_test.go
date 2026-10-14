package session

import (
	"strings"
	"testing"
)

func TestParseProcessesFiltersClassifiesAndSanitizes(t *testing.T) {
	output := strings.Join([]string{
		"101 1 ttys001 00:03 /Applications/Codex.app/Contents/MacOS/Codex --session one",
		"202 1 ttys002 01:10 node /extensions/github.copilot/chat.js",
		"303 1 ?? 02:20 /usr/local/bin/opencode serve\x1b[31m",
		"404 1 ttys004 03:30 /bin/zsh",
	}, "\n")

	processes := (&discoverer{}).parseProcesses(output)
	if len(processes) != 3 {
		t.Fatalf("process count = %d, want 3", len(processes))
	}
	if processes[0].Tool != "Codex" || processes[1].Tool != "Copilot" || processes[2].Tool != "OpenCode" {
		t.Fatalf("tool order = %q, %q, %q", processes[0].Tool, processes[1].Tool, processes[2].Tool)
	}
	if strings.ContainsRune(processes[2].Command, '\x1b') {
		t.Fatal("process command retained terminal escape character")
	}
	if processes[0].TTY != "ttys001" || processes[1].TTY != "ttys002" || processes[2].TTY != "" {
		t.Fatalf("TTY values = %q, %q, %q", processes[0].TTY, processes[1].TTY, processes[2].TTY)
	}
}

func TestGroupProcessesUsesTopmostMatchingParent(t *testing.T) {
	processes := []ProcessInfo{
		{Tool: "Codex", PID: 100, PPID: 1},
		{Tool: "Codex", PID: 101, PPID: 100},
		{Tool: "Codex", PID: 102, PPID: 101},
		{Tool: "Codex", PID: 200, PPID: 1},
		{Tool: "Codex", PID: 201, PPID: 200},
		{Tool: "Copilot", PID: 300, PPID: 1},
	}
	groups := (&discoverer{}).groupProcesses(processes)

	if len(groups) != 3 {
		t.Fatalf("group count = %d, want 3", len(groups))
	}
	if groups[0].Root.PID != 100 || len(groups[0].Processes) != 3 {
		t.Fatalf("first group root/count = %d/%d, want 100/3", groups[0].Root.PID, len(groups[0].Processes))
	}
	if groups[1].Root.PID != 200 || len(groups[1].Processes) != 2 {
		t.Fatalf("second group root/count = %d/%d, want 200/2", groups[1].Root.PID, len(groups[1].Processes))
	}
	if groups[2].Root.PID != 300 || len(groups[2].Processes) != 1 {
		t.Fatalf("third group root/count = %d/%d, want 300/1", groups[2].Root.PID, len(groups[2].Processes))
	}
}

func TestThreadIDFromRolloutPath(t *testing.T) {
	id, ok := ThreadIDFromRolloutPath("/tmp/rollout-2026-07-30T06-35-32-019fb33c-c5f4-75f3-b987-228eb484c6ec.jsonl")
	if !ok || id != "019fb33c-c5f4-75f3-b987-228eb484c6ec" {
		t.Fatalf("thread id = %q, ok=%v", id, ok)
	}
	if _, ok := ThreadIDFromRolloutPath("/tmp/not-a-rollout.jsonl"); ok {
		t.Fatal("invalid rollout produced thread id")
	}
}
