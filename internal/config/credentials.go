package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// ResolvePath returns the config file Load reads: the file FIREKEEPER_CONFIG
// names, else home/.firekeeper/config.toml. Empty home means os.UserHomeDir
// and nil getenv means os.Getenv.
func ResolvePath(home string, getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("config: find home directory")
		}
		home = h
	}
	if p := strings.TrimSpace(getenv(EnvConfig)); p != "" {
		return expand(p, home), nil
	}
	return DefaultPath(home), nil
}

// SaveCredentials sets the top-level server and token keys in the config
// file at path, creating the file (mode 0600) and its directory (0700) if
// needed. Everything else in the file, comments included, is kept as is.
// The file is replaced atomically and always ends up mode 0600, because it
// now holds a secret. A file that is not valid TOML is left alone.
func SaveCredentials(path, server, token string) error {
	for _, v := range []string{server, token} {
		if err := checkPlain(v); err != nil {
			return err
		}
	}
	return edit(path, true, func(lines []string) []string {
		lines = setKey(lines, "server", server)
		return setKey(lines, "token", token)
	}, func(m map[string]any) bool { return m["server"] == server && m["token"] == token })
}

// ClearToken removes the token key from the config file at path. It
// reports whether there was one. A missing file is not an error.
func ClearToken(path string) (bool, error) {
	removed := false
	err := edit(path, false, func(lines []string) []string {
		out := dropKey(lines, "token")
		removed = len(out) != len(lines)
		return out
	}, func(m map[string]any) bool { _, has := m["token"]; return !has })
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return removed, err
}

// checkPlain rejects values that would need escaping, which a URL or a
// generated token never does.
func checkPlain(v string) error {
	if v == "" {
		return errors.New("config: empty value")
	}
	for _, r := range v {
		if r < 0x21 || r > 0x7e || r == '"' || r == '\\' {
			return errors.New("config: value contains characters that cannot be stored safely")
		}
	}
	return nil
}

// edit rewrites the file's lines with fn and writes the result only if it
// parses and check accepts the top-level keys, so a layout the line editor
// misreads fails instead of corrupting the file.
func edit(path string, create bool, fn func([]string) []string, check func(map[string]any) bool) error {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var probe map[string]any
		if _, derr := toml.Decode(string(data), &probe); derr != nil {
			return fmt.Errorf("config: %s is not valid TOML; fix it first", path)
		}
	case errors.Is(err, fs.ErrNotExist):
		if !create {
			return err
		}
		data = nil
	default:
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	var lines []string
	if len(data) > 0 {
		lines = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
	out := strings.Join(fn(lines), "\n") + "\n"
	var parsed map[string]any
	if _, err := toml.Decode(out, &parsed); err != nil || !check(parsed) {
		return fmt.Errorf("config: refusing to edit %s: cannot update it safely; edit the file by hand", path)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml") // 0600
	if err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(out); err != nil {
		tmp.Close()
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

// topLevelEnd is the index of the first table header, or len(lines). Keys
// after it belong to a table, not the top level.
func topLevelEnd(lines []string) int {
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "[") {
			return i
		}
	}
	return len(lines)
}

func isKey(line, key string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), key)
	return ok && strings.HasPrefix(strings.TrimSpace(rest), "=")
}

func setKey(lines []string, key, value string) []string {
	line := fmt.Sprintf("%s = %q", key, value)
	end := topLevelEnd(lines)
	for i := 0; i < end; i++ {
		if isKey(lines[i], key) {
			lines[i] = line
			return lines
		}
	}
	// New keys go first, ahead of anything that could be a table header.
	return append([]string{line}, lines...)
}

func dropKey(lines []string, key string) []string {
	end := topLevelEnd(lines)
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if i < end && isKey(l, key) {
			continue
		}
		out = append(out, l)
	}
	return out
}
