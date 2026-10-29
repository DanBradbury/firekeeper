package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func writeClaudeTranscript(t *testing.T, root, project, id string, modified time.Time, lines ...string) {
	t.Helper()
	path := filepath.Join(root, "projects", project, id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func TestFetchClaudeUsageCountsEachMessageOnce(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	usage := `"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}`
	writeClaudeTranscript(t, root, "-work-repo", "11111111-1111-1111-1111-111111111111", time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC),
		`{"type":"user","timestamp":"2026-10-01T10:00:00Z","cwd":"/work/repo","gitBranch":"main","message":{"role":"user","content":"secret prompt"}}`,
		// One response written as two content block lines with the same usage.
		`{"type":"assistant","timestamp":"2026-10-01T10:00:01Z","message":{"id":"msg_a","model":"claude-opus-5-5",`+usage+`,"content":[{"type":"thinking"}]}}`,
		`{"type":"assistant","timestamp":"2026-10-01T10:00:01Z","message":{"id":"msg_a","model":"claude-opus-5-5",`+usage+`,"content":[{"type":"text"}]}}`,
		`{"type":"assistant","timestamp":"2026-10-01T10:00:02Z","message":{"id":"msg_b","model":"claude-sonnet-5-5","usage":{"input_tokens":1,"output_tokens":2},"content":[]}}`,
		`{"type":"assistant","timestamp":"2026-10-01T10:00:03Z","message":{"id":"msg_s","model":"<synthetic>","usage":{"input_tokens":999},"content":[]}}`,
		`{"type":"ai-title","aiTitle":"Fix the build"}`,
		"malformed",
		`{"type":"assistant","message":{"id":"msg_c","model":"claude-opus-5-5","usage":{"input_tokens":-4,"output_tokens":3}}}`,
	)
	// A later resumed session repeats msg_a; it must not count twice, and
	// stays credited to the session that produced it even though this file
	// sorts first by name.
	writeClaudeTranscript(t, root, "-work-other", "22222222-2222-2222-2222-222222222222", time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC),
		`{"type":"assistant","timestamp":"2026-10-02T09:00:00Z","cwd":"/work/other","message":{"id":"msg_a","model":"claude-opus-5-5",`+usage+`}}`,
	)

	snapshot, err := fetchClaudeUsage()
	if err != nil {
		t.Fatal(err)
	}
	s := snapshot.Summary
	if s.Sessions != 2 || s.Messages != 3 {
		t.Fatalf("sessions/messages = %d/%d, want 2/3", s.Sessions, s.Messages)
	}
	if s.InputTokens != 11 || s.OutputTokens != 10 || s.CacheRead != 100 || s.CacheCreation != 20 || s.LifetimeTokens != 141 {
		t.Fatalf("summary = %#v", s)
	}
	if len(snapshot.Models) != 2 || snapshot.Models[0].Model != "claude-opus-5-5" || snapshot.Models[0].Tokens != 138 || snapshot.Models[1].Tokens != 3 {
		t.Fatalf("models = %#v", snapshot.Models)
	}
	var first claudeHistoricalSession
	for _, session := range snapshot.History {
		if session.ID == "11111111-1111-1111-1111-111111111111" {
			first = session
		}
	}
	if first.Title != "Fix the build" || first.WorkDir != "/work/repo" || first.GitBranch != "main" || first.Model != "claude-opus-5-5" || first.LifetimeTokens != 141 || first.UndercountWarning != "" {
		t.Fatalf("session = %#v", first)
	}
	day := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC).Local().Format("2006-01-02")
	var dayTokens int64
	for _, bucket := range snapshot.Daily {
		if bucket.StartDate == day {
			dayTokens = bucket.Tokens
		}
	}
	if dayTokens != 138 {
		t.Fatalf("daily = %#v, want 138 tokens on %s", snapshot.Daily, day)
	}
}

