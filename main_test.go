package main

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestProcessRefreshRetainsKnownMetadataAfterTransientMiss(t *testing.T) {
	m := testModel()
	m.processGroups = []processGroup{{
		tool: "Codex",
		root: processInfo{pid: 101, tty: "ttys001", command: "codex"},
		sessions: []sessionInfo{{
			id:   "019fb33c-c5f4-75f3-b987-228eb484c6ec",
			name: "Known session metadata",
		}},
	}}

	updated, _ := m.Update(processResultMsg{
		groups: []processGroup{{
			tool: "Codex",
			root: processInfo{pid: 101, tty: "ttys001", command: "codex"},
		}},
		metadataWarning: "temporary metadata lookup failure",
		refreshed:       time.Now(),
	})
	m = updated.(model)

	if len(m.processGroups[0].sessions) != 1 || m.processGroups[0].sessions[0].name != "Known session metadata" {
		t.Fatal("transient refresh discarded known session metadata")
	}

	updated, _ = m.Update(processResultMsg{
		groups: []processGroup{{
			tool: "Codex",
			root: processInfo{pid: 101, tty: "ttys001", command: "codex"},
			sessions: []sessionInfo{{
				id:   "019fb33c-c5f4-75f3-b987-228eb484c6ed",
				name: "Fresh session metadata",
			}},
		}},
		refreshed: time.Now(),
	})
	m = updated.(model)
	if len(m.processGroups[0].sessions) != 1 || m.processGroups[0].sessions[0].name != "Fresh session metadata" {
		t.Fatal("successful refresh did not replace retained session metadata")
	}
}

func TestProcessRefreshRetainsClaudeMetadata(t *testing.T) {
	m := testModel()
	m.processGroups = []processGroup{{
		tool:     "Claude",
		root:     processInfo{pid: 101, tty: "ttys001", command: "claude"},
		sessions: []sessionInfo{{id: "0199c3d4-e5f6-7a8b-9c0d-000000000001", name: "Known Claude session"}},
	}}
	updated, _ := m.Update(processResultMsg{
		groups:          []processGroup{{tool: "Claude", root: processInfo{pid: 101, tty: "ttys001", command: "claude"}}},
		metadataWarning: "temporary metadata lookup failure",
		refreshed:       time.Now(),
	})
	m = updated.(model)
	if len(m.processGroups[0].sessions) != 1 || m.processGroups[0].sessions[0].name != "Known Claude session" {
		t.Fatal("transient refresh discarded known Claude session metadata")
	}
}

func TestProcessRefreshDropsUnidentifiedCodexGroups(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(processResultMsg{
		groups: []processGroup{
			{
				tool: "Codex",
				root: processInfo{pid: 101, tty: "ttys001", command: "codex"},
			},
			{
				tool: "Codex",
				root: processInfo{pid: 202, tty: "ttys002", command: "codex"},
				sessions: []sessionInfo{{
					id:    "019fb33c-c5f4-75f3-b987-228eb484c6ec",
					name:  "Known Codex session",
					state: sessionStateUnknown,
				}},
			},
			{tool: "OpenCode", root: processInfo{pid: 303}},
		},
		refreshed: time.Now(),
	})
	m = updated.(model)

	if len(m.processGroups) != 2 {
		t.Fatalf("visible process groups = %d, want 2", len(m.processGroups))
	}
	if m.processGroups[0].root.pid != 202 || m.processGroups[0].sessions[0].name != "Known Codex session" {
		t.Fatalf("identified Codex group was not preserved: %#v", m.processGroups[0])
	}
	if m.processGroups[1].tool != "OpenCode" {
		t.Fatalf("process-only provider was dropped: %#v", m.processGroups[1])
	}
}

