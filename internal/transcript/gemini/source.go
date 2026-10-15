// Package gemini reads Gemini CLI session logs as normalized transcript
// events.
//
// Each session is a log $GEMINI_CLI_HOME/.gemini/tmp/<project>/chats/
// session-<time>-<id prefix>.jsonl of metadata, message, and update records;
// see NOTES.md for the record shapes and known gaps.
package gemini

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// maxToolText is the longest tool output kept in Event.Text.
const maxToolText = 64 << 10

func init() {
	reader.Register(transcript.ProviderGemini, &Source{})
}

// Source implements reader.Source for Gemini CLI session logs.
// The zero value reads $GEMINI_CLI_HOME/.gemini, falling back to ~/.gemini
// and the ~/.cache/.gemini directory of Gemini CLI's macOS sandbox, as
// discovery does.
type Source struct {
	// Dir overrides Gemini CLI's runtime directory when set.
	Dir string
}

func (s *Source) dirs() ([]string, error) {
	if s.Dir != "" {
		return []string{s.Dir}, nil
	}
	dirs, err := session.GeminiDirs()
	if err != nil {
		return nil, errors.New("gemini: find home directory")
	}
	return dirs, nil
}

// Locate returns the session log for meta. Discovery already maps running
// Gemini CLI processes to their log, so meta.RolloutPath wins when its first
// record names this session. Otherwise the log is searched for by id.
// Subagent logs are not returned; see NOTES.md.
func (s *Source) Locate(meta session.Meta) ([]string, error) {
	if meta.Provider != "" && meta.Provider != string(transcript.ProviderGemini) {
		return nil, nil
	}
	if !session.ValidGeminiSessionID(meta.ID) {
		return nil, nil
	}
	if meta.RolloutPath != "" {
		if t, err := session.ReadGeminiTranscript(meta.RolloutPath); err == nil && t.SessionID == meta.ID {
			return []string{meta.RolloutPath}, nil
		}
	}
	dirs, err := s.dirs()
	if err != nil {
		return nil, err
	}
	if path := session.FindGeminiTranscript(dirs, meta.ID); path != "" {
		return []string{path}, nil
	}
	return nil, nil
}

// Read parses complete lines from fromOffset onward. Offsets are byte
// positions just past a newline; a trailing line without a newline is still
// being written and is left for the next read. Seq is the line index, and a
// message can be written again as it gains tool calls or token counts, so a
// read starting mid-file first parses the earlier lines to learn what they
// already emitted.
//
// A legacy .json session file is one JSON document, so it is read whole: seq
// is the message index, the returned offset is the file size, and a read
// from any other offset returns every message again for the reporter and the
// server to drop by seq.
func (s *Source) Read(path string, fromOffset int64) ([]transcript.Event, int64, error) {
	if fromOffset < 0 {
		return nil, 0, fmt.Errorf("gemini: negative offset %d", fromOffset)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fromOffset, fmt.Errorf("gemini: open transcript: %w", cause(err))
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fromOffset, fmt.Errorf("gemini: stat transcript: %w", cause(err))
	}
	if fromOffset > info.Size() {
		return nil, fromOffset, fmt.Errorf("gemini: offset %d is past end of transcript (%d bytes); file was truncated or rewritten", fromOffset, info.Size())
	}
	if strings.HasSuffix(path, ".json") {
		return readLegacy(file, info.Size(), fromOffset)
	}

	p := newParser(false)
	reader := bufio.NewReaderSize(file, 256<<10)
	var offset int64
	for offset < fromOffset {
		line, err := reader.ReadBytes('\n')
		offset += int64(len(line))
		if err != nil || offset > fromOffset {
			return nil, fromOffset, fmt.Errorf("gemini: offset %d is not at a line boundary", fromOffset)
		}
		p.parse(bytes.TrimRight(line, "\r\n"))
	}

	var events []transcript.Event
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return events, offset, nil
			}
			return events, offset, fmt.Errorf("gemini: read transcript: %w", cause(err))
		}
		offset += int64(len(line))
		events = append(events, p.parse(bytes.TrimRight(line, "\r\n")))
	}
}

