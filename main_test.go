package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi/kitty"
)

func TestEmbeddedWizardSpritesheet(t *testing.T) {
	animations, err := decodeWizardAnimations(wizardSheetPNG)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(animations), 4; got != want {
		t.Fatalf("animations = %d, want %d", got, want)
	}
	for index, want := range []int{6, 7, 18, 5} {
		if got := len(animations[index]); got != want {
			t.Fatalf("animation %d frames = %d, want %d", index, got, want)
		}
	}
	if frame := animations[0][0]; frame.width != 161 || frame.height != 106 {
		t.Fatalf("frame = %dx%d, want 161x106", frame.width, frame.height)
	}
}

func TestGroupWizardFramesRejectsWrongFrameCount(t *testing.T) {
	_, err := groupWizardFrames(make([]sprite, 35))
	if err == nil {
		t.Fatal("groupWizardFrames accepted incomplete sheet")
	}
}

func TestEmbeddedWizardHeadshot(t *testing.T) {
	headshot, err := decodeSprite(wizardHeadshotPNG)
	if err != nil {
		t.Fatal(err)
	}
	if headshot.width != 93 || headshot.height != 94 {
		t.Fatalf("headshot = %dx%d, want 93x94", headshot.width, headshot.height)
	}
}

func TestAnimationLeftRightControlsNoLongerChangeAnimation(t *testing.T) {
	m := testModel()
	m.animation = 3
	m.frame = 7

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(model)
	if m.animation != 3 || m.frame != 7 {
		t.Fatalf("left changed animation/frame to %d/%d", m.animation, m.frame)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	if m.animation != 3 || m.frame != 7 {
		t.Fatalf("right changed animation/frame to %d/%d", m.animation, m.frame)
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
	for _, option := range []string{"f=100", "q=2", "i=42", "p=1", "U=1", "C=1", "c=32", "r=16", "a=T"} {
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
	if got, want := strings.Count(view, string(kitty.Placeholder)), m.width*(m.height-chromeRows); got != want {
		t.Fatalf("placeholder count = %d, want %d", got, want)
	}
}

func TestTickAdvancesAndWrapsFrame(t *testing.T) {
	m := testModel()
	m.frame = 9

	updated, cmd := m.Update(tickMsg(time.Now()))
	m = updated.(model)
	if m.frame != 0 {
		t.Fatalf("frame after tick = %d, want 0", m.frame)
	}
	if cmd == nil {
		t.Fatal("tick did not schedule next tick")
	}
	m.playing = false
	updated, _ = m.Update(tickMsg(time.Now()))
	m = updated.(model)
	if m.frame != 0 {
		t.Fatalf("paused animation advanced to frame %d", m.frame)
	}
}

func TestActiveProviderAttacksThenPausesBeforeRepeating(t *testing.T) {
	frame := func(value uint8) sprite {
		return sprite{width: 1, height: 1, pixels: []rgba{{r: value, a: 255}}}
	}
	m := newModel([][]sprite{{frame(10)}})
	m.codexSprites = [][][]sprite{{{frame(10)}, {frame(20), frame(21)}}}
	m.processGroups = []processGroup{{tool: "Codex", sessions: []sessionInfo{{state: sessionStateActive}}}}
	start := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)

	m.advanceFrame(start)
	if !m.codexAttack.attacking || m.codexAttack.frame != 0 {
		t.Fatalf("first active tick = %#v, want attack frame 0", m.codexAttack)
	}
	if got := m.currentProviderFrame(m.codexSprites, m.codexSprite, m.codexAttack).at(0, 0).r; got != 20 {
		t.Fatalf("attack frame red = %d, want 20", got)
	}

	m.advanceFrame(start.Add(m.frameDuration))
	if m.codexAttack.frame != 1 {
		t.Fatalf("second active tick frame = %d, want 1", m.codexAttack.frame)
	}
	m.advanceFrame(start.Add(2 * m.frameDuration))
	if m.codexAttack.attacking {
		t.Fatal("attack did not enter pause after final frame")
	}
	pause := m.codexAttack.nextAttackAt.Sub(start.Add(2 * m.frameDuration))
	if pause < minimumAttackPause || pause > maximumAttackPause {
		t.Fatalf("attack pause = %s, want %s-%s", pause, minimumAttackPause, maximumAttackPause)
	}
	if got := m.currentProviderFrame(m.codexSprites, m.codexSprite, m.codexAttack).at(0, 0).r; got != 10 {
		t.Fatalf("pause frame red = %d, want idle 10", got)
	}

	m.advanceFrame(m.codexAttack.nextAttackAt)
	if !m.codexAttack.attacking || m.codexAttack.frame != 0 {
		t.Fatalf("attack did not restart after pause: %#v", m.codexAttack)
	}
	m.processGroups = nil
	m.advanceFrame(m.codexAttack.nextAttackAt.Add(m.frameDuration))
	if m.codexAttack.attacking || !m.codexAttack.nextAttackAt.IsZero() {
		t.Fatalf("inactive provider retained attack state: %#v", m.codexAttack)
	}
}

func TestNewProviderPlaysRevealEffectBeforeCharacter(t *testing.T) {
	frame := func(red uint8) sprite {
		return sprite{width: 1, height: 1, pixels: []rgba{{r: red, a: 255}}}
	}
	m := newModel([][]sprite{{frame(200)}}).withRevealEffect([]sprite{frame(80), frame(120)})
	updated, _ := m.Update(processResultMsg{groups: []processGroup{{
		tool:     "Codex",
		sessions: []sessionInfo{{name: "Revealed session"}},
	}}, refreshed: time.Now()})
	m = updated.(model)
	if !m.codexReveal.playing || m.codexReveal.frame != 0 {
		t.Fatalf("initial reveal = %#v, want frame zero", m.codexReveal)
	}
	scene := m.animationScene(40, 20)
	layout := m.animationLayout(scene.width, scene.height)
	if !layout.codexRevealing || scene.at(layout.characterEffectX, layout.characterEffectY).r != 80 {
		t.Fatal("first reveal frame did not replace Codex character")
	}

	m.advanceFrame(time.Now())
	if m.codexReveal.frame != 1 {
		t.Fatalf("reveal frame = %d, want 1", m.codexReveal.frame)
	}
	m.advanceFrame(time.Now().Add(m.frameDuration))
	if m.codexReveal.playing {
		t.Fatal("reveal effect did not finish")
	}
	scene = m.animationScene(40, 20)
	layout = m.animationLayout(scene.width, scene.height)
	if layout.codexRevealing || scene.at(layout.characterX, layout.characterY+layout.character.height-1).r != 200 {
		t.Fatal("Codex character was not revealed after effect")
	}
}

func TestTabCyclesThroughProcessUsageAndSettingsViews(t *testing.T) {
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
	if m.activeTab != settingsTab {
		t.Fatalf("third tab = %d, want settings tab", m.activeTab)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	if m.activeTab != animationTab {
		t.Fatalf("fourth tab = %d, want animation tab", m.activeTab)
	}
}

func TestSettingsCodexSpriteDefaultsToRangerAndEdits(t *testing.T) {
	m := testModel()
	if m.codexSprite != codexSpriteRanger {
		t.Fatalf("default Codex sprite = %s, want Ranger", m.codexSprite)
	}
	m.activeTab = settingsTab
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	if m.codexSprite != codexSpriteRanger || m.settingsEditing {
		t.Fatalf("browse changed setting: sprite=%s editing=%t", m.codexSprite, m.settingsEditing)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if !m.settingsEditing {
		t.Fatal("Enter did not start settings editing")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	if m.codexSprite != codexSpriteWarrior {
		t.Fatalf("edited Codex sprite = %s, want Warrior", m.codexSprite)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if m.settingsEditing {
		t.Fatal("Enter did not finish settings editing")
	}
	if !strings.Contains(m.View(), "SETTINGS") || !strings.Contains(m.View(), "Warrior") {
		t.Fatalf("settings view missing selected sprite:\n%s", m.View())
	}
}

func TestCodexSpriteSelectionUpdatesMainAnimation(t *testing.T) {
	ranger := sprite{width: 1, height: 1, pixels: []rgba{{r: 10, a: 255}}}
	warrior := sprite{width: 1, height: 1, pixels: []rgba{{r: 20, a: 255}}}
	m := newModel([][]sprite{{ranger}})
	m.codexSprites = [][][]sprite{{{ranger}}, {{warrior}}, {{sprite{width: 1, height: 1, pixels: []rgba{{r: 30, a: 255}}}}}}
	m.activeTab = settingsTab
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	if got := m.currentFrame().at(0, 0).r; got != 20 {
		t.Fatalf("main animation sprite red = %d, want 20", got)
	}
}

func TestProviderSpritesRenderAsSeparateCharacters(t *testing.T) {
	makeFrame := func(width, height int, value uint8) sprite {
		pixels := make([]rgba, width*height)
		for index := range pixels {
			pixels[index] = rgba{r: value, a: 255}
		}
		return sprite{width: width, height: height, pixels: pixels}
	}
	m := newModel([][]sprite{{makeFrame(4, 2, 10)}})
	m.copilotSprites = [][][]sprite{{{makeFrame(2, 4, 20)}}}
	m.kimiSprites = [][][]sprite{{{makeFrame(3, 1, 30)}}}
	m.processGroups = []processGroup{{tool: "Codex"}, {tool: "Copilot"}, {tool: "Kimi"}}
	layout := m.animationLayout(400, 200)
	if layout.character.width == 0 || layout.copilotCharacter.width == 0 || layout.kimiCharacter.width == 0 {
		t.Fatal("provider character missing from animation layout")
	}
	if !(layout.copilotX < layout.characterX && layout.characterX < layout.kimiX) {
		t.Fatalf("character positions = copilot %d, codex %d, kimi %d", layout.copilotX, layout.characterX, layout.kimiX)
	}
	if layout.copilotY != layout.characterY || layout.characterY != layout.kimiY {
		t.Fatalf("character tops = Copilot %d, Codex %d, Kimi %d; want exact alignment", layout.copilotY, layout.characterY, layout.kimiY)
	}
	if layout.copilotBadgeY != layout.sessionBadgeY || layout.sessionBadgeY != layout.kimiBadgeY {
		t.Fatalf("badge tops = Copilot %d, Codex %d, Kimi %d; want exact alignment", layout.copilotBadgeY, layout.sessionBadgeY, layout.kimiBadgeY)
	}
}

func TestProviderSpriteSettingsAreIndependent(t *testing.T) {
	makeFrame := func(value uint8) sprite {
		pixels := make([]rgba, 16)
		for index := range pixels {
			pixels[index] = rgba{r: value, a: 255}
		}
		return sprite{width: 4, height: 4, pixels: pixels}
	}
	m := newModel([][]sprite{{makeFrame(10)}})
	m.copilotSprites = [][][]sprite{{{makeFrame(20)}}, {{makeFrame(21)}}}
	m.kimiSprites = [][][]sprite{{{makeFrame(30)}}, {{makeFrame(31)}}}
	m.activeTab = settingsTab
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	if m.copilotSprite != codexSpriteMage || m.codexSprite != codexSpriteRanger {
		t.Fatalf("provider settings changed unexpectedly: codex=%s copilot=%s", m.codexSprite, m.copilotSprite)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = updated
}

func TestCodexPortraitUsesSelectedSpriteSet(t *testing.T) {
	makeFrame := func(value uint8) sprite {
		frame := sprite{width: 32, height: 32, pixels: make([]rgba, 32*32)}
		for index := range frame.pixels {
			frame.pixels[index] = rgba{r: value, a: 255}
		}
		return frame
	}
	m := newModel([][]sprite{{makeFrame(10)}})
	m.codexSprites = [][][]sprite{
		{{makeFrame(10)}},
		{{makeFrame(20)}},
		{{makeFrame(30)}},
	}
	for choice, want := range []uint8{10, 20, 30} {
		m.codexSprite = codexSpriteChoice(choice)
		portrait := m.codexPortrait()
		if got := portrait.at(portraitBoxSize/2, portraitBoxSize/2).r; got != want {
			t.Fatalf("choice %d portrait red = %d, want %d", choice, got, want)
		}
	}
}

func TestWizardSettingsPortraitUsesHeadshotForEachProvider(t *testing.T) {
	makeFrame := func(value uint8) sprite {
		return sprite{width: 1, height: 1, pixels: []rgba{{r: value, a: 255}}}
	}
	m := newModel([][]sprite{{makeFrame(10)}})
	m.codexSprites = [][][]sprite{{{makeFrame(10)}}, {{makeFrame(20)}}, {{makeFrame(30)}}}
	m.copilotSprites = m.codexSprites
	m.kimiSprites = m.codexSprites
	m.wizardHeadshot = makeFrame(99)

	for provider := 0; provider < 3; provider++ {
		m.settingsCursor = provider
		m.codexSprite = codexSpriteWarrior
		m.copilotSprite = codexSpriteWarrior
		m.kimiSprite = codexSpriteWarrior
		switch provider {
		case 1:
			m.copilotSprite = codexSpriteRanger
		case 2:
			m.kimiSprite = codexSpriteRanger
		default:
			m.codexSprite = codexSpriteRanger
		}
		if got := m.codexPortrait().at(portraitBoxSize/2, portraitBoxSize/2).r; got != 99 {
			t.Fatalf("provider %d portrait red = %d, want headshot red 99", provider, got)
		}
	}
}

func TestBackgroundSettingSelectsAndCancels(t *testing.T) {
	beachDay := sprite{width: 1, height: 1, pixels: []rgba{{g: 200, a: 255}}}
	beachNight := sprite{width: 1, height: 1, pixels: []rgba{{b: 200, a: 255}}}
	m := newModel([][]sprite{{sprite{width: 1, height: 1, pixels: []rgba{{r: 200, a: 255}}}}}).withSceneBackgrounds(beachDay, beachNight)
	m.activeTab = settingsTab
	m.settingsCursor = 3
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	if m.backgroundChoice != backgroundNone || m.sceneBackground.width != 0 {
		t.Fatalf("background selection = %s with %dx%d sprite, want None", m.backgroundChoice, m.sceneBackground.width, m.sceneBackground.height)
	}
	if !strings.Contains(m.View(), "PLAYER SELECTION") || !strings.Contains(m.View(), "BACKGROUND SELECTION") {
		t.Fatalf("settings categories missing:\n%s", m.View())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.backgroundChoice != backgroundBeach || m.sceneBackground.width != 1 {
		t.Fatalf("cancelled background selection = %s with %dx%d sprite, want Beach", m.backgroundChoice, m.sceneBackground.width, m.sceneBackground.height)
	}
	m.settingsCursor = 4
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	if m.backgroundTime != backgroundNight || m.sceneBackground.at(0, 0).b != 200 {
		t.Fatalf("background time = %s, want Night sprite", m.backgroundTime)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.backgroundTime != backgroundDay || m.sceneBackground.at(0, 0).g != 200 {
		t.Fatalf("cancelled time selection = %s, want Day sprite", m.backgroundTime)
	}
}

func TestCharacterSpritesheetsDecodeIntoAnimations(t *testing.T) {
	for _, test := range []struct {
		name                    string
		data                    []byte
		columns, rows           int
		frameWidth, frameHeight int
	}{
		{"warrior", warriorSheetPNG, warriorSheetColumns, warriorSheetRows, 182, 123},
		{"mage", mageSheetPNG, mageSheetColumns, mageSheetRows, 158, 173},
	} {
		animations, err := decodeGridAnimations(test.data, test.columns, test.rows)
		if err != nil {
			t.Fatalf("decode %s: %v", test.name, err)
		}
		if len(animations) != test.rows || len(animations[0]) != test.columns {
			t.Fatalf("%s animation dimensions = %d/%d, want %d/%d", test.name, len(animations), len(animations[0]), test.rows, test.columns)
		}
		if frame := animations[0][0]; frame.width != test.frameWidth || frame.height != test.frameHeight {
			t.Fatalf("%s frame = %dx%d, want %dx%d", test.name, frame.width, frame.height, test.frameWidth, test.frameHeight)
		}
	}
}

func TestRevealEffectSpritesheetDecodesIntoFrames(t *testing.T) {
	animations, err := decodeGridAnimations(revealEffectPNG, effectSheetColumns, effectSheetRows)
	if err != nil {
		t.Fatal(err)
	}
	if len(animations) != 1 || len(animations[0]) != 5 {
		t.Fatalf("reveal effect frames = %#v, want one row with five frames", animations)
	}
	if frame := animations[0][0]; frame.width != 126 || frame.height != 116 {
		t.Fatalf("reveal effect frame = %dx%d, want 126x116", frame.width, frame.height)
	}
}

func TestResizeToFitPreservesAspectRatio(t *testing.T) {
	for _, test := range []struct {
		sourceWidth, sourceHeight int
		maxWidth, maxHeight       int
		wantWidth, wantHeight     int
	}{
		{4, 2, 8, 8, 8, 4},
		{2, 4, 8, 8, 4, 8},
	} {
		source := sprite{width: test.sourceWidth, height: test.sourceHeight, pixels: make([]rgba, test.sourceWidth*test.sourceHeight)}
		got := resizeToFit(source, test.maxWidth, test.maxHeight)
		if got.width != test.wantWidth || got.height != test.wantHeight {
			t.Fatalf("resize %dx%d within %dx%d = %dx%d, want %dx%d", test.sourceWidth, test.sourceHeight, test.maxWidth, test.maxHeight, got.width, got.height, test.wantWidth, test.wantHeight)
		}
	}
}

func TestCoverSpriteFillsViewportWithoutStretching(t *testing.T) {
	source := sprite{width: 4, height: 2, pixels: []rgba{
		{r: 10, a: 255}, {r: 20, a: 255}, {r: 30, a: 255}, {r: 40, a: 255},
		{r: 10, a: 255}, {r: 20, a: 255}, {r: 30, a: 255}, {r: 40, a: 255},
	}}
	covered := coverSprite(source, 4, 4)
	if covered.width != 4 || covered.height != 4 {
		t.Fatalf("covered sprite = %dx%d, want 4x4", covered.width, covered.height)
	}
	if got := covered.at(0, 0).r; got != 20 {
		t.Fatalf("left crop pixel = %d, want source center red 20", got)
	}
	if got := covered.at(3, 0).r; got != 30 {
		t.Fatalf("right crop pixel = %d, want source center red 30", got)
	}
}

func TestAnimationSceneCompositesBackgroundBehindCharacters(t *testing.T) {
	character := sprite{width: 1, height: 1, pixels: []rgba{{r: 220, a: 255}}}
	m := newModel([][]sprite{{character}}).withSceneBackground(sprite{width: 1, height: 1, pixels: []rgba{{g: 200, a: 255}}})
	m.processGroups = []processGroup{{tool: "Codex"}}
	scene := m.animationScene(40, 20)
	if corner := scene.at(0, 0); corner.g <= corner.r {
		t.Fatalf("background wash = %#v, want beach green behind scene", corner)
	}
	layout := m.animationLayout(scene.width, scene.height)
	if got := scene.at(layout.characterX, layout.characterY+layout.character.height-1).r; got != 220 {
		t.Fatalf("character pixel = %d, want foreground red 220", got)
	}
}

func TestAnimationSceneDrawsProviderLabelsBelowCharacters(t *testing.T) {
	character := sprite{width: 1, height: 1, pixels: []rgba{{r: 220, a: 255}}}
	m := newModel([][]sprite{{character}})
	m.processGroups = []processGroup{{tool: "Codex"}}
	scene := m.animationScene(40, 20)
	layout := m.animationLayout(scene.width, scene.height)
	labelY := layout.characterY + layout.character.height + providerLabelGap
	labelWidth := pixelTextWidth("CODEX", providerLabelScale)
	labelX := layout.characterX + layout.character.width/2 - labelWidth/2
	if got := scene.at(labelX, labelY); got != (rgba{r: 248, g: 248, b: 255, a: 255}) {
		t.Fatalf("Codex label pixel = %#v, want white", got)
	}
}

func TestAnimationSceneHidesUnavailableProvidersAndCentersAvailableSprite(t *testing.T) {
	frame := func(red uint8) sprite {
		return sprite{width: 1, height: 1, pixels: []rgba{{r: red, a: 255}}}
	}
	m := newModel([][]sprite{{frame(20)}})
	m.copilotSprites = [][][]sprite{{{frame(80)}}}
	m.kimiSprites = [][][]sprite{{{frame(140)}}}
	m.processGroups = []processGroup{{tool: "Codex"}}

	scene := m.animationScene(40, 20)
	layout := m.animationLayout(scene.width, scene.height)
	if !layout.codexVisible || layout.copilotVisible || layout.kimiVisible {
		t.Fatalf("provider visibility = Codex %t, Copilot %t, Kimi %t", layout.codexVisible, layout.copilotVisible, layout.kimiVisible)
	}
	if got, want := layout.characterX+layout.character.width/2, scene.width/2; got != want {
		t.Fatalf("Codex center = %d, want %d", got, want)
	}
	for _, hidden := range []uint8{80, 140} {
		for _, pixel := range scene.pixels {
			if pixel.r == hidden {
				t.Fatalf("hidden provider pixel %d rendered", hidden)
			}
		}
	}
}

func TestPadSpriteBottomAlignsVisiblePixels(t *testing.T) {
	source := sprite{width: 2, height: 4, pixels: []rgba{
		{a: 255}, {a: 255},
		{}, {},
		{r: 200, a: 255}, {},
		{}, {},
	}}
	got := padSpriteBottom(source, 8)
	if got.height != 8 {
		t.Fatalf("padded height = %d, want 8", got.height)
	}
	if pixel := got.at(0, 7); pixel.r != 200 || pixel.a != 255 {
		t.Fatalf("visible bottom pixel = %#v, want opaque red at y=7", pixel)
	}
}

func TestSpriteOpaqueTopFindsFirstVisibleRow(t *testing.T) {
	source := sprite{width: 2, height: 4, pixels: []rgba{
		{}, {},
		{}, {},
		{r: 200, a: 255}, {},
		{}, {},
	}}
	if got := spriteOpaqueTop(source); got != 2 {
		t.Fatalf("opaque top = %d, want 2", got)
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
		"",
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

func TestFocusTerminalSessionSelectsGhosttyByWorkingDirectory(t *testing.T) {
	var gotTarget string
	app, err := focusTerminalSessionWith(
		"ttys009",
		"/workspace/firekeeper",
		"darwin",
		func(script, target string) (string, error) {
			if !strings.Contains(script, `application id "com.mitchellh.ghostty"`) {
				t.Fatal("Ghostty adapter was not attempted first")
			}
			for _, required := range []string{"select tab terminalTab", "activate window terminalWindow", "focus terminalPane"} {
				if !strings.Contains(script, required) {
					t.Fatalf("Ghostty adapter missing %q", required)
				}
			}
			gotTarget = target
			return "matched", nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if app != "Ghostty" || gotTarget != "/workspace/firekeeper" {
		t.Fatalf("Ghostty switch result = %q, %q", app, gotTarget)
	}
}

func TestFocusTerminalSessionRejectsMissingTTYAndOtherOS(t *testing.T) {
	unusedScript := func(string, string) (string, error) { return "", nil }
	if _, err := focusTerminalSessionWith("", "", "darwin", unusedScript); err == nil {
		t.Fatal("missing TTY accepted")
	}
	if _, err := focusTerminalSessionWith("ttys001", "", "linux", unusedScript); err == nil {
		t.Fatal("non-macOS terminal switch accepted")
	}
}

func TestFocusTerminalSessionSkipsAppsThatAreNotRunning(t *testing.T) {
	calls := 0
	app, err := focusTerminalSessionWith("ttys001", "", "darwin", func(string, string) (string, error) {
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

func TestAnimationEnterStartsSelectedPartyTerminalFocus(t *testing.T) {
	m := testModel()
	m.activeTab = animationTab
	m.processGroups = []processGroup{
		{
			tool:     "Codex",
			root:     processInfo{pid: 101, tty: "ttys001"},
			sessions: []sessionInfo{{cwd: "/workspace/first"}},
		},
		{
			tool:     "Copilot",
			root:     processInfo{pid: 202, tty: "ttys002"},
			sessions: []sessionInfo{{cwd: "/workspace/second"}},
		},
	}
	m.partyCursor = 1

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("Enter did not start terminal focus for selected party session")
	}
	if !strings.Contains(m.terminalStatus, "ttys002") {
		t.Fatalf("switch status = %q", m.terminalStatus)
	}
	if _, status := m.animationFooter(); !strings.Contains(status, "switching to ttys002") {
		t.Fatalf("animation footer status = %q", status)
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

func TestKittyAnimationCompositesSceneAcrossViewport(t *testing.T) {
	frame := sprite{width: 1, height: 1, pixels: []rgba{{r: 255, a: 255}}}
	m := newModelWithConfig([][]sprite{{frame}}, appConfig{
		renderer:      kittyRenderer,
		spriteColumns: 2,
		spriteRows:    1,
	})
	m.width = 8
	m.height = 7

	view := m.viewAnimation(m.height - chromeRows)
	if got, want := strings.Count(view, string(kitty.Placeholder)), m.width*(m.height-chromeRows); got != want {
		t.Fatalf("scene placeholder count = %d, want %d", got, want)
	}

	scene := m.animationScene(m.width, m.height-chromeRows)
	if got, want := scene.width, m.width*animationSourceScale; got != want {
		t.Fatalf("native scene width = %d, want %d", got, want)
	}
	if got, want := scene.height, (m.height-chromeRows)*2*animationSourceScale; got != want {
		t.Fatalf("native scene height = %d, want %d", got, want)
	}
}

func TestSettingsPortraitIsSquareAndStatic(t *testing.T) {
	frame := sprite{width: 32, height: 32, pixels: make([]rgba, 32*32)}
	for index := range frame.pixels {
		frame.pixels[index] = rgba{r: 200, g: 40, b: 20, a: 255}
	}
	m := newModel([][]sprite{{frame}, {sprite{width: 32, height: 32, pixels: make([]rgba, 32*32)}}})
	portrait := m.codexPortrait()
	if portrait.width != portraitBoxSize || portrait.height != portraitBoxSize {
		t.Fatalf("portrait size = %dx%d, want %dx%d", portrait.width, portrait.height, portraitBoxSize, portraitBoxSize)
	}
	if got := portrait.at(0, 0); got.r != 250 || got.g != 204 || got.a != 255 {
		t.Fatalf("portrait border = %#v, want gold opaque pixel", got)
	}
	if got := portrait.at(portraitBoxSize/2, portraitBoxSize/2); got.r != 200 || got.g != 40 || got.a != 255 {
		t.Fatalf("portrait interior = %#v, want character pixel", got)
	}
	m.frame = 0
	first := m.codexPortrait()
	m.frame = 1
	second := m.codexPortrait()
	if first.at(portraitBoxSize/2, portraitBoxSize/2) != second.at(portraitBoxSize/2, portraitBoxSize/2) {
		t.Fatal("portrait changed with animation frame")
	}
	m.width = 80
	m.height = 24
	m.activeTab = settingsTab
	if got, want := len(m.settingsPortraitLines()), 10; got != want {
		t.Fatalf("settings portrait rows = %d, want %d", got, want)
	}
}

func TestAnimationSceneDrawsCodexSessionBadgeAboveCharacter(t *testing.T) {
	character := sprite{width: 1, height: 1, pixels: []rgba{{r: 200, a: 255}}}
	m := newModelWithConfig([][]sprite{{character}}, appConfig{
		renderer:      blockRenderer,
		spriteColumns: 4,
		spriteRows:    2,
	})
	m.processGroups = []processGroup{
		{tool: "Codex"},
		{tool: "Copilot"},
		{tool: "Codex"},
		{tool: "Kimi"},
	}

	scene := m.animationScene(40, 20)
	layout := m.animationLayout(scene.width, scene.height)
	if layout.sessionBadge.width < 1 || layout.sessionBadge.height < 1 {
		t.Fatal("Codex session badge was not created")
	}
	if got, want := layout.sessionBadgeX+(layout.sessionBadge.width/2), layout.characterX+(layout.character.width/2); got != want {
		t.Fatalf("badge center = %d, want character center %d", got, want)
	}
	if layout.sessionBadgeY+layout.sessionBadge.height > layout.characterY {
		t.Fatalf("badge bottom %d overlaps character top %d", layout.sessionBadgeY+layout.sessionBadge.height, layout.characterY)
	}
	for _, item := range []struct {
		name                   string
		badge                  sprite
		badgeX, badgeY         int
		character              sprite
		characterX, characterY int
	}{
		{"Copilot", layout.copilotBadge, layout.copilotBadgeX, layout.copilotBadgeY, layout.copilotCharacter, layout.copilotX, layout.copilotY},
		{"Kimi", layout.kimiBadge, layout.kimiBadgeX, layout.kimiBadgeY, layout.kimiCharacter, layout.kimiX, layout.kimiY},
	} {
		if item.badge.width < 1 || item.badge.height < 1 {
			t.Fatalf("%s session badge was not created", item.name)
		}
		if got, want := item.badgeX+(item.badge.width/2), item.characterX+(item.character.width/2); got != want {
			t.Fatalf("%s badge center = %d, want character center %d", item.name, got, want)
		}
		if item.badgeY+item.badge.height > item.characterY {
			t.Fatalf("%s badge overlaps character", item.name)
		}
	}

	foundYellow := false
	foundInk := false
	for y := layout.sessionBadgeY; y < layout.sessionBadgeY+layout.sessionBadge.height; y++ {
		for x := layout.sessionBadgeX; x < layout.sessionBadgeX+layout.sessionBadge.width; x++ {
			pixel := scene.at(x, y)
			foundYellow = foundYellow || (pixel.r == questBadgeFill.r && pixel.g == questBadgeFill.g && pixel.b == questBadgeFill.b)
			foundInk = foundInk || (pixel.r == questBadgeInk.r && pixel.g == questBadgeInk.g && pixel.b == questBadgeInk.b)
		}
	}
	if !foundYellow || !foundInk {
		t.Fatalf("badge colors missing: yellow=%v ink=%v", foundYellow, foundInk)
	}
}

func TestProviderSessionBadgeRendersTotalAndActiveCounts(t *testing.T) {
	badge := providerSessionBadge(12, 3)
	if badge.width%2 == 0 {
		t.Fatalf("badge width = %d, want odd width for centering", badge.width)
	}
	inkPixels := 0
	for _, pixel := range badge.pixels {
		if pixel.r == questBadgeInk.r && pixel.g == questBadgeInk.g && pixel.b == questBadgeInk.b && pixel.a == 255 {
			inkPixels++
		}
	}
	if inkPixels < 20 {
		t.Fatalf("multi-digit total label contains only %d ink pixels", inkPixels)
	}
	activePixels := 0
	for _, pixel := range badge.pixels {
		if pixel == activeBadgeFill {
			activePixels++
		}
	}
	if activePixels == 0 {
		t.Fatal("active session tab is missing crimson pixels")
	}
}

func TestProviderActiveGroupCountCountsActiveRuntimesOnce(t *testing.T) {
	groups := []processGroup{
		{tool: "Codex", sessions: []sessionInfo{{state: sessionStateActive}, {state: sessionStateActive}}},
		{tool: "Codex", sessions: []sessionInfo{{state: sessionStateWaiting}}},
		{tool: "Copilot", sessions: []sessionInfo{{state: sessionStateActive}}},
	}
	if got := providerActiveGroupCount(groups, "Codex"); got != 1 {
		t.Fatalf("active Codex groups = %d, want 1", got)
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
