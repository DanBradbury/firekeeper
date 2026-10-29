package cli

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/DanBradbury/firekeeper/internal/config"
	"github.com/DanBradbury/firekeeper/internal/otelrecv"
)

func runTelemetry(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "Usage:\n  firekeeper telemetry enable\n  firekeeper telemetry disable\n  firekeeper telemetry status")
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "firekeeper telemetry: find home directory")
		return 1
	}
	claudeDir, err := claudeSettingsDir()
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper telemetry: %v\n", err)
		return 1
	}
	cfgPath, err := config.ResolvePath(home, os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper telemetry: %v\n", err)
		return 1
	}
	switch args[0] {
	case "enable":
		return telemetryEnable(stdout, stderr, claudeDir, cfgPath)
	case "disable":
		return telemetryDisable(stdout, stderr, claudeDir, cfgPath)
	case "status":
		return telemetryStatus(stdout, claudeDir)
	default:
		fmt.Fprintf(stderr, "firekeeper telemetry: unknown command %q\n", args[0])
		return 2
	}
}

func claudeSettingsDir() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory")
	}
	return filepath.Join(home, ".claude"), nil
}

func telemetryEnable(stdout, stderr io.Writer, claudeDir, cfgPath string) int {
	port := config.DefaultTelemetryPort
	if c, err := config.Load(config.Options{Getenv: os.Getenv}); err == nil && c.TelemetryPort > 0 {
		port = c.TelemetryPort
	}
	changed, err := otelrecv.EnableClaudeEnv(claudeDir, port)
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper telemetry: %v\n", err)
		return 1
	}
	if err := config.SetTelemetry(cfgPath, true, port); err != nil {
		fmt.Fprintf(stderr, "firekeeper telemetry: %v\n", err)
		return 1
	}
	if changed {
		fmt.Fprintf(stdout, "Claude Code settings now export api_request logs to http://127.0.0.1:%d\n", port)
		fmt.Fprintln(stdout, "A backup of the previous settings is settings.json.firekeeper-backup.")
	} else {
		fmt.Fprintln(stdout, "Claude Code telemetry settings were already set.")
	}
	fmt.Fprintf(stdout, "Firekeeper will listen on 127.0.0.1:%d when the daemon is running.\n", port)
	fmt.Fprintln(stdout, "Restart it with: firekeeper daemon")
	fmt.Fprintln(stdout, "Prompt and response logging stays off. This does not upload anything.")
	return 0
}

func telemetryDisable(stdout, stderr io.Writer, claudeDir, cfgPath string) int {
	port := config.DefaultTelemetryPort
	if c, err := config.Load(config.Options{Getenv: os.Getenv}); err == nil && c.TelemetryPort > 0 {
		port = c.TelemetryPort
	}
	changed, err := otelrecv.DisableClaudeEnv(claudeDir)
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper telemetry: %v\n", err)
		return 1
	}
	if err := config.SetTelemetry(cfgPath, false, port); err != nil {
		fmt.Fprintf(stderr, "firekeeper telemetry: %v\n", err)
		return 1
	}
	if changed {
		fmt.Fprintln(stdout, "Removed the Firekeeper telemetry environment from Claude Code settings.")
	} else {
		fmt.Fprintln(stdout, "Claude Code telemetry environment was already absent.")
	}
	fmt.Fprintln(stdout, "The listener stays off until you enable it again. Recorded usage is kept.")
	return 0
}

func telemetryStatus(stdout io.Writer, claudeDir string) int {
	c, err := config.Load(config.Options{Getenv: os.Getenv})
	if err != nil {
		fmt.Fprintf(stdout, "config: %v\n", err)
		return 1
	}
	port := c.TelemetryPort
	if port == 0 {
		port = config.DefaultTelemetryPort
	}
	on, err := otelrecv.ClaudeEnv(claudeDir, port)
	if err != nil {
		fmt.Fprintf(stdout, "Claude settings: %v\n", err)
	} else if on {
		fmt.Fprintf(stdout, "Claude settings: exporting to http://127.0.0.1:%d\n", port)
	} else {
		fmt.Fprintln(stdout, "Claude settings: telemetry env is not set")
	}
	if c.TelemetryEnabled {
		fmt.Fprintf(stdout, "Firekeeper config: listener enabled on 127.0.0.1:%d\n", port)
	} else {
		fmt.Fprintln(stdout, "Firekeeper config: listener off")
	}
	if probeLoopback(port) {
		fmt.Fprintf(stdout, "Listener: accepting connections on 127.0.0.1:%d\n", port)
	} else {
		fmt.Fprintf(stdout, "Listener: nothing is accepting connections on 127.0.0.1:%d\n", port)
	}
	path, err := otelrecv.DefaultPath()
	if err != nil {
		fmt.Fprintln(stdout, "Events: store unavailable")
		return 0
	}
	last, ok, err := otelrecv.LastEvent(path)
	if err != nil {
		fmt.Fprintln(stdout, "Events: store unreadable")
		return 0
	}
	if !ok {
		fmt.Fprintln(stdout, "Events: none recorded")
		return 0
	}
	age := time.Since(last).Round(time.Second)
	if age < 2*time.Minute && age >= 0 {
		fmt.Fprintf(stdout, "Events: last at %s (%s ago, recent)\n", last.Format(time.RFC3339), age)
	} else {
		fmt.Fprintf(stdout, "Events: last at %s (%s ago)\n", last.Format(time.RFC3339), age)
	}
	return 0
}

func probeLoopback(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
