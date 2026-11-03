package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/DanBradbury/firekeeper/internal/serverbin"
)

// The dashboard server is not part of this program. It ships as the separate,
// commercial firekeeper-server binary, and `firekeeper serve` hands the
// process over to it.
const (
	serverBinName = serverbin.Name
	// ServerBinEnv names the environment variable that points at the server
	// binary, ahead of every other place firekeeper looks.
	ServerBinEnv = serverbin.Env
	// exitServerMissing is the exit status when there is no server to run.
	exitServerMissing = 2
	// exitCannotExec is the exit status when the server was found but could
	// not be started, as shells use.
	exitCannotExec = 126
)

// serverLookup is everything resolving the server binary reads from the
// process. Tests replace it.
type serverLookup = serverbin.Lookup

var defaultLookup = serverbin.Default()

// startServer runs the server binary with argv and env. On Unix it replaces
// this process; elsewhere it runs a child and returns its exit status. Tests
// replace it.
var startServer = runServerProcess

var (
	serverUsable      = serverbin.Usable
	errServerNotFound = serverbin.ErrNotFound
	resolveServer     = serverbin.Resolve
)

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
	} else if err == nil && startsDashboard(args) {
		// Found one: offer a newer release, if there is one (same switches as
		// the install offer, plus FIREKEEPER_NO_UPDATE_CHECK).
		path = offerServerUpdate(path, stderr)
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
