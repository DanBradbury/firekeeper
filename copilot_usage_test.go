package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFetchCopilotUsageFromLocalStore(t *testing.T) {
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 unavailable")
	}
	home := t.TempDir()
	path := filepath.Join(home, "session-store.db")
	schema := `
		CREATE TABLE sessions (id TEXT PRIMARY KEY);
		CREATE TABLE assistant_usage_events (
			id INTEGER PRIMARY KEY,
			session_id TEXT,
			model TEXT,
			input_tokens INTEGER,
			output_tokens INTEGER,
			request_multiplier REAL,
			initiator TEXT,
			created_at TEXT
		);
		INSERT INTO sessions VALUES ('session-1'), ('session-2');
		INSERT INTO assistant_usage_events VALUES
			(1, 'session-1', 'claude-sonnet', 100, 20, 1.0, 'user', '2026-07-31T10:00:00Z'),
			(2, 'session-1', 'claude-sonnet', 200, 30, 1.0, 'agent', '2026-07-31T10:01:00Z'),
			(3, 'session-2', 'gpt-5', 50, 10, 0.0, 'compaction', '2026-08-01T10:00:00Z'),
			(4, 'session-2', 'gpt-5', 400, 40, 1.5, 'user', '2026-08-01T10:01:00Z');
	`
	if output, err := exec.Command(sqlite, path, schema).CombinedOutput(); err != nil {
		t.Fatalf("create Copilot usage fixture: %v: %s", err, output)
	}
	t.Setenv("COPILOT_HOME", home)

	snapshot, err := fetchCopilotUsage()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Summary.Sessions != 2 || snapshot.Summary.ModelCalls != 4 {
		t.Fatalf("sessions/calls = %d/%d", snapshot.Summary.Sessions, snapshot.Summary.ModelCalls)
	}
	if snapshot.Summary.LifetimeTokens != 850 || snapshot.Summary.InputTokens != 750 || snapshot.Summary.OutputTokens != 100 {
		t.Fatalf("token summary = %#v", snapshot.Summary)
	}
	if snapshot.Summary.UserRequests != 2.5 {
		t.Fatalf("user request equivalents = %v", snapshot.Summary.UserRequests)
	}
	if len(snapshot.Daily) != 2 || len(snapshot.DailyByModel) != 2 || len(snapshot.Models) != 2 {
		t.Fatalf("daily/daily-model/models = %d/%d/%d", len(snapshot.Daily), len(snapshot.DailyByModel), len(snapshot.Models))
	}
	if snapshot.Models[0].Model != "gpt-5" || snapshot.Models[0].Tokens != 500 {
		t.Fatalf("top model = %#v", snapshot.Models[0])
	}
}

func TestCopilotUsageViewShowsLocalHistory(t *testing.T) {
	m := testModel()
	m.activeTab = usageTab
	m.usageProvider = copilotProvider
	m.width = 100
	m.height = 24
	m.copilotUsageRefreshedAt = time.Now()
	m.copilotUsage = copilotUsageSnapshot{
		Summary: copilotUsageSummary{
			Sessions:       12,
			ModelCalls:     42,
			LifetimeTokens: 12_500_000,
			UserRequests:   7.5,
		},
		Plan: copilotPlanDetails{
			UsedCredits:  0,
			TotalCredits: 7000,
			Remaining:    7000,
			Available:    true,
		},
		Daily: []copilotDailyUsage{{
			StartDate:    time.Now().Format("2006-01-02"),
			Tokens:       2_300_000,
			UserRequests: 2,
		}},
		DailyByModel: []copilotDailyModelUsage{
			{StartDate: time.Now().Format("2006-01-02"), Model: "claude-sonnet-4.6", Tokens: 2_000_000},
			{StartDate: time.Now().Format("2006-01-02"), Model: "gpt-5", Tokens: 300_000},
		},
		Models: []copilotUsageModel{
			{Model: "claude-sonnet-4.6", Tokens: 12_500_000, ModelCalls: 42, UserRequests: 7.5},
			{Model: "gpt-5", Tokens: 800_000, ModelCalls: 3},
		},
	}

	view := m.View()
	for _, expected := range []string{
		"[ Usage ]", "[ COPILOT ]", "GitHub Copilot", "LOCAL CLI",
		"12 sessions", "42 model calls", "7.5 weighted requests",
		"AI Credits", "0 / 7,000 AIC", "TOKENS BY DAY · MODEL", "2.3M", "TOKENS BY MODEL",
		"CLAUDE SONNET 4.6", "12.5M", "GPT 5", "800.0K",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("Copilot usage view missing %q", expected)
		}
	}
	colors := modelUsageColorAssignments([]string{"claude-sonnet-4.6", "gpt-5"})
	for _, color := range colors {
		if !strings.Contains(view, "38;2;"+color) {
			t.Fatalf("Copilot model color %s missing", color)
		}
	}
}

