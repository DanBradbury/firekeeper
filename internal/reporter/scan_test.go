package reporter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// scanConfig is a RunOnce over f that scans its transcripts, with live as
// the sessions discovery sees running.
func (f *historyFixture) scanConfig(server string, live ...session.Meta) (Config, *fakeEnumerator) {
	bf := f.config(server)
	cfg := f.runOnceConfig(server, live...)
	cfg.ScanEnded = true
	enumerator, _ := bf.Enumerators(transcript.ProviderCodex)
	fake := enumerator.(*fakeEnumerator)
	cfg.ScanEnumerators = func(p transcript.Provider) (reader.Enumerator, bool) {
		if p == transcript.ProviderCodex {
			return fake, true
		}
		return nil, false
	}
	return cfg, fake
}

func pass(t *testing.T, cfg Config) Summary {
	t.Helper()
	summary, err := RunOnce(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if summary.Failed() {
		for _, s := range summary.Sessions {
			if s.Err != nil {
				t.Fatalf("session %s failed: %v", s.SessionID, s.Err)
			}
		}
	}
	return summary
}

func appendLines(t *testing.T, path string, from, to int) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for i := from; i < to; i++ {
		if _, err := file.WriteString(line(i, "")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanUploadsEndedSessionOnce(t *testing.T) {
	f := newHistoryFixture(t, 3)
	ts := newTestServer(t)
	cfg, _ := f.scanConfig(ts.URL)
	id := f.metas[0].ID

	got := pass(t, cfg)
	if len(got.Sessions) != 1 || got.Sessions[0].SessionID != id || got.Sessions[0].Events != 3 {
		t.Fatalf("first pass sessions = %+v", got.Sessions)
	}
	if got.Sessions[0].State != "ENDED" {
		t.Fatalf("state = %q, want ENDED", got.Sessions[0].State)
	}
	if want := (ScanSummary{Ran: true, Found: 1, Read: 1, Complete: true}); got.Scan != want {
		t.Fatalf("scan = %+v, want %+v", got.Scan, want)
	}
	if got.MachineID != testMachine {
		t.Fatalf("machine id = %q", got.MachineID)
	}
	assertSeqs(t, ts.storedFor(t, id), 3)
	if state := ts.sessionState(t, id); state != "ENDED" {
		t.Fatalf("server state = %q, want ENDED", state)
	}

	requests := ts.requestCount()
	again := pass(t, cfg)
	if len(again.Sessions) != 0 || again.Scan.Found != 0 || !again.Scan.Complete {
		t.Fatalf("second pass = %+v", again)
	}
	if ts.requestCount() != requests {
		t.Fatalf("second pass sent %d requests", ts.requestCount()-requests)
	}
}

func TestScanAppendsAndTruncation(t *testing.T) {
	f := newHistoryFixture(t, 3)
	ts := newTestServer(t)
	cfg, _ := f.scanConfig(ts.URL)
	meta := f.metas[0]
	pass(t, cfg)

	appendLines(t, meta.RolloutPath, 3, 8)
	got := pass(t, cfg)
	if len(got.Sessions) != 1 || got.Sessions[0].Events != 5 || got.Sessions[0].Duplicates != 0 {
		t.Fatalf("after append = %+v", got.Sessions)
	}
	assertSeqs(t, ts.storedFor(t, meta.ID), 8)

	// Rotation: the file starts over shorter than before. It is read from
	// the start, and the server drops what it already holds.
	var b strings.Builder
	for i := 0; i < 2; i++ {
		b.WriteString(line(i, ""))
	}
	if err := os.WriteFile(meta.RolloutPath, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got = pass(t, cfg)
	if len(got.Sessions) != 1 || got.Sessions[0].Events != 2 || got.Sessions[0].Duplicates != 2 {
		t.Fatalf("after truncation = %+v", got.Sessions)
	}
	assertSeqs(t, ts.storedFor(t, meta.ID), 8)
	if again := pass(t, cfg); len(again.Sessions) != 0 {
		t.Fatalf("pass after truncation re-read = %+v", again.Sessions)
	}
}

func TestScanSkipsRunningSessions(t *testing.T) {
	tests := []struct {
		name string
		id   func(string) string
	}{
		{"same id", func(id string) string { return id }},
		{"id in another case", strings.ToUpper},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHistoryFixture(t, 4)
			ts := newTestServer(t)
			live := f.metas[0]
			live.ID = tt.id(live.ID)
			live.State = session.SessionStateWaiting
			cfg, _ := f.scanConfig(ts.URL, live)

			got := pass(t, cfg)
			if len(got.Sessions) != 1 || got.Sessions[0].State != "WAITING" || got.Sessions[0].Events != 4 {
				t.Fatalf("sessions = %+v", got.Sessions)
			}
			if got.Scan.Found != 0 || got.Scan.Read != 0 {
				t.Fatalf("scan read the running session again: %+v", got.Scan)
			}
			if n := ts.requestCount(); n != 1 {
				t.Fatalf("requests = %d, want 1", n)
			}
		})
	}
}

// scanGuard fails the test when it locates or reads a forbidden session.
type scanGuard struct {
	reader.Source
	t         *testing.T
	forbidden map[string]bool // session ids and transcript paths
}

func (g scanGuard) Locate(meta session.Meta) ([]string, error) {
	if g.forbidden[meta.ID] || g.forbidden["*"] {
		g.t.Errorf("located the transcript of session %s, which must not be opened", meta.ID)
	}
	return g.Source.Locate(meta)
}

func (g scanGuard) Read(path string, from int64) ([]transcript.Event, int64, error) {
	if g.forbidden[path] || g.forbidden["*"] {
		g.t.Errorf("read a transcript that must not be opened")
	}
	return g.Source.Read(path, from)
}

type scanGuardEnumerator struct{ t *testing.T }

func (g scanGuardEnumerator) Enumerate(context.Context, reader.EnumerateOptions) ([]session.Meta, error) {
	g.t.Errorf("enumerated a provider that is not allowlisted")
	return nil, nil
}

func TestScanNeverOpensDisallowedOrExcluded(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, f *historyFixture, cfg *Config)
		excluded int
	}{
		{
			name: "provider not allowlisted",
			setup: func(t *testing.T, f *historyFixture, cfg *Config) {
				codexSources, codexEnumerators := cfg.Sources, cfg.ScanEnumerators
				cfg.Sources = func(p transcript.Provider) (reader.Source, bool) {
					if p == transcript.ProviderClaude {
						return scanGuard{Source: f.source(), t: t, forbidden: map[string]bool{"*": true}}, true
					}
					return codexSources(p)
				}
				cfg.ScanEnumerators = func(p transcript.Provider) (reader.Enumerator, bool) {
					if p == transcript.ProviderClaude {
						return scanGuardEnumerator{t}, true
					}
					return codexEnumerators(p)
				}
			},
		},
		{
			name: ".firekeeper-ignore",
			setup: func(t *testing.T, f *historyFixture, cfg *Config) {
				repo := filepath.Join(f.home, "src", "private")
				if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo, ".firekeeper-ignore"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
				f.metas[0].CWD = repo
			},
			excluded: 1,
		},
		{
			name: "exclude in config",
			setup: func(t *testing.T, f *historyFixture, cfg *Config) {
				repo := filepath.Join(f.home, "work", "secret")
				if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
					t.Fatal(err)
				}
				f.metas[0].CWD = repo
				cfg.Exclude = []string{filepath.Join(f.home, "work", "*")}
			},
			excluded: 1,
		},
		{
			name: "working directory unknown",
			setup: func(t *testing.T, f *historyFixture, cfg *Config) {
				f.metas[0].CWD = ""
			},
			excluded: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHistoryFixture(t, 2, 3)
			ts := newTestServer(t)
			cfg, enumerator := f.scanConfig(ts.URL)
			tt.setup(t, f, &cfg)
			enumerator.metas = f.metas
			allowedSource := cfg.Sources
			forbidden := map[string]bool{}
			if tt.excluded > 0 {
				forbidden[f.metas[0].ID] = true
				forbidden[f.metas[0].RolloutPath] = true
			}
			cfg.Sources = func(p transcript.Provider) (reader.Source, bool) {
				source, ok := allowedSource(p)
				if ok && p == transcript.ProviderCodex {
					return scanGuard{Source: source, t: t, forbidden: forbidden}, true
				}
				return source, ok
			}

			got := pass(t, cfg)
			if got.Scan.Excluded != tt.excluded {
				t.Fatalf("excluded = %d, want %d", got.Scan.Excluded, tt.excluded)
			}
			wantRead := 2 - tt.excluded
			if got.Scan.Read != wantRead || len(got.Sessions) != wantRead {
				t.Fatalf("read %d sessions (%d results), want %d", got.Scan.Read, len(got.Sessions), wantRead)
			}
			for _, s := range got.Sessions {
				if s.Provider != string(transcript.ProviderCodex) {
					t.Fatalf("read a %s session", s.Provider)
				}
			}
		})
	}
}

