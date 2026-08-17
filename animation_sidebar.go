package main

import (
	"path/filepath"
	"strconv"
	"strings"
)

const (
	partySidebarColumns          = 29
	partySidebarMinimumColumns   = 52
	partySidebarBorder           = 2
	partySidebarPadding          = 3
	partySidebarHeaderHeight     = 15
	partyCardHeight              = 27
	partyCardGap                 = 2
	partyCardPortraitSize        = 21
	partyCardTextGap             = 3
	partyCardTextLineHeight      = 8
	partyCardStatusIndicatorSize = 3
)

var (
	partyPanelBackground = rgb{r: 9, g: 14, b: 27}
	partyPanelBorder     = rgb{r: 197, g: 155, b: 58}
	partyPanelInner      = rgb{r: 62, g: 48, b: 31}
	partyTextBright      = rgb{r: 248, g: 248, b: 255}
	partyTextMuted       = rgb{r: 171, g: 184, b: 202}
	partyCardBorder      = rgb{r: 17, g: 22, b: 35}
	partySelectedBorder  = rgb{r: 255, g: 224, b: 117}
)

type animationPartySession struct {
	provider  string
	directory string
	state     sessionState
}

func animationPartySessions(groups []processGroup) []animationPartySession {
	sessions := make([]animationPartySession, 0, len(groups))
	for _, group := range groups {
		if len(group.sessions) == 0 {
			sessions = append(sessions, animationPartySession{
				provider:  emptyFallback(group.tool, "Provider"),
				directory: "DIR UNKNOWN",
				state:     sessionStateUnknown,
			})
			continue
		}
		for _, session := range group.sessions {
			sessions = append(sessions, animationPartySession{
				provider:  emptyFallback(group.tool, "Provider"),
				directory: animationSessionDirectory(session.cwd),
				state:     session.state,
			})
		}
	}
	return sessions
}

func selectedPartyTerminalTarget(groups []processGroup, selected int) (string, string, bool) {
	if selected < 0 {
		return "", "", false
	}
	for _, group := range groups {
		if len(group.sessions) == 0 {
			if selected == 0 {
				return group.root.tty, groupWorkingDirectory(group), true
			}
			selected--
			continue
		}
		for _, session := range group.sessions {
			if selected == 0 {
				cwd := session.cwd
				if cwd == "" {
					cwd = groupWorkingDirectory(group)
				}
				return group.root.tty, cwd, true
			}
			selected--
		}
	}
	return "", "", false
}

func animationSessionDirectory(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "DIR UNKNOWN"
	}
	cleaned := filepath.Clean(cwd)
	if cleaned == string(filepath.Separator) {
		return cleaned
	}
	name := filepath.Base(cleaned)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "DIR UNKNOWN"
	}
	return name
}

func (m model) drawAnimationPartySidebar(scene *canvas) {
	panelWidth := partySidebarWidth(scene.width)
	if panelWidth == 0 || scene.height < partySidebarHeaderHeight+partyCardHeight {
		return
	}

	panel := newCanvas(panelWidth, scene.height, partyPanelBackground)
	drawPartyFrame(&panel)
	sessions := animationPartySessions(m.processGroups)
	header := "PARTY 0"
	if len(sessions) > 0 {
		displayCursor := min(max(m.partyCursor, 0), len(sessions)-1)
		header = "PARTY " + strconv.Itoa(displayCursor+1) + "/" + strconv.Itoa(len(sessions))
	}
	panel.drawPartyTextCentered(panelWidth/2, 5, header, partyTextBright)

	capacity := partySidebarCapacity(scene.width, scene.height)
	if capacity == 0 {
		return
	}
	if len(sessions) == 0 {
		message := "NO SESSIONS"
		if m.refreshedAt.IsZero() {
			message = "SCANNING"
		} else if m.processErr != "" {
			message = "SCAN ERROR"
		}
		panel.drawPartyTextCentered(panelWidth/2, partySidebarHeaderHeight+9, message, partyTextMuted)
		scene.drawSprite(scene.width-panelWidth, 0, panel.sprite())
		return
	}

	cursor, start := partyViewport(m.partyCursor, m.partyScroll, len(sessions), capacity)
	end := min(start+capacity, len(sessions))
	cardY := partySidebarHeaderHeight
	for index := start; index < end; index++ {
		m.drawPartySessionCard(&panel, cardY, sessions[index], index == cursor)
		cardY += partyCardHeight + partyCardGap
	}

	scene.drawSprite(scene.width-panelWidth, 0, panel.sprite())
}

