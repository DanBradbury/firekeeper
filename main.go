package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
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
	wizardSheetColumns   = 6
	wizardSheetRows      = 6
	warriorSheetColumns  = 6
	warriorSheetRows     = 5
	mageSheetColumns     = 8
	mageSheetRows        = 6
	defaultFrameDuration = 120 * time.Millisecond
	processPollInterval  = 2 * time.Second
	defaultSpriteColumns = 32
	defaultSpriteRows    = 16
	minimumSpriteColumns = 2
	minimumSpriteRows    = 1
	maximumSpriteColumns = 128
	maximumSpriteRows    = 64
	kittyImageID         = 42
	kittyPlacementID     = 1
	// Forest scene displays its 320px scene at roughly 80 terminal columns.
	// Keep same 4 source pixels per terminal half-cell here so 16x16 ground
	// tiles remain native atlas sprites instead of being enlarged and blurred.
	animationSourceScale      = 4
	animationCharacterFireGap = 2
	portraitBoxSize           = 24
	animationQuestBadgeGap    = 3
	animationQuestBadgeDrop   = 52
	questBadgeMinDiameter     = 21
	questBadgeDigitScale      = 2
	attackAnimationIndex      = 1
	minimumAttackPause        = 2 * time.Second
	maximumAttackPause        = 5 * time.Second
	providerLabelGap          = 3
	providerLabelScale        = 2
	effectSheetColumns        = 5
	effectSheetRows           = 1
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
	usageTab
	settingsTab
	tabCount
)

type usageProvider int

const (
	codexProvider usageProvider = iota
	copilotProvider
	kimiProvider
	usageProviderCount
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

var (
	questBadgeFill    = rgba{r: 250, g: 204, b: 21, a: 255}
	questBadgeOutline = rgba{r: 91, g: 57, b: 13, a: 255}
	questBadgeInk     = rgba{r: 48, g: 31, b: 10, a: 255}
	activeBadgeFill   = rgba{r: 220, g: 48, b: 50, a: 255}
	idleBadgeFill     = rgba{r: 67, g: 82, b: 97, a: 255}
	activeBadgeInk    = rgba{r: 255, g: 242, b: 220, a: 255}
)

//go:embed "assets/wizard_spritesheet.png"
var wizardSheetPNG []byte

//go:embed "assets/wizard_headshot.png"
var wizardHeadshotPNG []byte

//go:embed "assets/warrior_spritesheet.png"
var warriorSheetPNG []byte

//go:embed "assets/warrior_headshot.png"
var warriorHeadshotPNG []byte

//go:embed "assets/mage_spritesheet.png"
var mageSheetPNG []byte

//go:embed "assets/mage_headshot.png"
var mageHeadshotPNG []byte

//go:embed "assets/bg_beach_day.png"
var beachBackgroundPNG []byte

//go:embed "assets/bg_beach_night.png"
var beachNightBackgroundPNG []byte

//go:embed "assets/effect.png"
var revealEffectPNG []byte

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
	repository  string
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
	tty         string
	app         string
	multiplexer string
	warning     string
	err         error
}

type providerAttackState struct {
	attacking    bool
	frame        int
	nextAttackAt time.Time
}

type providerRevealState struct {
	playing bool
	frame   int
}

type model struct {
	width, height           int
	activeTab               tab
	animations              [][]sprite
	codexSprites            [][][]sprite
	copilotSprites          [][][]sprite
	kimiSprites             [][][]sprite
	animation               int
	frame                   int
	menuOpen                bool
	menuCursor              int
	menuPage                rpgMenuPage
	statusCursor            int
	partyCursor             int
	partyScroll             int
	frameDuration           time.Duration
	playing                 bool
	codexSprite             codexSpriteChoice
	copilotSprite           codexSpriteChoice
	kimiSprite              codexSpriteChoice
	settingsCursor          int
	settingsEditing         bool
	settingsEditOriginal    persistentSettings
	settingsPath            string
	settingsSavePending     bool
	settingsErr             string
	wizardHeadshot          sprite
	warriorHeadshot         sprite
	mageHeadshot            sprite
	sceneBackground         sprite
	sceneBackgrounds        [][]sprite
	backgroundChoice        backgroundChoice
	backgroundTime          backgroundTime
	codexAttack             providerAttackState
	copilotAttack           providerAttackState
	kimiAttack              providerAttackState
	revealEffect            []sprite
	codexReveal             providerRevealState
	copilotReveal           providerRevealState
	kimiReveal              providerRevealState
	renderer                spriteRenderer
	spriteColumns           int
	spriteRows              int
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
	codexHistoryOpen        bool
	codexHistoryCursor      int
	usageProvider           usageProvider
	copilotUsage            copilotUsageSnapshot
	copilotUsageErr         string
	copilotUsageLoading     bool
	copilotUsageRefreshedAt time.Time
	copilotHistoryErr       string
	copilotHistoryOpen      bool
	copilotHistoryCursor    int
	kimiUsage               kimiUsageSnapshot
	kimiUsageErr            string
	kimiUsageLoading        bool
	kimiUsageRefreshedAt    time.Time
	kimiHistoryOpen         bool
	kimiHistoryCursor       int
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
		codexSprites:   [][][]sprite{animations},
		copilotSprites: [][][]sprite{animations},
		kimiSprites:    [][][]sprite{animations},
		frameDuration:  defaultFrameDuration,
		playing:        true,
		codexSprite:    codexSpriteRanger,
		copilotSprite:  codexSpriteWarrior,
		kimiSprite:     codexSpriteMage,
		renderer:       config.renderer,
		spriteColumns:  config.spriteColumns,
		spriteRows:     config.spriteRows,
		expandedGroups: make(map[int]bool),
	}
}

func (m model) withCodexSprites(sprites [][][]sprite) model {
	m.codexSprites = sprites
	return m
}

func (m model) withProviderSprites(copilot, kimi [][][]sprite) model {
	m.copilotSprites = copilot
	m.kimiSprites = kimi
	return m
}