// ingestRecorder is a minimal server that accepts every ingest and
// heartbeat and records which sessions each carried, in order.
type ingestRecorder struct {
	*httptest.Server
	mu         sync.Mutex
	sessions   []string
	heartbeats []heartbeatRequest
}

func newIngestRecorder(t *testing.T) *ingestRecorder {
	t.Helper()
	rec := &ingestRecorder{}
	rec.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		defer rec.mu.Unlock()
		switch r.URL.Path {
		case "/v1/ingest":
			var req ingestRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("ingest body: %v", err)
			}
			n := 0
			for _, s := range req.Sessions {
				rec.sessions = append(rec.sessions, s.Meta.SessionID)
				n += len(s.Events)
			}
			json.NewEncoder(w).Encode(ingestResponse{Accepted: n})
		case "/v1/heartbeat":
			var req heartbeatRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("heartbeat body: %v", err)
			}
			rec.heartbeats = append(rec.heartbeats, req)
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(rec.Close)
	return rec
}

func TestScanBurstUploadsNewestFirstOverSeveralPasses(t *testing.T) {
	for _, tt := range []struct {
		name  string
		limit int
		total int
	}{
		{"default limit", 0, 2*DefaultScanLimit + 1},
		{"explicit limit", 2, 5},
		{"exact boundary", 2, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := make([]int, tt.total)
			for i := range events {
				events[i] = 1
			}
			f := newHistoryFixture(t, events...)
			rec := newIngestRecorder(t)
			cfg, enumerator := f.scanConfig(rec.URL)
			cfg.ScanLimit = tt.limit
			// Start oldest first so the test actually exercises sorting.
			enumerator.metas = slices.Clone(f.metas)
			slices.Reverse(enumerator.metas)
			now := time.Now()
			cfg.Now = func() time.Time { return now }

			limit := tt.limit
			if limit == 0 {
				limit = DefaultScanLimit
			}
			for uploaded := 0; uploaded < tt.total; {
				read := min(limit, tt.total-uploaded)
				want := ScanSummary{
					Ran: true, Found: tt.total - uploaded, Read: read,
					Deferred: tt.total - uploaded - read,
					Complete: uploaded+read == tt.total,
				}
				got := pass(t, cfg)
				if got.Scan != want {
					t.Fatalf("after %d uploads: scan = %+v, want %+v", uploaded, got.Scan, want)
				}
				uploaded += read
				if len(rec.sessions) != uploaded {
					t.Fatalf("uploaded %d sessions, want %d", len(rec.sessions), uploaded)
				}
			}
			for i, id := range rec.sessions {
				if id != f.metas[i].ID {
					t.Fatalf("upload %d is session %s, want %s (newest first)", i, id, f.metas[i].ID)
				}
			}
			if again := pass(t, cfg); again.Scan != (ScanSummary{Ran: true, Complete: true}) || len(rec.sessions) != tt.total {
				t.Fatalf("pass after the burst: scan = %+v, uploads = %d", again.Scan, len(rec.sessions))
			}
		})
	}
}

