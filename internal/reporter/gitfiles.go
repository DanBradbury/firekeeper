package reporter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Bounds on the Git file list. A session's list rides along with every
// ingest batch, so it stays far below the request size limit.
const (
	maxGitFiles       = 5000
	maxGitListBytes   = 512 << 10
	maxGitPathBytes   = 1024
	maxGitOutput      = 4 << 20
	gitCommandTimeout = 10 * time.Second
)

// errGitOutput marks git output larger than maxGitOutput.
var errGitOutput = errors.New("git output too large")

// gitChangedFiles lists the files changed in the repository containing cwd
// since start, the commit HEAD held when the session started (see
// gitAtStart). Paths are absolute. ok is false when the list cannot be
// established: cwd is outside a repository, start is unknown or no longer
// in the repository, or git is missing or fails.
//
// The end of the range is the commit HEAD held at the session's last
// activity. While HEAD still points there, uncommitted changes and
// untracked files written since the session started count too, so work in
// progress shows up; once HEAD has moved on, only start..end counts, so
// later work in the same checkout is left out. Changes the user made in the
// same checkout during the session are counted with the agent's.
func gitChangedFiles(ctx context.Context, cwd, start string, started, last *time.Time) (files []string, ok bool) {
	if cwd == "" || !isHash(start) {
		return nil, false
	}
	root, gitDir := findRepo(cwd)
	if root == "" || gitDir == "" {
		return nil, false
	}
	head, err := runGit(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return nil, false
	}
	current := string(bytes.TrimSpace(head))
	end := current
	if last != nil && !last.IsZero() {
		if c, _ := reflogAt(filepath.Join(gitDir, "logs", "HEAD"), last.Unix()); c != "" {
			end = c
		}
	}

	var changed []string
	if end == current {
		out, err := runGit(ctx, root, "diff", "--name-only", "-z", "--no-ext-diff", "--no-textconv", start, "--")
		if err != nil {
			return nil, false
		}
		changed = splitNUL(out)
		out, err = runGit(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return nil, false
		}
		for _, p := range splitNUL(out) {
			if newSince(filepath.Join(root, filepath.FromSlash(p)), started) {
				changed = append(changed, p)
			}
		}
	} else {
		out, err := runGit(ctx, root, "diff", "--name-only", "-z", "--no-ext-diff", "--no-textconv", start, end, "--")
		if err != nil {
			return nil, false
		}
		changed = splitNUL(out)
	}

	seen := make(map[string]bool, len(changed))
	files = []string{}
	size := 0
	for _, p := range changed {
		if p == "" || len(p) > maxGitPathBytes || seen[p] {
			continue
		}
		seen[p] = true
		abs := filepath.Join(root, filepath.FromSlash(p))
		size += len(abs)
		if len(files) == maxGitFiles || size > maxGitListBytes {
			break
		}
		files = append(files, abs)
	}
	return files, true
}

// newSince reports whether the file at path was modified at or after
// started. With no start time every file counts.
func newSince(path string, started *time.Time) bool {
	if started == nil || started.IsZero() {
		return true
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return !info.ModTime().Before(started.Truncate(time.Second))
}

// runGit runs a read-only git command in root and returns its stdout. Git
// is told not to take optional locks, so it never rewrites the index, and
// not to run an fsmonitor hook, so a repository's config cannot make the
// reporter start a program. Stderr is discarded; it can name paths.
func runGit(ctx context.Context, root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	base := []string{"-C", root, "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.quotePath=false"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, maxGitOutput+1))
	if len(out) > maxGitOutput {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errGitOutput
	}
	if err := cmd.Wait(); err != nil {
		return nil, err
	}
	return out, readErr
}

// splitNUL splits -z output into its NUL-terminated fields.
func splitNUL(out []byte) []string {
	var fields []string
	for _, f := range bytes.Split(out, []byte{0}) {
		if len(f) > 0 {
			fields = append(fields, string(f))
		}
	}
	return fields
}
