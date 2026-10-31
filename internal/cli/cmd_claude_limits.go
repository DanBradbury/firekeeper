package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/DanBradbury/firekeeper/internal/claudelimits"
	"github.com/DanBradbury/firekeeper/internal/config"
)

const claudeLimitsUsage = "Usage:\n  firekeeper claude-limits enable\n  firekeeper claude-limits disable\n  firekeeper claude-limits status"

// runClaudeLimits turns on, turns off, or checks the dashboard's Claude limit
// meters. Enabling lets the dashboard read the sign-in Claude Code already
// stores on this machine and ask Anthropic's usage endpoint, the way Claude
// Code's /usage does. Nothing is uploaded and nothing is written to Claude
// Code's files.
func runClaudeLimits(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, claudeLimitsUsage)
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "firekeeper claude-limits: find home directory")
		return 1
	}
	cfgPath, err := config.ResolvePath(home, os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper claude-limits: %v\n", err)
		return 1
	}
	switch args[0] {
	case "enable":
		if err := config.SetClaudeLimits(cfgPath, true); err != nil {
			fmt.Fprintf(stderr, "firekeeper claude-limits: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "Claude limits are on. The dashboard will now read the Claude Code sign-in stored")
		fmt.Fprintln(stdout, "on this machine and ask Anthropic's usage endpoint for your 5-hour and weekly limits.")
		fmt.Fprintln(stdout, "That endpoint is undocumented and may change. The token is used for that request")
		fmt.Fprintln(stdout, "only: it is not stored, refreshed, logged, or uploaded. On macOS the Keychain may")
		fmt.Fprintln(stdout, "ask you to allow access.")
		fmt.Fprintln(stdout, "Turn it off with: firekeeper claude-limits disable")
		return 0
	case "disable":
		if err := config.SetClaudeLimits(cfgPath, false); err != nil {
			fmt.Fprintf(stderr, "firekeeper claude-limits: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "Claude limits are off. Firekeeper will not read the Claude Code sign-in.")
		return 0
	case "status":
		return claudeLimitsStatus(stdout)
	default:
		fmt.Fprintf(stderr, "firekeeper claude-limits: unknown command %q\n%s\n", args[0], claudeLimitsUsage)
		return 2
	}
}

func claudeLimitsStatus(stdout io.Writer) int {
	c, err := config.Load(config.Options{Getenv: os.Getenv})
	if err != nil {
		fmt.Fprintf(stdout, "config: %v\n", err)
		return 1
	}
	if !c.ClaudeLimits {
		fmt.Fprintln(stdout, "Claude limits: off (firekeeper claude-limits enable)")
		return 0
	}
	fmt.Fprintln(stdout, "Claude limits: on")
	client := claudelimits.Client{}
	if !client.HasToken() {
		fmt.Fprintf(stdout, "Sign-in: %v\n", claudelimits.ErrNoToken)
		return 0
	}
	fmt.Fprintln(stdout, "Sign-in: found")
	limits, err := client.Fetch(context.Background())
	if err != nil {
		fmt.Fprintf(stdout, "Usage endpoint: %v\n", err)
		return 0
	}
	for _, w := range []struct {
		name   string
		window *claudelimits.Window
	}{{"5-hour", limits.FiveHour}, {"weekly", limits.SevenDay}, {"weekly Opus", limits.SevenDayOpus}, {"weekly Sonnet", limits.SevenDaySonnet}} {
		if w.window != nil {
			fmt.Fprintf(stdout, "%s: %.0f%% used\n", w.name, w.window.UsedPercent)
		}
	}
	return 0
}
