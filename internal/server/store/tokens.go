package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Token scopes.
const (
	ScopeIngest = "ingest"
	ScopeRead   = "read"
)

// TokenPrefix starts every generated token so leaked ones are recognizable.
const TokenPrefix = "fk_"

// Token is a stored token. Only the SHA-256 hash of the secret is kept.
type Token struct {
	ID        string
	AccountID string
	Name      string
	Scope     string
	MachineID string // set for ingest tokens
	Hash      string // hex SHA-256 of the secret
	CreatedAt time.Time
	RevokedAt *time.Time
	// LastUsedAt is when the token last authenticated a request, to within
	// a minute. Nil if it never has.
	LastUsedAt *time.Time
}

// MaxTokenNameLen bounds a token's name.
const MaxTokenNameLen = 64

// HashToken returns the hex SHA-256 of a token secret.
func HashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// CreateToken generates a token owned by accountID, stores its hash, and
// returns the secret, which is never recoverable afterwards. Ingest tokens
// need a machine id.
func (s *Store) CreateToken(ctx context.Context, accountID, name, scope, machineID string) (Token, string, error) {
	return createToken(ctx, s.db, accountID, name, scope, machineID)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func createToken(ctx context.Context, db execer, accountID, name, scope, machineID string) (Token, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Token{}, "", fmt.Errorf("%w: token name is required", ErrInvalid)
	}
	if len([]rune(name)) > MaxTokenNameLen || hasControl(name) {
		return Token{}, "", fmt.Errorf("%w: token name must be at most %d printable characters", ErrInvalid, MaxTokenNameLen)
	}
	switch scope {
	case ScopeIngest:
		if machineID == "" {
			return Token{}, "", fmt.Errorf("%w: ingest tokens need a machine id", ErrInvalid)
		}
	case ScopeRead:
		machineID = ""
	default:
		return Token{}, "", fmt.Errorf("%w: scope must be ingest or read", ErrInvalid)
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Token{}, "", err
	}
	secret := TokenPrefix + base64.RawURLEncoding.EncodeToString(b[:])
	var id [4]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Token{}, "", err
	}
	t := Token{ID: hex.EncodeToString(id[:]), AccountID: accountID, Name: name, Scope: scope, MachineID: machineID,
		Hash: HashToken(secret), CreatedAt: time.Now().UTC().Truncate(time.Second)}
	_, err := db.ExecContext(ctx, `INSERT INTO tokens(id, account_id, name, scope, machine_id, hash, created_at) VALUES(?,?,?,?,?,?,?)`,
		t.ID, t.AccountID, t.Name, t.Scope, t.MachineID, t.Hash, t.CreatedAt.Format(time.RFC3339))
	if err != nil {
		return Token{}, "", err
	}
	return t, secret, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

const tokenColumns = `id, account_id, name, scope, machine_id, hash, created_at, revoked_at, last_used_at`

func scanTokens(rows *sql.Rows) ([]Token, error) {
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		var created string
		var revoked, used *string
		if err := rows.Scan(&t.ID, &t.AccountID, &t.Name, &t.Scope, &t.MachineID, &t.Hash, &created, &revoked, &used); err != nil {
			return nil, err
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339, created)
		if revoked != nil {
			if rt, err := time.Parse(time.RFC3339, *revoked); err == nil {
				t.RevokedAt = &rt
			}
		}
		if used != nil {
			if ut, err := time.Parse(time.RFC3339, *used); err == nil {
				t.LastUsedAt = &ut
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListTokens returns all tokens of every account, oldest first. Revoked ones
// are included unless activeOnly is set.
func (s *Store) ListTokens(ctx context.Context, activeOnly bool) ([]Token, error) {
	q := `SELECT ` + tokenColumns + ` FROM tokens`
	if activeOnly {
		q += ` WHERE revoked_at IS NULL`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	return scanTokens(rows)
}

// ListAccountTokens returns one account's tokens, revoked ones included,
// oldest first.
func (s *Store) ListAccountTokens(ctx context.Context, accountID string) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenColumns+` FROM tokens WHERE account_id = ? ORDER BY created_at, id`, accountID)
	if err != nil {
		return nil, err
	}
	return scanTokens(rows)
}

// GetToken returns the account's token with the id, or ErrNotFound. A token
// of another account is not found.
func (s *Store) GetToken(ctx context.Context, accountID, id string) (Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenColumns+` FROM tokens WHERE account_id = ? AND id = ?`, accountID, id)
	if err != nil {
		return Token{}, err
	}
	toks, err := scanTokens(rows)
	if err != nil {
		return Token{}, err
	}
	if len(toks) == 0 {
		return Token{}, ErrNotFound
	}
	return toks[0], nil
}

// RevokeAccountToken revokes the account's active token with the id. It
// reports false when there is none, including when the id belongs to
// another account.
func (s *Store) RevokeAccountToken(ctx context.Context, accountID, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE tokens SET revoked_at = ? WHERE revoked_at IS NULL AND account_id = ? AND id = ?`,
		time.Now().UTC().Format(time.RFC3339), accountID, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// TouchInterval is the finest resolution of Token.LastUsedAt: requests
// within it of the stored time do not write.
const TouchInterval = time.Minute

// TouchToken records that the token authenticated a request at now. It
// writes at most once per TouchInterval.
func (s *Store) TouchToken(ctx context.Context, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at <= ?)`,
		now.UTC().Format(time.RFC3339), id, now.Add(-TouchInterval).UTC().Format(time.RFC3339))
	return err
}

// RevokeToken revokes the active token with the given id or name. It
// returns how many tokens were revoked.
func (s *Store) RevokeToken(ctx context.Context, idOrName string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE tokens SET revoked_at = ? WHERE revoked_at IS NULL AND (id = ? OR name = ?)`,
		time.Now().UTC().Format(time.RFC3339), idOrName, idOrName)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
