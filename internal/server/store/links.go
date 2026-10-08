package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Machine linking. A linking machine calls StartLink and gets a device code
// it keeps and a short user code it shows. A signed-in person approves the
// user code with ApproveLink, naming the machine, and the machine's next
// PollLink receives an ingest token for that account and machine. Codes
// expire after LinkTTL and work once.

// Link limits.
const (
	LinkTTL = 10 * time.Minute
	// MaxPendingLinks bounds the unexpired link codes the server holds, so
	// unauthenticated starts cannot grow the table without limit.
	MaxPendingLinks = 1000
	// MaxMachineIDLen bounds the machine id a link carries.
	MaxMachineIDLen = 128
)

// UserCodeAlphabet omits characters people confuse: 0/O, 1/I/L, and the
// vowels that could spell words.
const userCodeAlphabet = "BCDFGHJKMNPQRSTVWXZ23456789"

var (
	// ErrLinkInvalid marks a code that is unknown, already approved, or
	// already used.
	ErrLinkInvalid = errors.New("invalid link code")
	// ErrLinkExpired marks a device code past LinkTTL.
	ErrLinkExpired = errors.New("link code expired")
	// ErrLinkBusy marks a server holding MaxPendingLinks codes.
	ErrLinkBusy = errors.New("too many pending link codes")
)

// LinkStart is what a linking machine gets back.
type LinkStart struct {
	// DeviceCode is the secret the machine polls with.
	DeviceCode string
	// UserCode is the short code a person approves, formatted XXXX-XXXX.
	UserCode  string
	ExpiresAt time.Time
}

// NormalizeUserCode upper-cases a typed user code and drops separators.
func NormalizeUserCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func newUserCode() (string, error) {
	// Reject bytes that would skew the distribution toward early letters.
	limit := byte(256 - 256%len(userCodeAlphabet))
	out := make([]byte, 0, 8)
	var buf [16]byte
	for len(out) < 8 {
		if _, err := rand.Read(buf[:]); err != nil {
			return "", err
		}
		for _, v := range buf {
			if v < limit && len(out) < 8 {
				out = append(out, userCodeAlphabet[int(v)%len(userCodeAlphabet)])
			}
		}
	}
	return string(out), nil
}

// FormatUserCode renders a normalized code as XXXX-XXXX.
func FormatUserCode(code string) string {
	if len(code) == 8 {
		return code[:4] + "-" + code[4:]
	}
	return code
}

