package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// stubPrompt answers each password prompt in turn and records the prompts.
func stubPrompt(t *testing.T, answers ...string) *[]string {
	t.Helper()
	var prompts []string
	previous := promptPassword
	promptPassword = func(prompt string, _ io.Writer) (string, error) {
		prompts = append(prompts, prompt)
		if len(answers) == 0 {
			return "", errNoTerminal
		}
		a := answers[0]
		answers = answers[1:]
		return a, nil
	}
	t.Cleanup(func() { promptPassword = previous })
	return &prompts
}

func TestAdminCreateAccount(t *testing.T) {
	const pw = "correct horse battery"
	db := filepath.Join(t.TempDir(), "d", "dashboard.db")
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := runServe(append([]string{"admin", "create-account", "--db", db}, args...), &out, &errb)
		return code, out.String(), errb.String()
	}

	tests := []struct {
		name    string
		answers []string
		args    []string
		code    int
		stderr  string
	}{
		{"no email", nil, nil, 2, "usage"},
		{"password as argument is refused", nil, []string{"--email", "a@example.com", pw}, 2, "unexpected argument"},
		{"no terminal", nil, []string{"--email", "a@example.com"}, 1, "not a terminal"},
		{"too short", []string{"short"}, []string{"--email", "a@example.com"}, 2, "password must be"},
		{"mismatch", []string{pw, pw + "x"}, []string{"--email", "a@example.com"}, 2, "do not match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubPrompt(t, tt.answers...)
			code, _, stderr := run(tt.args...)
			if code != tt.code || !strings.Contains(stderr, tt.stderr) {
				t.Fatalf("code %d stderr %q", code, stderr)
			}
			if strings.Contains(stderr, pw) {
				t.Fatal("stderr echoes the password")
			}
		})
	}

	prompts := stubPrompt(t, pw, pw)
	code, out, stderr := run("--email", "A@Example.com")
	if code != 0 {
		t.Fatalf("create: code %d stderr %q", code, stderr)
	}
	if len(*prompts) != 2 || !strings.Contains(out, "a@example.com") || !strings.Contains(out, "multi-user mode") {
		t.Fatalf("prompts %q out %q", *prompts, out)
	}
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.AccountByEmail(context.Background(), "a@example.com")
	s.Close()
	if err != nil || !auth.VerifyPassword(a.PasswordHash, pw) || !strings.HasPrefix(a.PasswordHash, "$argon2id$") {
		t.Fatalf("stored account %+v, %v", a, err)
	}

	// A second account is not the first, and a taken email is refused.
	stubPrompt(t, pw, pw)
	if code, out, _ := run("--email", "b@example.com"); code != 0 || strings.Contains(out, "multi-user mode") {
		t.Fatalf("second account: code %d out %q", code, out)
	}
	stubPrompt(t, pw, pw)
	if code, _, stderr := run("--email", "a@example.com"); code != 2 || !strings.Contains(stderr, "already") {
		t.Fatalf("duplicate: code %d stderr %q", code, stderr)
	}
}

func TestPromptPasswordNeedsTerminal(t *testing.T) {
	// go test's stdin is not a terminal.
	if _, err := promptPassword("Password: ", io.Discard); !errors.Is(err, errNoTerminal) {
		t.Fatalf("err = %v", err)
	}
}

