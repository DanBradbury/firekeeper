package store

import (
	"context"
	"database/sql"
	"path"
	"strings"
)

// SessionFile is one file a session's tool calls changed. Path is relative
// to the session's cwd, or absolute when the file is outside it or the cwd
// was unknown at ingest. FirstSeq and LastSeq are the first and last events
// that changed it; Changes counts those events.
type SessionFile struct {
	Path     string `json:"path"`
	Absolute bool   `json:"absolute"`
	FirstSeq int64  `json:"first_seq"`
	LastSeq  int64  `json:"last_seq"`
	Changes  int64  `json:"changes"`
	// URL links to the file on its repository host, when the server has a
	// link template and the file is inside the cwd. Set by the API.
	URL string `json:"url,omitempty"`
}

// touch is one file named by one newly stored event.
type touch struct {
	path string
	seq  int64
}

// storeFiles records touched paths against the session's stored cwd. The
// paths and cwd arrive redacted, so both share the same "~" home prefix.
func storeFiles(ctx context.Context, tx *sql.Tx, accountID, machineID, sessionID string, touched []touch) error {
	if len(touched) == 0 {
		return nil
	}
	var cwd string
	if err := tx.QueryRowContext(ctx, `SELECT cwd FROM sessions WHERE account_id = ? AND machine_id = ? AND session_id = ?`,
		accountID, machineID, sessionID).Scan(&cwd); err != nil {
		return err
	}
	for _, t := range touched {
		p := relPath(cwd, t.path)
		if p == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO session_files(account_id, machine_id, session_id, path, first_seq, last_seq, changes)
VALUES (?, ?, ?, ?, ?, ?, 1)
ON CONFLICT(account_id, machine_id, session_id, path) DO UPDATE SET
    first_seq = MIN(session_files.first_seq, excluded.first_seq),
    last_seq = MAX(session_files.last_seq, excluded.last_seq),
    changes = session_files.changes + 1`,
			accountID, machineID, sessionID, p, t.seq, t.seq); err != nil {
			return err
		}
	}
	return nil
}

// isAbs reports whether p is absolute, counting the reporter's "~" home
// prefix and Windows drive or UNC paths.
func isAbs(p string) bool {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		(('a' <= p[0] && p[0] <= 'z') || ('A' <= p[0] && p[0] <= 'Z'))
}

// relPath returns p relative to cwd when it lies inside cwd, and otherwise
// p as an absolute path. A relative p is taken as relative to cwd; one that
// climbs out of it is joined onto cwd. With no usable cwd, p is only
// cleaned. Paths use forward slashes whatever the server's platform.
func relPath(cwd, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !strings.Contains(p, "/") && strings.Contains(p, `\`) {
		p = strings.ReplaceAll(p, `\`, "/")
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" || !isAbs(cwd) {
		return path.Clean(p)
	}
	cwd = path.Clean(cwd)
	abs := p
	if !isAbs(p) {
		abs = cwd + "/" + p
	}
	abs = path.Clean(abs)
	if abs == cwd {
		return abs // the directory itself is not a file inside it
	}
	prefix := strings.TrimSuffix(cwd, "/") + "/"
	if rel, ok := strings.CutPrefix(abs, prefix); ok {
		return rel
	}
	return abs
}

// ListFiles returns the files a session changed, ordered by path.
func (s *Store) ListFiles(ctx context.Context, accountID, machineID, sessionID string) ([]SessionFile, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT path, first_seq, last_seq, changes FROM session_files
WHERE account_id = ? AND machine_id = ? AND session_id = ? ORDER BY path`, accountID, machineID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionFile{}
	for rows.Next() {
		var f SessionFile
		if err := rows.Scan(&f.Path, &f.FirstSeq, &f.LastSeq, &f.Changes); err != nil {
			return nil, err
		}
		f.Absolute = isAbs(f.Path)
		out = append(out, f)
	}
	return out, rows.Err()
}
