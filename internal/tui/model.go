// Package tui is the Firekeeper terminal dashboard: a Bubble Tea program with
// Processes and Usage tabs that shows coding-agent runtimes found by
// internal/session and lets the user switch to their terminals.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	chromeRows          = 3
	processPollInterval = 2 * time.Second
	// sessionMetadataGraceRefreshes is how many consecutive refreshes may
	// miss a runtime's session metadata before the last known copy is
	// dropped. It rides out a transient lsof miss, while a session closed
	// inside a long-lived runtime such as the Codex app-server daemon still
	// disappears a few seconds later.
	sessionMetadataGraceRefreshes = 2
)

type tab int

const (
	processesTab tab = iota
	usageTab
	tabCount
)

type usageProvider int

const (
	codexProvider usageProvider = iota
	copilotProvider
	kimiProvider
	claudeProvider
	usageProviderCount
)

type processPollMsg time.Time

type processInfo struct {
	tool    string
	pid     int
	ppid    int
	tty     string
	elapsed string
	command string
}

type sessionInfo struct {
	id                string
	name              string
	state             sessionState
	cwd               string
	model             string
	source            string
	repository        string
	gitBranch         string
	rolloutPath       string
	updatedAt         time.Time
	tokensUsed        int64
	tokens            tokenBreakdown
	undercountWarning string
}

type tokenBreakdown = session.TokenBreakdown

type processGroup struct {
	tool      string
	root      processInfo
	processes []processInfo
	sessions  []sessionInfo
}

type processResultMsg struct {
	groups          []processGroup
	refreshed       time.Time
	metadataWarning string
	err             error
}

type terminalSwitchResultMsg struct {
	tty         string
	app         string
	multiplexer string
	warning     string
	err         error
}

type model struct {
	width, height           int
	activeTab               tab
	processGroups           []processGroup
	processCursor           int
	processScroll           int
	expandedGroups          map[int]bool
	processErr              string
	processMetadataWarning  string
	terminalStatus          string
	refreshedAt             time.Time
	codexUsage              codexUsageSnapshot
	codexUsageErr           string
	codexUsageLoading       bool
	codexUsageRefreshedAt   time.Time
	codexHistoryErr         string
	codexHistoryCursor      int
	usageProvider           usageProvider
	copilotUsage            copilotUsageSnapshot
	copilotUsageErr         string
	copilotUsageLoading     bool
	copilotUsageRefreshedAt time.Time
	copilotHistoryErr       string
	copilotHistoryCursor    int
	kimiUsage               kimiUsageSnapshot
	kimiUsageErr            string
	kimiUsageLoading        bool
	kimiUsageRefreshedAt    time.Time
	kimiHistoryCursor       int
	claudeUsage             claudeUsageSnapshot
	claudeUsageErr          string
	claudeUsageLoading      bool
	claudeUsageRefreshedAt  time.Time
	claudeHistoryCursor     int
	helpOpen                bool
	metadataMisses          map[int]int
	chat                    chatState
}

// NewModel returns the dashboard's initial Bubble Tea model.
func NewModel() model {
	return model{width: 80, height: 24, activeTab: processesTab, expandedGroups: make(map[int]bool)}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(pollProcesses(), refreshProcesses())
}