// readLegacy reads a whole-document session file written before Gemini CLI
// switched to JSONL logs.
func readLegacy(file *os.File, size, fromOffset int64) ([]transcript.Event, int64, error) {
	if fromOffset == size && size > 0 {
		return nil, size, nil
	}
	if size > session.GeminiLegacyMaxBytes {
		return nil, fromOffset, errors.New("gemini: legacy session file is too large")
	}
	var doc struct {
		SessionID string            `json:"sessionId"`
		Messages  []json.RawMessage `json:"messages"`
	}
	if err := json.NewDecoder(io.LimitReader(file, size)).Decode(&doc); err != nil {
		return nil, fromOffset, errors.New("gemini: legacy session file is not valid JSON")
	}
	p := newParser(true)
	p.sessionID = doc.SessionID
	events := make([]transcript.Event, 0, len(doc.Messages))
	for _, message := range doc.Messages {
		var compacted bytes.Buffer
		if json.Compact(&compacted, message) != nil {
			compacted.Reset()
			compacted.Write(message)
		}
		events = append(events, p.parse(compacted.Bytes()))
	}
	return events, size, nil
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

// record is one session log line. Only the fields the parser reads are
// decoded; Raw keeps the rest.
type record struct {
	// Metadata record.
	SessionID string `json:"sessionId"`
	StartTime string `json:"startTime"`
	// Message record.
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	Timestamp      string          `json:"timestamp"`
	Content        json.RawMessage `json:"content"`
	DisplayContent json.RawMessage `json:"displayContent"`
	ToolCalls      []toolCall      `json:"toolCalls"`
	Thoughts       []thought       `json:"thoughts"`
	Tokens         *tokenSummary   `json:"tokens"`
	Model          string          `json:"model"`
	// Updates.
	Set *struct {
		LastUpdated string `json:"lastUpdated"`
	} `json:"$set"`
	Patch    json.RawMessage `json:"$patch"`
	RewindTo *string         `json:"$rewindTo"`
}

type toolCall struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Args          json.RawMessage `json:"args"`
	Result        json.RawMessage `json:"result"`
	ResultDisplay json.RawMessage `json:"resultDisplay"`
}

type thought struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
}

// tokenSummary is Gemini's usage metadata for one model response.
type tokenSummary struct {
	Input    int64 `json:"input"`
	Output   int64 `json:"output"`
	Cached   int64 `json:"cached"`
	Thoughts int64 `json:"thoughts"`
	Tool     int64 `json:"tool"`
}

// part is one Gemini content part.
type part struct {
	Text             string `json:"text"`
	Thought          bool   `json:"thought"`
	FunctionResponse *struct {
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Response json.RawMessage `json:"response"`
	} `json:"functionResponse"`
}

// message is what earlier lines emitted for one message id. Gemini CLI writes
// a message again, whole, when it gains tool calls or token counts, and each
// later copy emits only what is new.
type message struct {
	text   string
	calls  map[string]bool
	tokens bool
}

// parser carries the state later lines depend on.
type parser struct {
	sessionID string
	seq       int64
	model     string
	// legacy reads tool results from tool call records. Current logs also
	// write each result as a functionResponse user message, which is the one
	// emitted.
	legacy    bool
	messages  map[string]*message
	toolNames map[string]string
	results   map[string]bool
}

func newParser(legacy bool) *parser {
	return &parser{legacy: legacy, messages: map[string]*message{}, toolNames: map[string]string{}, results: map[string]bool{}}
}

