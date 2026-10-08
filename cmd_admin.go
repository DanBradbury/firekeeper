package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
)

// errNoTerminal means a password prompt has no terminal to read from.
var errNoTerminal = errors.New("standard input is not a terminal; run this in an interactive shell")

// promptPassword shows prompt on stderr and reads a password from the
// terminal without echoing it. It is replaced in tests.
var promptPassword = func(prompt string, stderr io.Writer) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errNoTerminal
	}
	fmt.Fprint(stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(stderr)
	return string(b), err
}

// runAdmin implements `firekeeper serve admin`. T5.4 adds the rest of the
// admin commands.
func runAdmin(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "create-account" {
		fmt.Fprintln(stderr, "usage: firekeeper serve admin create-account --email EMAIL [--db PATH]")
		return 2
	}
	return runAdminCreateAccount(args[1:], stdout, stderr)
}

// runAdminCreateAccount creates an account straight in the database,
// whatever --signup says. The password is only ever read from the terminal,
// twice, never from a flag or argument. The first account switches the
// server to multi-user mode.
func runAdminCreateAccount(args []string, stdout, stderr io.Writer) int {
	const name = "firekeeper serve admin create-account"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", "", "dashboard database path (default ~/.firekeeper/dashboard.db)")
	email := fs.String("email", "", "account email (required)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		// Never echo it: a stray argument may be a password typed in the
		// wrong place.
		fmt.Fprintf(stderr, "%s: unexpected argument; the password is only read from the terminal prompt\n", name)
		return 2
	}
	addr, err := store.NormalizeEmail(*email)
	if err != nil {
		fmt.Fprintf(stderr, "usage: %s --email EMAIL [--db PATH]\n", name)
		return 2
	}

	pw, err := promptPassword("Password: ", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	if n := len([]rune(pw)); n < store.MinPasswordLen || len(pw) > store.MaxPasswordLen {
		fmt.Fprintf(stderr, "%s: password must be %d to %d characters\n", name, store.MinPasswordLen, store.MaxPasswordLen)
		return 2
	}
	again, err := promptPassword("Repeat password: ", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	if again != pw {
		fmt.Fprintf(stderr, "%s: passwords do not match\n", name)
		return 2
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}

	ctx := context.Background()
	s, ok := openDashboardStore(ctx, name, *db, stderr)
	if !ok {
		return 1
	}
	defer s.Close()
	first, err := s.HasAccounts(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	a, err := s.CreateAccount(ctx, addr, hash, "", time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		if errors.Is(err, store.ErrEmailTaken) || errors.Is(err, store.ErrInvalid) {
			return 2
		}
		return 1
	}
	fmt.Fprintf(stdout, "created account %s (%s)\n", a.ID, a.Email)
	if !first {
		fmt.Fprintln(stdout, "this is the first account: the dashboard now runs in multi-user mode and every page and API call needs a sign-in or a token")
	}
	return 0
}
