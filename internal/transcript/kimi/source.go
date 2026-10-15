// Package kimi reads Kimi Code session transcripts as normalized transcript
// events.
//
// Each session is a directory $KIMI_CODE_HOME/sessions/<work dir key>/<session
// id>/ whose main agent writes agents/main/wire.jsonl; see NOTES.md for the
// record shapes and known gaps.
package kimi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// maxToolText is the longest tool output kept in Event.Text.
const maxToolText = 64 << 10

// maxIDLength bounds session directory names accepted as ids.
const maxIDLength = 128

func init() {
	reader.Register(transcript.ProviderKimi, &Source{})
}

// Source implements reader.Source for Kimi Code transcripts.
// The zero value reads KIMI_CODE_HOME, then KIMI_SHARE_DIR, falling back to
// ~/.kimi-code, as discovery does.
type Source struct {
	// Home overrides Kimi Code's data directory when set.
	Home string
}

// Locate returns the main agent's wire log for meta. Discovery already maps
// running Kimi Code processes to their session directory, so meta.RolloutPath
// wins when it names this session. Otherwise the session id is matched
// against session directories in every work directory bucket. Subagent logs
// are not returned; see NOTES.md.
func (s *Source) Locate(meta session.Meta) ([]string, error) {
	if meta.Provider != "" && meta.Provider != string(transcript.ProviderKimi) {
		return nil, nil
	}
	if !validID(meta.ID) {
		return nil, nil
	}
	if meta.RolloutPath != "" && sessionIDFromPath(meta.RolloutPath) == meta.ID {
		if info, err := os.Stat(meta.RolloutPath); err == nil && info.Mode().IsRegular() {
			return []string{meta.RolloutPath}, nil
		}
	}
	home, err := s.home()
	if err != nil {
		return nil, err
	}
	matches, err := filepath.Glob(session.KimiWirePath(filepath.Join(home, "sessions", "*", meta.ID)))
	if err != nil {
		return nil, errors.New("kimi: search transcripts failed")
	}
	// A session id names one directory; if two buckets hold it, the most
	// recently written log is the live one.
	var best string
	var bestTime time.Time
	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) || (info.ModTime().Equal(bestTime) && path < best) {
			best, bestTime = path, info.ModTime()
		}
	}
	if best == "" {
		return nil, nil
	}
	return []string{best}, nil
}

func (s *Source) home() (string, error) {
	if s.Home != "" {
		return s.Home, nil
	}
	home, err := session.KimiHome()
	if err != nil {
		return "", errors.New("kimi: find home directory")
	}
	return home, nil
}

