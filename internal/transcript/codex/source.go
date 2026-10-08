// Package codex reads Codex rollout files as normalized transcript events.
//
// Rollouts under $CODEX_HOME/sessions are the complete conversation record;
// see NOTES.md for the record shapes and known gaps.
package codex

import (
	"bufio"
	"bytes"
	"context"
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

func init() {
	transcript.Register(transcript.ProviderCodex, &Source{})
}

// Source implements transcript.TranscriptSource for Codex rollouts. The zero
// value reads CODEX_HOME, falling back to ~/.codex.
type Source struct {
	// CodexHome overrides CODEX_HOME when set.
	CodexHome string
	// Home overrides the user's home directory when set.
	Home string

	// open and run replace os.Open and command execution in tests.
	open func(name string) (io.ReadCloser, error)
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Locate returns the rollout for meta. Discovery already maps running Codex
// processes to their open rollout files, so meta.RolloutPath wins when set.
// Otherwise the session id is matched against rollout file names under
// sessions/ and archived_sessions/, the same way open rollouts are matched.
func (s *Source) Locate(meta session.Meta) ([]string, error) {
	if meta.Provider != "" && meta.Provider != string(transcript.ProviderCodex) {
		return nil, nil
	}
	if meta.RolloutPath != "" {
		if id, ok := session.ThreadIDFromRolloutPath(meta.RolloutPath); ok && (meta.ID == "" || strings.EqualFold(id, meta.ID)) {
			if _, err := os.Stat(meta.RolloutPath); err == nil {
				return []string{meta.RolloutPath}, nil
			}
		}
	}
	if !session.ValidUUID(meta.ID) {
		return nil, nil
	}
	home, err := s.codexHome()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, pattern := range []string{
		filepath.Join(home, "sessions", "*", "*", "*", "rollout-*.jsonl"),
		filepath.Join(home, "archived_sessions", "rollout-*.jsonl"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("codex: search rollouts: %w", err)
		}
		for _, path := range matches {
			if id, ok := session.ThreadIDFromRolloutPath(path); ok && strings.EqualFold(id, meta.ID) {
				paths = append(paths, path)
			}
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (s *Source) codexHome() (string, error) {
	if s.CodexHome != "" {
		return s.CodexHome, nil
	}
	if env := os.Getenv("CODEX_HOME"); env != "" {
		return env, nil
	}
	home := s.Home
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", errors.New("codex: find home directory")
		}
	}
	return filepath.Join(home, ".codex"), nil
}

// Read parses complete lines from fromOffset onward. Offsets are byte
// positions just past a newline; a trailing line without a newline is still
// being written and is left for the next read. Seq is the line index, so a
// read starting mid-file first scans the earlier lines to count them and to
// recover the model and tool names that later records refer to.
func (s *Source) Read(path string, fromOffset int64) ([]transcript.Event, int64, error) {
	if fromOffset < 0 {
		return nil, 0, fmt.Errorf("codex: negative offset %d", fromOffset)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fromOffset, fmt.Errorf("codex: open rollout: %w", cause(err))
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fromOffset, fmt.Errorf("codex: stat rollout: %w", cause(err))
	}
	if fromOffset > info.Size() {
		return nil, fromOffset, fmt.Errorf("codex: offset %d is past end of rollout (%d bytes); file was truncated or rotated", fromOffset, info.Size())
	}

	p := parser{sessionID: sessionIDFromPath(path), toolNames: map[string]string{}}
	reader := bufio.NewReaderSize(file, 256<<10)
	var offset int64
	for offset < fromOffset {
		line, err := reader.ReadBytes('\n')
		offset += int64(len(line))
		if err != nil || offset > fromOffset {
			return nil, fromOffset, fmt.Errorf("codex: offset %d is not at a line boundary", fromOffset)
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
			return events, offset, fmt.Errorf("codex: read rollout: %w", cause(err))
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

func sessionIDFromPath(path string) string {
	id, _ := session.ThreadIDFromRolloutPath(path)
	return id
}

// record is the rollout line envelope: {"timestamp","type","payload"}.
type record struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type payload struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Role      string          `json:"role"`
	Name      string          `json:"name"`
	CallID    string          `json:"call_id"`
	Arguments string          `json:"arguments"`
	Input     string          `json:"input"`
	Output    json.RawMessage `json:"output"`
	Content   json.RawMessage `json:"content"`
	Summary   json.RawMessage `json:"summary"`
	Model     string          `json:"model"`
	Info      *struct {
		Last *usage `json:"last_token_usage"`
	} `json:"info"`
}

type usage struct {
	Input  int64 `json:"input_tokens"`
	Cached int64 `json:"cached_input_tokens"`
	Output int64 `json:"output_tokens"`
}

type textPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// parser carries the state later records depend on: the current model from
// turn_context and the tool name for each call id.
type parser struct {
	sessionID string
	seq       int64
	model     string
	toolNames map[string]string
}

// scanPrefix updates parser state from a line before fromOffset without
// building an event. Lines that cannot affect state are skipped undecoded.
func (p *parser) scanPrefix(line []byte) {
	if !bytes.Contains(line, []byte(`"turn_context"`)) && !bytes.Contains(line, []byte(`"call_id"`)) &&
		!bytes.Contains(line, []byte(`"session_meta"`)) {
		return
	}
	var rec record
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	var body payload
	if json.Unmarshal(rec.Payload, &body) != nil {
		return
	}
	p.track(rec.Type, body)
}

func (p *parser) track(recordType string, body payload) {
	switch {
	case recordType == "session_meta" && p.sessionID == "" && session.ValidUUID(body.ID):
		p.sessionID = body.ID
	case recordType == "turn_context" && body.Model != "":
		p.model = body.Model
	case recordType == "response_item" && isToolCall(body.Type) && body.CallID != "":
		p.toolNames[body.CallID] = body.Name
	}
}

func isToolCall(payloadType string) bool {
	return payloadType == "function_call" || payloadType == "custom_tool_call"
}

func isToolOutput(payloadType string) bool {
	return payloadType == "function_call_output" || payloadType == "custom_tool_call_output"
}

func (p *parser) parse(line []byte) transcript.Event {
	event := transcript.Event{
		SessionID: p.sessionID,
		Provider:  transcript.ProviderCodex,
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
	var body payload
	if len(rec.Payload) > 0 && json.Unmarshal(rec.Payload, &body) != nil {
		return event
	}
	p.track(rec.Type, body)
	event.SessionID = p.sessionID

	switch rec.Type {
	case "turn_context":
		event.Model = optional(p.model)
	case "event_msg":
		if body.Type == "token_count" && body.Info != nil && body.Info.Last != nil {
			event.Tokens = tokens(*body.Info.Last)
			event.Model = optional(p.model)
		}
	case "response_item":
		p.responseItem(&event, body)
	}
	return event
}

func (p *parser) responseItem(event *transcript.Event, body payload) {
	switch {
	case body.Type == "message":
		switch body.Role {
		case "user":
			event.Role = transcript.RoleUser
		case "assistant":
			event.Role = transcript.RoleAssistant
			event.Model = optional(p.model)
		case "developer", "system":
			event.Role = transcript.RoleSystem
		default:
			return
		}
		event.Text = joinText(body.Content)
	case body.Type == "reasoning":
		// Reasoning is usually encrypted; only the optional summary is
		// readable, and it is not a reply, so it stays meta.
		event.Text = joinText(body.Summary)
	case isToolCall(body.Type):
		event.Role = transcript.RoleToolCall
		event.ToolName = optional(body.Name)
		event.Text = body.Arguments
		if body.Type == "custom_tool_call" {
			event.Text = body.Input
		}
	case isToolOutput(body.Type):
		event.Role = transcript.RoleToolResult
		event.ToolName = optional(p.toolNames[body.CallID])
		event.Text = truncate(outputText(body.Output))
	}
}

// joinText concatenates the text parts of a content or summary array.
func joinText(raw json.RawMessage) string {
	var parts []textPart
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// outputText reads a tool output, which is either a string or an array of
// text parts.
func outputText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return joinText(raw)
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

// tokens maps OpenAI usage onto the event contract. Codex input_tokens
// includes cached tokens, so input here is the uncached remainder and the
// three counts add up to the reported total.
func tokens(u usage) transcript.Tokens {
	return transcript.Tokens{
		Input:  max(u.Input-u.Cached, 0),
		Output: u.Output,
		Cache:  u.Cached,
	}
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
