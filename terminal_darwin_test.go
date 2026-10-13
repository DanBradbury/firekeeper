package main

import (
	"os"
	"strings"
	"testing"
)

// This test runs the real osascript path, so it only builds on macOS and only
// runs when FIREKEEPER_MACOS_INTEGRATION=1 (set by the macos CI job). Run
// locally it could ask for Automation permission. The adapter scripts are
// compiled by TestMacTerminalAdapterScriptsCompile in main_test.go.

func requireMacOSIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("FIREKEEPER_MACOS_INTEGRATION") != "1" {
		t.Skip("set FIREKEEPER_MACOS_INTEGRATION=1 to run AppleScript integration tests")
	}
}

// TestFocusTerminalSessionRunsOsascript drives the production osascript path
// end to end for a TTY no terminal owns. Each adapter must run and report
// either not-running or not-found; a script error fails the test.
func TestFocusTerminalSessionRunsOsascript(t *testing.T) {
	requireMacOSIntegration(t)
	app, err := focusTerminalSession("ttys-firekeeper-ci", t.TempDir())
	if err == nil {
		t.Fatalf("focused %s for a TTY no terminal owns", app)
	}
	message := err.Error()
	if strings.Contains(message, "-1743") || strings.Contains(message, "Not authorized") {
		t.Skipf("Automation permission unavailable on this runner: %v", err)
	}
	if message != "no supported terminal app is running" &&
		message != "no supported terminal session owns /dev/ttys-firekeeper-ci" {
		t.Fatalf("unexpected osascript result: %v", err)
	}
}
