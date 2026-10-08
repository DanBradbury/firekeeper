//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package daemon

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// acquireLock takes an exclusive, non-blocking flock on path. The kernel
// releases it when the process exits, however it exits, so a killed daemon
// never leaves a stale lock. The file is left in place; removing it would
// let two daemons lock different files.
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock: %w", cause(err))
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock daemon: %w", err)
	}
	// The pid is informational, for someone looking for the running daemon.
	if f.Truncate(0) == nil {
		fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return f, nil
}