func partySidebarWidth(sceneWidth int) int {
	minimumWidth := partySidebarMinimumColumns * animationSourceScale
	if sceneWidth < minimumWidth {
		return 0
	}
	return min(partySidebarColumns*animationSourceScale, sceneWidth/2)
}

func partySidebarCapacity(sceneWidth, sceneHeight int) int {
	if partySidebarWidth(sceneWidth) == 0 || sceneHeight < partySidebarHeaderHeight+partyCardHeight {
		return 0
	}
	return max((sceneHeight-partySidebarHeaderHeight+partyCardGap)/(partyCardHeight+partyCardGap), 0)
}

func partyViewport(cursor, scroll, total, capacity int) (int, int) {
	if total < 1 || capacity < 1 {
		return 0, 0
	}
	cursor = min(max(cursor, 0), total-1)
	maxScroll := max(total-capacity, 0)
	scroll = min(max(scroll, 0), maxScroll)
	if cursor < scroll {
		scroll = cursor
	} else if cursor >= scroll+capacity {
		scroll = cursor - capacity + 1
	}
	return cursor, min(max(scroll, 0), maxScroll)
}

func (m *model) movePartyCursor(delta int) {
	count := len(animationPartySessions(m.processGroups))
	if count < 1 {
		m.partyCursor = 0
		m.partyScroll = 0
		return
	}
	m.partyCursor = (m.partyCursor + delta + count) % count
	m.ensurePartySelectionVisible()
}

func (m *model) clampPartySelection() {
	count := len(animationPartySessions(m.processGroups))
	if count < 1 {
		m.partyCursor = 0
		m.partyScroll = 0
		return
	}
	m.partyCursor = min(max(m.partyCursor, 0), count-1)
	m.ensurePartySelectionVisible()
}

func (m *model) ensurePartySelectionVisible() {
	count := len(animationPartySessions(m.processGroups))
	capacity := partySidebarCapacity(
		max(m.width, 1)*animationSourceScale,
		max(m.height-chromeRows, 0)*2*animationSourceScale,
	)
	if count < 1 || capacity < 1 {
		m.partyScroll = 0
		return
	}
	m.partyCursor, m.partyScroll = partyViewport(m.partyCursor, m.partyScroll, count, capacity)
}

func drawPartyFrame(panel *canvas) {
	for offset := 0; offset < partySidebarBorder; offset++ {
		drawPartyRectOutline(panel, offset, offset, panel.width-offset*2, panel.height-offset*2, partyPanelBorder)
	}
	drawPartyRectOutline(
		panel,
		partySidebarBorder,
		partySidebarHeaderHeight-2,
		panel.width-partySidebarBorder*2,
		2,
		partyPanelInner,
	)
}

