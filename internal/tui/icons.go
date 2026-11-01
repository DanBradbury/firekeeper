package tui

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/DanBradbury/firekeeper/internal/iconfont"
)

// iconMode selects how a harness name is marked in the dashboard.
type iconMode int

const (
	// iconsOff shows the name alone.
	iconsOff iconMode = iota
	// iconsFont puts the harness logo from FirekeeperIcons.ttf (see
	// scripts/icon-font) before the name. A terminal without that font shows
	// empty boxes, so this is the default only once `firekeeper install-font`
	// has installed it.
	iconsFont
	// iconsUnicode puts a plain Unicode dot before the name; it needs no font.
	iconsUnicode
)

// envIcons names the environment variable that picks the icon mode.
const envIcons = "FIREKEEPER_ICONS"

// iconGap is what follows an icon. The logo glyphs are drawn wider than the
// one cell the width helpers count, so they spill into the next cell; two
// spaces leave one visible between logo and name.
const iconGap = "  "

// iconCellWidth is the columns an icon and its gap take.
const iconCellWidth = 1 + len(iconGap)

// unicodeIcon is the glyph iconsUnicode uses for every harness; the name's
// color already tells them apart.
const unicodeIcon = "●"

// toolFontIcons maps the dashboard's tool names to the private use codepoints
// scripts/icon-font/build.mjs gives each logo. Keep the two lists in step.
var toolFontIcons = map[string]string{
	"Claude":   "",
	"Codex":    "",
	"Copilot":  "",
	"Cursor":   "",
	"Gemini":   "",
	"Kimi":     "",
	"OpenCode": "",
}

// parseIconMode reads a FIREKEEPER_ICONS value. An empty value means no
// choice was made (see defaultIconMode); an unrecognized one is an error so
// the caller can fall back to that default.
func parseIconMode(value string) (iconMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "0", "false", "none":
		return iconsOff, nil
	case "font", "1", "true", "on":
		return iconsFont, nil
	case "unicode":
		return iconsUnicode, nil
	case "":
		return defaultIconMode(), nil
	default:
		return defaultIconMode(), fmt.Errorf("unknown %s value %q", envIcons, value)
	}
}

// activeIconMode is read at render time; tests set it directly.
var activeIconMode = iconModeFromEnv()

func iconModeFromEnv() iconMode {
	mode, _ := parseIconMode(os.Getenv(envIcons))
	return mode
}

// fontInstalled reports whether the logo font sits in the user's font folder;
// tests replace it. That is a hint, not proof the terminal uses the font.
var fontInstalled = func() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	dir, err := iconfont.UserDir(runtime.GOOS, home, os.Getenv("XDG_DATA_HOME"))
	return err == nil && iconfont.Installed(dir)
}

// defaultIconMode is the logos once the font is installed, and a plain dot
// until then, so a fresh install never shows empty boxes.
func defaultIconMode() iconMode {
	if fontInstalled() {
		return iconsFont
	}
	return iconsUnicode
}

// toolIcon returns the icon for a tool, or "" when icons are off or the tool
// has none.
func toolIcon(tool string) string {
	switch activeIconMode {
	case iconsFont:
		return toolFontIcons[tool]
	case iconsUnicode:
		return unicodeIcon
	case iconsOff:
		return ""
	default:
		return ""
	}
}

// toolIconWidth is the extra columns a tool label needs for its icon.
func toolIconWidth() int {
	if activeIconMode == iconsOff {
		return 0
	}
	return iconCellWidth
}

// toolIconPrefix is the icon and the space after it, or blanks of the same
// width for a tool with no icon, or "" when icons are off.
func toolIconPrefix(tool string) string {
	if icon := toolIcon(tool); icon != "" {
		return icon + iconGap
	}
	return strings.Repeat(" ", toolIconWidth())
}

// toolLabel is the tool name with its icon in front, when it has one.
func toolLabel(tool string) string {
	return toolIconPrefix(tool) + tool
}
