//go:build windows

package reporter

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// tryLock takes an exclusive LockFileEx lock on path without blocking. It
// reports false with no error when another process holds the lock. Windows
// releases the lock when the process exits, however it exits, so a killed
// pass never leaves a stale lock. The file is left in place.
func tryLock(path string) (unlock func(), ok bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open state lock: %w", cause(err))
	}
	overlapped := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lock reporter state: %w", err)
	}
	return func() { f.Close() }, true, nil
}
