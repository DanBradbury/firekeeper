package reporter

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/DanBradbury/firekeeper/internal/server/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	hashA = "1111111111111111111111111111111111111111"
	hashB = "2222222222222222222222222222222222222222"
	hashC = "3333333333333333333333333333333333333333"
	zero  = "0000000000000000000000000000000000000000"
)

// reflogEntry formats one synthetic HEAD reflog line.
func reflogEntry(old, new string, unix int64, msg string) string {
	return fmt.Sprintf("%s %s Test User <test@example.invalid> %d +0000\t%s\n", old, new, unix, msg)
}

// writeRepo creates gitDir with HEAD and a HEAD reflog.
func writeRepo(t *testing.T, gitDir, head string, reflog ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(gitDir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(head+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "logs", "HEAD"), []byte(strings.Join(reflog, "")), 0o644); err != nil {
		t.Fatal(err)
	}
}

func unixTime(sec int64) *time.Time {
	t := time.Unix(sec, 500).UTC()
	return &t
}

func TestGitAtStart(t *testing.T) {
	log := []string{
		reflogEntry(zero, hashA, 100, "clone: from somewhere"),
		reflogEntry(hashA, hashB, 200, "commit: second"),
		reflogEntry(hashB, hashC, 300, "commit: third"),
	}
	tests := []struct {
		name         string
		head         string
		reflog       []string
		started      *time.Time
		branch, want string
	}{
		{"before first entry", "ref: refs/heads/main", log, unixTime(50), "main", ""},
		{"at an entry", "ref: refs/heads/main", log, unixTime(200), "main", hashB},
		{"between entries", "ref: refs/heads/main", log, unixTime(250), "main", hashB},
		{"after last entry", "ref: refs/heads/main", log, unixTime(400), "main", hashC},
		{"no start time", "ref: refs/heads/main", log, nil, "", ""},
		{"detached head", hashC, log, unixTime(400), "", hashC},
		{"checkout since start hides branch", "ref: refs/heads/feature/x",
			append(append([]string{}, log...), reflogEntry(hashC, hashA, 500, "checkout: moving from main to feature/x")),
			unixTime(400), "", hashC},
		{"malformed lines skipped", "ref: refs/heads/main",
			[]string{"garbage\n", reflogEntry(zero, "nothex", 100, "x"), reflogEntry(zero, hashA, 100, "x"), "\n"},
			unixTime(150), "main", hashA},
		{"no reflog", "ref: refs/heads/main", nil, unixTime(150), "main", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			writeRepo(t, filepath.Join(repo, ".git"), tt.head, tt.reflog...)
			if tt.reflog == nil {
				os.Remove(filepath.Join(repo, ".git", "logs", "HEAD"))
			}
			sub := filepath.Join(repo, "pkg", "deep")
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			branch, commit := gitAtStart(sub, tt.started)
			if branch != tt.branch || commit != tt.want {
				t.Fatalf("got (%q, %q), want (%q, %q)", branch, commit, tt.branch, tt.want)
			}
		})
	}
}

func TestGitAtStartWorktreeAndMissing(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, "main.git", "worktrees", "wt")
	writeRepo(t, gitDir, "ref: refs/heads/topic", reflogEntry(zero, hashB, 100, "reset: moving to HEAD"))
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: ../main.git/worktrees/wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, c := gitAtStart(wt, unixTime(150)); b != "topic" || c != hashB {
		t.Fatalf("worktree: got (%q, %q)", b, c)
	}
	if b, c := gitAtStart(t.TempDir(), unixTime(150)); b != "" || c != "" {
		t.Fatalf("no repository: got (%q, %q)", b, c)
	}
	if b, c := gitAtStart("", unixTime(150)); b != "" || c != "" {
		t.Fatalf("no cwd: got (%q, %q)", b, c)
	}
}

func TestReflogTailOnly(t *testing.T) {
	// A reflog over the read limit is read from its tail, skipping the
	// partial first line.
	dir := t.TempDir()
	var b strings.Builder
	pad := strings.Repeat("x", 200)
	for b.Len() < maxReflogBytes+4096 {
		b.WriteString(reflogEntry(zero, hashA, 100, pad))
	}
	b.WriteString(reflogEntry(hashA, hashB, 200, "commit: last"))
	path := filepath.Join(dir, "HEAD")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, _ := reflogAt(path, 150); c != hashA {
		t.Fatalf("at 150: %q", c)
	}
	if c, _ := reflogAt(path, 250); c != hashB {
		t.Fatalf("at 250: %q", c)
	}
}

func TestReportSendsGitContextAndFiles(t *testing.T) {
	f := newFixture(t)
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f.meta.StartedAt = &started
	f.meta.Branch = ""
	writeRepo(t, filepath.Join(f.repo, ".git"), "ref: refs/heads/main",
		reflogEntry(zero, hashA, started.Unix()-60, "commit (initial): start"),
		reflogEntry(hashA, hashB, started.Unix()+60, "commit: during the session"))

	patch := "*** Begin Patch\n*** Update File: " + filepath.Join(f.repo, "main.go") + "\n*** Add File: docs/new.md\n*** End Patch\n"
	rec, _ := json.Marshal(map[string]any{
		"timestamp": "2026-01-02T03:04:06.000Z",
		"type":      "response_item",
		"payload":   map[string]any{"type": "custom_tool_call", "call_id": "c1", "name": "apply_patch", "input": patch},
	})
	if err := os.WriteFile(f.rollout, append(rec, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(t)
	if _, err := RunOnce(context.Background(), f.config(srv.URL)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	se, err := srv.store.GetSession(ctx, store.DefaultAccountID, testMachine, testSession)
	if err != nil {
		t.Fatal(err)
	}
	if se.Commit != hashA || se.Branch != "main" {
		t.Fatalf("session git context = (%q, %q), want (main, %s)", se.Branch, se.Commit, hashA)
	}
	if se.CWD != "~/src/demo" {
		t.Fatalf("cwd = %q, want it redacted", se.CWD)
	}
	files, err := srv.store.ListFiles(ctx, store.DefaultAccountID, testMachine, testSession)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	if strings.Join(paths, ",") != "docs/new.md,main.go" {
		t.Fatalf("files = %q", paths)
	}
}
