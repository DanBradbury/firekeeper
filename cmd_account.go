package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server"
	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
)

// stdin is replaced in tests.
var stdin io.Reader = os.Stdin

// openDashboardStore opens the dashboard database at db, or the default path
// when db is empty, creating its directory.
func openDashboardStore(ctx context.Context, name, db string, stderr io.Writer) (*store.Store, bool) {
	path := db
	if path == "" {
		var err error
		if path, err = server.DefaultDBPath(); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return nil, false
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return nil, false
	}
	s, err := store.Open(ctx, path)
	if err != nil {
		fmt.Fprintf(stderr, "%s: open database: %v\n", name, err)
		return nil, false
	}
	return s, true
}

// resolveAccount maps a --account email to an account id. Empty means the
// default (single-user) account.
func resolveAccount(ctx context.Context, s *store.Store, email string) (string, error) {
	if email == "" {
		return store.DefaultAccountID, nil
	}
	a, err := s.AccountByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("no account with email %q", email)
	}
	if err != nil {
		return "", err
	}
	return a.ID, nil
}

// runAccount implements `firekeeper serve account create|list|disable|enable`.
// There is no signup page for the first account, so this is how a server
// gets its owner.
func runAccount(args []string, stdout, stderr io.Writer) int {
	const name = "firekeeper serve account"
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: firekeeper serve account create|list|disable|enable [flags]")
		return 2
	}
	fs := flag.NewFlagSet(name+" "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", "", "dashboard database path (default ~/.firekeeper/dashboard.db)")
	var email string
	var passwordStdin bool
	switch args[0] {
	case "create":
		fs.StringVar(&email, "email", "", "account email (required)")
		fs.BoolVar(&passwordStdin, "password-stdin", false, "read the password from the first line of standard input (required)")
	case "list", "disable", "enable":
	default:
		fmt.Fprintf(stderr, "%s: unknown command %q\n", name, args[0])
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	switch args[0] {
	case "disable", "enable":
		if fs.NArg() != 1 {
			fmt.Fprintf(stderr, "usage: %s %s EMAIL\n", name, args[0])
			return 2
		}
	default:
		if fs.NArg() > 0 {
			fmt.Fprintf(stderr, "%s: unexpected argument %q\n", name, fs.Arg(0))
			return 2
		}
	}

	var hash string
	if args[0] == "create" {
		if email == "" || !passwordStdin {
			fmt.Fprintf(stderr, "usage: %s create --email EMAIL --password-stdin\n", name)
			return 2
		}
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			fmt.Fprintf(stderr, "%s: read password: %v\n", name, err)
			return 1
		}
		pw := strings.TrimRight(line, "\r\n")
		if n := len([]rune(pw)); n < store.MinPasswordLen || len(pw) > store.MaxPasswordLen {
			fmt.Fprintf(stderr, "%s: password must be %d to %d characters\n", name, store.MinPasswordLen, store.MaxPasswordLen)
			return 2
		}
		if hash, err = auth.HashPassword(pw); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return 1
		}
	}

	ctx := context.Background()
	s, ok := openDashboardStore(ctx, name, *db, stderr)
	if !ok {
		return 1
	}
	defer s.Close()

	switch args[0] {
	case "create":
		a, err := s.CreateAccount(ctx, email, hash, "", time.Now())
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			if errors.Is(err, store.ErrInvalid) {
				return 2
			}
			return 1
		}
		fmt.Fprintf(stdout, "created account %s (%s)\n", a.ID, a.Email)
	case "list":
		accts, err := s.ListAccounts(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tEMAIL\tCREATED\tSTATUS")
		for _, a := range accts {
			status, em := "active", a.Email
			if a.Disabled {
				status = "disabled"
			}
			if a.ID == store.DefaultAccountID {
				em = "(default, single-user)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.ID, em, a.CreatedAt.Format(time.RFC3339), status)
		}
		tw.Flush()
	case "disable", "enable":
		a, err := s.AccountByEmail(ctx, fs.Arg(0))
		if err == nil {
			err = s.SetAccountDisabled(ctx, a.ID, args[0] == "disable")
		}
		if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(stderr, "%s: no account with email %q\n", name, fs.Arg(0))
			return 1
		}
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return 1
		}
		fmt.Fprintf(stdout, "%sd %s\n", args[0], a.Email)
	}
	return 0
}

// runInvite implements `firekeeper serve invite create`.
func runInvite(args []string, stdout, stderr io.Writer) int {
	const name = "firekeeper serve invite"
	if len(args) == 0 || args[0] != "create" {
		fmt.Fprintln(stderr, "usage: firekeeper serve invite create [--ttl 168h] [--account EMAIL]")
		return 2
	}
	fs := flag.NewFlagSet(name+" create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", "", "dashboard database path (default ~/.firekeeper/dashboard.db)")
	ttl := fs.Duration("ttl", 7*24*time.Hour, "how long the invite code stays valid")
	acct := fs.String("account", "", "email of the inviting account (default: the single-user account)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: unexpected argument %q\n", name, fs.Arg(0))
		return 2
	}
	ctx := context.Background()
	s, ok := openDashboardStore(ctx, name, *db, stderr)
	if !ok {
		return 1
	}
	defer s.Close()
	id, err := resolveAccount(ctx, s, *acct)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	code, err := s.CreateInvite(ctx, id, *ttl, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		if errors.Is(err, store.ErrInvalid) {
			return 2
		}
		return 1
	}
	fmt.Fprintln(stdout, code)
	fmt.Fprintf(stderr, "invite valid until %s. It works once; it is not shown again.\n", time.Now().Add(*ttl).Format(time.RFC3339))
	return 0
}
