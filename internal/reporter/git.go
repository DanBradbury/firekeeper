package reporter

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// maxReflogBytes bounds how much of a HEAD reflog is read. Entries are in
// time order, so the tail holds the history around recent sessions.
const maxReflogBytes = 4 << 20

// gitAtStart reads, from the repository containing cwd, the branch and
// commit HEAD pointed at when a session started. It reads files only and
// runs no git command. The commit comes from the newest HEAD reflog entry at
// or before started; the branch is the checked-out branch, reported only
// when no checkout has moved HEAD since started. Anything it cannot
// establish is returned empty, including both values when started is nil.
// Reflog entries name their author; only hashes, times, and the checkout
// marker are read from them.
func gitAtStart(cwd string, started *time.Time) (branch, commit string) {
	if cwd == "" || started == nil || started.IsZero() {
		return "", ""
	}
	_, gitDir := findRepo(cwd)
	if gitDir == "" {
		return "", ""
	}
	head, err := readSmall(filepath.Join(gitDir, "HEAD"), 4096)
	if err != nil {
		return "", ""
	}
	current := ""
	if ref, ok := strings.CutPrefix(strings.TrimSpace(head), "ref: refs/heads/"); ok {
		current = ref
	}
	commit, checkoutSince := reflogAt(filepath.Join(gitDir, "logs", "HEAD"), started.Unix())
	if !checkoutSince {
		branch = current
	}
	return branch, commit
}

// findRepo returns the root of the repository containing dir and its Git
// directory, following a ".git" file (worktrees, submodules) to its gitdir.
// Both are empty outside a repository; gitDir is empty when the ".git" file
// cannot be read.
func findRepo(dir string) (root, gitDir string) {
	dir = filepath.Clean(dir)
	for {
		dotGit := filepath.Join(dir, ".git")
		if info, err := os.Stat(dotGit); err == nil {
			if info.IsDir() {
				return dir, dotGit
			}
			data, err := readSmall(dotGit, 4096)
			if err != nil {
				return dir, ""
			}
			target, ok := strings.CutPrefix(strings.TrimSpace(data), "gitdir: ")
			if !ok || target == "" {
				return dir, ""
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(dir, target)
			}
			return dir, target
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
}

// remoteURLs returns the remote URLs in a repository's config. A worktree's
// Git directory names the shared one in its "commondir" file.
func remoteURLs(gitDir string) []string {
	if common, err := readSmall(filepath.Join(gitDir, "commondir"), 4096); err == nil {
		if c := strings.TrimSpace(common); c != "" {
			if !filepath.IsAbs(c) {
				c = filepath.Join(gitDir, c)
			}
			gitDir = c
		}
	}
	data, err := readSmall(filepath.Join(gitDir, "config"), 1<<20)
	if err != nil {
		return nil
	}
	var urls []string
	inRemote := false
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inRemote = strings.HasPrefix(line, "[remote ")
			continue
		}
		if !inRemote {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "url", "pushurl":
			if v := strings.Trim(strings.TrimSpace(value), `"`); v != "" {
				urls = append(urls, v)
			}
		}
	}
	return urls
}

func readSmall(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit))
	return string(data), err
}

// reflogAt scans a HEAD reflog for the commit HEAD held at unix time at,
// and reports whether a checkout moved HEAD after it.
func reflogAt(path string, at int64) (commit string, checkoutSince bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	skipPartial := false
	if info, err := f.Stat(); err == nil && info.Size() > maxReflogBytes {
		if _, err := f.Seek(info.Size()-maxReflogBytes, io.SeekStart); err != nil {
			return "", false
		}
		skipPartial = true
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		if skipPartial {
			skipPartial = false
			continue
		}
		newHash, ts, msg, ok := parseReflogLine(sc.Bytes())
		if !ok {
			continue
		}
		if ts <= at {
			commit = newHash
			checkoutSince = false
		} else if strings.HasPrefix(msg, "checkout:") {
			checkoutSince = true
		}
	}
	if sc.Err() != nil {
		return "", false
	}
	return commit, checkoutSince
}

// parseReflogLine reads "<old> <new> <name> <<email>> <unix> <tz>\t<msg>".
func parseReflogLine(line []byte) (newHash string, ts int64, msg string, ok bool) {
	head, rest, _ := bytes.Cut(line, []byte("\t"))
	fields := bytes.Fields(head)
	if len(fields) < 4 {
		return "", 0, "", false
	}
	newHash = string(fields[1])
	if !isHash(newHash) {
		return "", 0, "", false
	}
	ts, err := strconv.ParseInt(string(fields[len(fields)-2]), 10, 64)
	if err != nil {
		return "", 0, "", false
	}
	return newHash, ts, string(rest), true
}

// isHash reports whether s is a full SHA-1 or SHA-256 object id other than
// the all-zero id Git uses for "no commit".
func isHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	zero := true
	for _, c := range s {
		switch {
		case c == '0':
		case ('1' <= c && c <= '9') || ('a' <= c && c <= 'f'):
			zero = false
		default:
			return false
		}
	}
	return !zero
}