func pollProcesses() tea.Cmd {
	return tea.Tick(processPollInterval, func(t time.Time) tea.Msg { return processPollMsg(t) })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.helpOpen {
			return m.updateHelp(msg)
		}
		if m.chat.open {
			return m.updateChat(msg)
		}
		if m.activeTab == processesTab && msg.String() != "s" {
			m.terminalStatus = ""
		}
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "?":
			m.helpOpen = true
		case "c":
			if m.activeTab == processesTab {
				return m, m.openChat()
			}
		case "tab", "shift+tab":
			m.activeTab = (m.activeTab + 1) % tabCount
			if m.activeTab == processesTab {
				return m, refreshProcesses()
			}
			if m.activeTab == usageTab {
				return m, m.beginUsageRefresh()
			}
		case "left", "h":
			if m.activeTab == usageTab {
				return m, m.cycleUsageProvider(-1)
			}
		case "right", "l":
			if m.activeTab == usageTab {
				return m, m.cycleUsageProvider(1)
			}
		case "up", "k":
			if m.activeTab == processesTab {
				m.processCursor--
				m.clampProcessSelection()
			} else if m.activeTab == usageTab {
				m.moveUsageCursor(-1)
			}
		case "down", "j":
			if m.activeTab == processesTab {
				m.processCursor++
				m.clampProcessSelection()
			} else if m.activeTab == usageTab {
				m.moveUsageCursor(1)
			}
		case "pgup":
			if m.activeTab == processesTab {
				m.processCursor -= max(m.processPageSize()/2, 1)
				m.clampProcessSelection()
			} else if m.activeTab == usageTab {
				m.moveUsageCursor(-usageSessionPage)
			}
		case "pgdown":
			if m.activeTab == processesTab {
				m.processCursor += max(m.processPageSize()/2, 1)
				m.clampProcessSelection()
			} else if m.activeTab == usageTab {
				m.moveUsageCursor(usageSessionPage)
			}
		case "home":
			if m.activeTab == processesTab {
				m.processCursor = 0
				m.clampProcessSelection()
			} else if m.activeTab == usageTab {
				*m.usageCursorRef() = 0
			}
		case "end":
			if m.activeTab == processesTab {
				m.processCursor = len(m.processGroups) - 1
				m.clampProcessSelection()
			} else if m.activeTab == usageTab {
				m.setUsageCursorEnd()
			}
		case "enter":
			if m.activeTab == processesTab && len(m.processGroups) > 0 {
				rootPID := m.processGroups[m.processCursor].root.pid
				m.expandedGroups[rootPID] = !m.expandedGroups[rootPID]
				m.ensureSelectedProcessVisible()
			} else if m.activeTab == usageTab {
				return m, m.openUsageSessionChat()
			}
		case "r":
			if m.activeTab == processesTab {
				return m, refreshProcesses()
			}
			if m.activeTab == usageTab {
				return m, m.beginUsageRefresh()
			}
		case "s":
			if m.activeTab == processesTab && len(m.processGroups) > 0 {
				group := m.processGroups[m.processCursor]
				target := newTerminalSwitchTarget(group, 0)
				m.terminalStatus = "switching to " + displayTTY(target.tty) + "…"
				return m, switchToTerminal(target)
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ensureSelectedProcessVisible()
		if m.chat.open {
			m.clampChatScroll()
		}

	case processPollMsg:
		return m, tea.Batch(refreshProcesses(), pollProcesses(), m.refreshChat())

	case chatResultMsg:
		m.applyChatResult(msg)

	case processResultMsg:
		m.refreshedAt = msg.refreshed
		if msg.err != nil {
			m.processErr = msg.err.Error()
		} else {
			selectedPID := m.selectedProcessRootPID()
			var groups []processGroup
			groups, m.metadataMisses = retainKnownSessionMetadata(m.processGroups, msg.groups, m.metadataMisses)
			m.processGroups = filterUnidentifiedCodexGroups(groups)
			m.restoreProcessSelection(selectedPID)
			m.processErr = ""
			m.processMetadataWarning = msg.metadataWarning
			m.ensureSelectedProcessVisible()
		}

	case terminalSwitchResultMsg:
		if msg.err != nil {
			m.terminalStatus = "terminal switch failed: " + sanitizeProcessCommand(msg.err.Error())
		} else {
			m.terminalStatus = "switched to " + msg.app + " " + displayTTY(msg.tty)
			if msg.multiplexer != "" {
				m.terminalStatus += " • " + msg.multiplexer + " pane"
			}
			if msg.warning != "" {
				m.terminalStatus += " • " + sanitizeProcessCommand(msg.warning)
			}
		}

	case codexUsageResultMsg:
		m.codexUsageLoading = false
		m.codexUsageRefreshedAt = msg.refreshed
		if msg.err != nil {
			m.codexUsageErr = sanitizeProcessCommand(msg.err.Error())
		} else {
			history := m.codexUsage.History
			m.codexUsage = msg.snapshot
			m.codexUsage.History = history
			m.codexUsageErr = ""
		}
		if msg.historyErr != nil {
			m.codexHistoryErr = sanitizeProcessCommand(msg.historyErr.Error())
		} else {
			m.codexUsage.History = msg.snapshot.History
			m.codexHistoryErr = ""
			m.clampUsageCursors()
		}

	case copilotUsageResultMsg:
		m.copilotUsageLoading = false
		m.copilotUsageRefreshedAt = msg.refreshed
		if msg.err != nil {
			m.copilotUsageErr = sanitizeProcessCommand(msg.err.Error())
		} else {
			history := m.copilotUsage.History
			m.copilotUsage = msg.snapshot
			m.copilotUsage.History = history
			m.copilotUsageErr = ""
		}
		if msg.historyErr != nil {
			m.copilotHistoryErr = sanitizeProcessCommand(msg.historyErr.Error())
		} else {
			m.copilotUsage.History = msg.snapshot.History
			m.copilotHistoryErr = ""
			m.clampUsageCursors()
		}

	case kimiUsageResultMsg:
		m.kimiUsageLoading = false
		m.kimiUsageRefreshedAt = msg.refreshed
		if msg.err != nil {
			m.kimiUsageErr = sanitizeProcessCommand(msg.err.Error())
		} else {
			m.kimiUsage = msg.snapshot
			m.kimiUsageErr = ""
		}

	case claudeUsageResultMsg:
		m.claudeUsageLoading = false
		m.claudeUsageRefreshedAt = msg.refreshed
		if msg.err != nil {
			m.claudeUsageErr = sanitizeProcessCommand(msg.err.Error())
		} else {
			m.claudeUsage = msg.snapshot
			m.claudeUsageErr = ""
			m.clampUsageCursors()
		}

	}

	return m, nil
}

