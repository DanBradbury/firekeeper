package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
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
