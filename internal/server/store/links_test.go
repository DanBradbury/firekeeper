package store

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func linkStore(t *testing.T) (*Store, Account, Account) {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now()
	a, err := s.CreateAccount(context.Background(), "a@example.com", "$argon2id$stub", "", now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateAccount(context.Background(), "b@example.com", "$argon2id$stub", "", now)
	if err != nil {
		t.Fatal(err)
	}
	return s, a, b
}

func TestLinkHappyPath(t *testing.T) {
	ctx := context.Background()
	s, a, _ := linkStore(t)
	now := time.Now()

	ls, err := s.StartLink(ctx, "machine-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls.UserCode) != 9 || ls.UserCode[4] != '-' || !strings.HasPrefix(ls.DeviceCode, "fkd_") {
		t.Fatalf("codes = %q, %q", ls.UserCode, ls.DeviceCode)
	}
	if got := ls.ExpiresAt.Sub(now); got != LinkTTL {
		t.Fatalf("lifetime %v, want %v", got, LinkTTL)
	}
	if res, err := s.PollLink(ctx, ls.DeviceCode, now); err != nil || res.Approved {
		t.Fatalf("poll before approval = %+v, %v", res, err)
	}
	// Typing is forgiving: case and separators do not matter.
	typed := strings.ToLower(strings.ReplaceAll(ls.UserCode, "-", " "))
	ap, err := s.ApproveLink(ctx, a.ID, typed, "  My Laptop ", now)
	if err != nil || ap.MachineID != "machine-1" || ap.MachineName != "My Laptop" {
		t.Fatalf("approve = %+v, %v", ap, err)
	}
	res, err := s.PollLink(ctx, ls.DeviceCode, now)
	if err != nil || !res.Approved {
		t.Fatalf("poll after approval = %+v, %v", res, err)
	}
	tok := res.Token
	if tok.AccountID != a.ID || tok.Scope != ScopeIngest || tok.MachineID != "machine-1" || tok.Name != "My Laptop" {
		t.Fatalf("token = %+v", tok)
	}
	if !strings.HasPrefix(res.Secret, TokenPrefix) || tok.Hash != HashToken(res.Secret) {
		t.Fatal("secret does not match the stored hash")
	}
	// The link table keeps hashes only.
	var n int
	for _, secret := range []string{res.Secret, ls.DeviceCode, NormalizeUserCode(ls.UserCode)} {
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM link_codes WHERE device_hash = ? OR user_hash = ? OR machine_name = ?`, secret, secret, secret).Scan(&n); err != nil || n != 0 {
			t.Fatalf("secret stored in the clear (%d, %v)", n, err)
		}
	}
}

func TestLinkSingleUseAndExpiry(t *testing.T) {
	ctx := context.Background()
	s, a, b := linkStore(t)
	now := time.Now()

	// Reused: a second poll after the token was handed over fails.
	ls, _ := s.StartLink(ctx, "m1", now)
	if _, err := s.ApproveLink(ctx, a.ID, ls.UserCode, "m", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PollLink(ctx, ls.DeviceCode, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PollLink(ctx, ls.DeviceCode, now); !errors.Is(err, ErrLinkInvalid) {
		t.Fatalf("second poll = %v, want ErrLinkInvalid", err)
	}
	// ...and the user code cannot be approved again, by anyone.
	for _, id := range []string{a.ID, b.ID} {
		if _, err := s.ApproveLink(ctx, id, ls.UserCode, "m", now); !errors.Is(err, ErrLinkInvalid) {
			t.Fatalf("approve of a used code by %s = %v", id, err)
		}
	}

	// Wrong account: once A approved, B cannot take the code over, and the
	// token goes to A only.
	ls, _ = s.StartLink(ctx, "m2", now)
	if _, err := s.ApproveLink(ctx, a.ID, ls.UserCode, "a-box", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveLink(ctx, b.ID, ls.UserCode, "b-box", now); !errors.Is(err, ErrLinkInvalid) {
		t.Fatalf("second approver = %v, want ErrLinkInvalid", err)
	}
	res, err := s.PollLink(ctx, ls.DeviceCode, now)
	if err != nil || res.Token.AccountID != a.ID || res.Token.Name != "a-box" {
		t.Fatalf("token = %+v, %v", res.Token, err)
	}

	// Expired before approval: neither approve nor poll works.
	ls, _ = s.StartLink(ctx, "m3", now)
	late := now.Add(LinkTTL + time.Second)
	if _, err := s.ApproveLink(ctx, a.ID, ls.UserCode, "m", late); !errors.Is(err, ErrLinkInvalid) {
		t.Fatalf("approve after expiry = %v", err)
	}
	if _, err := s.PollLink(ctx, ls.DeviceCode, late); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("poll after expiry = %v", err)
	}
	// Approved but not collected in time is expired too, and mints nothing.
	ls, _ = s.StartLink(ctx, "m4", now)
	if _, err := s.ApproveLink(ctx, a.ID, ls.UserCode, "m", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PollLink(ctx, ls.DeviceCode, late); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("late collect = %v", err)
	}
	toks, _ := s.ListAccountTokens(ctx, a.ID)
	if len(toks) != 2 { // m1 and m2 only
		t.Fatalf("%d tokens, want 2", len(toks))
	}

	// Unknown codes.
	if _, err := s.PollLink(ctx, "fkd_nope", now); !errors.Is(err, ErrLinkInvalid) {
		t.Fatalf("unknown device code = %v", err)
	}
	for _, code := range []string{"", "ABCD", "AAAA-AAAA"} {
		if _, err := s.ApproveLink(ctx, a.ID, code, "m", now); !errors.Is(err, ErrLinkInvalid) {
			t.Fatalf("approve %q = %v", code, err)
		}
	}
}

func TestLinkDisabledAccountGetsNoToken(t *testing.T) {
	ctx := context.Background()
	s, a, _ := linkStore(t)
	now := time.Now()
	ls, _ := s.StartLink(ctx, "m1", now)
	if _, err := s.ApproveLink(ctx, a.ID, ls.UserCode, "m", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountDisabled(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PollLink(ctx, ls.DeviceCode, now); !errors.Is(err, ErrLinkInvalid) {
		t.Fatalf("poll for a disabled account = %v", err)
	}
	if toks, _ := s.ListAccountTokens(ctx, a.ID); len(toks) != 0 {
		t.Fatalf("%d tokens minted", len(toks))
	}
}

func TestLinkValidationAndLimits(t *testing.T) {
	ctx := context.Background()
	s, a, _ := linkStore(t)
	now := time.Now()
	for _, id := range []string{"", strings.Repeat("x", MaxMachineIDLen+1), "bad\nid"} {
		if _, err := s.StartLink(ctx, id, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("StartLink(%q) = %v, want ErrInvalid", id, err)
		}
	}
	ls, _ := s.StartLink(ctx, "m1", now)
	for _, name := range []string{"", "   ", strings.Repeat("n", MaxTokenNameLen+1), "bad\x07name"} {
		if _, err := s.ApproveLink(ctx, a.ID, ls.UserCode, name, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("name %q = %v, want ErrInvalid", name, err)
		}
	}
	// A rejected name does not burn the code.
	if _, err := s.ApproveLink(ctx, a.ID, ls.UserCode, "ok", now); err != nil {
		t.Fatalf("approve after bad names: %v", err)
	}

	// Unauthenticated starts are capped.
	s2, _, _ := linkStore(t)
	for i := 0; i < MaxPendingLinks; i++ {
		if _, err := s2.db.Exec(`INSERT INTO link_codes(device_hash, user_hash, machine_id, created_at, expires_at) VALUES(?,?,?,?,?)`,
			string(rune('a'))+strings.Repeat("0", 5)+strconv.Itoa(i), "u"+strconv.Itoa(i), "m", at(now), at(now.Add(time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s2.StartLink(ctx, "m", now); !errors.Is(err, ErrLinkBusy) {
		t.Fatalf("start over the cap = %v", err)
	}
	// Old rows are swept, so the cap frees up.
	if _, err := s2.StartLink(ctx, "m", now.Add(2*time.Hour)); err != nil {
		t.Fatalf("start after expiry: %v", err)
	}
}

func TestUserCodeAlphabet(t *testing.T) {
	seen := map[byte]bool{}
	for i := 0; i < 400; i++ {
		c, err := newUserCode()
		if err != nil || len(c) != 8 {
			t.Fatalf("code %q, %v", c, err)
		}
		for j := 0; j < len(c); j++ {
			if !strings.ContainsRune(userCodeAlphabet, rune(c[j])) {
				t.Fatalf("code %q has %q outside the alphabet", c, c[j])
			}
			seen[c[j]] = true
		}
	}
	if len(seen) < len(userCodeAlphabet)-3 {
		t.Fatalf("only %d distinct characters in 3200 draws", len(seen))
	}
	for _, bad := range "01OILAEUY" {
		if strings.ContainsRune(userCodeAlphabet, bad) {
			t.Fatalf("alphabet contains ambiguous %q", bad)
		}
	}
}

func TestAccountTokenScoping(t *testing.T) {
	ctx := context.Background()
	s, a, b := linkStore(t)
	ta, _, err := s.CreateToken(ctx, a.ID, "a-token", ScopeRead, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetToken(ctx, b.ID, ta.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("B read A's token: %v", err)
	}
	if ok, err := s.RevokeAccountToken(ctx, b.ID, ta.ID); err != nil || ok {
		t.Fatalf("B revoked A's token: %v, %v", ok, err)
	}
	if toks, _ := s.ListAccountTokens(ctx, b.ID); len(toks) != 0 {
		t.Fatal("B lists A's tokens")
	}
	if ok, err := s.RevokeAccountToken(ctx, a.ID, ta.ID); err != nil || !ok {
		t.Fatalf("A revoke = %v, %v", ok, err)
	}
	if ok, _ := s.RevokeAccountToken(ctx, a.ID, ta.ID); ok {
		t.Fatal("revoked twice")
	}
	if _, _, err := s.CreateToken(ctx, a.ID, strings.Repeat("n", MaxTokenNameLen+1), ScopeRead, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long name = %v", err)
	}
}

func TestTouchTokenIsThrottled(t *testing.T) {
	ctx := context.Background()
	s, a, _ := linkStore(t)
	tok, _, _ := s.CreateToken(ctx, a.ID, "t", ScopeRead, "")
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	last := func() *time.Time {
		got, err := s.GetToken(ctx, a.ID, tok.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.LastUsedAt
	}
	if last() != nil {
		t.Fatal("new token has a last-used time")
	}
	s.TouchToken(ctx, tok.ID, t0)
	s.TouchToken(ctx, tok.ID, t0.Add(30*time.Second)) // inside the interval: ignored
	if got := last(); got == nil || !got.Equal(t0) {
		t.Fatalf("last used = %v, want %v", got, t0)
	}
	s.TouchToken(ctx, tok.ID, t0.Add(2*time.Minute))
	if got := last(); got == nil || !got.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("last used = %v after a later touch", got)
	}
}
