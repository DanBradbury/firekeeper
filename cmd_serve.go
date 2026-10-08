package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/DanBradbury/firekeeper/internal/server"
	"github.com/DanBradbury/firekeeper/internal/server/auth"
)

// serveRun is replaced in tests so serve never binds a real port.
var serveRun = server.Run

func runServe(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "token" {
		return runToken(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "account" {
		return runAccount(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "invite" {
		return runInvite(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("firekeeper serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := server.Config{Out: stdout, Err: stderr}
	fs.StringVar(&cfg.Listen, "listen", server.DefaultListen, "address to listen on")
	fs.StringVar(&cfg.DB, "db", "", "dashboard database path (default ~/.firekeeper/dashboard.db)")
	fs.BoolVar(&cfg.Insecure, "insecure", false, "allow a non-loopback --listen address with no tokens or accounts; the API is then unauthenticated")
	signup := fs.String("signup", string(auth.SignupClosed), "who may create accounts: closed, invite, or open")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "firekeeper serve: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	mode, err := auth.ParseSignupMode(*signup)
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper serve: %v\n", err)
		return 2
	}
	cfg.Signup = mode

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serveRun(ctx, cfg); err != nil {
		fmt.Fprintf(stderr, "firekeeper serve: %v\n", err)
		if errors.Is(err, server.ErrNotLoopback) {
			return 2
		}
		return 1
	}
	return 0
}
