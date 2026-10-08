package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// ErrNotFound marks a lookup for a session that is not stored.
var ErrNotFound = errors.New("not found")

// Session is stored session metadata as served by the read API.
type Session struct {
	UID            string            `json:"uid"`
	MachineID      string            `json:"machine_id"`
	SessionID      string            `json:"session_id"`
	Provider       string            `json:"provider"`
	CWD            string            `json:"cwd"`
	Project        string            `json:"project"`
	Branch         string            `json:"branch"`
	Commit         string            `json:"commit"`
	Model          string            `json:"model"`
	State          string            `json:"state"`
	Title          string            `json:"title"`
	StartedAt      *time.Time        `json:"started_at"`
	LastActivityAt *time.Time        `json:"last_activity_at"`
	EventCount     int64             `json:"event_count"`
	Tokens         transcript.Tokens `json:"tokens"`
	// Snippets are the best-matching transcript excerpts, set only for
	// searches (q). Text is HTML-escaped except for <mark> around matches.
	Snippets []Snippet `json:"snippets,omitempty"`
}

// Snippet is a matched excerpt of one event's text.
type Snippet struct {
	Seq  int64  `json:"seq"`
	Text string `json:"text"`
}

// MaxSnippets is the most snippets returned per session.
const MaxSnippets = 3

// MachineInfo is a machine with its heartbeat and session count.
type MachineInfo struct {
	Machine
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	SessionCount    int64      `json:"session_count"`
}

// SessionFilter selects sessions for ListSessions. Empty fields do not
// filter. Cursor is the opaque value returned by a previous page.
type SessionFilter struct {
	Machine  string
	Provider string
	Project  string
	State    string
	Q        string
	Limit    int
	Cursor   string
}

// UID joins a machine and session id into the API's session identifier.
func UID(machineID, sessionID string) string { return machineID + ":" + sessionID }

// SplitUID splits a session identifier at its first colon.
func SplitUID(uid string) (machineID, sessionID string, ok bool) {
	m, s, ok := strings.Cut(uid, ":")
	if !ok || m == "" || s == "" {
		return "", "", false
	}
	return m, s, true
}

// parseTime reads a stored timestamp. RFC3339Nano parsing accepts both the
// fixed-width layout and older trimmed values.
func parseTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, ns.String)
	if err != nil {
		return nil
	}
	return &t
}

// cursor is the keyset position after the last row of a page. Key is the
// session's last_activity_at as stored, or "" when it is NULL.
type cursor struct {
	Offset    int    `json:"o,omitempty"` // search results are paged by offset
	Key       string `json:"k"`
	MachineID string `json:"m"`
	SessionID string `json:"s"`
}

func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (cursor, error) {
	var c cursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &c) != nil || c.Offset < 0 || (c.Offset == 0 && (c.MachineID == "" || c.SessionID == "")) {
		return c, fmt.Errorf("%w: invalid cursor", ErrInvalid)
	}
	return c, nil
}

