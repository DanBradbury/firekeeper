package config

import (
	"path/filepath"
	"strings"
)

// isDirPattern reports whether an exclude entry is a directory glob rather
// than a Git remote pattern.
func isDirPattern(e string) bool {
	return strings.HasPrefix(e, "/") || strings.HasPrefix(e, "~") || filepath.IsAbs(e)
}

// Excluded reports the first exclude entry matching a session, given its
// directories (cwd and Git root) and its repository's remote URLs.
//
// A directory glob matches a directory or any of its ancestors, so
// "~/clients" and "~/clients/*" both exclude everything below ~/clients.
// A remote pattern is compared with each remote URL after both are reduced
// to "host/owner/repo" form (scheme, user, port, and ".git" dropped, and
// "host:path" read as "host/path"); it too matches the remote or any
// leading part of it, so "github.com/acme" excludes every acme repository.
// Globs use filepath.Match syntax, where "*" stays within one segment.
func (c Config) Excluded(dirs, remotes []string) (string, bool) {
	for _, e := range c.Exclude {
		if isDirPattern(e) {
			for _, d := range dirs {
				if d != "" && matchAncestors(e, filepath.Clean(d), filepath.Dir) {
					return e, true
				}
			}
			continue
		}
		pattern := normalizeRemote(e)
		for _, r := range remotes {
			if r = normalizeRemote(r); r != "" && matchAncestors(pattern, r, remoteParent) {
				return e, true
			}
		}
	}
	return "", false
}

// matchAncestors matches pattern against p and each parent of p.
func matchAncestors(pattern, p string, parent func(string) string) bool {
	for {
		if ok, _ := filepath.Match(pattern, p); ok {
			return true
		}
		next := parent(p)
		if next == p || next == "." || next == "" {
			return false
		}
		p = next
	}
}

func remoteParent(p string) string {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return p
	}
	return p[:i]
}

// normalizeRemote reduces a Git remote URL or pattern to "host/path".
func normalizeRemote(u string) string {
	u = strings.TrimSpace(u)
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
		if at := strings.LastIndex(strings.SplitN(u, "/", 2)[0], "@"); at >= 0 {
			u = u[at+1:]
		}
		host, rest, _ := strings.Cut(u, "/")
		if h, _, ok := strings.Cut(host, ":"); ok {
			host = h // drop a port
		}
		u = host + "/" + rest
	} else {
		// scp-like: [user@]host:path
		if at := strings.Index(u, "@"); at >= 0 && at < strings.IndexAny(u+":", ":/") {
			u = u[at+1:]
		}
		if host, rest, ok := strings.Cut(u, ":"); ok && !strings.Contains(host, "/") {
			u = host + "/" + strings.TrimPrefix(rest, "/")
		}
	}
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	return strings.ToLower(u)
}
