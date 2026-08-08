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
	codexSpriteMage
	codexSpriteChoiceCount
)

const settingsItemCount = 3

type persistentSettings struct {
	CodexSprite   codexSpriteChoice `json:"codex_sprite"`
	CopilotSprite codexSpriteChoice `json:"copilot_sprite"`
	KimiSprite    codexSpriteChoice `json:"kimi_sprite"`
}

type settingsSaveResultMsg struct {
	err error
}

func defaultPersistentSettings() persistentSettings {
	return persistentSettings{CodexSprite: codexSpriteRanger, CopilotSprite: codexSpriteWarrior, KimiSprite: codexSpriteMage}
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
	if settings.CodexSprite < codexSpriteRanger || settings.CodexSprite >= codexSpriteChoiceCount || settings.CopilotSprite < codexSpriteRanger || settings.CopilotSprite >= codexSpriteChoiceCount || settings.KimiSprite < codexSpriteRanger || settings.KimiSprite >= codexSpriteChoiceCount {
		return defaultPersistentSettings(), path, fmt.Errorf("settings contain invalid provider sprite")
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
	case codexSpriteMage:
		return "Mage"
	default:
		return "Wizard"
	}
}

func (m *model) cycleCodexSprite(delta int) {
	m.codexSprite = (m.codexSprite + codexSpriteChoice(delta) + codexSpriteChoiceCount) % codexSpriteChoiceCount
	m.applyCodexSprite()
}

func (m *model) cycleSelectedSprite(delta int) {
	choice := (m.selectedSprite() + codexSpriteChoice(delta) + codexSpriteChoiceCount) % codexSpriteChoiceCount
	switch m.settingsCursor {
	case 1:
		m.copilotSprite = choice
	case 2:
		m.kimiSprite = choice
	default:
		m.codexSprite = choice
		m.applyCodexSprite()
	}
}

func (m model) selectedSprite() codexSpriteChoice {
	switch m.settingsCursor {
	case 1:
		return m.copilotSprite
	case 2:
		return m.kimiSprite
	default:
		return m.codexSprite
	}
}

func (m model) selectedProviderName() string {
	switch m.settingsCursor {
	case 1:
		return "Copilot"
	case 2:
		return "Kimi"
	default:
		return "Codex"
	}
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
		m.codexSprite = m.settingsEditOriginal.CodexSprite
		m.copilotSprite = m.settingsEditOriginal.CopilotSprite
		m.kimiSprite = m.settingsEditOriginal.KimiSprite
		m.applyCodexSprite()
	}
	m.settingsEditing = false
}

func (m model) saveSettingsCmd() tea.Cmd {
	if m.settingsPath == "" {
		return nil
	}
	settings := persistentSettings{CodexSprite: m.codexSprite, CopilotSprite: m.copilotSprite, KimiSprite: m.kimiSprite}
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
		usagePaintLine(m.settingsMarker(0)+"Codex Sprite  "+m.codexSprite.String(), m.width, usageBrightColor, m.settingsCursor == 0, usagePanelColor),
		usagePaintLine(m.settingsMarker(1)+"Copilot Sprite  "+m.copilotSprite.String(), m.width, usageBrightColor, m.settingsCursor == 1, usagePanelColor),
		usagePaintLine(m.settingsMarker(2)+"Kimi Sprite  "+m.kimiSprite.String(), m.width, usageBrightColor, m.settingsCursor == 2, usagePanelColor),
	}
	lines = append(lines, m.settingsPortraitLines()...)
	if m.settingsEditing {
		lines = append(lines,
			usagePaintLine("  Editing "+m.selectedProviderName()+" Sprite • ←/→ choose • Enter save", m.width, usageMutedColor, false, usagePanelColor),
		)
		options := []string{"Wizard", "Warrior", "Mage"}
		for index, option := range options {
			selected := codexSpriteChoice(index) == m.selectedSprite()
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

func (m model) settingsMarker(index int) string {
	if index == m.settingsCursor {
		return "▶ "
	}
	return "  "
}

func (m model) codexPortrait() sprite {
	if headshot := m.selectedHeadshot(); headshot.width > 0 && headshot.height > 0 {
		return m.portraitBox(headshot)
	}
	animations := m.selectedAnimations()
	if len(animations) == 0 || len(animations[0]) == 0 {
		return sprite{}
	}
	frame := animations[0][0]
	if frame.width < 1 || frame.height < 1 {
		return sprite{}
	}
	portrait := frame
	if frame.width >= 24 && frame.height >= 24 {
		portrait = frame.crop(8, 0, 16, 16)
	}
	return m.portraitBox(portrait)
}

func (m model) selectedHeadshot() sprite {
	switch m.selectedSprite() {
	case codexSpriteWarrior:
		return m.warriorHeadshot
	case codexSpriteMage:
		return m.mageHeadshot
	default:
		return m.wizardHeadshot
	}
}

func (m model) portraitBox(portrait sprite) sprite {
	portrait = resizeToFit(portrait, portraitBoxSize-4, portraitBoxSize-4)
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
	box.draw((box.width-portrait.width)/2, (box.height-portrait.height)/2, portrait)
	return box
}

func (m model) selectedAnimations() [][]sprite {
	switch m.settingsCursor {
	case 1:
		if int(m.copilotSprite) < len(m.copilotSprites) && len(m.copilotSprites[m.copilotSprite]) > 0 {
			return m.copilotSprites[m.copilotSprite]
		}
	case 2:
		if int(m.kimiSprite) < len(m.kimiSprites) && len(m.kimiSprites[m.kimiSprite]) > 0 {
			return m.kimiSprites[m.kimiSprite]
		}
	default:
		if int(m.codexSprite) < len(m.codexSprites) && len(m.codexSprites[m.codexSprite]) > 0 {
			return m.codexSprites[m.codexSprite]
		}
	}
	return m.animations
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
		return "  ←/→ change  •  Enter save  •  Esc cancel", "  Editing " + m.selectedProviderName() + " sprite: " + m.selectedSprite().String()
	}
	if m.settingsSavePending {
		return "  saving settings…  •  Tab switch  •  q quit", "  Codex " + m.codexSprite.String() + " • Copilot " + m.copilotSprite.String() + " • Kimi " + m.kimiSprite.String()
	}
	if m.settingsErr != "" {
		return "  settings save failed  •  Enter edit  •  q quit", "  " + m.settingsErr
	}
	return "  ↑/↓ select  •  Enter edit  •  Tab switch  •  q quit", "  Codex " + m.codexSprite.String() + " • Copilot " + m.copilotSprite.String() + " • Kimi " + m.kimiSprite.String()
}
