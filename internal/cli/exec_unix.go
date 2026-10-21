//go:build unix

package cli

import (
	"errors"
	"syscall"
)

var errExecUnsupported = errors.New("exec is not supported on this platform")

// execReplace replaces the current process with path. It returns only on
// failure.
func execReplace(path string, argv, env []string) error {
	return syscall.Exec(path, argv, env)
}