// adminFixture is a dashboard database with two accounts that each hold
// data, a token and a browser session.
type adminFixture struct {
	db           string
	a, b         store.Account
	cookieA      string
	cookieB      string
	oldHashA     string
	inviteIssuer string
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	ctx := context.Background()
	f := &adminFixture{db: filepath.Join(t.TempDir(), "d", "dashboard.db")}
	if err := os.MkdirAll(filepath.Dir(f.db), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, f.db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f.oldHashA, _ = auth.HashPassword("old password one")
	hashB, _ := auth.HashPassword("old password two")
	now := time.Now()
	if f.a, err = s.CreateAccount(ctx, "a@example.com", f.oldHashA, "", now); err != nil {
		t.Fatal(err)
	}
	if f.b, err = s.CreateAccount(ctx, "b@example.com", hashB, "", now); err != nil {
		t.Fatal(err)
	}
	for _, a := range []store.Account{f.a, f.b} {
		var evs []transcript.Event
		for i := 0; i < 3; i++ {
			evs = append(evs, transcript.Event{Provider: transcript.ProviderCodex, Seq: int64(i), Role: transcript.RoleUser, Text: "hello", Raw: json.RawMessage(`{}`)})
		}
		batch := store.SessionBatch{Meta: store.SessionMeta{SessionID: "s1", Provider: "codex"}, Events: evs}
		if _, _, err := s.Ingest(ctx, a.ID, store.Machine{ID: "m1"}, []store.SessionBatch{batch}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.CreateToken(ctx, a.ID, "laptop", store.ScopeIngest, "m1"); err != nil {
			t.Fatal(err)
		}
	}
	f.cookieA, _, _ = s.CreateWebSession(ctx, f.a.ID, time.Hour, now)
	f.cookieB, _, _ = s.CreateWebSession(ctx, f.b.ID, time.Hour, now)
	return f
}

func (f *adminFixture) run(args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = runServe(append([]string{"admin", args[0], "--db", f.db}, args[1:]...), &out, &errb)
	return code, out.String(), errb.String()
}

func (f *adminFixture) store(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), f.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAdminUsageAndUnknownCommand(t *testing.T) {
	for _, args := range [][]string{{"admin"}, {"admin", "frobnicate"}} {
		var out, errb bytes.Buffer
		if code := runServe(args, &out, &errb); code != 2 || !strings.Contains(errb.String(), "create-invite") || !strings.Contains(errb.String(), "delete-account") {
			t.Fatalf("%v: code %d stderr %q", args, code, errb.String())
		}
	}
}

func TestAdminListAccounts(t *testing.T) {
	f := newAdminFixture(t)
	s := f.store(t)
	if err := s.SetAccountDisabled(context.Background(), f.b.ID, true); err != nil {
		t.Fatal(err)
	}
	s.Close()
	code, out, stderr := f.run("list-accounts")
	if code != 0 {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	for _, want := range []string{"ID", "STATUS", "STORED", f.a.ID, "a@example.com", "b@example.com", "disabled", "active", "(default, single-user)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "a@example.com") {
			continue
		}
		// ... SESSIONS EVENTS <bytes> B: one session, three events.
		if f := strings.Fields(line); len(f) < 8 || f[len(f)-4] != "1" || f[len(f)-3] != "3" {
			t.Errorf("counts wrong in %q", line)
		}
	}
	if strings.Contains(out, "argon2") || strings.Contains(out, f.oldHashA) {
		t.Fatal("list-accounts prints a password hash")
	}
	if code, _, stderr := f.run("list-accounts", "extra"); code != 2 || !strings.Contains(stderr, "unexpected argument") {
		t.Fatalf("stray argument: code %d stderr %q", code, stderr)
	}
}

func TestAdminCreateInvite(t *testing.T) {
	f := newAdminFixture(t)
	code, out, stderr := f.run("create-invite", "--ttl", "1h")
	if code != 0 || !strings.HasPrefix(out, store.InvitePrefix) || !strings.Contains(stderr, "valid until") {
		t.Fatalf("code %d out %q stderr %q", code, out, stderr)
	}
	invite := strings.TrimSpace(out)
	s := f.store(t)
	if _, err := s.CreateAccount(context.Background(), "new@example.com", "hash", invite, time.Now()); err != nil {
		t.Fatalf("the printed invite does not work: %v", err)
	}
	if _, err := s.CreateAccount(context.Background(), "again@example.com", "hash", invite, time.Now()); !errors.Is(err, store.ErrInviteInvalid) {
		t.Fatalf("invite worked twice: %v", err)
	}
	s.Close()

	// On behalf of an account; unknown accounts and bad lifetimes fail.
	if code, out, _ := f.run("create-invite", "--account", "a@example.com"); code != 0 || !strings.HasPrefix(out, store.InvitePrefix) {
		t.Fatalf("--account: code %d out %q", code, out)
	}
	if code, _, stderr := f.run("create-invite", "--account", "nobody@example.com"); code != 1 || !strings.Contains(stderr, "no account") {
		t.Fatalf("unknown account: code %d stderr %q", code, stderr)
	}
	if code, _, _ := f.run("create-invite", "--ttl", "0s"); code != 2 {
		t.Fatalf("zero ttl: code %d", code)
	}
}

func TestAdminDisableAccount(t *testing.T) {
	f := newAdminFixture(t)
	if code, _, _ := f.run("disable-account"); code != 2 {
		t.Fatalf("no email: code %d", code)
	}
	if code, _, stderr := f.run("disable-account", "--email", "nobody@example.com"); code != 1 || !strings.Contains(stderr, "no account") {
		t.Fatalf("unknown: code %d stderr %q", code, stderr)
	}
	code, out, stderr := f.run("disable-account", "--email", " A@Example.com ")
	if code != 0 || !strings.Contains(out, "disabled a@example.com") {
		t.Fatalf("code %d out %q stderr %q", code, out, stderr)
	}
	s := f.store(t)
	ctx := context.Background()
	if a, _ := s.GetAccount(ctx, f.a.ID); !a.Disabled {
		t.Fatal("account not disabled")
	}
	if b, _ := s.GetAccount(ctx, f.b.ID); b.Disabled {
		t.Fatal("another account was disabled")
	}
	if _, err := s.LookupWebSession(ctx, f.cookieA, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("disabled account's session survives: %v", err)
	}
	if _, err := s.LookupWebSession(ctx, f.cookieB, time.Now()); err != nil {
		t.Fatalf("other account's session ended: %v", err)
	}
	// Its data is kept, and `account enable` brings it back.
	if st, _ := s.AccountStats(ctx, f.a.ID); st.Events != 3 {
		t.Fatalf("disable removed data: %+v", st)
	}
	s.Close()
	if code, _, _ := f.run("disable-account", "--email", "a@example.com"); code != 0 {
		t.Fatal("disabling twice failed")
	}
}

func TestAdminResetPassword(t *testing.T) {
	const newPW = "a brand new password"
	f := newAdminFixture(t)
	check := func(name string, answers []string, args []string, wantCode int, wantErr string) {
		t.Helper()
		stubPrompt(t, answers...)
		code, out, stderr := f.run(append([]string{"reset-password"}, args...)...)
		if code != wantCode || !strings.Contains(stderr, wantErr) {
			t.Fatalf("%s: code %d stdout %q stderr %q", name, code, out, stderr)
		}
		if strings.Contains(out+stderr, newPW) {
			t.Fatalf("%s: output echoes the password", name)
		}
	}
	check("no email", nil, nil, 2, "usage")
	check("password as argument", nil, []string{"--email", "a@example.com", newPW}, 2, "unexpected argument")
	check("no terminal", nil, []string{"--email", "a@example.com"}, 1, "not a terminal")
	check("too short", []string{"short"}, []string{"--email", "a@example.com"}, 2, "password must be")
	check("mismatch", []string{newPW, newPW + "x"}, []string{"--email", "a@example.com"}, 2, "do not match")
	check("unknown account", []string{newPW, newPW}, []string{"--email", "nobody@example.com"}, 1, "no account")

	s := f.store(t)
	ctx := context.Background()
	if a, _ := s.GetAccount(ctx, f.a.ID); a.PasswordHash != f.oldHashA {
		t.Fatal("a refused reset changed the password")
	}
	s.Close()

	prompts := stubPrompt(t, newPW, newPW)
	code, out, stderr := f.run("reset-password", "--email", "a@example.com")
	if code != 0 || len(*prompts) != 2 || !strings.Contains(out, "a@example.com") || strings.Contains(out+stderr, newPW) {
		t.Fatalf("reset: code %d out %q stderr %q prompts %q", code, out, stderr, *prompts)
	}
	s = f.store(t)
	a, _ := s.GetAccount(ctx, f.a.ID)
	if !auth.VerifyPassword(a.PasswordHash, newPW) || auth.VerifyPassword(a.PasswordHash, "old password one") {
		t.Fatal("password not replaced")
	}
	if _, err := s.LookupWebSession(ctx, f.cookieA, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old browser session survives a reset: %v", err)
	}
	b, _ := s.GetAccount(ctx, f.b.ID)
	if !auth.VerifyPassword(b.PasswordHash, "old password two") {
		t.Fatal("another account's password changed")
	}
	if _, err := s.LookupWebSession(ctx, f.cookieB, time.Now()); err != nil {
		t.Fatalf("another account's session ended: %v", err)
	}
	if st, _ := s.AccountStats(ctx, f.a.ID); st.Events != 3 || st.Tokens != 1 {
		t.Fatalf("reset touched data or tokens: %+v", st)
	}
}

func TestAdminDeleteAccount(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	if code, _, _ := f.run("delete-account"); code != 2 {
		t.Fatalf("no email: code %d", code)
	}
	if code, _, stderr := f.run("delete-account", "--email", "nobody@example.com", "--yes"); code != 1 || !strings.Contains(stderr, "no account") {
		t.Fatalf("unknown: code %d stderr %q", code, stderr)
	}

	// Without --yes it only says what it would remove.
	code, out, stderr := f.run("delete-account", "--email", "a@example.com")
	if code != 2 || out != "" || !strings.Contains(stderr, "--yes") || !strings.Contains(stderr, "3 events") || !strings.Contains(stderr, "1 sessions") {
		t.Fatalf("dry run: code %d stdout %q stderr %q", code, out, stderr)
	}
	s := f.store(t)
	if st, err := s.AccountStats(ctx, f.a.ID); err != nil || st.Events != 3 {
		t.Fatalf("a dry run deleted data: %+v %v", st, err)
	}
	s.Close()

	code, out, stderr = f.run("delete-account", "--email", "a@example.com", "--yes")
	if code != 0 || !strings.Contains(out, "deleted a@example.com") || !strings.Contains(out, "3 events") {
		t.Fatalf("delete: code %d stdout %q stderr %q", code, out, stderr)
	}
	s = f.store(t)
	if _, err := s.GetAccount(ctx, f.a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("account remains: %v", err)
	}
	if hits, _ := s.SearchText(ctx, f.a.ID, "hello", 10); len(hits) != 0 {
		t.Fatalf("deleted account still searchable: %v", hits)
	}
	if st, err := s.AccountStats(ctx, f.b.ID); err != nil || st.Events != 3 || st.Tokens != 1 {
		t.Fatalf("another account changed: %+v %v", st, err)
	}
	if _, err := s.LookupWebSession(ctx, f.cookieB, time.Now()); err != nil {
		t.Fatalf("another account's session ended: %v", err)
	}
	s.Close()
	if _, list, _ := f.run("list-accounts"); strings.Contains(list, "a@example.com") {
		t.Fatalf("deleted account still listed:\n%s", list)
	}
	if code, _, stderr := f.run("delete-account", "--email", "a@example.com", "--yes"); code != 1 || !strings.Contains(stderr, "no account") {
		t.Fatalf("deleting twice: code %d stderr %q", code, stderr)
	}
}
