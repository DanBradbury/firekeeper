package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime/debug"
)

// Release builds set these with -ldflags "-X main.version=... -X main.commit=...
// -X main.date=...". Plain go build and go run leave them at "dev".
var (
	version = "dev"
	commit  = "dev"
	date    = "dev"
)

// readBuildInfo is replaced in tests.
var readBuildInfo = debug.ReadBuildInfo

// buildVersion returns the version, commit, and build date. When no -ldflags
// were given, a binary installed with go install ...@vX.Y.Z still reports
// its module version and VCS revision where the toolchain recorded them.
func buildVersion() (v, c, d string) {
	v, c, d = version, commit, date
	if v != "dev" {
		return v, c, d
	}
	info, ok := readBuildInfo()
	if !ok {
		return v, c, d
	}
	if mv := info.Main.Version; mv != "" && mv != "(devel)" {
		v = mv
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if c == "dev" && s.Value != "" {
				c = s.Value
			}
		case "vcs.time":
			if d == "dev" && s.Value != "" {
				d = s.Value
			}
		}
	}
	return v, c, d
}

// reporterVersion is sent as machine.version in ingest and heartbeat requests.
func reporterVersion() string {
	v, _, _ := buildVersion()
	return v
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("firekeeper version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "firekeeper version: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	v, c, d := buildVersion()
	fmt.Fprintf(stdout, "firekeeper %s (commit %s, built %s)\n", v, c, d)
	return 0
}
