// Command firekeeper is a keyboard-driven terminal dashboard for monitoring
// coding-agent harnesses. Subcommands live in internal/cli; with no
// subcommand it starts the dashboard from internal/tui.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/DanBradbury/firekeeper/internal/cli"
	"github.com/DanBradbury/firekeeper/internal/tui"
	"github.com/DanBradbury/firekeeper/internal/update"
	"github.com/DanBradbury/firekeeper/internal/version"
)

func main() {
	if code, ok := cli.RunSubcommand(os.Args[1:], os.Stdout, os.Stderr); ok {
		os.Exit(code)
	}
	flag.Usage = func() { cli.PrintUsage(flag.CommandLine) }
	flag.Parse()
	if offerUpdate() {
		return
	}
	p := tea.NewProgram(tui.NewModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "firekeeper TUI failed: %v\n", err)
		os.Exit(1)
	}
}

func offerUpdate() bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	checker := &update.Checker{Current: version.Version, Dir: filepath.Join(home, ".firekeeper")}
	ctx := context.Background()
	latest, ok := checker.Available(ctx)
	if !ok {
		return false
	}
	return checker.Prompt(ctx, latest, os.Stdin, os.Stdout, os.Stderr)
}
