package tui

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/firekeeper-ai/firekeeper/internal/iconfont"
	"github.com/firekeeper-ai/firekeeper/internal/transcript"
	"github.com/firekeeper-ai/firekeeper/pkg/event"
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
	if got := toolLabel(event.ProviderClaude); got != "Claude" {
		t.Fatalf("off: %q", got)
	}
	withIconMode(t, iconsFont)
	if got := toolLabel(event.ProviderClaude); got != "  Claude" {
		t.Fatalf("font: %q", got)
	}
	// A tool with no logo still lines up with the others.
	if got := toolLabel(event.Provider("mystery")); got != "   mystery" {
		t.Fatalf("font, unknown tool: %q", got)
	}
	withIconMode(t, iconsUnicode)
	if got := toolLabel(event.ProviderCodex); got != "●  Codex" {
		t.Fatalf("unicode: %q", got)
	}
}

func TestToolLabelWidthIsNameWidthPlusIconWidth(t *testing.T) {
	for _, mode := range []iconMode{iconsOff, iconsFont, iconsUnicode} {
		withIconMode(t, mode)
		for _, provider := range event.Providers() {
			if got, want := ansi.StringWidth(toolLabel(provider)), len(providerName(provider))+toolIconWidth(); got != want {
				t.Errorf("mode %d %s: width %d, want %d", mode, provider, got, want)
			}
		}
	}
}

// Every harness the dashboard can show must have a logo.
func TestEveryProviderHasFontIcon(t *testing.T) {
	for _, provider := range transcript.Providers() {
		if providerStyles[provider].icon == "" {
			t.Errorf("provider %q has no font icon", provider)
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
	if len(script) != len(providerStyles) {
		t.Fatalf("build.mjs has %d glyphs, providers.go has %d", len(script), len(providerStyles))
	}
	for provider, style := range providerStyles {
		if got := script[strings.ToLower(providerName(provider))]; got != style.icon {
			t.Errorf("%s: providers.go %U, build.mjs %s", provider, []rune(style.icon)[0], fmt.Sprintf("%U", []rune(got)))
		}
	}
}

func TestProviderLinesWithIconsStayWithinWidth(t *testing.T) {
	withIconMode(t, iconsFont)
	summary := dashSummary{tools: []dashToolSummary{
		{provider: event.ProviderClaude, runtimes: 2, tokens: 1200},
		{provider: event.ProviderOpenCode, runtimes: 1, tokens: 300},
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
	withUsageData(&m)
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
	for provider, style := range providerStyles {
		if r := []rune(style.icon)[0]; r < iconfont.FirstCodepoint || r > iconfont.LastCodepoint {
			t.Errorf("%s at %U is outside the Ghostty mapping", provider, r)
		}
	}
}
