package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// TestLoadRecentSessionsReadsProviderTranscripts runs the real Claude
// enumerator against a synthetic home and checks the list entry.
func TestLoadRecentSessionsReadsProviderTranscripts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{"CODEX_HOME", "COPILOT_HOME", "KIMI_CODE_HOME", "OPENCODE_DB", "XDG_DATA_HOME", "GEMINI_CLI_HOME", "CURSOR_CONFIG_DIR"} {
		t.Setenv(name, filepath.Join(home, "none-"+name))
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	project := filepath.Join(home, "repo")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	head, _ := json.Marshal(map[string]any{"cwd": project, "timestamp": "2026-01-01T00:00:00Z"})
	dir := filepath.Join(home, ".claude", "projects", "-encoded")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const id = "0199c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d"
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, append(head, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	recent := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, recent, recent); err != nil {
		t.Fatal(err)
	}

	got := loadRecentSessions(context.Background(), time.Now())
	for _, s := range got {
		if s.provider == transcript.ProviderClaude && s.id == id {
			if s.tool != "Claude" || s.project != "repo" || s.path != path || s.last.IsZero() {
				t.Fatalf("recent session fields wrong: %+v", s)
			}
			return
		}
	}
	t.Fatalf("claude session missing from %d results", len(got))
}

func TestBuildRecentReportJSON(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	last := now.Add(-3 * 24 * time.Hour)
	report := buildRecentReport(now, []recentSession{{
		provider: transcript.ProviderClaude,
		id:       "abc-123",
		project:  "firekeeper",
		title:    "",
		cwd:      "/tmp/firekeeper",
		last:     last,
	}})
	if report.GeneratedAt != now || len(report.Sessions) != 1 {
		t.Fatalf("report = %+v", report)
	}
	row := report.Sessions[0]
	if row.Provider != "claude" || row.SessionID != "abc-123" || row.Title == "" || row.LastActivityAt == nil || !row.LastActivityAt.Equal(last) {
		t.Fatalf("row = %+v", row)
	}
}

func TestRunRecentRequiresJSON(t *testing.T) {
	var out, errb bytes.Buffer
	if code := RunRecent(nil, &out, &errb); code != 2 || !strings.Contains(errb.String(), "only --json") {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
	previousSource := recentSessionsSource
	recentSessionsSource = func(context.Context, time.Time) []recentSession {
		return []recentSession{{provider: transcript.ProviderCodex, id: "x", title: "Hello", last: time.Now()}}
	}
	t.Cleanup(func() {
		recentSessionsSource = previousSource
	})
	out.Reset()
	errb.Reset()
	if code := RunRecent([]string{"--json"}, &out, &errb); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
	var report RecentReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if len(report.Sessions) != 1 || report.Sessions[0].Title != "Hello" {
		t.Fatalf("sessions = %+v", report.Sessions)
	}
}
