package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/daemon"
	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/claudecloud"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// backfillRun, backfillInput, and backfillInteractive are replaced in tests
// so backfill never reads real transcripts or waits on a terminal.
var (
	backfillRun                       = reporter.Backfill
	backfillInput           io.Reader = os.Stdin
	backfillInteractive               = stdinIsTerminal
	detectBackfillProviders           = func() ([]transcript.Provider, error) {
		home, err := configHome()
		if err != nil {
			return nil, errors.New("find home directory")
		}
		return daemon.DetectProviders(home, nil, nil), nil
	}
)

// claudeCloudSync downloads the Claude Code on the web sessions into dir. It
// is replaced in tests so backfill never reads the keychain or the network.
var claudeCloudSync = func(ctx context.Context, home, dir string, after time.Time, progress io.Writer) (claudecloud.SyncResult, error) {
	creds, err := claudecloud.LoadCredentials(ctx, home)
	if err != nil {
		return claudecloud.SyncResult{}, err
	}
	return claudecloud.Sync(ctx, claudecloud.NewClient(creds), claudecloud.SyncOptions{Dir: dir, After: after, Progress: progress})
}

// claudeCloudDir is where cloud sessions are cached: Firekeeper's own
// directory, never the Claude Code config directory.
func claudeCloudDir(home string) string {
	return filepath.Join(home, ".firekeeper", "cloud", "claude")
}

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
	all := fs.Bool("all", false, "import all supported providers found locally for this pass")
	fs.DurationVar(&cfg.Since, "since", 0, "only import transcripts modified within this duration (for example 720h)")
	after := fs.String("after", "", "only import transcripts modified after this date (YYYY-MM-DD, local time)")
	fs.IntVar(&cfg.Limit, "limit", 0, "import at most this many sessions, newest first")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "print the plan without uploading")
	cloud := fs.Bool("claude-cloud", false, "import Claude Code on the web sessions instead of local ones (macOS; uses your Claude Code login and Anthropic's internal API)")
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
	if *all && len(providers) > 0 {
		fmt.Fprintln(stderr, "firekeeper backfill: use --all or --provider, not both")
		return 2
	}
	if *cloud && (*all || len(providers) > 0) {
		fmt.Fprintln(stderr, "firekeeper backfill: --claude-cloud imports only cloud sessions; run it without --all or --provider")
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *cloud {
		if code := prepareClaudeCloud(ctx, &cfg, stdout, stderr); code != 0 {
			return code
		}
	}
	if *all {
		cfg.Providers, err = detectBackfillProviders()
		if err != nil {
			fmt.Fprintf(stderr, "firekeeper backfill: detect providers: %v\n", err)
			return 1
		}
		if len(cfg.Providers) == 0 {
			fmt.Fprintln(stdout, "firekeeper backfill: no supported providers found locally; nothing uploaded")
			return 0
		}
		names := make([]string, len(cfg.Providers))
		for i, provider := range cfg.Providers {
			names[i] = string(provider)
		}
		fmt.Fprintf(stdout, "Detected providers: %s\n", strings.Join(names, ", "))
	}
	if len(cfg.Providers) == 0 && !cfg.DryRun {
		fmt.Fprintln(stderr, "firekeeper backfill: no --provider given; uploading nothing. Showing the plan.")
	}
	if !*yes {
		cfg.Confirm = func(plan reporter.Plan) bool { return confirmBackfill(plan, cfg.Server, stderr) }
	}

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

// prepareClaudeCloud downloads the cloud sessions and points cfg at the
// cache, so the normal plan, confirm and upload steps handle them. It runs
// only for --claude-cloud. Even with --dry-run it fetches, since the plan
// needs the events; nothing is uploaded to the dashboard.
func prepareClaudeCloud(ctx context.Context, cfg *reporter.BackfillConfig, stdout, stderr io.Writer) int {
	home, err := configHome()
	if err != nil {
		fmt.Fprintln(stderr, "firekeeper backfill: find home directory")
		return 1
	}
	cutoff := cfg.After
	if cfg.Since > 0 {
		if since := time.Now().Add(-cfg.Since); since.After(cutoff) {
			cutoff = since
		}
	}
	dir := claudeCloudDir(home)
	fmt.Fprintln(stderr, "Fetching Claude Code on the web sessions from api.anthropic.com with your Claude Code login (read-only)...")
	res, err := claudeCloudSync(ctx, home, dir, cutoff, stderr)
	switch {
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(stderr, "firekeeper backfill: interrupted; run it again to resume")
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "firekeeper backfill: %v\n", err)
		return 1
	}
	for _, failure := range res.Failed {
		fmt.Fprintf(stderr, "firekeeper backfill warning: cloud session %s\n", failure)
	}
	fmt.Fprintf(stdout, "Cloud sessions: %d listed, %d downloaded, %d already cached, %d older than the cutoff\n",
		res.Listed, res.Fetched, res.Current, res.Skipped)
	source := &claudecloud.Source{Dir: dir}
	only := func(provider transcript.Provider) bool { return provider == transcript.ProviderClaude }
	cfg.Providers = []transcript.Provider{transcript.ProviderClaude}
	cfg.Sources = func(provider transcript.Provider) (reader.Source, bool) { return source, only(provider) }
	cfg.Enumerators = func(provider transcript.Provider) (reader.Enumerator, bool) { return source, only(provider) }
	return 0
}
