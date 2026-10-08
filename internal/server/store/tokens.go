package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	Name      string
	Scope     string
	MachineID string // set for ingest tokens
	Hash      string // hex SHA-256 of the secret
	CreatedAt time.Time
	RevokedAt *time.Time
}

// HashToken returns the hex SHA-256 of a token secret.
func HashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// CreateToken generates a token, stores its hash, and returns the secret,
// which is never recoverable afterwards. Ingest tokens need a machine id.
func (s *Store) CreateToken(ctx context.Context, name, scope, machineID string) (Token, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Token{}, "", fmt.Errorf("%w: token name is required", ErrInvalid)
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
	t := Token{ID: hex.EncodeToString(id[:]), Name: name, Scope: scope, MachineID: machineID,
		Hash: HashToken(secret), CreatedAt: time.Now().UTC().Truncate(time.Second)}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens(id, name, scope, machine_id, hash, created_at) VALUES(?,?,?,?,?,?)`,
		t.ID, t.Name, t.Scope, t.MachineID, t.Hash, t.CreatedAt.Format(time.RFC3339))
	if err != nil {
		return Token{}, "", err
	}
	return t, secret, nil
}

// ListTokens returns all tokens, oldest first. Revoked ones are included
// unless activeOnly is set.
func (s *Store) ListTokens(ctx context.Context, activeOnly bool) ([]Token, error) {
	q := `SELECT id, name, scope, machine_id, hash, created_at, revoked_at FROM tokens`
	if activeOnly {
		q += ` WHERE revoked_at IS NULL`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		var created string
		var revoked *string
		if err := rows.Scan(&t.ID, &t.Name, &t.Scope, &t.MachineID, &t.Hash, &created, &revoked); err != nil {
			return nil, err
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339, created)
		if revoked != nil {
			if rt, err := time.Parse(time.RFC3339, *revoked); err == nil {
				t.RevokedAt = &rt
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
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
