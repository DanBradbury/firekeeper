package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

func writeHistorical(t *testing.T, dir, id, content string, mtime time.Time) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEnumerateHistory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	project := filepath.Join(home, "repo")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	head, _ := json.Marshal(map[string]any{"cwd": project, "gitBranch": "main", "timestamp": "2026-01-01T00:00:00Z", "message": map[string]string{"model": "claude-test"}})
	body := "malformed synthetic record\n" + string(head) + "\n"
	dir := filepath.Join(home, ".claude", "projects", "-encoded")
	old := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	const otherID = "0199c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6e"
	writeHistorical(t, dir, fixtureID, body, old)
	path := writeHistorical(t, filepath.Join(home, ".claude", "projects", "-duplicate"), fixtureID, body, newer)
	writeHistorical(t, dir, otherID, body, old)
	writeHistorical(t, filepath.Join(dir, fixtureID, "subagents"), "agent-ignored", body, newer)
	source := &Source{Home: home}
	metas, err := source.Enumerate(context.Background(), transcript.EnumerateOptions{})
	if err != nil || len(metas) != 2 {
		t.Fatalf("count = %d, error = %v", len(metas), err)
	}
	m := metas[0]
	if m.ID != fixtureID || m.RolloutPath != path || m.CWD != project || m.Project != "repo" || m.Branch != "main" || m.Model != "claude-test" || m.State != session.SessionStateEnded || m.StartedAt == nil || !m.LastActivityAt.Equal(newer) {
		t.Fatal("historical metadata incorrect")
	}
	paths, err := source.Locate(m)
	if err != nil || len(paths) != 1 || paths[0] != path {
		t.Fatal("enumerated transcript not located")
	}
	metas, err = source.Enumerate(context.Background(), transcript.EnumerateOptions{ModifiedAfter: old})
	if err != nil || len(metas) != 1 {
		t.Fatal("modification cutoff not applied")
	}
	skipped := 0
	metas, err = source.Enumerate(context.Background(), transcript.EnumerateOptions{Skip: func(m session.Meta) bool {
		skipped++
		if m.CWD != project {
			t.Fatal("exclusion lacks cwd")
		}
		return true
	}})
	if err != nil || len(metas) != 0 || skipped != 2 {
		t.Fatal("skip not applied once per session")
	}
}

func TestEnumeratePartialAndMissing(t *testing.T) {
	home := t.TempDir()
	source := &Source{ConfigDir: home}
	metas, err := source.Enumerate(context.Background(), transcript.EnumerateOptions{})
	if err != nil || len(metas) != 0 {
		t.Fatal("missing history should be empty")
	}
	dir := filepath.Join(home, "projects", "-encoded")
	now := time.Now()
	writeHistorical(t, dir, fixtureID, "", now)
	const corruptID = "0199c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6f"
	writeHistorical(t, dir, corruptID, "SYNTHETIC_PRIVATE_MARKER\n", now)
	const partialID = "0199c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c60"
	writeHistorical(t, dir, partialID, "{\"type\":\"future\",\"cwd\":\"/synthetic/project\"}\n{unfinished", now)
	metas, err = source.Enumerate(context.Background(), transcript.EnumerateOptions{})
	var warning *session.Warning
	if !errors.As(err, &warning) || len(metas) != 1 || metas[0].ID != partialID {
		t.Fatal("corrupt files should leave usable partial metadata")
	}
	if strings.Contains(err.Error(), home) || strings.Contains(err.Error(), "SYNTHETIC_PRIVATE_MARKER") {
		t.Fatal("warning exposed private data")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Enumerate(ctx, transcript.EnumerateOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
}

func TestSessionHeadBounded(t *testing.T) {
	dir := t.TempDir()
	body := "{\"cwd\":\"/synthetic/project\"}\n" + strings.Repeat(" \n", transcript.HeadBytes) + "{\"gitBranch\":\"past-limit\"}\n"
	path := writeHistorical(t, dir, fixtureID, body, time.Now())
	meta, err := readSessionHead(context.Background(), path)
	if err != nil || meta.CWD != "/synthetic/project" || meta.Branch != "" {
		t.Fatal("head read exceeded limit")
	}
}
