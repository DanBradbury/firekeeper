package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi/kitty"
)

const (
	chromeRows           = 3
	sheetColumns         = 10
	sheetRows            = 10
	defaultFrameDuration = 120 * time.Millisecond
	rateStep             = 20 * time.Millisecond
	minimumFrameDuration = 40 * time.Millisecond
	maximumFrameDuration = time.Second
	processPollInterval  = 2 * time.Second
	defaultSpriteColumns = 16
	defaultSpriteRows    = 8
	minimumSpriteColumns = 2
	minimumSpriteRows    = 1
	maximumSpriteColumns = 128
	maximumSpriteRows    = 64
	kittyImageID         = 42
	kittyPlacementID     = 1
	// Forest demo displays its 320px scene at roughly 80 terminal columns.
	// Keep same 4 source pixels per terminal half-cell here so 16x16 ground
	// tiles remain native atlas sprites instead of being enlarged and blurred.
	animationSourceScale       = 4
	animationFireWidthIncrease = 2
	animationCharacterFireGap  = 2
)

type spriteRenderer int

const (
	blockRenderer spriteRenderer = iota
	kittyRenderer
)

func (r spriteRenderer) String() string {
	if r == kittyRenderer {
		return "kitty"
	}
	return "blocks"
}

type appConfig struct {
	renderer      spriteRenderer
	spriteColumns int
	spriteRows    int
}

type tab int

const (
	animationTab tab = iota
	processesTab
	codexUsageTab
	tabCount
)

type rpgMenuPage int

const (
	rpgMenuCommands rpgMenuPage = iota
	rpgMenuStatus
	rpgStatusMenuIndex = 3
)

type rgb struct {
	r, g, b uint8
}

type rgba struct {
	r, g, b, a uint8
}

type sprite struct {
	width, height int
	pixels        []rgba
}

var background = rgb{r: 8, g: 12, b: 24}

//go:embed "ranger spritesheet calciumtrice.png"
var rangerSheetPNG []byte

type tickMsg time.Time

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
	id          string
	name        string
	state       sessionState
	cwd         string
	model       string
	source      string
	gitBranch   string
	rolloutPath string
	updatedAt   time.Time
	tokensUsed  int64
}

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
	tty string
	app string
	err error
}

type model struct {
	width, height          int
	activeTab              tab
	animations             [][]sprite
	grass                  sprite
	animation              int
	frame                  int
	fireFrames             []sprite
	fireFrame              int
	menuOpen               bool
	menuCursor             int
	menuPage               rpgMenuPage
	statusCursor           int
	frameDuration          time.Duration
	playing                bool
	renderer               spriteRenderer
	spriteColumns          int
	spriteRows             int
	processGroups          []processGroup
	processCursor          int
	processScroll          int
	expandedGroups         map[int]bool
	processErr             string
	processMetadataWarning string
	terminalStatus         string
	refreshedAt            time.Time
	codexUsage             codexUsageSnapshot
	codexUsageErr          string
	codexUsageLoading      bool
	codexUsageRefreshedAt  time.Time
}

func newModel(animations [][]sprite) model {
	return newModelWithConfig(animations, appConfig{
		renderer:      blockRenderer,
		spriteColumns: defaultSpriteColumns,
		spriteRows:    defaultSpriteRows,
	})
}

func newModelWithConfig(animations [][]sprite, config appConfig) model {
	return model{
		width:          80,
		height:         24,
		animations:     animations,
		frameDuration:  defaultFrameDuration,
		playing:        true,
		renderer:       config.renderer,
		spriteColumns:  config.spriteColumns,
		spriteRows:     config.spriteRows,
		expandedGroups: make(map[int]bool),
	}
}

func newModelWithGrass(animations [][]sprite, config appConfig, grass sprite) model {
	m := newModelWithConfig(animations, config)
	m.grass = grass
	return m
}

func (m model) withFire(frames []sprite) model {
	m.fireFrames = frames
	return m
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		tick(m.frameDuration),
		pollProcesses(),
		refreshProcesses(),
	)
}