func TestScanTimeBudget(t *testing.T) {
	f := newHistoryFixture(t, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1)
	rec := newIngestRecorder(t)
	cfg, _ := f.scanConfig(rec.URL)
	// Every clock reading is a second later than the one before, so a 3
	// second budget runs out after a few sessions.
	var mu sync.Mutex
	clock := time.Now()
	cfg.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(time.Second)
		return clock
	}
	cfg.ScanBudget = 3 * time.Second

	got := pass(t, cfg)
	if got.Scan.Read == 0 || got.Scan.Read >= 10 || got.Scan.Complete || got.Scan.Read+got.Scan.Deferred != 10 {
		t.Fatalf("scan = %+v, want a partial pass", got.Scan)
	}
	total := got.Scan.Read
	for i := 0; i < 10 && total < 10; i++ {
		total += pass(t, cfg).Scan.Read
	}
	if total != 10 || len(rec.sessions) != 10 {
		t.Fatalf("read %d sessions (%d uploads) over several passes, want 10", total, len(rec.sessions))
	}
}

func TestScanWindow(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name  string
		cfg   func(*Config)
		found int
	}{
		{"default lookback", func(*Config) {}, 1},
		{"shorter lookback", func(c *Config) { c.ScanLookback = time.Hour }, 0},
		{"window opened earlier", func(c *Config) { c.ScanAfter = now.Add(-3 * time.Hour) }, 1},
		{"window opened later", func(c *Config) { c.ScanAfter = now.Add(-time.Hour) }, 0},
		{"since narrows the window", func(c *Config) { c.ScanAfter = now.Add(-3 * time.Hour); c.Since = time.Hour }, 0},
		{"since wider than the window", func(c *Config) { c.ScanLookback = 3 * time.Hour; c.Since = 48 * time.Hour }, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHistoryFixture(t, 1)
			old := now.Add(-2 * time.Hour)
			if err := os.Chtimes(f.metas[0].RolloutPath, old, old); err != nil {
				t.Fatal(err)
			}
			cfg, _ := f.scanConfig("")
			cfg.DryRun = true
			cfg.Now = func() time.Time { return now }
			tt.cfg(&cfg)
			if got := pass(t, cfg); got.Scan.Found != tt.found {
				t.Fatalf("found = %d, want %d", got.Scan.Found, tt.found)
			}
		})
	}
}