func TestFetchClaudeUsageWarnsWhenSubagentsExist(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	const id = "11111111-1111-1111-1111-111111111111"
	writeClaudeTranscript(t, root, "-work-repo", id, time.Date(2026, 10, 29, 13, 0, 0, 0, time.UTC),
		`{"type":"assistant","timestamp":"2026-10-29T12:00:00Z","cwd":"/work/repo","message":{"id":"msg_main","model":"claude-test","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}}`,
	)
	sub := filepath.Join(root, "projects", "-work-repo", id, "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "agent-alpha.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := fetchClaudeUsage()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Summary.LifetimeTokens != 135 || len(snapshot.History) != 1 || snapshot.History[0].LifetimeTokens != 135 {
		t.Fatalf("measured totals changed: %#v", snapshot.Summary)
	}
	if snapshot.History[0].UndercountWarning != session.ClaudeUndercountWarning {
		t.Fatalf("warning = %q", snapshot.History[0].UndercountWarning)
	}
	m := NewModel()
	m.width, m.height = 160, 40
	m.usageProvider = claudeProvider
	m.claudeUsage = snapshot
	view := ansi.Strip(m.viewUsagePage(m.height - chromeRows))
	if !strings.Contains(view, "135") || !strings.Contains(view, "Has subagents:") {
		t.Fatalf("usage view missing the undercount warning:\n%s", view)
	}
}

func TestFetchClaudeUsageNeverExposesTranscriptText(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	writeClaudeTranscript(t, root, "-p", "33333333-3333-3333-3333-333333333333", time.Now(),
		`{"type":"user","cwd":"/p","message":{"role":"user","content":"do not show this prompt"}}`,
	)
	snapshot, err := fetchClaudeUsage()
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel()
	m.width = 120
	m.claudeUsage = snapshot
	m.usageProvider = claudeProvider
	if view := ansi.Strip(m.viewUsagePage(30)); strings.Contains(view, "do not show") || !strings.Contains(view, "Claude Code session") {
		t.Fatalf("history view:\n%s", view)
	}
}

func TestClaudeUsageMissingHistoryReturnsActionableError(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	_, err := fetchClaudeUsage()
	if err == nil || !strings.Contains(err.Error(), "find Claude Code session history") {
		t.Fatalf("error = %v", err)
	}
}

func TestClaudeUsageViewShowsLocalHistoryAndLimitation(t *testing.T) {
	m := NewModel()
	m.width = 110
	m.usageProvider = claudeProvider
	today := time.Now().Format("2006-01-02")
	m.claudeUsage = claudeUsageSnapshot{
		Summary: claudeUsageSummary{Sessions: 2, Messages: 4, LifetimeTokens: 1200, InputTokens: 900, CacheRead: 200, OutputTokens: 100},
		Daily:   []claudeDailyUsage{{StartDate: today, Tokens: 1200}},
		Models:  []claudeUsageModel{{Model: "claude-opus-5-5", Tokens: 1200}},
	}
	rawView := m.viewUsagePage(24)
	view := ansi.Strip(rawView)
	for _, expected := range []string{"╭─lifetime", "Responses", "1.2K", "Plan limits: Claude Code /usage", "╭─tokens by day", "Today", "CLAUDE OPUS"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("Claude usage view missing %q:\n%s", expected, view)
		}
	}
	assertModelBreakdownAboveDays(t, rawView)
}

func TestUsageProviderCyclesToClaudeAndLoads(t *testing.T) {
	m := NewModel()
	m.activeTab = usageTab
	m.usageProvider = kimiProvider
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	if m.usageProvider != claudeProvider || !m.claudeUsageLoading || cmd == nil {
		t.Fatalf("provider = %v loading = %v cmd nil = %v", m.usageProvider, m.claudeUsageLoading, cmd == nil)
	}
	if !strings.Contains(m.usageProviderSelectorLine(), "[ CLAUDE ]") {
		t.Fatalf("selector = %q", m.usageProviderSelectorLine())
	}
	updated, _ = m.Update(claudeUsageResultMsg{refreshed: time.Now(), snapshot: claudeUsageSnapshot{Summary: claudeUsageSummary{Sessions: 1}}})
	m = updated.(model)
	if m.claudeUsageLoading || m.claudeUsage.Summary.Sessions != 1 {
		t.Fatalf("result not applied: %#v", m.claudeUsage)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if updated.(model).usageProvider != codexProvider {
		t.Fatal("Right from Claude did not wrap to Codex")
	}
}

func TestClaudeUsageArrowsSelectAndEnterOpensChat(t *testing.T) {
	m := NewModel()
	m.activeTab = usageTab
	m.usageProvider = claudeProvider
	m.width, m.height = 120, 40
	m.claudeUsage.History = []claudeHistoricalSession{
		{ID: "11111111-1111-1111-1111-111111111111", Title: "one"},
		{ID: "22222222-2222-2222-2222-222222222222", Title: "two", GitBranch: "main"},
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	if m.claudeHistoryCursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.claudeHistoryCursor)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if cmd == nil || !m.chat.open || m.chat.meta.ID != "22222222-2222-2222-2222-222222222222" || m.chat.meta.Provider != "claude" || m.chat.tool != "Claude" {
		t.Fatalf("chat = %+v", m.chat)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.chat.open || m.activeTab != usageTab {
		t.Fatal("Esc should return to the Usage tab")
	}
}

func TestUsageViewIsCardsAndWidthSafe(t *testing.T) {
	for _, provider := range []usageProvider{codexProvider, copilotProvider, kimiProvider, claudeProvider} {
		m := model{width: 80, height: 24, usageProvider: provider}
		lines := strings.Split(m.viewUsage(20), "\n")
		if len(lines) != 20 {
			t.Fatalf("%v: rows = %d, want 20", provider, len(lines))
		}
		for _, line := range lines {
			if got := ansi.StringWidth(line); got != 80 {
				t.Fatalf("%v: line width = %d, want 80", provider, got)
			}
		}
		wantTitle := map[usageProvider]string{codexProvider: "limits", copilotProvider: "plan", kimiProvider: "lifetime", claudeProvider: "lifetime"}[provider]
		if !strings.Contains(ansi.Strip(lines[1]), "╭─"+wantTitle) {
			t.Fatalf("%v: missing panel border: %q", provider, ansi.Strip(lines[1]))
		}
	}
}
