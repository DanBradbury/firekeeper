//go:build windows

package daemon

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

// lockOffset is where the one-byte lock sits: far past the pid, because
// Windows locks are mandatory and a reader could not read a locked byte. A
// lock may cover bytes beyond the end of the file.
const lockOffset = 0x7fffffff

// acquireLock takes an exclusive, non-blocking LockFileEx lock on one byte
// of path. Windows releases it when the process exits, however it
// exits, so a killed daemon never leaves a stale lock. The file is left in
// place; removing it would let two daemons lock different files.
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock: %w", cause(err))
	}
	overlapped := &windows.Overlapped{Offset: lockOffset}
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
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
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Access denied still means the process exists.
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(handle)
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	const stillActive = 259
	return code == stillActive
}