func tick(duration time.Duration) tea.Cmd {
	return tea.Tick(duration, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func pollProcesses() tea.Cmd {
	return tea.Tick(processPollInterval, func(t time.Time) tea.Msg { return processPollMsg(t) })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.activeTab == processesTab && msg.String() != "s" {
			m.terminalStatus = ""
		}
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "tab", "shift+tab":
			m.activeTab = (m.activeTab + 1) % tabCount
			m.menuOpen = false
			if m.activeTab == processesTab {
				return m, refreshProcesses()
			}
			if m.activeTab == codexUsageTab {
				m.codexUsageLoading = true
				return m, refreshCodexUsage()
			}
		case "left", "h":
			if m.activeTab == animationTab {
				if m.menuOpen && m.menuPage == rpgMenuCommands {
					m.moveMenuCursor(-1, 0)
				} else if !m.menuOpen {
					m.cycleAnimation(-1)
				}
			}
		case "right", "l":
			if m.activeTab == animationTab {
				if m.menuOpen && m.menuPage == rpgMenuCommands {
					m.moveMenuCursor(1, 0)
				} else if !m.menuOpen {
					m.cycleAnimation(1)
				}
			}
		case "up", "k":
			if m.activeTab == animationTab {
				if m.menuOpen && m.menuPage == rpgMenuCommands {
					m.moveMenuCursor(0, -1)
				} else if m.menuOpen && m.menuPage == rpgMenuStatus {
					m.moveStatusCursor(-1)
				} else if !m.menuOpen {
					m.frameDuration = max(m.frameDuration-rateStep, minimumFrameDuration)
				}
			} else if m.activeTab == processesTab {
				m.processCursor--
				m.clampProcessSelection()
			}
		case "down", "j":
			if m.activeTab == animationTab {
				if m.menuOpen && m.menuPage == rpgMenuCommands {
					m.moveMenuCursor(0, 1)
				} else if m.menuOpen && m.menuPage == rpgMenuStatus {
					m.moveStatusCursor(1)
				} else if !m.menuOpen {
					m.frameDuration = min(m.frameDuration+rateStep, maximumFrameDuration)
				}
			} else if m.activeTab == processesTab {
				m.processCursor++
				m.clampProcessSelection()
			}
		case "pgup":
			if m.activeTab == processesTab {
				m.processCursor -= max(m.processPageSize()/2, 1)
				m.clampProcessSelection()
			}
		case "pgdown":
			if m.activeTab == processesTab {
				m.processCursor += max(m.processPageSize()/2, 1)
				m.clampProcessSelection()
			}
		case "home":
			if m.activeTab == processesTab {
				m.processCursor = 0
				m.clampProcessSelection()
			}
		case "end":
			if m.activeTab == processesTab {
				m.processCursor = len(m.processGroups) - 1
				m.clampProcessSelection()
			}
		case "enter":
			if m.activeTab == processesTab && len(m.processGroups) > 0 {
				rootPID := m.processGroups[m.processCursor].root.pid
				m.expandedGroups[rootPID] = !m.expandedGroups[rootPID]
				m.ensureSelectedProcessVisible()
			} else if m.activeTab == animationTab && m.menuOpen && m.menuPage == rpgMenuCommands && m.menuCursor == rpgStatusMenuIndex {
				m.menuPage = rpgMenuStatus
				m.statusCursor = 0
			}
		case "esc", "backspace":
			if m.activeTab == animationTab && m.menuOpen {
				if m.menuPage == rpgMenuStatus {
					m.menuPage = rpgMenuCommands
				} else {
					m.menuOpen = false
				}
			}
		case "r":
			if m.activeTab == processesTab {
				return m, refreshProcesses()
			}
			if m.activeTab == codexUsageTab {
				m.codexUsageLoading = true
				return m, refreshCodexUsage()
			}
		case "s":
			if m.activeTab == processesTab && len(m.processGroups) > 0 {
				m.terminalStatus = "switching to " + displayTTY(m.processGroups[m.processCursor].root.tty) + "…"
				return m, switchToTerminal(m.processGroups[m.processCursor].root.tty)
			}
		case "m", "M":
			if m.activeTab == animationTab {
				m.menuOpen = !m.menuOpen
				if m.menuOpen {
					m.menuCursor = 0
					m.menuPage = rpgMenuCommands
				}
			}
		case " ":
			if m.activeTab == animationTab {
				m.playing = !m.playing
			}
		case "[", "-":
			if m.activeTab == animationTab {
				m.resizeSprite(-1)
			}
		case "]", "+", "=":
			if m.activeTab == animationTab {
				m.resizeSprite(1)
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ensureSelectedProcessVisible()

	case tickMsg:
		if m.playing {
			m.advanceFrame()
		}
		return m, tick(m.frameDuration)

	case processPollMsg:
		return m, tea.Batch(refreshProcesses(), pollProcesses())

	case processResultMsg:
		m.refreshedAt = msg.refreshed
		if msg.err != nil {
			m.processErr = msg.err.Error()
		} else {
			selectedPID := m.selectedProcessRootPID()
			m.processGroups = retainKnownSessionMetadata(m.processGroups, msg.groups)
			m.restoreProcessSelection(selectedPID)
			m.processErr = ""
			m.processMetadataWarning = msg.metadataWarning
			m.clampStatusCursor()
			m.ensureSelectedProcessVisible()
		}

	case terminalSwitchResultMsg:
		if msg.err != nil {
			m.terminalStatus = "terminal switch failed: " + sanitizeProcessCommand(msg.err.Error())
		} else {
			m.terminalStatus = "switched to " + msg.app + " " + displayTTY(msg.tty)
		}

	case codexUsageResultMsg:
		m.codexUsageLoading = false
		m.codexUsageRefreshedAt = msg.refreshed
		if msg.err != nil {
			m.codexUsageErr = sanitizeProcessCommand(msg.err.Error())
		} else {
			m.codexUsage = msg.snapshot
			m.codexUsageErr = ""
		}
	}

	return m, nil
}

func (m *model) cycleAnimation(delta int) {
	if len(m.animations) == 0 {
		return
	}
	m.animation = (m.animation + delta + len(m.animations)) % len(m.animations)
	m.frame = 0
}

func (m *model) advanceFrame() {
	if len(m.animations) > 0 && len(m.animations[m.animation]) > 0 {
		m.frame = (m.frame + 1) % len(m.animations[m.animation])
	}
	if len(m.fireFrames) > 0 {
		m.fireFrame = (m.fireFrame + 1) % len(m.fireFrames)
	}
}

func (m *model) resizeSprite(delta int) {
	m.spriteColumns = min(
		max(m.spriteColumns+delta*2, minimumSpriteColumns),
		maximumSpriteColumns,
	)
	m.spriteRows = min(
		max(m.spriteRows+delta, minimumSpriteRows),
		maximumSpriteRows,
	)
}

func (m model) currentFrame() sprite {
	if len(m.animations) == 0 || len(m.animations[m.animation]) == 0 {
		return sprite{}
	}
	return m.animations[m.animation][m.frame]
}

func (m model) currentFireFrame() sprite {
	if len(m.fireFrames) == 0 {
		return sprite{}
	}
	return m.fireFrames[m.fireFrame%len(m.fireFrames)]
}

func (m model) View() string {
	if m.width < 20 || m.height < 6 {
		return "Terminal too small (need 20x6)\n"
	}

	contentRows := m.height - chromeRows
	var content, help, status string
	if m.activeTab == animationTab {
		if m.menuOpen {
			content = m.viewAnimationMenu(contentRows)
		} else {
			content = m.viewAnimation(contentRows)
		}
		help, status = m.animationFooter()
	} else if m.activeTab == processesTab {
		content = m.viewProcesses(contentRows)
		help, status = m.processFooter()
	} else {
		content = m.viewCodexUsage(contentRows)
		help, status = m.codexUsageFooter()
	}

	return strings.Join([]string{
		fitLine(m.tabBar(), m.width),
		content,
		fitLine(help, m.width),
		fitLine(status, m.width),
	}, "\n")
}

func (m model) tabBar() string {
	switch m.activeTab {
	case animationTab:
		return "  [ Animation ]   Processes    Codex Usage    •    Tab switch"
	case processesTab:
		return "    Animation    [ Processes ]  Codex Usage    •    Tab switch"
	default:
		return "    Animation      Processes  [ Codex Usage ]  •    Tab switch"
	}
}

func (m model) viewAnimation(contentRows int) string {
	if m.renderer == kittyRenderer {
		return m.viewKittyAnimation(contentRows)
	}
	return m.viewBlockAnimation(contentRows)
}

func (m model) viewAnimationMenu(contentRows int) string {
	scene := strings.Split(m.viewAnimation(contentRows), "\n")
	menuHeight := min(contentRows, 9)
	if m.menuPage == rpgMenuStatus {
		menuHeight = min(contentRows, max(5, activeCodexGroupCount(m.processGroups)+4))
	}
	menu := rpgMenuLines(m.width, menuHeight, m.menuCursor)
	if m.menuPage == rpgMenuStatus {
		menu = rpgStatusMenuLines(m.width, menuHeight, m.processGroups, m.statusCursor)
	}
	overlayTop := contentRows - menuHeight

	lines := make([]string, contentRows)
	copy(lines, scene)
	for row := range menu {
		lines[overlayTop+row] = menu[row]
	}
	return strings.Join(lines, "\n")
}

var rpgMenuItems = []string{"ITEM", "MAGIC", "EQUIP", "STATUS", "FORMATION", "CONFIG"}

func rpgMenuLines(width, height, selected int) []string {
	width = max(width, 2)
	height = max(height, 1)
	lines := make([]string, height)
	interiorWidth := width - 2

	for row := range lines {
		var raw string
		switch row {
		case 0:
			raw = "╔" + strings.Repeat("═", interiorWidth) + "╗"
		case height - 1:
			raw = "╚" + strings.Repeat("═", interiorWidth) + "╝"
		default:
			raw = "║" + strings.Repeat(" ", interiorWidth) + "║"
		}
		lines[row] = styleRPGMenuLine(raw)
	}

	columns := rpgMenuColumns(width)
	itemRows := rpgMenuItemRows(interiorWidth, columns, selected)
	itemTop := 2
	if columns < 3 {
		itemTop = 1
	}
	for index, items := range itemRows {
		row := itemTop + index
		if row >= height-1 {
			break
		}
		lines[row] = styleRPGMenuLine("║" + items + "║")
	}

	separator := height - 4
	if separator > itemTop+len(itemRows) && separator > 0 {
		lines[separator] = styleRPGMenuLine("╠" + strings.Repeat("═", interiorWidth) + "╣")
		lines[separator+1] = styleRPGMenuLine("║" + padRPGMenuText("  TIME  00:00        GIL  0000", interiorWidth) + "║")
	}
	return lines
}

func rpgStatusMenuLines(width, height int, groups []processGroup, selected int) []string {
	width = max(width, 2)
	height = max(height, 1)
	interiorWidth := width - 2
	lines := rpgMenuLines(width, height, -1)
	for row := 1; row < height-1; row++ {
		lines[row] = styleRPGMenuLine("║" + strings.Repeat(" ", interiorWidth) + "║")
	}

	var codexGroups []processGroup
	for _, group := range groups {
		if group.tool == "Codex" {
			codexGroups = append(codexGroups, group)
		}
	}
	if height > 2 {
		title := fmt.Sprintf("  STATUS — CODEX SESSIONS (%d)", len(codexGroups))
		lines[1] = styleRPGMenuLine("║" + padRPGMenuText(title, interiorWidth) + "║")
	}
	if height > 3 {
		lines[2] = styleRPGMenuLine("╠" + strings.Repeat("═", interiorWidth) + "╣")
	}

	availableRows := max(height-4, 0)
	if len(codexGroups) == 0 && availableRows > 0 {
		lines[3] = styleRPGMenuLine("║" + padRPGMenuText("  No Codex sessions", interiorWidth) + "║")
		return lines
	}
	selected = min(max(selected, 0), max(len(codexGroups)-1, 0))
	start := 0
	if selected >= availableRows && availableRows > 0 {
		start = selected - availableRows + 1
	}
	end := min(start+availableRows, len(codexGroups))
	for index := start; index < end; index++ {
		entry := formatRPGCodexEntry(index+1, codexSessionSummary(codexGroups[index]), interiorWidth, index == selected)
		lines[index-start+3] = styleRPGMenuLine("║" + entry + "║")
	}
	return lines
}

func activeCodexGroupCount(groups []processGroup) int {
	count := 0
	for _, group := range groups {
		if group.tool == "Codex" {
			count++
		}
	}
	return count
}

func codexSessionSummary(group processGroup) string {
	if len(group.sessions) > 0 {
		session := group.sessions[0]
		parts := []string{session.state.String()}
		if session.name != "" {
			parts = append(parts, session.name)
		}
		if session.model != "" {
			parts = append(parts, session.model)
		}
		if session.cwd != "" {
			parts = append(parts, session.cwd)
		}
		if len(parts) > 0 {
			return strings.Join(parts, " • ")
		}
	}
	return fmt.Sprintf("UNKNOWN • metadata unavailable • %s • PID %d", displayTTY(group.root.tty), group.root.pid)
}

func formatRPGCodexEntry(index int, summary string, width int, selected bool) string {
	marker := "  "
	if selected {
		marker = "▶ "
	}
	leftPadding := "  "
	contentWidth := max(width-len([]rune(leftPadding+marker)), 0)
	prefix := fmt.Sprintf("Codex%d [", index)
	suffix := "]"
	available := contentWidth - len([]rune(prefix)) - len([]rune(suffix))
	if available < 1 {
		content := padRPGMenuText(truncateRPGText(prefix+suffix, contentWidth), contentWidth)
		return padRPGMenuText(leftPadding+marker+content, width)
	}
	content := padRPGMenuText(prefix+truncateRPGText(summary, available)+suffix, contentWidth)
	return padRPGMenuText(leftPadding+marker+content, width)
}

func truncateRPGText(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width < 1 {
		return ""
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func rpgMenuColumns(width int) int {
	interiorWidth := max(width-2, 0)
	if interiorWidth < 26 {
		return 1
	}
	if interiorWidth < 45 {
		return 2
	}
	return 3
}

func rpgMenuItemRows(width, columns, selected int) []string {
	columns = min(max(columns, 1), len(rpgMenuItems))
	rows := (len(rpgMenuItems) + columns - 1) / columns
	result := make([]string, rows)
	for row := 0; row < rows; row++ {
		var line strings.Builder
		for column := 0; column < columns; column++ {
			index := row*columns + column
			cellWidth := width / columns
			if column == columns-1 {
				cellWidth += width % columns
			}
			value := ""
			if index < len(rpgMenuItems) {
				prefix := "  "
				if index == selected {
					prefix = "▶ "
				}
				value = prefix + rpgMenuItems[index]
			}
			line.WriteString(padRPGMenuText(value, cellWidth))
		}
		result[row] = line.String()
	}
	return result
}

func (m *model) moveMenuCursor(horizontal, vertical int) {
	columns := rpgMenuColumns(m.width)
	rows := (len(rpgMenuItems) + columns - 1) / columns
	row := m.menuCursor / columns
	column := m.menuCursor % columns
	row = (row + vertical + rows) % rows
	column = (column + horizontal + columns) % columns
	m.menuCursor = row*columns + column
}

func (m *model) moveStatusCursor(delta int) {
	count := activeCodexGroupCount(m.processGroups)
	if count < 1 {
		m.statusCursor = 0
		return
	}
	m.statusCursor = (m.statusCursor + delta + count) % count
}

func (m *model) clampStatusCursor() {
	count := activeCodexGroupCount(m.processGroups)
	if count < 1 {
		m.statusCursor = 0
		return
	}
	m.statusCursor = min(max(m.statusCursor, 0), count-1)
}

func padRPGMenuText(value string, width int) string {
	runes := []rune(value)
	if len(runes) > width {
		return string(runes[:width])
	}
	return value + strings.Repeat(" ", width-len(runes))
}

func styleRPGMenuLine(value string) string {
	return "\x1b[1;38;2;248;248;255;48;2;12;28;112m" + value + "\x1b[0m"
}

func (m model) viewBlockAnimation(contentRows int) string {
	pixelHeight := contentRows * 2
	if m.grass.width > 0 && m.grass.height > 0 {
		scene := m.grassAnimationScene(m.width, contentRows).resize(m.width, pixelHeight)
		canvas := newCanvas(m.width, pixelHeight, background)
		canvas.drawSprite(0, 0, scene)
		return canvas.render()
	}

	frame := m.currentFrame().fit(
		min(m.spriteColumns, m.width),
		min(m.spriteRows*2, pixelHeight),
	)
	x := (m.width - frame.width) / 2
	y := (pixelHeight - frame.height) / 2

	canvas := newCanvas(m.width, pixelHeight, background)
	canvas.tileSprite(m.grass)
	canvas.drawSprite(x, y, frame)
	return canvas.render()
}

func (m model) viewKittyAnimation(contentRows int) string {
	if m.grass.width > 0 && m.grass.height > 0 {
		return m.viewKittyGrassAnimation(contentRows)
	}

	lines := make([]string, contentRows)
	for row := range lines {
		lines[row] = strings.Repeat(" ", m.width)
	}

	frame := m.currentFrame()
	if frame.width < 1 || frame.height < 1 || contentRows < 1 {
		return strings.Join(lines, "\n")
	}

	columns := min(max(m.spriteColumns, 1), m.width)
	rows := min(max(m.spriteRows, 1), contentRows)
	upload, err := encodeKittySprite(frame, columns, rows)
	if err != nil {
		return m.viewBlockAnimation(contentRows)
	}

	top := (contentRows - rows) / 2
	left := (m.width - columns) / 2
	for row := 0; row < rows; row++ {
		prefix := strings.Repeat(" ", left)
		if row == 0 {
			prefix += upload
		}
		placeholder := kittySpritePlaceholderRow(kittyImageID, kittyPlacementID, row, columns)
		lines[top+row] = prefix + placeholder + strings.Repeat(" ", max(m.width-left-columns, 0))
	}
	return strings.Join(lines, "\n")
}

func (m model) viewKittyGrassAnimation(contentRows int) string {
	lines := make([]string, contentRows)
	for row := range lines {
		lines[row] = strings.Repeat(" ", m.width)
	}
	if m.width < 1 || contentRows < 1 {
		return strings.Join(lines, "\n")
	}

	scene := m.grassAnimationScene(m.width, contentRows)
	upload, err := encodeKittySprite(scene, m.width, contentRows)
	if err != nil {
		return m.viewBlockAnimation(contentRows)
	}
	for row := 0; row < contentRows; row++ {
		prefix := ""
		if row == 0 {
			prefix = upload
		}
		lines[row] = prefix + kittySpritePlaceholderRow(kittyImageID, kittyPlacementID, row, m.width)
	}
	return strings.Join(lines, "\n")
}

func (m model) grassAnimationScene(columns, rows int) sprite {
	width := max(columns, 1) * animationSourceScale
	height := max(rows, 1) * 2 * animationSourceScale
	canvas := newCanvas(width, height, background)
	canvas.tileSprite(m.grass)

	layout := m.animationLayout(width, height)
	canvas.drawSprite(layout.characterX, layout.characterY, layout.character)
	canvas.drawSprite(layout.fireX, layout.fireY, layout.fire)
	return canvas.sprite()
}

type animationLayout struct {
	character              sprite
	characterX, characterY int
	fire                   sprite
	fireX, fireY           int
}

func (m model) animationLayout(width, height int) animationLayout {
	frameWidth := min(m.spriteColumns, max(width/animationSourceScale, 1)) * animationSourceScale
	frameHeight := min(m.spriteRows, max(height/(2*animationSourceScale), 1)) * 2 * animationSourceScale
	frame := m.currentFrame().resize(frameWidth, frameHeight)
	fire := m.currentFireFrame()
	if fire.width < 1 || fire.height < 1 {
		return animationLayout{
			character:  frame,
			characterX: (width - frame.width) / 2,
			characterY: (height - frame.height) / 2,
		}
	}
	scaledFireWidth := fire.width + animationFireWidthIncrease
	scaledFireHeight := (fire.height*scaledFireWidth + fire.width/2) / fire.width
	fire = fire.resize(scaledFireWidth, scaledFireHeight)

	fireX := (width - fire.width) / 2
	fireY := (height - fire.height) / 2
	return animationLayout{
		character:  frame,
		characterX: fireX - animationCharacterFireGap - frame.width,
		characterY: fireY + fire.height - frame.height,
		fire:       fire,
		fireX:      fireX,
		fireY:      fireY,
	}
}

func (m model) animationFooter() (string, string) {
	state := "playing"
	if !m.playing {
		state = "paused"
	}
	fps := float64(time.Second) / float64(m.frameDuration)
	menuControl := "M menu"
	if m.menuOpen {
		menuControl = "arrows navigate  •  Esc/M close menu"
		if m.menuPage == rpgMenuStatus {
			menuControl = "↑/↓ navigate  •  Esc back  •  M close menu"
		}
	}
	help := "  " + menuControl + "  •  ←/→ animation  •  ↑/↓ rate  •  [/] size  •  space pause  •  q quit"
	status := fmt.Sprintf(
		"  animation %02d/%02d  •  frame %02d/%02d  •  fire %02d/%02d  •  %.1f fps (%d ms)  •  %s %d×%d  •  %s",
		m.animation+1,
		len(m.animations),
		m.frame+1,
		len(m.animations[m.animation]),
		m.fireFrame+1,
		max(len(m.fireFrames), 1),
		fps,
		m.frameDuration.Milliseconds(),
		m.renderer,
		m.spriteColumns,
		m.spriteRows,
		state,
	)
	return help, status
}

func (m model) viewProcesses(contentRows int) string {
	lines := make([]string, 0, contentRows)
	if contentRows < 1 {
		return ""
	}

	lines = append(lines, fitLine("  RUNTIME       ROOT PID TTY       PROCS ELAPSED       SESSION", m.width))
	if contentRows > 1 {
		lines = append(lines, strings.Repeat("─", m.width))
	}

	capacity := m.processPageSize()
	if len(m.processGroups) == 0 && contentRows > 2 {
		message := "  No running Codex, Copilot, or OpenCode processes found."
		if m.refreshedAt.IsZero() {
			message = "  Scanning for Codex, Copilot, and OpenCode processes…"
		}
		lines = append(lines, fitLine(message, m.width))
	} else if capacity > 0 {
		body, _ := m.processBodyLines()
		end := min(m.processScroll+capacity, len(body))
		for _, line := range body[m.processScroll:end] {
			lines = append(lines, fitLine(line, m.width))
		}
	}

	for len(lines) < contentRows {
		lines = append(lines, strings.Repeat(" ", m.width))
	}
	return strings.Join(lines[:contentRows], "\n")
}

func (m model) processFooter() (string, string) {
	help := "  ↑/↓ select  •  Enter expand  •  s switch terminal  •  r refresh  •  q quit"
	if m.processErr != "" {
		return help, "  process scan failed: " + m.processErr
	}
	if m.terminalStatus != "" {
		return help, "  " + m.terminalStatus
	}
	if m.refreshedAt.IsZero() {
		return help, "  scanning processes…"
	}

	processCount := 0
	for _, group := range m.processGroups {
		processCount += len(group.processes)
	}
	if m.processMetadataWarning != "" {
		return help, fmt.Sprintf(
			"  %d runtimes / %d processes  •  session metadata unavailable: %s",
			len(m.processGroups),
			processCount,
			m.processMetadataWarning,
		)
	}
	status := fmt.Sprintf(
		"  %d runtimes / %d processes  •  selected %d/%d  •  refreshed %s",
		len(m.processGroups),
		processCount,
		min(m.processCursor+1, len(m.processGroups)),
		len(m.processGroups),
		m.refreshedAt.Format("15:04:05"),
	)
	return help, status
}

func (m model) processPageSize() int {
	return max(m.height-chromeRows-2, 0)
}

func (m model) processBodyLines() ([]string, []int) {
	var lines []string
	offsets := make([]int, len(m.processGroups))
	for index, group := range m.processGroups {
		offsets[index] = len(lines)
		selected := " "
		if index == m.processCursor {
			selected = ">"
		}
		disclosure := "▸"
		if m.expandedGroups[group.root.pid] {
			disclosure = "▾"
		}

		sessionName := "UNKNOWN • metadata unavailable"
		if len(group.sessions) > 0 {
			name := group.sessions[0].name
			if name == "" {
				name = filepath.Base(group.sessions[0].cwd)
			}
			sessionName = group.sessions[0].state.String() + " • " + name
		}
		lines = append(lines, fmt.Sprintf(
			"%s %s %-10s %8d %-9s %5d %-13s %s",
			selected,
			disclosure,
			group.tool,
			group.root.pid,
			displayTTY(group.root.tty),
			len(group.processes),
			group.root.elapsed,
			sessionName,
		))

		if !m.expandedGroups[group.root.pid] {
			continue
		}
		if len(group.sessions) == 0 {
			lines = append(lines, "      session  no Codex rollout metadata found")
		}
		for _, session := range group.sessions {
			lines = append(lines,
				"      session  "+session.state.String()+" • "+session.name,
				"      cwd      "+session.cwd,
				fmt.Sprintf(
					"      model    %s  •  source %s  •  updated %s",
					emptyFallback(session.model, "unknown"),
					emptyFallback(session.source, "unknown"),
					formatSessionTime(session.updatedAt),
				),
				fmt.Sprintf(
					"      thread   %s  •  branch %s  •  tokens %d",
					session.id,
					emptyFallback(session.gitBranch, "none"),
					session.tokensUsed,
				),
			)
		}
		for processIndex, process := range group.processes {
			branch := "├─"
			if processIndex == len(group.processes)-1 {
				branch = "└─"
			}
			lines = append(lines, fmt.Sprintf(
				"      %s PID %-7d PPID %-7d TTY %-9s %-13s %s",
				branch,
				process.pid,
				process.ppid,
				displayTTY(process.tty),
				process.elapsed,
				process.command,
			))
		}
	}
	return lines, offsets
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

func retainKnownSessionMetadata(previous, refreshed []processGroup) []processGroup {
	known := make(map[int]processGroup, len(previous))
	for _, group := range previous {
		if group.tool == "Codex" && len(group.sessions) > 0 {
			known[group.root.pid] = group
		}
	}
	for index := range refreshed {
		group := &refreshed[index]
		if group.tool != "Codex" || len(group.sessions) > 0 {
			continue
		}
		old, ok := known[group.root.pid]
		if !ok || old.root.tty != group.root.tty || old.root.command != group.root.command {
			continue
		}
		group.sessions = append([]sessionInfo(nil), old.sessions...)
	}
	return refreshed
}

func refreshProcesses() tea.Cmd {
	return func() tea.Msg {
		result := processResultMsg{refreshed: time.Now()}
		output, err := exec.Command("ps", "-axo", "pid=,ppid=,tty=,etime=,command=").Output()
		if err != nil {
			result.err = fmt.Errorf("run ps: %w", err)
			return result
		}
		result.groups = groupProcesses(parseProcesses(string(output)))
		if err := enrichCodexSessions(result.groups); err != nil {
			result.metadataWarning = sanitizeProcessCommand(err.Error())
		}
		return result
	}
}

type terminalAdapter struct {
	name   string
	script string
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
}

func switchToTerminal(tty string) tea.Cmd {
	return func() tea.Msg {
		app, err := focusTerminalSession(tty)
		return terminalSwitchResultMsg{tty: tty, app: app, err: err}
	}
}

func focusTerminalSession(tty string) (string, error) {
	return focusTerminalSessionWith(
		tty,
		runtime.GOOS,
		func(script, targetTTY string) (string, error) {
			output, err := exec.Command("osascript", "-e", script, targetTTY).CombinedOutput()
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
	goos string,
	runScript func(string, string) (string, error),
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
		result, err := runScript(adapter.script, targetTTY)
		if err != nil {
			failures = append(failures, adapter.name+": "+err.Error())
			continue
		}
		switch result {
		case "matched":
			return adapter.name, nil
		case "not-running":
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
	return "", fmt.Errorf("no iTerm2 or Terminal.app session owns %s", targetTTY)
}

func groupProcesses(processes []processInfo) []processGroup {
	byPID := make(map[int]processInfo, len(processes))
	for _, process := range processes {
		byPID[process.pid] = process
	}

	grouped := make(map[int][]processInfo)
	roots := make(map[int]processInfo)
	for _, process := range processes {
		root := process
		seen := map[int]bool{root.pid: true}
		for {
			parent, ok := byPID[root.ppid]
			if !ok || parent.tool != process.tool || seen[parent.pid] {
				break
			}
			seen[parent.pid] = true
			root = parent
		}
		grouped[root.pid] = append(grouped[root.pid], process)
		roots[root.pid] = root
	}

	groups := make([]processGroup, 0, len(grouped))
	for rootPID, members := range grouped {
		sort.Slice(members, func(i, j int) bool { return members[i].pid < members[j].pid })
		groups = append(groups, processGroup{
			tool:      roots[rootPID].tool,
			root:      roots[rootPID],
			processes: members,
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].tool == groups[j].tool {
			return groups[i].root.pid < groups[j].root.pid
		}
		return groups[i].tool < groups[j].tool
	})
	return groups
}

type threadMetadataRow struct {
	ID          string `json:"id"`
	Name        string `json:"display_name"`
	CWD         string `json:"cwd"`
	Model       string `json:"model"`
	Source      string `json:"source"`
	GitBranch   string `json:"git_branch"`
	UpdatedAtMS int64  `json:"updated_at_ms"`
	TokensUsed  int64  `json:"tokens_used"`
}

func enrichCodexSessions(groups []processGroup) error {
	var rootPIDs []int
	for _, group := range groups {
		if group.tool == "Codex" {
			rootPIDs = append(rootPIDs, group.root.pid)
		}
	}
	if len(rootPIDs) == 0 {
		return nil
	}

	rollouts, err := discoverOpenRollouts(rootPIDs)
	if err != nil {
		return err
	}
	threadIDs := make(map[string]bool)
	for _, paths := range rollouts {
		for _, path := range paths {
			if id, ok := threadIDFromRolloutPath(path); ok {
				threadIDs[id] = true
			}
		}
	}
	if len(threadIDs) == 0 {
		return fmt.Errorf("no open Codex rollout files found")
	}

	metadata, err := readThreadMetadata(threadIDs)
	if err != nil {
		return err
	}
	for groupIndex := range groups {
		group := &groups[groupIndex]
		if group.tool != "Codex" {
			continue
		}
		seen := make(map[string]bool)
		for _, rolloutPath := range rollouts[group.root.pid] {
			threadID, ok := threadIDFromRolloutPath(rolloutPath)
			if !ok || seen[threadID] {
				continue
			}
			seen[threadID] = true
			row, ok := metadata[threadID]
			if !ok {
				continue
			}
			name := sanitizeProcessCommand(row.Name)
			if name == "" {
				name = row.ID
			}
			state, _ := readRolloutSessionState(rolloutPath)
			group.sessions = append(group.sessions, sessionInfo{
				id:          row.ID,
				name:        name,
				state:       state,
				cwd:         sanitizeProcessCommand(row.CWD),
				model:       sanitizeProcessCommand(row.Model),
				source:      sanitizeProcessCommand(row.Source),
				gitBranch:   sanitizeProcessCommand(row.GitBranch),
				rolloutPath: rolloutPath,
				updatedAt:   time.UnixMilli(row.UpdatedAtMS),
				tokensUsed:  row.TokensUsed,
			})
		}
		sort.Slice(group.sessions, func(i, j int) bool {
			return group.sessions[i].updatedAt.After(group.sessions[j].updatedAt)
		})
	}
	return nil
}

func discoverOpenRollouts(rootPIDs []int) (map[int][]string, error) {
	pidStrings := make([]string, len(rootPIDs))
	for index, pid := range rootPIDs {
		pidStrings[index] = strconv.Itoa(pid)
	}
	output, err := exec.Command("lsof", "-Fn", "-p", strings.Join(pidStrings, ",")).Output()
	if err != nil && len(output) == 0 {
		return nil, fmt.Errorf("run lsof: %w", err)
	}

	rollouts := make(map[int][]string)
	currentPID := 0
	for _, line := range strings.Split(string(output), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			currentPID, _ = strconv.Atoi(line[1:])
		case 'n':
			path := line[1:]
			if currentPID == 0 || !strings.HasPrefix(filepath.Base(path), "rollout-") || filepath.Ext(path) != ".jsonl" {
				continue
			}
			if _, ok := threadIDFromRolloutPath(path); !ok {
				continue
			}
			rollouts[currentPID] = append(rollouts[currentPID], path)
		}
	}
	return rollouts, nil
}

func threadIDFromRolloutPath(path string) (string, bool) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if len(name) < 36 {
		return "", false
	}
	id := name[len(name)-36:]
	if !validUUID(id) {
		return "", false
	}
	return id, true
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func readThreadMetadata(threadIDs map[string]bool) (map[string]threadMetadataRow, error) {
	ids := make([]string, 0, len(threadIDs))
	for id := range threadIDs {
		if validUUID(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no valid Codex thread IDs found")
	}
	sort.Strings(ids)
	quoted := make([]string, len(ids))
	for index, id := range ids {
		quoted[index] = "'" + id + "'"
	}

	statePath, err := codexStatePath()
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`
		SELECT
			id,
			COALESCE(NULLIF(name, ''), NULLIF(title, ''), NULLIF(preview, ''), id) AS display_name,
			cwd,
			COALESCE(model, '') AS model,
			COALESCE(thread_source, source, '') AS source,
			COALESCE(git_branch, '') AS git_branch,
			COALESCE(updated_at_ms, updated_at * 1000) AS updated_at_ms,
			tokens_used
		FROM threads
		WHERE id IN (%s)
	`, strings.Join(quoted, ","))
	output, err := exec.Command("sqlite3", "-readonly", "-json", statePath, query).Output()
	if err != nil {
		return nil, fmt.Errorf("read Codex state: %w", err)
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return map[string]threadMetadataRow{}, nil
	}

	var rows []threadMetadataRow
	if err := json.Unmarshal(output, &rows); err != nil {
		return nil, fmt.Errorf("decode Codex state: %w", err)
	}
	result := make(map[string]threadMetadataRow, len(rows))
	for _, row := range rows {
		result[row.ID] = row
	}
	return result, nil
}

func codexStatePath() (string, error) {
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		codexHome = filepath.Join(home, ".codex")
	}
	path := filepath.Join(codexHome, "state_5.sqlite")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("find Codex state database: %w", err)
	}
	return path, nil
}

func emptyFallback(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func formatSessionTime(value time.Time) string {
	if value.IsZero() || value.UnixMilli() == 0 {
		return "unknown"
	}
	return value.Format("2006-01-02 15:04:05")
}

func parseProcesses(output string) []processInfo {
	var processes []processInfo
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		if pidErr != nil || ppidErr != nil {
			continue
		}

		command := sanitizeProcessCommand(strings.Join(fields[4:], " "))
		tool := classifyProcess(command)
		if tool == "" {
			continue
		}
		processes = append(processes, processInfo{
			tool:    tool,
			pid:     pid,
			ppid:    ppid,
			tty:     normalizeTTY(fields[2]),
			elapsed: fields[3],
			command: command,
		})
	}

	sort.Slice(processes, func(i, j int) bool {
		if processes[i].tool == processes[j].tool {
			return processes[i].pid < processes[j].pid
		}
		return processes[i].tool < processes[j].tool
	})
	return processes
}

func normalizeTTY(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "/dev/"))
	if value == "" || value == "??" || value == "?" || value == "-" {
		return ""
	}
	return value
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

func classifyProcess(command string) string {
	lower := strings.ToLower(command)
	switch {
	case strings.Contains(lower, "opencode"):
		return "OpenCode"
	case strings.Contains(lower, "copilot"):
		return "Copilot"
	case strings.Contains(lower, "codex"):
		return "Codex"
	default:
		return ""
	}
}

func sanitizeProcessCommand(command string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, command)
}

func newAppConfig(renderer string, columns, rows int, getenv func(string) string) (appConfig, error) {
	if columns < minimumSpriteColumns || columns > maximumSpriteColumns {
		return appConfig{}, fmt.Errorf(
			"sprite columns must be between %d and %d, got %d",
			minimumSpriteColumns,
			maximumSpriteColumns,
			columns,
		)
	}
	if rows < minimumSpriteRows || rows > maximumSpriteRows {
		return appConfig{}, fmt.Errorf(
			"sprite rows must be between %d and %d, got %d",
			minimumSpriteRows,
			maximumSpriteRows,
			rows,
		)
	}

	var mode spriteRenderer
	switch strings.ToLower(renderer) {
	case "auto":
		mode = blockRenderer
		if terminalSupportsKitty(getenv) {
			mode = kittyRenderer
		}
	case "kitty":
		mode = kittyRenderer
	case "blocks":
		mode = blockRenderer
	default:
		return appConfig{}, fmt.Errorf("renderer must be auto, kitty, or blocks, got %q", renderer)
	}

	return appConfig{
		renderer:      mode,
		spriteColumns: columns,
		spriteRows:    rows,
	}, nil
}

func terminalSupportsKitty(getenv func(string) string) bool {
	for _, key := range []string{
		"KITTY_WINDOW_ID",
		"GHOSTTY_RESOURCES_DIR",
		"WEZTERM_EXECUTABLE",
		"KONSOLE_VERSION",
		"WARP_IS_LOCAL_SHELL_SESSION",
	} {
		if getenv(key) != "" {
			return true
		}
	}

	term := strings.ToLower(getenv("TERM"))
	program := strings.ToLower(getenv("TERM_PROGRAM"))
	return strings.Contains(term, "kitty") ||
		strings.Contains(program, "ghostty") ||
		strings.Contains(program, "wezterm") ||
		strings.Contains(program, "iterm")
}

func decodeSprite(data []byte) (sprite, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return sprite{}, fmt.Errorf("decode PNG: %w", err)
	}

	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	result := sprite{
		width:  width,
		height: height,
		pixels: make([]rgba, width*height),
	}

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			pixel := color.NRGBAModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			result.pixels[y*width+x] = rgba{r: pixel.R, g: pixel.G, b: pixel.B, a: pixel.A}
		}
	}

	return result, nil
}