func (m *model) cycleUsageProvider(delta int) tea.Cmd {
	m.usageProvider = (m.usageProvider + usageProvider(delta) + usageProviderCount) % usageProviderCount
	if m.usageProvider == codexProvider && !m.codexUsageRefreshedAt.IsZero() && m.codexUsageErr == "" {
		return nil
	}
	if m.usageProvider == copilotProvider && !m.copilotUsageRefreshedAt.IsZero() && m.copilotUsageErr == "" {
		return nil
	}
	if m.usageProvider == kimiProvider && !m.kimiUsageRefreshedAt.IsZero() && m.kimiUsageErr == "" {
		return nil
	}
	if m.usageProvider == claudeProvider && !m.claudeUsageRefreshedAt.IsZero() && m.claudeUsageErr == "" {
		return nil
	}
	return m.beginUsageRefresh()
}

func (m *model) beginUsageRefresh() tea.Cmd {
	if m.usageProvider == copilotProvider {
		m.copilotUsageLoading = true
		return refreshCopilotUsage()
	}
	if m.usageProvider == kimiProvider {
		m.kimiUsageLoading = true
		return refreshKimiUsage()
	}
	if m.usageProvider == claudeProvider {
		m.claudeUsageLoading = true
		return refreshClaudeUsage()
	}
	m.codexUsageLoading = true
	return refreshCodexUsage()
}

func (m model) View() string {
	if m.width < 20 || m.height < 6 {
		return "Terminal too small (need 20x6)\n"
	}
	contentRows := m.height - chromeRows
	if m.helpOpen {
		help, status := m.helpFooter()
		return strings.Join([]string{m.dashHeaderLine(), m.viewHelp(contentRows), help, status}, "\n")
	}
	if m.chat.open {
		help, status := m.chatFooter()
		return strings.Join([]string{m.dashHeaderLine(), m.viewChat(contentRows), help, status}, "\n")
	}
	if m.activeTab == processesTab {
		help, status := m.processFooter()
		return strings.Join([]string{m.dashHeaderLine(), m.viewProcesses(contentRows), help, status}, "\n")
	}
	help, status := m.usageFooter()
	return strings.Join([]string{m.dashHeaderLine(), m.viewUsage(contentRows), help, status}, "\n")
}