// parse builds the event for one line and advances seq. Lines before a read's
// offset go through it too, so state never depends on where a read starts.
func (p *parser) parse(line []byte) transcript.Event {
	event := transcript.Event{
		SessionID: p.sessionID,
		Provider:  transcript.ProviderGemini,
		Seq:       p.seq,
		Role:      transcript.RoleMeta,
	}
	p.seq++
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
	switch {
	case rec.RewindTo != nil, rec.Patch != nil:
		// Rewinds and patches change earlier messages; what those messages
		// emitted stays in the transcript.
	case rec.Set != nil:
		event.TS = timestamp(rec.Set.LastUpdated)
	case rec.ID != "":
		event.TS = timestamp(rec.Timestamp)
		p.message(&event, rec)
	case rec.SessionID != "":
		event.TS = timestamp(rec.StartTime)
		if p.sessionID == "" {
			p.sessionID = rec.SessionID
			event.SessionID = rec.SessionID
		}
	}
	return event
}

func (p *parser) message(event *transcript.Event, rec record) {
	m, seen := p.messages[rec.ID]
	if !seen {
		m = &message{calls: map[string]bool{}}
		p.messages[rec.ID] = m
	}
	switch rec.Type {
	case "user":
		if !seen {
			p.userMessage(event, rec)
		}
	case "info", "warning", "error":
		if !seen {
			event.Role = transcript.RoleSystem
			event.Text = contentText(rec.Content)
		}
	case "gemini":
		p.geminiMessage(event, rec, m, !seen)
	}
}

// userMessage maps a user record. Tool responses are recorded as user
// messages of functionResponse parts; text Gemini CLI injects as session or
// hook context is the harness's, not the user's.
func (p *parser) userMessage(event *transcript.Event, rec record) {
	if responses := functionResponses(rec.Content); len(responses) > 0 {
		var texts []string
		for _, r := range responses {
			if r.ID != "" {
				if p.results[r.ID] {
					continue
				}
				p.results[r.ID] = true
			}
			if event.ToolName == nil {
				event.ToolName = optional(session.FirstNonEmpty(r.Name, p.toolNames[r.ID]))
			}
			texts = append(texts, responseText(r.Response))
		}
		if len(texts) > 0 {
			event.Role = transcript.RoleToolResult
			event.Text = truncate(strings.Join(texts, "\n"))
		}
		return
	}
	content := rec.Content
	if len(rec.DisplayContent) > 0 && !bytes.Equal(rec.DisplayContent, []byte("null")) {
		content = rec.DisplayContent
	}
	event.Text = contentText(content)
	trimmed := strings.TrimSpace(event.Text)
	if strings.HasPrefix(trimmed, "<session_context>") || strings.HasPrefix(trimmed, "<hook_context>") {
		event.Role = transcript.RoleSystem
		return
	}
	event.Role = transcript.RoleUser
}

// geminiMessage maps a model message, emitting only what earlier copies of
// it did not: new tool calls, reply text, and token counts once.
func (p *parser) geminiMessage(event *transcript.Event, rec record, m *message, first bool) {
	if model := strings.TrimSpace(rec.Model); model != "" {
		p.model = model
	}
	event.Model = optional(p.model)
	if rec.Tokens != nil && !m.tokens {
		m.tokens = true
		event.Tokens = tokens(*rec.Tokens)
	}

	text := contentText(rec.Content)
	newText := ""
	if text != "" && text != m.text {
		newText = text
		m.text = text
	}
	var calls []string
	var results []string
	for index, call := range rec.ToolCalls {
		key := call.ID
		if key == "" {
			key = fmt.Sprintf("#%d", index)
		} else {
			p.toolNames[call.ID] = call.Name
		}
		if !m.calls[key] {
			m.calls[key] = true
			if event.ToolName == nil {
				event.ToolName = optional(call.Name)
			}
			calls = append(calls, argsText(call.Args))
		}
		if p.legacy && call.ID != "" && !p.results[call.ID] && hasValue(call.Result) {
			p.results[call.ID] = true
			results = append(results, resultText(call))
		}
	}

	switch {
	case len(calls) > 0:
		event.Role = transcript.RoleToolCall
		texts := []string{}
		if newText != "" {
			texts = append(texts, newText)
		}
		for _, c := range calls {
			if c != "" {
				texts = append(texts, c)
			}
		}
		if len(results) > 0 {
			texts = append(texts, truncate(strings.Join(results, "\n")))
		}
		event.Text = strings.Join(texts, "\n")
	case newText != "":
		event.Role = transcript.RoleAssistant
		event.Text = newText
	case first && len(rec.Thoughts) > 0:
		// Thinking is not a reply, so it stays meta, like Claude's.
		event.Text = thoughtText(rec.Thoughts)
	}
}

