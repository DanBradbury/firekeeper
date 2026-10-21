package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// The dashboard server is not part of this program. It ships as the separate,
// commercial firekeeper-server binary, and `firekeeper serve` hands the
// process over to it.
const (
	serverBinName = "firekeeper-server"
	// ServerBinEnv names the environment variable that points at the server
	// binary, ahead of every other place firekeeper looks.
	ServerBinEnv = "FIREKEEPER_SERVER_BIN"
	// exitServerMissing is the exit status when there is no server to run.
	exitServerMissing = 2
	// exitCannotExec is the exit status when the server was found but could
	// not be started, as shells use.
	exitCannotExec = 126
)

// serverLookup is everything resolving the server binary reads from the
// process. Tests replace it.
type serverLookup struct {
	getenv     func(string) string
	executable func() (string, error)
	lookPath   func(string) (string, error)
}

var defaultLookup = serverLookup{getenv: os.Getenv, executable: os.Executable, lookPath: exec.LookPath}

// startServer runs the server binary with argv and env. On Unix it replaces
// this process; elsewhere it runs a child and returns its exit status. Tests
// replace it.
var startServer = runServerProcess

// serverUsable reports whether path names an existing, executable, regular
// file (after following symlinks).
func serverUsable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0
}

// errServerNotFound is returned when no place firekeeper looks has the server.
var errServerNotFound = errors.New("firekeeper-server not found")

// resolveServer finds the server binary, in this order:
//
//  1. $FIREKEEPER_SERVER_BIN. When it is set it is authoritative: if it does
//     not name an executable file that is an error, not a reason to look
//     elsewhere. A value without a path separator is looked up on PATH.
//  2. A firekeeper-server file next to the running executable, with symlinks
//     resolved so a bin directory of links (Homebrew's) finds the sibling in
//     the real install directory. The directory of the path as invoked is
//     tried second.
//  3. PATH.
func resolveServer(l serverLookup) (string, error) {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	if v := strings.TrimSpace(l.getenv(ServerBinEnv)); v != "" {
		if !strings.ContainsRune(v, filepath.Separator) && !strings.ContainsRune(v, '/') {
			if p, err := l.lookPath(v); err == nil && serverUsable(p) {
				return p, nil
			}
		} else if serverUsable(v) {
			return v, nil
		}
		return "", fmt.Errorf("%s=%s is not an executable file", ServerBinEnv, v)
	}
	if exe, err := l.executable(); err == nil {
		var dirs []string
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			dirs = append(dirs, filepath.Dir(real))
		}
		if d := filepath.Dir(exe); len(dirs) == 0 || dirs[0] != d {
			dirs = append(dirs, d)
		}
		for _, dir := range dirs {
			if p := filepath.Join(dir, serverBinName+suffix); serverUsable(p) {
				return p, nil
			}
		}
	}
	if p, err := l.lookPath(serverBinName); err == nil && serverUsable(p) {
		return p, nil
	} else if err != nil && !errors.Is(err, exec.ErrNotFound) && !errors.Is(err, exec.ErrDot) {
		return "", err
	}
	return "", errServerNotFound
}

// runServe hands over to `firekeeper-server serve args...`: same arguments,
// same environment, and the server's exit status becomes ours. Nothing about
// serving lives in this binary, so `firekeeper serve --help` is the
// server's help, and `firekeeper serve admin ...` and the other operator
// commands are the server's too.
func runServe(args []string, _, stderr io.Writer) int {
	path, err := resolveServer(defaultLookup)
	if errors.Is(err, errServerNotFound) {
		// Off by default; asks first, and only on a terminal.
		if path = offerInstall(stderr); path != "" {
			err = nil
		}
	}
	if err != nil {
		if errors.Is(err, errServerNotFound) {
			printServerMissing(stderr)
		} else {
			fmt.Fprintf(stderr, "firekeeper serve: %v\n", err)
		}
		return exitServerMissing
	}
	argv := append([]string{serverBinName, "serve"}, args...)
	return startServer(path, argv, os.Environ(), stderr)
}

func printServerMissing(stderr io.Writer) {
	fmt.Fprint(stderr, `firekeeper serve: the dashboard server is a separate commercial binary, firekeeper-server, and it was not found.
It is not part of this download. Read about it in the README, section "The dashboard server":
  https://github.com/DanBradbury/firekeeper#the-dashboard-server
If you already have it, put it next to firekeeper or on your PATH, or point at it:
  FIREKEEPER_SERVER_BIN=/path/to/firekeeper-server firekeeper serve
`)
}

// runServerProcess is the real startServer.
func runServerProcess(path string, argv, env []string, stderr io.Writer) int {
	err := execReplace(path, argv, env)
	if errors.Is(err, errExecUnsupported) {
		return runChild(path, argv, env, stderr)
	}
	// execReplace only returns when it failed to start the program.
	fmt.Fprintf(stderr, "firekeeper serve: cannot run %s: %v\n", path, err)
	return exitCannotExec
}
