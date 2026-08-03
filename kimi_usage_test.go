package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFetchKimiUsageAggregatesLocalWireEvents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", home)
	wire := filepath.Join(home, "sessions", "workspace", "session_123", "agents", "main", "wire.jsonl")
	if err := os.MkdirAll(filepath.Dir(wire), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := strings.Join([]string{
		`{"type":"usage.record","model":"kimi-k3","time":1784917338655,"usage":{"inputOther":10,"inputCacheRead":20,"inputCacheCreation":3,"output":7}}`,
		`{"type":"usage.record","model":"kimi-k3","time":1784917348986,"usage":{"inputOther":5,"inputCacheRead":0,"inputCacheCreation":0,"output":2}}`,
		`{"type":"not-a-usage-record","usage":{"output":999}}`,
		"malformed",
	}, "\n")
	if err := os.WriteFile(wire, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := fetchKimiUsage()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Summary.Sessions != 1 || snapshot.Summary.Turns != 2 {
		t.Fatalf("summary sessions/turns = %#v", snapshot.Summary)
	}
	if snapshot.Summary.InputTokens != 15 || snapshot.Summary.CacheRead != 20 || snapshot.Summary.CacheCreation != 3 || snapshot.Summary.OutputTokens != 9 || snapshot.Summary.LifetimeTokens != 47 {
		t.Fatalf("summary token totals = %#v", snapshot.Summary)
	}
	if len(snapshot.Models) != 1 || snapshot.Models[0].Model != "kimi-k3" || snapshot.Models[0].Tokens != 47 {
		t.Fatalf("models = %#v", snapshot.Models)
	}
}

func TestKimiUsageMissingHistoryReturnsActionableError(t *testing.T) {
	t.Setenv("KIMI_CODE_HOME", t.TempDir())
	_, err := fetchKimiUsage()
	if err == nil || !strings.Contains(err.Error(), "find Kimi session history") {
		t.Fatalf("error = %v", err)
	}
}

func TestKimiUsageViewShowsLocalHistoryAndQuotaLimitation(t *testing.T) {
	m := newModel(nil)
	m.width = 100
	today := time.Now().Format("2006-01-02")
	m.kimiUsage = kimiUsageSnapshot{Summary: kimiUsageSummary{Sessions: 2, Turns: 4, LifetimeTokens: 1200, InputTokens: 900, CacheRead: 200, OutputTokens: 100}, Daily: []kimiDailyUsage{{StartDate: today, Tokens: 1200}}}
	view := m.viewKimiUsage(20)
	for _, expected := range []string{"Kimi Code", "2 sessions", "1.2K tokens", "Quota and reset", "TOKENS BY DAY", "Today"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("Kimi usage view missing %q:\n%s", expected, view)
		}
	}
}

func TestRecentKimiDailyUsageFillsMissingDays(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	days := recentKimiDailyUsage([]kimiDailyUsage{{StartDate: "2026-08-02", Tokens: 42}}, now, 3)
	if len(days) != 3 || days[0].StartDate != "2026-07-31" || days[1].Tokens != 0 || days[2].Tokens != 42 {
		t.Fatalf("days = %#v", days)
	}
}
