package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/firekeeper-ai/firekeeper/internal/transcript"
)

// demoModel runs the real Firekeeper views against synthetic, in-memory data,
// for the browser demo on the landing page (cmd/demo-wasm). It never discovers
// processes, reads provider files, contacts a network or starts a helper:
// Update drops every command the wrapped model returns except the one that
// quits, so no I/O the dashboard normally starts can run. The native
// dashboard never builds it.
type demoModel struct {
	model
	frame  int
	paused bool
}

type demoTickMsg struct{}

// demoPauseMsg stops or resumes the sample sessions changing state, for
// visitors who need them to hold still.
type demoPauseMsg struct{ paused bool }

// NewDemoModel returns the dashboard seeded with invented sessions and usage.
func NewDemoModel() tea.Model {
	m := NewModel()
	m.processGroups = demoGroups(0)
	m.refreshedAt = time.Now()
	seedDemoUsage(&m, m.refreshedAt)
	return demoModel{model: m}
}

// NewDemoPauseMsg is the message that pauses or resumes a demo model.
func NewDemoPauseMsg(paused bool) tea.Msg { return demoPauseMsg{paused: paused} }

func demoTick() tea.Cmd {
	return tea.Tick(processPollInterval, func(time.Time) tea.Msg { return demoTickMsg{} })
}

func (d demoModel) Init() tea.Cmd { return demoTick() }

func (d demoModel) advance() demoModel {
	d.frame++
	updated, _ := d.model.Update(processResultMsg{groups: demoGroups(d.frame), refreshed: time.Now()})
	d.model = updated.(model)
	return d
}

func (d demoModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseMsg:
		return d, nil
	case demoPauseMsg:
		d.paused = msg.paused
		return d, nil
	case demoTickMsg:
		if !d.paused {
			d = d.advance()
		}
		return d, demoTick()
	case tea.KeyMsg:
		return d.updateKey(msg)
	}
	updated, _ := d.model.Update(msg)
	d.model = updated.(model)
	return d, nil
}

func (d demoModel) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case d.model.helpOpen:
		// Help consumes its own next key.
	case d.model.activeTab == usageTab && d.model.usageFilterEditing:
		// Typing a session filter: every key is text or a filter edit.
	case d.model.chat.open:
		switch key.String() {
		case "e", "o":
			d.model.terminalStatus = "export is unavailable in demo"
			return d, nil
		case "r":
			d.model.terminalStatus = "sample chat is already current"
			return d, nil
		}
	default:
		switch key.String() {
		case "s", "R", "o":
			d.model.terminalStatus = "that needs a real terminal and is unavailable in demo"
			return d, nil
		case "r":
			if d.model.activeTab == processesTab {
				d = d.advance()
			} else {
				d.model.terminalStatus = "sample usage is already current"
			}
			return d, nil
		case "tab", "shift+tab":
			if key.String() == "shift+tab" {
				d.model.activeTab = (d.model.activeTab + tabCount - 1) % tabCount
			} else {
				d.model.activeTab = (d.model.activeTab + 1) % tabCount
			}
			d.model.terminalStatus = ""
			return d, nil
		case "left", "h", "right", "l":
			if d.model.activeTab == usageTab {
				delta := 1
				if key.String() == "left" || key.String() == "h" {
					delta = -1
				}
				d.model.usageProvider = (d.model.usageProvider + usageProvider(delta) + usageProviderCount) % usageProviderCount
				return d, nil
			}
		case "enter":
			if d.model.activeTab == usageTab {
				_ = d.model.openUsageSessionChat()
				d.fillChat()
				return d, nil
			}
		case "c":
			if d.model.activeTab == processesTab {
				_ = d.model.openChat()
				d.fillChat()
				return d, nil
			}
		}
	}
	overlay := d.model.helpOpen || d.model.chat.open
	updated, cmd := d.model.Update(key)
	d.model = updated.(model)
	if key.String() == "ctrl+c" || (key.String() == "q" && !overlay) {
		// The only command the demo keeps: the one that ends the program.
		return d, cmd
	}
	return d, nil
}

// fillChat replaces whatever the chat view started loading with the sample
// conversation.
func (d *demoModel) fillChat() {
	if d.model.chat.open {
		d.model.chat.loading = false
		d.model.chat.messages = demoChatMessages()
	}
}

func demoGroups(frame int) []processGroup {
	states := []sessionState{sessionStateActive, sessionStateWaiting, sessionStateNeedsInput}
	entries := []struct {
		tool, project, branch, model, name string
	}{
		{"Claude", "atlas-studio", "launch", "claude-sonnet", "Shape the launch page"},
		{"Codex", "ember-api", "main", "gpt-5", "Triage flaky tests"},
		{"Copilot", "northwind-docs", "new-guide", "copilot", "Write onboarding guide"},
		{"Kimi", "blueprint-cli", "prototype", "kimi-k2", "Review command output"},
	}
	groups := make([]processGroup, 0, len(entries))
	for i, entry := range entries {
		pid := 1100 + i*100
		state := states[(i+frame/3)%len(states)]
		groups = append(groups, processGroup{
			tool:      entry.tool,
			root:      processInfo{pid: pid, ppid: 1, tty: "demo", elapsed: "00:42", command: entry.tool},
			processes: []processInfo{{pid: pid, ppid: 1, tty: "demo", elapsed: "00:42", command: entry.tool}},
			sessions: []sessionInfo{{
				id: "demo-session-" + entry.project, name: entry.name, state: state,
				cwd: "/demo/" + entry.project, repository: entry.project, gitBranch: entry.branch,
				model: entry.model, tokensUsed: int64(8000 + i*3600 + frame*37),
			}},
		})
	}
	return groups
}

func demoChatMessages() []chatMessage {
	return []chatMessage{
		{role: transcript.RoleUser, text: "Can you help plan the next release?"},
		{role: transcript.RoleAssistant, text: "I found three small tasks: update docs, check the build, and review the launch page."},
		{role: transcript.RoleUser, text: "Start with the launch page."},
		{role: transcript.RoleAssistant, text: "On it. The preview is ready for review."},
	}
}