// validID reports whether id can name a session directory. Kimi Code uses
// ids like session_<uuid>; anything that could escape the sessions tree or
// act as a glob pattern is rejected.
func validID(id string) bool {
	if id == "" || len(id) > maxIDLength || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// sessionIDFromPath returns the session directory name of a main agent wire
// log, or "" when path does not end in <session id>/agents/main/wire.jsonl.
func sessionIDFromPath(path string) string {
	path = filepath.Clean(path)
	if filepath.Base(path) != "wire.jsonl" {
		return ""
	}
	agent := filepath.Dir(path)
	agents := filepath.Dir(agent)
	if filepath.Base(agent) != "main" || filepath.Base(agents) != "agents" {
		return ""
	}
	id := filepath.Base(filepath.Dir(agents))
	if !validID(id) {
		return ""
	}
	return id
}

// Read parses complete lines from fromOffset onward. Offsets are byte
// positions just past a newline; a trailing line without a newline is still
// being written and is left for the next read. Seq is the line index, so a
// read starting mid-file first scans the earlier lines to count them and to
// recover the tool names and model that later records depend on.
func (s *Source) Read(path string, fromOffset int64) ([]transcript.Event, int64, error) {
	if fromOffset < 0 {
		return nil, 0, fmt.Errorf("kimi: negative offset %d", fromOffset)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fromOffset, fmt.Errorf("kimi: open transcript: %w", cause(err))
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fromOffset, fmt.Errorf("kimi: stat transcript: %w", cause(err))
	}
	if fromOffset > info.Size() {
		return nil, fromOffset, fmt.Errorf("kimi: offset %d is past end of transcript (%d bytes); file was truncated or rewritten", fromOffset, info.Size())
	}

	p := parser{sessionID: sessionIDFromPath(path), toolNames: map[string]string{}}
	reader := bufio.NewReaderSize(file, 256<<10)
	var offset int64
	for offset < fromOffset {
		line, err := reader.ReadBytes('\n')
		offset += int64(len(line))
		if err != nil || offset > fromOffset {
			return nil, fromOffset, fmt.Errorf("kimi: offset %d is not at a line boundary", fromOffset)
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
			return events, offset, fmt.Errorf("kimi: read transcript: %w", cause(err))
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

// record is the wire log line envelope: {"type", ...payload, "time"}. Only
// the fields the parser reads are decoded; Raw keeps the rest.
type record struct {
	Type      string          `json:"type"`
	Time      json.RawMessage `json:"time"`
	CreatedAt json.RawMessage `json:"created_at"`
	Message   *message        `json:"message"`
	Event     *loopEvent      `json:"event"`
	Model     string          `json:"model"`
	Usage     *usage          `json:"usage"`
	// context.apply_compaction: summary is a string, or a whole message in
	// one schema variant.
	Summary        json.RawMessage `json:"summary"`
	ContextSummary string          `json:"contextSummary"`
}

// message is a ContextMessage.
type message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []toolCall      `json:"toolCalls"`
	ToolCallID string          `json:"toolCallId"`
	Origin     json.RawMessage `json:"origin"`
}

// toolCall is a ContextMessage tool call; arguments is a JSON string or null.
type toolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// loopEvent is a LoopRecordedEvent.
type loopEvent struct {
	Type       string          `json:"type"`
	Part       *part           `json:"part"`
	ToolCallID string          `json:"toolCallId"`
	Name       string          `json:"name"`
	Args       json.RawMessage `json:"args"`
	Result     *struct {
		Output json.RawMessage `json:"output"`
	} `json:"result"`
}

// part is one ContentPart.
type part struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Think string `json:"think"`
}

type usage struct {
	InputOther         int64 `json:"inputOther"`
	Output             int64 `json:"output"`
	InputCacheRead     int64 `json:"inputCacheRead"`
	InputCacheCreation int64 `json:"inputCacheCreation"`
}

// parser carries the state later records depend on: the tool name for each
// tool call id, and the model of the latest llm.request, since usage.record
// carries only a configuration alias.
type parser struct {
	sessionID string
	seq       int64
	model     string
	toolNames map[string]string
}

// scanPrefix updates parser state from a line before fromOffset without
// building an event. Only model requests and tool calls affect state.
func (p *parser) scanPrefix(line []byte) {
	if !bytes.Contains(line, []byte(`"llm.request"`)) && !bytes.Contains(line, []byte(`"tool.call"`)) && !bytes.Contains(line, []byte(`"toolCalls"`)) {
		return
	}
	var rec record
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	p.track(rec)
}

func (p *parser) track(rec record) {
	switch rec.Type {
	case "llm.request":
		if model := strings.TrimSpace(rec.Model); model != "" {
			p.model = model
		}
	case "context.append_loop_event":
		if rec.Event != nil && rec.Event.Type == "tool.call" && rec.Event.ToolCallID != "" {
			p.toolNames[rec.Event.ToolCallID] = rec.Event.Name
		}
	case "context.append_message":
		if rec.Message != nil {
			for _, call := range rec.Message.ToolCalls {
				if call.ID != "" {
					p.toolNames[call.ID] = call.Name
				}
			}
		}
	}
}

func (p *parser) parse(line []byte) transcript.Event {
	event := transcript.Event{
		SessionID: p.sessionID,
		Provider:  transcript.ProviderKimi,
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
	event.TS = timestamp(rec.Time)
	p.track(rec)

	switch rec.Type {
	case "metadata":
		event.TS = timestamp(rec.CreatedAt)
	case "context.append_message":
		p.message(&event, rec.Message)
	case "context.append_loop_event":
		p.loopEvent(&event, rec.Event)
	case "context.apply_compaction":
		event.Role = transcript.RoleSystem
		event.Text = compactionText(rec)
	case "llm.request":
		event.Model = optional(strings.TrimSpace(rec.Model))
	case "usage.record":
		// Every provider call writes one usage.record, including compaction
		// and title calls that have no loop step, so token totals come from
		// here alone; step.end repeats the turn's counts.
		if rec.Usage != nil {
			event.Tokens = tokens(*rec.Usage)
		}
		event.Model = optional(session.FirstNonEmpty(p.model, modelID(rec.Model)))
	}
	return event
}

func (p *parser) message(event *transcript.Event, m *message) {
	if m == nil {
		return
	}
	content := parts(m.Content)
	switch m.Role {
	case "user":
		event.Role = transcript.RoleSystem
		if userTyped(m.Origin) {
			event.Role = transcript.RoleUser
		}
		event.Text = joinText(content)
	case "assistant":
		// Live sessions record replies as loop events; this form comes from
		// sessions migrated from older Kimi releases.
		event.Model = optional(p.model)
		text := joinText(content)
		if len(m.ToolCalls) > 0 {
			event.Role = transcript.RoleToolCall
			event.ToolName = optional(m.ToolCalls[0].Name)
			texts := []string{}
			if text != "" {
				texts = append(texts, text)
			}
			for _, call := range m.ToolCalls {
				if args := argumentsText(call.Arguments); args != "" {
					texts = append(texts, args)
				}
			}
			event.Text = strings.Join(texts, "\n")
			return
		}
		if text != "" {
			event.Role = transcript.RoleAssistant
			event.Text = text
			return
		}
		// Thinking alone is not a reply, so it stays meta.
		event.Text = joinThink(content)
	case "tool":
		event.Role = transcript.RoleToolResult
		event.ToolName = optional(p.toolNames[m.ToolCallID])
		event.Text = truncate(outputText(m.Content))
	case "system":
		event.Role = transcript.RoleSystem
		event.Text = joinText(content)
	}
}

func (p *parser) loopEvent(event *transcript.Event, e *loopEvent) {
	if e == nil {
		return
	}
	switch e.Type {
	case "content.part":
		if e.Part == nil {
			return
		}
		event.Model = optional(p.model)
		switch e.Part.Type {
		case "text":
			event.Role = transcript.RoleAssistant
			event.Text = e.Part.Text
		case "think":
			// Thinking is not a reply, so it stays meta, like Claude's.
			event.Text = e.Part.Think
		}
	case "tool.call":
		event.Role = transcript.RoleToolCall
		event.ToolName = optional(e.Name)
		event.Model = optional(p.model)
		if len(e.Args) > 0 && !bytes.Equal(e.Args, []byte("null")) {
			event.Text = compact(e.Args)
		}
	case "tool.result":
		event.Role = transcript.RoleToolResult
		event.ToolName = optional(p.toolNames[e.ToolCallID])
		if e.Result != nil {
			event.Text = truncate(outputText(e.Result.Output))
		}
	}
}

// userTyped reports whether a user message's origin is something a person
// typed: no origin, "user", a shell command, or a slash command. Origins are
// a string in the published manifest and an object with a kind in recorded
// logs, so both are read. Injected context, retries, compaction summaries,
// task and cron notifications, and hook results are the harness's, not the
// user's.
func userTyped(raw json.RawMessage) bool {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return true
	}
	var origin struct {
		Kind    string `json:"kind"`
		Trigger string `json:"trigger"`
	}
	if raw[0] == '"' {
		if json.Unmarshal(raw, &origin.Kind) != nil {
			return false
		}
	} else if json.Unmarshal(raw, &origin) != nil {
		return false
	}
	switch origin.Kind {
	case "", "user", "shell_command":
		return true
	case "skill_activation", "plugin_command":
		return origin.Trigger == "user-slash"
	default:
		return false
	}
}

// parts decodes a ContentPart array. It returns nil when raw is not one.
func parts(raw json.RawMessage) []part {
	var content []part
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &content) != nil {
		return nil
	}
	return content
}

// joinText concatenates text parts the way Kimi Code renders a message.
func joinText(content []part) string {
	var b strings.Builder
	for _, c := range content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func joinThink(content []part) string {
	var b strings.Builder
	for _, c := range content {
		if c.Type == "think" {
			b.WriteString(c.Think)
		}
	}
	return b.String()
}

// outputText reads a tool output, which is a string or an array of content
// parts. Only text parts are readable; media stays in Raw.
func outputText(raw json.RawMessage) string {
	var text string
	if len(raw) > 0 && json.Unmarshal(raw, &text) == nil {
		return text
	}
	return joinText(parts(raw))
}

func compactionText(rec record) string {
	var summary string
	if len(rec.Summary) > 0 && json.Unmarshal(rec.Summary, &summary) == nil && summary != "" {
		return summary
	}
	if rec.ContextSummary != "" {
		return rec.ContextSummary
	}
	var m message
	if len(rec.Summary) > 0 && json.Unmarshal(rec.Summary, &m) == nil {
		return joinText(parts(m.Content))
	}
	return ""
}

// argumentsText returns tool call arguments, which ContextMessage stores as a
// JSON-encoded string.
func argumentsText(raw json.RawMessage) string {
	var text string
	if len(raw) > 0 && json.Unmarshal(raw, &text) == nil {
		return text
	}
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	return compact(raw)
}

// modelID drops the placeholder alias Kimi Code records when the model comes
// from the environment, which names no model.
func modelID(alias string) string {
	alias = strings.TrimSpace(alias)
	if strings.HasPrefix(alias, "__") && strings.HasSuffix(alias, "__") {
		return ""
	}
	return alias
}

// timestamp reads Kimi Code's epoch-millisecond times. Values too small to
// be milliseconds are taken as seconds, as Kimi Code's own reader does.
func timestamp(raw json.RawMessage) *time.Time {
	if len(raw) == 0 {
		return nil
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		if n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil
		}
		if n < 1e12 {
			n *= 1000
		}
		if n > maxMillis {
			return nil
		}
		ts := time.UnixMilli(int64(n)).UTC()
		return &ts
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if ts, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return &ts
		}
	}
	return nil
}

// maxMillis bounds timestamps to year 9999 so bad input cannot overflow.
const maxMillis = 253402300799999

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

// tokens maps Kimi usage onto the event contract. inputOther excludes cached
// tokens, so cache reads and cache writes go to cache and the three counts
// add up to every token the request processed.
func tokens(u usage) transcript.Tokens {
	return transcript.Tokens{
		Input:  max(u.InputOther, 0),
		Output: max(u.Output, 0),
		Cache:  max(u.InputCacheRead, 0) + max(u.InputCacheCreation, 0),
	}
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
