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

	"github.com/DanBradbury/firekeeper/internal/daemon"
	"github.com/DanBradbury/firekeeper/internal/reporter"
)

// daemonRun is replaced in tests so daemon never takes the real lock.
var daemonRun = daemon.Run

func runDaemon(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("firekeeper daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := daemon.Config{Out: stderr}
	fs.StringVar(&cfg.Reporter.Server, "server", reporter.DefaultServer, "dashboard server URL")
	var providers providerList
	fs.Var(&providers, "provider", "upload this provider's sessions (repeatable); at least one is required")
	fs.DurationVar(&cfg.Interval, "interval", daemon.DefaultInterval, fmt.Sprintf("time between passes (at least %s)", daemon.MinInterval))
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "firekeeper daemon: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if cfg.Interval < daemon.MinInterval {
		fmt.Fprintf(stderr, "firekeeper daemon: --interval must be at least %s\n", daemon.MinInterval)
		return 2
	}
	if len(providers) == 0 {
		fmt.Fprintln(stderr, "firekeeper daemon: no --provider given; the daemon uploads nothing without one")
		return 2
	}
	cfg.Reporter.Providers = providers

	// The first signal stops gracefully after the batch in flight. Offsets
	// are saved after every batch, so a second signal can exit at once.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
		case <-ctx.Done():
			return
		}
		fmt.Fprintln(stderr, "firekeeper daemon: stopping after the batch in flight; interrupt again to quit now")
		cancel()
		<-signals
		os.Exit(130)
	}()

	if err := daemonRun(ctx, cfg); err != nil {
		fmt.Fprintf(stderr, "firekeeper daemon: %v\n", err)
		return 1
	}
	return 0
}
