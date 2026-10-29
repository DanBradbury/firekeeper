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
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
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
	metas, err := source.Enumerate(context.Background(), reader.EnumerateOptions{})
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
	metas, err = source.Enumerate(context.Background(), reader.EnumerateOptions{ModifiedAfter: old})
	if err != nil || len(metas) != 1 {
		t.Fatal("modification cutoff not applied")
	}
	skipped := 0
	metas, err = source.Enumerate(context.Background(), reader.EnumerateOptions{Skip: func(m session.Meta) bool {
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

func TestEnumerateAttachesSummaryEstimate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	dir := filepath.Join(home, ".claude", "projects", "-encoded")
	head := "{\"cwd\":\"/work/repo\",\"timestamp\":\"2026-10-29T12:00:00Z\",\"message\":{\"model\":\"claude-test\"}}\n"
	writeHistorical(t, dir, fixtureID, head, time.Date(2026, 10, 29, 13, 0, 0, 0, time.UTC))
	sub := filepath.Join(dir, fixtureID, "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{
		`{"type":"user","timestamp":"2026-10-29T12:00:00Z","message":{"role":"user","content":"synthetic-prompt-marker"}}`,
		`{"type":"assistant","timestamp":"2026-10-29T12:00:10Z","message":{"id":"msg_sub","usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":900,"cache_creation_input_tokens":0}}}`,
		`{"type":"user","timestamp":"2026-10-29T12:01:00Z","message":{"role":"user","content":"synthetic-prompt-marker"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(sub, "agent-alpha.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	metas, err := (&Source{Home: home}).Enumerate(context.Background(), reader.EnumerateOptions{})
	if err != nil || len(metas) != 1 {
		t.Fatalf("count = %d, error = %v", len(metas), err)
	}
	est := metas[0].AgentSummary
	if est == nil || est.Calls != 2 || est.Tokens != 2000 || est.CacheRead != 2000 || metas[0].Input != 0 || metas[0].EventCount != 0 {
		calls, tokens := int64(0), int64(0)
		if est != nil {
			calls, tokens = est.Calls, est.Tokens
		}
		t.Fatalf("estimate calls=%d tokens=%d measured input=%d events=%d", calls, tokens, metas[0].Input, metas[0].EventCount)
	}
	if strings.Contains(est.Label, "synthetic-prompt-marker") {
		t.Fatal("estimate label retained transcript text")
	}
}

func TestEnumeratePartialAndMissing(t *testing.T) {
	home := t.TempDir()
	source := &Source{ConfigDir: home}
	metas, err := source.Enumerate(context.Background(), reader.EnumerateOptions{})
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
	metas, err = source.Enumerate(context.Background(), reader.EnumerateOptions{})
	var warning *session.Warning
	if !errors.As(err, &warning) || len(metas) != 1 || metas[0].ID != partialID {
		t.Fatal("corrupt files should leave usable partial metadata")
	}
	if strings.Contains(err.Error(), home) || strings.Contains(err.Error(), "SYNTHETIC_PRIVATE_MARKER") {
		t.Fatal("warning exposed private data")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Enumerate(ctx, reader.EnumerateOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
}

func TestSessionHeadBounded(t *testing.T) {
	dir := t.TempDir()
	body := "{\"cwd\":\"/synthetic/project\"}\n" + strings.Repeat(" \n", reader.HeadBytes) + "{\"gitBranch\":\"past-limit\"}\n"
	path := writeHistorical(t, dir, fixtureID, body, time.Now())
	meta, err := readSessionHead(context.Background(), path)
	if err != nil || meta.CWD != "/synthetic/project" || meta.Branch != "" {
		t.Fatal("head read exceeded limit")
	}
}