func sliceSheet(sheet sprite, columns, rows int) ([][]sprite, error) {
	if columns < 1 || rows < 1 {
		return nil, fmt.Errorf("slice sheet: grid must be positive, got %dx%d", columns, rows)
	}
	if sheet.width%columns != 0 || sheet.height%rows != 0 {
		return nil, fmt.Errorf(
			"slice sheet: %dx%d image is not divisible by %dx%d grid",
			sheet.width,
			sheet.height,
			columns,
			rows,
		)
	}

	frameWidth := sheet.width / columns
	frameHeight := sheet.height / rows
	animations := make([][]sprite, rows)
	for row := 0; row < rows; row++ {
		animations[row] = make([]sprite, columns)
		for column := 0; column < columns; column++ {
			frame := sprite{
				width:  frameWidth,
				height: frameHeight,
				pixels: make([]rgba, frameWidth*frameHeight),
			}
			for y := 0; y < frameHeight; y++ {
				for x := 0; x < frameWidth; x++ {
					sourceX := column*frameWidth + x
					sourceY := row*frameHeight + y
					frame.pixels[y*frameWidth+x] = sheet.at(sourceX, sourceY)
				}
			}
			animations[row][column] = frame
		}
	}
	return animations, nil
}

func (s sprite) fit(maxWidth, maxHeight int) sprite {
	if s.width == 0 || s.height == 0 || maxWidth < 1 || maxHeight < 1 {
		return sprite{}
	}
	if s.width <= maxWidth && s.height <= maxHeight {
		return s
	}

	width, height := s.width, s.height
	if width > maxWidth {
		height = max(1, height*maxWidth/width)
		width = maxWidth
	}
	if height > maxHeight {
		width = max(1, width*maxHeight/height)
		height = maxHeight
	}

	result := sprite{width: width, height: height, pixels: make([]rgba, width*height)}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			sourceX := x * s.width / width
			sourceY := y * s.height / height
			result.pixels[y*width+x] = s.pixels[sourceY*s.width+sourceX]
		}
	}
	return result
}