func (m model) withCharacterHeadshots(wizard, warrior, mage sprite) model {
	m.wizardHeadshot = wizard
	m.warriorHeadshot = warrior
	m.mageHeadshot = mage
	return m
}

func (m model) withRevealEffect(frames []sprite) model {
	m.revealEffect = frames
	return m
}

func (m model) withSceneBackground(background sprite) model {
	return m.withSceneBackgrounds(background)
}

func (m model) withSceneBackgrounds(beach ...sprite) model {
	m.sceneBackgrounds = [][]sprite{beach, nil}
	m.applySceneBackground()
	return m
}

func (m model) withPersistentSettings(settings persistentSettings, path string) model {
	m.codexSprite = settings.CodexSprite
	m.copilotSprite = settings.CopilotSprite
	m.kimiSprite = settings.KimiSprite
	m.backgroundChoice = settings.Background
	m.backgroundTime = settings.BackgroundTime
	m.settingsPath = path
	m.applyCodexSprite()
	m.applySceneBackground()
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
		if (m.activeTab == processesTab && msg.String() != "s") ||
			(m.activeTab == animationTab && (msg.String() != "enter" || m.menuOpen)) {
			m.terminalStatus = ""
		}
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "tab", "shift+tab":
			m.cancelSettingsEdit()
			m.activeTab = (m.activeTab + 1) % tabCount
			m.menuOpen = false
			if m.activeTab == processesTab {
				return m, refreshProcesses()
			}
			if m.activeTab == usageTab {
				return m, m.beginUsageRefresh()
			}
		case "left", "h":
			if m.activeTab == animationTab {
				if m.menuOpen && m.menuPage == rpgMenuCommands {
					m.moveMenuCursor(-1, 0)
				}
			} else if m.activeTab == usageTab {
				if !m.usageHistoryOpen() {
					return m, m.cycleUsageProvider(-1)
				}
			} else if m.activeTab == settingsTab {
				if m.settingsEditing {
					m.cycleSelectedSetting(-1)
				}
			}
		case "right", "l":
			if m.activeTab == animationTab {
				if m.menuOpen && m.menuPage == rpgMenuCommands {
					m.moveMenuCursor(1, 0)
				}
			} else if m.activeTab == usageTab {
				if !m.usageHistoryOpen() {
					return m, m.cycleUsageProvider(1)
				}
			} else if m.activeTab == settingsTab {
				if m.settingsEditing {
					m.cycleSelectedSetting(1)
				}
			}
		case "up", "k":
			if m.activeTab == animationTab {
				if m.menuOpen && m.menuPage == rpgMenuCommands {
					m.moveMenuCursor(0, -1)
				} else if m.menuOpen && m.menuPage == rpgMenuStatus {
					m.moveStatusCursor(-1)
				} else if !m.menuOpen {
					m.movePartyCursor(-1)
				}
			} else if m.activeTab == processesTab {
				m.processCursor--
				m.clampProcessSelection()
			} else if m.activeTab == settingsTab {
				if !m.settingsEditing {
					m.settingsCursor = max(m.settingsCursor-1, 0)
				}
			} else if m.activeTab == usageTab {
				if m.usageProvider == codexProvider && m.codexHistoryOpen {
					m.codexHistoryCursor = max(m.codexHistoryCursor-1, 0)
				} else if m.usageProvider == copilotProvider && m.copilotHistoryOpen {
					m.copilotHistoryCursor = max(m.copilotHistoryCursor-1, 0)
				} else if m.usageProvider == kimiProvider && m.kimiHistoryOpen {
					m.kimiHistoryCursor = max(m.kimiHistoryCursor-1, 0)
				}
			}
		case "down", "j":
			if m.activeTab == animationTab {
				if m.menuOpen && m.menuPage == rpgMenuCommands {
					m.moveMenuCursor(0, 1)
				} else if m.menuOpen && m.menuPage == rpgMenuStatus {
					m.moveStatusCursor(1)
				} else if !m.menuOpen {
					m.movePartyCursor(1)
				}
			} else if m.activeTab == processesTab {
				m.processCursor++
				m.clampProcessSelection()
			} else if m.activeTab == settingsTab {
				if !m.settingsEditing {
					m.settingsCursor = min(m.settingsCursor+1, settingsItemCount-1)
				}
			} else if m.activeTab == usageTab {
				if m.usageProvider == codexProvider && m.codexHistoryOpen {
					m.codexHistoryCursor = min(m.codexHistoryCursor+1, max(len(m.codexUsage.History)-1, 0))
				} else if m.usageProvider == copilotProvider && m.copilotHistoryOpen {
					m.copilotHistoryCursor = min(m.copilotHistoryCursor+1, max(len(m.copilotUsage.History)-1, 0))
				} else if m.usageProvider == kimiProvider && m.kimiHistoryOpen {
					m.kimiHistoryCursor = min(m.kimiHistoryCursor+1, max(len(m.kimiUsage.History)-1, 0))
				}
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
			} else if m.activeTab == animationTab && !m.menuOpen {
				target, ok := selectedPartyTerminalTarget(m.processGroups, m.partyCursor)
				if ok {
					m.terminalStatus = "switching to " + displayTTY(target.tty) + "…"
					return m, switchToTerminal(target)
				}
			} else if m.activeTab == settingsTab {
				if m.settingsEditing {
					m.settingsEditing = false
					m.settingsSavePending = m.settingsPath != ""
					m.settingsErr = ""
					return m, m.saveSettingsCmd()
				}
				m.settingsEditOriginal = persistentSettings{CodexSprite: m.codexSprite, CopilotSprite: m.copilotSprite, KimiSprite: m.kimiSprite, Background: m.backgroundChoice, BackgroundTime: m.backgroundTime}
				m.settingsEditing = true
			} else if m.activeTab == usageTab {
				if m.usageProvider == codexProvider {
					if m.codexHistoryOpen {
						m.codexHistoryOpen = false
					} else {
						m.codexHistoryOpen = true
						m.codexHistoryCursor = 0
					}
				} else if m.usageProvider == copilotProvider {
					if m.copilotHistoryOpen {
						m.copilotHistoryOpen = false
					} else {
						m.copilotHistoryOpen = true
						m.copilotHistoryCursor = 0
					}
				} else if m.usageProvider == kimiProvider {
					if m.kimiHistoryOpen {
						m.kimiHistoryOpen = false
					} else if len(m.kimiUsage.History) > 0 {
						m.kimiHistoryOpen = true
						m.kimiHistoryCursor = 0
					}
				}
			}
		case "esc", "backspace":
			if m.activeTab == usageTab && m.usageProvider == codexProvider && m.codexHistoryOpen {
				m.codexHistoryOpen = false
			} else if m.activeTab == usageTab && m.usageProvider == copilotProvider && m.copilotHistoryOpen {
				m.copilotHistoryOpen = false
			} else if m.activeTab == usageTab && m.usageProvider == kimiProvider && m.kimiHistoryOpen {
				m.kimiHistoryOpen = false
			} else if m.activeTab == settingsTab {
				m.cancelSettingsEdit()
			} else if m.activeTab == animationTab && m.menuOpen {
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
		m.clampPartySelection()
		m.ensureSelectedProcessVisible()

	case tickMsg:
		if m.playing {
			m.advanceFrame(time.Time(msg))
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
			previousGroups := m.processGroups
			m.processGroups = filterUnidentifiedCodexGroups(
				retainKnownSessionMetadata(m.processGroups, msg.groups),
			)
			m.updateProviderReveals(previousGroups)
			m.restoreProcessSelection(selectedPID)
			m.processErr = ""
			m.processMetadataWarning = msg.metadataWarning
			m.clampStatusCursor()
			m.clampPartySelection()
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
			m.codexHistoryCursor = min(m.codexHistoryCursor, max(len(m.codexUsage.History)-1, 0))
			if len(m.codexUsage.History) == 0 {
				m.codexHistoryOpen = false
			}
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
			m.copilotHistoryCursor = min(m.copilotHistoryCursor, max(len(m.copilotUsage.History)-1, 0))
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

	case settingsSaveResultMsg:
		m.settingsSavePending = false
		if msg.err != nil {
			m.settingsErr = "save failed: " + sanitizeProcessCommand(msg.err.Error())
		} else {
			m.settingsErr = ""
		}
	}

	return m, nil
}

func (m *model) cycleUsageProvider(delta int) tea.Cmd {
	m.codexHistoryOpen = false
	m.copilotHistoryOpen = false
	m.kimiHistoryOpen = false
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
	return m.beginUsageRefresh()
}

func (m model) usageHistoryOpen() bool {
	return (m.usageProvider == codexProvider && m.codexHistoryOpen) ||
		(m.usageProvider == copilotProvider && m.copilotHistoryOpen) ||
		(m.usageProvider == kimiProvider && m.kimiHistoryOpen)
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
	m.codexUsageLoading = true
	return refreshCodexUsage()
}

func (m *model) advanceFrame(now time.Time) {
	if len(m.animations) > 0 && len(m.animations[m.animation]) > 0 {
		m.frame = (m.frame + 1) % len(m.animations[m.animation])
	}
	m.advanceProviderAttack(&m.codexAttack, providerActiveGroupCount(m.processGroups, "Codex") > 0, m.codexSprites, m.codexSprite, now)
	m.advanceProviderAttack(&m.copilotAttack, providerActiveGroupCount(m.processGroups, "Copilot") > 0, m.copilotSprites, m.copilotSprite, now)
	m.advanceProviderAttack(&m.kimiAttack, providerActiveGroupCount(m.processGroups, "Kimi") > 0, m.kimiSprites, m.kimiSprite, now)
	m.advanceProviderReveal(&m.codexReveal)
	m.advanceProviderReveal(&m.copilotReveal)
	m.advanceProviderReveal(&m.kimiReveal)
}

func (m *model) updateProviderReveals(previous []processGroup) {
	m.updateProviderReveal(&m.codexReveal, providerGroupCount(previous, "Codex") > 0, providerGroupCount(m.processGroups, "Codex") > 0)
	m.updateProviderReveal(&m.copilotReveal, providerGroupCount(previous, "Copilot") > 0, providerGroupCount(m.processGroups, "Copilot") > 0)
	m.updateProviderReveal(&m.kimiReveal, providerGroupCount(previous, "Kimi") > 0, providerGroupCount(m.processGroups, "Kimi") > 0)
}

func (m *model) updateProviderReveal(state *providerRevealState, wasAvailable, available bool) {
	if !available {
		*state = providerRevealState{}
		return
	}
	if !wasAvailable && len(m.revealEffect) > 0 {
		*state = providerRevealState{playing: true}
	}
}

func (m *model) advanceProviderReveal(state *providerRevealState) {
	if !state.playing {
		return
	}
	if state.frame+1 >= len(m.revealEffect) {
		*state = providerRevealState{}
		return
	}
	state.frame++
}

func (m *model) advanceProviderAttack(state *providerAttackState, active bool, choices [][][]sprite, choice codexSpriteChoice, now time.Time) {
	if !active {
		*state = providerAttackState{}
		return
	}
	if !state.attacking {
		if state.nextAttackAt.IsZero() || !now.Before(state.nextAttackAt) {
			state.attacking = true
			state.frame = 0
		}
		return
	}
	frames := providerAnimationFrames(choices, choice, attackAnimationIndex)
	if len(frames) == 0 || state.frame+1 >= len(frames) {
		state.attacking = false
		state.frame = 0
		state.nextAttackAt = now.Add(randomAttackPause())
		return
	}
	state.frame++
}

func randomAttackPause() time.Duration {
	seconds := int(minimumAttackPause/time.Second) + rand.IntN(int(maximumAttackPause/time.Second-minimumAttackPause/time.Second)+1)
	return time.Duration(seconds) * time.Second
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
	} else if m.activeTab == usageTab {
		content = m.viewUsage(contentRows)
		help, status = m.usageFooter()
	} else {
		content = m.viewSettings(contentRows)
		help, status = m.settingsFooter()
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
		return "  [ Animation ]   Processes    Usage    •    Tab switch"
	case processesTab:
		return "    Animation    [ Processes ]  Usage    Settings    •    Tab switch"
	case usageTab:
		return "    Animation      Processes  [ Usage ]  Settings    •    Tab switch"
	default:
		return "    Animation      Processes    Usage  [ Settings ]  •    Tab switch"
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
	return providerGroupCount(groups, "Codex")
}

func providerGroupCount(groups []processGroup, provider string) int {
	count := 0
	for _, group := range groups {
		if group.tool == provider {
			count++
		}
	}
	return count
}

func providerActiveGroupCount(groups []processGroup, provider string) int {
	count := 0
	for _, group := range groups {
		if group.tool != provider {
			continue
		}
		for _, session := range group.sessions {
			if session.state == sessionStateActive {
				count++
				break
			}
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
	scene := m.animationScene(m.width, contentRows).resize(m.width, pixelHeight)
	canvas := newCanvas(m.width, pixelHeight, background)
	canvas.drawSprite(0, 0, scene)
	return canvas.render()
}

func (m model) viewKittyAnimation(contentRows int) string {
	lines := make([]string, contentRows)
	for row := range lines {
		lines[row] = strings.Repeat(" ", m.width)
	}

	if contentRows < 1 {
		return strings.Join(lines, "\n")
	}
	scene := m.animationScene(m.width, contentRows)
	upload, err := encodeKittySprite(scene, m.width, contentRows)
	if err != nil {
		return m.viewBlockAnimation(contentRows)
	}
	for row := 0; row < contentRows; row++ {
		prefix := ""
		if row == 0 {
			prefix += upload
		}
		lines[row] = prefix + kittySpritePlaceholderRow(kittyImageID, kittyPlacementID, row, m.width)
	}
	return strings.Join(lines, "\n")
}

func (m model) animationScene(columns, rows int) sprite {
	width := max(columns, 1) * animationSourceScale
	height := max(rows, 1) * 2 * animationSourceScale
	canvas := newCanvas(width, height, background)
	if m.sceneBackground.width > 0 && m.sceneBackground.height > 0 {
		canvas.drawSprite(0, 0, coverSprite(m.sceneBackground, width, height))
		canvas.wash(rgba{r: 6, g: 18, b: 34, a: 48})
	}

	layout := m.animationLayout(width, height)
	if layout.copilotVisible {
		if layout.copilotRevealing {
			canvas.drawSprite(layout.copilotEffectX, layout.copilotEffectY, layout.copilotEffect)
		} else {
			canvas.drawCharacterShadow(layout.copilotX+layout.copilotCharacter.width/2, layout.copilotY+layout.copilotCharacter.height-2, layout.copilotCharacter.width)
			canvas.drawSprite(layout.copilotX, layout.copilotY, layout.copilotCharacter)
			canvas.drawSprite(layout.copilotBadgeX, layout.copilotBadgeY, layout.copilotBadge)
			canvas.drawPixelTextCentered(layout.copilotX+layout.copilotCharacter.width/2, layout.copilotY+layout.copilotCharacter.height+providerLabelGap, "COPILOT", providerLabelScale, rgb{r: 248, g: 248, b: 255})
		}
	}
	if layout.codexVisible {
		if layout.codexRevealing {
			canvas.drawSprite(layout.characterEffectX, layout.characterEffectY, layout.characterEffect)
		} else {
			canvas.drawCharacterShadow(layout.characterX+layout.character.width/2, layout.characterY+layout.character.height-2, layout.character.width)
			canvas.drawSprite(layout.characterX, layout.characterY, layout.character)
			canvas.drawSprite(layout.sessionBadgeX, layout.sessionBadgeY, layout.sessionBadge)
			canvas.drawPixelTextCentered(layout.characterX+layout.character.width/2, layout.characterY+layout.character.height+providerLabelGap, "CODEX", providerLabelScale, rgb{r: 248, g: 248, b: 255})
		}
	}
	if layout.kimiVisible {
		if layout.kimiRevealing {
			canvas.drawSprite(layout.kimiEffectX, layout.kimiEffectY, layout.kimiEffect)
		} else {
			canvas.drawCharacterShadow(layout.kimiX+layout.kimiCharacter.width/2, layout.kimiY+layout.kimiCharacter.height-2, layout.kimiCharacter.width)
			canvas.drawSprite(layout.kimiX, layout.kimiY, layout.kimiCharacter)
			canvas.drawSprite(layout.kimiBadgeX, layout.kimiBadgeY, layout.kimiBadge)
			canvas.drawPixelTextCentered(layout.kimiX+layout.kimiCharacter.width/2, layout.kimiY+layout.kimiCharacter.height+providerLabelGap, "KIMI", providerLabelScale, rgb{r: 248, g: 248, b: 255})
		}
	}
	m.drawAnimationPartySidebar(&canvas)
	return canvas.sprite()
}

type animationLayout struct {
	character              sprite
	characterX, characterY int
	codexVisible           bool
	characterEffect        sprite
	characterEffectX       int
	characterEffectY       int
	codexRevealing         bool
	copilotCharacter       sprite
	copilotX, copilotY     int
	copilotVisible         bool
	copilotEffect          sprite
	copilotEffectX         int
	copilotEffectY         int
	copilotRevealing       bool
	kimiCharacter          sprite
	kimiX, kimiY           int
	kimiVisible            bool
	kimiEffect             sprite
	kimiEffectX            int
	kimiEffectY            int
	kimiRevealing          bool
	sessionBadge           sprite
	sessionBadgeX          int
	sessionBadgeY          int
	copilotBadge           sprite
	copilotBadgeX          int
	copilotBadgeY          int
	kimiBadge              sprite
	kimiBadgeX             int
	kimiBadgeY             int
}

func (m model) animationLayout(width, height int) animationLayout {
	frameWidth := min(m.spriteColumns, max(width/animationSourceScale, 1)) * animationSourceScale
	frameHeight := min(m.spriteRows, max(height/(2*animationSourceScale), 1)) * 2 * animationSourceScale
	frame := padSpriteBottom(resizeToFit(m.currentProviderFrame(m.codexSprites, m.codexSprite, m.codexAttack), frameWidth, frameHeight), frameHeight)
	copilot := padSpriteBottom(resizeToFit(m.currentProviderFrame(m.copilotSprites, m.copilotSprite, m.copilotAttack), frameWidth, frameHeight), frameHeight)
	kimi := padSpriteBottom(resizeToFit(m.currentProviderFrame(m.kimiSprites, m.kimiSprite, m.kimiAttack), frameWidth, frameHeight), frameHeight)
	layout := animationLayout{
		character:        frame,
		copilotCharacter: copilot,
		kimiCharacter:    kimi,
		codexVisible:     providerGroupCount(m.processGroups, "Codex") > 0,
		copilotVisible:   providerGroupCount(m.processGroups, "Copilot") > 0,
		kimiVisible:      providerGroupCount(m.processGroups, "Kimi") > 0,
	}
	gap := animationCharacterFireGap * animationSourceScale
	totalWidth := 0
	visibleCount := 0
	for _, item := range []struct {
		visible bool
		width   int
	}{
		{layout.copilotVisible, copilot.width},
		{layout.codexVisible, frame.width},
		{layout.kimiVisible, kimi.width},
	} {
		if !item.visible {
			continue
		}
		if visibleCount > 0 {
			totalWidth += gap
		}
		totalWidth += item.width
		visibleCount++
	}
	startX := (width - totalWidth) / 2
	nextX := startX
	if layout.copilotVisible {
		layout.copilotX = nextX
		nextX += copilot.width + gap
	}
	if layout.codexVisible {
		layout.characterX = nextX
		nextX += frame.width + gap
	}
	if layout.kimiVisible {
		layout.kimiX = nextX
	}
	layout.copilotY = (height - copilot.height) / 2
	layout.characterY = (height - frame.height) / 2
	layout.kimiY = (height - kimi.height) / 2
	layout.codexRevealing = m.codexReveal.playing
	layout.copilotRevealing = m.copilotReveal.playing
	layout.kimiRevealing = m.kimiReveal.playing
	layout.characterEffect = m.providerRevealFrame(m.codexReveal, frameWidth, frameHeight)
	layout.copilotEffect = m.providerRevealFrame(m.copilotReveal, frameWidth, frameHeight)
	layout.kimiEffect = m.providerRevealFrame(m.kimiReveal, frameWidth, frameHeight)
	layout.characterEffectX = layout.characterX + (layout.character.width-layout.characterEffect.width)/2
	layout.characterEffectY = layout.characterY + layout.character.height - layout.characterEffect.height
	layout.copilotEffectX = layout.copilotX + (layout.copilotCharacter.width-layout.copilotEffect.width)/2
	layout.copilotEffectY = layout.copilotY + layout.copilotCharacter.height - layout.copilotEffect.height
	layout.kimiEffectX = layout.kimiX + (layout.kimiCharacter.width-layout.kimiEffect.width)/2
	layout.kimiEffectY = layout.kimiY + layout.kimiCharacter.height - layout.kimiEffect.height
	return m.withSessionBadge(layout)
}

func (m model) providerRevealFrame(state providerRevealState, maxWidth, maxHeight int) sprite {
	if !state.playing || state.frame < 0 || state.frame >= len(m.revealEffect) {
		return sprite{}
	}
	return resizeToFit(m.revealEffect[state.frame], maxWidth, maxHeight)
}

func (m model) currentProviderFrame(choices [][][]sprite, choice codexSpriteChoice, attack providerAttackState) sprite {
	if attack.attacking {
		frames := providerAnimationFrames(choices, choice, attackAnimationIndex)
		if len(frames) > 0 {
			return frames[min(attack.frame, len(frames)-1)]
		}
	}
	if int(choice) >= len(choices) || len(choices[choice]) == 0 {
		return m.currentFrame()
	}
	animations := choices[choice]
	if !attack.nextAttackAt.IsZero() {
		if len(animations[0]) > 0 {
			return animations[0][m.frame%len(animations[0])]
		}
	}
	animation := min(m.animation, len(animations)-1)
	if len(animations[animation]) == 0 {
		return m.currentFrame()
	}
	frame := min(m.frame, len(animations[animation])-1)
	return animations[animation][frame]
}

func providerAnimationFrames(choices [][][]sprite, choice codexSpriteChoice, animation int) []sprite {
	if int(choice) >= len(choices) || animation < 0 || animation >= len(choices[choice]) {
		return nil
	}
	return choices[choice][animation]
}

func (m model) withSessionBadge(layout animationLayout) animationLayout {
	badgeDrop := animationQuestBadgeDrop
	for _, item := range []struct {
		visible   bool
		character sprite
	}{
		{layout.codexVisible, layout.character},
		{layout.copilotVisible, layout.copilotCharacter},
		{layout.kimiVisible, layout.kimiCharacter},
	} {
		if item.visible {
			badgeDrop = min(badgeDrop, spriteOpaqueTop(item.character))
		}
	}
	if layout.codexVisible {
		layout.sessionBadge = providerSessionBadge(providerGroupCount(m.processGroups, "Codex"), providerActiveGroupCount(m.processGroups, "Codex"))
		layout.sessionBadgeX = layout.characterX + (layout.character.width-layout.sessionBadge.width)/2
		layout.sessionBadgeY = layout.characterY - layout.sessionBadge.height - animationQuestBadgeGap + badgeDrop
	}
	if layout.copilotVisible {
		layout.copilotBadge = providerSessionBadge(providerGroupCount(m.processGroups, "Copilot"), providerActiveGroupCount(m.processGroups, "Copilot"))
		layout.copilotBadgeX = layout.copilotX + (layout.copilotCharacter.width-layout.copilotBadge.width)/2
		layout.copilotBadgeY = layout.copilotY - layout.copilotBadge.height - animationQuestBadgeGap + badgeDrop
	}
	if layout.kimiVisible {
		layout.kimiBadge = providerSessionBadge(providerGroupCount(m.processGroups, "Kimi"), providerActiveGroupCount(m.processGroups, "Kimi"))
		layout.kimiBadgeX = layout.kimiX + (layout.kimiCharacter.width-layout.kimiBadge.width)/2
		layout.kimiBadgeY = layout.kimiY - layout.kimiBadge.height - animationQuestBadgeGap + badgeDrop
	}
	return layout
}

var questBadgeDigits = [10][5]string{
	{"111", "101", "101", "101", "111"},
	{"010", "110", "010", "010", "111"},
	{"111", "001", "111", "100", "111"},
	{"111", "001", "111", "001", "111"},
	{"101", "101", "111", "001", "001"},
	{"111", "100", "111", "001", "111"},
	{"111", "100", "111", "101", "111"},
	{"111", "001", "010", "010", "010"},
	{"111", "101", "111", "101", "111"},
	{"111", "101", "111", "001", "111"},
}

var providerLabelGlyphs = map[rune][5]string{
	'C': {"111", "100", "100", "100", "111"},
	'D': {"110", "101", "101", "101", "110"},
	'E': {"111", "100", "110", "100", "111"},
	'I': {"111", "010", "010", "010", "111"},
	'K': {"101", "101", "110", "101", "101"},
	'L': {"100", "100", "100", "100", "111"},
	'M': {"101", "111", "111", "101", "101"},
	'O': {"111", "101", "101", "101", "111"},
	'P': {"110", "101", "110", "100", "100"},
	'T': {"111", "010", "010", "010", "010"},
	'X': {"101", "101", "010", "101", "101"},
}

func providerSessionBadge(total, active int) sprite {
	totalLabel := strconv.Itoa(max(total, 0))
	activeLabel := strconv.Itoa(max(active, 0))
	totalTextWidth := badgeTextWidth(totalLabel)
	activeTextWidth := badgeTextWidth(activeLabel)
	diameter := max(questBadgeMinDiameter, totalTextWidth+8)
	if diameter%2 == 0 {
		diameter++
	}
	tabWidth := max(activeTextWidth+11, 17)
	tabHeight := 14
	coinY := tabHeight - 2
	width := max(diameter, tabWidth)

	badge := sprite{
		width:  width,
		height: coinY + diameter + 3,
		pixels: make([]rgba, width*(coinY+diameter+3)),
	}
	coinX := (width - diameter) / 2
	center := coinX + diameter/2
	outerRadius := diameter / 2
	innerRadius := max(outerRadius-2, 0)
	for y := 0; y < diameter; y++ {
		for x := 0; x < diameter; x++ {
			dx, dy := x-diameter/2, y-diameter/2
			distance := dx*dx + dy*dy
			if distance <= innerRadius*innerRadius {
				badge.set(coinX+x, coinY+y, questBadgeFill)
			} else if distance <= outerRadius*outerRadius {
				badge.set(coinX+x, coinY+y, questBadgeOutline)
			}
		}
	}

	tabX := (width - tabWidth) / 2
	tabFill := idleBadgeFill
	if active > 0 {
		tabFill = activeBadgeFill
	}
	for y := 0; y < tabHeight; y++ {
		for x := 0; x < tabWidth; x++ {
			if (y == 0 || y == tabHeight-1) && (x < 2 || x >= tabWidth-2) {
				continue
			}
			if x == 0 || x == tabWidth-1 || y == 0 || y == tabHeight-1 {
				badge.set(tabX+x, y, questBadgeOutline)
			} else {
				badge.set(tabX+x, y, tabFill)
			}
		}
	}
	drawActiveMark(&badge, tabX+3, 4)
	drawBadgeNumber(&badge, activeLabel, tabX+9, 2, activeBadgeInk)
	drawBadgeNumber(&badge, totalLabel, center-totalTextWidth/2, coinY+(diameter-5*questBadgeDigitScale)/2, questBadgeInk)
	return badge
}

func badgeTextWidth(label string) int {
	return (len(label)*3 + max(len(label)-1, 0)) * questBadgeDigitScale
}

func drawBadgeNumber(badge *sprite, label string, x, y int, ink rgba) {
	for index, character := range label {
		digit := questBadgeDigits[character-'0']
		digitX := x + index*4*questBadgeDigitScale
		for row, pattern := range digit {
			for column, value := range pattern {
				if value != '1' {
					continue
				}
				for offsetY := 0; offsetY < questBadgeDigitScale; offsetY++ {
					for offsetX := 0; offsetX < questBadgeDigitScale; offsetX++ {
						badge.set(
							digitX+column*questBadgeDigitScale+offsetX,
							y+row*questBadgeDigitScale+offsetY,
							ink,
						)
					}
				}
			}
		}
	}
}

func drawActiveMark(badge *sprite, x, y int) {
	for offset := 0; offset < 5; offset++ {
		badge.set(x+offset, y+offset, activeBadgeInk)
		badge.set(x+4-offset, y+offset, activeBadgeInk)
	}
	badge.set(x+2, y+5, activeBadgeInk)
}

func (s *sprite) set(x, y int, value rgba) {
	if x < 0 || x >= s.width || y < 0 || y >= s.height {
		return
	}
	s.pixels[y*s.width+x] = value
}

func (m model) animationFooter() (string, string) {
	state := "playing"
	if !m.playing {
		state = "paused"
	}
	fps := float64(time.Second) / float64(m.frameDuration)
	help := "  M menu  •  ↑/↓ party  •  Enter switch  •  [/] size  •  space pause  •  q quit"
	if m.menuOpen {
		menuControl := "arrows navigate  •  Esc/M close menu"
		if m.menuPage == rpgMenuStatus {
			menuControl = "↑/↓ navigate  •  Esc back  •  M close menu"
		}
		help = "  " + menuControl + "  •  q quit"
	}
	status := fmt.Sprintf(
		"  animation %02d/%02d  •  frame %02d/%02d  •  %.1f fps (%d ms)  •  %s %d×%d  •  %s",
		m.animation+1,
		len(m.animations),
		m.frame+1,
		len(m.animations[m.animation]),
		fps,
		m.frameDuration.Milliseconds(),
		m.renderer,
		m.spriteColumns,
		m.spriteRows,
		state,
	)
	if m.terminalStatus != "" {
		status = "  " + m.terminalStatus
	}
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
			message := "no session metadata adapter"
			switch group.tool {
			case "Codex":
				message = "no Codex rollout metadata found"
			case "Copilot":
				message = "no Copilot session metadata found"
			case "Kimi":
				message = "no Kimi session metadata found"
			}
			lines = append(lines, "      session  "+message)
		}
		for _, session := range group.sessions {
			originLabel := "source"
			origin := session.source
			if session.repository != "" {
				originLabel = "repo"
				origin = session.repository
			}
			lines = append(lines,
				"      session  "+session.state.String()+" • "+session.name,
				"      cwd      "+session.cwd,
				fmt.Sprintf(
					"      model    %s  •  %s %s  •  updated %s",
					emptyFallback(session.model, "unknown"),
					originLabel,
					emptyFallback(origin, "unknown"),
					formatSessionTime(session.updatedAt),
				),
				fmt.Sprintf(
					"      id       %s  •  branch %s  •  tokens %d",
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
		if (group.tool == "Codex" || group.tool == "Copilot" || group.tool == "Kimi") && len(group.sessions) > 0 {
			known[group.root.pid] = group
		}
	}
	for index := range refreshed {
		group := &refreshed[index]
		if (group.tool != "Codex" && group.tool != "Copilot" && group.tool != "Kimi") || len(group.sessions) > 0 {
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
		output, err := exec.Command("ps", "-axo", "pid=,ppid=,tty=,etime=,command=").Output()
		if err != nil {
			result.err = fmt.Errorf("run ps: %w", err)
			return result
		}
		result.groups = groupProcesses(parseProcesses(string(output)))
		var metadataWarnings []string
		if err := enrichCodexSessions(result.groups); err != nil {
			metadataWarnings = append(metadataWarnings, "Codex: "+sanitizeProcessCommand(err.Error()))
		}
		if err := enrichCopilotSessions(result.groups); err != nil {
			metadataWarnings = append(metadataWarnings, "Copilot: "+sanitizeProcessCommand(err.Error()))
		}
		if err := enrichKimiSessions(result.groups); err != nil {
			metadataWarnings = append(metadataWarnings, "Kimi: "+sanitizeProcessCommand(err.Error()))
		}
		result.metadataWarning = strings.Join(metadataWarnings, "; ")
		return result
	}
}

type terminalAdapter struct {
	name    string
	script  string
	usesCWD bool
}

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
		name: "Ghostty",
		script: `on run argv
	set targetCWD to item 1 of argv
	if application id "com.mitchellh.ghostty" is not running then return "not-running"
	tell application id "com.mitchellh.ghostty"
		repeat with terminalWindow in windows
			repeat with terminalTab in tabs of terminalWindow
				repeat with terminalPane in terminals of terminalTab
					if working directory of terminalPane is targetCWD then
						select tab terminalTab
						activate window terminalWindow
						focus terminalPane
						return "matched"
					end if
				end repeat
			end repeat
		end repeat
	end tell
	return "not-found"
end run`,
		usesCWD: true,
	},
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
	cwd string,
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
		target := targetTTY
		if adapter.usesCWD {
			target = cwd
			if target == "" {
				continue
			}
		}
		result, err := runScript(adapter.script, target)
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
	return "", fmt.Errorf("no supported terminal session owns %s", targetTTY)
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
		return nil
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
	case strings.Contains(lower, "kimi"):
		return "Kimi"
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

func decodeCodexAnimations(data []byte) ([][]sprite, error) {
	return decodeGridAnimations(data, sheetColumns, sheetRows)
}

func decodeGridAnimations(data []byte, columns, rows int) ([][]sprite, error) {
	sheet, err := decodeSprite(data)
	if err != nil {
		return nil, err
	}
	return sliceSheet(sheet, columns, rows)
}

func decodeWizardAnimations(data []byte) ([][]sprite, error) {
	sheet, err := decodeSprite(data)
	if err != nil {
		return nil, err
	}
	grid, err := sliceSheet(sheet, wizardSheetColumns, wizardSheetRows)
	if err != nil {
		return nil, err
	}
	frames := make([]sprite, 0, wizardSheetColumns*wizardSheetRows)
	for _, row := range grid {
		frames = append(frames, row...)
	}
	return groupWizardFrames(frames)
}

func groupWizardFrames(frames []sprite) ([][]sprite, error) {
	frameCounts := []int{6, 7, 18, 5}
	animations := make([][]sprite, len(frameCounts))
	next := 0
	for index, count := range frameCounts {
		if len(frames)-next < count {
			return nil, fmt.Errorf("group Wizard frames: need %d frames, have %d", next+count, len(frames))
		}
		animations[index] = frames[next : next+count]
		next += count
	}
	if next != len(frames) {
		return nil, fmt.Errorf("group Wizard frames: used %d of %d frames", next, len(frames))
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

func resizeToFit(source sprite, maxWidth, maxHeight int) sprite {
	if source.width < 1 || source.height < 1 || maxWidth < 1 || maxHeight < 1 {
		return sprite{}
	}
	width, height := maxWidth, maxHeight
	if source.width*maxHeight > source.height*maxWidth {
		height = max(1, source.height*maxWidth/source.width)
	} else {
		width = max(1, source.width*maxHeight/source.height)
	}
	return source.resize(width, height)
}

func coverSprite(source sprite, width, height int) sprite {
	if source.width < 1 || source.height < 1 || width < 1 || height < 1 {
		return sprite{}
	}
	scaledWidth, scaledHeight := width, height
	if source.width*height < source.height*width {
		scaledHeight = max(1, (source.height*width+source.width-1)/source.width)
	} else {
		scaledWidth = max(1, (source.width*height+source.height-1)/source.height)
	}
	scaled := source.resize(scaledWidth, scaledHeight)
	return scaled.crop((scaled.width-width)/2, (scaled.height-height)/2, width, height)
}

func padSpriteBottom(source sprite, height int) sprite {
	if source.width < 1 || source.height < 1 || height < source.height {
		return source
	}
	result := sprite{width: source.width, height: height, pixels: make([]rgba, source.width*height)}
	bottom := -1
	for y := source.height - 1; y >= 0 && bottom < 0; y-- {
		for x := 0; x < source.width; x++ {
			if source.at(x, y).a != 0 {
				bottom = y
				break
			}
		}
	}
	if bottom < 0 {
		return result
	}
	result.draw(0, height-1-bottom, source)
	return result
}

func spriteOpaqueTop(source sprite) int {
	for y := 0; y < source.height; y++ {
		for x := 0; x < source.width; x++ {
			if source.at(x, y).a != 0 {
				return y
			}
		}
	}
	return 0
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

func (c *canvas) drawPixelTextCentered(centerX, y int, label string, scale int, ink rgb) {
	if scale < 1 {
		return
	}
	width := pixelTextWidth(label, scale)
	x := centerX - width/2
	for _, character := range label {
		glyph, ok := providerLabelGlyphs[character]
		if !ok {
			x += 4 * scale
			continue
		}
		for row, pattern := range glyph {
			for column, value := range pattern {
				if value != '1' {
					continue
				}
				for offsetY := 0; offsetY < scale; offsetY++ {
					for offsetX := 0; offsetX < scale; offsetX++ {
						c.set(x+column*scale+offsetX, y+row*scale+offsetY, ink)
					}
				}
			}
		}
		x += 4 * scale
	}
}

func pixelTextWidth(label string, scale int) int {
	if len(label) == 0 || scale < 1 {
		return 0
	}
	return (len(label)*4 - 1) * scale
}

func (c *canvas) wash(tint rgba) {
	if tint.a == 0 {
		return
	}
	alpha := int(tint.a)
	inverse := 255 - alpha
	for index, pixel := range c.pixels {
		c.pixels[index] = rgb{
			r: uint8((int(tint.r)*alpha + int(pixel.r)*inverse + 127) / 255),
			g: uint8((int(tint.g)*alpha + int(pixel.g)*inverse + 127) / 255),
			b: uint8((int(tint.b)*alpha + int(pixel.b)*inverse + 127) / 255),
		}
	}
}

func (c *canvas) drawCharacterShadow(centerX, baseline, characterWidth int) {
	width := max(characterWidth*2/3, 10)
	height := max(width/5, 4)
	shadow := sprite{width: width, height: height, pixels: make([]rgba, width*height)}
	centerY := height / 2
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			dx, dy := x-width/2, y-centerY
			if dx*dx*height*height+dy*dy*width*width <= width*width*height*height/4 {
				shadow.set(x, y, rgba{r: 2, g: 8, b: 16, a: 105})
			}
		}
	}
	c.drawSprite(centerX-width/2, baseline-height/2, shadow)
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
	rendererFlag := flag.String("renderer", "auto", "sprite renderer: auto, kitty, or blocks")
	spriteColumnsFlag := flag.Int("sprite-cols", defaultSpriteColumns, "sprite width in terminal columns")
	spriteRowsFlag := flag.Int("sprite-rows", defaultSpriteRows, "sprite height in terminal rows")
	flag.Parse()

	config, err := newAppConfig(*rendererFlag, *spriteColumnsFlag, *spriteRowsFlag, os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: %v\n", err)
		os.Exit(2)
	}
	storedSettings, settingsPath, settingsErr := loadPersistentSettings()
	if settingsErr != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI warning: %v; using default settings\n", settingsErr)
	}
	animations, err := decodeWizardAnimations(wizardSheetPNG)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: load Wizard spritesheet: %v\n", err)
		os.Exit(1)
	}
	wizardHeadshot, err := decodeSprite(wizardHeadshotPNG)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: load Wizard headshot: %v\n", err)
		os.Exit(1)
	}
	warriorHeadshot, err := decodeSprite(warriorHeadshotPNG)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: load Warrior headshot: %v\n", err)
		os.Exit(1)
	}
	mageHeadshot, err := decodeSprite(mageHeadshotPNG)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: load Mage headshot: %v\n", err)
		os.Exit(1)
	}
	beachBackground, err := decodeSprite(beachBackgroundPNG)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: load beach background: %v\n", err)
		os.Exit(1)
	}
	beachNightBackground, err := decodeSprite(beachNightBackgroundPNG)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: load beach night background: %v\n", err)
		os.Exit(1)
	}
	revealEffect, err := decodeGridAnimations(revealEffectPNG, effectSheetColumns, effectSheetRows)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: load reveal effect: %v\n", err)
		os.Exit(1)
	}
	warriorAnimations, err := decodeGridAnimations(warriorSheetPNG, warriorSheetColumns, warriorSheetRows)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: load Warrior spritesheet: %v\n", err)
		os.Exit(1)
	}
	mageAnimations, err := decodeGridAnimations(mageSheetPNG, mageSheetColumns, mageSheetRows)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: load Mage spritesheet: %v\n", err)
		os.Exit(1)
	}
	p := tea.NewProgram(
		newModelWithConfig(animations, config).
			withCodexSprites([][][]sprite{animations, warriorAnimations, mageAnimations}).
			withProviderSprites([][][]sprite{animations, warriorAnimations, mageAnimations}, [][][]sprite{animations, warriorAnimations, mageAnimations}).
			withCharacterHeadshots(wizardHeadshot, warriorHeadshot, mageHeadshot).
			withRevealEffect(revealEffect[0]).
			withSceneBackgrounds(beachBackground, beachNightBackground).
			withPersistentSettings(storedSettings, settingsPath),
		tea.WithAltScreen(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "sprite TUI failed: %v\n", err)
		os.Exit(1)
	}
}
