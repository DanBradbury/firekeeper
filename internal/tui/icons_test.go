package tui

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/DanBradbury/firekeeper/internal/iconfont"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

func withIconMode(t *testing.T, mode iconMode) {
	t.Helper()
	previous := activeIconMode
	activeIconMode = mode
	t.Cleanup(func() { activeIconMode = previous })
}

func TestParseIconMode(t *testing.T) {
	for _, test := range []struct {
		in      string
		want    iconMode
		wantErr bool
	}{
		{"", iconsUnicode, false},
		{"off", iconsOff, false},
		{" Font ", iconsFont, false},
		{"1", iconsFont, false},
		{"unicode", iconsUnicode, false},
		{"none", iconsOff, false},
		{"emoji", iconsUnicode, true},
	} {
		got, err := parseIconMode(test.in)
		if got != test.want || (err != nil) != test.wantErr {
			t.Errorf("parseIconMode(%q) = %v, %v; want %v, error %v", test.in, got, err, test.want, test.wantErr)
		}
	}
}

func TestToolLabelModes(t *testing.T) {
	withIconMode(t, iconsOff)
	if got := toolLabel("Claude"); got != "Claude" {
		t.Fatalf("off: %q", got)
	}
	withIconMode(t, iconsFont)
	if got := toolLabel("Claude"); got != "  Claude" {
		t.Fatalf("font: %q", got)
	}
	// A tool with no logo still lines up with the others.
	if got := toolLabel("Mystery"); got != "   Mystery" {
		t.Fatalf("font, unknown tool: %q", got)
	}
	withIconMode(t, iconsUnicode)
	if got := toolLabel("Codex"); got != "●  Codex" {
		t.Fatalf("unicode: %q", got)
	}
}

func TestToolLabelWidthIsNameWidthPlusIconWidth(t *testing.T) {
	for _, mode := range []iconMode{iconsOff, iconsFont, iconsUnicode} {
		withIconMode(t, mode)
		for tool := range toolFontIcons {
			if got, want := ansi.StringWidth(toolLabel(tool)), len(tool)+toolIconWidth(); got != want {
				t.Errorf("mode %d %s: width %d, want %d", mode, tool, got, want)
			}
		}
	}
}

// Every harness the dashboard can show must have a logo.
func TestEveryProviderHasFontIcon(t *testing.T) {
	for _, provider := range transcript.Providers() {
		label := recentToolLabel(provider)
		if _, ok := toolFontIcons[label]; !ok {
			t.Errorf("provider %q (%s) has no font icon", provider, label)
		}
	}
}

// build.mjs assigns the codepoints the font really has; the two lists drift
// easily, so compare them.
func TestFontIconsMatchBuildScript(t *testing.T) {
	data, err := os.ReadFile("../../scripts/icon-font/build.mjs")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`(?m)^\s+(\w+): 0x([0-9a-f]+),`)
	script := map[string]string{}
	for _, match := range pattern.FindAllStringSubmatch(string(data), -1) {
		code, err := strconv.ParseInt(match[2], 16, 32)
		if err != nil {
			t.Fatal(err)
		}
		script[strings.ToLower(match[1])] = string(rune(code))
	}
	if len(script) != len(toolFontIcons) {
		t.Fatalf("build.mjs has %d glyphs, icons.go has %d", len(script), len(toolFontIcons))
	}
	for tool, icon := range toolFontIcons {
		if got := script[strings.ToLower(tool)]; got != icon {
			t.Errorf("%s: icons.go %U, build.mjs %s", tool, []rune(icon)[0], fmt.Sprintf("%U", []rune(got)))
		}
	}
}

func TestProviderLinesWithIconsStayWithinWidth(t *testing.T) {
	withIconMode(t, iconsFont)
	summary := dashSummary{tools: []dashToolSummary{
		{tool: "Claude", runtimes: 2, tokens: 1200},
		{tool: "OpenCode", runtimes: 1, tokens: 300},
	}}
	for _, line := range summary.providerLines(40) {
		if got := ansi.StringWidth(line); got > 40 {
			t.Errorf("line is %d columns, want at most 40: %q", got, line)
		}
	}
}

func TestUsageProviderLabelsCarryIcons(t *testing.T) {
	withIconMode(t, iconsFont)
	labels := usageProviderLabels()
	if len(labels) != len(usageProviderNames) {
		t.Fatalf("%d labels for %d providers", len(labels), len(usageProviderNames))
	}
	if labels[0] != "  CODEX" || labels[3] != "  CLAUDE" {
		t.Fatalf("labels = %q", labels)
	}
	withIconMode(t, iconsOff)
	if got := usageProviderLabels()[0]; got != "CODEX" {
		t.Fatalf("icons off: %q", got)
	}
}

func TestUsageSelectorHitTestMatchesIcons(t *testing.T) {
	withIconMode(t, iconsFont)
	m := model{width: 100, height: 30, activeTab: usageTab}
	// The selector starts with one space, then chips; the second chip must be
	// hit at the column the rendered line puts it, icons included.
	line := m.usageProviderSelectorLine()
	start := ansi.StringWidth(ansi.Strip(line)[:strings.Index(ansi.Strip(line), "COPILOT")])
	if got := m.hitTest(start, 1); got.kind != hitUsageProvider || got.index != 1 {
		t.Fatalf("hit at column %d = %+v", start, got)
	}
}

func TestDefaultIconModeFollowsInstalledFont(t *testing.T) {
	previous := fontInstalled
	t.Cleanup(func() { fontInstalled = previous })

	fontInstalled = func() bool { return true }
	if got, err := parseIconMode(""); got != iconsFont || err != nil {
		t.Fatalf("font installed: %v, %v", got, err)
	}
	// An explicit choice wins over what is installed.
	if got, _ := parseIconMode("unicode"); got != iconsUnicode {
		t.Fatalf("explicit unicode: %v", got)
	}
	fontInstalled = func() bool { return false }
	if got, _ := parseIconMode(""); got != iconsUnicode {
		t.Fatalf("font missing: %v", got)
	}
	if got, _ := parseIconMode("font"); got != iconsFont {
		t.Fatalf("explicit font: %v", got)
	}
}

func TestFontIconsFallInsideTheInstalledRange(t *testing.T) {
	for tool, icon := range toolFontIcons {
		if r := []rune(icon)[0]; r < iconfont.FirstCodepoint || r > iconfont.LastCodepoint {
			t.Errorf("%s at %U is outside the Ghostty mapping", tool, r)
		}
	}
}