func TestLiveOnlyDoesNotScan(t *testing.T) {
	f := newHistoryFixture(t, 2)
	rec := newIngestRecorder(t)
	cfg, enumerator := f.scanConfig(rec.URL)
	cfg.ScanEnded = false
	got := pass(t, cfg)
	if len(got.Sessions) != 0 || got.Scan != (ScanSummary{}) || enumerator.calls.Load() != 0 {
		t.Fatalf("live-only pass scanned: %+v, %d enumerations", got, enumerator.calls.Load())
	}
	if len(rec.sessions) != 0 {
		t.Fatalf("uploaded %d sessions", len(rec.sessions))
	}
}

func TestScanWithoutAllowlistReadsNothing(t *testing.T) {
	f := newHistoryFixture(t, 2)
	cfg, enumerator := f.scanConfig("")
	cfg.Providers = nil
	got := pass(t, cfg)
	if got.Scan.Found != 0 || len(got.Sessions) != 0 || enumerator.calls.Load() != 0 {
		t.Fatalf("pass without allowlist scanned: %+v", got)
	}
}

func TestHeartbeatReportsScannedSessionsAsEnded(t *testing.T) {
	f := newHistoryFixture(t, 1, 1)
	rec := newIngestRecorder(t)
	live := f.metas[0]
	live.State = session.SessionStateActive
	cfg, _ := f.scanConfig(rec.URL, live)

	summary := pass(t, cfg)
	if err := Heartbeat(context.Background(), cfg, summary); err != nil {
		t.Fatal(err)
	}
	if len(rec.heartbeats) != 1 {
		t.Fatalf("heartbeats = %d", len(rec.heartbeats))
	}
	states := map[string]string{}
	for _, s := range rec.heartbeats[0].Sessions {
		states[s.SessionID] = s.State
	}
	want := map[string]string{f.metas[0].ID: "ACTIVE", f.metas[1].ID: "ENDED"}
	if len(states) != len(want) || states[f.metas[0].ID] != want[f.metas[0].ID] || states[f.metas[1].ID] != want[f.metas[1].ID] {
		t.Fatalf("heartbeat states = %v, want %v", states, want)
	}
}
