package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type codexSpriteChoice int

const (
	codexSpriteRanger codexSpriteChoice = iota
	codexSpriteWarrior
	codexSpriteCleric
	codexSpriteChoiceCount
)

const settingsItemCount = 1

type persistentSettings struct {
	CodexSprite codexSpriteChoice `json:"codex_sprite"`
}

type settingsSaveResultMsg struct {
	err error
}

func defaultPersistentSettings() persistentSettings {
	return persistentSettings{CodexSprite: codexSpriteRanger}
}

func settingsFilePath() (string, error) {
	configDir := strings.TrimSpace(os.Getenv("FIREKEEPER_CONFIG_DIR"))
	if configDir == "" {
		var err error
		configDir, err = os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("find Firekeeper config directory: %w", err)
		}
	}
	return filepath.Join(configDir, "firekeeper", "settings.json"), nil
}

func loadPersistentSettings() (persistentSettings, string, error) {
	settings := defaultPersistentSettings()
	path, err := settingsFilePath()
	if err != nil {
		return settings, "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return settings, path, nil
	}
	if err != nil {
		return settings, path, fmt.Errorf("read settings: %w", err)
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return defaultPersistentSettings(), path, fmt.Errorf("decode settings: %w", err)
	}
	if settings.CodexSprite < codexSpriteRanger || settings.CodexSprite >= codexSpriteChoiceCount {
		return defaultPersistentSettings(), path, fmt.Errorf("settings contain invalid Codex sprite")
	}
	return settings, path, nil
}

func savePersistentSettings(path string, settings persistentSettings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("create settings file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect settings file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write settings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close settings file: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	return nil
}

func (choice codexSpriteChoice) String() string {
	switch choice {
	case codexSpriteWarrior:
		return "Warrior"
	case codexSpriteCleric:
		return "Cleric"
	default:
		return "Ranger"
	}
}

func (m *model) cycleCodexSprite(delta int) {
	m.codexSprite = (m.codexSprite + codexSpriteChoice(delta) + codexSpriteChoiceCount) % codexSpriteChoiceCount
	m.applyCodexSprite()
}

func (m *model) applyCodexSprite() {
	if int(m.codexSprite) >= len(m.codexSprites) || len(m.codexSprites[m.codexSprite]) == 0 {
		return
	}
	m.animations = m.codexSprites[m.codexSprite]
	if len(m.animations) == 0 {
		m.animation = 0
		m.frame = 0
		return
	}
	m.animation = min(m.animation, len(m.animations)-1)
	if len(m.animations[m.animation]) == 0 {
		m.frame = 0
	} else {
		m.frame = min(m.frame, len(m.animations[m.animation])-1)
	}
}

func (m *model) cancelSettingsEdit() {
	if m.activeTab == settingsTab && m.settingsEditing {
		m.codexSprite = m.settingsEditOriginal
		m.applyCodexSprite()
	}
	m.settingsEditing = false
}

func (m model) saveSettingsCmd() tea.Cmd {
	if m.settingsPath == "" {
		return nil
	}
	settings := persistentSettings{CodexSprite: m.codexSprite}
	path := m.settingsPath
	return func() tea.Msg {
		return settingsSaveResultMsg{err: savePersistentSettings(path, settings)}
	}
}

func (m model) viewSettings(contentRows int) string {
	lines := []string{
		usagePaintLine("  ╭────────────────────────────╮", m.width, usageBrightColor, true, usagePanelColor),
		usagePaintLine("  │ SETTINGS                   │", m.width, usageTextColor, true, usagePanelColor),
		usagePaintLine("  ╰────────────────────────────╯", m.width, usageMutedColor, false, usagePanelColor),
		usageSectionLine("SETTINGS", m.width),
		usagePaintLine("▶ Codex Sprite  "+m.codexSprite.String(), m.width, usageBrightColor, true, usagePanelColor),
	}
	lines = append(lines, m.settingsPortraitLines()...)
	if m.settingsEditing {
		lines = append(lines,
			usagePaintLine("  Editing Codex Sprite • ←/→ choose • Enter save", m.width, usageMutedColor, false, usagePanelColor),
		)
		options := []string{"Ranger", "Warrior", "Cleric"}
		for index, option := range options {
			selected := codexSpriteChoice(index) == m.codexSprite
			marker := "    "
			if selected {
				marker = "  ▶ "
			}
			lines = append(lines, usagePaintLine(marker+option, m.width, usageTextColor, selected, usagePanelColor))
		}
	} else {
		lines = append(lines, usagePaintLine("  ↑/↓ choose setting • Enter edit", m.width, usageMutedColor, false, usagePanelColor))
	}
	return fillUsageLines(lines, contentRows, m.width)
}

func (m model) codexPortrait() sprite {
	animations := m.animations
	if int(m.codexSprite) < len(m.codexSprites) && len(m.codexSprites[m.codexSprite]) > 0 {
		animations = m.codexSprites[m.codexSprite]
	}
	if len(animations) == 0 || len(animations[0]) == 0 {
		return sprite{}
	}
	frame := animations[0][0]
	if frame.width < 1 || frame.height < 1 {
		return sprite{}
	}
	portrait := frame.resize(portraitBoxSize-4, portraitBoxSize-4)
	if frame.width >= 24 && frame.height >= 24 {
		portrait = frame.crop(8, 0, 16, 16).resize(portraitBoxSize-4, portraitBoxSize-4)
	}
	box := sprite{
		width:  portraitBoxSize,
		height: portraitBoxSize,
		pixels: make([]rgba, portraitBoxSize*portraitBoxSize),
	}
	for y := 0; y < box.height; y++ {
		for x := 0; x < box.width; x++ {
			border := x == 0 || y == 0 || x == box.width-1 || y == box.height-1
			if border {
				box.set(x, y, rgba{r: 250, g: 204, b: 21, a: 255})
			} else {
				box.set(x, y, rgba{r: 24, g: 24, b: 32, a: 255})
			}
		}
	}
	box.draw(2, 2, portrait)
	return box
}

func (m model) settingsPortraitLines() []string {
	portrait := m.codexPortrait()
	if portrait.width < 1 {
		return nil
	}
	const columns = 10
	const rows = 10
	if m.renderer == kittyRenderer {
		upload, err := encodeKittySprite(portrait, columns, rows)
		if err != nil {
			return nil
		}
		lines := make([]string, rows)
		for row := range lines {
			prefix := strings.Repeat(" ", max((m.width-columns)/2, 0))
			if row == 0 {
				prefix += upload
			}
			lines[row] = prefix + kittySpritePlaceholderRow(kittyImageID, kittyPlacementID, row, columns)
		}
		return lines
	}
	canvas := newCanvas(columns, rows*2, background)
	canvas.drawSprite(0, 0, portrait.resize(columns, rows*2))
	return strings.Split(canvas.render(), "\n")
}

func (m model) settingsFooter() (string, string) {
	if m.settingsEditing {
		return "  ←/→ change  •  Enter save  •  Esc cancel", "  Editing Codex sprite: " + m.codexSprite.String()
	}
	if m.settingsSavePending {
		return "  saving settings…  •  Tab switch  •  q quit", "  Codex sprite: " + m.codexSprite.String()
	}
	if m.settingsErr != "" {
		return "  settings save failed  •  Enter edit  •  q quit", "  " + m.settingsErr
	}
	return "  ↑/↓ select  •  Enter edit  •  Tab switch  •  q quit", "  Codex sprite: " + m.codexSprite.String()
}
