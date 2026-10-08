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
	"syscall"
	"time"

	"github.com/DanBradbury/firekeeper/internal/config"
	"github.com/DanBradbury/firekeeper/internal/daemon"
	"github.com/DanBradbury/firekeeper/internal/reporter"
)

// daemonRun is replaced in tests so daemon never takes the real lock.
var daemonRun = daemon.Run

// newService is replaced in tests so install and uninstall never touch the
// real home directory or run launchctl or systemctl.
var newService = daemon.NewService

// daemonFlags are the flags the daemon runs with. install takes the same
// ones and writes the ones given into the service definition. cfg is the
// merged configuration once parsed.
type daemonFlags struct {
	server    string
	providers providerList
	interval  time.Duration
	cfg       config.Config
}

func registerDaemonFlags(fs *flag.FlagSet) *daemonFlags {
	f := &daemonFlags{}
	fs.StringVar(&f.server, "server", reporter.DefaultServer, "dashboard server URL")
	fs.Var(&f.providers, "provider", "upload this provider's sessions (repeatable); at least one is required")
	fs.DurationVar(&f.interval, "interval", daemon.DefaultInterval, fmt.Sprintf("time between passes (at least %s)", daemon.MinInterval))
	return f
}

// parseDaemonFlags parses args into fs, merges them with the config file
// and environment into f.cfg, and checks the result. It returns an exit
// code and false when the command should stop.
func parseDaemonFlags(name string, fs *flag.FlagSet, f *daemonFlags, args []string, stderr io.Writer) (int, bool) {
	if code, ok := parseNoArgs(name, fs, args, stderr); !ok {
		return code, false
	}
	c, err := loadConfig(configFlags{fs: fs, server: &f.server, providers: &f.providers, interval: &f.interval})
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 2, false
	}
	f.cfg = c
	if c.Interval < daemon.MinInterval {
		fmt.Fprintf(stderr, "%s: --interval must be at least %s\n", name, daemon.MinInterval)
		return 2, false
	}
	if len(c.Providers) == 0 {
		fmt.Fprintf(stderr, "%s: no --provider given (or providers in the config file); the daemon uploads nothing without one\n", name)
		return 2, false
	}
	return 0, true
}

func parseNoArgs(name string, fs *flag.FlagSet, args []string, stderr io.Writer) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, false
		}
		return 2, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: unexpected argument %q\n", name, fs.Arg(0))
		return 2, false
	}
	return 0, true
}

