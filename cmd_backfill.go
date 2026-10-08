package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/reporter"
)

// backfillRun, backfillInput, and backfillInteractive are replaced in tests
// so backfill never reads real transcripts or waits on a terminal.
var (
	backfillRun                   = reporter.Backfill
	backfillInput       io.Reader = os.Stdin
	backfillInteractive           = stdinIsTerminal
)

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func runBackfill(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("firekeeper backfill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := reporter.BackfillConfig{Config: reporter.Config{Out: stdout}, Progress: stderr}
	fs.StringVar(&cfg.Server, "server", reporter.DefaultServer, "dashboard server URL")
	token := tokenFlag(fs)
	var providers providerList
	fs.Var(&providers, "provider", "upload this provider's sessions (repeatable); with none, only the plan is shown")
	fs.DurationVar(&cfg.Since, "since", 0, "only import transcripts modified within this duration (for example 720h)")
	after := fs.String("after", "", "only import transcripts modified after this date (YYYY-MM-DD, local time)")
	fs.IntVar(&cfg.Limit, "limit", 0, "import at most this many sessions, newest first")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "print the plan without uploading")
	yes := fs.Bool("yes", false, "upload without asking for confirmation")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "firekeeper backfill: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if cfg.Since < 0 {
		fmt.Fprintln(stderr, "firekeeper backfill: --since must not be negative")
		return 2
	}
	if cfg.Limit < 0 {
		fmt.Fprintln(stderr, "firekeeper backfill: --limit must not be negative")
		return 2
	}
	if *after != "" {
		if cfg.Since > 0 {
			fmt.Fprintln(stderr, "firekeeper backfill: use --since or --after, not both")
			return 2
		}
		t, err := time.ParseInLocation("2006-01-02", *after, time.Local)
		if err != nil {
			fmt.Fprintf(stderr, "firekeeper backfill: --after %q is not a YYYY-MM-DD date\n", *after)
			return 2
		}
		cfg.After = t
	}
	c, err := loadConfig(configFlags{fs: fs, server: &cfg.Server, token: token, providers: &providers})
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper backfill: %v\n", err)
		return 2
	}
	applyReporterConfig(&cfg.Config, c)
	if len(cfg.Providers) == 0 && !cfg.DryRun {
		fmt.Fprintln(stderr, "firekeeper backfill: no --provider given; uploading nothing. Showing the plan.")
	}
	if !*yes {
		cfg.Confirm = func(plan reporter.Plan) bool { return confirmBackfill(plan, cfg.Server, stderr) }
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	result, err := backfillRun(ctx, cfg)
	if result.Warning != nil {
		fmt.Fprintf(stderr, "firekeeper backfill warning: %v\n", result.Warning)
	}
	switch {
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(stderr, "firekeeper backfill: interrupted; run it again to resume")
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "firekeeper backfill: %v\n", err)
		if len(result.Sessions) > 0 {
			fmt.Fprintln(stderr, "firekeeper backfill: run it again to resume")
		}
		return 1
	case result.Declined:
		return 1
	}
	if !result.Plan.Uploading {
		return 0
	}
	events := 0
	for _, s := range result.Sessions {
		events += s.Events
	}
	fmt.Fprintf(stdout, "backfill: uploaded %d events from %d %s\n", events, len(result.Sessions), plural(len(result.Sessions), "session", "sessions"))
	if result.Failed() {
		return 1
	}
	return 0
}

// confirmBackfill asks on stderr before uploading. Without a terminal it
// refuses, since only --yes may approve an upload non-interactively.
func confirmBackfill(plan reporter.Plan, server string, stderr io.Writer) bool {
	if !backfillInteractive() {
		fmt.Fprintln(stderr, "firekeeper backfill: input is not a terminal; pass --yes to upload")
		return false
	}
	total := plan.Total()
	fmt.Fprintf(stderr, "Upload %d %s (~%d events, ~%d %s) to %s? [y/N] ",
		total.Sessions, plural(total.Sessions, "session", "sessions"), total.Events,
		total.Requests, plural(total.Requests, "request", "requests"), server)
	answer, _ := bufio.NewReader(backfillInput).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	}
	fmt.Fprintln(stderr, "firekeeper backfill: nothing uploaded")
	return false
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
