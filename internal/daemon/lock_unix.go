//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package daemon

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
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

// lockOwnerAlive reports whether the pid written into the lock at path
// names a live process. It never takes the lock, so asking cannot make a
// daemon that is starting up fail with ErrLocked. A pid reused by an
// unrelated process reads as alive; callers treat the answer as a hint.
func lockOwnerAlive(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	err = syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
