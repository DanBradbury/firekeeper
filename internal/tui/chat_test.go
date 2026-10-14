package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/transcript"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const chatTestSessionID = "0199c3d4-e5f6-7a8b-9c0d-0000000000c4"

func chatClaudeLine(role, text string) string {
	if role == "user" {
		return `{"type":"user","timestamp":"2026-10-13T09:00:00Z","sessionId":"` + chatTestSessionID +
			`","message":{"role":"user","content":` + jsonQuote(text) + `}}` + "\n"
	}
	return `{"type":"assistant","timestamp":"2026-10-13T09:00:05Z","sessionId":"` + chatTestSessionID +
		`","message":{"id":"msg_` + text[:3] + `","role":"assistant","content":[{"type":"text","text":` + jsonQuote(text) + `}]}}` + "\n"
}

func jsonQuote(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(value) + `"`
}

func chatTestModel(t *testing.T) (model, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), chatTestSessionID+".jsonl")
	content := chatClaudeLine("user", "<command-name>/usage</command-name>") +
		chatClaudeLine("user", "Please add a chat view") +
		`{"type":"assistant","message":{"id":"msg_tool","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]}}` + "\n" +
		chatClaudeLine("assistant", "Sure, adding the chat view now.")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewModel()
	m.width, m.height = 100, 30
	m.processGroups = []processGroup{{
		tool:      "Claude",
		root:      processInfo{pid: 42, tty: "ttys009", elapsed: "00:10"},
		processes: []processInfo{{pid: 42, ppid: 1, command: "claude"}},
		sessions:  []sessionInfo{{id: chatTestSessionID, name: "Chat test", cwd: "/workspace/demo", rolloutPath: path}},
	}}
	return m, path
}

// openTestChat opens the chat for the selected runtime and applies its load.
func openTestChat(t *testing.T, m model) model {
	t.Helper()
	cmd := m.openChat()
	return runChatCmd(t, m, cmd)
}

func runChatCmd(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a chat command")
	}
	updated, _ := m.Update(cmd())
	return updated.(model)
}

