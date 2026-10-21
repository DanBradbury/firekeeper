// Package cursor reads Cursor CLI agent transcripts as normalized transcript
// events.
//
// Each session is one JSONL file under $CURSOR_CONFIG_DIR/projects/<encoded
// workspace path>/agent-transcripts/<id>/<id>.jsonl (default ~/.cursor); see
// NOTES.md for the record shapes and known gaps.
package cursor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// maxToolText is the longest tool output kept in Event.Text.
const maxToolText = 64 << 10

// Cursor CLI wraps the typed prompt in these tags, after any context it adds.
const (
	userQueryOpen  = "<user_query>"
	userQueryClose = "</user_query>"
)

func init() {
	reader.Register(transcript.ProviderCursor, &Source{})
}

// Source implements reader.Source for Cursor CLI transcripts.
// The zero value reads CURSOR_CONFIG_DIR, falling back to ~/.cursor.
type Source struct {
	// Dir overrides the Cursor data directory when set.
	Dir string
}

func (s *Source) dir() (string, error) {
	if s.Dir != "" {
		return s.Dir, nil
	}
	dir, err := session.CursorDir()
	if err != nil {
		return "", errors.New("cursor: find home directory")
	}
	return dir, nil
}

// Locate returns the transcript for meta. Discovery already maps running
// Cursor CLI processes to their transcript, so meta.RolloutPath wins when it
// names this session. Otherwise the id is searched for in every project
// directory. Subagent transcripts are not returned; see NOTES.md.
func (s *Source) Locate(meta session.Meta) ([]string, error) {
	if meta.Provider != "" && meta.Provider != string(transcript.ProviderCursor) {
		return nil, nil
	}
	if !session.ValidUUID(meta.ID) {
		return nil, nil
	}
	if meta.RolloutPath != "" && strings.EqualFold(session.CursorTranscriptID(meta.RolloutPath), meta.ID) {
		if _, err := os.Stat(meta.RolloutPath); err == nil {
			return []string{meta.RolloutPath}, nil
		}
	}
	dir, err := s.dir()
	if err != nil {
		return nil, err
	}
	if path := session.FindCursorTranscript(dir, meta.ID); path != "" {
		return []string{path}, nil
	}
	return nil, nil
}

// Read parses complete lines from fromOffset onward. Offsets are byte
// positions just past a newline; a trailing line without a newline is still
// being written and is left for the next read. Seq is the line index, so a
// read starting mid-file first scans the earlier lines to count them and to
// recover the tool names later tool results refer to.
func (s *Source) Read(path string, fromOffset int64) ([]transcript.Event, int64, error) {
	if fromOffset < 0 {
		return nil, 0, fmt.Errorf("cursor: negative offset %d", fromOffset)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fromOffset, fmt.Errorf("cursor: open transcript: %w", cause(err))
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fromOffset, fmt.Errorf("cursor: stat transcript: %w", cause(err))
	}
	if fromOffset > info.Size() {
		return nil, fromOffset, fmt.Errorf("cursor: offset %d is past end of transcript (%d bytes); file was truncated or rotated", fromOffset, info.Size())
	}

	p := parser{sessionID: sessionIDFromPath(path), toolNames: map[string]string{}}
	reader := bufio.NewReaderSize(file, 256<<10)
	var offset int64
	for offset < fromOffset {
		line, err := reader.ReadBytes('\n')
		offset += int64(len(line))
		if err != nil || offset > fromOffset {
			return nil, fromOffset, fmt.Errorf("cursor: offset %d is not at a line boundary", fromOffset)
		}
		p.scanPrefix(line)
		p.seq++
	}

	var events []transcript.Event
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return events, offset, nil
			}
			return events, offset, fmt.Errorf("cursor: read transcript: %w", cause(err))
		}
		offset += int64(len(line))
		events = append(events, p.parse(bytes.TrimRight(line, "\r\n")))
		p.seq++
	}
}

