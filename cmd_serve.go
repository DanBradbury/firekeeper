package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/DanBradbury/firekeeper/internal/server"
	"github.com/DanBradbury/firekeeper/internal/server/api"
	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
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
	if len(args) > 0 && args[0] == "admin" {
		return runAdmin(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "invite" {
		return runInvite(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("firekeeper serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := server.Config{Out: stdout, Err: stderr}
	fs.StringVar(&cfg.Listen, "listen", server.DefaultListen, "address to listen on")
	fs.StringVar(&cfg.DB, "db", "", "dashboard database path (default ~/.firekeeper/dashboard.db)")
	fs.StringVar(&cfg.FileLink, "file-link", "", "URL template linking changed files to the repository host, using {project}, {ref}, {commit}, {branch}, and {path} (default repo_url_template in the config file)")
	fs.BoolVar(&cfg.Insecure, "insecure", false, "allow a non-loopback --listen address with no tokens or accounts; the API is then unauthenticated")
	limits := api.DefaultLimits()
	fs.Var(&byteSize{&limits.MaxBytes}, "max-bytes", "most transcript data one account may store, such as 500MB or 2GiB; 0 for no limit")
	fs.Int64Var(&limits.MaxSessions, "max-sessions", limits.MaxSessions, "most sessions one account may store; 0 for no limit")
	fs.IntVar(&limits.IngestPerMinute, "max-ingest-per-minute", limits.IngestPerMinute, "most ingest requests one account may make per minute; 0 for no limit")
	fs.StringVar(&cfg.Banner, "banner", "", "text shown at the top of every page, such as \"Test bed: data may be wiped\"")
	var proxies stringList
	fs.Var(&proxies, "trusted-proxy", "reverse proxy whose X-Forwarded-For is believed, as an IP address or CIDR range; repeatable (default trusted_proxies in the config file; none)")
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

	c, err := loadConfig(configFlags{})
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper serve: %v\n", err)
		return 2
	}
	if limits.MaxBytes < 0 || limits.MaxSessions < 0 || limits.IngestPerMinute < 0 {
		fmt.Fprintln(stderr, "firekeeper serve: limits must not be negative")
		return 2
	}
	if err := server.ValidateBanner(cfg.Banner); err != nil {
		fmt.Fprintf(stderr, "firekeeper serve: %v\n", err)
		return 2
	}
	cfg.Limits = limits
	mode, err := auth.ParseSignupMode(*signup)
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper serve: %v\n", err)
		return 2
	}
	if cfg.FileLink == "" {
		cfg.FileLink = c.RepoURLTemplate
	}
	if len(c.Prices) > 0 {
		cfg.Prices = make(map[string]store.Price, len(c.Prices))
		for model, p := range c.Prices {
			cfg.Prices[model] = store.Price{Input: p.Input, Output: p.Output, Cache: p.Cache}
		}
	}
	cfg.Signup = mode
	specs := []string(proxies)
	if len(specs) == 0 {
		specs = c.TrustedProxies
	}
	if cfg.TrustedProxies, err = auth.ParseTrustedProxies(specs); err != nil {
		fmt.Fprintf(stderr, "firekeeper serve: %v\n", err)
		return 2
	}

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

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// byteSize is a flag.Value for a size such as "500MB" or "2GiB".
type byteSize struct{ p *int64 }

func (b *byteSize) String() string {
	if b == nil || b.p == nil {
		return ""
	}
	return strconv.FormatInt(*b.p, 10)
}

func (b *byteSize) Set(v string) error {
	n, err := parseByteSize(v)
	if err != nil {
		return err
	}
	*b.p = n
	return nil
}

// parseByteSize reads a non-negative size: a plain number of bytes, or a
// number with a unit. KB, MB, GB and TB are powers of 1000; KiB, MiB, GiB
// and TiB are powers of 1024. Units are case-insensitive.
func parseByteSize(v string) (int64, error) {
	t := strings.ToLower(strings.TrimSpace(v))
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{
		{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30}, {"tib", 1 << 40},
		{"kb", 1000}, {"mb", 1000 * 1000}, {"gb", 1000 * 1000 * 1000}, {"tb", 1000 * 1000 * 1000 * 1000},
		{"b", 1},
	} {
		if strings.HasSuffix(t, u.suffix) {
			t, mult = strings.TrimSpace(strings.TrimSuffix(t, u.suffix)), u.mult
			break
		}
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/mult {
		return 0, fmt.Errorf("invalid size %q: use a number of bytes or a unit such as 500MB or 2GiB", v)
	}
	return n * mult, nil
}
