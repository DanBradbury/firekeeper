package kimi

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// copyFixtures copies the fixture sessions tree into a temporary Kimi home
// and gives every file mtime, so ordering does not depend on checkout times.
func copyFixtures(t *testing.T, mtime time.Time) string {
	t.Helper()
	home := t.TempDir()
	err := filepath.WalkDir(filepath.Join(fixtureHome, "sessions"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(fixtureHome, path)
		dst := filepath.Join(home, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return err
		}
		return os.Chtimes(dst, mtime, mtime)
	})
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func TestEnumerate(t *testing.T) {
	// The log was last written before state.json's updatedAt, so activity
	// comes from state.json for the live session and from mtime otherwise.
	mtime := time.UnixMilli(1791460800100).UTC()
	home := copyFixtures(t, mtime)
	metas, err := (&Source{Home: home}).Enumerate(context.Background(), reader.EnumerateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Fatalf("got %d sessions, want 2", len(metas))
	}
	live, legacy := metas[0], metas[1]
	updated := time.UnixMilli(1791460800300).UTC()
	created := time.UnixMilli(1791460800000).UTC()
	if live.ID != fixtureID || live.Provider != "kimi" || live.State != session.SessionStateEnded ||
		live.CWD != "/work/demo" || live.Project != "demo" || live.Title != "List files" || live.Name != "List files" ||
		live.Model != model || live.StartedAt == nil || !live.StartedAt.Equal(created) ||
		live.LastActivityAt == nil || !live.LastActivityAt.Equal(updated) || !live.UpdatedAt.Equal(updated) ||
		live.RolloutPath != session.KimiWirePath(filepath.Join(home, "sessions", "wd_demo_0123456789ab", fixtureID)) {
		t.Errorf("live session: %+v", live)
	}
	// No state.json: cwd and start time come from the log head.
	legacyStart := time.UnixMilli(1791460800000 - 86400000).UTC()
	if legacy.ID != legacyID || legacy.CWD != "/work/legacy" || legacy.Project != "legacy" || legacy.Title != "" ||
		legacy.Model != "" || legacy.StartedAt == nil || !legacy.StartedAt.Equal(legacyStart) ||
		legacy.LastActivityAt == nil || !legacy.LastActivityAt.Equal(mtime) {
		t.Errorf("legacy session: %+v", legacy)
	}
	for _, m := range metas {
		got, err := (&Source{Home: home}).Locate(m)
		if err != nil || len(got) != 1 || got[0] != m.RolloutPath {
			t.Errorf("%s: Locate = %v, %v; want the enumerated path", m.ID, got, err)
		}
	}
}

func TestEnumerateModifiedAfter(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	home := copyFixtures(t, old)
	recent := session.KimiWirePath(filepath.Join(home, "sessions", "wd_legacy_ba9876543210", legacyID))
	now := old.Add(48 * time.Hour)
	if err := os.Chtimes(recent, now, now); err != nil {
		t.Fatal(err)
	}
	metas, err := (&Source{Home: home}).Enumerate(context.Background(), reader.EnumerateOptions{ModifiedAfter: old.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != legacyID {
		t.Fatalf("got %+v, want only the recently written session", metas)
	}
}

// Skip sees the working directory from state.json before the log is opened,
// and from the log head when there is no state.json.
func TestEnumerateSkip(t *testing.T) {
	home := copyFixtures(t, time.Now())
	liveLog := session.KimiWirePath(filepath.Join(home, "sessions", "wd_demo_0123456789ab", fixtureID))
	// Reading this log would count it as corrupt, so no warning proves Skip
	// ran without opening it.
	if err := os.WriteFile(liveLog, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	metas, err := (&Source{Home: home}).Enumerate(context.Background(), reader.EnumerateOptions{Skip: func(m session.Meta) bool {
		calls[m.ID]++
		if m.CWD == "" || m.Project == "" {
			t.Errorf("%s: Skip called without a working directory", m.ID)
		}
		return true
	}})
	if err != nil {
		t.Fatalf("Enumerate: %v (the skipped session's log must not be opened)", err)
	}
	if len(metas) != 0 {
		t.Fatalf("got %d sessions, want all skipped", len(metas))
	}
	if calls[fixtureID] != 1 || calls[legacyID] != 1 {
		t.Fatalf("Skip calls = %v, want one per session", calls)
	}
}

func TestEnumerateCorrupt(t *testing.T) {
	home := copyFixtures(t, time.Now())
	for name, body := range map[string]string{"session_empty": "", "session_garbage": "not json\n[1]\n"} {
		path := session.KimiWirePath(filepath.Join(home, "sessions", "wd_x", name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A directory where the log belongs, and a log with an unusable id, are
	// not sessions at all.
	if err := os.MkdirAll(session.KimiWirePath(filepath.Join(home, "sessions", "wd_x", "session_dir")), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sessions", "wd_x", "state.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	metas, err := (&Source{Home: home}).Enumerate(context.Background(), reader.EnumerateOptions{})
	var warning *session.Warning
	if !errors.As(err, &warning) || !strings.Contains(warning.Message, "skipped 2") {
		t.Fatalf("err = %v, want a warning about 2 transcripts", err)
	}
	if len(metas) != 2 {
		t.Fatalf("got %d usable sessions, want 2", len(metas))
	}
}

func TestEnumerateMissingHome(t *testing.T) {
	metas, err := (&Source{Home: filepath.Join(t.TempDir(), "missing")}).Enumerate(context.Background(), reader.EnumerateOptions{})
	if err != nil || len(metas) != 0 {
		t.Fatalf("got %v, %v; want nothing", metas, err)
	}
}

func TestEnumerateCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Source{Home: copyFixtures(t, time.Now())}).Enumerate(ctx, reader.EnumerateOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
