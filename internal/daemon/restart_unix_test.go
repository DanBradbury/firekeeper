//go:build unix

package daemon

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Killing and restarting the daemon needs SIGKILL and SIGTERM, hence the
// build constraint.

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
