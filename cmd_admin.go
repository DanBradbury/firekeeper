package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
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

// adminUsage lists the admin commands for the usage line.
const adminUsage = "usage: firekeeper serve admin create-account|create-invite|list-accounts|disable-account|reset-password|delete-account [flags]"

// runAdmin implements `firekeeper serve admin`. Every command works on the
// database directly, so the operator needs file access to it and no network
// or credentials. There is no email in the test bed, so resetting a
// password is an operator action.
func runAdmin(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, adminUsage)
		return 2
	}
	switch args[0] {
	case "create-account":
		return runAdminCreateAccount(args[1:], stdout, stderr)
	case "create-invite":
		return runInvite(append([]string{"create"}, args[1:]...), stdout, stderr)
	case "list-accounts":
		return runAdminListAccounts(args[1:], stdout, stderr)
	case "disable-account":
		return runAdminAccountAction("disable-account", args[1:], stdout, stderr)
	case "reset-password":
		return runAdminAccountAction("reset-password", args[1:], stdout, stderr)
	case "delete-account":
		return runAdminAccountAction("delete-account", args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "firekeeper serve admin: unknown command %q\n%s\n", args[0], adminUsage)
	return 2
}

// runAdminListAccounts prints every account with what it stores. It never
// prints password hashes.
func runAdminListAccounts(args []string, stdout, stderr io.Writer) int {
	const name = "firekeeper serve admin list-accounts"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", "", "dashboard database path (default ~/.firekeeper/dashboard.db)")
	if err := fs.Parse(args); err != nil {
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
	accts, err := s.ListAccounts(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tEMAIL\tCREATED\tSTATUS\tSESSIONS\tEVENTS\tSTORED")
	for _, a := range accts {
		st, err := s.AccountStats(ctx, a.ID)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return 1
		}
		status, email := "active", a.Email
		if a.Disabled {
			status = "disabled"
		}
		if a.ID == store.DefaultAccountID {
			email = "(default, single-user)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d B\n", a.ID, email, a.CreatedAt.Format(time.RFC3339), status, st.Sessions, st.Events, st.StoredBytes)
	}
	tw.Flush()
	return 0
}

// runAdminAccountAction runs the admin commands that act on one account
// named by --email: disable-account, reset-password and delete-account.
func runAdminAccountAction(action string, args []string, stdout, stderr io.Writer) int {
	name := "firekeeper serve admin " + action
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", "", "dashboard database path (default ~/.firekeeper/dashboard.db)")
	email := fs.String("email", "", "account email (required)")
	var yes bool
	if action == "delete-account" {
		fs.BoolVar(&yes, "yes", false, "really delete: without it, only show what would be removed")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		// Never echo it: a stray argument may be a password typed in the
		// wrong place.
		fmt.Fprintf(stderr, "%s: unexpected argument; passwords are only read from the terminal prompt\n", name)
		return 2
	}
	addr, err := store.NormalizeEmail(*email)
	if err != nil {
		usage := "usage: " + name + " --email EMAIL [--db PATH]"
		if action == "delete-account" {
			usage += " [--yes]"
		}
		fmt.Fprintln(stderr, usage)
		return 2
	}

	// Read and hash the new password before touching the database, so a bad
	// prompt changes nothing.
	var hash string
	if action == "reset-password" {
		pw, err := promptPassword("New password: ", stderr)
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
	a, err := s.AccountByEmail(ctx, addr)
	if errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(stderr, "%s: no account with email %q\n", name, addr)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}

	switch action {
	case "disable-account":
		err = s.SetAccountDisabled(ctx, a.ID, true)
		if err == nil {
			fmt.Fprintf(stdout, "disabled %s: it cannot sign in and its tokens stop working; its data is kept. Undo with: firekeeper serve account enable %s\n", a.Email, a.Email)
		}
	case "reset-password":
		err = s.SetPassword(ctx, a.ID, hash)
		if err == nil {
			fmt.Fprintf(stdout, "reset the password for %s and ended its browser sessions; its tokens still work\n", a.Email)
		}
	case "delete-account":
		var st store.AccountStats
		if !yes {
			if st, err = s.AccountStats(ctx, a.ID); err != nil {
				break
			}
			fmt.Fprintf(stderr, "%s would permanently delete %s with %d machines, %d sessions, %d events and %d active tokens.\nRun it again with --yes to do it. This cannot be undone.\n",
				name, a.Email, st.Machines, st.Sessions, st.Events, st.Tokens)
			return 2
		}
		if st, err = s.DeleteAccount(ctx, a.ID); err == nil {
			fmt.Fprintf(stdout, "deleted %s: %d machines, %d sessions, %d events, %d active tokens\n", a.Email, st.Machines, st.Sessions, st.Events, st.Tokens)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	return 0
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
