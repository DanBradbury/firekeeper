package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server"
	"github.com/DanBradbury/firekeeper/internal/server/store"
)

func runToken(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: firekeeper serve token create|list|revoke [flags]")
		return 2
	}
	fs := flag.NewFlagSet("firekeeper serve token "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", "", "dashboard database path (default ~/.firekeeper/dashboard.db)")
	var name, scope, machine string
	switch args[0] {
	case "create":
		fs.StringVar(&name, "name", "", "token name (required)")
		fs.StringVar(&scope, "scope", store.ScopeRead, "token scope: ingest or read")
		fs.StringVar(&machine, "machine", "", "machine id an ingest token is bound to (required for ingest)")
	case "list", "revoke":
	default:
		fmt.Fprintf(stderr, "firekeeper serve token: unknown command %q\n", args[0])
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if args[0] == "revoke" {
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: firekeeper serve token revoke ID|NAME")
			return 2
		}
	} else if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "firekeeper serve token: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	path := *db
	if path == "" {
		var err error
		if path, err = server.DefaultDBPath(); err != nil {
			fmt.Fprintf(stderr, "firekeeper serve token: %v\n", err)
			return 1
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintf(stderr, "firekeeper serve token: %v\n", err)
		return 1
	}
	ctx := context.Background()
	s, err := store.Open(ctx, path)
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper serve token: open database: %v\n", err)
		return 1
	}
	defer s.Close()

	switch args[0] {
	case "create":
		t, secret, err := s.CreateToken(ctx, name, scope, machine)
		if err != nil {
			fmt.Fprintf(stderr, "firekeeper serve token: %v\n", err)
			if errors.Is(err, store.ErrInvalid) {
				return 2
			}
			return 1
		}
		fmt.Fprintf(stdout, "%s\n", secret)
		fmt.Fprintf(stderr, "created %s token %s (%s). Store it now; it is not shown again.\n", t.Scope, t.ID, t.Name)
	case "list":
		toks, err := s.ListTokens(ctx, false)
		if err != nil {
			fmt.Fprintf(stderr, "firekeeper serve token: %v\n", err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tSCOPE\tMACHINE\tCREATED\tSTATUS")
		for _, t := range toks {
			status := "active"
			if t.RevokedAt != nil {
				status = "revoked"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Name, t.Scope, t.MachineID, t.CreatedAt.Format(time.RFC3339), status)
		}
		tw.Flush()
	case "revoke":
		n, err := s.RevokeToken(ctx, fs.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "firekeeper serve token: %v\n", err)
			return 1
		}
		if n == 0 {
			fmt.Fprintf(stderr, "firekeeper serve token: no active token %q\n", fs.Arg(0))
			return 1
		}
		fmt.Fprintf(stdout, "revoked %d token(s)\n", n)
	}
	return 0
}
