package tui

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/claudelimits"
)

// Claude limits are on by default, so no test may reach the real config,
// Keychain or network through refreshClaudeUsage.
func TestMain(m *testing.M) {
	recentSessionsSource = func(context.Context, time.Time) []recentSession { return nil }
	claudeLimitsSource = func() (claudelimits.Limits, bool, error) {
		return claudelimits.Limits{}, false, nil
	}
	claudeCloudStatusSource = func() string { return "" }
	os.Exit(m.Run())
}
