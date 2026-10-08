package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// reportOnce is replaced in tests so report never scans real processes.
var reportOnce = reporter.RunOnce

// providerList is a repeatable --provider flag.
type providerList []transcript.Provider

func (p *providerList) String() string {
	names := make([]string, len(*p))
	for i, provider := range *p {
		names[i] = string(provider)
	}
	return strings.Join(names, ",")
}

func (p *providerList) Set(value string) error {
	provider := transcript.Provider(strings.ToLower(strings.TrimSpace(value)))
	if !provider.Valid() {
		return fmt.Errorf("unknown provider %q", value)
	}
	*p = append(*p, provider)
	return nil
}

// tokenEnv names the environment variable that supplies the bearer token.
const tokenEnv = "FIREKEEPER_TOKEN"

// tokenFlag registers --token and returns a getter that falls back to
// FIREKEEPER_TOKEN when the flag is unset.
func tokenFlag(fs *flag.FlagSet) func() string {
	v := fs.String("token", "", "ingest token for the dashboard server (default $"+tokenEnv+")")
	return func() string {
		if *v != "" {
			return *v
		}
		return os.Getenv(tokenEnv)
	}
}

func runReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("firekeeper report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := reporter.Config{Out: stdout}
	fs.StringVar(&cfg.Server, "server", reporter.DefaultServer, "dashboard server URL")
	tokenFlag := tokenFlag(fs)
	var providers providerList
	fs.Var(&providers, "provider", "upload this provider's sessions (repeatable); with none, nothing is uploaded")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "print what would be uploaded without uploading")
	fs.DurationVar(&cfg.Since, "since", 0, "only read transcripts modified within this duration (for example 24h)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "firekeeper report: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if cfg.Since < 0 {
		fmt.Fprintln(stderr, "firekeeper report: --since must not be negative")
		return 2
	}
	cfg.Providers = providers
	cfg.Token = tokenFlag()
	cfg.Version = reporterVersion()
	if len(providers) == 0 && !cfg.DryRun {
		fmt.Fprintln(stderr, "firekeeper report: no --provider given; uploading nothing. Showing what would be uploaded.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	summary, err := reportOnce(ctx, cfg)
	if summary.Warning != nil {
		fmt.Fprintf(stderr, "firekeeper report warning: %v\n", summary.Warning)
	}
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper report: %v\n", err)
		return 1
	}
	if summary.Failed() {
		return 1
	}
	return 0
}