func TestReadCopilotHistoricalSessionsFromLocalStore(t *testing.T) {
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 unavailable")
	}
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	path := filepath.Join(home, "session-store.db")
	schema := `
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			cwd TEXT,
			repository TEXT,
			host_type TEXT,
			branch TEXT,
			summary TEXT,
			created_at TEXT,
			updated_at TEXT
		);
		CREATE TABLE assistant_usage_events (
			id INTEGER PRIMARY KEY,
			session_id TEXT,
			model TEXT,
			input_tokens INTEGER,
			output_tokens INTEGER,
			request_multiplier REAL,
			initiator TEXT,
			created_at TEXT
		);
		INSERT INTO sessions VALUES
			('older', '/work/older', 'owner/older', 'github', 'main', 'Older session', '2026-08-10T10:00:00Z', '2026-08-10T11:00:00Z'),
			('newer', '/work/newer', 'owner/newer', 'github', 'feature', 'Newer session', '2026-08-11T10:00:00Z', '2026-08-11T11:00:00Z');
		INSERT INTO assistant_usage_events VALUES
			(1, 'older', 'claude-sonnet', 100, 20, 1.0, 'user', '2026-08-10T10:30:00Z'),
			(2, 'newer', 'gpt-5', 200, 30, 0.0, 'agent', '2026-08-11T10:30:00Z'),
			(3, 'newer', 'claude-opus', 300, 40, 1.5, 'user', '2026-08-11T12:00:00Z');
	`
	if output, err := exec.Command(sqlite, path, schema).CombinedOutput(); err != nil {
		t.Fatalf("create Copilot history fixture: %v: %s", err, output)
	}

	sessions, err := readCopilotHistoricalSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0].ID != "newer" || sessions[1].ID != "older" {
		t.Fatalf("sessions = %#v", sessions)
	}
	newer := sessions[0]
	if newer.Title != "Newer session" || newer.Model != "claude-opus" || newer.LifetimeTokens != 570 {
		t.Fatalf("newest session = %#v", newer)
	}
	if newer.InputTokens != 500 || newer.OutputTokens != 70 || newer.ModelCalls != 2 || newer.UserRequests != 1.5 {
		t.Fatalf("newest usage = %#v", newer)
	}
	if newer.Repository != "owner/newer" || newer.GitBranch != "feature" || newer.UpdatedAt.Format(time.RFC3339) != "2026-08-11T12:00:00Z" {
		t.Fatalf("newest metadata = %#v", newer)
	}
}

