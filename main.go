// Command firekeeper is a keyboard-driven terminal dashboard for monitoring
// coding-agent harnesses. Subcommands live in internal/cli; with no
// subcommand it starts the dashboard from internal/tui.
package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DanBradbury/firekeeper/internal/cli"
	"github.com/DanBradbury/firekeeper/internal/tui"
)

func main() {
	if code, ok := cli.RunSubcommand(os.Args[1:], os.Stdout, os.Stderr); ok {
		os.Exit(code)
	}
	flag.Usage = func() { cli.PrintUsage(flag.CommandLine) }
	flag.Parse()
	p := tea.NewProgram(tui.NewModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "firekeeper TUI failed: %v\n", err)
		os.Exit(1)
	}
}
