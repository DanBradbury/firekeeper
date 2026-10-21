package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

const testCopilotSessionID = "dc315f47-bf71-4447-b252-5ba9cbad8b51"

func TestCopilotProcessDetailsShowProviderMetadata(t *testing.T) {
	m := testModel()
	m.processGroups = []processGroup{{
		tool: "Copilot",
		root: processInfo{pid: 300, tty: "ttys003", elapsed: "00:42"},
		processes: []processInfo{{
			pid: 300, ppid: 1, tty: "ttys003", elapsed: "00:42", command: "copilot",
		}},
		sessions: []sessionInfo{{
			id:         testCopilotSessionID,
			name:       "Copilot metadata support",
			state:      sessionStateNeedsInput,
			cwd:        "/workspace/firekeeper",
			model:      "claude-sonnet-4.6",
			repository: "DanBradbury/firekeeper",
			gitBranch:  "feature/copilot",
			tokensUsed: 321,
		}},
	}}
	m.expandedGroups[300] = true
	lines, _ := m.processBodyLines()
	view := ansi.Strip(strings.Join(lines, "\n"))
	for _, expected := range []string{
		"NEEDS INPUT", "Copilot metadata support", "/workspace/firekeeper",
		"claude-sonnet-4.6", "DanBradbury/firekeeper", "feature/copilot", "tokens 321",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("Copilot details missing %q:\n%s", expected, view)
		}
	}
}
