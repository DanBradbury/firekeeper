package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Limits caps what one account may store. Zero means no limit.
type Limits struct {
	// MaxBytes caps the size of stored event text plus raw payloads.
	MaxBytes int64
	// MaxSessions caps the number of stored sessions.
	MaxSessions int64
}

var (
	// ErrStorageLimit marks an ingest refused because it would take the
	// account past Limits.MaxBytes. The whole request is rolled back.
	ErrStorageLimit = errors.New("storage limit reached")
	// ErrSessionLimit marks an ingest refused because it would take the
	// account past Limits.MaxSessions. The whole request is rolled back.
	ErrSessionLimit = errors.New("session limit reached")
)

// eventBytes is how much an event counts against the storage limit: its
// text plus its raw payload as stored.
func eventBytes(text, raw string) int64 { return int64(len(text) + len(raw)) }

// AccountStats is what an account currently stores.
type AccountStats struct {
	Machines    int64 `json:"machines"`
	Sessions    int64 `json:"sessions"`
	Events      int64 `json:"events"`
	Tokens      int64 `json:"tokens"`
	StoredBytes int64 `json:"stored_bytes"`
}

// AccountStats returns the account's stored totals. Tokens counts active
// ones. An unknown account is ErrNotFound.
func (s *Store) AccountStats(ctx context.Context, accountID string) (AccountStats, error) {
	var st AccountStats
	err := s.db.QueryRowContext(ctx, `
SELECT a.stored_bytes,
    (SELECT COUNT(*) FROM machines m WHERE m.account_id = a.id),
    (SELECT COUNT(*) FROM sessions s WHERE s.account_id = a.id),
    (SELECT COALESCE(SUM(event_count), 0) FROM sessions s WHERE s.account_id = a.id),
    (SELECT COUNT(*) FROM tokens t WHERE t.account_id = a.id AND t.revoked_at IS NULL)
FROM accounts a WHERE a.id = ?`, accountID).
		Scan(&st.StoredBytes, &st.Machines, &st.Sessions, &st.Events, &st.Tokens)
	if errors.Is(err, sql.ErrNoRows) {
		return st, ErrNotFound
	}
	return st, err
}

// SetPassword replaces an account's password hash and ends its browser
// sessions, so a reset also signs out whoever held the old password. Tokens
// are unaffected. The default account has no password and is refused.
func (s *Store) SetPassword(ctx context.Context, accountID, passwordHash string) error {
	if accountID == DefaultAccountID {
		return fmt.Errorf("%w: the default account has no password", ErrInvalid)
	}
	if passwordHash == "" {
		return fmt.Errorf("%w: password hash is required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE accounts SET password_hash = ? WHERE id = ?`, passwordHash, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM web_sessions WHERE account_id = ?`, accountID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteAccount removes an account and everything it owns in one
// transaction: its events (and so their full-text index entries, through
// the delete trigger), files, sessions, machines, tokens, browser sessions
// and invites. It returns what was removed. The default account owns
// single-user data and cannot be deleted. Open streams for the account end
// with its browser session; nothing is left to deliver.
func (s *Store) DeleteAccount(ctx context.Context, accountID string) (AccountStats, error) {
	if accountID == "" || accountID == DefaultAccountID {
		return AccountStats{}, fmt.Errorf("%w: the default account cannot be deleted", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AccountStats{}, err
	}
	defer tx.Rollback()
	var st AccountStats
	err = tx.QueryRowContext(ctx, `
SELECT a.stored_bytes,
    (SELECT COUNT(*) FROM machines m WHERE m.account_id = a.id),
    (SELECT COUNT(*) FROM sessions s WHERE s.account_id = a.id),
    (SELECT COALESCE(SUM(event_count), 0) FROM sessions s WHERE s.account_id = a.id),
    (SELECT COUNT(*) FROM tokens t WHERE t.account_id = a.id AND t.revoked_at IS NULL)
FROM accounts a WHERE a.id = ?`, accountID).
		Scan(&st.StoredBytes, &st.Machines, &st.Sessions, &st.Events, &st.Tokens)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountStats{}, ErrNotFound
	}
	if err != nil {
		return AccountStats{}, err
	}
	// Children before parents, because foreign keys are enforced. Invites
	// the account made or used go with it; a used code stays unusable.
	for _, q := range []string{
		`DELETE FROM session_files WHERE account_id = ?`,
		`DELETE FROM events WHERE account_id = ?`,
		`DELETE FROM sessions WHERE account_id = ?`,
		`DELETE FROM machines WHERE account_id = ?`,
		`DELETE FROM tokens WHERE account_id = ?`,
		`DELETE FROM web_sessions WHERE account_id = ?`,
		`DELETE FROM invites WHERE created_by = ? OR used_by = ?`,
		`DELETE FROM accounts WHERE id = ?`,
	} {
		args := make([]any, strings.Count(q, "?"))
		for i := range args {
			args[i] = accountID
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return AccountStats{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AccountStats{}, err
	}
	return st, nil
}