func padText(value string, width int) string {
	runes := []rune(value)
	if len(runes) > width {
		return string(runes[:width])
	}
	return value + strings.Repeat(" ", width-len(runes))
}

func (m *model) clampProcessSelection() {
	if len(m.processGroups) == 0 {
		m.processCursor = 0
		m.processScroll = 0
		return
	}
	m.processCursor = min(max(m.processCursor, 0), len(m.processGroups)-1)
	m.ensureSelectedProcessVisible()
}

func (m *model) ensureSelectedProcessVisible() {
	if len(m.processGroups) == 0 || m.processPageSize() < 1 {
		m.processScroll = 0
		return
	}
	body, offsets := m.processBodyLines()
	start := offsets[m.processCursor]
	end := len(body)
	if m.processCursor+1 < len(offsets) {
		end = offsets[m.processCursor+1]
	}
	pageSize := m.processPageSize()
	if end-start > pageSize {
		m.processScroll = start
	} else {
		if start < m.processScroll {
			m.processScroll = start
		}
		if end > m.processScroll+pageSize {
			m.processScroll = end - pageSize
		}
	}
	m.processScroll = min(max(m.processScroll, 0), max(len(body)-pageSize, 0))
}

func (m model) selectedProcessRootPID() int {
	if len(m.processGroups) == 0 || m.processCursor >= len(m.processGroups) {
		return 0
	}
	return m.processGroups[m.processCursor].root.pid
}

func (m *model) restoreProcessSelection(rootPID int) {
	if rootPID != 0 {
		for index, group := range m.processGroups {
			if group.root.pid == rootPID {
				m.processCursor = index
				return
			}
		}
	}
	m.clampProcessSelection()
}

// retainKnownSessionMetadata copies a runtime's last known sessions forward
// when a refresh finds none, for at most sessionMetadataGraceRefreshes
// consecutive refreshes. misses counts those refreshes per root PID; the
// updated counts are returned.
func retainKnownSessionMetadata(previous, refreshed []processGroup, misses map[int]int) ([]processGroup, map[int]int) {
	nextMisses := make(map[int]int)
	known := make(map[int]processGroup, len(previous))
	for _, group := range previous {
		if hasSessionMetadataAdapter(group.tool) && len(group.sessions) > 0 {
			known[group.root.pid] = group
		}
	}
	for index := range refreshed {
		group := &refreshed[index]
		if !hasSessionMetadataAdapter(group.tool) || len(group.sessions) > 0 {
			continue
		}
		old, ok := known[group.root.pid]
		if !ok || old.root.tty != group.root.tty || old.root.command != group.root.command {
			continue
		}
		count := misses[group.root.pid] + 1
		if count > sessionMetadataGraceRefreshes {
			continue
		}
		nextMisses[group.root.pid] = count
		group.sessions = append([]sessionInfo(nil), old.sessions...)
	}
	return refreshed, nextMisses
}

// hasSessionMetadataAdapter reports whether discovery enriches tool's runtimes
// with session metadata worth retaining across a transient miss.
func hasSessionMetadataAdapter(tool string) bool {
	switch tool {
	case "Codex", "Copilot", "Kimi", "Claude", "OpenCode", "Gemini", "Cursor":
		return true
	}
	return false
}

func filterUnidentifiedCodexGroups(groups []processGroup) []processGroup {
	visible := make([]processGroup, 0, len(groups))
	for _, group := range groups {
		if group.tool == "Codex" && len(group.sessions) == 0 {
			continue
		}
		visible = append(visible, group)
	}
	return visible
}

