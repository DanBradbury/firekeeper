//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package reporter

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// tryLock takes an exclusive flock on path without blocking. It reports
// false with no error when another process holds the lock. The kernel
// releases the lock when the process exits, however it exits, so a killed
// pass never leaves a stale lock. The file is left in place.
func tryLock(path string) (unlock func(), ok bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open state lock: %w", cause(err))
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lock reporter state: %w", err)
	}
	return func() { f.Close() }, true, nil
}
