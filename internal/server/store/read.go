package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	Model          string            `json:"model"`
	State          string            `json:"state"`
	Title          string            `json:"title"`
	StartedAt      *time.Time        `json:"started_at"`
	LastActivityAt *time.Time        `json:"last_activity_at"`
	EventCount     int64             `json:"event_count"`
	Tokens         transcript.Tokens `json:"tokens"`
}

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
	if err != nil || json.Unmarshal(b, &c) != nil || c.MachineID == "" || c.SessionID == "" {
		return c, fmt.Errorf("%w: invalid cursor", ErrInvalid)
	}
	return c, nil
}

// ftsPhrase quotes q as a single FTS5 phrase so user input cannot inject
// query syntax.
func ftsPhrase(q string) string {
	return `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
}

const sessionColumns = `s.machine_id, s.session_id, s.provider, s.cwd, s.project, s.branch, s.model,
    s.state, s.title, s.started_at, s.last_activity_at, s.event_count,
    s.input_tokens, s.output_tokens, s.cache_tokens`

func scanSession(sc interface{ Scan(...any) error }) (Session, string, error) {
	var se Session
	var started, last sql.NullString
	err := sc.Scan(&se.MachineID, &se.SessionID, &se.Provider, &se.CWD, &se.Project, &se.Branch,
		&se.Model, &se.State, &se.Title, &started, &last, &se.EventCount,
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
func (s *Store) ListSessions(ctx context.Context, f SessionFilter) ([]Session, string, error) {
	if f.Limit < 1 {
		return nil, "", fmt.Errorf("%w: limit must be positive", ErrInvalid)
	}
	var where []string
	var args []any
	for _, eq := range []struct{ col, val string }{
		{"s.machine_id", f.Machine}, {"s.provider", f.Provider},
		{"s.project", f.Project}, {"s.state", f.State},
	} {
		if eq.val != "" {
			where = append(where, eq.col+" = ?")
			args = append(args, eq.val)
		}
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		where = append(where, `(instr(lower(s.title), lower(?)) > 0 OR EXISTS (
    SELECT 1 FROM events_fts f JOIN events e ON e.rowid = f.rowid
    WHERE events_fts MATCH ? AND e.machine_id = s.machine_id AND e.session_id = s.session_id))`)
		args = append(args, q, ftsPhrase(q))
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
func (s *Store) GetSession(ctx context.Context, machineID, sessionID string) (Session, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions s
WHERE s.machine_id = ? AND s.session_id = ?`, machineID, sessionID)
	se, _, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return se, ErrNotFound
	}
	return se, err
}

// ListEvents returns up to limit events with seq greater than afterSeq, in
// seq order. hasMore reports whether further events follow. Pass afterSeq -1
// to start from the first event.
func (s *Store) ListEvents(ctx context.Context, machineID, sessionID string, afterSeq int64, limit int) (events []transcript.Event, hasMore bool, err error) {
	if limit < 1 {
		return nil, false, fmt.Errorf("%w: limit must be positive", ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT seq, provider, ts, role, text, tool_name, model, input_tokens, output_tokens, cache_tokens, raw
FROM events WHERE machine_id = ? AND session_id = ? AND seq > ?
ORDER BY seq LIMIT ?`, machineID, sessionID, afterSeq, limit+1)
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

// ListMachines returns every machine ordered by id.
func (s *Store) ListMachines(ctx context.Context) ([]MachineInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, m.name, m.hostname, m.os, m.version, m.last_heartbeat_at,
    (SELECT COUNT(*) FROM sessions s WHERE s.machine_id = m.id)
FROM machines m ORDER BY m.id`)
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
