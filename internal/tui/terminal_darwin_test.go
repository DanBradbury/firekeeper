package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/testenv"
)

// These tests use macOS tools, so this file only builds on macOS.
// TestFocusTerminalSessionRunsOsascript runs the real osascript path and only
// runs when FIREKEEPER_MACOS_INTEGRATION=1 (set by the macos CI job); run
// locally it could ask for Automation permission.
// TestMacTerminalAdapterScriptsCompile only compiles the adapter scripts.

func requireMacOSIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("FIREKEEPER_MACOS_INTEGRATION") != "1" {
		if testenv.InCI() {
			t.Fatal("FIREKEEPER_MACOS_INTEGRATION=1 is required in CI")
		}
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
		if testenv.InCI() {
			t.Fatalf("Automation permission is required in CI: %v", err)
		}
		t.Skipf("Automation permission unavailable: %v", err)
	}
	if message != "no supported terminal app is running" &&
		message != "no supported terminal session owns /dev/ttys-firekeeper-ci" {
		t.Fatalf("unexpected osascript result: %v", err)
	}
}

func TestMacTerminalAdapterScriptsCompile(t *testing.T) {
	for _, adapter := range macTerminalAdapters {
		t.Run(adapter.name, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "adapter.scpt")
			if output, err := exec.Command("osacompile", "-o", outputPath, "-e", adapter.script).CombinedOutput(); err != nil {
				t.Fatalf("compile AppleScript: %s: %v", strings.TrimSpace(string(output)), err)
			}
		})
	}
}

func TestResumeTerminalScriptsCompile(t *testing.T) {
	for name, script := range resumeTerminalScripts {
		t.Run(name, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "resume.scpt")
			if output, err := exec.Command("osacompile", "-o", outputPath, "-e", script).CombinedOutput(); err != nil {
				t.Fatalf("compile AppleScript: %s: %v", strings.TrimSpace(string(output)), err)
			}
		})
	}
}
