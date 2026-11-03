// Package serverbin finds the separate, commercial firekeeper-server binary.
// `firekeeper serve` hands over to it, and the dashboard asks it to render a
// session as HTML. Finding it never downloads or installs anything.
package serverbin

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// Name is the server binary's name, without the Windows .exe suffix.
	Name = "firekeeper-server"
	// Env names the environment variable that points at the server binary,
	// ahead of every other place firekeeper looks.
	Env = "FIREKEEPER_SERVER_BIN"
)

// Lookup is everything resolving the server binary reads from the process.
// Tests replace it.
type Lookup struct {
	Getenv     func(string) string
	Executable func() (string, error)
	LookPath   func(string) (string, error)
}

// Default reads the real environment, executable path, and PATH.
func Default() Lookup {
	return Lookup{Getenv: os.Getenv, Executable: os.Executable, LookPath: exec.LookPath}
}

// Usable reports whether path names an existing, executable, regular file
// (after following symlinks).
func Usable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0
}

// ErrNotFound is returned when no place firekeeper looks has the server.
var ErrNotFound = errors.New("firekeeper-server not found")

// Resolve finds the server binary, in this order:
//
//  1. $FIREKEEPER_SERVER_BIN. When it is set it is authoritative: if it does
//     not name an executable file that is an error, not a reason to look
//     elsewhere. A value without a path separator is looked up on PATH.
//  2. A firekeeper-server file next to the running executable, with symlinks
//     resolved so a bin directory of links (Homebrew's) finds the sibling in
//     the real install directory. The directory of the path as invoked is
//     tried second.
//  3. PATH.
//  4. ~/.local/bin, where the install offer puts the server when the
//     firekeeper directory is not writable. It is looked at even when it is not
//     on PATH, so a server installed there is found by the next `firekeeper
//     serve`.
func Resolve(l Lookup) (string, error) {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	if v := strings.TrimSpace(l.Getenv(Env)); v != "" {
		if !strings.ContainsRune(v, filepath.Separator) && !strings.ContainsRune(v, '/') {
			if p, err := l.LookPath(v); err == nil && Usable(p) {
				return p, nil
			}
		} else if Usable(v) {
			return v, nil
		}
		return "", fmt.Errorf("%s=%s is not an executable file", Env, v)
	}
	if exe, err := l.Executable(); err == nil {
		var dirs []string
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			dirs = append(dirs, filepath.Dir(real))
		}
		if d := filepath.Dir(exe); len(dirs) == 0 || dirs[0] != d {
			dirs = append(dirs, d)
		}
		for _, dir := range dirs {
			if p := filepath.Join(dir, Name+suffix); Usable(p) {
				return p, nil
			}
		}
	}
	if p, err := l.LookPath(Name); err == nil && Usable(p) {
		return p, nil
	} else if err != nil && !errors.Is(err, exec.ErrNotFound) && !errors.Is(err, exec.ErrDot) {
		return "", err
	}
	if home := strings.TrimSpace(l.Getenv("HOME")); home != "" {
		if p := filepath.Join(home, ".local", "bin", Name+suffix); Usable(p) {
			return p, nil
		}
	}
	return "", ErrNotFound
}