func (m model) drawPartySessionCard(panel *canvas, y int, session animationPartySession, selected bool) {
	x := partySidebarPadding
	width := panel.width - partySidebarPadding*2
	fill, accent := partySessionColors(session.provider, session.state)
	drawPartyFilledRect(panel, x, y, width, partyCardHeight, fill)
	border := partyCardBorder
	if selected {
		border = partySelectedBorder
	}
	drawPartyRectOutline(panel, x, y, width, partyCardHeight, border)
	drawPartyFilledRect(panel, x+1, y+1, partyCardStatusIndicatorSize, partyCardHeight-2, accent)

	portrait := m.animationProviderPortrait(session.provider)
	if portrait.width > 0 && portrait.height > 0 {
		portrait = portrait.resize(partyCardPortraitSize, partyCardPortraitSize)
		panel.drawSprite(x+partyCardStatusIndicatorSize+2, y+(partyCardHeight-partyCardPortraitSize)/2, portrait)
	} else {
		drawGenericPartyPortrait(panel, x+partyCardStatusIndicatorSize+2, y+(partyCardHeight-partyCardPortraitSize)/2, session.provider)
	}

	textX := x + partyCardStatusIndicatorSize + 2 + partyCardPortraitSize + partyCardTextGap
	textWidth := max(x+width-3-textX, 0)
	maxCharacters := max((textWidth+1)/4, 0)
	panel.drawPartyText(textX, y+3, truncatePartyText(session.provider, maxCharacters), partyTextBright)
	panel.drawPartyText(textX, y+3+partyCardTextLineHeight, truncatePartyText(session.directory, maxCharacters), partyTextMuted)
	panel.drawPartyText(textX, y+3+partyCardTextLineHeight*2, truncatePartyText(session.state.String(), maxCharacters), accent)
}

func partySessionColors(provider string, state sessionState) (rgb, rgb) {
	fill := rgb{r: 51, g: 55, b: 70}
	switch strings.ToUpper(provider) {
	case "CODEX":
		fill = rgb{r: 77, g: 49, b: 92}
	case "COPILOT":
		fill = rgb{r: 31, g: 72, b: 83}
	case "KIMI":
		fill = rgb{r: 89, g: 56, b: 28}
	case "OPENCODE":
		fill = rgb{r: 47, g: 72, b: 49}
	}

	switch state {
	case sessionStateActive:
		return fill, rgb{r: 81, g: 220, b: 118}
	case sessionStateWaiting:
		return fill, rgb{r: 91, g: 176, b: 235}
	case sessionStateNeedsInput:
		return fill, rgb{r: 255, g: 184, b: 61}
	default:
		return fill, rgb{r: 142, g: 151, b: 166}
	}
}

func (m model) animationProviderPortrait(provider string) sprite {
	choice := m.codexSprite
	animations := m.codexSprites
	switch provider {
	case "Copilot":
		choice = m.copilotSprite
		animations = m.copilotSprites
	case "Kimi":
		choice = m.kimiSprite
		animations = m.kimiSprites
	case "Codex":
	default:
		return sprite{}
	}

	var portrait sprite
	switch choice {
	case codexSpriteWarrior:
		portrait = m.warriorHeadshot
	case codexSpriteMage:
		portrait = m.mageHeadshot
	default:
		portrait = m.wizardHeadshot
	}
	if portrait.width > 0 && portrait.height > 0 {
		return m.portraitBox(portrait)
	}
	if int(choice) >= len(animations) || len(animations[choice]) == 0 || len(animations[choice][0]) == 0 {
		return sprite{}
	}
	portrait = animations[choice][0][0]
	if portrait.width >= 24 && portrait.height >= 24 {
		portrait = portrait.crop(8, 0, 16, 16)
	}
	return m.portraitBox(portrait)
}

func drawGenericPartyPortrait(panel *canvas, x, y int, provider string) {
	drawPartyFilledRect(panel, x, y, partyCardPortraitSize, partyCardPortraitSize, rgb{r: 24, g: 24, b: 32})
	drawPartyRectOutline(panel, x, y, partyCardPortraitSize, partyCardPortraitSize, partyPanelBorder)
	initial := "?"
	if runes := []rune(strings.TrimSpace(provider)); len(runes) > 0 {
		initial = string(runes[0])
	}
	panel.drawPartyTextCentered(x+partyCardPortraitSize/2, y+8, initial, partyTextBright)
}

func drawPartyFilledRect(target *canvas, x, y, width, height int, fill rgb) {
	for row := y; row < y+height; row++ {
		for column := x; column < x+width; column++ {
			target.set(column, row, fill)
		}
	}
}