func (s sprite) resize(width, height int) sprite {
	if s.width < 1 || s.height < 1 || width < 1 || height < 1 {
		return sprite{}
	}
	result := sprite{width: width, height: height, pixels: make([]rgba, width*height)}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			result.pixels[y*width+x] = s.at(x*s.width/width, y*s.height/height)
		}
	}
	return result
}

func (s sprite) at(x, y int) rgba {
	if x < 0 || x >= s.width || y < 0 || y >= s.height {
		return rgba{}
	}
	return s.pixels[y*s.width+x]
}

func encodeKittySprite(source sprite, columns, rows int) (string, error) {
	frame := image.NewNRGBA(image.Rect(0, 0, source.width, source.height))
	for y := 0; y < source.height; y++ {
		for x := 0; x < source.width; x++ {
			pixel := source.at(x, y)
			frame.SetNRGBA(x, y, color.NRGBA{R: pixel.r, G: pixel.g, B: pixel.b, A: pixel.a})
		}
	}

	var output bytes.Buffer
	options := &kitty.Options{
		Action:           kitty.TransmitAndPut,
		Quite:            2,
		ID:               kittyImageID,
		PlacementID:      kittyPlacementID,
		Format:           kitty.PNG,
		Transmission:     kitty.Direct,
		Chunk:            true,
		Columns:          columns,
		Rows:             rows,
		VirtualPlacement: true,
		DoNotMoveCursor:  true,
	}
	if err := kitty.EncodeGraphics(&output, frame, options); err != nil {
		return "", err
	}
	return output.String(), nil
}

