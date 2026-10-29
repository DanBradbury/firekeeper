// Package cli implements Firekeeper's subcommands, such as report, daemon,
// login, and version. Running firekeeper with no subcommand starts the
// terminal dashboard instead (see internal/tui).
package cli

import (
	"flag"
	"fmt"
	"io"
)

// subcommand is an opt-in reporting entry point. Running firekeeper without
// one starts the TUI exactly as before.
type subcommand struct {
	name    string
	summary string
	run     func(args []string, stdout, stderr io.Writer) int
}

var subcommands = []subcommand{
	{"snapshot", "print currently discovered sessions (--json)", runSnapshot},
	{"export", "export local transcripts (not implemented)", runExport},
	{"report", "upload session metadata and redacted transcripts once", runReport},
	{"backfill", "upload sessions already on this machine once (--all or --provider)", runBackfill},
	{"serve", "run the dashboard server (separate firekeeper-server binary)", runServe},
	{"login", "link this machine to a dashboard server (--server URL)", runLogin},
	{"logout", "revoke and remove this machine's stored token", runLogout},
	{"whoami", "print the server, account and machine the stored token belongs to", runWhoami},
	{"config", "print the merged reporting configuration (show)", runConfig},
	{"daemon", "upload continuously (--provider required); install, uninstall, status, logs", runDaemon},
	{"otel-listen", "experimental: record local Claude Code OTLP usage on 127.0.0.1", runOtelListen},
	{"otel-reconcile", "experimental: compare an otel-listen file with transcript totals", runOtelReconcile},
	{"telemetry", "opt-in Claude Code usage listener: enable, disable, status", runTelemetry},
	{"version", "print the version, commit and build date (also --version)", runVersion},
}

// RunSubcommand dispatches on the first argument. It reports false when the
// arguments do not name a subcommand, so the caller falls through to the TUI.
func RunSubcommand(args []string, stdout, stderr io.Writer) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	if args[0] == "--version" {
		return runVersion(args[1:], stdout, stderr), true
	}
	for _, cmd := range subcommands {
		if cmd.name == args[0] {
			return cmd.run(args[1:], stdout, stderr), true
		}
	}
	return 0, false
}

// PrintUsage writes the top-level usage text and the subcommand list to fs's
// output.
func PrintUsage(fs *flag.FlagSet) {
	out := fs.Output()
	fmt.Fprintf(out, "Usage:\n  firekeeper\n  firekeeper <command> [args]\n\nCommands:\n")
	for _, cmd := range subcommands {
		fmt.Fprintf(out, "  %-16s %s\n", cmd.name, cmd.summary)
	}
}

func notImplemented(name string, stderr io.Writer) int {
	fmt.Fprintf(stderr, "firekeeper %s: not implemented\n", name)
	return 2
}