func refreshProcesses() tea.Cmd {
	return func() tea.Msg {
		result := processResultMsg{refreshed: time.Now()}
		metas, err := session.Discover(context.Background(), session.Options{})
		var warning *session.Warning
		if errors.As(err, &warning) {
			result.metadataWarning = warning.Error()
		} else {
			result.err = err
		}
		seen := make(map[int]bool)
		for _, meta := range metas {
			if !seen[meta.Runtime.Root.PID] {
				seen[meta.Runtime.Root.PID] = true
				result.groups = append(result.groups, fromProcessGroup(meta.Runtime))
			}
		}
		return result
	}
}

type terminalAdapter struct {
	name    string
	script  string
	usesCWD bool
}

var ghosttyTerminalIDs sync.Map

func groupWorkingDirectory(group processGroup) string {
	for _, session := range group.sessions {
		if session.cwd != "" {
			return session.cwd
		}
	}
	return ""
}

var macTerminalAdapters = []terminalAdapter{
	{
		name: "iTerm2",
		script: `on run argv
	set targetTTY to item 1 of argv
	if application id "com.googlecode.iterm2" is not running then return "not-running"
	tell application id "com.googlecode.iterm2"
		repeat with terminalWindow in windows
			repeat with terminalTab in tabs of terminalWindow
				repeat with terminalSession in sessions of terminalTab
					if tty of terminalSession is targetTTY then
						select terminalSession
						select terminalTab
						select terminalWindow
						activate
						return "matched"
					end if
				end repeat
			end repeat
		end repeat
	end tell
	return "not-found"
end run`,
	},
	{
		name: "Terminal.app",
		script: `on run argv
	set targetTTY to item 1 of argv
	if application id "com.apple.Terminal" is not running then return "not-running"
	tell application id "com.apple.Terminal"
		repeat with terminalWindow in windows
			repeat with terminalTab in tabs of terminalWindow
				if tty of terminalTab is targetTTY then
					set selected tab of terminalWindow to terminalTab
					set frontmost of terminalWindow to true
					activate
					return "matched"
				end if
			end repeat
		end repeat
	end tell
	return "not-found"
end run`,
	},
	{
		name: "Ghostty",
		script: `on run argv
	set targetTTY to item 1 of argv
	set targetCWD to item 2 of argv
	set cachedTerminalID to item 3 of argv
	if application id "com.mitchellh.ghostty" is not running then return "not-running"
	set ttySupported to true
	set cwdMatchCount to 0
	set cwdMatchWindow to missing value
	set cwdMatchTab to missing value
	set cwdMatchPane to missing value
	tell application id "com.mitchellh.ghostty"
		repeat with terminalWindow in windows
			repeat with terminalTab in tabs of terminalWindow
				repeat with terminalPane in terminals of terminalTab
					if cachedTerminalID is not "" and id of terminalPane is cachedTerminalID then
						select tab terminalTab
						activate window terminalWindow
						focus terminalPane
						return "matched:" & cachedTerminalID
					end if
					try
						set paneTTY to «class Gtty» of terminalPane
						if paneTTY is not missing value then
							set paneTTYText to paneTTY as text
							if paneTTYText is targetTTY or ("/dev/" & paneTTYText) is targetTTY then
								select tab terminalTab
								activate window terminalWindow
								focus terminalPane
								return "matched:" & id of terminalPane
							end if
						end if
					on error errorMessage number errorNumber
						if errorNumber is -1728 then
							set ttySupported to false
						else
							error errorMessage number errorNumber
						end if
					end try
					if working directory of terminalPane is targetCWD then
						set cwdMatchCount to cwdMatchCount + 1
						if cwdMatchCount is 1 then
							set cwdMatchWindow to terminalWindow
							set cwdMatchTab to terminalTab
							set cwdMatchPane to terminalPane
						end if
					end if
				end repeat
			end repeat
		end repeat
		if ttySupported then return "not-found"
		if cwdMatchCount > 0 then
			select tab cwdMatchTab
			activate window cwdMatchWindow
			focus cwdMatchPane
			return "matched:" & id of cwdMatchPane
		end if
	end tell
	return "not-found"
end run`,
		usesCWD: true,
	},
}

