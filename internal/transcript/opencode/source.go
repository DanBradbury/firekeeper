// Package opencode reads OpenCode session transcripts as normalized
// transcript events.
//
// OpenCode keeps every session in one SQLite database, read here with
// sqlite3 -readonly. A transcript path names the database and a session in
// it; see session.OpenCodeTranscriptPath and NOTES.md for the tables read and
// known gaps.
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

const (
	// maxToolText is the longest tool output kept in Event.Text.
	maxToolText = 64 << 10
	// pageRows bounds the rows one sqlite3 call returns.
	pageRows = 500
	// legacyPageRows is the same bound for the legacy layout, whose rows
	// carry the message's parts and so are larger.
	legacyPageRows = 50
	// queryTimeout bounds one sqlite3 call.
	queryTimeout = 30 * time.Second
	// settleAfter is how long a message still being written may go
	// unchanged before it is read as final. OpenCode leaves a message
	// unfinished when it exits mid-turn and finishes it on resume.
	settleAfter = time.Hour
)

// ErrUnsupportedSchema reports an OpenCode database with neither the
// session_v2 and session_message tables nor the session, message, and part
// tables, written by an OpenCode release that changed its schema since.
var ErrUnsupportedSchema = errors.New("opencode: database has no known session tables; update Firekeeper if OpenCode is current")

func init() {
	reader.Register(transcript.ProviderOpenCode, &Source{})
}

// Source implements reader.Source, reader.Stater, and reader.Enumerator for
// OpenCode. The zero value reads the database OpenCode itself uses; see
// session.OpenCodeDatabase.
type Source struct {
	// Database overrides the OpenCode database path when set.
	Database string
	// Home overrides the user's home directory when set.
	Home string

	// run and now replace sqlite3 and the clock in tests.
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
	now func() time.Time
}

func (s *Source) database() (string, error) {
	if s.Database != "" {
		return s.Database, nil
	}
	db, err := session.OpenCodeDatabase(s.Home)
	if err != nil {
		return "", errors.New("opencode: find database")
	}
	return db, nil
}

