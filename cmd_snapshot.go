package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/DanBradbury/firekeeper/internal/session"
)

// discoverSessions is replaced in tests so snapshot never scans real processes.
var discoverSessions = session.Discover

func runSnapshot(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("firekeeper snapshot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print sessions as a JSON array")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "firekeeper snapshot: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if !*asJSON {
		fmt.Fprintln(stderr, "firekeeper snapshot: only --json output is implemented")
		return 2
	}

	metas, err := discoverSessions(context.Background(), session.Options{})
	var warning *session.Warning
	if err != nil && !errors.As(err, &warning) {
		fmt.Fprintf(stderr, "firekeeper snapshot: %v\n", err)
		return 1
	}
	if warning != nil {
		fmt.Fprintf(stderr, "firekeeper snapshot warning: %v\n", warning)
	}
	if metas == nil {
		metas = []session.Meta{}
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(metas); err != nil {
		fmt.Fprintf(stderr, "firekeeper snapshot: write JSON: %v\n", err)
		return 1
	}
	return 0
}
