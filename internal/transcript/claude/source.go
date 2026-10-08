// Package claude reads Claude Code session transcripts as normalized
// transcript events.
//
// Each session is one JSONL file under $CLAUDE_CONFIG_DIR/projects/<encoded
// project path>/<session id>.jsonl; see NOTES.md for the record shapes and
// known gaps.
package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// maxToolText is the longest tool output kept in Event.Text.
const maxToolText = 64 << 10

// syntheticModel marks assistant messages Claude Code writes itself, such as
// API error notices, rather than ones a model produced.
const syntheticModel = "<synthetic>"

func init() {
	transcript.Register(transcript.ProviderClaude, &Source{})
}

// Source implements transcript.TranscriptSource for Claude Code transcripts.
// The zero value reads CLAUDE_CONFIG_DIR, falling back to ~/.claude.
type Source struct {
	// ConfigDir overrides CLAUDE_CONFIG_DIR when set.
	ConfigDir string
	// Home overrides the user's home directory when set.
	Home string
}

// Locate returns the transcript for meta. Discovery already maps running
// Claude Code processes to their transcript, so meta.RolloutPath wins when it
// names this session. Otherwise the session id is matched against file names
// in every project directory. Subagent transcripts are not returned; see
// NOTES.md.
func (s *Source) Locate(meta session.Meta) ([]string, error) {
	if meta.Provider != "" && meta.Provider != string(transcript.ProviderClaude) {
		return nil, nil
	}
	if !session.ValidUUID(meta.ID) {
		return nil, nil
	}
	if meta.RolloutPath != "" && strings.EqualFold(sessionIDFromPath(meta.RolloutPath), meta.ID) {
		if _, err := os.Stat(meta.RolloutPath); err == nil {
			return []string{meta.RolloutPath}, nil
		}
	}
	dir, err := s.configDir()
	if err != nil {
		return nil, err
	}
	matches, err := filepath.Glob(filepath.Join(dir, "projects", "*", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("claude: search transcripts: %w", err)
	}
	var paths []string
	for _, path := range matches {
		if strings.EqualFold(sessionIDFromPath(path), meta.ID) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (s *Source) configDir() (string, error) {
	if s.ConfigDir != "" {
		return s.ConfigDir, nil
	}
	if env := os.Getenv("CLAUDE_CONFIG_DIR"); env != "" {
		return env, nil
	}
	home := s.Home
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", errors.New("claude: find home directory")
		}
	}
	return filepath.Join(home, ".claude"), nil
}

// Read parses complete lines from fromOffset onward. Offsets are byte
// positions just past a newline; a trailing line without a newline is still
// being written and is left for the next read. Seq is the line index, so a
// read starting mid-file first scans the earlier lines to count them and to
// recover the tool names and token accounting that later records depend on.
func (s *Source) Read(path string, fromOffset int64) ([]transcript.Event, int64, error) {
	if fromOffset < 0 {
		return nil, 0, fmt.Errorf("claude: negative offset %d", fromOffset)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fromOffset, fmt.Errorf("claude: open transcript: %w", cause(err))
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fromOffset, fmt.Errorf("claude: stat transcript: %w", cause(err))
	}
	if fromOffset > info.Size() {
		return nil, fromOffset, fmt.Errorf("claude: offset %d is past end of transcript (%d bytes); file was truncated or rotated", fromOffset, info.Size())
	}

	p := parser{sessionID: sessionIDFromPath(path), toolNames: map[string]string{}, counted: map[string]bool{}}
	reader := bufio.NewReaderSize(file, 256<<10)
	var offset int64
	for offset < fromOffset {
		line, err := reader.ReadBytes('\n')
		offset += int64(len(line))
		if err != nil || offset > fromOffset {
			return nil, fromOffset, fmt.Errorf("claude: offset %d is not at a line boundary", fromOffset)
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
			return events, offset, fmt.Errorf("claude: read transcript: %w", cause(err))
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
	Type        string          `json:"type"`
	Subtype     string          `json:"subtype"`
	Timestamp   string          `json:"timestamp"`
	SessionID   string          `json:"sessionId"`
	IsSidechain bool            `json:"isSidechain"`
	IsMeta      bool            `json:"isMeta"`
	Content     json.RawMessage `json:"content"`
	Message     *message        `json:"message"`
}

type message struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   *usage          `json:"usage"`
}

type usage struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
}

// block is one entry of a message content array.
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

// parser carries the state later records depend on: the tool name for each
// tool_use id, and which API message ids already had their usage counted.
// Claude Code writes one line per content block and repeats the message's
// usage on each, so tokens are taken from the first line only.
type parser struct {
	sessionID string
	seq       int64
	toolNames map[string]string
	counted   map[string]bool
}

// scanPrefix updates parser state from a line before fromOffset without
// building an event. Only assistant lines affect state once the session id
// is known.
func (p *parser) scanPrefix(line []byte) {
	if p.sessionID != "" && !bytes.Contains(line, []byte(`"assistant"`)) {
		return
	}
	var rec record
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	p.track(rec)
}

// track records session id, tool names, and counted usage from rec and
// reports whether rec's usage should be counted on this line.
func (p *parser) track(rec record) bool {
	if p.sessionID == "" && session.ValidUUID(rec.SessionID) {
		p.sessionID = rec.SessionID
	}
	if rec.Type != "assistant" || rec.Message == nil {
		return false
	}
	for _, b := range blocks(rec.Message.Content) {
		if b.Type == "tool_use" && b.ID != "" {
			p.toolNames[b.ID] = b.Name
		}
	}
	if rec.Message.Usage == nil {
		return false
	}
	if rec.Message.ID == "" {
		return true
	}
	if p.counted[rec.Message.ID] {
		return false
	}
	p.counted[rec.Message.ID] = true
	return true
}

func (p *parser) parse(line []byte) transcript.Event {
	event := transcript.Event{
		SessionID: p.sessionID,
		Provider:  transcript.ProviderClaude,
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
	if ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
		event.TS = &ts
	}
	countUsage := p.track(rec)
	event.SessionID = p.sessionID

	switch rec.Type {
	case "user":
		p.user(&event, rec)
	case "assistant":
		p.assistant(&event, rec, countUsage)
	case "system":
		event.Role = transcript.RoleSystem
		event.Text = stringValue(rec.Content)
	}
	// Subagent work is kept, but as meta so it does not read as part of the
	// main conversation. Raw keeps the record's own isSidechain tag.
	if rec.IsSidechain {
		event.Role = transcript.RoleMeta
	}
	return event
}

func (p *parser) user(event *transcript.Event, rec record) {
	if rec.Message == nil {
		return
	}
	role := transcript.RoleUser
	if rec.IsMeta {
		// Context Claude Code injects on the user's behalf.
		role = transcript.RoleSystem
	}
	if text, ok := stringContent(rec.Message.Content); ok {
		event.Role = role
		event.Text = text
		return
	}
	content := blocks(rec.Message.Content)
	if content == nil {
		return
	}
	var texts, results []string
	for _, b := range content {
		switch b.Type {
		case "text":
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		case "tool_result":
			if event.ToolName == nil {
				event.ToolName = optional(p.toolNames[b.ToolUseID])
			}
			if text := resultText(b.Content); text != "" {
				results = append(results, text)
			}
		}
	}
	if hasType(content, "tool_result") {
		event.Role = transcript.RoleToolResult
		event.Text = truncate(strings.Join(append(results, texts...), "\n"))
		return
	}
	event.Role = role
	event.Text = strings.Join(texts, "\n")
}

func (p *parser) assistant(event *transcript.Event, rec record, countUsage bool) {
	if rec.Message == nil {
		return
	}
	if model := rec.Message.Model; model != syntheticModel {
		event.Model = optional(model)
	}
	if countUsage {
		event.Tokens = tokens(*rec.Message.Usage)
	}
	content := blocks(rec.Message.Content)
	if content == nil {
		if text, ok := stringContent(rec.Message.Content); ok {
			event.Role = transcript.RoleAssistant
			event.Text = text
		}
		return
	}
	var texts, thinking, inputs []string
	for _, b := range content {
		switch b.Type {
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
				event.ToolName = optional(b.Name)
			}
			if len(b.Input) > 0 {
				inputs = append(inputs, compact(b.Input))
			}
		}
	}
	switch {
	case hasType(content, "tool_use"):
		event.Role = transcript.RoleToolCall
		event.Text = strings.Join(append(texts, inputs...), "\n")
	case len(texts) > 0:
		event.Role = transcript.RoleAssistant
		event.Text = strings.Join(texts, "\n")
	default:
		// Thinking is not a reply, so it stays meta, like Codex reasoning.
		event.Text = strings.Join(thinking, "\n")
	}
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

func hasType(content []block, blockType string) bool {
	for _, b := range content {
		if b.Type == blockType {
			return true
		}
	}
	return false
}

func stringContent(raw json.RawMessage) (string, bool) {
	var text string
	if len(raw) == 0 || json.Unmarshal(raw, &text) != nil {
		return "", false
	}
	return text, true
}

func stringValue(raw json.RawMessage) string {
	text, _ := stringContent(raw)
	return text
}

// resultText reads a tool_result content, which is either a string or an
// array of blocks. Only text blocks are readable; images stay in Raw.
func resultText(raw json.RawMessage) string {
	if text, ok := stringContent(raw); ok {
		return text
	}
	var texts []string
	for _, b := range blocks(raw) {
		if b.Type == "text" && b.Text != "" {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n")
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

// tokens maps Anthropic usage onto the event contract. input_tokens excludes
// cached tokens, so cache reads and cache writes go to cache and the three
// counts add up to every token the request processed.
func tokens(u usage) transcript.Tokens {
	return transcript.Tokens{
		Input:  max(u.Input, 0),
		Output: max(u.Output, 0),
		Cache:  max(u.CacheRead, 0) + max(u.CacheCreation, 0),
	}
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
