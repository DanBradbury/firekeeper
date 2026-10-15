package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	_ "github.com/DanBradbury/firekeeper/internal/transcript/claude"
	_ "github.com/DanBradbury/firekeeper/internal/transcript/codex"
	_ "github.com/DanBradbury/firekeeper/internal/transcript/copilot"
	_ "github.com/DanBradbury/firekeeper/internal/transcript/opencode"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// chat.go renders the c view: the user and assistant messages of the selected
// session, read locally through the provider's transcript reader. Tool calls,
// tool output, and context the harness injects are left out. While open it
// re-reads new transcript lines on every process poll and follows the latest
// message unless the user has scrolled up.

const chatMaxMessages = 1000

type chatMessage struct {
	role transcript.Role
	text string
	ts   *time.Time
}

// chatState is the open chat view. offsets track how far each transcript file
// has been read so polls only parse new lines.
type chatState struct {
	open     bool
	rootPID  int
	tool     string
	meta     session.Meta
	title    string
	messages []chatMessage
	paths    []string
	offsets  map[string]int64
	loading  bool
	loadedAt time.Time
	err      string
	scroll   int
	follow   bool
}

type chatResultMsg struct {
	rootPID   int
	sessionID string
	paths     []string
	offsets   map[string]int64
	messages  []chatMessage
	reset     bool
	loadedAt  time.Time
	err       error
}

func (m *model) openChat() tea.Cmd {
	if len(m.processGroups) == 0 || m.processCursor >= len(m.processGroups) {
		return nil
	}
	group := m.processGroups[m.processCursor]
	info, ok := firstSession(group)
	if !ok {
		m.terminalStatus = "no session metadata for this runtime, so no chat history to show"
		return nil
	}
	m.chat = chatState{
		open:    true,
		rootPID: group.root.pid,
		tool:    group.tool,
		meta: session.Meta{
			ID:          info.id,
			Provider:    strings.ToLower(group.tool),
			CWD:         info.cwd,
			RolloutPath: info.rolloutPath,
		},
		title:   dashProjectName(info) + "/" + dashShortID(info.id),
		loading: true,
		follow:  true,
	}
	return loadChat(m.chat.rootPID, m.chat.meta, nil, nil)
}

// loadChat reads transcript events after offsets. With no paths it locates
// the session's transcript first and reads from the start.
func loadChat(rootPID int, meta session.Meta, paths []string, offsets map[string]int64) tea.Cmd {
	return func() tea.Msg {
		result := chatResultMsg{rootPID: rootPID, sessionID: meta.ID, loadedAt: time.Now()}
		source, ok := reader.For(transcript.Provider(meta.Provider))
		if !ok {
			result.err = fmt.Errorf("no transcript reader for %s sessions", meta.Provider)
			return result
		}
		if len(paths) == 0 {
			located, err := source.Locate(meta)
			if err != nil {
				result.err = err
				return result
			}
			if len(located) == 0 {
				result.err = fmt.Errorf("no transcript found for this session")
				return result
			}
			paths, offsets, result.reset = located, map[string]int64{}, true
		}
		result.paths = paths
		result.offsets = make(map[string]int64, len(paths))
		for _, path := range paths {
			events, next, err := source.Read(path, offsets[path])
			if err != nil && offsets[path] > 0 {
				// Truncated or rotated: start over from the beginning.
				return loadChat(rootPID, meta, nil, nil)()
			}
			if err != nil {
				result.err = err
				return result
			}
			result.offsets[path] = next
			result.messages = append(result.messages, chatMessages(events)...)
		}
		return result
	}
}

func chatMessages(events []transcript.Event) []chatMessage {
	var messages []chatMessage
	for _, event := range events {
		if event.Role != transcript.RoleUser && event.Role != transcript.RoleAssistant {
			continue
		}
		text := strings.TrimSpace(event.Text)
		if text == "" || (event.Role == transcript.RoleUser && chatInjectedContext(text)) {
			continue
		}
		messages = append(messages, chatMessage{role: event.Role, text: text, ts: event.TS})
	}
	return messages
}

var chatTagBlock = regexp.MustCompile(`(?s)^<([A-Za-z][\w-]*)[^>]*>.*</([A-Za-z][\w-]*)>$`)

// chatInjectedContext reports whether a user-role message is context the
// harness added rather than something the user typed: environment and
// instruction blocks, and command wrappers, each a single tag-wrapped block.
func chatInjectedContext(text string) bool {
	if strings.HasPrefix(text, "# AGENTS.md instructions") {
		return true
	}
	match := chatTagBlock.FindStringSubmatch(text)
	return match != nil
}

func (m *model) applyChatResult(msg chatResultMsg) {
	if !m.chat.open || msg.rootPID != m.chat.rootPID || msg.sessionID != m.chat.meta.ID {
		return
	}
	m.chat.loading = false
	if msg.err != nil {
		m.chat.err = sanitizeProcessCommand(msg.err.Error())
		return
	}
	m.chat.err = ""
	m.chat.loadedAt = msg.loadedAt
	if msg.reset {
		m.chat.messages = nil
	}
	m.chat.paths = msg.paths
	m.chat.offsets = msg.offsets
	m.chat.messages = append(m.chat.messages, msg.messages...)
	if extra := len(m.chat.messages) - chatMaxMessages; extra > 0 {
		m.chat.messages = append([]chatMessage(nil), m.chat.messages[extra:]...)
	}
	m.clampChatScroll()
}

