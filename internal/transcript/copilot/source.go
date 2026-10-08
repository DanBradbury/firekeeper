// Package copilot reads GitHub Copilot CLI session event logs as normalized
// transcript events.
//
// Each session directory under $COPILOT_HOME/session-state holds an
// append-only events.jsonl that is the complete conversation record; see
// NOTES.md for the record shapes and known gaps.
package copilot

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
	"time"
	"unicode/utf8"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// maxToolText is the longest tool output kept in Event.Text.
const maxToolText = 64 << 10

const eventsFile = "events.jsonl"

func init() {
	transcript.Register(transcript.ProviderCopilot, &Source{})
}

// Source implements transcript.TranscriptSource for Copilot CLI sessions. The
// zero value reads COPILOT_HOME, falling back to ~/.copilot.
type Source struct {
	// CopilotHome overrides COPILOT_HOME when set.
	CopilotHome string
	// Home overrides the user's home directory when set.
	Home string

	// open and run replace os.Open and command execution in tests.
	open func(name string) (io.ReadCloser, error)
}

// Locate returns the events.jsonl for meta. Discovery already sets
// meta.RolloutPath to the session's events file, so it wins when it belongs
// to the same session. Otherwise the file is looked up by session id under
// session-state/.
func (s *Source) Locate(meta session.Meta) ([]string, error) {
	if meta.Provider != "" && meta.Provider != string(transcript.ProviderCopilot) {
		return nil, nil
	}
	if meta.RolloutPath != "" && filepath.Base(meta.RolloutPath) == eventsFile {
		if id, ok := session.CopilotSessionIDFromStatePath(meta.RolloutPath); ok && (meta.ID == "" || strings.EqualFold(id, meta.ID)) {
			if _, err := os.Stat(meta.RolloutPath); err == nil {
				return []string{meta.RolloutPath}, nil
			}
		}
	}
	if !session.ValidUUID(meta.ID) {
		return nil, nil
	}
	home, err := s.copilotHome()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, "session-state", strings.ToLower(meta.ID), eventsFile)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("copilot: find events file: %w", cause(err))
	}
	return []string{path}, nil
}

func (s *Source) copilotHome() (string, error) {
	if s.CopilotHome != "" {
		return s.CopilotHome, nil
	}
	if env := strings.TrimSpace(os.Getenv("COPILOT_HOME")); env != "" {
		return env, nil
	}
	home := s.Home
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", errors.New("copilot: find home directory")
		}
	}
	return filepath.Join(home, ".copilot"), nil
}