// cause drops the path from filesystem errors so messages stay free of
// machine-specific locations.
func cause(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

// sessionIDFromPath returns the uuid file name of a transcript, or "".
// Records carry no session id.
func sessionIDFromPath(path string) string {
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if !session.ValidUUID(id) {
		return ""
	}
	return id
}

// record is the transcript line envelope. Only the fields the parser reads
// are decoded; Raw keeps the rest.
type record struct {
	Role    string   `json:"role"`
	Type    string   `json:"type"`
	Status  string   `json:"status"`
	Error   string   `json:"error"`
	Model   string   `json:"model"`
	Message *message `json:"message"`
}

type message struct {
	Content json.RawMessage `json:"content"`
	Text    string          `json:"text"`
}

// block is one entry of a message content array. Cursor has written the
// tool block types with underscores and with hyphens.
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Tool      string          `json:"tool"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	Output    json.RawMessage `json:"output"`
}

func (b block) kind() string {
	switch t := strings.ToLower(b.Type); t {
	case "tool_use", "tool-use", "tool_call", "tool-call":
		return "tool_use"
	case "tool_result", "tool-result":
		return "tool_result"
	default:
		return t
	}
}

func (b block) toolName() string { return session.FirstNonEmpty(b.Name, b.Tool) }

// parser carries the tool name for each tool_use id, so a tool result that
// names only the id gets its tool.
type parser struct {
	sessionID string
	seq       int64
	toolNames map[string]string
}

// scanPrefix updates parser state from a line before fromOffset without
// building an event.
func (p *parser) scanPrefix(line []byte) {
	if !bytes.Contains(line, []byte(`"assistant"`)) {
		return
	}
	var rec record
	if json.Unmarshal(line, &rec) == nil {
		p.track(rec)
	}
}

func (p *parser) track(rec record) {
	if !strings.EqualFold(rec.Role, "assistant") || rec.Message == nil {
		return
	}
	for _, b := range blocks(rec.Message.Content) {
		if b.kind() == "tool_use" && b.ID != "" {
			p.toolNames[b.ID] = b.toolName()
		}
	}
}

func (p *parser) parse(line []byte) transcript.Event {
	event := transcript.Event{
		SessionID: p.sessionID,
		Provider:  transcript.ProviderCursor,
		Seq:       p.seq,
		Role:      transcript.RoleMeta,
	}
	if !json.Valid(line) {
		// Keep unreadable lines, quoted so Raw stays valid JSON.
		event.Raw, _ = json.Marshal(string(line))
		return event
	}
	event.Raw = json.RawMessage(bytes.Clone(line))

	var rec record
	if json.Unmarshal(line, &rec) != nil {
		return event
	}
	p.track(rec)
	event.Model = optional(rec.Model)

	switch strings.ToLower(rec.Role) {
	case "user", "human":
		p.user(&event, rec)
	case "assistant", "model":
		p.assistant(&event, rec)
	case "system":
		event.Role = transcript.RoleSystem
		event.Text = messageText(rec.Message)
	case "":
		if rec.Type == "turn_ended" && rec.Status == "error" && rec.Error != "" {
			event.Role = transcript.RoleSystem
			event.Text = rec.Error
		}
	}
	return event
}

func (p *parser) user(event *transcript.Event, rec record) {
	if rec.Message == nil {
		return
	}
	content := blocks(rec.Message.Content)
	if content == nil {
		event.Role = transcript.RoleUser
		event.Text = userQuery(messageText(rec.Message))
		return
	}
	var texts, results []string
	for _, b := range content {
		switch b.kind() {
		case "text":
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		case "tool_result":
			if event.ToolName == nil {
				event.ToolName = optional(session.FirstNonEmpty(b.toolName(), p.toolNames[b.ToolUseID]))
			}
			if text := resultText(b); text != "" {
				results = append(results, text)
			}
		}
	}
	if hasKind(content, "tool_result") {
		event.Role = transcript.RoleToolResult
		event.Text = truncate(strings.Join(append(results, texts...), "\n"))
		return
	}
	event.Role = transcript.RoleUser
	event.Text = userQuery(strings.Join(texts, "\n"))
}

func (p *parser) assistant(event *transcript.Event, rec record) {
	if rec.Message == nil {
		return
	}
	content := blocks(rec.Message.Content)
	if content == nil {
		if text := messageText(rec.Message); text != "" {
			event.Role = transcript.RoleAssistant
			event.Text = text
		}
		return
	}
	var texts, thinking, inputs []string
	for _, b := range content {
		switch b.kind() {
		case "text":
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		case "thinking":
			if b.Thinking != "" {
				thinking = append(thinking, b.Thinking)
			}
		case "tool_use":
			if event.ToolName == nil {
				event.ToolName = optional(b.toolName())
			}
			if len(b.Input) > 0 {
				inputs = append(inputs, compact(b.Input))
			}
		default:
			// Unknown blocks with visible text still read as the reply.
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
	}
	switch {
	case hasKind(content, "tool_use"):
		event.Role = transcript.RoleToolCall
		event.Text = strings.Join(append(texts, inputs...), "\n")
	case len(texts) > 0:
		event.Role = transcript.RoleAssistant
		event.Text = strings.Join(texts, "\n")
	default:
		// Thinking is not a reply, so it stays meta, like Claude Code's.
		event.Text = strings.Join(thinking, "\n")
	}
}

// userQuery returns the prompt inside Cursor's <user_query> tags, which
// follow context Cursor CLI adds, or text unchanged when it has none.
func userQuery(text string) string {
	start := strings.Index(text, userQueryOpen)
	if start < 0 {
		return text
	}
	inner := text[start+len(userQueryOpen):]
	if end := strings.LastIndex(inner, userQueryClose); end >= 0 {
		inner = inner[:end]
	}
	return strings.TrimSpace(inner)
}

// blocks decodes a content array. It returns nil when content is not one.
func blocks(raw json.RawMessage) []block {
	var content []block
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &content) != nil {
		return nil
	}
	if content == nil {
		content = []block{}
	}
	return content
}

func hasKind(content []block, kind string) bool {
	for _, b := range content {
		if b.kind() == kind {
			return true
		}
	}
	return false
}

// messageText reads a message whose content is a plain string.
func messageText(m *message) string {
	if m == nil {
		return ""
	}
	if text, ok := stringContent(m.Content); ok {
		return text
	}
	return m.Text
}

func stringContent(raw json.RawMessage) (string, bool) {
	var text string
	if len(raw) == 0 || json.Unmarshal(raw, &text) != nil {
		return "", false
	}
	return text, true
}

// resultText reads a tool result's content or output, each either a string
// or an array of blocks. Only text blocks are readable; the rest stays in
// Raw.
func resultText(b block) string {
	for _, raw := range []json.RawMessage{b.Content, b.Output} {
		if text, ok := stringContent(raw); ok {
			return text
		}
		var texts []string
		for _, inner := range blocks(raw) {
			if inner.kind() == "text" && inner.Text != "" {
				texts = append(texts, inner.Text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}
	return ""
}

func compact(raw json.RawMessage) string {
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return string(raw)
	}
	return out.String()
}

func truncate(text string) string {
	if len(text) <= maxToolText {
		return text
	}
	cut := maxToolText
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return fmt.Sprintf("%s\n[truncated %d bytes]", text[:cut], len(text)-cut)
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
