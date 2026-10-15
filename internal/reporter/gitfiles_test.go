package reporter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/testenv"
)

// gitT0 is the synthetic repository's base time, in unix seconds.
const gitT0 = 1_700_000_000

// isolateGit keeps git away from the developer's own configuration.
func isolateGit(t *testing.T) {
	t.Helper()
	testenv.NeedTool(t, "git")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_AUTHOR_NAME", "Test User")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Test User")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
}

// gitAt runs git in dir with author and committer dates, and so the HEAD
// reflog entry, at unix time at.
func gitAt(t *testing.T, dir string, at int64, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	date := fmt.Sprintf("@%d +0000", at)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeAt writes a file under root and sets its modification time.
func writeAt(t *testing.T, root, name, body string, at int64) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := time.Unix(at, 0)
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
}

// sessionRepo builds a repository where a session started at gitT0+10 from
// the first commit, then committed a change to a.go and a new c.go at
// gitT0+20, left b.go modified and new.txt untracked, and was last active
// at gitT0+40. old.txt was untracked before the session; debug.log is
// ignored.
func sessionRepo(t *testing.T) (root, start string) {
	t.Helper()
	isolateGit(t)
	root = filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, root, gitT0, "init", "-q", "-b", "main")
	writeAt(t, root, "a.go", "package a\n", gitT0)
	writeAt(t, root, "b.go", "package b\n", gitT0)
	writeAt(t, root, "sub/d.go", "package d\n", gitT0)
	writeAt(t, root, ".gitignore", "*.log\n", gitT0)
	gitAt(t, root, gitT0, "add", ".")
	gitAt(t, root, gitT0, "commit", "-q", "-m", "initial")
	start = gitAt(t, root, gitT0, "rev-parse", "HEAD")
	writeAt(t, root, "old.txt", "before the session\n", gitT0)

	writeAt(t, root, "a.go", "package a // changed\n", gitT0+20)
	writeAt(t, root, "c.go", "package c\n", gitT0+20)
	gitAt(t, root, gitT0+20, "add", "a.go", "c.go")
	gitAt(t, root, gitT0+20, "commit", "-q", "-m", "during the session")
	writeAt(t, root, "b.go", "package b // uncommitted\n", gitT0+30)
	writeAt(t, root, "new.txt", "untracked\n", gitT0+30)
	writeAt(t, root, "debug.log", "ignored\n", gitT0+30)
	return root, start
}

func names(root string, files []string) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		rel, err := filepath.Rel(root, f)
		if err != nil {
			rel = f
		}
		out = append(out, filepath.ToSlash(rel))
	}
	slices.Sort(out)
	return out
}

func TestGitChangedFiles(t *testing.T) {
	root, start := sessionRepo(t)
	started, last := unixTime(gitT0+10), unixTime(gitT0+40)
	ctx := context.Background()

	t.Run("HEAD unchanged since last activity", func(t *testing.T) {
		files, ok := gitChangedFiles(ctx, root, start, started, last)
		if !ok {
			t.Fatal("ok = false")
		}
		if got, want := names(root, files), []string{"a.go", "b.go", "c.go", "new.txt"}; !slices.Equal(got, want) {
			t.Fatalf("files = %q, want %q", got, want)
		}
	})

	t.Run("cwd in a subdirectory", func(t *testing.T) {
		files, ok := gitChangedFiles(ctx, filepath.Join(root, "sub"), start, started, last)
		if !ok {
			t.Fatal("ok = false")
		}
		if got, want := names(root, files), []string{"a.go", "b.go", "c.go", "new.txt"}; !slices.Equal(got, want) {
			t.Fatalf("files = %q, want %q", got, want)
		}
	})

	t.Run("HEAD moved after last activity", func(t *testing.T) {
		writeAt(t, root, "later.go", "package later\n", gitT0+100)
		gitAt(t, root, gitT0+100, "add", "later.go")
		gitAt(t, root, gitT0+100, "commit", "-q", "-m", "after the session")
		files, ok := gitChangedFiles(ctx, root, start, started, last)
		if !ok {
			t.Fatal("ok = false")
		}
		if got, want := names(root, files), []string{"a.go", "c.go"}; !slices.Equal(got, want) {
			t.Fatalf("files = %q, want %q", got, want)
		}
	})
}

func TestGitChangedFilesUnavailable(t *testing.T) {
	root, start := sessionRepo(t)
	started, last := unixTime(gitT0+10), unixTime(gitT0+40)
	ctx := context.Background()
	tests := []struct {
		name, cwd, start string
	}{
		{"outside a repository", t.TempDir(), start},
		{"no start commit", root, ""},
		{"start commit not in the repository", root, hashA},
		{"no cwd", "", start},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if files, ok := gitChangedFiles(ctx, tt.cwd, tt.start, started, last); ok || files != nil {
				t.Fatalf("got (%q, %v), want no list", files, ok)
			}
		})
	}
}

