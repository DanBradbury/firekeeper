package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

// DefaultAccountID owns data that exists before accounts do and everything
// in single-user mode. It has no email or password, so nobody can sign in
// to it.
const DefaultAccountID = "default"

// Prefixes of generated secrets, so leaked ones are recognizable.
const (
	InvitePrefix  = "fki_"
	SessionPrefix = "fks_"
)

// Account and password bounds.
const (
	MaxEmailLen    = 254
	MinPasswordLen = 10
	MaxPasswordLen = 256
)

var (
	// ErrEmailTaken marks a signup for an email that already has an account.
	ErrEmailTaken = errors.New("email already registered")
	// ErrInviteInvalid marks an invite code that is unknown, used or expired.
	ErrInviteInvalid = errors.New("invalid invite code")
)

// Account is a person's tenant. PasswordHash is empty for the default
// account.
type Account struct {
	ID           string
	Email        string
	PasswordHash string
	CreatedAt    time.Time
	Disabled     bool
}

// NormalizeEmail lowercases and validates an email address. It accepts only
// a bare address, not "Name <addr>".
func NormalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || len(email) > MaxEmailLen {
		return "", fmt.Errorf("%w: a valid email is required", ErrInvalid)
	}
	a, err := mail.ParseAddress(email)
	if err != nil || a.Address != email || a.Name != "" || !strings.Contains(email, "@") {
		return "", fmt.Errorf("%w: a valid email is required", ErrInvalid)
	}
	return email, nil
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randSecret(prefix string) (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func at(t time.Time) string { return t.UTC().Format(timeLayout) }

// CreateAccount stores a new account whose email is normalized and unique.
// passwordHash is the encoded hash, never the password. A non-empty invite
// code is consumed in the same transaction, so a code works once even under
// concurrent signups.
func (s *Store) CreateAccount(ctx context.Context, email, passwordHash, inviteCode string, now time.Time) (Account, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return Account{}, err
	}
	if passwordHash == "" {
		return Account{}, fmt.Errorf("%w: password hash is required", ErrInvalid)
	}
	id, err := randHex(8)
	if err != nil {
		return Account{}, err
	}
	a := Account{ID: id, Email: email, PasswordHash: passwordHash, CreatedAt: now.UTC().Truncate(time.Second)}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO accounts(id, email, password_hash, created_at, disabled) VALUES(?,?,?,?,0)`,
		a.ID, a.Email, a.PasswordHash, at(now)); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Account{}, ErrEmailTaken
		}
		return Account{}, err
	}
	if inviteCode != "" {
		res, err := tx.ExecContext(ctx, `UPDATE invites SET used_by = ? WHERE code_hash = ? AND used_by IS NULL AND expires_at > ?`,
			a.ID, hashSecret(inviteCode), at(now))
		if err != nil {
			return Account{}, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return Account{}, ErrInviteInvalid
		}
	}
	if err := tx.Commit(); err != nil {
		return Account{}, err
	}
	return a, nil
}

func scanAccount(sc interface{ Scan(...any) error }) (Account, error) {
	var a Account
	var created string
	var disabled int
	if err := sc.Scan(&a.ID, &a.Email, &a.PasswordHash, &created, &disabled); err != nil {
		return a, err
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	a.Disabled = disabled != 0
	return a, nil
}

const accountColumns = `id, email, password_hash, created_at, disabled`

// AccountByEmail returns the account with the email, or ErrNotFound.
func (s *Store) AccountByEmail(ctx context.Context, email string) (Account, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return Account{}, ErrNotFound
	}
	a, err := scanAccount(s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE email = ?`, email))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// GetAccount returns the account with the id, or ErrNotFound.
