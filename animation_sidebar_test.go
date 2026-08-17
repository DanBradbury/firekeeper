package main

import "testing"

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

func TestTruncatePartyTextPreservesDirectoryTail(t *testing.T) {
	if got := truncatePartyText("long-workspace-name", 10); got != "~pace-name" {
		t.Fatalf("truncated directory = %q, want %q", got, "~pace-name")
	}
}
