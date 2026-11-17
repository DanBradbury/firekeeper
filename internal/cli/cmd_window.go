package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/DanBradbury/firekeeper/internal/claudelimits"
	"github.com/DanBradbury/firekeeper/internal/config"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/window"
)

// windowLimits returns the Claude 5-hour limit when limits are on. It is a
// variable so tests never reach the config, Keychain or network.
var windowLimits = func(ctx context.Context) (*claudelimits.Window, error) {
	cfg, err := config.Load(config.Options{Getenv: os.Getenv})
	if err != nil || !cfg.ClaudeLimits {
		return nil, nil
	}
	limits, err := claudelimits.Client{}.Fetch(ctx)
	return limits.FiveHour, err
}

var windowNow = time.Now

// runWindow breaks the current 5-hour window down by session, so a user near
// the limit can see what used it. Everything is read locally; the HTML page is
// a file on this machine, never uploaded.
func runWindow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("firekeeper window", flag.ContinueOnError)
	fs.SetOutput(stderr)
	provider := fs.String("provider", string(transcript.ProviderClaude), "provider to review")
	asJSON := fs.Bool("json", false, "print the breakdown as JSON")
	htmlPath := fs.String("html", "", "write the breakdown as a standalone HTML page to `file` and print its file:// URL")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "firekeeper window: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *asJSON && *htmlPath != "" {
		fmt.Fprintln(stderr, "firekeeper window: use --json or --html, not both")
		return 2
	}
	prov := transcript.Provider(*provider)
	if !prov.Valid() {
		fmt.Fprintf(stderr, "firekeeper window: unknown provider %q\n", *provider)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	opts := window.Options{Provider: prov}
	if prov == transcript.ProviderClaude {
		// A failed limits request only costs the exact reset time.
		if w, err := windowLimits(ctx); err == nil && w != nil {
			opts.ResetsAt = w.ResetsAt
			used := w.UsedPercent
			opts.UsedPercent = &used
		}
	}
	report, err := window.Build(ctx, windowNow(), opts)
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper window: %v\n", err)
		return 1
	}

	switch {
	case *asJSON:
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(stderr, "firekeeper window: write JSON: %v\n", err)
			return 1
		}
	case *htmlPath != "":
		abs, err := filepath.Abs(*htmlPath)
		if err != nil {
			fmt.Fprintf(stderr, "firekeeper window: %v\n", err)
			return 1
		}
		f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			fmt.Fprintf(stderr, "firekeeper window: create page: %v\n", err)
			return 1
		}
		werr := window.WriteHTML(f, report)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			fmt.Fprintf(stderr, "firekeeper window: write page: %v\n", werr)
			return 1
		}
		fmt.Fprintln(stdout, (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String())
	default:
		window.WriteText(stdout, report)
	}
	return 0
}
