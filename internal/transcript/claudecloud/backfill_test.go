package claudecloud

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/testenv/fakeapi"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

const testMachine = "0199ffff-0000-7000-8000-000000000001"

// TestBackfillUploadsCloudSessions runs the cached cloud sessions through the
// real reporter: they upload under their cloud ids as provider claude, and a
// second pass, including after the session grew, sends only what is new.
func TestBackfillUploadsCloudSessions(t *testing.T) {
	f, c := newFakeCloud(t)
	f.sessions = []map[string]any{cloudSession(sid, "2026-10-01T11:00:00Z")}
	f.events[sid] = conversation(sid)

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".firekeeper"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".firekeeper", "machine-id"), []byte(testMachine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".firekeeper", "cloud", "claude")
	syncOnce(t, c, dir, time.Time{})

	api := fakeapi.New()
	dashboard := httptest.NewServer(api.Handler())
	defer dashboard.Close()
	src := &Source{Dir: dir}
	only := func(p transcript.Provider) bool { return p == transcript.ProviderClaude }
	pass := func() reporter.BackfillResult {
		t.Helper()
		result, err := reporter.Backfill(context.Background(), reporter.BackfillConfig{
			Config: reporter.Config{
				Server:    dashboard.URL,
				Providers: []transcript.Provider{transcript.ProviderClaude},
				Home:      home,
				Discover:  func(context.Context, session.Options) ([]session.Meta, error) { return nil, nil },
				Sources:   func(p transcript.Provider) (reader.Source, bool) { return src, only(p) },
			},
			Enumerators: func(p transcript.Provider) (reader.Enumerator, bool) { return src, only(p) },
			Sleep:       func(context.Context, time.Duration) error { return nil },
		})
		if err != nil || result.Failed() {
			t.Fatalf("backfill: %v, %+v", err, result.Sessions)
		}
		return result
	}

	first := pass()
	if len(first.Sessions) != 1 || first.Sessions[0].Events != 5 {
		t.Fatalf("first pass = %+v", first.Sessions)
	}
	if got := api.Events(testMachine, sid); len(got) != 5 || got[0].Provider != transcript.ProviderClaude {
		t.Fatalf("server holds %d events", len(got))
	}
	stored, ok := api.Session(testMachine, sid)
	if !ok || stored.Provider != "claude" || stored.Project != "demo" || stored.Model != "claude-sonnet-5-5" || stored.Title != "Fix the thing" || stored.Branch != "feature/x" || stored.CWD != "/claude-cloud/octo/demo" || stored.Origin != "cloud" {
		t.Fatalf("stored session = %+v", stored)
	}

	if again := pass(); len(again.Sessions) != 0 {
		t.Fatalf("second pass uploaded %d sessions, want none", len(again.Sessions))
	}

	// The session continues in the cloud; only the new event uploads.
	f.sessions = []map[string]any{cloudSession(sid, "2026-10-01T12:00:00Z")}
	f.events[sid] = append(f.events[sid], map[string]any{"type": "assistant", "uuid": "e8", "session_id": sid,
		"created_at": "2026-10-01T11:30:00.000000Z", "timestamp": "2026-10-01T11:30:00.500Z",
		"message": map[string]any{"id": "m4", "role": "assistant", "model": "claude-sonnet-5-5", "content": []map[string]any{{"type": "text", "text": "more"}}}})
	syncOnce(t, c, dir, time.Time{})
	third := pass()
	if len(third.Sessions) != 1 || third.Sessions[0].Events != 1 {
		t.Fatalf("third pass = %+v, want one new event", third.Sessions)
	}
	if got := api.Events(testMachine, sid); len(got) != 6 {
		t.Fatalf("server holds %d events, want 6", len(got))
	}
}
