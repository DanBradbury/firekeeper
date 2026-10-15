package store

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"strings"
)

// FilesSource says where a session's changed-file list comes from.
type FilesSource string

const (
	// FilesFromToolCalls lists the files the session's edit, write, and
	// patch tool calls named. It is best effort: files changed any other
	// way, such as by a shell command or a script, are missing.
	FilesFromToolCalls FilesSource = "tool_calls"
	// FilesFromGit lists what Git reports changed in the session's
	// repository since the commit checked out when it started.
	FilesFromGit FilesSource = "git"
)

// ParseFilesSource parses a files_source value. Empty means
// FilesFromToolCalls.
func ParseFilesSource(s string) (FilesSource, error) {
	switch FilesSource(s) {
	case "", FilesFromToolCalls:
		return FilesFromToolCalls, nil
	case FilesFromGit:
		return FilesFromGit, nil
	}
	return "", fmt.Errorf("%w: invalid files_source", ErrInvalid)
}

// storedFilesSource maps a stored files_source column to its value. Only
// FilesFromGit is ever stored; anything else reads as FilesFromToolCalls.
func storedFilesSource(s string) FilesSource {
	if FilesSource(s) == FilesFromGit {
		return FilesFromGit
	}
	return FilesFromToolCalls
}

// Bounds on a Git file list in one ingest request.
const (
	MaxGitFiles     = 5000
	maxGitPathBytes = 4096
)

// validateFiles checks a batch's files_source and Git file list.
func validateFiles(sm SessionMeta) error {
	source, err := ParseFilesSource(string(sm.FilesSource))
	if err != nil {
		return err
	}
	if source != FilesFromGit {
		if len(sm.Files) > 0 {
			return fmt.Errorf("%w: files requires files_source %q", ErrInvalid, FilesFromGit)
		}
		return nil
	}
	if len(sm.Files) > MaxGitFiles {
		return fmt.Errorf("%w: at most %d files per session", ErrInvalid, MaxGitFiles)
	}
	for _, f := range sm.Files {
		if strings.TrimSpace(f) == "" || len(f) > maxGitPathBytes {
			return fmt.Errorf("%w: invalid file path", ErrInvalid)
		}
	}
	return nil
}

// SessionFile is one file a session changed. Path is relative to the
// session's cwd, or absolute when the file is outside it or the cwd was
// unknown at ingest. FirstSeq and LastSeq are the first and last events
// whose tool calls changed it, and Changes counts those events; a file only
// Git reports has Changes 0 and both seqs -1.
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

// storeGitFiles replaces the session's Git file list with files, made
// relative to its stored cwd like tool-call paths, and marks the session's
// list as coming from Git.
func storeGitFiles(ctx context.Context, tx *sql.Tx, accountID, machineID, sessionID string, files []string) error {
	var cwd string
	if err := tx.QueryRowContext(ctx, `SELECT cwd FROM sessions WHERE account_id = ? AND machine_id = ? AND session_id = ?`,
		accountID, machineID, sessionID).Scan(&cwd); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET files_source = ? WHERE account_id = ? AND machine_id = ? AND session_id = ?`,
		string(FilesFromGit), accountID, machineID, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_git_files WHERE account_id = ? AND machine_id = ? AND session_id = ?`,
		accountID, machineID, sessionID); err != nil {
		return err
	}
	for _, f := range files {
		p := relPath(cwd, f)
		if p == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO session_git_files(account_id, machine_id, session_id, path) VALUES (?, ?, ?, ?)`,
			accountID, machineID, sessionID, p); err != nil {
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

// ListFiles returns the files a session changed, ordered by path. When the
// session has a Git file list, that list is the answer, with tool-call
// counts and seqs filled in for the files tool calls also named; otherwise
// it is the files tool calls changed. An unknown session has no files.
func (s *Store) ListFiles(ctx context.Context, accountID, machineID, sessionID string) ([]SessionFile, error) {
	var source string
	err := s.db.QueryRowContext(ctx, `SELECT files_source FROM sessions WHERE account_id = ? AND machine_id = ? AND session_id = ?`,
		accountID, machineID, sessionID).Scan(&source)
	if err == sql.ErrNoRows {
		return []SessionFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	query := `
SELECT path, first_seq, last_seq, changes FROM session_files
WHERE account_id = ? AND machine_id = ? AND session_id = ? ORDER BY path`
	if storedFilesSource(source) == FilesFromGit {
		query = `
SELECT g.path, COALESCE(f.first_seq, -1), COALESCE(f.last_seq, -1), COALESCE(f.changes, 0)
FROM session_git_files g
LEFT JOIN session_files f ON f.account_id = g.account_id AND f.machine_id = g.machine_id
    AND f.session_id = g.session_id AND f.path = g.path
WHERE g.account_id = ? AND g.machine_id = ? AND g.session_id = ? ORDER BY g.path`
	}
	rows, err := s.db.QueryContext(ctx, query, accountID, machineID, sessionID)
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