func runDaemon(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "install":
			return runDaemonInstall(args[1:], stdout, stderr)
		case "uninstall":
			return runDaemonUninstall(args[1:], stdout, stderr)
		case "status":
			return runDaemonStatus(args[1:], stdout, stderr)
		case "logs":
			return runDaemonLogs(args[1:], stdout, stderr)
		}
	}
	fs := flag.NewFlagSet("firekeeper daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage:\n  firekeeper daemon --provider NAME [flags]\n  firekeeper daemon install --provider NAME [flags] [--dry-run]\n  firekeeper daemon uninstall [--purge]\n  firekeeper daemon status\n  firekeeper daemon logs [-n LINES] [-f]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	flags := registerDaemonFlags(fs)
	quiet := fs.Bool("quiet", false, "log only to ~/.firekeeper/daemon.log, not to stderr")
	if code, ok := parseDaemonFlags("firekeeper daemon", fs, flags, args, stderr); !ok {
		return code
	}
	cfg := daemon.Config{Interval: flags.cfg.Interval}
	if !*quiet {
		cfg.Out = stderr
	}
	applyReporterConfig(&cfg.Reporter, flags.cfg)

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

func runDaemonInstall(args []string, stdout, stderr io.Writer) int {
	const name = "firekeeper daemon install"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	flags := registerDaemonFlags(fs)
	dryRun := fs.Bool("dry-run", false, "print the service file and commands without writing or running anything")
	if code, ok := parseDaemonFlags(name, fs, flags, args, stderr); !ok {
		return code
	}
	// The service runs the daemon with the flags given now; settings left
	// to the config file are read by the daemon each time it starts, so
	// editing the file and restarting the service takes effect. --quiet
	// keeps it from writing every log line twice.
	set := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
	daemonArgs := []string{"--quiet"}
	if set["server"] {
		daemonArgs = append(daemonArgs, "--server", flags.server)
	}
	for _, p := range flags.providers {
		daemonArgs = append(daemonArgs, "--provider", string(p))
	}
	if set["interval"] {
		daemonArgs = append(daemonArgs, "--interval", flags.interval.String())
	}

	svc, err := newService(daemonArgs, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	if *dryRun {
		content, err := svc.File()
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return 1
		}
		fmt.Fprintf(stdout, "would write %s:\n\n%s\nthen run:\n", svc.Path(), content)
		for _, cmd := range svc.Commands() {
			fmt.Fprintf(stdout, "  %s\n", strings.Join(cmd, " "))
		}
		return 0
	}
	if err := svc.Install(context.Background()); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	return 0
}

func runDaemonUninstall(args []string, stdout, stderr io.Writer) int {
	const name = "firekeeper daemon uninstall"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	purge := fs.Bool("purge", false, "also delete read offsets, the machine id, and daemon logs from ~/.firekeeper")
	if code, ok := parseNoArgs(name, fs, args, stderr); !ok {
		return code
	}
	svc, err := newService(nil, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	err = svc.Uninstall(context.Background(), *purge)
	if errors.Is(err, daemon.ErrNotInstalled) {
		fmt.Fprintln(stdout, "the daemon service is not installed")
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	return 0
}

// runDaemonStatus exits 0 when the service is running and 3 when it is
// not, like systemctl status.
func runDaemonStatus(args []string, stdout, stderr io.Writer) int {
	const name = "firekeeper daemon status"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if code, ok := parseNoArgs(name, fs, args, stderr); !ok {
		return code
	}
	svc, err := newService(nil, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	st, err := svc.Status(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	installed := "no"
	if st.Installed {
		installed = "yes"
	}
	fmt.Fprintf(stdout, "service file: %s (installed: %s)\n", svc.Path(), installed)
	switch {
	case st.Running && st.PID > 0:
		fmt.Fprintf(stdout, "state:        running, pid %d\n", st.PID)
	case st.Loaded && st.State != "":
		fmt.Fprintf(stdout, "state:        %s\n", st.State)
	case st.Loaded:
		fmt.Fprintln(stdout, "state:        loaded")
	default:
		fmt.Fprintln(stdout, "state:        not loaded")
	}
	if st.Loaded && st.LastExit != "" {
		fmt.Fprintf(stdout, "last exit:    %s\n", st.LastExit)
	}
	fmt.Fprintf(stdout, "log:          %s\n", svc.LogPath())
	if !st.Running {
		return 3
	}
	return 0
}

func runDaemonLogs(args []string, stdout, stderr io.Writer) int {
	const name = "firekeeper daemon logs"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	lines := fs.Int("n", 50, "number of lines to show")
	follow := fs.Bool("f", false, "keep printing new lines until interrupted")
	if code, ok := parseNoArgs(name, fs, args, stderr); !ok {
		return code
	}
	if *lines < 0 {
		fmt.Fprintf(stderr, "%s: -n must not be negative\n", name)
		return 2
	}
	svc, err := newService(nil, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	offset, err := daemon.Tail(svc.LogPath(), *lines, stdout)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "%s: no daemon log yet at %s\n", name, svc.LogPath())
		printStartupLogHint(svc, stderr)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	if !*follow {
		printStartupLogHint(svc, stderr)
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := daemon.Follow(ctx, svc.LogPath(), offset, 500*time.Millisecond, stdout); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	return 0
}

// printStartupLogHint points at where the service manager keeps errors
// from before the daemon opened its log, such as a bad flag or the lock
// being held.
func printStartupLogHint(svc *daemon.Service, stderr io.Writer) {
	if svc.GOOS == "linux" {
		fmt.Fprintf(stderr, "startup errors are in the user journal: journalctl --user -u %s\n", daemon.SystemdUnit)
		return
	}
	if info, err := os.Stat(svc.StartupLogPath()); err == nil && info.Size() > 0 {
		fmt.Fprintf(stderr, "startup errors are in %s\n", svc.StartupLogPath())
	}
}
