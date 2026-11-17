package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func demoKey(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func newSizedDemo(t *testing.T) tea.Model {
	t.Helper()
	m, _ := NewDemoModel().Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	return m
}

// quitsProgram reports whether cmd produces tea.QuitMsg.
func quitsProgram(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// The demo runs in a browser with no provider files, so no key may leave a
// command behind except the one that quits, whatever view it is pressed in.
func TestDemoKeysNeverStartCommands(t *testing.T) {
	keys := []string{
		"up", "down", "left", "right", "pgup", "pgdown", "home", "end", "tab", "shift+tab",
		"enter", "esc", "c", "s", "R", "r", "o", "e", "?", "j", "k", "h", "l", "i", "u", "x", "/", "backspace",
	}
	for _, start := range [][]string{nil, {"tab"}, {"c"}, {"tab", "enter"}, {"?"}} {
		for _, key := range keys {
			var m tea.Model = newSizedDemo(t)
			for _, k := range start {
				m, _ = m.Update(demoKey(k))
			}
			next, cmd := m.Update(demoKey(key))
			if cmd != nil {
				t.Errorf("after %v, key %q returned a command", start, key)
			}
			if _, ok := next.(demoModel); !ok {
				t.Fatalf("after %v, key %q returned %T, not a demoModel", start, key, next)
			}
			_ = next.View()
		}
	}
}

func TestDemoQuit(t *testing.T) {
	tests := []struct {
		name  string
		setup []string
		key   string
		quit  bool
	}{
		{"q on the process list", nil, "q", true},
		{"ctrl+c on the process list", nil, "ctrl+c", true},
		{"ctrl+c in help", []string{"?"}, "ctrl+c", true},
		{"ctrl+c in chat", []string{"c"}, "ctrl+c", true},
		{"q in help only closes help", []string{"?"}, "q", false},
		{"q in chat only closes chat", []string{"c"}, "q", false},
		{"other key does not quit", nil, "x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m tea.Model = newSizedDemo(t)
			for _, k := range tt.setup {
				m, _ = m.Update(demoKey(k))
			}
			_, cmd := m.Update(demoKey(tt.key))
			if got := quitsProgram(cmd); got != tt.quit {
				t.Fatalf("quits = %v, want %v", got, tt.quit)
			}
		})
	}
}

func TestDemoShowsSampleDataOnly(t *testing.T) {
	var m tea.Model = newSizedDemo(t)
	view := m.View()
	for _, want := range []string{"atlas-studio", "ember-api", "northwind-docs", "blueprint-cli"} {
		if !strings.Contains(view, want) {
			t.Errorf("process view missing %q", want)
		}
	}
	m, _ = m.Update(demoKey("c"))
	if !strings.Contains(m.View(), "plan the next release") {
		t.Error("chat view does not show the sample conversation")
	}
	m, _ = m.Update(demoKey("esc"))
	m, _ = m.Update(demoKey("tab"))
	if v := m.View(); !strings.Contains(v, "Codex") || !strings.Contains(v, "Copilot") {
		t.Error("overview does not show the sample providers")
	}
	m, _ = m.Update(demoKey("tab"))
	for range usageProviderCount {
		m, _ = m.Update(demoKey("enter"))
		if strings.Contains(m.View(), "plan the next release") {
			return
		}
		m, _ = m.Update(demoKey("right"))
	}
	t.Error("no usage provider opens the sample conversation on Enter")
}

func TestDemoPause(t *testing.T) {
	var m tea.Model = newSizedDemo(t)
	frame := func() int { return m.(demoModel).frame }
	m, cmd := m.Update(demoTickMsg{})
	if frame() != 1 || cmd == nil {
		t.Fatalf("tick did not advance and reschedule: frame %d", frame())
	}
	m, _ = m.Update(NewDemoPauseMsg(true))
	m, cmd = m.Update(demoTickMsg{})
	if frame() != 1 {
		t.Errorf("paused demo advanced to frame %d", frame())
	}
	if cmd == nil {
		t.Error("paused demo stopped ticking, so it could never resume")
	}
	m, _ = m.Update(NewDemoPauseMsg(false))
	m, _ = m.Update(demoTickMsg{})
	if frame() != 2 {
		t.Errorf("resumed demo at frame %d, want 2", frame())
	}
}

func TestDemoIgnoresMouseAndUnknownMessages(t *testing.T) {
	var m tea.Model = newSizedDemo(t)
	before := m.View()
	m, cmd := m.Update(tea.MouseMsg{X: 3, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if cmd != nil || m.View() != before {
		t.Error("mouse input changed the demo")
	}
	_, cmd = m.Update(struct{}{})
	if cmd != nil {
		t.Error("unknown message started a command")
	}
}