// Locate returns the transcript path for meta when its session is in the
// database. Discovery already sets meta.RolloutPath for running sessions.
func (s *Source) Locate(meta session.Meta) ([]string, error) {
	if meta.Provider != "" && meta.Provider != string(transcript.ProviderOpenCode) {
		return nil, nil
	}
	if !session.ValidOpenCodeSessionID(meta.ID) {
		return nil, nil
	}
	db := ""
	if path, id, ok := session.SplitOpenCodeTranscriptPath(meta.RolloutPath); ok && id == meta.ID {
		db = path
	} else {
		var err error
		if db, err = s.database(); err != nil || db == "" {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	l, ok, err := s.open(ctx, db)
	if !ok || err != nil {
		return nil, err
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := s.query(ctx, db, "SELECT id FROM "+l.sessionTable()+" WHERE id = "+quote(meta.ID), &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return []string{session.OpenCodeTranscriptPath(db, meta.ID)}, nil
}

// layout names the tables a database keeps sessions in. Current OpenCode
// releases use session_v2 and session_message. Earlier ones use session,
// message, and part: a message's content is split across part rows, and
// message has no type or seq column, so the legacy layout uses the message
// role as its type and the row's rowid, which only grows, as its seq.
type layout struct{ legacy bool }

func (l layout) sessionTable() string {
	if l.legacy {
		return "session"
	}
	return "session_v2"
}

func (l layout) messageTable() string {
	if l.legacy {
		return "message"
	}
	return "session_message"
}

// seqColumn is the column Stat uses as a message's seq.
func (l layout) seqColumn() string {
	if l.legacy {
		return "rowid"
	}
	return "seq"
}

// open reports whether db exists and which of the known layouts it has.
func (s *Source) open(ctx context.Context, db string) (layout, bool, error) {
	if _, err := os.Stat(db); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return layout{}, false, nil
		}
		return layout{}, false, fmt.Errorf("opencode: stat database: %w", cause(err))
	}
	var rows []struct {
		Name string `json:"name"`
	}
	const query = `SELECT name FROM sqlite_master WHERE type = 'table'
		AND name IN ('session_v2', 'session_message', 'session', 'message', 'part')`
	if err := s.query(ctx, db, query, &rows); err != nil {
		return layout{}, false, err
	}
	have := make(map[string]bool, len(rows))
	for _, r := range rows {
		have[r.Name] = true
	}
	switch {
	case have["session_v2"] && have["session_message"]:
		return layout{}, true, nil
	case have["session"] && have["message"] && have["part"]:
		return layout{legacy: true}, true, nil
	}
	return layout{}, false, ErrUnsupportedSchema
}

// row is one session_message row. Data is the message JSON as OpenCode
// stored it, without the id and type it keeps in their own columns.
type row struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Seq         int64  `json:"seq"`
	TimeCreated int64  `json:"time_created"`
	TimeUpdated int64  `json:"time_updated"`
	Data        string `json:"data"`
	// Parts is the legacy layout's part rows as a JSON array.
	Parts string `json:"parts,omitempty"`
}

// Read returns the messages of the session at path whose seq is at least
// fromOffset, in seq order. Offsets are a seq cursor: the returned offset is
// one past the last message returned. Seq is OpenCode's own message seq, its
// durable event sequence, which never changes or repeats but has gaps.
//
// OpenCode updates a message in place while it is being written, so Read
// stops before the first message that is not finished yet and returns its
// seq as the offset; it is read once finished. See NOTES.md.
func (s *Source) Read(path string, fromOffset int64) ([]transcript.Event, int64, error) {
	if fromOffset < 0 {
		return nil, 0, fmt.Errorf("opencode: negative offset %d", fromOffset)
	}
	db, id, ok := session.SplitOpenCodeTranscriptPath(path)
	if !ok {
		return nil, fromOffset, errors.New("opencode: not an OpenCode transcript path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if _, err := os.Stat(db); err != nil {
		return nil, fromOffset, fmt.Errorf("opencode: stat database: %w", cause(err))
	}
	l, ok, err := s.open(ctx, db)
	if err != nil {
		return nil, fromOffset, err
	}
	if !ok {
		return nil, fromOffset, fmt.Errorf("opencode: stat database: %w", os.ErrNotExist)
	}
	var idle []struct {
		Seq int64 `json:"seq"`
	}
	if l.legacy {
		// No idle markers: every message but the session's last is final.
		err = s.query(ctx, db, `SELECT COALESCE(MAX(rowid), -1) AS seq FROM message
			WHERE session_id = `+quote(id), &idle)
	} else {
		err = s.query(ctx, db, `SELECT COALESCE(MAX(seq), -1) AS seq FROM session_message
			WHERE type = 'idle' AND session_id = `+quote(id), &idle)
	}
	if err != nil {
		return nil, fromOffset, err
	}
	lastIdle := int64(-1)
	if len(idle) > 0 {
		lastIdle = idle[0].Seq
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}

	page := pageRows
	if l.legacy {
		page = legacyPageRows
	}
	var events []transcript.Event
	offset := fromOffset
	for {
		var rows []row
		if err := s.query(ctx, db, l.readQuery(id, offset, page), &rows); err != nil {
			return events, offset, err
		}
		for _, r := range rows {
			if l.legacy {
				r = legacyRow(r)
			}
			msg, decoded := decode(r.Data)
			if !settled(r, msg, decoded, lastIdle, now) {
				return events, r.Seq, nil
			}
			events = append(events, parse(id, r, msg, decoded))
			offset = r.Seq + 1
		}
		if len(rows) < page {
			return events, offset, nil
		}
	}
}

// readQuery selects up to limit messages of session id from seq on. The
// legacy layout's parts come back as a JSON array in the parts column. A
// legacy user message's summary, a diff of the files it touched that can be
// hundreds of KiB, is left out: sqlite3 -json takes seconds to print it.
func (l layout) readQuery(id string, from int64, limit int) string {
	if !l.legacy {
		return fmt.Sprintf(`SELECT id, type, seq, time_created, time_updated, data
			FROM session_message WHERE session_id = %s AND seq >= %d
			ORDER BY seq LIMIT %d`, quote(id), from, limit)
	}
	return fmt.Sprintf(`SELECT m.id, '' AS type, m.rowid AS seq, m.time_created, m.time_updated,
			CASE WHEN json_valid(m.data) THEN json_remove(m.data, '$.summary') ELSE m.data END AS data,
			COALESCE((SELECT json_group_array(CASE WHEN json_valid(p.data) THEN json(p.data) ELSE json_quote(p.data) END)
				FROM (SELECT data FROM part WHERE message_id = m.id ORDER BY id) p), '[]') AS parts
		FROM message m WHERE m.session_id = %s AND m.rowid >= %d
		ORDER BY m.rowid LIMIT %d`, quote(id), from, limit)
}

// Stat implements reader.Stater. Size is one past the session's highest
// message seq, the offset a Read to the end returns, and ModTime is when the
// session or any of its messages last changed.
func (s *Source) Stat(path string) (reader.Info, error) {
	db, id, ok := session.SplitOpenCodeTranscriptPath(path)
	if !ok {
		return reader.Info{}, errors.New("opencode: not an OpenCode transcript path")
	}
	if _, err := os.Stat(db); err != nil {
		return reader.Info{}, fmt.Errorf("opencode: stat database: %w", cause(err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	l, ok, err := s.open(ctx, db)
	if err != nil {
		return reader.Info{}, err
	}
	if !ok {
		return reader.Info{}, fmt.Errorf("opencode: session not in database: %w", os.ErrNotExist)
	}
	var rows []struct {
		Session *int64 `json:"session_updated"`
		Message *int64 `json:"message_updated"`
		MaxSeq  *int64 `json:"max_seq"`
	}
	query := fmt.Sprintf(`SELECT
			(SELECT time_updated FROM %[2]s WHERE id = %[1]s) AS session_updated,
			(SELECT MAX(time_updated) FROM %[3]s WHERE session_id = %[1]s) AS message_updated,
			(SELECT MAX(%[4]s) FROM %[3]s WHERE session_id = %[1]s) AS max_seq`,
		quote(id), l.sessionTable(), l.messageTable(), l.seqColumn())
	if err := s.query(ctx, db, query, &rows); err != nil {
		return reader.Info{}, err
	}
	if len(rows) == 0 || rows[0].Session == nil {
		return reader.Info{}, fmt.Errorf("opencode: session not in database: %w", os.ErrNotExist)
	}
	info := reader.Info{ModTime: time.UnixMilli(*rows[0].Session)}
	if m := rows[0].Message; m != nil && *m > *rows[0].Session {
		info.ModTime = time.UnixMilli(*m)
	}
	if rows[0].MaxSeq != nil {
		info.Size = *rows[0].MaxSeq + 1
	}
	return info, nil
}

// Pending implements reader.Stater. It counts every message from offset on,
// including one still being written, and sizes them by their stored JSON.
func (s *Source) Pending(ctx context.Context, path string, from int64) (events, bytes int64, err error) {
	db, id, ok := session.SplitOpenCodeTranscriptPath(path)
	if !ok {
		return 0, 0, errors.New("opencode: not an OpenCode transcript path")
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	l, ok, err := s.open(ctx, db)
	if err != nil || !ok {
		return 0, 0, err
	}
	var rows []struct {
		Events int64 `json:"events"`
		Bytes  int64 `json:"bytes"`
	}
	query := fmt.Sprintf(`SELECT COUNT(*) AS events, COALESCE(SUM(LENGTH(data)), 0) AS bytes
		FROM session_message WHERE session_id = %s AND seq >= %d`, quote(id), max(from, 0))
	if l.legacy {
		query = fmt.Sprintf(`SELECT COUNT(*) AS events, COALESCE(SUM(LENGTH(m.data)
				+ COALESCE((SELECT SUM(LENGTH(p.data)) FROM part p WHERE p.message_id = m.id), 0)), 0) AS bytes
			FROM message m WHERE m.session_id = %s AND m.rowid >= %d`, quote(id), max(from, 0))
	}
	if err := s.query(ctx, db, query, &rows); err != nil {
		return 0, 0, err
	}
	if len(rows) == 0 {
		return 0, 0, nil
	}
	return rows[0].Events, rows[0].Bytes, nil
}

// query runs one read-only sqlite3 query and decodes its JSON rows.
func (s *Source) query(ctx context.Context, db, query string, rows any) error {
	args := []string{"-readonly", "-json", db, query}
	var output []byte
	var err error
	if s.run != nil {
		output, err = s.run(ctx, "sqlite3", args...)
	} else {
		output, err = exec.CommandContext(ctx, "sqlite3", args...).Output()
	}
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("opencode: query database: %w", ctx.Err())
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			// sqlite3's message can quote the query, so it is not shown.
			return fmt.Errorf("opencode: query database: sqlite3 exited with status %d", exit.ExitCode())
		}
		return fmt.Errorf("opencode: query database: %w", cause(err))
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return nil
	}
	if err := json.Unmarshal(output, rows); err != nil {
		return errors.New("opencode: decode database rows")
	}
	return nil
}

// quote returns id as an SQL string literal. Callers pass only ids that
// session.ValidOpenCodeSessionID accepts, which hold no quotes.
func quote(id string) string {
	return "'" + strings.ReplaceAll(id, "'", "''") + "'"
}

// cause drops the path from filesystem errors so messages stay free of
// machine-specific locations.
func cause(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return execErr.Err
	}
	return err
}

// message holds the fields of a session_message data object the parser
// reads. Every message type shares this envelope; see NOTES.md.
type message struct {
	Time struct {
		Created   *float64 `json:"created"`
		Completed *float64 `json:"completed"`
	} `json:"time"`
	Text        json.RawMessage `json:"text"`
	Description string          `json:"description"`
	Command     string          `json:"command"`
	Status      string          `json:"status"`
	Output      *struct {
		Output string `json:"output"`
	} `json:"output"`
	Model   json.RawMessage `json:"model"`
	Content []part          `json:"content"`
	Tokens  *tokenUsage     `json:"tokens"`
	Summary string          `json:"summary"`
	Error   json.RawMessage `json:"error"`
}

type part struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Name  string `json:"name"`
	State struct {
		Status  string          `json:"status"`
		Input   json.RawMessage `json:"input"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Error json.RawMessage `json:"error"`
	} `json:"state"`
}

type tokenUsage struct {
	Input     float64 `json:"input"`
	Output    float64 `json:"output"`
	Reasoning float64 `json:"reasoning"`
	Cache     struct {
		Read  float64 `json:"read"`
		Write float64 `json:"write"`
	} `json:"cache"`
}

func decode(data string) (message, bool) {
	var msg message
	if json.Unmarshal([]byte(data), &msg) != nil {
		return message{}, false
	}
	return msg, true
}

// settled reports whether r is finished being written. An assistant
// message is finished once it has a completion time, a shell command once it
// stops running, and a compaction once it completes or fails. Anything
// before the session's latest idle marker belongs to an ended turn and is
// final too, as is a message left unchanged for settleAfter.
func settled(r row, msg message, decoded bool, lastIdle int64, now time.Time) bool {
	if !decoded || r.Seq < lastIdle || now.Sub(time.UnixMilli(r.TimeUpdated)) >= settleAfter {
		return true
	}
	switch r.Type {
	case "assistant":
		return msg.Time.Completed != nil
	case "shell", "compaction":
		return msg.Status != "running"
	}
	return true
}

func parse(sessionID string, r row, msg message, decoded bool) transcript.Event {
	event := transcript.Event{
		SessionID: sessionID,
		Provider:  transcript.ProviderOpenCode,
		Seq:       r.Seq,
		Role:      transcript.RoleMeta,
	}
	event.Raw = rawRecord(r, decoded)
	ts := r.TimeCreated
	if decoded && msg.Time.Created != nil && *msg.Time.Created > 0 {
		ts = int64(*msg.Time.Created)
	}
	if ts > 0 {
		t := time.UnixMilli(ts).UTC()
		event.TS = &t
	}
	if !decoded {
		return event
	}
	switch r.Type {
	case "user":
		event.Role = transcript.RoleUser
		event.Text = stringValue(msg.Text)
	case "synthetic", "system", "skill":
		// Context OpenCode adds on the user's behalf.
		event.Role = transcript.RoleSystem
		event.Text = stringValue(msg.Text)
	case "shell":
		// A command the user ran with !, and its output.
		event.Role = transcript.RoleToolResult
		event.ToolName = optional("shell")
		text := "$ " + msg.Command
		if msg.Output != nil && msg.Output.Output != "" {
			text += "\n" + msg.Output.Output
		}
		event.Text = truncate(text)
	case "assistant":
		assistant(&event, msg)
	case "compaction":
		event.Model = optional(session.OpenCodeModel(msg.Model))
		event.Tokens = tokens(msg.Tokens)
		if msg.Status == "completed" {
			event.Role = transcript.RoleSystem
			event.Text = msg.Summary
		}
	case "model-switched":
		event.Model = optional(session.OpenCodeModel(msg.Model))
	}
	return event
}

// assistant maps one model step. A step holding both a reply and tool calls
// reads as the reply, with the first tool's name kept; a step with only tool
// calls reads as the calls and their results; reasoning alone stays meta,
// like Codex and Claude Code reasoning.
func assistant(event *transcript.Event, msg message) {
	event.Model = optional(session.OpenCodeModel(msg.Model))
	event.Tokens = tokens(msg.Tokens)
	var texts, reasoning, tools []string
	for _, p := range msg.Content {
		switch p.Type {
		case "text":
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		case "reasoning":
			if p.Text != "" {
				reasoning = append(reasoning, p.Text)
			}
		case "tool":
			if event.ToolName == nil {
				event.ToolName = optional(p.Name)
			}
			tools = append(tools, toolText(p))
		}
	}
	switch {
	case len(texts) > 0:
		event.Role = transcript.RoleAssistant
		event.Text = strings.Join(texts, "\n")
	case len(tools) > 0:
		event.Role = transcript.RoleToolCall
		event.Text = truncate(strings.Join(tools, "\n"))
	default:
		event.Text = strings.Join(reasoning, "\n")
	}
}

// toolText is a tool call's input followed by its text output or error.
// File output stays in Raw.
func toolText(p part) string {
	lines := []string{}
	if input := compact(p.State.Input); input != "" {
		lines = append(lines, input)
	}
	for _, c := range p.State.Content {
		if c.Type == "text" && c.Text != "" {
			lines = append(lines, c.Text)
		}
	}
	if len(p.State.Error) > 0 && string(p.State.Error) != "null" {
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(p.State.Error, &e) == nil && e.Message != "" {
			lines = append(lines, "error: "+e.Message)
		}
	}
	return strings.Join(lines, "\n")
}

// rawRecord is the row as one JSON object, with data embedded as JSON when
// it decodes and as a string otherwise, so Raw stays valid JSON.
func rawRecord(r row, decoded bool) json.RawMessage {
	data := json.RawMessage(r.Data)
	if !decoded || !json.Valid(data) {
		data, _ = json.Marshal(r.Data)
	}
	raw, err := json.Marshal(struct {
		ID          string          `json:"id"`
		Type        string          `json:"type"`
		Seq         int64           `json:"seq"`
		TimeCreated int64           `json:"time_created"`
		TimeUpdated int64           `json:"time_updated"`
		Data        json.RawMessage `json:"data"`
	}{r.ID, r.Type, r.Seq, r.TimeCreated, r.TimeUpdated, data})
	if err != nil {
		raw, _ = json.Marshal(r.Data)
	}
	return raw
}

func stringValue(raw json.RawMessage) string {
	var text string
	if len(raw) == 0 || json.Unmarshal(raw, &text) != nil {
		return ""
	}
	return text
}

func compact(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if text, ok := stringOK(raw); ok {
		return text // input still streaming arrives as a string
	}
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return string(raw)
	}
	return out.String()
}

func stringOK(raw json.RawMessage) (string, bool) {
	var text string
	if raw[0] != '"' || json.Unmarshal(raw, &text) != nil {
		return "", false
	}
	return text, true
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

// tokens maps OpenCode usage onto the event contract. OpenCode counts input
// without cached tokens and reasoning apart from output, so reasoning is
// added to output and cache reads and writes go to cache.
func tokens(u *tokenUsage) transcript.Tokens {
	if u == nil {
		return transcript.Tokens{}
	}
	n := func(v float64) int64 { return max(int64(v), 0) }
	return transcript.Tokens{
		Input:  n(u.Input),
		Output: n(u.Output) + n(u.Reasoning),
		Cache:  n(u.Cache.Read) + n(u.Cache.Write),
	}
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