func kittySpritePlaceholderRow(id, placement, row, columns int) string {
	red := (id >> 16) & 0xff
	green := (id >> 8) & 0xff
	blue := id & 0xff
	placementRed := (placement >> 16) & 0xff
	placementGreen := (placement >> 8) & 0xff
	placementBlue := placement & 0xff

	var output strings.Builder
	fmt.Fprintf(
		&output,
		"\x1b[38;2;%d;%d;%d;58;2;%d;%d;%dm",
		red,
		green,
		blue,
		placementRed,
		placementGreen,
		placementBlue,
	)
	for column := 0; column < columns; column++ {
		output.WriteRune(kitty.Placeholder)
		output.WriteRune(kitty.Diacritic(row))
		output.WriteRune(kitty.Diacritic(column))
	}
	output.WriteString("\x1b[39;59m")
	return output.String()
}

type canvas struct {
	width, height int
	pixels        []rgb
}

func newCanvas(width, height int, fill rgb) canvas {
	pixels := make([]rgb, width*height)
	for i := range pixels {
		pixels[i] = fill
	}
	return canvas{width: width, height: height, pixels: pixels}
}

func (c *canvas) set(x, y int, value rgb) {
	if x < 0 || x >= c.width || y < 0 || y >= c.height {
		return
	}
	c.pixels[y*c.width+x] = value
}

