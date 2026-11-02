package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/iconfont"
	tea "github.com/charmbracelet/bubbletea"
)

// iconSetupTools is the order the setup panel previews the logos in.
var iconSetupTools = []string{"Claude", "Codex", "Copilot", "Cursor", "Gemini", "Kimi", "OpenCode"}

// updateIconSetup handles keys while the setup panel blocks the dashboard.
// Ctrl+C and q quit so the user can restart after changing the setting.
func (m model) updateIconSetup(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "r":
		m.iconSetupNote = "checking…"
		return m, iconSetupCmd(true)
	case "u":
		activeIconMode = iconsUnicode
		m.iconSetupOpen = false
		m.terminalStatus = "using plain dots for icons this run"
	case "enter", "esc":
		m.iconSetupOpen = false
	}
	return m, nil
}

// applyIconSetup opens, updates or closes the setup panel from a check result.
func (m model) applyIconSetup(msg iconSetupMsg) model {
	switch {
	case msg.hint != "":
		m.iconSetupOpen = true
		m.iconSetupNote = ""
		if msg.recheck {
			m.iconSetupNote = "Still not set: " + msg.hint
		}
	case msg.recheck:
		m.iconSetupOpen = false
		m.iconSetupNote = ""
		m.terminalStatus = "iTerm2 is set to use FirekeeperIcons"
	}
	return m
}

func (m model) viewIconSetup(contentRows int) string {
	if contentRows < 1 {
		return ""
	}
	text := func(value string) string { return dashPaint(value, usageTextColor, false) }
	var preview strings.Builder
	for _, tool := range iconSetupTools {
		preview.WriteString(toolFontIcons[tool] + iconGap)
	}
	body := []string{
		dashPaint("Logo icons need one iTerm2 setting.", dashOrange, true),
		"",
		text("1. iTerm2 > Settings > Profiles > Text"),
		text("2. Tick \"Use a different font for non-ASCII text\""),
		text("3. Choose FirekeeperIcons"),
		"",
		text("Preview: ") + dashPaint(preview.String(), dashBlue, true),
		text("Logos above? It works. Boxes? Not applied yet."),
	}
	if m.iconSetupNote != "" {
		body = append(body, "", dashPaint(sanitizeProcessCommand(m.iconSetupNote), dashRed, false))
	}
	width := min(m.width, 64)
	height := min(contentRows, len(body)+2)
	box := dashBox("icon font", body, width, height, dashOrange)
	left := (m.width - width) / 2
	top := (contentRows - height) / 2
	lines := make([]string, 0, contentRows)
	for len(lines) < top {
		lines = append(lines, dashFit("", m.width, usagePanelColor))
	}
	for _, row := range box {
		lines = append(lines, dashFit(dashPaint(strings.Repeat(" ", left), usageTextColor, false)+row, m.width, usagePanelColor))
	}
	for len(lines) < contentRows {
		lines = append(lines, dashFit("", m.width, usagePanelColor))
	}
	return strings.Join(lines[:contentRows], "\n")
}

func (m model) iconSetupFooter() string {
	return renderFooterKeys([]footerKey{{"r", "check again"}, {"enter", "continue"}, {"u", "use dots"}, {"q", "quit"}}, m.width)
}

// iconSetupHint says iTerm2 is not set to draw the logo codepoints from
// FirekeeperIcons. The dashboard opens a setup panel when it gets one.
const iconSetupHint = "iTerm2 is not using FirekeeperIcons for non-ASCII text"

// iconSetupTimeout bounds the two preference reads.
const iconSetupTimeout = 3 * time.Second

// iconSetupMsg carries a hint, or "" when nothing is wrong or the terminal's
// setup could not be checked. recheck marks the answer to the user's request
// to check again, as opposed to the one startup check.
type iconSetupMsg struct {
	hint    string
	recheck bool
}

// iconSetupSource returns the hint for the current terminal; tests replace it.
var iconSetupSource = iTermIconHint

// checkIconSetup runs the setup check once, off the Update path. It does
// nothing unless the logos are the active icon mode.
func checkIconSetup() tea.Cmd {
	if activeIconMode != iconsFont {
		return nil
	}
	return iconSetupCmd(false)
}

func iconSetupCmd(recheck bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), iconSetupTimeout)
		defer cancel()
		return iconSetupMsg{hint: iconSetupSource(ctx), recheck: recheck}
	}
}

// iTermIconHint returns iconSetupHint when the running iTerm2 profile does
// not use FirekeeperIcons for non-ASCII text. It is best effort: any terminal
// other than iTerm2, an unreadable preference file, or an unknown profile
// gives "" so a working setup is never nagged.
func iTermIconHint(ctx context.Context) string {
	if runtime.GOOS != "darwin" || os.Getenv("TERM_PROGRAM") != "iTerm.app" {
		return ""
	}
	profile := os.Getenv("ITERM_PROFILE")
	if profile == "" {
		return ""
	}
	prefs, err := readITermPrefs(ctx)
	if err != nil {
		return ""
	}
	if configured, found := profileUsesIconFont(prefs, profile); found && !configured {
		return iconSetupHint
	}
	return ""
}

// readITermPrefs returns iTerm2's profile list as a JSON array, read with the
// system defaults and plutil tools. Only that key is converted: the full
// preferences hold binary values JSON cannot represent. Nothing is written.
func readITermPrefs(ctx context.Context) ([]byte, error) {
	plist, err := exec.CommandContext(ctx, "/usr/bin/defaults", "export", "com.googlecode.iterm2", "-").Output()
	if err != nil {
		return nil, err
	}
	convert := exec.CommandContext(ctx, "/usr/bin/plutil", "-extract", "New Bookmarks", "json", "-o", "-", "-")
	convert.Stdin = bytes.NewReader(plist)
	return convert.Output()
}

// profileUsesIconFont reports whether the named iTerm2 profile uses
// FirekeeperIcons as its non-ASCII font, and whether the profile exists.
func profileUsesIconFont(prefs []byte, name string) (configured, found bool) {
	var profiles []map[string]any
	if err := json.Unmarshal(prefs, &profiles); err != nil {
		return false, false
	}
	for _, profile := range profiles {
		if profile["Name"] != name {
			continue
		}
		font, _ := profile["Non Ascii Font"].(string)
		return iTermFlag(profile["Use Non-ASCII Font"]) &&
			strings.Contains(strings.ToLower(font), strings.ToLower(strings.TrimSuffix(iconfont.FileName, ".ttf"))), true
	}
	return false, false
}

// iTermFlag reads a boolean preference, which plutil may emit as a number.
func iTermFlag(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	default:
		return false
	}
}
