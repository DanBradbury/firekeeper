//go:build !(darwin || linux || freebsd || openbsd || netbsd || dragonfly)

package daemon

import (
	"errors"
	"os"
)

func acquireLock(string) (*os.File, error) {
	return nil, errors.New("the daemon lock is not supported on this platform")
}