// ftsPhrase quotes q as a single FTS5 phrase so user input cannot inject
// query syntax.
func ftsPhrase(q string) string {
	return `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
}

const sessionColumns = `s.machine_id, s.session_id, s.provider, s.cwd, s.project, s.branch, s."commit", s.model,
    s.state, s.title, s.started_at, s.last_activity_at, s.event_count,
    s.input_tokens, s.output_tokens, s.cache_tokens`

func scanSession(sc interface{ Scan(...any) error }) (Session, string, error) {
	var se Session
	var started, last sql.NullString
	err := sc.Scan(&se.MachineID, &se.SessionID, &se.Provider, &se.CWD, &se.Project, &se.Branch,
		&se.Commit, &se.Model, &se.State, &se.Title, &started, &last, &se.EventCount,
		&se.Tokens.Input, &se.Tokens.Output, &se.Tokens.Cache)
	if err != nil {
		return se, "", err
	}
	se.UID = UID(se.MachineID, se.SessionID)
	se.StartedAt = parseTime(started)
	se.LastActivityAt = parseTime(last)
	return se, last.String, nil
}

// ListSessions returns sessions newest activity first, with sessions that
// have no activity time last. It returns the cursor for the next page, or ""
// when there are no more rows.
func (s *Store) ListSessions(ctx context.Context, accountID string, f SessionFilter) ([]Session, string, error) {
	if err := requireAccount(accountID); err != nil {
		return nil, "", err
	}
	if f.Limit < 1 {
		return nil, "", fmt.Errorf("%w: limit must be positive", ErrInvalid)
	}
	if strings.TrimSpace(f.Q) != "" {
		return s.searchSessions(ctx, accountID, f)
	}
	where := []string{"s.account_id = ?"}
	args := []any{accountID}
	for _, eq := range []struct{ col, val string }{
		{"s.machine_id", f.Machine}, {"s.provider", f.Provider},
		{"s.project", f.Project}, {"s.state", f.State},
	} {
		if eq.val != "" {
			where = append(where, eq.col+" = ?")
			args = append(args, eq.val)
		}
	}
	if f.Cursor != "" {
		c, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		where = append(where, `(COALESCE(s.last_activity_at, ''), s.machine_id, s.session_id) < (?, ?, ?)`)
		args = append(args, c.Key, c.MachineID, c.SessionID)
	}
	query := `SELECT ` + sessionColumns + ` FROM sessions s`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	query += ` ORDER BY COALESCE(s.last_activity_at, '') DESC, s.machine_id DESC, s.session_id DESC LIMIT ?`
	args = append(args, f.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []Session
	var keys []string
	for rows.Next() {
		se, key, err := scanSession(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, se)
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) <= f.Limit {
		return out, "", nil
	}
	out = out[:f.Limit]
	last := out[f.Limit-1]
	return out, encodeCursor(cursor{Key: keys[f.Limit-1], MachineID: last.MachineID, SessionID: last.SessionID}), nil
}

// GetSession returns one session or ErrNotFound.
func (s *Store) GetSession(ctx context.Context, accountID, machineID, sessionID string) (Session, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions s
WHERE s.account_id = ? AND s.machine_id = ? AND s.session_id = ?`, accountID, machineID, sessionID)
	se, _, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return se, ErrNotFound
	}
	return se, err
}

