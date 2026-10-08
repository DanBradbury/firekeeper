package reporter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/api"
	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/codex"
)

const (
	testMachine = "0199ffff-0000-7000-8000-000000000001"
	testSession = "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
)

// fixture is a home directory with one Codex rollout and a repository the
// session runs in.
type fixture struct {
	home    string
	repo    string
	rollout string
	meta    session.Meta
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := t.TempDir()
	repo := filepath.Join(home, "src", "demo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".codex", "sessions", "2026", "01", "02")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(dir, "rollout-2026-01-02T03-04-05-"+testSession+".jsonl")
	if err := os.WriteFile(rollout, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return &fixture{
		home:    home,
		repo:    repo,
		rollout: rollout,
		meta: session.Meta{
			ID: testSession, MachineID: testMachine, Provider: "codex", Project: "demo",
			CWD: repo, RolloutPath: rollout, State: session.SessionStateWaiting, Title: "Demo",
		},
	}
}

func line(n int, text string) string {
	rec := map[string]any{
		"timestamp": "2026-01-02T03:04:05.000Z",
		"type":      "response_item",
		"payload": map[string]any{
			"type": "message", "role": "user",
			"content": []map[string]string{{"type": "input_text", "text": fmt.Sprintf("message %d %s", n, text)}},
		},
	}
	data, _ := json.Marshal(rec)
	return string(data) + "\n"
}

func (f *fixture) append(t *testing.T, from, to int) {
	t.Helper()
	file, err := os.OpenFile(f.rollout, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var b strings.Builder
	for i := from; i < to; i++ {
		b.WriteString(line(i, ""))
	}
	if _, err := file.WriteString(b.String()); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) config(server string) Config {
	return Config{
		Server:    server,
		Providers: []transcript.Provider{transcript.ProviderCodex},
		Home:      f.home,
		Discover: func(context.Context, session.Options) ([]session.Meta, error) {
			return []session.Meta{f.meta}, nil
		},
		Sources: func(p transcript.Provider) (transcript.TranscriptSource, bool) {
			if p == transcript.ProviderCodex {
				return &codex.Source{CodexHome: filepath.Join(f.home, ".codex")}, true
			}
			return nil, false
		},
	}
}

func (f *fixture) state(t *testing.T) FileState {
	t.Helper()
	state, err := LoadState(filepath.Join(f.home, ".firekeeper", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return state.Files[f.rollout]
}

// testServer runs the real ingest API over a temporary store. intercept, if
// set, sees each ingest request first and may answer it instead.
type testServer struct {
	*httptest.Server
	store     *store.Store
	mu        sync.Mutex
	requests  int
	maxEvents int
	intercept func(n int, w http.ResponseWriter, r *http.Request, next http.Handler) bool
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fk.db"))
	if err != nil {
		t.Fatal(err)
	}
	ts := &testServer{store: st}
	handler := api.Handler(st)
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/ingest" {
			body, _ := io.ReadAll(r.Body)
			var req ingestRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("ingest body: %v", err)
			}
			n := 0
			for _, s := range req.Sessions {
				n += len(s.Events)
			}
			ts.mu.Lock()
			ts.requests++
			count := ts.requests
			ts.maxEvents = max(ts.maxEvents, n)
			ts.mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(body))
			if ts.intercept != nil && ts.intercept(count, w, r, handler) {
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		ts.Close()
		st.Close()
	})
	return ts
}

// stored returns the seqs the server holds for the test session.
func (ts *testServer) stored(t *testing.T) []int64 {
	t.Helper()
	var seqs []int64
	after := int64(-1)
	for {
		resp, err := http.Get(fmt.Sprintf("%s/v1/sessions/%s:%s/events?limit=1000&after_seq=%d", ts.URL, testMachine, testSession, after))
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

func assertSeqs(t *testing.T, got []int64, n int) {
	t.Helper()
	if len(got) != n {
		t.Fatalf("server holds %d events, want %d", len(got), n)
	}
	for i, seq := range got {
		if seq != int64(i) {
			t.Fatalf("event %d has seq %d", i, seq)
		}
	}
}

func runOnce(t *testing.T, cfg Config) SessionResult {
	t.Helper()
	summary, err := RunOnce(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(summary.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(summary.Sessions))
	}
	return summary.Sessions[0]
}

func TestRunTwiceUploadsNothingTheSecondTime(t *testing.T) {
	f := newFixture(t)
	ts := newTestServer(t)
	f.append(t, 0, 3)
	var out bytes.Buffer
	cfg := f.config(ts.URL)
	cfg.Out = &out

	first := runOnce(t, cfg)
	if first.Err != nil || first.Events != 3 || first.Batches != 1 || first.Duplicates != 0 {
		t.Fatalf("first run = %+v", first)
	}
	second := runOnce(t, cfg)
	if second.Err != nil || second.Events != 0 || second.Batches != 0 {
		t.Fatalf("second run = %+v", second)
	}
	if ts.requests != 1 {
		t.Fatalf("server saw %d ingest requests, want 1", ts.requests)
	}
	assertSeqs(t, ts.stored(t), 3)

	want := "codex " + testSession + " demo: uploaded 3 events in 1 batch (0 duplicates, "
	if !strings.HasPrefix(out.String(), want) {
		t.Fatalf("output = %q, want prefix %q", out.String(), want)
	}
	if strings.Contains(out.String(), "message 0") {
		t.Fatal("summary printed transcript text")
	}

	// New lines are picked up; a partial trailing line waits for its newline.
	f.append(t, 3, 5)
	file, _ := os.OpenFile(f.rollout, os.O_APPEND|os.O_WRONLY, 0)
	file.WriteString(strings.TrimSuffix(line(5, ""), "\n"))
	file.Close()
	third := runOnce(t, cfg)
	if third.Events != 2 {
		t.Fatalf("third run read %d events, want 2", third.Events)
	}
	assertSeqs(t, ts.stored(t), 5)
}

func TestBatchesHoldAtMost500Events(t *testing.T) {
	f := newFixture(t)
	ts := newTestServer(t)
	f.append(t, 0, 1203)

	got := runOnce(t, f.config(ts.URL))
	if got.Err != nil || got.Events != 1203 || got.Batches != 3 {
		t.Fatalf("run = %+v", got)
	}
	if ts.maxEvents > 500 {
		t.Fatalf("largest batch had %d events", ts.maxEvents)
	}
	assertSeqs(t, ts.stored(t), 1203)
}

func TestSplitRespectsByteLimit(t *testing.T) {
	big := strings.Repeat("x", 1<<20)
	events := make([]transcript.Event, 9)
	for i := range events {
		events[i] = transcript.Event{Seq: int64(i), Provider: transcript.ProviderCodex, Role: transcript.RoleMeta, Text: big}
	}
	batches, err := split(events)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, b := range batches {
		data, _ := json.Marshal(b)
		if len(data) > maxBatchBytes+len(b) {
			t.Fatalf("batch is %d bytes", len(data))
		}
		total += len(b)
	}
	if len(batches) < 3 || total != len(events) {
		t.Fatalf("got %d batches with %d events", len(batches), total)
	}

	events[0].Text = strings.Repeat("x", maxBatchBytes+1)
	if _, err := split(events[:1]); err == nil {
		t.Fatal("oversized event was accepted")
	}
}

// A pass that stops after the server stored a batch, but before the
// reporter recorded it, re-sends only that batch; one that stops after the
// reporter recorded it re-sends nothing. Either way the server ends up with
// every event exactly once.
func TestInterruptedRunLosesAndDuplicatesNothing(t *testing.T) {
	tests := []struct {
		name string
		// fail answers request n, simulating a kill at that point.
		fail          func(n int, w http.ResponseWriter, r *http.Request, next http.Handler) bool
		wantResent    int
		wantLastSeq   int64
		wantStoredMid int
	}{
		{
			name: "server error on second batch",
			fail: func(n int, w http.ResponseWriter, r *http.Request, next http.Handler) bool {
				if n != 2 {
					return false
				}
				http.Error(w, `{"error":"boom","code":"internal"}`, http.StatusInternalServerError)
				return true
			},
			wantLastSeq:   499,
			wantStoredMid: 500,
		},
		{
			name: "connection drops after server stored second batch",
			fail: func(n int, w http.ResponseWriter, r *http.Request, next http.Handler) bool {
				if n != 2 {
					return false
				}
				next.ServeHTTP(httptest.NewRecorder(), r)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return true
			},
			wantResent:    500,
			wantLastSeq:   499,
			wantStoredMid: 1000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			ts := newTestServer(t)
			ts.intercept = tt.fail
			f.append(t, 0, 1203)
			cfg := f.config(ts.URL)

			got := runOnce(t, cfg)
			if got.Err == nil || got.Batches != 1 {
				t.Fatalf("interrupted run = %+v", got)
			}
			pos := f.state(t)
			if pos.Offset != 0 || pos.LastSeq != tt.wantLastSeq {
				t.Fatalf("state after interruption = %+v", pos)
			}
			if n := len(ts.stored(t)); n != tt.wantStoredMid {
				t.Fatalf("server holds %d events mid-run, want %d", n, tt.wantStoredMid)
			}

			ts.intercept = nil
			got = runOnce(t, cfg)
			if got.Err != nil || got.Events != 1203-500 || got.Duplicates != tt.wantResent {
				t.Fatalf("rerun = %+v", got)
			}
			assertSeqs(t, ts.stored(t), 1203)
			if again := runOnce(t, cfg); again.Events != 0 {
				t.Fatalf("third run read %d events", again.Events)
			}
		})
	}
}

func TestTruncatedFileResetsOffset(t *testing.T) {
	f := newFixture(t)
	ts := newTestServer(t)
	f.append(t, 0, 5)
	cfg := f.config(ts.URL)
	runOnce(t, cfg)

	if err := os.WriteFile(f.rollout, []byte(line(0, "rotated")+line(1, "rotated")), 0o644); err != nil {
		t.Fatal(err)
	}
	got := runOnce(t, cfg)
	if got.Err != nil || got.Events != 2 || got.Duplicates != 2 {
		t.Fatalf("run after truncation = %+v", got)
	}
	info, _ := os.Stat(f.rollout)
	if pos := f.state(t); pos.Offset != info.Size() || pos.Size != info.Size() || pos.LastSeq != 1 {
		t.Fatalf("state after truncation = %+v, size %d", pos, info.Size())
	}
	assertSeqs(t, ts.stored(t), 5)
}

// failTransport fails the test if anything tries to send a request.
type failTransport struct{ t *testing.T }

func (ft failTransport) RoundTrip(*http.Request) (*http.Response, error) {
	ft.t.Error("dry run sent an HTTP request")
	return nil, fmt.Errorf("network disabled")
}

func TestDryRunOpensNoNetworkConnection(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*Config)
	}{
		{"dry run flag", func(c *Config) { c.DryRun = true }},
		{"no provider allowlisted", func(c *Config) { c.Providers = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			var conns atomic.Int32
			ts := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			ts.Config.ConnState = func(net.Conn, http.ConnState) { conns.Add(1) }
			ts.Start()
			defer ts.Close()

			token := "ghp_" + strings.Repeat("a1", 18)
			file, _ := os.OpenFile(f.rollout, os.O_APPEND|os.O_WRONLY, 0)
			file.WriteString(line(0, "") + line(1, "token "+token) + line(2, ""))
			file.Close()

			var out bytes.Buffer
			cfg := f.config(ts.URL)
			cfg.Out = &out
			cfg.Client = &http.Client{Transport: failTransport{t}}
			tt.edit(&cfg)
			got := runOnce(t, cfg)
			if got.Err != nil || got.Events != 3 || got.Batches != 0 {
				t.Fatalf("dry run = %+v", got)
			}
			// The token appears in Text and Raw; the cwd's home prefix once.
			if got.Redactions < 2 {
				t.Fatalf("redactions = %d, want at least 2", got.Redactions)
			}
			if conns.Load() != 0 {
				t.Fatalf("dry run opened %d connections", conns.Load())
			}
			if _, err := os.Stat(filepath.Join(f.home, ".firekeeper", "state.json")); !os.IsNotExist(err) {
				t.Fatalf("dry run wrote state: %v", err)
			}
			want := fmt.Sprintf("codex %s demo: would upload 3 events (%d redactions)\n", testSession, got.Redactions)
			if out.String() != want {
				t.Fatalf("output = %q, want %q", out.String(), want)
			}
			if strings.Contains(out.String(), token) {
				t.Fatal("output contains the token")
			}
		})
	}
}

func TestUploadedEventsAreRedacted(t *testing.T) {
	f := newFixture(t)
	ts := newTestServer(t)
	token := "ghp_" + strings.Repeat("b2", 18)
	file, _ := os.OpenFile(f.rollout, os.O_APPEND|os.O_WRONLY, 0)
	file.WriteString(line(0, token+" in "+f.repo))
	file.Close()

	runOnce(t, f.config(ts.URL))
	resp, err := http.Get(fmt.Sprintf("%s/v1/sessions/%s:%s/events", ts.URL, testMachine, testSession))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if bytes.Contains(body, []byte(token)) || bytes.Contains(body, []byte(f.home)) {
		t.Fatal("server stored unredacted event")
	}
	if !bytes.Contains(body, []byte("[REDACTED:github_token]")) || !bytes.Contains(body, []byte("~/src/demo")) {
		t.Fatal("server events lack redaction markers")
	}
	resp, err = http.Get(fmt.Sprintf("%s/v1/sessions/%s:%s", ts.URL, testMachine, testSession))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		CWD   string `json:"cwd"`
		State string `json:"state"`
	}
	json.NewDecoder(resp.Body).Decode(&meta)
	resp.Body.Close()
	if meta.CWD != "~/src/demo" || meta.State != "WAITING" {
		t.Fatalf("session meta = %+v", meta)
	}
}

