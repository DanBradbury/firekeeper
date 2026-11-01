package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/transcript"
	tea "github.com/charmbracelet/bubbletea"
)

func TestResumeArgs(t *testing.T) {
	cases := map[transcript.Provider][]string{
		transcript.ProviderCodex:    {"codex", "resume", "abc-123"},
		transcript.ProviderCopilot:  {"copilot", "--resume=abc-123"},
		transcript.ProviderClaude:   {"claude", "--resume", "abc-123"},
		transcript.ProviderKimi:     {"kimi", "--session", "abc-123"},
		transcript.ProviderOpenCode: {"opencode", "--session", "abc-123"},
		transcript.ProviderGemini:   {"gemini", "--resume", "abc-123"},
		transcript.ProviderCursor:   {"cursor-agent", "--resume", "abc-123"},
	}
	for _, provider := range transcript.Providers() {
		want, ok := cases[provider]
		if !ok {
			t.Fatalf("no resume case for provider %q", provider)
		}
		got, err := resumeArgs(provider, "abc-123")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, %v", provider, got, err)
		}
	}
	if _, err := resumeArgs("unknown", "abc"); err == nil {
		t.Error("unknown provider accepted")
	}
	for _, id := range []string{"", "-rf", "a b", "a;rm -rf /", "a$(x)", "a'b", "a\nb"} {
		if _, err := resumeArgs(transcript.ProviderClaude, id); err == nil {
			t.Errorf("id %q accepted", id)
		}
	}
}

func TestResumeShellLineQuotes(t *testing.T) {
	got := resumeShellLine([]string{"claude", "--resume", "abc"}, "/work/it's here")
	want := `cd '/work/it'\''s here' && 'claude' '--resume' 'abc'`
	if got != want {
		t.Fatalf("got %s", got)
	}
	if got := resumeShellLine([]string{"codex"}, ""); strings.Contains(got, "cd ") {
		t.Fatalf("cd without cwd: %s", got)
	}
}

func TestOpenResumeTab(t *testing.T) {
	var scripts, calls [][]string
	run := func(script string, args ...string) (string, error) {
		scripts = append(scripts, []string{script})
		calls = append(calls, args)
		return "ok", nil
	}
	for _, c := range []struct{ term, app string }{{"ghostty", "Ghostty"}, {"iTerm.app", "iTerm2"}, {"Apple_Terminal", "Terminal.app"}} {
		got, err := openResumeTab("darwin", c.term, "LINE", "/cwd", run)
		if err != nil || got != c.app {
			t.Fatalf("%s: %q, %v", c.term, got, err)
		}
		last := len(calls) - 1
		if calls[last][0] != "LINE" || calls[last][1] != "/cwd" || scripts[last][0] != resumeTerminalScripts[c.app] {
			t.Fatalf("%s: script not run with line and cwd: %v", c.app, calls[last])
		}
	}
	for _, c := range []struct{ goos, term string }{{"darwin", "tmux"}, {"darwin", ""}, {"linux", "ghostty"}, {"windows", ""}} {
		_, err := openResumeTab(c.goos, c.term, "LINE", "/cwd", run)
		if err == nil || !strings.Contains(err.Error(), "run: LINE") || len(calls) != 3 {
			t.Errorf("%v: unsupported terminal ran script or hid the command: %v", c, err)
		}
	}
	_, err := openResumeTab("darwin", "ghostty", "LINE", "/cwd", func(string, ...string) (string, error) { return "", errors.New("denied") })
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("runner error lost: %v", err)
	}
}

func TestResumeKeyOnIdleList(t *testing.T) {
	m := idleRecentModel(90, 22)
	m.recentSessions[0].provider = transcript.ProviderClaude
	m.recentSessions[0].id = "abc-123"
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if cmd == nil || !strings.Contains(next.(model).terminalStatus, "resuming Copilot") {
		t.Fatalf("R did not start a resume: %q", next.(model).terminalStatus)
	}
	m.recentSessions[0].id = "bad id"
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if cmd != nil || !strings.Contains(next.(model).terminalStatus, "resume failed") {
		t.Fatalf("bad id resumed: %q", next.(model).terminalStatus)
	}
	next, _ = next.(model).Update(resumeResultMsg{app: "Ghostty"})
	if got := next.(model).terminalStatus; got != "resumed in a new Ghostty tab" {
		t.Fatalf("status = %q", got)
	}
}