func (c *canvas) at(x, y int) rgb {
	if x < 0 || x >= c.width || y < 0 || y >= c.height {
		return background
	}
	return c.pixels[y*c.width+x]
}

func (c *canvas) drawSprite(x, y int, source sprite) {
	for row := 0; row < source.height; row++ {
		for column := 0; column < source.width; column++ {
			pixel := source.at(column, row)
			if pixel.a == 0 {
				continue
			}

			destination := c.at(x+column, y+row)
			alpha := int(pixel.a)
			inverseAlpha := 255 - alpha
			blended := rgb{
				r: uint8((int(pixel.r)*alpha + int(destination.r)*inverseAlpha + 127) / 255),
				g: uint8((int(pixel.g)*alpha + int(destination.g)*inverseAlpha + 127) / 255),
				b: uint8((int(pixel.b)*alpha + int(destination.b)*inverseAlpha + 127) / 255),
			}
			c.set(x+column, y+row, blended)
		}
	}
}

func (c *canvas) tileSprite(tile sprite) {
	if tile.width < 1 || tile.height < 1 {
		return
	}
	for y := 0; y < c.height; y += tile.height {
		for x := 0; x < c.width; x += tile.width {
			c.drawSprite(x, y, tile)
		}
	}
}

func (c canvas) sprite() sprite {
	result := sprite{width: c.width, height: c.height, pixels: make([]rgba, c.width*c.height)}
	for index, pixel := range c.pixels {
		result.pixels[index] = rgba{r: pixel.r, g: pixel.g, b: pixel.b, a: 255}
	}
	return result
}

