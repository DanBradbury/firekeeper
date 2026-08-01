package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi/kitty"
)

func TestEmbeddedSheetSlicesIntoTenAnimations(t *testing.T) {
	sheet, err := decodeSprite(rangerSheetPNG)
	if err != nil {
		t.Fatal(err)
	}
	if sheet.width != 320 || sheet.height != 320 {
		t.Fatalf("sheet dimensions = %dx%d, want 320x320", sheet.width, sheet.height)
	}

	animations, err := sliceSheet(sheet, sheetColumns, sheetRows)
	if err != nil {
		t.Fatal(err)
	}
	if len(animations) != 10 {
		t.Fatalf("animation count = %d, want 10", len(animations))
	}
	for animation, frames := range animations {
		if len(frames) != 10 {
			t.Fatalf("animation %d frame count = %d, want 10", animation, len(frames))
		}
		for frame, image := range frames {
			if image.width != 32 || image.height != 32 {
				t.Fatalf("animation %d frame %d = %dx%d, want 32x32", animation, frame, image.width, image.height)
			}
		}
	}
}

func TestAnimationControlsWrapAndResetFrame(t *testing.T) {
	m := testModel()
	m.frame = 7

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(model)
	if m.animation != 9 || m.frame != 0 {
		t.Fatalf("left from first animation = animation %d frame %d, want 9/0", m.animation, m.frame)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	if m.animation != 0 || m.frame != 0 {
		t.Fatalf("right from last animation = animation %d frame %d, want 0/0", m.animation, m.frame)
	}
}

func TestAnimationRateControlsAndBounds(t *testing.T) {
	m := testModel()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	if m.frameDuration != defaultFrameDuration-rateStep {
		t.Fatalf("up duration = %s, want %s", m.frameDuration, defaultFrameDuration-rateStep)
	}

	m.frameDuration = minimumFrameDuration
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	if m.frameDuration != minimumFrameDuration {
		t.Fatalf("minimum duration crossed: %s", m.frameDuration)
	}

	m.frameDuration = maximumFrameDuration
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	if m.frameDuration != maximumFrameDuration {
		t.Fatalf("maximum duration crossed: %s", m.frameDuration)
	}
}

func TestSpriteSizeControlsAndBounds(t *testing.T) {
	m := testModel()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	m = updated.(model)
	if m.spriteColumns != defaultSpriteColumns+2 || m.spriteRows != defaultSpriteRows+1 {
		t.Fatalf("larger size = %dx%d, want %dx%d", m.spriteColumns, m.spriteRows, defaultSpriteColumns+2, defaultSpriteRows+1)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	m = updated.(model)
	if m.spriteColumns != defaultSpriteColumns || m.spriteRows != defaultSpriteRows {
		t.Fatalf("restored size = %dx%d, want %dx%d", m.spriteColumns, m.spriteRows, defaultSpriteColumns, defaultSpriteRows)
	}

	m.spriteColumns = minimumSpriteColumns
	m.spriteRows = minimumSpriteRows
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	m = updated.(model)
	if m.spriteColumns != minimumSpriteColumns || m.spriteRows != minimumSpriteRows {
		t.Fatalf("minimum size crossed: %dx%d", m.spriteColumns, m.spriteRows)
	}
}

func TestAppConfigSelectsRendererAndSize(t *testing.T) {
	kittyEnv := map[string]string{"KITTY_WINDOW_ID": "1"}
	config, err := newAppConfig("auto", 24, 12, func(key string) string { return kittyEnv[key] })
	if err != nil {
		t.Fatal(err)
	}
	if config.renderer != kittyRenderer || config.spriteColumns != 24 || config.spriteRows != 12 {
		t.Fatalf("auto Kitty config = %#v", config)
	}

	config, err = newAppConfig("blocks", 10, 5, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if config.renderer != blockRenderer {
		t.Fatalf("forced renderer = %s, want blocks", config.renderer)
	}

	if _, err := newAppConfig("kitty", 0, 8, func(string) string { return "" }); err == nil {
		t.Fatal("zero sprite width accepted")
	}
}

func TestKittyAnimationUsesConfiguredPlacement(t *testing.T) {
	frame := sprite{
		width:  1,
		height: 1,
		pixels: []rgba{{r: 255, g: 128, b: 64, a: 255}},
	}
	transmission, err := encodeKittySprite(frame, defaultSpriteColumns, defaultSpriteRows)
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range []string{"f=100", "q=2", "i=42", "p=1", "U=1", "C=1", "c=16", "r=8", "a=T"} {
		if !strings.Contains(transmission, option) {
			t.Errorf("Kitty transmission missing %q", option)
		}
	}

	m := newModelWithConfig([][]sprite{{frame}}, appConfig{
		renderer:      kittyRenderer,
		spriteColumns: defaultSpriteColumns,
		spriteRows:    defaultSpriteRows,
	})
	m.width = 40
	m.height = 20
	view := m.viewAnimation(m.height - chromeRows)
	if got, want := strings.Count(view, string(kitty.Placeholder)), defaultSpriteColumns*defaultSpriteRows; got != want {
		t.Fatalf("placeholder count = %d, want %d", got, want)
	}
}

func TestTickAdvancesAndWrapsFrame(t *testing.T) {
	m := testModel()
	m.frame = 9
	m.fireFrames = make([]sprite, forestFireFrames)
	m.fireFrame = forestFireFrames - 1

	updated, cmd := m.Update(tickMsg(time.Now()))
	m = updated.(model)
	if m.frame != 0 {
		t.Fatalf("frame after tick = %d, want 0", m.frame)
	}
	if cmd == nil {
		t.Fatal("tick did not schedule next tick")
	}
	if m.fireFrame != 0 {
		t.Fatalf("fire frame after tick = %d, want 0", m.fireFrame)
	}

	m.playing = false
	updated, _ = m.Update(tickMsg(time.Now()))
	m = updated.(model)
	if m.frame != 0 {
		t.Fatalf("paused animation advanced to frame %d", m.frame)
	}
}

func TestTabCyclesThroughProcessAndUsageViews(t *testing.T) {
	m := testModel()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)

	if m.activeTab != processesTab {
		t.Fatalf("active tab = %d, want processes tab", m.activeTab)
	}
	if cmd == nil {
		t.Fatal("opening process tab did not request refresh")
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	if m.activeTab != usageTab {
		t.Fatalf("second tab = %d, want usage tab", m.activeTab)
	}
	if cmd == nil || !m.codexUsageLoading {
		t.Fatal("opening Codex usage tab did not request refresh")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	if m.activeTab != animationTab {
		t.Fatalf("third tab = %d, want animation tab", m.activeTab)
	}
}

func TestMenuKeyTogglesRPGMenuOnAnimationTab(t *testing.T) {
	m := testModel()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = updated.(model)
	if !m.menuOpen {
		t.Fatal("M did not open RPG menu")
	}

	view := m.View()
	for _, item := range []string{"▶ ITEM", "MAGIC", "EQUIP", "STATUS", "FORMATION", "CONFIG", "TIME", "GIL"} {
		if !strings.Contains(view, item) {
			t.Fatalf("RPG menu missing %q", item)
		}
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}})
	m = updated.(model)
	if m.menuOpen {
		t.Fatal("second M did not close RPG menu")
	}
}

func TestEscapeClosesTopLevelRPGMenu(t *testing.T) {
	m := testModel()
	m.menuOpen = true
	m.menuPage = rpgMenuCommands

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.menuOpen {
		t.Fatal("Esc did not close top-level RPG menu")
	}
}

func TestRPGMenuDoesNotOpenOnProcessesTab(t *testing.T) {
	m := testModel()
	m.activeTab = processesTab
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if updated.(model).menuOpen {
		t.Fatal("M opened RPG menu on processes tab")
	}
}

func TestArrowKeysNavigateOpenRPGMenu(t *testing.T) {
	m := testModel()
	m.width = 80
	m.menuOpen = true
	originalAnimation := m.animation
	originalRate := m.frameDuration

	for _, step := range []struct {
		key  tea.KeyType
		want int
	}{
		{tea.KeyRight, 1},
		{tea.KeyDown, 4},
		{tea.KeyLeft, 3},
		{tea.KeyUp, 0},
	} {
		updated, _ := m.Update(tea.KeyMsg{Type: step.key})
		m = updated.(model)
		if m.menuCursor != step.want {
			t.Fatalf("menu cursor after %v = %d, want %d", step.key, m.menuCursor, step.want)
		}
	}
	if m.animation != originalAnimation || m.frameDuration != originalRate {
		t.Fatal("menu navigation changed animation controls")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	if !strings.Contains(m.View(), "▶ MAGIC") {
		t.Fatal("menu cursor did not render on MAGIC")
	}
}

func TestEnterOnStatusShowsActiveCodexSessions(t *testing.T) {
	m := testModel()
	m.width = 64
	m.height = 24
	m.menuOpen = true
	m.menuCursor = rpgStatusMenuIndex
	m.processGroups = []processGroup{
		{
			tool: "Codex",
			root: processInfo{pid: 101, tty: "ttys001"},
			sessions: []sessionInfo{{
				name:  "Implementing an intentionally long active session title that must be truncated for this panel",
				model: "gpt-5.6",
				cwd:   "/workspace/firekeeper",
			}},
		},
		{tool: "Copilot", root: processInfo{pid: 202, tty: "ttys002"}},
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if m.menuPage != rpgMenuStatus {
		t.Fatal("Enter on STATUS did not open Codex session list")
	}
	view := m.View()
	for _, expected := range []string{"CODEX SESSIONS (1)", "║  ▶ Codex1 [", "UNKNOWN", "…]"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("status menu missing %q", expected)
		}
	}
	if strings.Contains(view, "Copilot1") {
		t.Fatal("status menu included non-Codex runtime")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.menuPage != rpgMenuCommands {
		t.Fatal("Esc did not return to command menu")
	}
}

func TestStatusMenuFallsBackWhenMetadataUnavailable(t *testing.T) {
	lines := rpgStatusMenuLines(80, 9, []processGroup{{
		tool: "Codex",
		root: processInfo{pid: 303, tty: "ttys009"},
	}}, 0)
	joined := strings.Join(lines, "\n")
	for _, expected := range []string{"Codex1 [", "metadata unavailable", "ttys009", "PID 303"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("fallback status menu missing %q", expected)
		}
	}
}

func TestStatusMenuUpDownNavigationWrapsAndRendersCursor(t *testing.T) {
	m := testModel()
	m.width = 80
	m.height = 24
	m.menuOpen = true
	m.menuPage = rpgMenuStatus
	for index := 0; index < 3; index++ {
		m.processGroups = append(m.processGroups, processGroup{
			tool:     "Codex",
			root:     processInfo{pid: 400 + index, tty: fmt.Sprintf("ttys00%d", index)},
			sessions: []sessionInfo{{name: fmt.Sprintf("Session %d", index+1)}},
		})
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	if m.statusCursor != 1 || !strings.Contains(m.View(), "║  ▶ Codex2 [") {
		t.Fatalf("down selection = %d", m.statusCursor)
	}
	if !strings.Contains(m.View(), "║    Codex1 [") {
		t.Fatal("unselected status row missing left padding")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	if m.statusCursor != 2 || !strings.Contains(m.View(), "║  ▶ Codex3 [") {
		t.Fatalf("wrapped up selection = %d", m.statusCursor)
	}
}

func TestStatusMenuScrollsToSelectedSession(t *testing.T) {
	groups := make([]processGroup, 8)
	for index := range groups {
		groups[index] = processGroup{
			tool:     "Codex",
			root:     processInfo{pid: 500 + index},
			sessions: []sessionInfo{{name: fmt.Sprintf("Session %d", index+1)}},
		}
	}
	view := strings.Join(rpgStatusMenuLines(80, 9, groups, 6), "\n")
	if !strings.Contains(view, "║  ▶ Codex7 [") {
		t.Fatal("selected scrolled session not visible")
	}
	if strings.Contains(view, "Codex1 [") {
		t.Fatal("status list did not scroll away from first session")
	}
}

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

func TestRPGMenuLinesFillRequestedPanel(t *testing.T) {
	lines := rpgMenuLines(24, 18, 0)
	if len(lines) != 18 {
		t.Fatalf("menu height = %d, want 18", len(lines))
	}
	if !strings.Contains(lines[0], "╔"+strings.Repeat("═", 22)+"╗") {
		t.Fatalf("menu top border = %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "╚"+strings.Repeat("═", 22)+"╝") {
		t.Fatalf("menu bottom border = %q", lines[len(lines)-1])
	}
}

func TestRPGMenuOverlaysBottomWithoutResizingScene(t *testing.T) {
	m := testModel()
	m.width = 80
	m.height = 24
	contentRows := m.height - chromeRows
	base := strings.Split(m.viewAnimation(contentRows), "\n")
	overlaid := strings.Split(m.viewAnimationMenu(contentRows), "\n")
	overlayTop := contentRows - 9

	if len(base) != len(overlaid) {
		t.Fatalf("menu changed scene height from %d to %d", len(base), len(overlaid))
	}
	for row := 0; row < overlayTop; row++ {
		if base[row] != overlaid[row] {
			t.Fatalf("menu shifted scene row %d", row)
		}
	}
	if !strings.Contains(overlaid[overlayTop], "╔") {
		t.Fatalf("bottom overlay missing at row %d", overlayTop)
	}
	if !strings.Contains(overlaid[len(overlaid)-1], "╚") {
		t.Fatal("bottom overlay missing closing border")
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

func TestFocusTerminalSessionSelectsMatchingMacAdapter(t *testing.T) {
	var gotTTY string
	app, err := focusTerminalSessionWith(
		"ttys009",
		"darwin",
		func(script, tty string) (string, error) {
			gotTTY = tty
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

func TestFocusTerminalSessionRejectsMissingTTYAndOtherOS(t *testing.T) {
	unusedScript := func(string, string) (string, error) { return "", nil }
	if _, err := focusTerminalSessionWith("", "darwin", unusedScript); err == nil {
		t.Fatal("missing TTY accepted")
	}
	if _, err := focusTerminalSessionWith("ttys001", "linux", unusedScript); err == nil {
		t.Fatal("non-macOS terminal switch accepted")
	}
}

func TestFocusTerminalSessionSkipsAppsThatAreNotRunning(t *testing.T) {
	calls := 0
	app, err := focusTerminalSessionWith("ttys001", "darwin", func(string, string) (string, error) {
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

func TestProcessViewSelectsGroupsWithoutChangingAnimationRate(t *testing.T) {
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
	originalRate := m.frameDuration

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	if m.processCursor != 1 {
		t.Fatalf("process cursor = %d, want 1", m.processCursor)
	}
	if m.frameDuration != originalRate {
		t.Fatalf("process scrolling changed animation rate to %s", m.frameDuration)
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
	joined := strings.Join(lines, "\n")
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

func TestDrawSpriteHonorsTransparency(t *testing.T) {
	fill := rgb{r: 1, g: 2, b: 3}
	c := newCanvas(2, 1, fill)
	s := sprite{
		width:  2,
		height: 1,
		pixels: []rgba{
			{r: 20, g: 30, b: 40, a: 255},
			{r: 255, g: 255, b: 255, a: 0},
		},
	}
	c.drawSprite(0, 0, s)

	if got := c.at(0, 0); got != (rgb{r: 20, g: 30, b: 40}) {
		t.Fatalf("opaque pixel = %#v", got)
	}
	if got := c.at(1, 0); got != fill {
		t.Fatalf("transparent pixel changed to %#v", got)
	}
}

func TestCanvasRepeatsGrassTileBehindSprite(t *testing.T) {
	tile := sprite{
		width:  2,
		height: 2,
		pixels: []rgba{
			{r: 10, a: 255}, {r: 20, a: 255},
			{r: 30, a: 255}, {r: 40, a: 255},
		},
	}
	canvas := newCanvas(5, 3, background)
	canvas.tileSprite(tile)

	for _, check := range []struct {
		x, y int
		red  uint8
	}{
		{0, 0, 10}, {1, 0, 20}, {2, 0, 10}, {4, 0, 10},
		{0, 1, 30}, {3, 1, 40}, {4, 2, 10},
	} {
		if got := canvas.at(check.x, check.y).r; got != check.red {
			t.Fatalf("pixel (%d,%d) red = %d, want %d", check.x, check.y, got, check.red)
		}
	}

	foreground := sprite{width: 1, height: 1, pixels: []rgba{{r: 99, a: 255}}}
	canvas.drawSprite(2, 1, foreground)
	if got := canvas.at(2, 1).r; got != 99 {
		t.Fatalf("foreground red = %d, want 99", got)
	}
	if got := canvas.at(2, 2).r; got != 10 {
		t.Fatalf("grass beside foreground red = %d, want 10", got)
	}
}

func TestKittyAnimationCompositesGrassAcrossViewport(t *testing.T) {
	frame := sprite{width: 1, height: 1, pixels: []rgba{{r: 255, a: 255}}}
	grass := sprite{width: 1, height: 1, pixels: []rgba{{g: 80, a: 255}}}
	m := newModelWithGrass([][]sprite{{frame}}, appConfig{
		renderer:      kittyRenderer,
		spriteColumns: 2,
		spriteRows:    1,
	}, grass)
	m.width = 8
	m.height = 7

	view := m.viewAnimation(m.height - chromeRows)
	if got, want := strings.Count(view, string(kitty.Placeholder)), m.width*(m.height-chromeRows); got != want {
		t.Fatalf("grass scene placeholder count = %d, want %d", got, want)
	}

	scene := m.grassAnimationScene(m.width, m.height-chromeRows)
	if got, want := scene.width, m.width*animationSourceScale; got != want {
		t.Fatalf("native grass scene width = %d, want %d", got, want)
	}
	if got, want := scene.height, (m.height-chromeRows)*2*animationSourceScale; got != want {
		t.Fatalf("native grass scene height = %d, want %d", got, want)
	}
}

func TestAnimationSceneScalesFireAndKeepsCharacterClear(t *testing.T) {
	grass := sprite{width: 1, height: 1, pixels: []rgba{{a: 255}}}
	character := sprite{width: 1, height: 1, pixels: []rgba{{r: 200, a: 255}}}
	fire := sprite{width: 2, height: 4, pixels: []rgba{
		{g: 200, a: 255}, {g: 200, a: 255},
		{g: 200, a: 255}, {g: 200, a: 255},
		{g: 200, a: 255}, {g: 200, a: 255},
		{g: 200, a: 255}, {g: 200, a: 255},
	}}
	m := newModelWithGrass([][]sprite{{character}}, appConfig{
		renderer:      blockRenderer,
		spriteColumns: 4,
		spriteRows:    2,
	}, grass).withFire([]sprite{fire})

	scene := m.grassAnimationScene(40, 20)
	layout := m.animationLayout(scene.width, scene.height)
	if got, want := layout.fire.width, fire.width+animationFireWidthIncrease; got != want {
		t.Fatalf("scaled fire width = %d, want %d", got, want)
	}
	if layout.fire.width*fire.height != layout.fire.height*fire.width {
		t.Fatalf("scaled fire size %dx%d does not preserve %dx%d aspect ratio", layout.fire.width, layout.fire.height, fire.width, fire.height)
	}
	if got, want := layout.fireX, (scene.width-layout.fire.width)/2; got != want {
		t.Fatalf("fire left = %d, want centered at %d", got, want)
	}
	if got := scene.at(layout.fireX, layout.fireY); got.g != 200 {
		t.Fatalf("center fire pixel = %#v, want green fire", got)
	}

	if got := scene.at(layout.characterX, layout.characterY); got.r != 200 {
		t.Fatalf("left character pixel = %#v, want red character", got)
	}
	if got, want := layout.fireX-(layout.characterX+layout.character.width), animationCharacterFireGap; got != want {
		t.Fatalf("character-to-fire gap = %d, want %d", got, want)
	}
	if got, want := layout.characterY+layout.character.height, layout.fireY+layout.fire.height; got != want {
		t.Fatalf("character bottom = %d, fire bottom = %d", got, want)
	}
}

func TestSliceSheetRejectsInvalidGrid(t *testing.T) {
	_, err := sliceSheet(sprite{width: 31, height: 32}, 10, 10)
	if err == nil {
		t.Fatal("sliceSheet accepted non-divisible grid")
	}
}

func testModel() model {
	animations := make([][]sprite, 10)
	for animation := range animations {
		animations[animation] = make([]sprite, 10)
	}
	return newModel(animations)
}
