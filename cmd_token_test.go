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
