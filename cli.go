package main

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
	{"serve", "run the local dashboard server and web UI", runServe},
	{"daemon", "report continuously in the background (not implemented)", runDaemon},
}

// runSubcommand dispatches on the first argument. It reports false when the
// arguments do not name a subcommand, so the caller falls through to the TUI.
func runSubcommand(args []string, stdout, stderr io.Writer) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	for _, cmd := range subcommands {
		if cmd.name == args[0] {
			return cmd.run(args[1:], stdout, stderr), true
		}
	}
	return 0, false
}

func printUsage(fs *flag.FlagSet) {
	out := fs.Output()
	fmt.Fprintf(out, "Usage:\n  firekeeper [flags]\n  firekeeper <command> [args]\n\nCommands:\n")
	for _, cmd := range subcommands {
		fmt.Fprintf(out, "  %-10s %s\n", cmd.name, cmd.summary)
	}
	fmt.Fprintf(out, "\nFlags:\n")
	fs.PrintDefaults()
}

func notImplemented(name string, stderr io.Writer) int {
	fmt.Fprintf(stderr, "firekeeper %s: not implemented\n", name)
	return 2
}
