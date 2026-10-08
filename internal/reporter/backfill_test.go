package reporter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/codex"
)

// historyFixture is a home directory with several ended Codex sessions, the
// newest first in metas.
type historyFixture struct {
	home  string
	repo  string
	metas []session.Meta
}

func newHistoryFixture(t *testing.T, events ...int) *historyFixture {
	t.Helper()
	home := t.TempDir()
	repo := filepath.Join(home, "src", "demo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".firekeeper")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "machine-id"), []byte(testMachine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &historyFixture{home: home, repo: repo}
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i, n := range events {
		id := fmt.Sprintf("0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a%02d", i)
		day := filepath.Join(home, ".codex", "sessions", "2026", "09", fmt.Sprintf("%02d", i+1))
		if err := os.MkdirAll(day, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(day, "rollout-2026-09-01T12-00-00-"+id+".jsonl")
		var b strings.Builder
		for j := 0; j < n; j++ {
			b.WriteString(line(j, ""))
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		// Later sessions are newer; metas holds them newest first.
		activity := base.Add(time.Duration(i) * 24 * time.Hour)
		f.metas = append([]session.Meta{{
			ID: id, Provider: "codex", Project: "demo", CWD: repo, RolloutPath: path,
			State: session.SessionStateEnded, LastActivityAt: &activity,
		}}, f.metas...)
	}
	return f
}

type fakeEnumerator struct {
	metas []session.Meta
	calls atomic.Int32
}

func (e *fakeEnumerator) Enumerate(_ context.Context, opts transcript.EnumerateOptions) ([]session.Meta, error) {
	e.calls.Add(1)
	var out []session.Meta
	for _, m := range e.metas {
		if info, err := os.Stat(m.RolloutPath); err != nil || !info.ModTime().After(opts.ModifiedAfter) {
			continue
		}
		if opts.Skip != nil && opts.Skip(m) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func (f *historyFixture) source() *codex.Source {
	return &codex.Source{CodexHome: filepath.Join(f.home, ".codex")}
}

func (f *historyFixture) config(server string, live ...session.Meta) BackfillConfig {
	enumerator := &fakeEnumerator{metas: f.metas}
	return BackfillConfig{
		Config: Config{
			Server:    server,
			Providers: []transcript.Provider{transcript.ProviderCodex},
			Home:      f.home,
			Discover: func(context.Context, session.Options) ([]session.Meta, error) {
				return live, nil
			},
			Sources: func(p transcript.Provider) (transcript.TranscriptSource, bool) {
				if p == transcript.ProviderCodex {
					return f.source(), true
				}
				return nil, false
			},
		},
		Enumerators: func(p transcript.Provider) (transcript.Enumerator, bool) {
			if p == transcript.ProviderCodex {
				return enumerator, true
			}
			return nil, false
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
}

// runOnceConfig is a RunOnce over the same home that discovers metas as
// running sessions.
func (f *historyFixture) runOnceConfig(server string, metas ...session.Meta) Config {
	cfg := f.config(server).Config
	for i := range metas {
		metas[i].MachineID = testMachine
	}
	cfg.Discover = func(context.Context, session.Options) ([]session.Meta, error) {
		return metas, nil
	}
	return cfg
}

func (f *historyFixture) loadState(t *testing.T) *State {
	t.Helper()
	state, err := LoadState(filepath.Join(f.home, ".firekeeper", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func backfill(t *testing.T, cfg BackfillConfig) BackfillResult {
	t.Helper()
	result, err := Backfill(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	return result
}

func (ts *testServer) storedFor(t *testing.T, sessionID string) []int64 {
	t.Helper()
	var seqs []int64
	after := int64(-1)
	for {
		resp, err := http.Get(fmt.Sprintf("%s/v1/sessions/%s/events?limit=1000&after_seq=%d", ts.URL, url.PathEscape(testMachine+":"+sessionID), after))
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Events []transcript.Event `json:"events"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) == 0 {
			return seqs
		}
		for _, e := range page.Events {
			seqs = append(seqs, e.Seq)
			after = e.Seq
		}
	}
}

func (ts *testServer) sessionState(t *testing.T, sessionID string) string {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/v1/sessions/%s", ts.URL, url.PathEscape(testMachine+":"+sessionID)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if s, ok := body["state"].(string); ok {
		return s
	}
	if meta, ok := body["session"].(map[string]any); ok {
		s, _ := meta["state"].(string)
		return s
	}
	t.Fatalf("session response has no state: %v", body)
	return ""
}

func (ts *testServer) requestCount() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.requests
}

func TestBackfillTwiceUploadsNothingTheSecondTime(t *testing.T) {
	f := newHistoryFixture(t, 3, 1203, 7)
	ts := newTestServer(t)
	var progress bytes.Buffer
	cfg := f.config(ts.URL)
	cfg.Progress = &progress

	got := backfill(t, cfg)
	if got.Failed() || len(got.Sessions) != 3 {
		t.Fatalf("first backfill = %+v", got)
	}
	total := got.Plan.Total()
	if total.Sessions != 3 || total.Files != 3 || total.Events != 3+1203+7 || total.Requests < 5 {
		t.Fatalf("plan totals = %+v", total)
	}
	for _, m := range f.metas {
		assertSeqs(t, ts.storedFor(t, m.ID), map[string]int{f.metas[0].ID: 7, f.metas[1].ID: 1203, f.metas[2].ID: 3}[m.ID])
	}
	if lines := strings.Count(progress.String(), "\n"); lines != 3 {
		t.Fatalf("progress has %d lines, want one per session: %q", lines, progress.String())
	}

	requests := ts.requestCount()
	again := backfill(t, cfg)
	if len(again.Sessions) != 0 || again.Plan.Total().Sessions != 0 || again.Plan.Total().UpToDate != 3 {
		t.Fatalf("second backfill = %+v", again)
	}
	if ts.requestCount() != requests {
		t.Fatalf("second backfill sent %d requests", ts.requestCount()-requests)
	}
}

func TestBackfillUploadsNewestFirstWithinLimit(t *testing.T) {
	f := newHistoryFixture(t, 1, 1, 1)
	ts := newTestServer(t)
	cfg := f.config(ts.URL)
	cfg.Limit = 2
	got := backfill(t, cfg)
	if len(got.Sessions) != 2 || got.Sessions[0].SessionID != f.metas[0].ID || got.Sessions[1].SessionID != f.metas[1].ID {
		t.Fatalf("sessions = %+v", got.Sessions)
	}
	if pp := got.Plan.Providers[0]; pp.OverLimit != 1 || pp.Sessions != 2 {
		t.Fatalf("plan = %+v", pp)
	}
	if n := len(ts.storedFor(t, f.metas[2].ID)); n != 0 {
		t.Fatalf("oldest session uploaded past the limit: %d events", n)
	}
	cfg.Limit = 0
	if rest := backfill(t, cfg); len(rest.Sessions) != 1 || rest.Sessions[0].SessionID != f.metas[2].ID {
		t.Fatalf("rerun = %+v", rest.Sessions)
	}
}

// Killing a backfill partway, here by cancelling while the server is
// storing a batch, and running it again loses and duplicates nothing.
func TestBackfillInterruptedRunResumes(t *testing.T) {
	f := newHistoryFixture(t, 1003, 600)
	ts := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	ts.intercept = func(n int, w http.ResponseWriter, r *http.Request, next http.Handler) bool {
		if n != 3 {
			return false
		}
		next.ServeHTTP(httptest.NewRecorder(), r)
		cancel()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
		return true
	}
	cfg := f.config(ts.URL)
	result, err := Backfill(ctx, cfg)
	if !errors.Is(err, context.Canceled) || len(result.Sessions) != 2 {
		t.Fatalf("interrupted backfill = %+v, err %v", result, err)
	}

	ts.intercept = nil
	got := backfill(t, cfg)
	if got.Failed() {
		t.Fatalf("rerun = %+v", got)
	}
	assertSeqs(t, ts.storedFor(t, f.metas[0].ID), 600)
	assertSeqs(t, ts.storedFor(t, f.metas[1].ID), 1003)
	if again := backfill(t, cfg); len(again.Sessions) != 0 {
		t.Fatalf("third backfill uploaded %d sessions", len(again.Sessions))
	}
}

func TestRunOnceAfterBackfillUploadsOnlyNewEvents(t *testing.T) {
	f := newHistoryFixture(t, 5)
	ts := newTestServer(t)
	backfill(t, f.config(ts.URL))

	file, err := os.OpenFile(f.metas[0].RolloutPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString(line(5, "") + line(6, ""))
	file.Close()

	summary, err := RunOnce(context.Background(), f.runOnceConfig(ts.URL, f.metas[0]))
	if err != nil || len(summary.Sessions) != 1 {
		t.Fatalf("RunOnce = %+v, %v", summary, err)
	}
	if got := summary.Sessions[0]; got.Err != nil || got.Events != 2 || got.Duplicates != 0 {
		t.Fatalf("RunOnce after backfill = %+v", got)
	}
	assertSeqs(t, ts.storedFor(t, f.metas[0].ID), 7)
}

func TestBackfillKeepsLiveState(t *testing.T) {
	f := newHistoryFixture(t, 2, 2)
	ts := newTestServer(t)
	live := f.metas[0]
	live.State = session.SessionStateActive
	live.MachineID = testMachine
	live.PID = 42
	live.LastActivityAt = nil
	got := backfill(t, f.config(ts.URL, live))
	if got.Failed() {
		t.Fatalf("backfill = %+v", got)
	}
	if s := ts.sessionState(t, f.metas[0].ID); s != "ACTIVE" {
		t.Fatalf("live session state = %q, want ACTIVE", s)
	}
	if s := ts.sessionState(t, f.metas[1].ID); s != "ENDED" {
		t.Fatalf("ended session state = %q, want ENDED", s)
	}
}

func TestBackfillDryRunOpensNoNetworkConnection(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*BackfillConfig)
	}{
		{"dry run flag", func(c *BackfillConfig) { c.DryRun = true }},
		{"no provider allowlisted", func(c *BackfillConfig) { c.Providers = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newHistoryFixture(t, 3, 2)
			secret := "ghp_" + strings.Repeat("c3", 18)
			file, _ := os.OpenFile(f.metas[0].RolloutPath, os.O_APPEND|os.O_WRONLY, 0)
			file.WriteString(line(2, secret))
			file.Close()

			var conns atomic.Int32
			ts := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			ts.Config.ConnState = func(net.Conn, http.ConnState) { conns.Add(1) }
			ts.Start()
			defer ts.Close()

			var out bytes.Buffer
			cfg := f.config(ts.URL)
			cfg.Out = &out
			cfg.Client = &http.Client{Transport: failTransport{t}}
			cfg.Discover = func(context.Context, session.Options) ([]session.Meta, error) {
				t.Error("dry run discovered live sessions")
				return nil, nil
			}
			cfg.Confirm = func(Plan) bool {
				t.Error("dry run asked to confirm")
				return true
			}
			tt.edit(&cfg)
			got := backfill(t, cfg)
			if len(got.Sessions) != 0 || got.Plan.Uploading {
				t.Fatalf("dry run = %+v", got)
			}
			if total := got.Plan.Total(); total.Sessions != 2 || total.Events != 6 {
				t.Fatalf("plan totals = %+v", total)
			}
			if conns.Load() != 0 {
				t.Fatalf("dry run opened %d connections", conns.Load())
			}
			if _, err := os.Stat(filepath.Join(f.home, ".firekeeper", "state.json")); !os.IsNotExist(err) {
				t.Fatalf("dry run wrote state: %v", err)
			}
			if !strings.Contains(out.String(), "codex    2 sessions, 2 files,") || strings.Contains(out.String(), "message") || strings.Contains(out.String(), secret) {
				t.Fatalf("plan output = %q", out.String())
			}
		})
	}
}

func TestBackfillExcludesIgnoredDirectories(t *testing.T) {
	f := newHistoryFixture(t, 1, 1)
	ignoredRepo := filepath.Join(f.home, "src", "private")
	os.MkdirAll(filepath.Join(ignoredRepo, ".git"), 0o755)
	os.WriteFile(filepath.Join(ignoredRepo, ".firekeeper-ignore"), nil, 0o644)
	f.metas[0].CWD = ignoredRepo
	f.metas[1].CWD = ""
	cfg := f.config("")
	cfg.DryRun = true
	got := backfill(t, cfg)
	if pp := got.Plan.Providers[0]; pp.Sessions != 0 || pp.Excluded != 2 {
		t.Fatalf("plan = %+v", pp)
	}
}

func TestBackfillDeclinedUploadsNothing(t *testing.T) {
	f := newHistoryFixture(t, 1)
	ts := newTestServer(t)
	cfg := f.config(ts.URL)
	var shown Plan
	cfg.Confirm = func(p Plan) bool { shown = p; return false }
	got := backfill(t, cfg)
	if !got.Declined || len(got.Sessions) != 0 || shown.Total().Sessions != 1 || ts.requestCount() != 0 {
		t.Fatalf("declined backfill = %+v, requests %d", got, ts.requestCount())
	}
}

func TestBackfillStopsAfterConsecutiveFailures(t *testing.T) {
	f := newHistoryFixture(t, 1, 1, 1)
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"down","code":"unavailable"}`))
	}))
	defer ts.Close()
	cfg := f.config(ts.URL)
	var waits []time.Duration
	cfg.Sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	got, err := Backfill(context.Background(), cfg)
	if !errors.Is(err, ErrTooManyFailures) || !strings.Contains(err.Error(), "503 (unavailable)") || strings.Contains(err.Error(), "down") {
		t.Fatalf("err = %v", err)
	}
	if requests.Load() != 5 || len(got.Sessions) != 1 {
		t.Fatalf("requests = %d, sessions = %d", requests.Load(), len(got.Sessions))
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Fatalf("backoff = %v, want %v", waits, want)
	}
	if state := f.loadState(t); len(state.Files) != 0 {
		t.Fatalf("offsets saved after failures: %+v", state.Files)
	}
}

func TestBackfillRetriesTransientFailures(t *testing.T) {
	f := newHistoryFixture(t, 2)
	ts := newTestServer(t)
	ts.intercept = func(n int, w http.ResponseWriter, r *http.Request, next http.Handler) bool {
		if n > 2 {
			return false
		}
		http.Error(w, `{"error":"x","code":"internal"}`, http.StatusInternalServerError)
		return true
	}
	got := backfill(t, f.config(ts.URL))
	if got.Failed() {
		t.Fatalf("backfill = %+v", got)
	}
	assertSeqs(t, ts.storedFor(t, f.metas[0].ID), 2)
}

// Backfill and RunOnce read the same transcripts at once. Neither may move
// a saved position backwards, and the server ends up with every event once.
func TestBackfillAndRunOnceConcurrently(t *testing.T) {
	f := newHistoryFixture(t, 1100, 700, 300, 1300)
	ts := newTestServer(t)
	statePath := filepath.Join(f.home, ".firekeeper", "state.json")

	done := make(chan struct{})
	var watch sync.WaitGroup
	watch.Add(1)
	go func() {
		defer watch.Done()
		seen := map[string]FileState{}
		for {
			select {
			case <-done:
				return
			default:
			}
			state, err := LoadState(statePath)
			if err != nil {
				t.Errorf("load state: %v", err)
				return
			}
			for path, pos := range state.Files {
				if prev, ok := seen[path]; ok && (pos.Offset < prev.Offset || pos.LastSeq < prev.LastSeq) {
					t.Errorf("position moved backwards: %+v then %+v", prev, pos)
				}
				seen[path] = pos
			}
			time.Sleep(time.Millisecond)
		}
	}()

	var wg sync.WaitGroup
	errs := make(chan error, 3)
	wg.Add(3)
	go func() {
		defer wg.Done()
		result, err := Backfill(context.Background(), f.config(ts.URL))
		if err == nil && result.Failed() {
			err = errors.New("backfill session failed")
		}
		errs <- err
	}()
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			summary, err := RunOnce(context.Background(), f.runOnceConfig(ts.URL, append([]session.Meta(nil), f.metas...)...))
			if err == nil && summary.Failed() {
				err = errors.New("RunOnce session failed")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(done)
	watch.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	want := map[string]int{f.metas[0].ID: 1300, f.metas[1].ID: 300, f.metas[2].ID: 700, f.metas[3].ID: 1100}
	state := f.loadState(t)
	for _, m := range f.metas {
		assertSeqs(t, ts.storedFor(t, m.ID), want[m.ID])
		info, _ := os.Stat(m.RolloutPath)
		if pos := state.Files[m.RolloutPath]; pos.Offset != info.Size() || pos.LastSeq != int64(want[m.ID]-1) {
			t.Fatalf("final position = %+v, size %d", pos, info.Size())
		}
	}
	if again := backfill(t, f.config(ts.URL)); len(again.Sessions) != 0 {
		t.Fatalf("backfill after both uploaded %d sessions", len(again.Sessions))
	}
}

func TestStateLockTimesOut(t *testing.T) {
	f := newHistoryFixture(t, 1)
	unlock, ok, err := tryLock(filepath.Join(f.home, ".firekeeper", "state.lock"))
	if err != nil || !ok {
		t.Skipf("state lock unavailable: ok %v, err %v", ok, err)
	}
	defer unlock()

	cfg := f.runOnceConfig("http://127.0.0.1:1", f.metas[0])
	cfg.lockWait = 100 * time.Millisecond
	if _, err := RunOnce(context.Background(), cfg); !errors.Is(err, ErrStateLocked) {
		t.Fatalf("RunOnce err = %v, want ErrStateLocked", err)
	}
	bcfg := f.config("http://127.0.0.1:1")
	bcfg.lockWait = 100 * time.Millisecond
	if _, err := Backfill(context.Background(), bcfg); !errors.Is(err, ErrStateLocked) {
		t.Fatalf("Backfill err = %v, want ErrStateLocked", err)
	}
	// A dry run saves nothing, so it does not wait for the lock.
	bcfg.DryRun = true
	if _, err := Backfill(context.Background(), bcfg); err != nil {
		t.Fatalf("dry run err = %v", err)
	}
}

func TestMergeFileState(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	stale := FileState{Offset: 900, Size: 900, Mtime: at, LastSeq: 20}
	for _, tt := range []struct {
		name     string
		ours     FileState
		disk     FileState
		known    bool
		replaced *FileState
		size     int64
		want     FileState
	}{
		{"no saved position", FileState{Offset: 10, Size: 10, LastSeq: 3}, FileState{}, false, nil, 10, FileState{Offset: 10, Size: 10, LastSeq: 3}},
		{"disk further along", FileState{Offset: 0, LastSeq: 4}, FileState{Offset: 50, Size: 50, Mtime: at, LastSeq: 9}, true, nil, 60, FileState{Offset: 50, Size: 50, Mtime: at, LastSeq: 9}},
		{"ours further along", FileState{Offset: 60, Size: 60, LastSeq: 12}, FileState{Offset: 50, Size: 50, LastSeq: 9}, true, nil, 60, FileState{Offset: 60, Size: 60, LastSeq: 12}},
		{"mid-file batch keeps disk offset", FileState{Offset: 0, Size: 0, LastSeq: 30}, FileState{Offset: 50, Size: 50, LastSeq: 9}, true, nil, 60, FileState{Offset: 50, Size: 50, LastSeq: 30}},
		{"ours replaced the stale entry", FileState{Offset: 0, LastSeq: 1}, stale, true, &stale, 2000, FileState{Offset: 0, LastSeq: 1}},
		{"disk records a larger file", FileState{Offset: 0, LastSeq: 1}, stale, true, nil, 100, FileState{Offset: 0, LastSeq: 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := mergeFileState(tt.ours, tt.disk, tt.known, tt.replaced, tt.size); !sameFileState(got, tt.want) {
				t.Fatalf("merge = %+v, want %+v", got, tt.want)
			}
		})
	}
}
