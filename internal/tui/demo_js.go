//go:build js

package tui

import (
	"time"

	"github.com/DanBradbury/firekeeper/internal/transcript"
	tea "github.com/charmbracelet/bubbletea"
)

// demoModel runs the real Firekeeper views against synthetic, in-memory data.
// Its commands never discover processes, read provider files, or start helpers.
type demoModel struct {
	model
	frame int
}

type demoTickMsg struct{}

func NewDemoModel() tea.Model {
	m := NewModel()
	m.processGroups = demoGroups(0)
	m.refreshedAt = time.Now()
	seedDemoUsage(&m, m.refreshedAt)
	return demoModel{model: m}
}

func demoTick() tea.Cmd {
	return tea.Tick(processPollInterval, func(time.Time) tea.Msg { return demoTickMsg{} })
}

func (d demoModel) Init() tea.Cmd { return demoTick() }

func (d demoModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.MouseMsg); ok {
		return d, nil
	}
	if _, ok := msg.(demoTickMsg); ok {
		d.frame++
		updated, _ := d.model.Update(processResultMsg{groups: demoGroups(d.frame), refreshed: time.Now()})
		d.model = updated.(model)
		return d, demoTick()
	}
	if key, ok := msg.(tea.KeyMsg); ok {
		switch {
		case d.model.helpOpen:
			// Help consumes its own next key.
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
			case "s", "R":
				d.model.terminalStatus = "terminal switching is unavailable in demo"
				return d, nil
			case "r":
				if d.model.activeTab == processesTab {
					d.frame++
					updated, _ := d.model.Update(processResultMsg{groups: demoGroups(d.frame), refreshed: time.Now()})
					d.model = updated.(model)
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
					if d.model.chat.open {
						d.model.chat.loading = false
						d.model.chat.messages = demoChatMessages()
					}
					return d, nil
				}
			case "c":
				if d.model.activeTab == processesTab {
					updated, _ := d.model.Update(msg)
					d.model = updated.(model)
					if d.model.chat.open {
						d.model.chat.loading = false
						d.model.chat.messages = demoChatMessages()
					}
					return d, nil
				}
			}
		}
	}
	updated, cmd := d.model.Update(msg)
	d.model = updated.(model)
	return d, cmd
}

func (d demoModel) View() string { return d.model.View() }

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