func (s *Store) GetAccount(ctx context.Context, id string) (Account, error) {
	a, err := scanAccount(s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// SetAccountDisabled disables or re-enables an account. A disabled account
// cannot sign in, and its sessions and tokens stop working. Disabling also
// deletes its browser sessions.
func (s *Store) SetAccountDisabled(ctx context.Context, id string, disabled bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	d := 0
	if disabled {
		d = 1
	}
	res, err := tx.ExecContext(ctx, `UPDATE accounts SET disabled = ? WHERE id = ?`, d, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if disabled {
		if _, err := tx.ExecContext(ctx, `DELETE FROM web_sessions WHERE account_id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// HasAccounts reports whether any account other than the default exists.
// Once one does, the API requires authentication.
func (s *Store) HasAccounts(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id <> ?)`, DefaultAccountID).Scan(&n)
	return n == 1, err
}

// CreateInvite stores an invite valid until now+ttl and returns the code,
// which is not recoverable afterwards. createdBy is the inviting account.
func (s *Store) CreateInvite(ctx context.Context, createdBy string, ttl time.Duration, now time.Time) (string, error) {
	if ttl <= 0 {
		return "", fmt.Errorf("%w: invite lifetime must be positive", ErrInvalid)
	}
	code, err := randSecret(InvitePrefix)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO invites(code_hash, created_by, created_at, expires_at) VALUES(?,?,?,?)`,
		hashSecret(code), createdBy, at(now), at(now.Add(ttl)))
	if err != nil {
		return "", err
	}
	return code, nil
}

// WebSession is a signed-in browser session.
type WebSession struct {
	AccountID string
	CSRFToken string
	ExpiresAt time.Time
	// ID is the stored hash of the cookie value, used to delete the session.
	ID string
}

// CreateWebSession starts a browser session lasting ttl and returns the
// cookie value, which is stored only as a hash. It also deletes expired
// sessions.
func (s *Store) CreateWebSession(ctx context.Context, accountID string, ttl time.Duration, now time.Time) (cookie string, ws WebSession, err error) {
	cookie, err = randSecret(SessionPrefix)
	if err != nil {
		return "", ws, err
	}
	csrf, err := randSecret("")
	if err != nil {
		return "", ws, err
	}
	ws = WebSession{AccountID: accountID, CSRFToken: csrf, ExpiresAt: now.Add(ttl).UTC(), ID: hashSecret(cookie)}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE expires_at <= ?`, at(now)); err != nil {
		return "", ws, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO web_sessions(id_hash, account_id, csrf_token, created_at, expires_at) VALUES(?,?,?,?,?)`,
		ws.ID, accountID, csrf, at(now), at(ws.ExpiresAt))
	if err != nil {
		return "", WebSession{}, err
	}
	return cookie, ws, nil
}

// LookupWebSession returns the live session for a cookie value, or
// ErrNotFound if it is unknown, expired, or its account is disabled.
func (s *Store) LookupWebSession(ctx context.Context, cookie string, now time.Time) (WebSession, error) {
	var ws WebSession
	var exp string
	err := s.db.QueryRowContext(ctx, `
SELECT w.id_hash, w.account_id, w.csrf_token, w.expires_at
FROM web_sessions w JOIN accounts a ON a.id = w.account_id
WHERE w.id_hash = ? AND w.expires_at > ? AND a.disabled = 0`, hashSecret(cookie), at(now)).
		Scan(&ws.ID, &ws.AccountID, &ws.CSRFToken, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return ws, ErrNotFound
	}
	if err != nil {
		return ws, err
	}
	ws.ExpiresAt, _ = time.Parse(time.RFC3339Nano, exp)
	return ws, nil
}

// DeleteWebSession ends a session by its ID. Unknown ids are not an error.
func (s *Store) DeleteWebSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE id_hash = ?`, id)
	return err
}

// WebSessionAlive reports whether the session with the given id is still
// valid. A lookup error counts as not alive.
func (s *Store) WebSessionAlive(ctx context.Context, id string, now time.Time) bool {
	var n int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM web_sessions w JOIN accounts a ON a.id = w.account_id
WHERE w.id_hash = ? AND w.expires_at > ? AND a.disabled = 0`, id, at(now)).Scan(&n)
	return err == nil && n == 1
}

// ListAccounts returns every account, oldest first, including disabled ones
// and the default account.
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountColumns+` FROM accounts ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