func TestParseProcessesFiltersClassifiesAndSanitizes(t *testing.T) {
	output := strings.Join([]string{
		"101 1 ttys001 00:03 /Applications/Codex.app/Contents/MacOS/Codex --session one",
		"202 1 ttys002 01:10 node /extensions/github.copilot/chat.js",
		"303 1 ?? 02:20 /usr/local/bin/opencode serve\x1b[31m",
		"404 1 ttys004 03:30 /bin/zsh",
	}, "\n")

	processes := parseProcesses(output)
	if len(processes) != 3 {
		t.Fatalf("process count = %d, want 3", len(processes))
	}
	if processes[0].tool != "Codex" || processes[1].tool != "Copilot" || processes[2].tool != "OpenCode" {
		t.Fatalf("tool order = %q, %q, %q", processes[0].tool, processes[1].tool, processes[2].tool)
	}
	if strings.ContainsRune(processes[2].command, '\x1b') {
		t.Fatal("process command retained terminal escape character")
	}
	if processes[0].tty != "ttys001" || processes[1].tty != "ttys002" || processes[2].tty != "" {
		t.Fatalf("TTY values = %q, %q, %q", processes[0].tty, processes[1].tty, processes[2].tty)
	}
}

func TestDiscoverProcessesPreservesLongCopilotCommand(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("requires BSD or Linux ps")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	t.Setenv("COLUMNS", "80")
	command := "node /opt/" + strings.Repeat("long-installation-path/", 12) + "@github/copilot/index.js --resume=" + testCopilotSessionID
	child := exec.Command("bash", "-c", `exec -a "$1" sleep 60`, "firekeeper-test", command)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()
	// Wait for bash to replace itself with the synthetic runtime.
	deadline := time.Now().Add(3 * time.Second)
	for {
		output, err := discoverProcesses()
		if err != nil {
			t.Fatal(err)
		}
		for _, process := range parseProcesses(string(output)) {
			if process.pid == child.Process.Pid && process.tool == "Copilot" && process.command == command+" 60" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("process scan did not preserve the long Copilot command and session ID")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFocusTerminalSessionSelectsMatchingMacAdapter(t *testing.T) {
	var gotTTY string
	app, err := focusTerminalSessionWith(
		"ttys009",
		"",
		"darwin",
		func(script string, arguments ...string) (string, error) {
			gotTTY = arguments[0]
			if !strings.Contains(script, "select terminalSession") {
				t.Fatal("iTerm adapter did not select session")
			}
			return "matched", nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if app != "iTerm2" || gotTTY != "/dev/ttys009" {
		t.Fatalf("switch result = %q, %q", app, gotTTY)
	}
}

func TestFocusTerminalSessionSelectsGhosttyByWorkingDirectory(t *testing.T) {
	var gotTargets []string
	app, err := focusTerminalSessionWith(
		"ttys009",
		"/workspace/firekeeper",
		"darwin",
		func(script string, arguments ...string) (string, error) {
			if !strings.Contains(script, `application id "com.mitchellh.ghostty"`) {
				return "not-running", nil
			}
			for _, required := range []string{"select tab terminalTab", "activate window terminalWindow", "focus terminalPane"} {
				if !strings.Contains(script, required) {
					t.Fatalf("Ghostty adapter missing %q", required)
				}
			}
			gotTargets = append([]string(nil), arguments...)
			return "matched", nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if app != "Ghostty" || len(gotTargets) != 3 || gotTargets[0] != "/dev/ttys009" || gotTargets[1] != "/workspace/firekeeper" || gotTargets[2] != "" {
		t.Fatalf("Ghostty switch result = %q, %q", app, gotTargets)
	}
}

func TestFocusTerminalSessionPrefersExactTTYMatchOverGhosttyWorkingDirectory(t *testing.T) {
	var attempted []string
	app, err := focusTerminalSessionWith(
		"ttys009",
		"/workspace/firekeeper",
		"darwin",
		func(script string, _ ...string) (string, error) {
			switch {
			case strings.Contains(script, `application id "com.googlecode.iterm2"`):
				attempted = append(attempted, "iTerm2")
				return "matched", nil
			case strings.Contains(script, `application id "com.mitchellh.ghostty"`):
				attempted = append(attempted, "Ghostty")
				return "matched", nil
			default:
				return "not-running", nil
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if app != "iTerm2" || len(attempted) != 1 || attempted[0] != "iTerm2" {
		t.Fatalf("switch result = %q after attempts %v", app, attempted)
	}
}

func TestGhosttyAdapterMatchesTTYAndReusesStableTerminalID(t *testing.T) {
	var script string
	for _, adapter := range macTerminalAdapters {
		if adapter.name == "Ghostty" {
			script = adapter.script
			break
		}
	}
	if script == "" {
		t.Fatal("Ghostty adapter is missing")
	}
	for _, required := range []string{
		`set targetTTY to item 1 of argv`,
		`set targetCWD to item 2 of argv`,
		`set cachedTerminalID to item 3 of argv`,
		`«class Gtty» of terminalPane`,
		`cwdMatchCount`,
		`id of terminalPane is cachedTerminalID`,
		`return "matched:"`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("Ghostty adapter missing %q", required)
		}
	}
}

func TestMacTerminalAdapterScriptsCompile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("AppleScript is only available on macOS")
	}
	for _, adapter := range macTerminalAdapters {
		t.Run(adapter.name, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "adapter.scpt")
			if output, err := exec.Command("osacompile", "-o", outputPath, "-e", adapter.script).CombinedOutput(); err != nil {
				t.Fatalf("compile AppleScript: %s: %v", strings.TrimSpace(string(output)), err)
			}
		})
	}
}

func TestFocusTerminalSessionCachesGhosttyTerminalIDForRepeatedSwitches(t *testing.T) {
	const targetTTY = "/dev/ttys099"
	ghosttyTerminalIDs.Delete(targetTTY)
	t.Cleanup(func() { ghosttyTerminalIDs.Delete(targetTTY) })

	ghosttyCalls := 0
	runScript := func(script string, arguments ...string) (string, error) {
		if !strings.Contains(script, `application id "com.mitchellh.ghostty"`) {
			return "not-running", nil
		}
		ghosttyCalls++
		if len(arguments) != 3 {
			t.Fatalf("Ghostty arguments = %q", arguments)
		}
		if ghosttyCalls == 1 && arguments[2] != "" {
			t.Fatalf("initial cached terminal ID = %q", arguments[2])
		}
		if ghosttyCalls == 2 && arguments[2] != "terminal-123" {
			t.Fatalf("reused terminal ID = %q", arguments[2])
		}
		return "matched:terminal-123", nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		app, err := focusTerminalSessionWith("ttys099", "/workspace/firekeeper", "darwin", runScript)
		if err != nil {
			t.Fatal(err)
		}
		if app != "Ghostty" {
			t.Fatalf("switch %d app = %q", attempt+1, app)
		}
	}
	if ghosttyCalls != 2 {
		t.Fatalf("Ghostty calls = %d", ghosttyCalls)
	}
}

func TestFocusTerminalSessionRejectsMissingTTYAndOtherOS(t *testing.T) {
	unusedScript := func(string, ...string) (string, error) { return "", nil }
	if _, err := focusTerminalSessionWith("", "", "darwin", unusedScript); err == nil {
		t.Fatal("missing TTY accepted")
	}
	if _, err := focusTerminalSessionWith("ttys001", "", "linux", unusedScript); err == nil {
		t.Fatal("non-macOS terminal switch accepted")
	}
}

func TestFocusTerminalSessionSkipsAppsThatAreNotRunning(t *testing.T) {
	calls := 0
	app, err := focusTerminalSessionWith("ttys001", "", "darwin", func(string, ...string) (string, error) {
		calls++
		if calls == 1 {
			return "not-running", nil
		}
		return "matched", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if app != "Terminal.app" || calls != 2 {
		t.Fatalf("fallback result = %q after %d calls", app, calls)
	}
}

func TestProcessViewSelectsGroups(t *testing.T) {
	m := testModel()
	m.activeTab = processesTab
	m.height = 10
	m.processGroups = make([]processGroup, 20)
	for index := range m.processGroups {
		m.processGroups[index] = processGroup{
			tool: "Codex",
			root: processInfo{pid: index + 1},
		}
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	if m.processCursor != 1 {
		t.Fatalf("process cursor = %d, want 1", m.processCursor)
	}
}

func TestProcessSwitchKeyStartsTerminalFocus(t *testing.T) {
	m := testModel()
	m.activeTab = processesTab
	m.processGroups = []processGroup{{
		tool: "Codex",
		root: processInfo{pid: 101, tty: "ttys001"},
	}}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("switch key did not start terminal focus")
	}
	if !strings.Contains(m.terminalStatus, "ttys001") {
		t.Fatalf("switch status = %q", m.terminalStatus)
	}
}

func TestTerminalSwitchResultReportsHerdrPane(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(terminalSwitchResultMsg{
		tty:         "ttys009",
		app:         "Terminal.app",
		multiplexer: "Herdr",
	})
	m = updated.(model)
	for _, expected := range []string{"Terminal.app", "ttys009", "Herdr pane"} {
		if !strings.Contains(m.terminalStatus, expected) {
			t.Fatalf("terminal status %q missing %q", m.terminalStatus, expected)
		}
	}
}

func TestSwitchTerminalTargetUsesHerdrClientTTY(t *testing.T) {
	target := terminalSwitchTarget{tty: "ttys-inner", cwd: "/workspace/firekeeper"}
	var focusedTTY, focusedCWD string
	result := switchTerminalTargetWith(
		target,
		func(context.Context, terminalSwitchTarget) (herdrFocusTarget, bool, error) {
			return herdrFocusTarget{session: "work", paneID: "2-3", clientTTY: "ttys-outer"}, true, nil
		},
		func(tty, cwd string) (string, error) {
			focusedTTY, focusedCWD = tty, cwd
			return "Terminal.app", nil
		},
	)
	if focusedTTY != "ttys-outer" || focusedCWD != target.cwd {
		t.Fatalf("terminal focus target = %q, %q", focusedTTY, focusedCWD)
	}
	if result.multiplexer != "Herdr" || result.tty != "ttys-outer" || result.err != nil {
		t.Fatalf("switch result = %#v", result)
	}
}

func TestSwitchTerminalTargetFallsBackWhenHerdrFocusFails(t *testing.T) {
	target := terminalSwitchTarget{tty: "ttys001", cwd: "/workspace/firekeeper"}
	result := switchTerminalTargetWith(
		target,
		func(context.Context, terminalSwitchTarget) (herdrFocusTarget, bool, error) {
			return herdrFocusTarget{session: "work", paneID: "2-3"}, true, errors.New("server unavailable")
		},
		func(tty, cwd string) (string, error) {
			if tty != target.tty || cwd != target.cwd {
				t.Fatalf("fallback terminal focus target = %q, %q", tty, cwd)
			}
			return "Ghostty", nil
		},
	)
	if result.app != "Ghostty" || result.err != nil || !strings.Contains(result.warning, "server unavailable") {
		t.Fatalf("fallback switch result = %#v", result)
	}
}

func TestGroupProcessesUsesTopmostMatchingParent(t *testing.T) {
	processes := []processInfo{
		{tool: "Codex", pid: 100, ppid: 1},
		{tool: "Codex", pid: 101, ppid: 100},
		{tool: "Codex", pid: 102, ppid: 101},
		{tool: "Codex", pid: 200, ppid: 1},
		{tool: "Codex", pid: 201, ppid: 200},
		{tool: "Copilot", pid: 300, ppid: 1},
	}
	groups := groupProcesses(processes)

	if len(groups) != 3 {
		t.Fatalf("group count = %d, want 3", len(groups))
	}
	if groups[0].root.pid != 100 || len(groups[0].processes) != 3 {
		t.Fatalf("first group root/count = %d/%d, want 100/3", groups[0].root.pid, len(groups[0].processes))
	}
	if groups[1].root.pid != 200 || len(groups[1].processes) != 2 {
		t.Fatalf("second group root/count = %d/%d, want 200/2", groups[1].root.pid, len(groups[1].processes))
	}
	if groups[2].root.pid != 300 || len(groups[2].processes) != 1 {
		t.Fatalf("third group root/count = %d/%d, want 300/1", groups[2].root.pid, len(groups[2].processes))
	}
}

func TestExpandedProcessGroupShowsSessionAndChildren(t *testing.T) {
	m := testModel()
	m.processGroups = []processGroup{{
		tool: "Codex",
		root: processInfo{pid: 100, tty: "ttys001", elapsed: "01:23"},
		processes: []processInfo{
			{pid: 100, ppid: 1, tty: "ttys001", elapsed: "01:23", command: "codex"},
			{pid: 101, ppid: 100, tty: "ttys001", elapsed: "01:22", command: "codex app-server"},
		},
		sessions: []sessionInfo{{
			id:        "019fb33c-c5f4-75f3-b987-228eb484c6ec",
			name:      "Firekeeper",
			cwd:       "/workspace/firekeeper",
			model:     "gpt-5.6",
			source:    "appServer",
			gitBranch: "main",
		}},
	}}
	m.expandedGroups[100] = true
	lines, _ := m.processBodyLines()
	joined := ansi.Strip(strings.Join(lines, "\n"))
	for _, expected := range []string{"Firekeeper", "/workspace/firekeeper", "gpt-5.6", "PID 101", "ttys001"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expanded group missing %q:\n%s", expected, joined)
		}
	}
}

func TestThreadIDFromRolloutPath(t *testing.T) {
	id, ok := threadIDFromRolloutPath("/tmp/rollout-2026-07-30T06-35-32-019fb33c-c5f4-75f3-b987-228eb484c6ec.jsonl")
	if !ok || id != "019fb33c-c5f4-75f3-b987-228eb484c6ec" {
		t.Fatalf("thread id = %q, ok=%v", id, ok)
	}
	if _, ok := threadIDFromRolloutPath("/tmp/not-a-rollout.jsonl"); ok {
		t.Fatal("invalid rollout produced thread id")
	}
}

func testModel() model { return newModel() }

func TestTUIStartsOnProcessesAndCyclesOnlyTextViews(t *testing.T) {
	m := newModel()
	if m.activeTab != processesTab {
		t.Fatal("TUI must start on Processes")
	}
	for _, key := range []tea.KeyType{tea.KeyTab, tea.KeyShiftTab} {
		updated, cmd := m.Update(tea.KeyMsg{Type: key})
		m = updated.(model)
		if m.activeTab != usageTab || cmd == nil || !m.codexUsageLoading {
			t.Fatal("switching to Usage must request usage refresh")
		}
		updated, cmd = m.Update(tea.KeyMsg{Type: key})
		m = updated.(model)
		if m.activeTab != processesTab || cmd == nil {
			t.Fatal("switching back to Processes must request process refresh")
		}
	}
}

func TestTextViewsFitTerminal(t *testing.T) {
	for _, size := range [][2]int{{20, 6}, {80, 24}, {120, 40}} {
		for _, active := range []tab{processesTab, usageTab} {
			m := newModel()
			m.width, m.height = size[0], size[1]
			m.activeTab = active
			view := m.View()
			lines := strings.Split(view, "\n")
			if len(lines) != m.height {
				t.Fatalf("view %d at %v: rows = %d, want %d", active, size, len(lines), m.height)
			}
			for _, line := range lines {
				if width := ansi.StringWidth(line); width > m.width {
					t.Fatalf("view %d at %v: line width = %d", active, size, width)
				}
			}
			if strings.Contains(view, "\x1b_G") || strings.Contains(view, "Animation") || strings.Contains(view, "Settings") {
				t.Fatal("text view contains removed graphics UI")
			}
		}
	}
}

func TestRetainKnownSessionMetadataExpiresAfterGrace(t *testing.T) {
	root := processInfo{pid: 100, command: "codex app-server"}
	withSession := []processGroup{{tool: "Codex", root: root, sessions: []sessionInfo{{id: "thread-1", state: sessionStateWaiting}}}}
	empty := func() []processGroup { return []processGroup{{tool: "Codex", root: root}} }

	groups, misses := withSession, map[int]int(nil)
	for refresh := 1; refresh <= sessionMetadataGraceRefreshes; refresh++ {
		groups, misses = retainKnownSessionMetadata(groups, empty(), misses)
		if len(groups[0].sessions) != 1 {
			t.Fatalf("refresh %d dropped metadata inside the grace period", refresh)
		}
	}
	groups, misses = retainKnownSessionMetadata(groups, empty(), misses)
	if len(groups[0].sessions) != 0 || len(filterUnidentifiedCodexGroups(groups)) != 0 {
		t.Fatal("closed Codex session must disappear after the grace period")
	}
	if len(misses) != 0 {
		t.Fatalf("misses = %v, want none once metadata is dropped", misses)
	}
}

func TestRetainKnownSessionMetadataResetsWhenMetadataReturns(t *testing.T) {
	root := processInfo{pid: 100, command: "codex"}
	withSession := func() []processGroup {
		return []processGroup{{tool: "Codex", root: root, sessions: []sessionInfo{{id: "thread-1"}}}}
	}
	empty := []processGroup{{tool: "Codex", root: root}}

	groups, misses := retainKnownSessionMetadata(withSession(), empty, nil)
	groups, misses = retainKnownSessionMetadata(groups, withSession(), misses)
	if len(misses) != 0 {
		t.Fatalf("misses = %v, want reset after metadata returned", misses)
	}
	for refresh := 1; refresh <= sessionMetadataGraceRefreshes; refresh++ {
		groups, misses = retainKnownSessionMetadata(groups, []processGroup{{tool: "Codex", root: root}}, misses)
		if len(groups[0].sessions) != 1 {
			t.Fatalf("refresh %d: grace period did not restart", refresh)
		}
	}
}

func TestRetainKnownSessionMetadataSkipsChangedRuntime(t *testing.T) {
	previous := []processGroup{{tool: "Codex", root: processInfo{pid: 100, command: "codex"}, sessions: []sessionInfo{{id: "a"}}}}
	refreshed := []processGroup{{tool: "Codex", root: processInfo{pid: 100, command: "codex resume"}}}
	groups, _ := retainKnownSessionMetadata(previous, refreshed, nil)
	if len(groups[0].sessions) != 0 {
		t.Fatal("metadata must not carry over to a different command")
	}
}

func TestProcessRefreshDropsClosedCodexSession(t *testing.T) {
	m := testModel()
	root := processInfo{pid: 100, command: "codex app-server"}
	updated, _ := m.Update(processResultMsg{refreshed: time.Now(), groups: []processGroup{
		{tool: "Codex", root: root, processes: []processInfo{root}, sessions: []sessionInfo{{id: "thread-1", state: sessionStateWaiting}}},
	}})
	m = updated.(model)
	for refresh := 0; refresh <= sessionMetadataGraceRefreshes; refresh++ {
		updated, _ = m.Update(processResultMsg{refreshed: time.Now(), groups: []processGroup{
			{tool: "Codex", root: root, processes: []processInfo{root}},
		}})
		m = updated.(model)
	}
	if len(m.processGroups) != 0 {
		t.Fatalf("closed Codex session still listed: %d runtimes", len(m.processGroups))
	}
}