func TestSessionsThatAreNotRead(t *testing.T) {
	tests := []struct {
		name string
		edit func(*fixture, *Config)
		want string
	}{
		{"provider not allowlisted", func(f *fixture, c *Config) { c.Providers = []transcript.Provider{transcript.ProviderCopilot} }, "skipped (provider not allowlisted)"},
		{"ignore marker at repo root", func(f *fixture, c *Config) {
			os.WriteFile(filepath.Join(f.repo, ".firekeeper-ignore"), nil, 0o644)
			f.meta.CWD = filepath.Join(f.repo, "sub")
		}, "skipped (.firekeeper-ignore)"},
		{"unknown cwd", func(f *fixture, c *Config) { f.meta.CWD = "" }, "skipped (working directory unknown)"},
		{"no reader", func(f *fixture, c *Config) { f.meta.Provider = "kimi" }, "skipped (no transcript reader for provider)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.append(t, 0, 2)
			var out bytes.Buffer
			cfg := f.config("http://127.0.0.1:1")
			cfg.Out = &out
			cfg.Client = &http.Client{Transport: failTransport{t}}
			tt.edit(f, &cfg)
			got := runOnce(t, cfg)
			if got.Err != nil || got.Events != 0 || !strings.Contains(out.String(), tt.want) {
				t.Fatalf("result = %+v, output %q", got, out.String())
			}
		})
	}
}

