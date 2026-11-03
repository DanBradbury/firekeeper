package tui

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/claudelimits"
)

// Claude limits are on by default, so no test may reach the real config,
// Keychain or network through refreshClaudeUsage.
func TestMain(m *testing.M) {
	runCodexUsageHelper()
	runShareServerHelper()
	// Existing layout tests assert plain names; icon tests set their own mode.
	activeIconMode = iconsOff
	fontInstalled = func() bool { return false }
	recentSessionsSource = func(context.Context, time.Time) []recentSession { return nil }
	claudeLimitsSource = func() (claudelimits.Limits, bool, error) {
		return claudelimits.Limits{}, false, nil
	}
	iconSetupSource = func(context.Context) string { return "" }
	claudeCloudStatusSource = func() string { return "" }
	exportLookup = noServerLookup
	openLocalFile = func(string) error { panic("a test opened a file without replacing openLocalFile") }
	exportHome = func() (string, error) { return "", errors.New("test did not set exportHome") }
	os.Exit(m.Run())
}
