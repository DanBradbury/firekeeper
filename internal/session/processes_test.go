package session

import (
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/pkg/event"
)

func TestParseProcessesFiltersClassifiesAndSanitizes(t *testing.T) {
	output := strings.Join([]string{
		"101 1 ttys001 00:03 /usr/local/bin/codex resume",
		"202 1 ttys002 01:10 node /opt/homebrew/lib/node_modules/@github/copilot/index.js",
		"303 1 ?? 02:20 /usr/local/bin/opencode serve\x1b[31m",
		"404 1 ttys004 03:30 /bin/zsh",
		// Step 0 rows 2 and 4: an editor's Copilot extension and Codex.app's
		// window are no provider.
		"505 1 ttys005 00:03 /Applications/Codex.app/Contents/MacOS/Codex --session one",
		"606 1 ttys006 01:10 node /extensions/github.copilot/chat.js",
		"malformed",
		"x 1 ttys007 00:01 codex",
	}, "\n")

	processes := fixtureDiscoverer().parseProcesses(output)
	if len(processes) != 3 {
		t.Fatalf("process count = %d, want 3: %+v", len(processes), processes)
	}
	if processes[0].Provider != event.ProviderCodex || processes[1].Provider != event.ProviderCopilot || processes[2].Provider != event.ProviderOpenCode {
		t.Fatalf("provider order = %q, %q, %q", processes[0].Provider, processes[1].Provider, processes[2].Provider)
	}
	if processes[0].Role != RoleSession || processes[1].Role != RoleSession || processes[2].Role != RoleServer {
		t.Fatalf("roles = %s, %s, %s", processes[0].Role, processes[1].Role, processes[2].Role)
	}
	for _, p := range processes {
		if p.Tool != toolName(p.Provider) {
			t.Fatalf("Tool %q doesn't match provider %q", p.Tool, p.Provider)
		}
		if p.Invocation.Program == "" {
			t.Fatalf("process %d has no invocation", p.PID)
		}
	}
	if strings.ContainsRune(processes[2].Command, '\x1b') {
		t.Fatal("process command retained terminal escape character")
	}
	if processes[0].TTY != "ttys001" || processes[1].TTY != "ttys002" || processes[2].TTY != "" {
		t.Fatalf("TTY values = %q, %q, %q", processes[0].TTY, processes[1].TTY, processes[2].TTY)
	}
}

func proc(p event.Provider, role Role, pid, ppid int) ProcessInfo {
	return ProcessInfo{Provider: p, Role: role, Tool: toolName(p), PID: pid, PPID: ppid}
}

func TestGroupProcessesUsesTopmostMatchingParent(t *testing.T) {
	processes := []ProcessInfo{
		proc(event.ProviderCodex, RoleSession, 100, 1),
		proc(event.ProviderCodex, RoleSession, 101, 100),
		proc(event.ProviderCodex, RoleServer, 102, 101),
		proc(event.ProviderCodex, RoleSession, 200, 1),
		proc(event.ProviderCodex, RoleTool, 201, 200),
		proc(event.ProviderCopilot, RoleSession, 300, 1),
	}
	groups := fixtureDiscoverer().groupProcesses(processes)

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

// groupOf returns the root PID of the group containing pid.
func groupOf(groups []ProcessGroup, pid int) int {
	for _, g := range groups {
		for _, p := range g.Processes {
			if p.PID == pid {
				return g.Root.PID
			}
		}
	}
	return 0
}

// Invariant 7: the walk climbs from servers and tools into the session that
// started them, but never from a session into a server or tool.
func TestGroupProcessesRoles(t *testing.T) {
	tests := []struct {
		name      string
		processes []ProcessInfo
		want      map[int]int // pid -> root pid
	}{
		{
			name: "server under a session joins it",
			processes: []ProcessInfo{
				proc(event.ProviderCodex, RoleSession, 100, 1),
				proc(event.ProviderCodex, RoleServer, 101, 100),
			},
			want: map[int]int{100: 100, 101: 100},
		},
		{
			name: "session under a server gets its own group",
			processes: []ProcessInfo{
				proc(event.ProviderCodex, RoleServer, 100, 1),
				proc(event.ProviderCodex, RoleSession, 200, 100),
			},
			want: map[int]int{100: 100, 200: 200},
		},
		{
			name: "session under a tool gets its own group",
			processes: []ProcessInfo{
				proc(event.ProviderKimi, RoleTool, 100, 1),
				proc(event.ProviderKimi, RoleSession, 200, 100),
			},
			want: map[int]int{100: 100, 200: 200},
		},
		{
			// The case the stop condition must check root.Role for.
			name: "tool started by a session under a server joins the session",
			processes: []ProcessInfo{
				proc(event.ProviderCodex, RoleServer, 100, 1),
				proc(event.ProviderCodex, RoleSession, 200, 100),
				proc(event.ProviderCodex, RoleTool, 300, 200),
			},
			want: map[int]int{100: 100, 200: 200, 300: 200},
		},
		{
			name: "tool started by a server started by a session joins the session",
			processes: []ProcessInfo{
				proc(event.ProviderCodex, RoleSession, 100, 1),
				proc(event.ProviderCodex, RoleServer, 200, 100),
				proc(event.ProviderCodex, RoleTool, 300, 200),
			},
			want: map[int]int{100: 100, 200: 100, 300: 100},
		},
		{
			name: "session under a session joins it",
			processes: []ProcessInfo{
				proc(event.ProviderClaude, RoleSession, 100, 1),
				proc(event.ProviderClaude, RoleSession, 200, 100),
			},
			want: map[int]int{100: 100, 200: 100},
		},
		{
			name: "another provider breaks the chain",
			processes: []ProcessInfo{
				proc(event.ProviderClaude, RoleSession, 100, 1),
				proc(event.ProviderCodex, RoleServer, 200, 100),
			},
			want: map[int]int{100: 100, 200: 200},
		},
		{
			name: "a parent cycle ends the walk",
			processes: []ProcessInfo{
				proc(event.ProviderCodex, RoleServer, 100, 200),
				proc(event.ProviderCodex, RoleServer, 200, 100),
			},
			want: map[int]int{100: 200, 200: 100},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups := fixtureDiscoverer().groupProcesses(tt.processes)
			for pid, root := range tt.want {
				if got := groupOf(groups, pid); got != root {
					t.Fatalf("process %d in group %d, want %d", pid, got, root)
				}
			}
			for _, g := range groups {
				if g.Provider != g.Root.Provider || g.Role != g.Root.Role || g.Tool != g.Root.Tool {
					t.Fatalf("group %d has %q %s %q, root has %q %s %q", g.Root.PID, g.Provider, g.Role, g.Tool, g.Root.Provider, g.Root.Role, g.Root.Tool)
				}
			}
		})
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

// classifyLine classifies a ps command line the way parseProcesses'
// fallback does. It returns "" for no provider, the display name for a
// session, and "Name/role" for a server or tool.
func classifyLine(command string) string {
	provider, role, ok := standalone().classifyProcess(parseInvocation(fallbackArgs(command)))
	if !ok {
		return ""
	}
	if role == RoleSession {
		return toolName(provider)
	}
	return toolName(provider) + "/" + role.String()
}