func TestChatOpensWithUserAndAssistantMessages(t *testing.T) {
	m, _ := chatTestModel(t)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(model)
	if !m.chat.open || !m.chat.loading {
		t.Fatal("c did not open the chat view")
	}
	m = runChatCmd(t, m, cmd)
	if m.chat.err != "" {
		t.Fatalf("chat error: %s", m.chat.err)
	}
	if len(m.chat.messages) != 2 {
		t.Fatalf("messages = %d, want the user prompt and the assistant reply", len(m.chat.messages))
	}
	view := ansi.Strip(m.View())
	for _, expected := range []string{"chat (demo/0199c3d4)", "You", "Please add a chat view", "Claude", "Sure, adding the chat view now.", "2 messages"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("chat view missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, "command-name") {
		t.Fatal("chat view shows injected command context")
	}
}

func TestChatPicksUpNewMessagesOnPoll(t *testing.T) {
	m, path := chatTestModel(t)
	m = openTestChat(t, m)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString(chatClaudeLine("user", "Thanks, one more change") + chatClaudeLine("assistant", "Done with the change."))
	file.Close()

	cmd := m.refreshChat()
	m = runChatCmd(t, m, cmd)
	if len(m.chat.messages) != 4 {
		t.Fatalf("messages = %d, want 4 after the poll", len(m.chat.messages))
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Done with the change.") {
		t.Fatalf("followed chat does not show the newest message:\n%s", view)
	}
}

func TestChatKeysScrollAndClose(t *testing.T) {
	m, path := chatTestModel(t)
	var long strings.Builder
	for index := range 40 {
		long.WriteString(chatClaudeLine("assistant", "Reply number "+strings.Repeat("x", index%5)+" with some text"))
	}
	os.WriteFile(path, []byte(long.String()), 0o600)
	m = openTestChat(t, m)
	if !m.chat.follow || m.chat.scroll == 0 {
		t.Fatalf("chat should open at the latest message: scroll=%d follow=%v", m.chat.scroll, m.chat.follow)
	}
	bottom := m.chat.scroll
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	if m.chat.scroll != bottom-1 || m.chat.follow {
		t.Fatalf("up did not scroll: scroll=%d follow=%v", m.chat.scroll, m.chat.follow)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(model)
	if m.chat.scroll != 0 {
		t.Fatalf("g did not jump to the top: %d", m.chat.scroll)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = updated.(model)
	if m.chat.scroll != bottom || !m.chat.follow {
		t.Fatal("G did not return to the latest message")
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = updated.(model)
	if m.chat.open || cmd != nil {
		t.Fatal("q must close the chat view without quitting")
	}
	if m.processCursor != 0 || m.activeTab != processesTab {
		t.Fatal("closing chat must return to the same Processes selection")
	}
}

func TestChatWithoutSessionMetadataShowsStatus(t *testing.T) {
	m := NewModel()
	m.processGroups = []processGroup{{tool: "OpenCode", root: processInfo{pid: 7}}}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(model)
	if m.chat.open || cmd != nil || !strings.Contains(m.terminalStatus, "no session metadata") {
		t.Fatalf("chat opened without metadata: open=%v status=%q", m.chat.open, m.terminalStatus)
	}
}

func TestChatReportsMissingTranscriptReader(t *testing.T) {
	m := NewModel()
	m.processGroups = []processGroup{{tool: "Kimi", root: processInfo{pid: 7}, sessions: []sessionInfo{{id: "k1"}}}}
	m = openTestChat(t, m)
	if !strings.Contains(m.chat.err, "no transcript reader for kimi") {
		t.Fatalf("chat error = %q", m.chat.err)
	}
}

func TestChatIgnoresStaleResults(t *testing.T) {
	m, _ := chatTestModel(t)
	result := m.openChat()()
	m.chat = chatState{}
	updated, _ := m.Update(result)
	if updated.(model).chat.open || len(updated.(model).chat.messages) != 0 {
		t.Fatal("a result for a closed chat must be ignored")
	}
}

func TestChatViewFitsTerminal(t *testing.T) {
	for _, size := range [][2]int{{20, 6}, {40, 12}, {100, 30}} {
		m, _ := chatTestModel(t)
		m.width, m.height = size[0], size[1]
		m = openTestChat(t, m)
		lines := strings.Split(m.View(), "\n")
		if len(lines) != m.height {
			t.Fatalf("%v: rows = %d", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) != m.width {
				t.Fatalf("%v: line width = %d", size, ansi.StringWidth(line))
			}
		}
	}
}

func TestChatMessagesFilter(t *testing.T) {
	ts := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	events := []transcript.Event{
		{Role: transcript.RoleUser, Text: "  real prompt  ", TS: &ts},
		{Role: transcript.RoleUser, Text: "<environment_context>\n<cwd>/x</cwd>\n</environment_context>"},
		{Role: transcript.RoleUser, Text: "# AGENTS.md instructions for /x\n..."},
		{Role: transcript.RoleUser, Text: "<b>bold</b> is how I'd write it"},
		{Role: transcript.RoleAssistant, Text: "reply"},
		{Role: transcript.RoleAssistant, Text: "   "},
		{Role: transcript.RoleToolCall, Text: "Read"},
		{Role: transcript.RoleToolResult, Text: "file contents"},
		{Role: transcript.RoleSystem, Text: "system prompt"},
		{Role: transcript.RoleMeta, Text: "{}"},
	}
	messages := chatMessages(events)
	var got []string
	for _, message := range messages {
		got = append(got, string(message.role)+":"+message.text)
	}
	want := []string{"user:real prompt", "user:<b>bold</b> is how I'd write it", "assistant:reply"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("messages = %q, want %q", got, want)
	}
}

func TestChatWrapSanitizesAndWraps(t *testing.T) {
	lines := chatWrap("one two three four\x1b[31m red\tTab\n\nlast", 10)
	for _, line := range lines {
		if ansi.StringWidth(line) > 10 || strings.ContainsRune(line, '\x1b') || strings.ContainsRune(line, '\t') {
			t.Fatalf("unsafe wrapped line %q", line)
		}
	}
	if lines[len(lines)-2] != "" || lines[len(lines)-1] != "last" {
		t.Fatalf("blank lines not kept: %q", lines)
	}
}