// Read parses complete lines from fromOffset onward. Offsets are byte
// positions just past a newline; a trailing line without a newline is still
// being written and is left for the next read. Seq is the line index, so a
// read starting mid-file first scans the earlier lines to count them and to
// recover the model and tool names that later records refer to.
func (s *Source) Read(path string, fromOffset int64) ([]transcript.Event, int64, error) {
	if fromOffset < 0 {
		return nil, 0, fmt.Errorf("copilot: negative offset %d", fromOffset)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fromOffset, fmt.Errorf("copilot: open events: %w", cause(err))
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fromOffset, fmt.Errorf("copilot: stat events: %w", cause(err))
	}
	if fromOffset > info.Size() {
		return nil, fromOffset, fmt.Errorf("copilot: offset %d is past end of events (%d bytes); file was truncated or rotated", fromOffset, info.Size())
	}

	p := parser{sessionID: sessionIDForPath(path), toolNames: map[string]string{}}
	reader := bufio.NewReaderSize(file, 256<<10)
	var offset int64
	for offset < fromOffset {
		line, err := reader.ReadBytes('\n')
		offset += int64(len(line))
		if err != nil || offset > fromOffset {
			return nil, fromOffset, fmt.Errorf("copilot: offset %d is not at a line boundary", fromOffset)
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
			return events, offset, fmt.Errorf("copilot: read events: %w", cause(err))
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

// sessionIDForPath takes the id from the session directory name, then from
// the workspace.yaml beside the events file. An empty result leaves
// session.start to supply it.
func sessionIDForPath(path string) string {
	if id, ok := session.CopilotSessionIDFromStatePath(path); ok {
		return id
	}
	workspace, err := session.ReadCopilotWorkspace(filepath.Join(filepath.Dir(path), "workspace.yaml"))
	if err == nil && session.ValidUUID(workspace.ID) {
		return workspace.ID
	}
	return ""
}

// record is one events.jsonl line: {"type","data","id","parentId","timestamp"}.
type record struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

type data struct {
	SessionID    string          `json:"sessionId"`
	Content      json.RawMessage `json:"content"`
	Model        string          `json:"model"`
	NewModel     string          `json:"newModel"`
	ToolCallID   string          `json:"toolCallId"`
	ToolName     string          `json:"toolName"`
	Arguments    json.RawMessage `json:"arguments"`
	Result       *toolResult     `json:"result"`
	Error        json.RawMessage `json:"error"`
	ToolRequests []struct {
		ToolCallID string `json:"toolCallId"`
		Name       string `json:"name"`
	} `json:"toolRequests"`
	ModelCall *struct {
		Model string `json:"model"`
	} `json:"modelCall"`
	ResponseUsage *usage `json:"responseUsage"`
	ModelMetrics  map[string]struct {
		Usage *metricUsage `json:"usage"`
	} `json:"modelMetrics"`
}

type toolResult struct {
	Content json.RawMessage `json:"content"`
}

// usage is an OpenAI-style per-call usage object.
type usage struct {
	Prompt     int64 `json:"prompt_tokens"`
	Completion int64 `json:"completion_tokens"`
	Details    *struct {
		Cached int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// metricUsage is one model's per-run totals in session.shutdown.
type metricUsage struct {
	Input     int64 `json:"inputTokens"`
	Output    int64 `json:"outputTokens"`
	CacheRead int64 `json:"cacheReadTokens"`
}

// parser carries the state later records depend on: the current model and
// the tool name for each tool call id.
type parser struct {
	sessionID string
	seq       int64
	model     string
	toolNames map[string]string
}

// prefixMarkers are the record types that change parser state. Lines before
// fromOffset that mention none of them are skipped undecoded.
var prefixMarkers = [][]byte{
	[]byte(`"session.start"`),
	[]byte(`"session.resume"`),
	[]byte(`"session.model_change"`),
	[]byte(`"assistant.message"`),
	[]byte(`"tool.execution_start"`),
}

// scanPrefix updates parser state from a line before fromOffset without
// building an event.
func (p *parser) scanPrefix(line []byte) {
	relevant := false
	for _, marker := range prefixMarkers {
		if bytes.Contains(line, marker) {
			relevant = true
			break
		}
	}
	if !relevant {
		return
	}
	var rec record
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	var body data
	if json.Unmarshal(rec.Data, &body) != nil {
		return
	}
	p.track(rec.Type, body)
}

func (p *parser) track(recordType string, body data) {
	switch recordType {
	case "session.start", "session.resume":
		if p.sessionID == "" && session.ValidUUID(body.SessionID) {
			p.sessionID = body.SessionID
		}
	case "session.model_change":
		if body.NewModel != "" {
			p.model = body.NewModel
		}
	case "assistant.message":
		if body.Model != "" {
			p.model = body.Model
		}
		for _, request := range body.ToolRequests {
			if request.ToolCallID != "" && request.Name != "" {
				p.toolNames[request.ToolCallID] = request.Name
			}
		}
	case "tool.execution_start":
		if body.ToolCallID != "" && body.ToolName != "" {
			p.toolNames[body.ToolCallID] = body.ToolName
		}
	}
}

func (p *parser) parse(line []byte) transcript.Event {
	event := transcript.Event{
		SessionID: p.sessionID,
		Provider:  transcript.ProviderCopilot,
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
	var body data
	if len(rec.Data) > 0 && json.Unmarshal(rec.Data, &body) != nil {
		return event
	}
	p.track(rec.Type, body)
	event.SessionID = p.sessionID

	switch rec.Type {
	case "user.message":
		event.Role = transcript.RoleUser
		event.Text, _ = asString(body.Content)
	case "assistant.message":
		event.Role = transcript.RoleAssistant
		event.Text, _ = asString(body.Content)
		event.Model = optional(p.modelFor(body))
	case "system.message":
		event.Role = transcript.RoleSystem
		event.Text, _ = asString(body.Content)
	case "tool.execution_start":
		event.Role = transcript.RoleToolCall
		event.ToolName = optional(body.ToolName)
		event.Text = argumentsText(body.Arguments)
		event.Model = optional(p.modelFor(body))
	case "tool.execution_complete":
		event.Role = transcript.RoleToolResult
		event.ToolName = optional(p.toolNames[body.ToolCallID])
		event.Text = truncate(resultText(body))
		event.Model = optional(p.modelFor(body))
	case "session.model_change":
		event.Model = optional(body.NewModel)
	case "model.model_call_success":
		// Auxiliary model calls log their own usage; main-agent calls do
		// not, so their totals come from session.shutdown instead.
		if body.ResponseUsage != nil {
			event.Tokens = callTokens(*body.ResponseUsage)
			if body.ModelCall != nil {
				event.Model = optional(body.ModelCall.Model)
			}
		}
	case "session.shutdown":
		event.Tokens = runTokens(body)
	}
	return event
}

func (p *parser) modelFor(body data) string {
	if body.Model != "" {
		return body.Model
	}
	return p.model
}

// argumentsText renders tool arguments, which are usually an object, as
// compact JSON; a string is returned as is.
func argumentsText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if text, ok := asString(raw); ok {
		return text
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return ""
	}
	return compact.String()
}

// asString decodes raw when it is a JSON string.
func asString(raw json.RawMessage) (string, bool) {
	var text string
	return text, json.Unmarshal(raw, &text) == nil
}

// resultText prefers result.content, then a string or {message} error.
func resultText(body data) string {
	if body.Result != nil {
		if text, ok := asString(body.Result.Content); ok {
			return text
		}
	}
	if text, ok := asString(body.Error); ok {
		return text
	}
	var withMessage struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body.Error, &withMessage) == nil {
		return withMessage.Message
	}
	return ""
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

// callTokens maps OpenAI usage onto the event contract. prompt_tokens
// includes cached tokens, so input is the uncached remainder.
func callTokens(u usage) transcript.Tokens {
	var cached int64
	if u.Details != nil {
		cached = u.Details.Cached
	}
	return transcript.Tokens{
		Input:  max(u.Prompt-cached, 0),
		Output: u.Completion,
		Cache:  cached,
	}
}

// runTokens sums session.shutdown modelMetrics. inputTokens includes cache
// reads, so input is the remainder, matching callTokens.
func runTokens(body data) transcript.Tokens {
	var tokens transcript.Tokens
	for _, metric := range body.ModelMetrics {
		if metric.Usage == nil {
			continue
		}
		tokens.Input += max(metric.Usage.Input-metric.Usage.CacheRead, 0)
		tokens.Output += metric.Usage.Output
		tokens.Cache += metric.Usage.CacheRead
	}
	return tokens
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