// parts decodes Gemini content, which is a string, one part, or a list of
// parts. A string becomes one text part.
func parts(raw json.RawMessage) []part {
	if len(raw) == 0 {
		return nil
	}
	switch raw[0] {
	case '"':
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return []part{{Text: text}}
		}
	case '{':
		var p part
		if json.Unmarshal(raw, &p) == nil {
			return []part{p}
		}
	case '[':
		// Lists may mix strings and parts.
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) == nil {
			var out []part
			for _, item := range items {
				out = append(out, parts(item)...)
			}
			return out
		}
	}
	return nil
}

// contentText concatenates the text parts a person would read, leaving out
// thoughts. Media and function parts stay in Raw.
func contentText(raw json.RawMessage) string {
	var b strings.Builder
	for _, p := range parts(raw) {
		if !p.Thought {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

type functionResponse struct {
	ID, Name string
	Response json.RawMessage
}

func functionResponses(raw json.RawMessage) []functionResponse {
	var out []functionResponse
	for _, p := range parts(raw) {
		if p.FunctionResponse != nil {
			out = append(out, functionResponse{p.FunctionResponse.ID, p.FunctionResponse.Name, p.FunctionResponse.Response})
		}
	}
	return out
}

// responseText reads a function response, {"output": ...} on success and
// {"error": ...} on failure, falling back to the compact JSON.
func responseText(raw json.RawMessage) string {
	var response struct {
		Output json.RawMessage `json:"output"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &response) == nil {
		for _, field := range []json.RawMessage{response.Output, response.Error} {
			var text string
			if len(field) > 0 && json.Unmarshal(field, &text) == nil {
				return text
			}
			if hasValue(field) {
				return compact(field)
			}
		}
	}
	if !hasValue(raw) {
		return ""
	}
	return compact(raw)
}

// resultText reads a legacy tool call's result: the function responses in
// result, else the resultDisplay string the UI showed.
func resultText(call toolCall) string {
	var texts []string
	for _, r := range functionResponses(call.Result) {
		texts = append(texts, responseText(r.Response))
	}
	if text := contentText(call.Result); text != "" {
		texts = append(texts, text)
	}
	if len(texts) == 0 {
		var display string
		if json.Unmarshal(call.ResultDisplay, &display) == nil {
			return display
		}
	}
	return strings.Join(texts, "\n")
}

func argsText(raw json.RawMessage) string {
	if !hasValue(raw) {
		return ""
	}
	return compact(raw)
}

func thoughtText(thoughts []thought) string {
	texts := make([]string, 0, len(thoughts))
	for _, t := range thoughts {
		texts = append(texts, strings.TrimSpace(strings.Join([]string{t.Subject, t.Description}, "\n")))
	}
	return strings.Join(texts, "\n\n")
}

func hasValue(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}

func timestamp(value string) *time.Time {
	if value == "" {
		return nil
	}
	ts, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &ts
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

// tokens maps Gemini usage onto the event contract. Gemini's prompt count
// includes cached tokens and leaves out tool-use prompt tokens, and its
// candidate count leaves out thinking tokens, so input is the uncached prompt
// plus tool-use prompt, output is candidates plus thoughts, and the three
// counts add up to Gemini's total.
func tokens(t tokenSummary) transcript.Tokens {
	cached := max(t.Cached, 0)
	return transcript.Tokens{
		Input:  max(t.Input-cached, 0) + max(t.Tool, 0),
		Output: max(t.Output, 0) + max(t.Thoughts, 0),
		Cache:  cached,
	}
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
