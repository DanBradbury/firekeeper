package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// The test executable doubles as a Codex app-server, including on Windows
// where a shell-script stub would not be an executable.
func runCodexUsageHelper() {
	mode := os.Getenv("FIREKEEPER_TEST_CODEX_USAGE")
	if mode == "" || len(os.Args) != 4 || os.Args[1] != "app-server" ||
		os.Args[2] != "--listen" || os.Args[3] != "stdio://" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		switch request.Method {
		case "initialize":
			fmt.Println(`{"id":0,"result":{}}`)
		case "initialized":
		case "account/rateLimits/read":
			fmt.Println(`{"id":1,"result":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":25,"windowDurationMins":300}}}}`)
		case "account/usage/read":
			if mode == "timeout" {
				time.Sleep(2 * codexUsageTimeout)
				os.Exit(3)
			}
			fmt.Println(`{"id":2,"result":{"summary":{"lifetimeTokens":1234},"dailyUsageBuckets":[{"startDate":"2026-11-02","tokens":1234}]}}`)
			os.Exit(0)
		default:
			os.Exit(2)
		}
	}
	os.Exit(2)
}

func TestCodexUsageExecutableWithoutShell(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "Codex bin #1")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	name := "codex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), content, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("CODEX_HOME", t.TempDir())
	for _, mode := range []string{"success", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("FIREKEEPER_TEST_CODEX_USAGE", mode)
			start := time.Now()
			snapshot, err := fetchCodexUsage()
			if mode == "timeout" {
				if err == nil || err.Error() != "Codex usage request timed out" {
					t.Fatalf("stalled app-server error = %v", err)
				}
				if elapsed := time.Since(start); elapsed > codexUsageTimeout+5*time.Second {
					t.Fatalf("stalled app-server was not bounded: %v", elapsed)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Summary.LifetimeTokens == nil || *snapshot.Summary.LifetimeTokens != 1234 ||
				len(snapshot.Daily) != 1 || snapshot.Daily[0].Tokens != 1234 {
				t.Fatalf("usage response = %+v", snapshot)
			}
			limit := snapshot.RateLimits["codex"]
			if limit.Primary == nil || limit.Primary.UsedPercent != 25 {
				t.Fatalf("rate limit = %+v", limit)
			}
		})
	}
}

func TestUsageProviderFailuresRemainIndependent(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("COPILOT_HOME", t.TempDir())
	t.Setenv("KIMI_CODE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	// A store without the usage table reproduces SQLITE_ERROR, without
	// guessing which schema change caused a real user's failure.
	writeUsageDatabase(t, filepath.Join(os.Getenv("COPILOT_HOME"), "session-store.db"),
		`CREATE TABLE sessions (id TEXT PRIMARY KEY);`)
	writeClaudeTranscript(t, os.Getenv("CLAUDE_CONFIG_DIR"), "windows-project",
		"11111111-1111-1111-1111-111111111111", time.Now(),
		`{"type":"assistant","timestamp":"2026-11-02T10:00:00Z","cwd":"C:\\work\\project","message":{"id":"msg_test","model":"claude-test","usage":{"input_tokens":1200,"output_tokens":34}}}`)

	m := NewModel()
	m.width, m.height = 108, 40
	m.activeTab = overviewTab
	m.codexUsageLoading, m.copilotUsageLoading = true, true
	m.kimiUsageLoading, m.claudeUsageLoading = true, true
	messages := []tea.Msg{
		codexUsageResultMsg{err: fmt.Errorf("Codex usage request timed out"), refreshed: time.Now()},
		refreshCopilotUsage()(),
		refreshKimiUsage()(),
		refreshClaudeUsage()(),
	}
	for _, msg := range messages {
		updated, _ := m.Update(msg)
		m = updated.(model)
	}
	if m.codexUsageLoading || m.copilotUsageLoading || m.kimiUsageLoading || m.claudeUsageLoading {
		t.Fatal("provider refresh left a loading flag set")
	}
	if !strings.Contains(m.copilotUsageErr, "SQLITE_ERROR") ||
		!strings.Contains(m.kimiUsageErr, "find Kimi session history") ||
		m.claudeUsageErr != "" || m.claudeUsage.Summary.LifetimeTokens != 1234 {
		t.Fatalf("provider states: Copilot %q, Kimi %q, Claude %q (%d tokens)",
			m.copilotUsageErr, m.kimiUsageErr, m.claudeUsageErr, m.claudeUsage.Summary.LifetimeTokens)
	}
	// Providers that failed before producing data are left out; Claude,
	// which has data, is still shown.
	failures := []string{"Codex usage request timed out", "SQLITE_ERROR", "find Kimi session history"}
	view := ansi.Strip(m.View())
	for _, want := range []string{"Claude", "1.2K"} {
		if !strings.Contains(view, want) {
			t.Errorf("overview missing %q", want)
		}
	}
	for _, unwanted := range failures {
		if strings.Contains(view, unwanted) {
			t.Errorf("overview shows failed provider %q", unwanted)
		}
	}
	m.activeTab = usageTab
	if m.usageProvider != claudeProvider {
		t.Errorf("Usage selection = %d, want Claude, the only provider with data", m.usageProvider)
	}
	view = ansi.Strip(m.View())
	selector := strings.Split(view, "\n")[1]
	if !strings.Contains(selector, "[ CLAUDE ]") || strings.Contains(selector, "CODEX") || strings.Contains(selector, "COPILOT") || strings.Contains(selector, "KIMI") {
		t.Errorf("selector lists unavailable providers: %q", selector)
	}
	if !strings.Contains(view, "1.2K") {
		t.Error("Usage tab missing Claude data")
	}
	for _, unwanted := range failures {
		if strings.Contains(view, unwanted) {
			t.Errorf("Usage tab shows failed provider %q", unwanted)
		}
	}
	for _, tab := range []tab{overviewTab, usageTab} {
		m.activeTab = tab
		for _, provider := range []usageProvider{codexProvider, copilotProvider, kimiProvider, claudeProvider} {
			m.usageProvider = provider
			for _, size := range [][2]int{{40, 12}, {80, 24}, {108, 30}} {
				m.width, m.height = size[0], size[1]
				lines := strings.Split(m.View(), "\n")
				if len(lines) != m.height {
					t.Fatalf("tab %d provider %d size %v: got %d rows", tab, provider, size, len(lines))
				}
				for i, line := range lines {
					if width := ansi.StringWidth(line); width != m.width {
						t.Fatalf("tab %d provider %d size %v row %d: width %d", tab, provider, size, i, width)
					}
				}
			}
		}
	}
}