// ListEvents returns up to limit events with seq greater than afterSeq, in
// seq order. hasMore reports whether further events follow. Pass afterSeq -1
// to start from the first event.
func (s *Store) ListEvents(ctx context.Context, accountID, machineID, sessionID string, afterSeq int64, limit int) (events []transcript.Event, hasMore bool, err error) {
	if limit < 1 {
		return nil, false, fmt.Errorf("%w: limit must be positive", ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT seq, provider, ts, role, text, tool_name, model, input_tokens, output_tokens, cache_tokens, raw
FROM events WHERE account_id = ? AND machine_id = ? AND session_id = ? AND seq > ?
ORDER BY seq LIMIT ?`, accountID, machineID, sessionID, afterSeq, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		e := transcript.Event{MachineID: machineID, SessionID: sessionID}
		var ts sql.NullString
		var tool, model sql.NullString
		var raw string
		if err := rows.Scan(&e.Seq, &e.Provider, &ts, &e.Role, &e.Text, &tool, &model,
			&e.Tokens.Input, &e.Tokens.Output, &e.Tokens.Cache, &raw); err != nil {
			return nil, false, err
		}
		e.TS = parseTime(ts)
		if tool.Valid {
			e.ToolName = &tool.String
		}
		if model.Valid {
			e.Model = &model.String
		}
		e.Raw = json.RawMessage(raw)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(events) > limit {
		return events[:limit], true, nil
	}
	return events, false, nil
}

// ListMachines returns the account's machines ordered by id.
func (s *Store) ListMachines(ctx context.Context, accountID string) ([]MachineInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, m.name, m.hostname, m.os, m.version, m.last_heartbeat_at,
    (SELECT COUNT(*) FROM sessions s WHERE s.account_id = m.account_id AND s.machine_id = m.id)
FROM machines m WHERE m.account_id = ? ORDER BY m.id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineInfo
	for rows.Next() {
		var mi MachineInfo
		var hb sql.NullString
		if err := rows.Scan(&mi.ID, &mi.Name, &mi.Hostname, &mi.OS, &mi.Version, &hb, &mi.SessionCount); err != nil {
			return nil, err
		}
		mi.LastHeartbeatAt = parseTime(hb)
		out = append(out, mi)
	}
	return out, rows.Err()
}

const (
	markOpen  = "\x02"
	markClose = "\x03"
)

var markReplacer = strings.NewReplacer(markOpen, "<mark>", markClose, "</mark>")

// highlight HTML-escapes an FTS snippet delimited by markOpen/markClose and
// swaps the delimiters for <mark> tags.
func highlight(s string) string {
	return markReplacer.Replace(html.EscapeString(s))
}

// searchSessions runs q as an FTS5 phrase over event text (and, as a
// fallback, session titles). Sessions are ranked by their best-matching
// event using bm25; title-only matches rank after transcript matches. Each
// result carries up to MaxSnippets snippets. Pages are offset-based.
func (s *Store) searchSessions(ctx context.Context, accountID string, f SessionFilter) ([]Session, string, error) {
	q := strings.TrimSpace(f.Q)
	phrase := ftsPhrase(q)
	offset := 0
	if f.Cursor != "" {
		c, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		offset = c.Offset
	}
	where := []string{`s.account_id = ?`, `(h.sid IS NOT NULL OR instr(lower(s.title), lower(?)) > 0)`}
	args := []any{phrase, accountID, accountID, q}
	for _, eq := range []struct{ col, val string }{
		{"s.machine_id", f.Machine}, {"s.provider", f.Provider},
		{"s.project", f.Project}, {"s.state", f.State},
	} {
		if eq.val != "" {
			where = append(where, eq.col+" = ?")
			args = append(args, eq.val)
		}
	}
	args = append(args, f.Limit+1, offset)
	query := `WITH hits AS (
    SELECT e.machine_id AS mid, e.session_id AS sid, MIN(events_fts.rank) AS score
    FROM events_fts JOIN events e ON e.rowid = events_fts.rowid
    WHERE events_fts MATCH ? AND e.account_id = ? GROUP BY e.machine_id, e.session_id)
SELECT ` + sessionColumns + ` FROM sessions s
LEFT JOIN hits h ON h.mid = s.machine_id AND h.sid = s.session_id
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY COALESCE(h.score, 0), COALESCE(s.last_activity_at, '') DESC, s.machine_id DESC, s.session_id DESC
LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	var out []Session
	for rows.Next() {
		se, _, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return nil, "", err
		}
		out = append(out, se)
	}
	if err := rows.Close(); err != nil {
		return nil, "", err
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > f.Limit {
		out = out[:f.Limit]
		next = encodeCursor(cursor{Offset: offset + f.Limit})
	}
	for i := range out {
		sn, err := s.snippets(ctx, accountID, out[i].MachineID, out[i].SessionID, phrase)
		if err != nil {
			return nil, "", err
		}
		out[i].Snippets = sn
	}
	return out, next, nil
}

func (s *Store) snippets(ctx context.Context, accountID, machineID, sessionID, phrase string) ([]Snippet, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.seq, snippet(events_fts, 0, ?, ?, '…', 24)
FROM events_fts JOIN events e ON e.rowid = events_fts.rowid
WHERE events_fts MATCH ? AND e.account_id = ? AND e.machine_id = ? AND e.session_id = ?
ORDER BY events_fts.rank, e.seq LIMIT ?`, markOpen, markClose, phrase, accountID, machineID, sessionID, MaxSnippets)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snippet
	for rows.Next() {
		var sn Snippet
		if err := rows.Scan(&sn.Seq, &sn.Text); err != nil {
			return nil, err
		}
		sn.Text = highlight(sn.Text)
		out = append(out, sn)
	}
	return out, rows.Err()
}
