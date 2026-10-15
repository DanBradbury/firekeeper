package cursor

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/testenv"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// projectDirName encodes a workspace path the way Cursor names its project
// directories.
func projectDirName(path string) string {
	return strings.TrimLeft(strings.ReplaceAll(filepath.ToSlash(path), "/", "-"), "-")
}

// writeChat creates a chat database holding meta as Cursor stores it:
// hex-encoded JSON in row "0" of the meta table.
func writeChat(t *testing.T, path, meta string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	sql := "CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT); CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB);" +
		"INSERT INTO meta VALUES ('0', '" + hex.EncodeToString([]byte(meta)) + "');"
	if out, err := exec.Command("sqlite3", path, sql).CombinedOutput(); err != nil {
		t.Fatalf("create chat database: %v: %s", err, out)
	}
}

func writeTranscript(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	write(t, path, `{"role":"user","message":{"content":[{"type":"text","text":"<user_query>hi</user_query>"}]}}`+"\n")
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestEnumerateHistory(t *testing.T) {
	testenv.NeedTool(t, "sqlite3")
	dir := t.TempDir()
	repo := filepath.Join(t.TempDir(), "my-repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	const otherID = "1a2b3c4d-0000-4000-8000-000000000001"
	const goneID = "2b3c4d5e-0000-4000-8000-000000000002"
	transcripts := filepath.Join(dir, "projects", projectDirName(repo), "agent-transcripts")
	old := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	path := filepath.Join(transcripts, fixtureID, fixtureID+".jsonl")
	writeTranscript(t, path, newer)
	// An older flat transcript, and a subagent that is never listed.
	writeTranscript(t, filepath.Join(transcripts, otherID+".jsonl"), old)
	writeTranscript(t, filepath.Join(transcripts, fixtureID, "subagents", goneID+".jsonl"), newer)
	// A project whose workspace no longer exists has no working directory.
	writeTranscript(t, filepath.Join(dir, "projects", "nowhere-at-all", "agent-transcripts", goneID, goneID+".jsonl"), old)
	writeChat(t, filepath.Join(session.CursorChatsDir(dir, repo), fixtureID, "store.db"),
		`{"agentId":"`+fixtureID+`","name":"Fix the build","createdAt":1788220800000,"mode":"default","lastUsedModel":"claude-4-sonnet","latestRootBlobId":"ab"}`)
	// Chat metadata for another session is not trusted.
	writeChat(t, filepath.Join(session.CursorChatsDir(dir, repo), otherID, "store.db"), `{"agentId":"`+goneID+`"}`)

	source := &Source{Dir: dir}
	metas, err := source.Enumerate(context.Background(), reader.EnumerateOptions{})
	var warning *session.Warning
	if !errors.As(err, &warning) || len(metas) != 3 {
		t.Fatalf("count = %d, error = %v; want 3 and a warning for the mismatched chat", len(metas), err)
	}
	m := metas[0]
	started := time.UnixMilli(1788220800000)
	if m.ID != fixtureID || m.Provider != "cursor" || m.RolloutPath != path || m.CWD != repo || m.Project != "my-repo" ||
		m.Title != "Fix the build" || m.Model != "claude-4-sonnet" || m.State != session.SessionStateEnded ||
		m.StartedAt == nil || !m.StartedAt.Equal(started) || !m.LastActivityAt.Equal(newer) {
		t.Fatalf("historical metadata incorrect: %+v", m)
	}
	byID := map[string]session.Meta{}
	for _, m := range metas {
		byID[m.ID] = m
	}
	if m := byID[otherID]; m.CWD != repo || m.Title != "" {
		t.Errorf("flat transcript: cwd %q title %q", m.CWD, m.Title)
	}
	if m := byID[goneID]; m.CWD != "" || m.Project != "" {
		t.Errorf("missing workspace: cwd %q project %q", m.CWD, m.Project)
	}
	paths, err := source.Locate(m)
	if err != nil || len(paths) != 1 || paths[0] != path {
		t.Fatal("enumerated transcript not located")
	}

	metas, _ = source.Enumerate(context.Background(), reader.EnumerateOptions{ModifiedAfter: old})
	if len(metas) != 1 || metas[0].ID != fixtureID {
		t.Fatal("modification cutoff not applied")
	}
	// Skip runs before the chat database is read, once per session.
	skipped := 0
	metas, err = source.Enumerate(context.Background(), reader.EnumerateOptions{Skip: func(m session.Meta) bool {
		skipped++
		if m.Title != "" {
			t.Error("chat metadata read before Skip")
		}
		return true
	}})
	if err != nil || len(metas) != 0 || skipped != 3 {
		t.Fatalf("skip not applied once per session: %d skipped, %v", skipped, err)
	}
}

func TestEnumerateEmpty(t *testing.T) {
	metas, err := (&Source{Dir: filepath.Join(t.TempDir(), "missing")}).Enumerate(context.Background(), reader.EnumerateOptions{})
	if err != nil || len(metas) != 0 {
		t.Fatalf("got %d, %v", len(metas), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Source{Dir: t.TempDir()}).Enumerate(ctx, reader.EnumerateOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}