// Claude Code transcripts are read only when claude is allowlisted, like
// every other provider.
func TestClaudeIsOffUnlessAllowlisted(t *testing.T) {
	const claudeSession = "0199c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d"
	f := newFixture(t)
	dir := filepath.Join(f.home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, "projects", "-src-demo", claudeSession+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	record := `{"type":"user","sessionId":"` + claudeSession + `","message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(path, []byte(record+record), 0o644); err != nil {
		t.Fatal(err)
	}
	f.meta = session.Meta{ID: claudeSession, MachineID: testMachine, Provider: "claude", Project: "demo", CWD: f.repo, RolloutPath: path}

	for _, tt := range []struct {
		name      string
		providers []transcript.Provider
		want      string
	}{
		{"other provider allowlisted", []transcript.Provider{transcript.ProviderCodex}, "skipped (provider not allowlisted)"},
		{"claude allowlisted", []transcript.Provider{transcript.ProviderClaude}, "would upload 2 events"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			cfg := f.config("http://127.0.0.1:1")
			cfg.Providers = tt.providers
			cfg.DryRun = true
			cfg.Sources = nil // the registered sources
			cfg.Out = &out
			cfg.Client = &http.Client{Transport: failTransport{t}}
			runOnce(t, cfg)
			if !strings.Contains(out.String(), "claude "+claudeSession+" demo: "+tt.want) {
				t.Fatalf("output %q, want %q", out.String(), tt.want)
			}
		})
	}
}

func TestIgnoreMarkerOutsideRepoDoesNotApply(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.WriteFile(filepath.Join(root, ".firekeeper-ignore"), nil, 0o644)
	if ignored(repo) {
		t.Fatal("marker above the Git root applied to the repository")
	}
	if !ignored(filepath.Join(root, "plain")) {
		t.Fatal("marker did not apply to a directory below it")
	}
}

func TestSinceSkipsOldTranscripts(t *testing.T) {
	f := newFixture(t)
	f.append(t, 0, 2)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	os.Chtimes(f.rollout, now.Add(-48*time.Hour), now.Add(-48*time.Hour))
	cfg := f.config("")
	cfg.DryRun = true
	cfg.Now = func() time.Time { return now }

	cfg.Since = 24 * time.Hour
	if got := runOnce(t, cfg); got.Events != 0 {
		t.Fatalf("read %d events from an old transcript", got.Events)
	}
	cfg.Since = 72 * time.Hour
	if got := runOnce(t, cfg); got.Events != 2 {
		t.Fatalf("read %d events, want 2", got.Events)
	}
}

func TestServerErrorsAreReportedWithoutBodies(t *testing.T) {
	f := newFixture(t)
	f.append(t, 0, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		w.Write([]byte(`{"error":"secret detail","code":"too_many_events"}`))
	}))
	defer ts.Close()
	summary, err := RunOnce(context.Background(), f.config(ts.URL))
	if err != nil {
		t.Fatal(err)
	}
	got := summary.Sessions[0]
	if got.Err == nil || got.Err.Error() != "upload: server returned 413 (too_many_events)" || !summary.Failed() {
		t.Fatalf("result = %+v", got)
	}
	if pos := f.state(t); pos != (FileState{}) {
		t.Fatalf("offset advanced after failure: %+v", pos)
	}
}

func TestRunOnceRejectsBadConfig(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("ftp://example.invalid")
	if _, err := RunOnce(context.Background(), cfg); err == nil {
		t.Fatal("accepted non-HTTP server URL")
	}
	cfg = f.config("")
	cfg.Providers = []transcript.Provider{"nope"}
	if _, err := RunOnce(context.Background(), cfg); err == nil {
		t.Fatal("accepted unknown provider")
	}
}

func TestDiscoveryWarningsDoNotStopThePass(t *testing.T) {
	f := newFixture(t)
	f.append(t, 0, 1)
	cfg := f.config("")
	cfg.DryRun = true
	cfg.Discover = func(context.Context, session.Options) ([]session.Meta, error) {
		return []session.Meta{f.meta, f.meta, {Provider: "opencode"}}, &session.Warning{Message: "Copilot: unavailable"}
	}
	summary, err := RunOnce(context.Background(), cfg)
	if err != nil || summary.Warning == nil || len(summary.Sessions) != 1 || summary.Sessions[0].Events != 1 {
		t.Fatalf("summary = %+v, err %v", summary, err)
	}
}
