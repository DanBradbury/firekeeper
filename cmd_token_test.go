package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestTokenCommands(t *testing.T) {
	db := filepath.Join(t.TempDir(), "d.db")
	run := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := runServe(tokenArgs(args, db), &o, &e)
		return code, o.String(), e.String()
	}
	if code, _, _ := run("create", "--name", "x", "--scope", "ingest"); code != 2 {
		t.Fatalf("ingest without machine: %d", code)
	}
	code, out, _ := run("create", "--name", "laptop", "--scope", "ingest", "--machine", "m1")
	secret := strings.TrimSpace(out)
	if code != 0 || !strings.HasPrefix(secret, "fk_") {
		t.Fatalf("create: %d %q", code, out)
	}
	_, list, _ := run("list")
	if !strings.Contains(list, "laptop") || strings.Contains(list, secret) || !strings.Contains(list, "active") {
		t.Fatalf("list = %q", list)
	}
	if code, _, _ := run("revoke", "laptop"); code != 0 {
		t.Fatalf("revoke: %d", code)
	}
	if _, list, _ = run("list"); !strings.Contains(list, "revoked") {
		t.Fatalf("list = %q", list)
	}
	if code, _, _ := run("revoke", "laptop"); code != 1 {
		t.Fatalf("second revoke: %d", code)
	}
}

func tokenArgs(args []string, db string) []string {
	out := []string{"token", args[0], "--db", db}
	return append(out, args[1:]...)
}

func TestAccountInviteAndTokenOwnership(t *testing.T) {
	db := filepath.Join(t.TempDir(), "d.db")
	run := func(in string, args ...string) (int, string, string) {
		old := stdin
		stdin = strings.NewReader(in)
		defer func() { stdin = old }()
		var o, e bytes.Buffer
		code := runServe(args, &o, &e)
		return code, o.String(), e.String()
	}
	const pwd = "correct horse battery"
	if code, _, _ := run(pwd, "account", "create", "--db", db, "--email", "a@example.com"); code != 2 {
		t.Fatalf("create without --password-stdin: %d", code)
	}
	if code, _, e := run("short\n", "account", "create", "--db", db, "--email", "a@example.com", "--password-stdin"); code != 2 {
		t.Fatalf("short password: %d %q", code, e)
	}
	code, out, e := run(pwd+"\n", "account", "create", "--db", db, "--email", "a@example.com", "--password-stdin")
	if code != 0 || !strings.Contains(out, "a@example.com") || strings.Contains(out+e, pwd) {
		t.Fatalf("create: %d %q %q", code, out, e)
	}
	if code, _, _ := run(pwd+"\n", "account", "create", "--db", db, "--email", "A@example.com", "--password-stdin"); code != 1 {
		t.Fatalf("duplicate: %d", code)
	}
	if _, out, _ := run("", "account", "list", "--db", db); !strings.Contains(out, "a@example.com") || strings.Contains(out, pwd) {
		t.Fatalf("list = %q", out)
	}

	code, out, _ = run("", "invite", "create", "--db", db, "--account", "a@example.com", "--ttl", "1h")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(out), "fki_") {
		t.Fatalf("invite: %d %q", code, out)
	}
	if code, _, _ := run("", "invite", "create", "--db", db, "--account", "nobody@example.com"); code != 1 {
		t.Fatalf("invite for unknown account: %d", code)
	}

	if code, _, _ := run("", "token", "create", "--db", db, "--name", "t", "--account", "nobody@example.com"); code != 1 {
		t.Fatalf("token for unknown account: %d", code)
	}
	if code, out, _ := run("", "token", "create", "--db", db, "--name", "mine", "--account", "a@example.com"); code != 0 || !strings.HasPrefix(strings.TrimSpace(out), "fk_") {
		t.Fatalf("token create: %d %q", code, out)
	}
	if code, _, _ := run("", "token", "create", "--db", db, "--name", "legacy"); code != 0 {
		t.Fatal("token without --account must still work")
	}
	_, list, _ := run("", "token", "list", "--db", db)
	for _, line := range strings.Split(list, "\n") {
		switch {
		case strings.Contains(line, "mine") && !strings.Contains(line, "a@example.com"):
			t.Fatalf("token not owned by a@example.com: %q", line)
		case strings.Contains(line, "legacy") && !strings.Contains(line, "(default)"):
			t.Fatalf("token not owned by the default account: %q", line)
		}
	}

	if code, _, _ := run("", "account", "disable", "--db", db, "a@example.com"); code != 0 {
		t.Fatalf("disable: %d", code)
	}
	if _, out, _ := run("", "account", "list", "--db", db); !strings.Contains(out, "disabled") {
		t.Fatalf("list after disable = %q", out)
	}
	if code, _, _ := run("", "account", "enable", "--db", db, "nobody@example.com"); code != 1 {
		t.Fatalf("enable unknown: %d", code)
	}
}

func TestServeRejectsBadSignupMode(t *testing.T) {
	var o, e bytes.Buffer
	if code := runServe([]string{"--signup", "maybe"}, &o, &e); code != 2 || !strings.Contains(e.String(), "signup mode") {
		t.Fatalf("bad mode: %d %q", code, e.String())
	}
}
