package iconfont

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGhosttyConfigPath(t *testing.T) {
	has := func(paths ...string) func(string) bool {
		return func(p string) bool {
			for _, candidate := range paths {
				if p == candidate {
					return true
				}
			}
			return false
		}
	}
	xdg := filepath.Join("/h", ".config", "ghostty", "config")
	support := filepath.Join("/h", "Library", "Application Support", "com.mitchellh.ghostty", "config")
	for _, test := range []struct {
		name, goos, xdgConfig, want string
		exists                      func(string) bool
	}{
		{"fresh", "darwin", "", xdg, has()},
		{"both exist", "darwin", "", xdg, has(xdg, support)},
		{"only support exists", "darwin", "", support, has(support)},
		{"linux ignores support", "linux", "", xdg, has(support)},
		{"XDG_CONFIG_HOME", "linux", "/c", filepath.Join("/c", "ghostty", "config"), has()},
	} {
		if got := GhosttyConfigPath(test.goos, "/h", test.xdgConfig, test.exists); got != test.want {
			t.Errorf("%s: %q, want %q", test.name, got, test.want)
		}
	}
}

func TestConfigureGhosttyKeepsExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghostty", "config")

	// Fresh: the folder and file are created.
	if changed, err := ConfigureGhostty(path); err != nil || !changed {
		t.Fatalf("fresh = %v, %v", changed, err)
	}
	if changed, err := ConfigureGhostty(path); err != nil || changed {
		t.Fatalf("second = %v, %v", changed, err)
	}

	// Existing content without a trailing newline is kept intact.
	if err := os.WriteFile(path, []byte("theme = dark\nfont-family = Mono"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := ConfigureGhostty(path); err != nil || !changed {
		t.Fatalf("existing = %v, %v", changed, err)
	}
	got, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(got), "theme = dark\nfont-family = Mono\n") || !strings.Contains(string(got), ghosttyLine+"\n") {
		t.Fatalf("config = %q", got)
	}
	// Windows has no Unix permission bits (Go reports 0666), so only the
	// content checks apply there.
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed to %v", info.Mode().Perm())
	}

	// Remove restores the user's lines and nothing else.
	if changed, err := UnconfigureGhostty(path); err != nil || !changed {
		t.Fatalf("remove = %v, %v", changed, err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "theme = dark\nfont-family = Mono\n" {
		t.Fatalf("after remove = %q", got)
	}
	if changed, err := UnconfigureGhostty(path); err != nil || changed {
		t.Fatalf("second remove = %v, %v", changed, err)
	}
	if changed, err := UnconfigureGhostty(filepath.Join(t.TempDir(), "missing")); err != nil || changed {
		t.Fatalf("missing file = %v, %v", changed, err)
	}
}

func TestConfigureGhosttyFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles-config")
	link := filepath.Join(dir, "config")
	if err := os.WriteFile(target, []byte("a = b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := ConfigureGhostty(link); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Lstat(link); info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	if got, _ := os.ReadFile(target); !strings.Contains(string(got), "FirekeeperIcons") {
		t.Fatalf("target = %q", got)
	}
}

func TestGhosttyLineCoversTheGlyphRange(t *testing.T) {
	want := fmt.Sprintf("U+%X-U+%X=FirekeeperIcons", FirstCodepoint, LastCodepoint)
	if !strings.HasSuffix(ghosttyLine, want) {
		t.Fatalf("%q does not end with %q", ghosttyLine, want)
	}
}