func switchToTerminal(target terminalSwitchTarget) tea.Cmd {
	return func() tea.Msg {
		return switchTerminalTargetWith(target, focusHerdrTarget, focusTerminalSession)
	}
}

func switchTerminalTargetWith(
	target terminalSwitchTarget,
	focusHerdr func(context.Context, terminalSwitchTarget) (herdrFocusTarget, bool, error),
	focusTerminal func(string, string) (string, error),
) terminalSwitchResultMsg {
	focusTTY := target.tty
	multiplexer := ""
	warning := ""
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resolved, found, herdrErr := focusHerdr(ctx, target)
	if found && herdrErr == nil {
		multiplexer = "Herdr"
		if resolved.clientTTY != "" {
			focusTTY = resolved.clientTTY
		}
	} else if found {
		warning = "Herdr pane focus failed: " + herdrErr.Error()
	}
	app, err := focusTerminal(focusTTY, target.cwd)
	return terminalSwitchResultMsg{
		tty:         focusTTY,
		app:         app,
		multiplexer: multiplexer,
		warning:     warning,
		err:         err,
	}
}

func focusTerminalSession(tty, cwd string) (string, error) {
	return focusTerminalSessionWith(
		tty,
		cwd,
		runtime.GOOS,
		func(script string, arguments ...string) (string, error) {
			commandArguments := append([]string{"-e", script}, arguments...)
			output, err := exec.Command("osascript", commandArguments...).CombinedOutput()
			result := strings.TrimSpace(string(output))
			if err != nil {
				if result != "" {
					return "", fmt.Errorf("%s: %w", sanitizeProcessCommand(result), err)
				}
				return "", err
			}
			return result, nil
		},
	)
}

func focusTerminalSessionWith(
	tty string,
	cwd string,
	goos string,
	runScript func(string, ...string) (string, error),
) (string, error) {
	if goos != "darwin" {
		return "", fmt.Errorf("terminal switching requires macOS")
	}
	targetTTY := deviceTTY(tty)
	if targetTTY == "" {
		return "", fmt.Errorf("selected runtime has no controlling TTY")
	}

	var failures []string
	runningAdapters := 0
	for _, adapter := range macTerminalAdapters {
		targets := []string{targetTTY}
		if adapter.usesCWD {
			if cwd == "" {
				continue
			}
			cachedTerminalID := ""
			if cached, ok := ghosttyTerminalIDs.Load(targetTTY); ok {
				cachedTerminalID, _ = cached.(string)
			}
			targets = append(targets, cwd, cachedTerminalID)
		}
		result, err := runScript(adapter.script, targets...)
		if err != nil {
			failures = append(failures, adapter.name+": "+err.Error())
			continue
		}
		switch {
		case result == "matched" || strings.HasPrefix(result, "matched:"):
			if adapter.name == "Ghostty" {
				terminalID := strings.TrimPrefix(result, "matched:")
				if terminalID != result && terminalID != "" {
					ghosttyTerminalIDs.Store(targetTTY, terminalID)
				}
			}
			return adapter.name, nil
		case result == "not-running":
			continue
		default:
			runningAdapters++
		}
	}
	if len(failures) > 0 {
		return "", fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	if runningAdapters == 0 {
		return "", fmt.Errorf("no supported terminal app is running")
	}
	return "", fmt.Errorf("no supported terminal session owns %s", targetTTY)
}

func formatSessionTime(value time.Time) string {
	if value.IsZero() || value.UnixMilli() == 0 {
		return "unknown"
	}
	return value.Format("2006-01-02 15:04:05")
}

func displayTTY(value string) string {
	if value = normalizeTTY(value); value != "" {
		return value
	}
	return "—"
}

func deviceTTY(value string) string {
	if value = normalizeTTY(value); value != "" {
		return "/dev/" + value
	}
	return ""
}

func fitLine(s string, width int) string {
	runes := []rune(s)
	if len(runes) > width {
		return string(runes[:width])
	}
	return s + strings.Repeat(" ", width-len(runes))
}
