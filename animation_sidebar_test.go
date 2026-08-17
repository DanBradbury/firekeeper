package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAnimationPartySessionsIncludeAllProvidersAndStates(t *testing.T) {
	groups := []processGroup{
		{
			tool: "Codex",
			sessions: []sessionInfo{{
				cwd:   "/workspace/firekeeper",
				state: sessionStateActive,
			}},
		},
		{
			tool: "Copilot",
			sessions: []sessionInfo{
				{cwd: "/workspace/frontend", state: sessionStateNeedsInput},
				{cwd: "/workspace/backend", state: sessionStateWaiting},
			},
		},
		{tool: "OpenCode"},
	}

	sessions := animationPartySessions(groups)
	if len(sessions) != 4 {
		t.Fatalf("party session count = %d, want 4", len(sessions))
	}
	for index, want := range []animationPartySession{
		{provider: "Codex", directory: "firekeeper", state: sessionStateActive},
		{provider: "Copilot", directory: "frontend", state: sessionStateNeedsInput},
		{provider: "Copilot", directory: "backend", state: sessionStateWaiting},
		{provider: "OpenCode", directory: "DIR UNKNOWN", state: sessionStateUnknown},
	} {
		if sessions[index] != want {
			t.Fatalf("party session %d = %#v, want %#v", index, sessions[index], want)
		}
	}
}

func TestSelectedPartyTerminalTargetTracksFlattenedSessionSelection(t *testing.T) {
	groups := []processGroup{
		{
			tool: "Copilot",
			root: processInfo{tty: "ttys001"},
			sessions: []sessionInfo{
				{cwd: "/workspace/frontend"},
				{cwd: "/workspace/backend"},
			},
		},
		{tool: "OpenCode", root: processInfo{tty: "ttys002"}},
	}

	target, ok := selectedPartyTerminalTarget(groups, 1)
	if !ok || target.tty != "ttys001" || target.cwd != "/workspace/backend" {
		t.Fatalf("second party target = %#v, %t", target, ok)
	}
	target, ok = selectedPartyTerminalTarget(groups, 2)
	if !ok || target.tty != "ttys002" || target.cwd != "" {
		t.Fatalf("process-only party target = %#v, %t", target, ok)
	}
	if _, ok := selectedPartyTerminalTarget(groups, 3); ok {
		t.Fatal("out-of-range party selection returned a target")
	}
}

func TestAnimationProviderPortraitMatchesConfiguredHeadshot(t *testing.T) {
	solid := func(red uint8) sprite {
		return sprite{width: 1, height: 1, pixels: []rgba{{r: red, a: 255}}}
	}
	m := newModel([][]sprite{{solid(1)}})
	m.wizardHeadshot = solid(40)
	m.warriorHeadshot = solid(80)
	m.mageHeadshot = solid(120)
	m.codexSprite = codexSpriteRanger
	m.copilotSprite = codexSpriteWarrior
	m.kimiSprite = codexSpriteMage

	for _, test := range []struct {
		provider string
		want     uint8
	}{
		{provider: "Codex", want: 40},
		{provider: "Copilot", want: 80},
		{provider: "Kimi", want: 120},
	} {
		portrait := m.animationProviderPortrait(test.provider)
		if got := portrait.at(portrait.width/2, portrait.height/2).r; got != test.want {
			t.Fatalf("%s portrait red = %d, want %d", test.provider, got, test.want)
		}
	}
	if portrait := m.animationProviderPortrait("OpenCode"); portrait.width != 0 || portrait.height != 0 {
		t.Fatalf("OpenCode portrait = %dx%d, want generic fallback", portrait.width, portrait.height)
	}
}

