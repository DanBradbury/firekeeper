package main

import (
	"fmt"
	"io"
	"runtime"
)

// Set at build time with -ldflags "-X main.version=... -X main.commit=...
// -X main.date=...". Plain go build and go run leave them at their defaults.
var (
	version = "dev"
	commit  = "dev"
	date    = "dev"
)

// versionString is the one-line output of --version and the version command.
func versionString() string {
	return fmt.Sprintf("firekeeper %s (commit %s, built %s, %s/%s)", version, commit, date, runtime.GOOS, runtime.GOARCH)
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "firekeeper version: unexpected argument %q\n", args[0])
		return 2
	}
	fmt.Fprintln(stdout, versionString())
	return 0
}