// StartLink opens a link for machineID. It also deletes codes that expired
// more than an hour ago.
func (s *Store) StartLink(ctx context.Context, machineID string, now time.Time) (LinkStart, error) {
	if machineID == "" || len(machineID) > MaxMachineIDLen || hasControl(machineID) {
		return LinkStart{}, fmt.Errorf("%w: machine_id must be 1 to %d printable characters", ErrInvalid, MaxMachineIDLen)
	}
	device, err := randSecret("fkd_")
	if err != nil {
		return LinkStart{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LinkStart{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM link_codes WHERE expires_at <= ?`, at(now.Add(-time.Hour))); err != nil {
		return LinkStart{}, err
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM link_codes WHERE expires_at > ? AND consumed_at IS NULL`, at(now)).Scan(&pending); err != nil {
		return LinkStart{}, err
	}
	if pending >= MaxPendingLinks {
		return LinkStart{}, ErrLinkBusy
	}
	ls := LinkStart{DeviceCode: device, ExpiresAt: now.Add(LinkTTL).UTC()}
	// A user code collision on the unique hash is astronomically unlikely
	// with live codes, but retry rather than fail the caller.
	for try := 0; ; try++ {
		code, err := newUserCode()
		if err != nil {
			return LinkStart{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO link_codes(device_hash, user_hash, machine_id, created_at, expires_at) VALUES(?,?,?,?,?)`,
			hashSecret(device), hashSecret(code), machineID, at(now), at(ls.ExpiresAt))
		if err == nil {
			ls.UserCode = FormatUserCode(code)
			break
		}
		if try >= 4 || !strings.Contains(err.Error(), "UNIQUE") {
			return LinkStart{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return LinkStart{}, err
	}
	return ls, nil
}

// LinkApproval describes a link a person approved.
type LinkApproval struct {
	MachineID   string
	MachineName string
}

// ApproveLink approves the user code for accountID and names the machine.
// It fails with ErrLinkInvalid when the code is unknown, expired, or was
// already approved, by this account or another.
func (s *Store) ApproveLink(ctx context.Context, accountID, userCode, machineName string, now time.Time) (LinkApproval, error) {
	if err := requireAccount(accountID); err != nil {
		return LinkApproval{}, err
	}
	machineName = strings.TrimSpace(machineName)
	if machineName == "" || len([]rune(machineName)) > MaxTokenNameLen || hasControl(machineName) {
		return LinkApproval{}, fmt.Errorf("%w: machine name must be 1 to %d printable characters", ErrInvalid, MaxTokenNameLen)
	}
	code := NormalizeUserCode(userCode)
	if len(code) != 8 {
		return LinkApproval{}, ErrLinkInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LinkApproval{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
UPDATE link_codes SET account_id = ?, machine_name = ?, approved_at = ?
WHERE user_hash = ? AND approved_at IS NULL AND consumed_at IS NULL AND expires_at > ?`,
		accountID, machineName, at(now), hashSecret(code), at(now))
	if err != nil {
		return LinkApproval{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return LinkApproval{}, ErrLinkInvalid
	}
	var a LinkApproval
	a.MachineName = machineName
	if err := tx.QueryRowContext(ctx, `SELECT machine_id FROM link_codes WHERE user_hash = ?`, hashSecret(code)).Scan(&a.MachineID); err != nil {
		return LinkApproval{}, err
	}
	if err := tx.Commit(); err != nil {
		return LinkApproval{}, err
	}
	return a, nil
}

// LinkResult is the outcome of a poll. Token and Secret are set once, on
// the poll that consumes an approval.
type LinkResult struct {
	Approved bool
	Token    Token
	Secret   string
	Account  Account
}

// PollLink checks a device code. While nobody has approved it, the result
// is not Approved. On the first poll after approval it creates the ingest
// token (named for the machine, bound to its id, owned by the approving
// account) and marks the code used, in one transaction; later polls get
// ErrLinkInvalid. ErrLinkExpired means the code outlived LinkTTL.
func (s *Store) PollLink(ctx context.Context, deviceCode string, now time.Time) (LinkResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LinkResult{}, err
	}
	defer tx.Rollback()
	var (
		accountID, approved, consumed, expires *string
		machineID, machineName                 string
	)
	err = tx.QueryRowContext(ctx, `
SELECT account_id, approved_at, consumed_at, expires_at, machine_id, machine_name
FROM link_codes WHERE device_hash = ?`, hashSecret(deviceCode)).
		Scan(&accountID, &approved, &consumed, &expires, &machineID, &machineName)
	if errors.Is(err, sql.ErrNoRows) {
		return LinkResult{}, ErrLinkInvalid
	}
	if err != nil {
		return LinkResult{}, err
	}
	if consumed != nil {
		return LinkResult{}, ErrLinkInvalid
	}
	if exp, err := time.Parse(time.RFC3339Nano, *expires); err != nil || !exp.After(now) {
		return LinkResult{}, ErrLinkExpired
	}
	if approved == nil || accountID == nil {
		return LinkResult{}, nil
	}
	acct, err := scanAccount(tx.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = ?`, *accountID))
	if err != nil {
		return LinkResult{}, err
	}
	if acct.Disabled {
		return LinkResult{}, ErrLinkInvalid
	}
	res, err := tx.ExecContext(ctx, `UPDATE link_codes SET consumed_at = ? WHERE device_hash = ? AND consumed_at IS NULL`,
		at(now), hashSecret(deviceCode))
	if err != nil {
		return LinkResult{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return LinkResult{}, ErrLinkInvalid
	}
	tok, secret, err := createToken(ctx, tx, acct.ID, machineName, ScopeIngest, machineID)
	if err != nil {
		return LinkResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return LinkResult{}, err
	}
	return LinkResult{Approved: true, Token: tok, Secret: secret, Account: acct}, nil
}