func TestAnimationSceneDrawsStackedPartyCards(t *testing.T) {
	solid := func(red uint8) sprite {
		return sprite{width: 1, height: 1, pixels: []rgba{{r: red, a: 255}}}
	}
	m := newModel([][]sprite{{solid(1)}})
	m.wizardHeadshot = solid(220)
	m.processGroups = []processGroup{
		{tool: "Codex", sessions: []sessionInfo{{cwd: "/workspace/firekeeper", state: sessionStateActive}}},
		{tool: "OpenCode"},
	}

	scene := m.animationScene(80, 21)
	panelWidth := partySidebarWidth(scene.width)
	panelX := scene.width - panelWidth
	if got := scene.at(panelX, 0); got != (rgba{r: partyPanelBorder.r, g: partyPanelBorder.g, b: partyPanelBorder.b, a: 255}) {
		t.Fatalf("party panel border = %#v, want %#v", got, partyPanelBorder)
	}

	firstCardX := panelX + partySidebarPadding
	firstCardY := partySidebarHeaderHeight
	if got := scene.at(firstCardX, firstCardY); got != (rgba{r: partySelectedBorder.r, g: partySelectedBorder.g, b: partySelectedBorder.b, a: 255}) {
		t.Fatalf("selected card border = %#v, want %#v", got, partySelectedBorder)
	}
	activeAccent := rgb{r: 81, g: 220, b: 118}
	if got := scene.at(firstCardX+1, firstCardY+1); got != (rgba{r: activeAccent.r, g: activeAccent.g, b: activeAccent.b, a: 255}) {
		t.Fatalf("active card accent = %#v, want %#v", got, activeAccent)
	}
	portraitCenterX := firstCardX + partyCardStatusIndicatorSize + 2 + partyCardPortraitSize/2
	portraitCenterY := firstCardY + partyCardHeight/2
	if got := scene.at(portraitCenterX, portraitCenterY).r; got != 220 {
		t.Fatalf("party portrait red = %d, want configured headshot red 220", got)
	}

	secondCardY := firstCardY + partyCardHeight + partyCardGap
	unknownAccent := rgb{r: 142, g: 151, b: 166}
	if got := scene.at(firstCardX+1, secondCardY+1); got != (rgba{r: unknownAccent.r, g: unknownAccent.g, b: unknownAccent.b, a: 255}) {
		t.Fatalf("unknown card accent = %#v, want %#v", got, unknownAccent)
	}
}

func TestAnimationPartySidebarUsesResponsiveWidth(t *testing.T) {
	if got := partySidebarWidth((partySidebarMinimumColumns - 1) * animationSourceScale); got != 0 {
		t.Fatalf("narrow sidebar width = %d, want hidden", got)
	}
	if got := partySidebarWidth(80 * animationSourceScale); got != partySidebarColumns*animationSourceScale {
		t.Fatalf("wide sidebar width = %d, want %d", got, partySidebarColumns*animationSourceScale)
	}
}

func TestAnimationPartyKeysSelectAndScrollEveryMember(t *testing.T) {
	m := testModel()
	m.width = 80
	m.height = 15
	for index := 0; index < 5; index++ {
		m.processGroups = append(m.processGroups, processGroup{
			tool: "Codex",
			sessions: []sessionInfo{{
				cwd:   "/workspace/member",
				state: sessionStateActive,
			}},
		})
	}
	if capacity := partySidebarCapacity(m.width*animationSourceScale, (m.height-chromeRows)*2*animationSourceScale); capacity != 2 {
		t.Fatalf("party capacity = %d, want 2", capacity)
	}
	originalAnimation := m.animation
	originalRate := m.frameDuration

	for wantCursor := 1; wantCursor < 5; wantCursor++ {
		key := tea.KeyMsg{Type: tea.KeyDown}
		if wantCursor == 1 {
			key = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
		}
		updated, _ := m.Update(key)
		m = updated.(model)
		if m.partyCursor != wantCursor {
			t.Fatalf("party cursor = %d, want %d", m.partyCursor, wantCursor)
		}
	}
	if m.partyScroll != 3 {
		t.Fatalf("party scroll at last member = %d, want 3", m.partyScroll)
	}
	if m.animation != originalAnimation || m.frameDuration != originalRate {
		t.Fatalf("party navigation changed animation/rate to %d/%s", m.animation, m.frameDuration)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	if m.partyCursor != 0 || m.partyScroll != 0 {
		t.Fatalf("wrapped party selection = cursor %d scroll %d, want 0/0", m.partyCursor, m.partyScroll)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	if m.partyCursor != 4 || m.partyScroll != 3 {
		t.Fatalf("reverse wrapped party selection = cursor %d scroll %d, want 4/3", m.partyCursor, m.partyScroll)
	}
}

func TestPartyViewportKeepsSelectedMemberVisible(t *testing.T) {
	for _, test := range []struct {
		name                   string
		cursor, scroll         int
		total, capacity        int
		wantCursor, wantScroll int
	}{
		{name: "scroll down", cursor: 4, total: 6, capacity: 3, wantCursor: 4, wantScroll: 2},
		{name: "scroll up", cursor: 1, scroll: 3, total: 6, capacity: 3, wantCursor: 1, wantScroll: 1},
		{name: "clamp stale selection", cursor: 9, scroll: 9, total: 4, capacity: 2, wantCursor: 3, wantScroll: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			cursor, scroll := partyViewport(test.cursor, test.scroll, test.total, test.capacity)
			if cursor != test.wantCursor || scroll != test.wantScroll {
				t.Fatalf("viewport = cursor %d scroll %d, want %d/%d", cursor, scroll, test.wantCursor, test.wantScroll)
			}
		})
	}
}

func TestTruncatePartyTextPreservesDirectoryTail(t *testing.T) {
	if got := truncatePartyText("long-workspace-name", 10); got != "~pace-name" {
		t.Fatalf("truncated directory = %q, want %q", got, "~pace-name")
	}
}
