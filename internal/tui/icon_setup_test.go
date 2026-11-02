package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const iTermPrefsFixture = `[
 {"Name":"Default","Use Non-ASCII Font":false,"Non Ascii Font":"Menlo-Regular 12"},
 {"Name":"Icons","Use Non-ASCII Font":true,"Non Ascii Font":"FirekeeperIcons-Regular 12"},
 {"Name":"Numeric","Use Non-ASCII Font":1,"Non Ascii Font":"FirekeeperIcons 12"},
 {"Name":"WrongFont","Use Non-ASCII Font":true,"Non Ascii Font":"Menlo-Regular 12"},
 {"Name":"Disabled","Use Non-ASCII Font":false,"Non Ascii Font":"FirekeeperIcons-Regular 12"}]`

func TestProfileUsesIconFont(t *testing.T) {
	for _, test := range []struct {
		name, prefs    string
		configured, ok bool
	}{
		{"Default", iTermPrefsFixture, false, true},
		{"Icons", iTermPrefsFixture, true, true},
		{"Numeric", iTermPrefsFixture, true, true},
		{"WrongFont", iTermPrefsFixture, false, true},
		{"Disabled", iTermPrefsFixture, false, true},
		{"Missing", iTermPrefsFixture, false, false},
		{"Default", `not json`, false, false},
		{"Default", `{"New Bookmarks":[]}`, false, false},
	} {
		configured, found := profileUsesIconFont([]byte(test.prefs), test.name)
		if configured != test.configured || found != test.ok {
			t.Errorf("%s: got configured=%v found=%v, want %v %v", test.name, configured, found, test.configured, test.ok)
		}
	}
}

func TestCheckIconSetupOnlyForLogoMode(t *testing.T) {
	previous := iconSetupSource
	iconSetupSource = func(context.Context) string { return iconSetupHint }
	t.Cleanup(func() { iconSetupSource = previous })

	withIconMode(t, iconsUnicode)
	if checkIconSetup() != nil {
		t.Fatal("check ran without the logo font mode")
	}
	withIconMode(t, iconsFont)
	cmd := checkIconSetup()
	if cmd == nil {
		t.Fatal("no check in logo font mode")
	}
	if msg, _ := cmd().(iconSetupMsg); msg.hint != iconSetupHint {
		t.Fatalf("hint = %q", msg.hint)
	}
}

func openedIconSetup(t *testing.T) model {
	t.Helper()
	updated, _ := NewModel().Update(iconSetupMsg{hint: iconSetupHint})
	m := updated.(model)
	if !m.iconSetupOpen {
		t.Fatal("hint did not open the setup panel")
	}
	return m
}

func TestIconSetupBlocksUntilDismissed(t *testing.T) {
	m := openedIconSetup(t)
	tab := tea.KeyMsg{Type: tea.KeyTab}
	updated, _ := m.Update(tab)
	if got := updated.(model); !got.iconSetupOpen || got.activeTab != m.activeTab {
		t.Fatal("tab got past the setup panel")
	}
	updated, cmd := m.Update(tea.MouseMsg{X: 5, Y: 0, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if !updated.(model).iconSetupOpen || cmd != nil {
		t.Fatal("mouse got past the setup panel")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.(model).iconSetupOpen {
		t.Fatal("enter did not continue")
	}
}

func TestIconSetupQuitKeys(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyCtrlC}, {Type: tea.KeyRunes, Runes: []rune{'q'}}} {
		_, cmd := openedIconSetup(t).Update(key)
		if cmd == nil {
			t.Fatalf("%v did not quit", key)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%v did not quit", key)
		}
	}
}

func TestIconSetupRecheck(t *testing.T) {
	previous := iconSetupSource
	t.Cleanup(func() { iconSetupSource = previous })
	m := openedIconSetup(t)

	iconSetupSource = func(context.Context) string { return iconSetupHint }
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatal("r started no check")
	}
	msg := cmd().(iconSetupMsg)
	if !msg.recheck {
		t.Fatal("answer not marked as a recheck")
	}
	updated, _ = updated.Update(msg)
	if got := updated.(model); !got.iconSetupOpen || !strings.Contains(got.iconSetupNote, "Still not set") {
		t.Fatalf("failed recheck: open=%v note=%q", got.iconSetupOpen, got.iconSetupNote)
	}

	updated, _ = updated.Update(iconSetupMsg{recheck: true})
	if got := updated.(model); got.iconSetupOpen || got.terminalStatus == "" {
		t.Fatal("passing recheck did not close the panel with a status")
	}
}

func TestIconSetupUseDots(t *testing.T) {
	withIconMode(t, iconsFont)
	updated, _ := openedIconSetup(t).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	if updated.(model).iconSetupOpen || activeIconMode != iconsUnicode {
		t.Fatalf("u: open=%v mode=%v", updated.(model).iconSetupOpen, activeIconMode)
	}
}

func TestIconSetupViewFits(t *testing.T) {
	m := openedIconSetup(t)
	for _, size := range [][2]int{{80, 24}, {40, 12}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		for index, row := range strings.Split(m.View(), "\n") {
			if got := ansi.StringWidth(row); got > m.width {
				t.Fatalf("%dx%d row %d is %d wide", size[0], size[1], index, got)
			}
		}
	}
	m.width, m.height = 80, 24
	view := ansi.Strip(m.View())
	for _, want := range []string{"Profiles > Text", "FirekeeperIcons", "check again"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q", want)
		}
	}
}
