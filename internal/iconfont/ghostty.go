package iconfont

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The logo glyphs sit at U+E9A0 to U+E9A6; scripts/icon-font/build.mjs
// assigns them.
const (
	FirstCodepoint = 0xE9A0
	LastCodepoint  = 0xE9A6
)

const (
	ghosttyMarker = "# Firekeeper logo font (firekeeper install-font --remove undoes this)"
	ghosttyLine   = "font-codepoint-map = U+E9A0-U+E9A6=FirekeeperIcons"
)

// GhosttyConfigPath returns the Ghostty config file to edit. Ghostty reads
// both $XDG_CONFIG_HOME/ghostty/config and, on macOS, the Application Support
// one; an existing file wins, otherwise the XDG path is used.
func GhosttyConfigPath(goos, home, xdgConfig string, exists func(string) bool) string {
	if xdgConfig == "" {
		xdgConfig = filepath.Join(home, ".config")
	}
	xdg := filepath.Join(xdgConfig, "ghostty", "config")
	if goos == "darwin" {
		support := filepath.Join(home, "Library", "Application Support", "com.mitchellh.ghostty", "config")
		if !exists(xdg) && exists(support) {
			return support
		}
	}
	return xdg
}

// ConfigureGhostty makes Ghostty draw the logo codepoints from FirekeeperIcons.
// It maps only those codepoints, so the user's own font stays the main one,
// and appends to the file so a symlinked or hand-written config is kept. It
// reports whether the file changed.
func ConfigureGhostty(path string) (bool, error) {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read Ghostty config: %w", err)
	}
	if bytes.Contains(existing, []byte("FirekeeperIcons")) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create Ghostty config folder: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return false, fmt.Errorf("write Ghostty config: %w", err)
	}
	block := ghosttyMarker + "\n" + ghosttyLine + "\n"
	if len(existing) > 0 {
		if !bytes.HasSuffix(existing, []byte("\n")) {
			block = "\n" + block
		}
		block = "\n" + block
	}
	if _, err := file.WriteString(block); err != nil {
		file.Close()
		return false, fmt.Errorf("write Ghostty config: %w", err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("write Ghostty config: %w", err)
	}
	return true, nil
}

// UnconfigureGhostty removes what ConfigureGhostty added, reporting whether
// the file changed. A missing file is not an error.
func UnconfigureGhostty(path string) (bool, error) {
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read Ghostty config: %w", err)
	}
	lines := strings.Split(string(existing), "\n")
	kept := lines[:0:0]
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == ghosttyMarker || trimmed == ghosttyLine {
			continue
		}
		kept = append(kept, line)
	}
	if len(kept) == len(lines) {
		return false, nil
	}
	// Writing in place keeps the file's mode and any symlink to it.
	if err := os.WriteFile(path, []byte(strings.TrimRight(strings.Join(kept, "\n"), "\n")+"\n"), 0o644); err != nil {
		return false, fmt.Errorf("write Ghostty config: %w", err)
	}
	return true, nil
}
