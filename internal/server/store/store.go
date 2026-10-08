// Package store persists machines, sessions, and transcript events in SQLite.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DanBradbury/firekeeper/internal/transcript"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Machine identifies a reporting host.
type Machine struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Version  string `json:"version"`
}

// SessionMeta is the session metadata carried by an ingest request.
// Event count and token totals are always recomputed from stored events.
type SessionMeta struct {
	SessionID      string     `json:"session_id"`
	Provider       string     `json:"provider"`
	CWD            string     `json:"cwd"`
	Project        string     `json:"project"`
	Branch         string     `json:"branch"`
	Model          string     `json:"model"`
	State          string     `json:"state"`
	Title          string     `json:"title"`
	StartedAt      *time.Time `json:"started_at"`
	LastActivityAt *time.Time `json:"last_activity_at"`
}

// SessionBatch is one session's metadata plus new events.
type SessionBatch struct {
	Meta   SessionMeta        `json:"meta"`
	Events []transcript.Event `json:"events"`
}

// Heartbeat is a lightweight state update for one session.
type Heartbeat struct {
	SessionID      string     `json:"session_id"`
	State          string     `json:"state"`
	LastActivityAt *time.Time `json:"last_activity_at"`
}

// ChangeKind names a change notification.
type ChangeKind string

const (
	SessionUpdated ChangeKind = "session.updated"
	EventAppended  ChangeKind = "event.appended"
	MachineStatus  ChangeKind = "machine.status"
)

// Change is emitted after a transaction commits. AccountID names the tenant
// it belongs to; consumers must not deliver it to anyone else. SessionID is
// empty for machine-level changes. Seq is the highest newly stored seq for
// EventAppended.
type Change struct {
	Kind      ChangeKind
	AccountID string
	MachineID string
	SessionID string
	Seq       int64
}

// Valid session states.
var validStates = map[string]bool{
	"ACTIVE": true, "WAITING": true, "NEEDS_INPUT": true, "ENDED": true, "UNKNOWN": true,
}

// ErrInvalid marks a request rejected for bad input.
var ErrInvalid = errors.New("invalid request")

const changeBuffer = 1024

// Store is a SQLite-backed store. It is safe for concurrent use.
type Store struct {
	db      *sql.DB
	changes chan Change

	// mu guards closed so notify never sends on a closed channel.
	mu     sync.RWMutex
	closed bool
}

// Open opens (creating if needed) the database at path and applies migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// A single connection serializes writers and keeps behavior simple.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, changes: make(chan Change, changeBuffer)}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the change channel and the database. It is safe to call
// while writes are in flight; their notifications are discarded.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.changes)
	s.mu.Unlock()
	return s.db.Close()
}

// Changes returns the notification channel. Notifications are dropped when
// the buffer is full rather than blocking ingest, so consumers must treat
// them as hints and re-read state (for example events after the last seq
// they saw) instead of relying on every notification arriving.
func (s *Store) Changes() <-chan Change { return s.changes }

func (s *Store) notify(cs []Change) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	for _, c := range cs {
		select {
		case s.changes <- c:
		default:
		}
	}
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var cur int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&cur); err != nil {
		return err
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		prefix, _, _ := strings.Cut(name, "_")
		v, err := strconv.Atoi(prefix)
		if err != nil {
			return fmt.Errorf("store: bad migration name %q", name)
		}
		if v <= cur {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES (?)`, v); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// SchemaVersion returns the applied schema version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v)
	return v, err
}

// timeLayout is fixed-width UTC so stored timestamps order correctly as
// strings. RFC3339Nano trims trailing zeros, which breaks lexical order.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

func fmtTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(timeLayout)
}

func upsertMachine(ctx context.Context, tx *sql.Tx, accountID string, m Machine, now time.Time, heartbeat bool) error {
	var hb any
	if heartbeat {
		hb = now.UTC().Format(timeLayout)
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO machines(account_id, id, name, hostname, os, version, last_heartbeat_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(account_id, id) DO UPDATE SET
    name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE machines.name END,
    hostname = CASE WHEN excluded.hostname <> '' THEN excluded.hostname ELSE machines.hostname END,
    os = CASE WHEN excluded.os <> '' THEN excluded.os ELSE machines.os END,
    version = CASE WHEN excluded.version <> '' THEN excluded.version ELSE machines.version END,
    last_heartbeat_at = COALESCE(excluded.last_heartbeat_at, machines.last_heartbeat_at)`,
		accountID, m.ID, m.Name, m.Hostname, m.OS, m.Version, hb)
	return err
}