func TestDecodeCopilotHistoricalSessionsSanitizesPartialRows(t *testing.T) {
	sessions, err := decodeCopilotHistoricalSessions([]byte(`[{"id":"session-1","summary":"Fix\u001b[31m tests","cwd":"/work/firekeeper","updated_at":"2026-08-12T10:00:00Z"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || strings.Contains(sessions[0].Title, "\x1b") || sessions[0].UpdatedAt.IsZero() {
		t.Fatalf("sessions = %#v", sessions)
	}
	if _, err := decodeCopilotHistoricalSessions([]byte(`{malformed`)); err == nil {
		t.Fatal("malformed history JSON was accepted")
	}
}

func TestCopilotUsageEnterBrowsesHistoricalSessions(t *testing.T) {
	m := testModel()
	m.activeTab = usageTab
	m.usageProvider = copilotProvider
	m.width = 100
	m.copilotUsage.History = []copilotHistoricalSession{
		{ID: "one", Title: "First session", Model: "gpt-5", WorkDir: "/work/one"},
		{ID: "two", Title: "Second session", Model: "claude-opus", WorkDir: "/work/two"},
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if !m.copilotHistoryOpen {
		t.Fatal("Enter did not open Copilot history")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	if m.copilotHistoryCursor != 1 || !strings.Contains(m.viewCopilotHistory(7), "Second session") {
		t.Fatalf("history cursor/view = %d\n%s", m.copilotHistoryCursor, m.viewCopilotHistory(7))
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.copilotHistoryOpen {
		t.Fatal("Esc did not close Copilot history")
	}
}

func TestCopilotUsageEnterShowsHistoryError(t *testing.T) {
	m := testModel()
	m.activeTab = usageTab
	m.usageProvider = copilotProvider
	m.width = 100
	m.copilotHistoryErr = "session store unavailable"

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if !m.copilotHistoryOpen {
		t.Fatal("Enter did not open empty Copilot history")
	}
	if view := m.viewCopilotHistory(8); !strings.Contains(view, "Session history unavailable") {
		t.Fatalf("empty history did not explain failure:\n%s", view)
	}
}

func TestCopilotHistoryRefreshFailureRetainsLastGoodSessions(t *testing.T) {
	m := testModel()
	m.copilotUsage.History = []copilotHistoricalSession{{ID: "known"}}
	updated, _ := m.Update(copilotUsageResultMsg{
		snapshot:   copilotUsageSnapshot{Summary: copilotUsageSummary{Sessions: 1}},
		refreshed:  time.Now(),
		historyErr: errTestCodexUsage,
	})
	m = updated.(model)
	if len(m.copilotUsage.History) != 1 || m.copilotUsage.History[0].ID != "known" {
		t.Fatal("failed history refresh discarded last good sessions")
	}
	if m.copilotHistoryErr == "" {
		t.Fatal("failed history refresh did not expose error")
	}
}

func TestDecodeCopilotPlan(t *testing.T) {
	plan, err := decodeCopilotPlan([]byte(`{
		"copilot_plan": "pro",
		"quota_reset_date_utc": "2026-09-01T00:00:00Z",
		"quota_snapshots": {
			"premium_interactions": {"entitlement": 7000, "remaining": 6500}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Name != "pro" || plan.UsedCredits != 500 || plan.TotalCredits != 7000 || plan.Remaining != 6500 {
		t.Fatalf("plan = %#v", plan)
	}
	if plan.ResetAt.IsZero() || !plan.Available {
		t.Fatalf("reset/availability = %#v", plan)
	}
}

func TestDecodeCopilotPlanAcceptsAICreditsQuota(t *testing.T) {
	plan, err := decodeCopilotPlan([]byte(`{
		"plan": "pro+",
		"quota_snapshots": {"ai_credits": {"used": 12.5, "entitlement": 7000, "remaining": 6987.5}}
	}`))
	if err != nil || plan.Name != "pro+" || plan.UsedCredits != 12.5 {
		t.Fatalf("plan/error = %#v/%v", plan, err)
	}
}

func TestUsageArrowKeysSwitchProviders(t *testing.T) {
	m := testModel()
	m.activeTab = usageTab
	m.usageProvider = codexProvider
	m.codexUsageRefreshedAt = time.Now()

	updated, command := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	if m.usageProvider != copilotProvider {
		t.Fatalf("usage provider = %d, want Copilot", m.usageProvider)
	}
	if command == nil || !m.copilotUsageLoading {
		t.Fatal("switching to unloaded Copilot usage did not start refresh")
	}

	m.copilotUsageRefreshedAt = time.Now()
	updated, command = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(model)
	if m.usageProvider != codexProvider {
		t.Fatalf("usage provider = %d, want Codex", m.usageProvider)
	}
	if command != nil {
		t.Fatal("switching to loaded Codex usage refreshed unexpectedly")
	}
}

func TestCopilotUsageErrorRetainsLastGoodSnapshot(t *testing.T) {
	m := testModel()
	m.copilotUsage = copilotUsageSnapshot{Summary: copilotUsageSummary{LifetimeTokens: 99}}

	updated, _ := m.Update(copilotUsageResultMsg{err: errTestCodexUsage, refreshed: time.Now()})
	m = updated.(model)
	if m.copilotUsage.Summary.LifetimeTokens != 99 {
		t.Fatal("failed refresh discarded last good Copilot usage snapshot")
	}
	if m.copilotUsageErr == "" {
		t.Fatal("failed refresh did not expose Copilot error")
	}
}
