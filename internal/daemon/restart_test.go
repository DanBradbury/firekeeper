package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/testenv/fakeapi"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/codex"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

const (
	testMachine = "0199ffff-0000-7000-8000-0000000000d1"
	testSession = "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4ad1"
	// secretText is in every transcript line and must never reach the log.
	secretText = "transcript-body-text"
)

// writeHome creates a home with one Codex rollout of n synthetic events.
func writeHome(t *testing.T, home string, n int) (rollout, repo string) {
	t.Helper()
	repo = filepath.Join(home, "src", "demo")
	dir := filepath.Join(home, ".codex", "sessions", "2026", "01", "02")
	for _, d := range []string{filepath.Join(repo, ".git"), dir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var b strings.Builder
	for i := range n {
		rec := map[string]any{
			"timestamp": "2026-01-02T03:04:05.000Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message", "role": "user",
				"content": []map[string]string{{"type": "input_text", "text": fmt.Sprintf("%s %d", secretText, i)}},
			},
		}
		data, _ := json.Marshal(rec)
		b.Write(data)
		b.WriteByte('\n')
	}
	rollout = filepath.Join(dir, "rollout-2026-01-02T03-04-05-"+testSession+".jsonl")
	if err := os.WriteFile(rollout, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return rollout, repo
}

// reporterConfig reports one fixed Codex session from home, without
// scanning real processes.
func reporterConfig(home, server, rollout, repo string) reporter.Config {
	meta := session.Meta{
		ID: testSession, MachineID: testMachine, Provider: "codex", Project: "demo",
		CWD: repo, RolloutPath: rollout, State: session.SessionStateWaiting,
	}
	return reporter.Config{
		Server:    server,
		Providers: []transcript.Provider{transcript.ProviderCodex},
		Home:      home,
		Version:   "test",
		Discover: func(context.Context, session.Options) ([]session.Meta, error) {
			return []session.Meta{meta}, nil
		},
		Sources: func(p transcript.Provider) (reader.Source, bool) {
			if p == transcript.ProviderCodex {
				return &codex.Source{CodexHome: filepath.Join(home, ".codex")}, true
			}
			return nil, false
		},
	}
}

// recordingServer runs the fakeapi stand-in server and records the
// seqs of each ingest request and the number of heartbeats. hold, when not
// nil, runs instead of the API for the nth ingest request and decides what
// happens.
type recordingServer struct {
	*httptest.Server
	mu         sync.Mutex
	posts      [][]int64
	heartbeats int
}

type holdFunc func(n int, w http.ResponseWriter, r *http.Request, next http.Handler)

func newRecordingServer(t *testing.T, hold holdFunc) *recordingServer {
	t.Helper()
	rs := &recordingServer{}
	handler := fakeapi.New().Handler()
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/heartbeat":
			rs.mu.Lock()
			rs.heartbeats++
			rs.mu.Unlock()
		case "/v1/ingest":
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var req struct {
				Sessions []struct {
					Events []struct {
						Seq int64 `json:"seq"`
					} `json:"events"`
				} `json:"sessions"`
			}
			json.Unmarshal(body, &req)
			var seqs []int64
			for _, s := range req.Sessions {
				for _, e := range s.Events {
					seqs = append(seqs, e.Seq)
				}
			}
			rs.mu.Lock()
			rs.posts = append(rs.posts, seqs)
			n := len(rs.posts)
			rs.mu.Unlock()
			if hold != nil {
				hold(n, w, r, handler)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *recordingServer) snapshot() (posts [][]int64, heartbeats int) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([][]int64(nil), rs.posts...), rs.heartbeats
}

// stored returns every seq the server holds for the test session, in order.
func (rs *recordingServer) stored(t *testing.T) []int64 {
	t.Helper()
	uid := url.PathEscape(testMachine + ":" + testSession)
	var seqs []int64
	after := int64(-1)
	for {
		resp, err := http.Get(fmt.Sprintf("%s/v1/sessions/%s/events?limit=1000&after_seq=%d", rs.URL, uid, after))
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Events []struct {
				Seq int64 `json:"seq"`
			} `json:"events"`
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

func readState(t *testing.T, home, rollout string) reporter.FileState {
	t.Helper()
	state, err := reporter.LoadState(filepath.Join(home, ".firekeeper", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return state.Files[rollout]
}

func seqRange(from, to int64) []int64 {
	var seqs []int64
	for i := from; i < to; i++ {
		seqs = append(seqs, i)
	}
	return seqs
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestGracefulStopFinishesBatchInFlight(t *testing.T) {
	home := t.TempDir()
	rollout, repo := writeHome(t, home, 1200)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Stop arrives while the first batch is on the wire.
	rs := newRecordingServer(t, func(n int, w http.ResponseWriter, r *http.Request, next http.Handler) {
		if n == 1 {
			cancel()
		}
		next.ServeHTTP(w, r)
	})
	err := Run(ctx, Config{Reporter: reporterConfig(home, rs.URL, rollout, repo), Dir: filepath.Join(home, ".firekeeper")})
	if err != nil {
		t.Fatal(err)
	}
	posts, heartbeats := rs.snapshot()
	if len(posts) != 1 || heartbeats != 0 {
		t.Fatalf("ingest requests = %d, heartbeats = %d; want the one batch in flight and nothing after", len(posts), heartbeats)
	}
	assertSeqs(t, rs.stored(t), 500)
	if st := readState(t, home, rollout); st.LastSeq != 499 {
		t.Fatalf("state last_seq = %d, want 499 so the next run resumes after the finished batch", st.LastSeq)
	}
}