// refreshChat reads lines appended since the last read.
func (m *model) refreshChat() tea.Cmd {
	if !m.chat.open || m.chat.loading {
		return nil
	}
	m.chat.loading = true
	return loadChat(m.chat.rootPID, m.chat.meta, m.chat.paths, m.chat.offsets)
}

func (m model) updateChat(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	page := max(m.chatPageRows()-1, 1)
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "c", "q", "backspace":
		m.chat = chatState{}
		return m, nil
	case "?":
		m.helpOpen = true
	case "up", "k":
		m.scrollChat(-1)
	case "down", "j":
		m.scrollChat(1)
	case "pgup", "b":
		m.scrollChat(-page)
	case "pgdown", " ", "f":
		m.scrollChat(page)
	case "home", "g":
		m.chat.follow = false
		m.chat.scroll = 0
	case "end", "G":
		m.chat.follow = true
		m.clampChatScroll()
	case "r":
		if !m.chat.loading {
			m.chat.loading = true
			return m, loadChat(m.chat.rootPID, m.chat.meta, nil, nil)
		}
	}
	return m, nil
}

func (m *model) scrollChat(delta int) {
	m.chat.follow = false
	m.chat.scroll += delta
	m.clampChatScroll()
}

func (m *model) clampChatScroll() {
	maxScroll := max(len(m.chatLines(m.chatInnerWidth()))-m.chatPageRows(), 0)
	if m.chat.follow || m.chat.scroll >= maxScroll {
		m.chat.scroll = maxScroll
		m.chat.follow = true
		return
	}
	m.chat.scroll = max(m.chat.scroll, 0)
}

func (m model) chatInnerWidth() int { return max(m.width-4, 1) }

// chatPageRows is the number of message lines visible inside the chat box.
func (m model) chatPageRows() int { return max(m.height-chromeRows-2, 1) }

// chatLines renders every message as a role header followed by its text
// wrapped to width, with a blank line between messages.
func (m model) chatLines(width int) []string {
	var lines []string
	assistant := m.chat.tool
	if assistant == "" {
		assistant = "Assistant"
	}
	for index, message := range m.chat.messages {
		if index > 0 {
			lines = append(lines, "")
		}
		label, color := "You", dashBlue
		if message.role == transcript.RoleAssistant {
			label, color = assistant, dashToolColor(m.chat.tool)
		}
		header := dashPaint("▍", color, true) + dashPaint(label, color, true)
		if message.ts != nil {
			header += dashPaint("  "+message.ts.Local().Format("2006-01-02 15:04"), usageMutedColor, false)
		}
		lines = append(lines, header)
		textColor := usageTextColor
		if message.role == transcript.RoleUser {
			textColor = usageBrightColor
		}
		for _, line := range chatWrap(message.text, max(width-2, 1)) {
			lines = append(lines, dashPaint("▍ ", color, false)+dashPaint(line, textColor, false))
		}
	}
	return lines
}

// chatWrap strips control characters, expands tabs, and wraps text to width.
func chatWrap(text string, width int) []string {
	var wrapped []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = sanitizeProcessCommand(strings.ReplaceAll(line, "\t", "    "))
		if line == "" {
			wrapped = append(wrapped, "")
			continue
		}
		wrapped = append(wrapped, strings.Split(ansi.Wrap(line, width, ""), "\n")...)
	}
	return wrapped
}

func (m model) viewChat(contentRows int) string {
	if contentRows < 1 {
		return ""
	}
	var body []string
	switch {
	case m.chat.err != "":
		body = []string{dashPaint("Chat history unavailable: "+m.chat.err, dashRed, false)}
	case len(m.chat.messages) == 0 && m.chat.loading:
		body = []string{dashPaint("Reading transcript…", usageMutedColor, false)}
	case len(m.chat.messages) == 0:
		body = []string{dashPaint("No user or assistant messages in this session yet.", usageMutedColor, false)}
	default:
		lines := m.chatLines(m.chatInnerWidth())
		start := min(m.chat.scroll, len(lines))
		body = lines[start:min(start+m.chatPageRows(), len(lines))]
	}
	title := "chat (" + sanitizeProcessCommand(m.chat.title) + ")"
	if contentRows < 3 {
		lines := make([]string, contentRows)
		for index := range lines {
			line := ""
			if index < len(body) {
				line = body[index]
			}
			lines[index] = dashFit(line, m.width, usagePanelColor)
		}
		return strings.Join(lines, "\n")
	}
	return strings.Join(dashBox(title, body, m.width, contentRows, dashToolColor(m.chat.tool)), "\n")
}

func (m model) chatFooter() (string, string) {
	keys := []struct{ key, action string }{
		{"↑↓", "scroll"}, {"pgup/pgdn", "page"}, {"g/G", "top/latest"}, {"r", "reload"}, {"esc", "back"},
	}
	help := dashPaint(" ", usageTextColor, false)
	for _, key := range keys {
		help += dashPaint(key.key+" ", dashBlue, true) + dashPaint(key.action+"  ", usageTextColor, false)
	}
	status := fmt.Sprintf("%d messages", len(m.chat.messages))
	if !m.chat.loadedAt.IsZero() {
		status += "  •  read " + m.chat.loadedAt.Format("15:04:05")
	}
	if m.chat.follow {
		status += "  •  following latest"
	} else {
		status += "  •  scrolled (G to follow)"
	}
	return dashFit(help, m.width, usagePanelColor), dashFit(dashPaint("  "+status, usageMutedColor, false), m.width, usagePanelColor)
}