// requireAccount rejects an empty account id so a missing tenant can never
// turn into an unscoped write.
func requireAccount(accountID string) error {
	if accountID == "" {
		return fmt.Errorf("%w: account is required", ErrInvalid)
	}
	return nil
}

// Ingest stores one request for an account in a single transaction. It
// returns how many events were newly stored and how many already existed.
func (s *Store) Ingest(ctx context.Context, accountID string, m Machine, batches []SessionBatch) (accepted, duplicates int, err error) {
	if err := requireAccount(accountID); err != nil {
		return 0, 0, err
	}
	if m.ID == "" {
		return 0, 0, fmt.Errorf("%w: machine.id is required", ErrInvalid)
	}
	for _, b := range batches {
		if b.Meta.SessionID == "" {
			return 0, 0, fmt.Errorf("%w: session_id is required", ErrInvalid)
		}
		if !transcript.Provider(b.Meta.Provider).Valid() {
			return 0, 0, fmt.Errorf("%w: invalid provider", ErrInvalid)
		}
		if b.Meta.State != "" && !validStates[b.Meta.State] {
			return 0, 0, fmt.Errorf("%w: invalid state", ErrInvalid)
		}
		for _, e := range b.Events {
			if err := e.Validate(); err != nil {
				return 0, 0, fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			if e.SessionID != "" && e.SessionID != b.Meta.SessionID {
				return 0, 0, fmt.Errorf("%w: event session_id mismatch", ErrInvalid)
			}
			if e.MachineID != "" && e.MachineID != m.ID {
				return 0, 0, fmt.Errorf("%w: event machine_id mismatch", ErrInvalid)
			}
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	now := time.Now()
	if err := upsertMachine(ctx, tx, accountID, m, now, true); err != nil {
		return 0, 0, err
	}
	var changes []Change
	changes = append(changes, Change{Kind: MachineStatus, AccountID: accountID, MachineID: m.ID})

	for _, b := range batches {
		sm := b.Meta
		state := sm.State
		if state == "" {
			state = "UNKNOWN"
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO sessions(account_id, machine_id, session_id, provider, cwd, project, branch, model, state, title, started_at, last_activity_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(account_id, machine_id, session_id) DO UPDATE SET
    provider = excluded.provider,
    cwd = CASE WHEN excluded.cwd <> '' THEN excluded.cwd ELSE sessions.cwd END,
    project = CASE WHEN excluded.project <> '' THEN excluded.project ELSE sessions.project END,
    branch = CASE WHEN excluded.branch <> '' THEN excluded.branch ELSE sessions.branch END,
    model = CASE WHEN excluded.model <> '' THEN excluded.model ELSE sessions.model END,
    state = CASE WHEN ? <> '' THEN excluded.state ELSE sessions.state END,
    title = CASE WHEN excluded.title <> '' THEN excluded.title ELSE sessions.title END,
    started_at = COALESCE(sessions.started_at, excluded.started_at),
    last_activity_at = CASE
        WHEN sessions.last_activity_at IS NULL OR excluded.last_activity_at > sessions.last_activity_at
        THEN COALESCE(excluded.last_activity_at, sessions.last_activity_at)
        ELSE sessions.last_activity_at END`,
			accountID, m.ID, sm.SessionID, sm.Provider, sm.CWD, sm.Project, sm.Branch, sm.Model, state, sm.Title,
			fmtTime(sm.StartedAt), fmtTime(sm.LastActivityAt), sm.State); err != nil {
			return 0, 0, err
		}

		var maxNew int64 = -1
		for _, e := range b.Events {
			raw := string(e.Raw)
			if raw == "" {
				raw = "null"
			}
			res, err := tx.ExecContext(ctx, `
INSERT INTO events(account_id, machine_id, session_id, seq, provider, ts, role, text, tool_name, model, input_tokens, output_tokens, cache_tokens, raw)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(account_id, machine_id, session_id, seq) DO NOTHING`,
				accountID, m.ID, sm.SessionID, e.Seq, sm.Provider, fmtTime(e.TS), string(e.Role), e.Text,
				e.ToolName, e.Model, e.Tokens.Input, e.Tokens.Output, e.Tokens.Cache, raw)
			if err != nil {
				return 0, 0, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				accepted++
				if e.Seq > maxNew {
					maxNew = e.Seq
				}
			} else {
				duplicates++
			}
		}

		if _, err := tx.ExecContext(ctx, `
UPDATE sessions SET
    event_count = (SELECT COUNT(*) FROM events e WHERE e.account_id = sessions.account_id AND e.machine_id = sessions.machine_id AND e.session_id = sessions.session_id),
    input_tokens = (SELECT COALESCE(SUM(input_tokens), 0) FROM events e WHERE e.account_id = sessions.account_id AND e.machine_id = sessions.machine_id AND e.session_id = sessions.session_id),
    output_tokens = (SELECT COALESCE(SUM(output_tokens), 0) FROM events e WHERE e.account_id = sessions.account_id AND e.machine_id = sessions.machine_id AND e.session_id = sessions.session_id),
    cache_tokens = (SELECT COALESCE(SUM(cache_tokens), 0) FROM events e WHERE e.account_id = sessions.account_id AND e.machine_id = sessions.machine_id AND e.session_id = sessions.session_id)
WHERE account_id = ? AND machine_id = ? AND session_id = ?`, accountID, m.ID, sm.SessionID); err != nil {
			return 0, 0, err
		}
		changes = append(changes, Change{Kind: SessionUpdated, AccountID: accountID, MachineID: m.ID, SessionID: sm.SessionID})
		if maxNew >= 0 {
			changes = append(changes, Change{Kind: EventAppended, AccountID: accountID, MachineID: m.ID, SessionID: sm.SessionID, Seq: maxNew})
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	s.notify(changes)
	return accepted, duplicates, nil
}

// Heartbeat marks the machine online and updates session states. Unknown
// sessions are ignored.
func (s *Store) Heartbeat(ctx context.Context, accountID string, m Machine, hbs []Heartbeat) error {
	if err := requireAccount(accountID); err != nil {
		return err
	}
	if m.ID == "" {
		return fmt.Errorf("%w: machine.id is required", ErrInvalid)
	}
	for _, h := range hbs {
		if h.SessionID == "" {
			return fmt.Errorf("%w: session_id is required", ErrInvalid)
		}
		if !validStates[h.State] {
			return fmt.Errorf("%w: invalid state", ErrInvalid)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := upsertMachine(ctx, tx, accountID, m, time.Now(), true); err != nil {
		return err
	}
	changes := []Change{{Kind: MachineStatus, AccountID: accountID, MachineID: m.ID}}
	for _, h := range hbs {
		res, err := tx.ExecContext(ctx, `
UPDATE sessions SET state = ?,
    last_activity_at = CASE
        WHEN ? IS NOT NULL AND (last_activity_at IS NULL OR ? > last_activity_at) THEN ?
        ELSE last_activity_at END
WHERE account_id = ? AND machine_id = ? AND session_id = ?`,
			h.State, fmtTime(h.LastActivityAt), fmtTime(h.LastActivityAt), fmtTime(h.LastActivityAt), accountID, m.ID, h.SessionID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			changes = append(changes, Change{Kind: SessionUpdated, AccountID: accountID, MachineID: m.ID, SessionID: h.SessionID})
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notify(changes)
	return nil
}

// SessionStats holds recomputed totals for one session.
type SessionStats struct {
	EventCount           int64
	Input, Output, Cache int64
	State                string
}

// Stats returns stored totals for a session (used by tests and handlers).
func (s *Store) Stats(ctx context.Context, accountID, machineID, sessionID string) (SessionStats, error) {
	var st SessionStats
	err := s.db.QueryRowContext(ctx, `SELECT event_count, input_tokens, output_tokens, cache_tokens, state
FROM sessions WHERE account_id = ? AND machine_id = ? AND session_id = ?`, accountID, machineID, sessionID).
		Scan(&st.EventCount, &st.Input, &st.Output, &st.Cache, &st.State)
	return st, err
}

// SearchText returns "machine:session:seq" keys of events whose text matches
// the FTS5 query.
func (s *Store) SearchText(ctx context.Context, accountID, query string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT e.machine_id, e.session_id, e.seq FROM events_fts f
JOIN events e ON e.rowid = f.rowid
WHERE events_fts MATCH ? AND e.account_id = ? ORDER BY e.machine_id, e.session_id, e.seq LIMIT ?`, query, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m, sid string
		var seq int64
		if err := rows.Scan(&m, &sid, &seq); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s:%s:%d", m, sid, seq))
	}
	return out, rows.Err()
}
