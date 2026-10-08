package reporter

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// trapSource fails any attempt to find or read a transcript. Its transcript
// path is a directory, so even a direct open-and-read would fail.
type trapSource struct {
	path  string
	calls int
}

var errTrapped = errors.New("transcript was opened")

func (s *trapSource) Locate(session.Meta) ([]string, error) {
	s.calls++
	return []string{s.path}, errTrapped
}

func (s *trapSource) Read(string, int64) ([]transcript.Event, int64, error) {
	s.calls++
	return nil, 0, errTrapped
}

func writeGitConfig(t *testing.T, repo, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExcludedSessionNeverOpened(t *testing.T) {
	tests := []struct {
		name    string
		exclude []string
		remote  string
		marker  bool
		reason  string // "" means the session is read
	}{
		{name: "cwd glob", exclude: []string{"<home>/src/*"}, reason: "excluded by config: <home>/src/*"},
		{name: "git root dir", exclude: []string{"<home>/src/demo"}, reason: "excluded by config: <home>/src/demo"},
		{name: "remote pattern", exclude: []string{"github.com/acme"},
			remote: "[core]\n\tbare = false\n[remote \"origin\"]\n\turl = git@github.com:acme/demo.git\n", reason: "excluded by config: github.com/acme"},
		{name: "ignore marker", marker: true, reason: ".firekeeper-ignore"},
		{name: "unrelated patterns", exclude: []string{"<home>/other", "github.com/someone-else"},
			remote: "[remote \"origin\"]\n\turl = https://github.com/acme/demo\n"},
	}
	for _, tt := range tests {
		for _, dryRun := range []bool{false, true} {
			name := tt.name
			if dryRun {
				name += "/dry-run"
			}
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				trap := &trapSource{path: t.TempDir()}
				if tt.remote != "" {
					writeGitConfig(t, f.repo, tt.remote)
				}
				if tt.marker {
					if err := os.WriteFile(filepath.Join(f.repo, ".firekeeper-ignore"), nil, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				var out bytes.Buffer
				cfg := f.config("http://127.0.0.1:1") // never contacted
				cfg.DryRun = dryRun
				cfg.Out = &out
				for _, e := range tt.exclude {
					cfg.Exclude = append(cfg.Exclude, strings.ReplaceAll(e, "<home>", f.home))
				}
				cfg.Sources = func(transcript.Provider) (transcript.TranscriptSource, bool) { return trap, true }

				summary, err := RunOnce(context.Background(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				if len(summary.Sessions) != 1 {
					t.Fatalf("sessions = %d", len(summary.Sessions))
				}
				res := summary.Sessions[0]
				reason := strings.ReplaceAll(tt.reason, "<home>", f.home)
				if reason == "" {
					// Control: the trap fires when the session is not excluded.
					if trap.calls == 0 || !errors.Is(res.Err, errTrapped) {
						t.Fatalf("control session was not read: %+v", res)
					}
					return
				}
				if trap.calls != 0 {
					t.Fatalf("excluded transcript was opened %d times", trap.calls)
				}
				if res.Skipped != reason || res.Err != nil {
					t.Fatalf("result = %+v, want skipped %q", res, reason)
				}
				if want := "skipped (" + reason + ")"; !strings.Contains(out.String(), want) {
					t.Fatalf("output %q does not list %q", out.String(), want)
				}
			})
		}
	}
}

func TestBackfillDryRunListsSkipped(t *testing.T) {
	f := newHistoryFixture(t, 2, 1)
	excluded := f.metas[0]
	// Move one session into an excluded directory without touching its
	// transcript, and point its transcript somewhere unreadable.
	excluded.CWD = filepath.Join(f.home, "clients", "acme")
	excluded.Project = "acme"
	excluded.RolloutPath = t.TempDir()
	f.metas[0] = excluded

	var out bytes.Buffer
	cfg := f.config("")
	cfg.DryRun = true
	cfg.Out = &out
	cfg.Exclude = []string{filepath.Join(f.home, "clients")}
	real := cfg.Sources
	cfg.Sources = func(p transcript.Provider) (transcript.TranscriptSource, bool) {
		src, ok := real(p)
		return guardSource{TranscriptSource: src, t: t, forbidden: excluded.RolloutPath}, ok
	}
	res, err := Backfill(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Plan.Total(); got.Sessions != 1 || got.Excluded != 1 {
		t.Fatalf("plan = %+v", got)
	}
	want := "skipped codex " + excluded.ID + " acme: excluded by config: " + filepath.Join(f.home, "clients")
	if !strings.Contains(out.String(), want) {
		t.Fatalf("plan output %q does not contain %q", out.String(), want)
	}
	if strings.Contains(out.String(), "not applied yet") {
		t.Fatalf("plan still says exclusion is not applied: %q", out.String())
	}
}

// guardSource fails the test if the forbidden transcript is located or read.
type guardSource struct {
	transcript.TranscriptSource
	t         *testing.T
	forbidden string
}

func (g guardSource) Locate(meta session.Meta) ([]string, error) {
	if meta.RolloutPath == g.forbidden {
		g.t.Errorf("excluded session located")
	}
	return g.TranscriptSource.Locate(meta)
}

func (g guardSource) Read(path string, off int64) ([]transcript.Event, int64, error) {
	if path == g.forbidden {
		g.t.Errorf("excluded transcript read")
	}
	return g.TranscriptSource.Read(path, off)
}

func TestRedactPathsApplied(t *testing.T) {
	f := newFixture(t)
	f.append(t, 0, 1)
	secretDir := filepath.Join(f.home, "clients", "acme")
	rollout, err := os.ReadFile(f.rollout)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Replace(string(rollout), "message 0", "see "+secretDir+"/plan.md and ~/clients/acme/x", 1)
	if err := os.WriteFile(f.rollout, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t)
	cfg := f.config(srv.URL)
	cfg.RedactPaths = []string{secretDir}
	if _, err := RunOnce(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	events, _, err := srv.store.ListEvents(context.Background(), testMachine, testSession, -1, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("events %d %v", len(events), err)
	}
	for _, s := range []string{events[0].Text, string(events[0].Raw)} {
		if strings.Contains(s, "acme") || strings.Count(s, "[REDACTED:path]") != 2 {
			t.Fatalf("not redacted: %s", s)
		}
	}
}
