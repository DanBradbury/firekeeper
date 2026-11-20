package tui

import (
	"strings"
	"testing"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// boxTitles returns the title set into each panel's top border in view.
func boxTitles(view string) []string {
	var titles []string
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		for _, part := range strings.Split(line, "╭─")[1:] {
			title, _, _ := strings.Cut(part, "─")
			title = strings.TrimSuffix(title, "╮")
			if title = strings.TrimSpace(title); title != "" {
				titles = append(titles, title)
			}
		}
	}
	return titles
}

// lowercaseTitle reports whether the first letter of title is lowercase.
// Icon glyphs before it are not letters and are skipped.
func lowercaseTitle(title string) bool {
	for _, r := range title {
		if unicode.IsLetter(r) {
			return unicode.IsLower(r)
		}
	}
	return false
}

func TestBoxTitlesAndLowercaseTitle(t *testing.T) {
	titles := boxTitles("╭─Sessions (2)──╮╭─lifetime──╮\n│ body │\n╭──────╮\n╰──╯")
	if got := strings.Join(titles, "|"); got != "Sessions (2)|lifetime" {
		t.Fatalf("titles = %q", got)
	}
	for title, want := range map[string]bool{
		"Sessions (2)": false, " Codex": false, "Tokens (demo/abc)": false,
		"lifetime": true, " codex": true, "chat (Demo)": true, "": false, "(2)": false,
	} {
		if got := lowercaseTitle(title); got != want {
			t.Errorf("lowercaseTitle(%q) = %v, want %v", title, got, want)
		}
	}
}

// Every titled panel in the dashboard starts with a capital letter.
func TestBoxTitlesAreCapitalized(t *testing.T) {
	sequences := [][]string{
		nil,
		{"c"},
		{"?"},
		{"tab"},
		{"tab", "right"},
		{"tab", "right", "right"},
		{"tab", "right", "right", "right"},
		{"tab", "right", "right", "right", "right"},
		{"tab", "down"},
	}
	seen := 0
	for _, keys := range sequences {
		var m tea.Model = newSizedDemo(t)
		for _, key := range keys {
			m, _ = m.Update(demoKey(key))
		}
		for _, title := range boxTitles(m.View()) {
			seen++
			if lowercaseTitle(title) {
				t.Errorf("after %v: box title %q starts lowercase", keys, title)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no box titles found")
	}
}
