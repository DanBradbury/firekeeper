package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/term"

	"github.com/DanBradbury/firekeeper/internal/update"
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

// offerUpdate runs the launch-time update check and prompt. It returns true
// when the user updated, so the caller exits instead of starting the TUI.
// Nothing runs unless both stdin and stdout are terminals.
func offerUpdate() bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	c := &update.Checker{Current: version, Dir: filepath.Join(home, ".firekeeper")}
	ctx := context.Background()
	latest, ok := c.Available(ctx)
	if !ok {
		return false
	}
	return c.Prompt(ctx, latest, os.Stdin, os.Stdout, os.Stderr)
}
