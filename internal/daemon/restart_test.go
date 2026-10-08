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
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/server/api"
	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/codex"
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
		Sources: func(p transcript.Provider) (transcript.TranscriptSource, bool) {
			if p == transcript.ProviderCodex {
				return &codex.Source{CodexHome: filepath.Join(home, ".codex")}, true
			}
			return nil, false
		},
	}
}

// recordingServer runs the real API over a temporary store and records the
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
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fk.db"))
	if err != nil {
		t.Fatal(err)
	}
	rs := &recordingServer{}
	handler := api.Handler(st)
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
	t.Cleanup(func() {
		rs.Close()
		st.Close()
	})
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

// TestHelperDaemon is the daemon process for TestKillAndRestartMidBatch.
// It does nothing unless run as a child of that test.
func TestHelperDaemon(t *testing.T) {
	if os.Getenv("FIREKEEPER_DAEMON_HELPER") != "1" {
		t.Skip("helper process")
	}
	home := os.Getenv("FIREKEEPER_HELPER_HOME")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	cfg := Config{
		Reporter: reporterConfig(home, os.Getenv("FIREKEEPER_HELPER_SERVER"), os.Getenv("FIREKEEPER_HELPER_ROLLOUT"), os.Getenv("FIREKEEPER_HELPER_REPO")),
		Dir:      filepath.Join(home, ".firekeeper"),
		Interval: MinInterval,
	}
	if err := Run(ctx, cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
}

func TestKillAndRestartMidBatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs SIGKILL and SIGTERM")
	}
	const total = 1200 // three batches: 500, 500, 200
	home := t.TempDir()
	rollout, repo := writeHome(t, home, total)

	// The server commits the second batch, then never answers: the daemon
	// is killed while waiting, so the batch is stored but unconfirmed.
	committed := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	rs := newRecordingServer(t, func(n int, w http.ResponseWriter, r *http.Request, next http.Handler) {
		if n != 2 {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(httptest.NewRecorder(), r)
		close(committed)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})

	start := func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperDaemon$")
		cmd.Env = append(os.Environ(),
			"FIREKEEPER_DAEMON_HELPER=1",
			"FIREKEEPER_HELPER_HOME="+home,
			"FIREKEEPER_HELPER_SERVER="+rs.URL,
			"FIREKEEPER_HELPER_ROLLOUT="+rollout,
			"FIREKEEPER_HELPER_REPO="+repo,
		)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cmd.Process.Kill()
			cmd.Wait()
		})
		return cmd
	}

	first := start()
	select {
	case <-committed:
	case <-time.After(30 * time.Second):
		t.Fatal("second batch never arrived")
	}
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	first.Wait()

	assertSeqs(t, rs.stored(t), 1000)
	if st := readState(t, home, rollout); st.LastSeq != 499 || st.Offset != 0 {
		t.Fatalf("after kill: last_seq = %d, offset = %d; want only the first batch confirmed", st.LastSeq, st.Offset)
	}
	postsBefore, _ := rs.snapshot()

	second := start()
	waitFor(t, "a full pass and heartbeat after restart", func() bool {
		posts, heartbeats := rs.snapshot()
		return heartbeats >= 1 && len(posts) > len(postsBefore)
	})

	// A second daemon in another process cannot start while this one runs.
	err := Run(context.Background(), Config{Reporter: reporterConfig(home, rs.URL, rollout, repo), Dir: filepath.Join(home, ".firekeeper")})
	if err != ErrLocked {
		t.Fatalf("Run beside a running daemon = %v, want ErrLocked", err)
	}

	if err := second.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := second.Wait(); err != nil {
		t.Fatalf("daemon did not exit cleanly on SIGTERM: %v", err)
	}

	// Nothing lost: every event is stored exactly once.
	assertSeqs(t, rs.stored(t), total)
	// Nothing confirmed was re-sent: the restart sent only the unconfirmed
	// second batch and the third.
	posts, _ := rs.snapshot()
	var resent []int64
	for _, p := range posts[len(postsBefore):] {
		resent = append(resent, p...)
	}
	if fmt.Sprint(resent) != fmt.Sprint(seqRange(500, total)) {
		t.Fatalf("restart sent seqs %d..%d (%d events), want 500..%d", resent[0], resent[len(resent)-1], len(resent), total-1)
	}
	info, _ := os.Stat(rollout)
	if st := readState(t, home, rollout); st.LastSeq != total-1 || st.Offset != info.Size() {
		t.Fatalf("after restart: last_seq = %d, offset = %d; want %d, %d", st.LastSeq, st.Offset, total-1, info.Size())
	}

	logData, err := os.ReadFile(filepath.Join(home, ".firekeeper", "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(logData)
	if strings.Contains(log, secretText) {
		t.Fatal("daemon log contains transcript text")
	}
	for _, want := range []string{"started", "uploaded 700 events in 2 batches (500 duplicates)", "stopped"} {
		if !strings.Contains(log, want) {
			t.Errorf("daemon log lacks %q", want)
		}
	}
}