func TestGitChangedFilesNoChanges(t *testing.T) {
	isolateGit(t)
	root := t.TempDir()
	gitAt(t, root, gitT0, "init", "-q", "-b", "main")
	writeAt(t, root, "a.go", "package a\n", gitT0)
	gitAt(t, root, gitT0, "add", ".")
	gitAt(t, root, gitT0, "commit", "-q", "-m", "initial")
	start := gitAt(t, root, gitT0, "rev-parse", "HEAD")
	files, ok := gitChangedFiles(context.Background(), root, start, unixTime(gitT0+10), unixTime(gitT0+20))
	if !ok || files == nil || len(files) != 0 {
		t.Fatalf("got (%q, %v), want an empty list", files, ok)
	}
}

func TestSplitNUL(t *testing.T) {
	got := splitNUL([]byte("a.go\x00dir/b c.go\x00\x00"))
	if want := []string{"a.go", "dir/b c.go"}; !slices.Equal(got, want) {
		t.Fatalf("splitNUL = %q, want %q", got, want)
	}
	if got := splitNUL(nil); got != nil {
		t.Fatalf("splitNUL(nil) = %q", got)
	}
}

// Regression: a session that changed most files through shell commands
// listed only the files its edit tool calls named. With a Git repository
// the uploaded list is Git's, and tool-call counts stay on the files they
// also named.
func TestReportSendsGitFileList(t *testing.T) {
	isolateGit(t)
	f := newFixture(t)
	if err := os.RemoveAll(filepath.Join(f.repo, ".git")); err != nil {
		t.Fatal(err)
	}
	gitAt(t, f.repo, gitT0, "init", "-q", "-b", "main")
	writeAt(t, f.repo, "main.go", "package main\n", gitT0)
	writeAt(t, f.repo, "util.go", "package main\n", gitT0)
	gitAt(t, f.repo, gitT0, "add", ".")
	gitAt(t, f.repo, gitT0, "commit", "-q", "-m", "initial")
	start := gitAt(t, f.repo, gitT0, "rev-parse", "HEAD")
	// main.go through apply_patch; util.go and gen/out.go as a shell
	// command or script would, with no tool call naming them.
	writeAt(t, f.repo, "main.go", "package main // patched\n", gitT0+20)
	writeAt(t, f.repo, "util.go", "package main // sed -i\n", gitT0+20)
	writeAt(t, f.repo, "gen/out.go", "package gen\n", gitT0+20)

	f.meta.StartedAt, f.meta.LastActivityAt = unixTime(gitT0+10), unixTime(gitT0+30)
	patch := "*** Begin Patch\n*** Update File: " + filepath.Join(f.repo, "main.go") + "\n*** End Patch\n"
	rec, _ := json.Marshal(map[string]any{
		"timestamp": "2023-11-14T22:13:40.000Z",
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
	if se.Commit != start || se.FilesSource != store.FilesFromGit {
		t.Fatalf("session = (commit %q, files_source %q), want (%s, git)", se.Commit, se.FilesSource, start)
	}
	files, err := srv.store.ListFiles(ctx, store.DefaultAccountID, testMachine, testSession)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, file := range files {
		got = append(got, fmt.Sprintf("%s:%d", file.Path, file.Changes))
	}
	if want := []string{"gen/out.go:0", "main.go:1", "util.go:0"}; !slices.Equal(got, want) {
		t.Fatalf("files = %q, want %q", got, want)
	}
}

// Outside a Git repository the server keeps the tool-call list and marks
// it as such.
func TestReportWithoutGitKeepsToolCallFiles(t *testing.T) {
	f := newFixture(t)
	if err := os.RemoveAll(filepath.Join(f.repo, ".git")); err != nil {
		t.Fatal(err)
	}
	f.meta.StartedAt = unixTime(gitT0 + 10)
	patch := "*** Begin Patch\n*** Update File: main.go\n*** End Patch\n"
	rec, _ := json.Marshal(map[string]any{
		"timestamp": "2023-11-14T22:13:40.000Z",
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
	if se.FilesSource != store.FilesFromToolCalls {
		t.Fatalf("files_source = %q, want tool_calls", se.FilesSource)
	}
	files, err := srv.store.ListFiles(ctx, store.DefaultAccountID, testMachine, testSession)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "main.go" || files[0].Changes != 1 {
		t.Fatalf("files = %+v", files)
	}
}
