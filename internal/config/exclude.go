package config

import (
	"os"
	"path/filepath"
	"strings"
)

// Excluder decides whether a session's directory is excluded by config.
type Excluder struct {
	dirs    []string
	remotes []string
}

// NewExcluder splits patterns into directory globs and Git remote patterns.
// A pattern is a remote pattern when it contains "://" or "@", or looks like
// host/path (first element contains a dot) and is not absolute or "~"-based.
// Directory globs match a directory or any directory beneath it; "~" expands
// to home. Remote patterns use "*" wildcards matched against the remote URL
// as written and in normalized host/path form.
func NewExcluder(patterns []string, home string) *Excluder {
	e := &Excluder{}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if isRemotePattern(p) {
			e.remotes = append(e.remotes, strings.ToLower(p))
			continue
		}
		if p == "~" || strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
		e.dirs = append(e.dirs, filepath.Clean(p))
	}
	return e
}

func isRemotePattern(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || strings.HasPrefix(p, ".") {
		return false
	}
	if strings.Contains(p, "://") || strings.Contains(p, "@") {
		return true
	}
	first, _, _ := strings.Cut(p, "/")
	return strings.Contains(first, ".")
}

// Excluded reports whether cwd or its Git root matches an exclude pattern,
// and which pattern did. It only reads directory metadata and .git/config.
func (e *Excluder) Excluded(cwd string) (string, bool) {
	if e == nil || cwd == "" || (len(e.dirs) == 0 && len(e.remotes) == 0) {
		return "", false
	}
	cwd = filepath.Clean(cwd)
	root, remotes := gitInfo(cwd)
	for _, start := range []string{cwd, root} {
		if start == "" {
			continue
		}
		for dir := start; ; dir = filepath.Dir(dir) {
			for _, pat := range e.dirs {
				if ok, _ := filepath.Match(pat, dir); ok {
					return pat, true
				}
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	for _, remote := range remotes {
		forms := []string{strings.ToLower(remote), normalizeRemote(remote)}
		for _, pat := range e.remotes {
			for _, f := range forms {
				if wildcard(pat, f) {
					return pat, true
				}
			}
		}
	}
	return "", false
}

// gitInfo finds the Git root above dir and the remote URLs in its config.
func gitInfo(dir string) (string, []string) {
	for ; ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(filepath.Join(dir, ".git"))
		if err == nil {
			if !info.IsDir() {
				return dir, nil
			}
			data, _ := os.ReadFile(filepath.Join(dir, ".git", "config"))
			return dir, parseRemotes(string(data))
		}
		if filepath.Dir(dir) == dir {
			return "", nil
		}
	}
}

func parseRemotes(config string) []string {
	var urls []string
	inRemote := false
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inRemote = strings.HasPrefix(line, "[remote ")
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && inRemote && strings.TrimSpace(k) == "url" {
			urls = append(urls, strings.Trim(strings.TrimSpace(v), `"`))
		}
	}
	return urls
}

// normalizeRemote turns any Git remote URL into lowercase host/path.
func normalizeRemote(u string) string {
	if _, rest, ok := strings.Cut(u, "://"); ok {
		u = rest
	}
	if at := strings.Index(u, "@"); at >= 0 && at < strings.IndexAny(u+"/", "/") {
		u = u[at+1:]
	}
	u = strings.Replace(u, ":", "/", 1)
	u = strings.TrimSuffix(strings.TrimRight(u, "/"), ".git")
	return strings.ToLower(u)
}

// wildcard matches s against pattern where "*" matches any run of characters.
func wildcard(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}
