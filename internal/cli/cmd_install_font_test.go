package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/iconfont"
)

func useFontDir(t *testing.T, dir string, err error) {
	t.Helper()
	previous := fontDir
	fontDir = func() (string, error) { return dir, err }
	t.Cleanup(func() { fontDir = previous })
}

func useGhosttyConfig(t *testing.T, path string) {
	t.Helper()
	previous := ghosttyConfig
	ghosttyConfig = func() string { return path }
	t.Cleanup(func() { ghosttyConfig = previous })
}

func TestInstallFontInstallsAndRemoves(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Fonts")
	useFontDir(t, dir, nil)
	useGhosttyConfig(t, "")

	var stdout, stderr bytes.Buffer
	if code := runInstallFont(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !iconfont.Installed(dir) {
		t.Fatal("font not installed")
	}
	if out := stdout.String(); !strings.Contains(out, "installed ") || !strings.Contains(out, "Ghostty") {
		t.Fatalf("output = %q", out)
	}

	stdout.Reset()
	if code := runInstallFont(nil, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "already installed") {
		t.Fatalf("second run: exit %d, %q", code, stdout.String())
	}

	stdout.Reset()
	if code := runInstallFont([]string{"--remove"}, &stdout, &stderr); code != 0 || iconfont.Installed(dir) {
		t.Fatalf("remove: exit %d, installed=%v", code, iconfont.Installed(dir))
	}
	stdout.Reset()
	if code := runInstallFont([]string{"--remove"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "was not installed") {
		t.Fatalf("second remove: exit %d, %q", code, stdout.String())
	}
}

func TestInstallFontReportsFailures(t *testing.T) {
	var stdout, stderr bytes.Buffer
	useFontDir(t, "", iconfont.ErrUnsupported)
	if code := runInstallFont(nil, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "not supported") {
		t.Fatalf("unsupported: exit %d, %q", code, stderr.String())
	}
	useFontDir(t, "", errors.New("boom"))
	if code := runInstallFont(nil, &stdout, &stderr); code != 1 {
		t.Fatalf("dir error: exit %d", code)
	}
	if code := runInstallFont([]string{"extra"}, &stdout, &stderr); code != 2 {
		t.Fatalf("extra argument: exit %d", code)
	}
}

func TestInstallFontConfiguresGhostty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Fonts")
	config := filepath.Join(t.TempDir(), "ghostty", "config")
	useFontDir(t, dir, nil)
	useGhosttyConfig(t, config)

	var stdout, stderr bytes.Buffer
	if code := runInstallFont(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if got, _ := os.ReadFile(config); !strings.Contains(string(got), "font-codepoint-map = U+E9A0-U+E9A6=FirekeeperIcons") {
		t.Fatalf("config = %q", got)
	}
	if !strings.Contains(stdout.String(), "Reload Ghostty") {
		t.Fatalf("output = %q", stdout.String())
	}

	if code := runInstallFont([]string{"--remove"}, &stdout, &stderr); code != 0 {
		t.Fatalf("remove exit %d: %s", code, stderr.String())
	}
	if got, _ := os.ReadFile(config); strings.Contains(string(got), "FirekeeperIcons") {
		t.Fatalf("config after remove = %q", got)
	}
}

func TestInstallFontNoTerminalLeavesGhosttyAlone(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config")
	useFontDir(t, t.TempDir(), nil)
	useGhosttyConfig(t, config)

	var stdout, stderr bytes.Buffer
	if code := runInstallFont([]string{"--no-terminal"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatalf("config touched: %v", err)
	}
}