func drawPartyRectOutline(target *canvas, x, y, width, height int, ink rgb) {
	if width < 1 || height < 1 {
		return
	}
	for column := x; column < x+width; column++ {
		target.set(column, y, ink)
		target.set(column, y+height-1, ink)
	}
	for row := y; row < y+height; row++ {
		target.set(x, row, ink)
		target.set(x+width-1, row, ink)
	}
}

func (c *canvas) drawPartyText(x, y int, label string, ink rgb) {
	for _, character := range strings.ToUpper(label) {
		glyph, ok := partyGlyphs[character]
		if !ok {
			glyph = partyGlyphs['?']
		}
		for row, pattern := range glyph {
			for column, value := range pattern {
				if value == '1' {
					c.set(x+column, y+row, ink)
				}
			}
		}
		x += 4
	}
}

func (c *canvas) drawPartyTextCentered(centerX, y int, label string, ink rgb) {
	c.drawPartyText(centerX-partyTextWidth(label)/2, y, label, ink)
}

func partyTextWidth(label string) int {
	length := len([]rune(label))
	if length == 0 {
		return 0
	}
	return length*4 - 1
}

func truncatePartyText(value string, maxCharacters int) string {
	runes := []rune(value)
	if len(runes) <= maxCharacters {
		return value
	}
	if maxCharacters < 1 {
		return ""
	}
	if maxCharacters == 1 {
		return "~"
	}
	return "~" + string(runes[len(runes)-maxCharacters+1:])
}

var partyGlyphs = map[rune][5]string{
	' ': {"000", "000", "000", "000", "000"},
	'A': {"010", "101", "111", "101", "101"},
	'B': {"110", "101", "110", "101", "110"},
	'C': {"011", "100", "100", "100", "011"},
	'D': {"110", "101", "101", "101", "110"},
	'E': {"111", "100", "110", "100", "111"},
	'F': {"111", "100", "110", "100", "100"},
	'G': {"011", "100", "101", "101", "011"},
	'H': {"101", "101", "111", "101", "101"},
	'I': {"111", "010", "010", "010", "111"},
	'J': {"001", "001", "001", "101", "010"},
	'K': {"101", "101", "110", "101", "101"},
	'L': {"100", "100", "100", "100", "111"},
	'M': {"101", "111", "111", "101", "101"},
	'N': {"101", "111", "111", "111", "101"},
	'O': {"010", "101", "101", "101", "010"},
	'P': {"110", "101", "110", "100", "100"},
	'Q': {"010", "101", "101", "111", "011"},
	'R': {"110", "101", "110", "101", "101"},
	'S': {"011", "100", "010", "001", "110"},
	'T': {"111", "010", "010", "010", "010"},
	'U': {"101", "101", "101", "101", "111"},
	'V': {"101", "101", "101", "101", "010"},
	'W': {"101", "101", "111", "111", "101"},
	'X': {"101", "101", "010", "101", "101"},
	'Y': {"101", "101", "010", "010", "010"},
	'Z': {"111", "001", "010", "100", "111"},
	'0': {"111", "101", "101", "101", "111"},
	'1': {"010", "110", "010", "010", "111"},
	'2': {"110", "001", "010", "100", "111"},
	'3': {"110", "001", "010", "001", "110"},
	'4': {"101", "101", "111", "001", "001"},
	'5': {"111", "100", "110", "001", "110"},
	'6': {"011", "100", "110", "101", "010"},
	'7': {"111", "001", "010", "010", "010"},
	'8': {"010", "101", "010", "101", "010"},
	'9': {"010", "101", "011", "001", "110"},
	'-': {"000", "000", "111", "000", "000"},
	'_': {"000", "000", "000", "000", "111"},
	'.': {"000", "000", "000", "000", "010"},
	'/': {"001", "001", "010", "100", "100"},
	':': {"000", "010", "000", "010", "000"},
	'~': {"000", "010", "101", "010", "000"},
	'?': {"110", "001", "010", "000", "010"},
}
