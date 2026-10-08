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

func runReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("firekeeper report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := reporter.Config{Out: stdout}
	fileCfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper report: %v\n", err)
		return 2
	}
	fs.StringVar(&cfg.Server, "server", fileCfg.Server, "dashboard server URL")
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
	if len(providers) == 0 {
		for _, name := range fileCfg.Providers {
			provider := transcript.Provider(strings.ToLower(name))
			if !provider.Valid() {
				fmt.Fprintf(stderr, "firekeeper report: config: unknown provider %q\n", name)
				return 2
			}
			providers = append(providers, provider)
		}
	}
	cfg.Providers = providers
	cfg.Exclude = fileCfg.Exclude
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