// render packs two square pixels into each terminal cell. Foreground colors
// upper half-block; background colors lower half.
func (c canvas) render() string {
	var out strings.Builder
	for y := 0; y < c.height; y += 2 {
		for x := 0; x < c.width; x++ {
			top := c.at(x, y)
			bottom := c.at(x, y+1)
			fmt.Fprintf(
				&out,
				"\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀",
				top.r, top.g, top.b,
				bottom.r, bottom.g, bottom.b,
			)
		}
		out.WriteString("\x1b[0m")
		if y+2 < c.height {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func fitLine(s string, width int) string {
	runes := []rune(s)
	if len(runes) > width {
		return string(runes[:width])
	}
	return s + strings.Repeat(" ", width-len(runes))
}

func main() {
	demoFlag := flag.String("demo", "sprites", "demo to run: sprites or forest")
	forestOutputFlag := flag.String("forest-output", "", "write one forest scene PNG and exit")
	rendererFlag := flag.String("renderer", "auto", "sprite renderer: auto, kitty, or blocks")
	spriteColumnsFlag := flag.Int("sprite-cols", defaultSpriteColumns, "sprite width in terminal columns")
	spriteRowsFlag := flag.Int("sprite-rows", defaultSpriteRows, "sprite height in terminal rows")
	flag.Parse()

	config, err := newAppConfig(*rendererFlag, *spriteColumnsFlag, *spriteRowsFlag, os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: %v\n", err)
		os.Exit(2)
	}
	if *demoFlag == "forest" || *forestOutputFlag != "" {
		tileset, err := decodeSprite(forestTilesetPNG)
		if err == nil {
			err = validateForestTileset(tileset)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "forest demo failed: %v\n", err)
			os.Exit(1)
		}
		if *forestOutputFlag != "" {
			output, err := os.Create(*forestOutputFlag)
			if err == nil {
				err = writeForestPreview(output, tileset)
				closeErr := output.Close()
				if err == nil {
					err = closeErr
				}
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "forest demo failed: %v\n", err)
				os.Exit(1)
			}
			return
		}

		p := tea.NewProgram(newForestModel(tileset, config.renderer), tea.WithAltScreen())
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "forest demo failed: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *demoFlag != "sprites" {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: demo must be sprites or forest, got %q\n", *demoFlag)
		os.Exit(2)
	}

	sheet, err := decodeSprite(rangerSheetPNG)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: %v\n", err)
		os.Exit(1)
	}
	animations, err := sliceSheet(sheet, sheetColumns, sheetRows)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: %v\n", err)
		os.Exit(1)
	}
	forestTileset, err := decodeSprite(forestTilesetPNG)
	if err == nil {
		err = validateForestTileset(forestTileset)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: load grass background: %v\n", err)
		os.Exit(1)
	}
	grass := forestTileset.crop(0, 0, forestGroundSize, forestGroundSize)
	fireFrames := make([]sprite, forestFireFrames)
	for frame := range fireFrames {
		fireFrames[frame] = forestTileset.crop(frame*forestFireWidth, forestFireY, forestFireWidth, forestFireHeight)
	}

	p := tea.NewProgram(
		newModelWithGrass(animations, config, grass).withFire(fireFrames),
		tea.WithAltScreen(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: %v\n", err)
		os.Exit(1)
	}
}
