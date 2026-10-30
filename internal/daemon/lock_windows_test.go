//go:build windows

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestWindowsLockIsExclusiveAndReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	first, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock err = %v, want ErrLocked", err)
	}
	// Windows locks are mandatory; the pid must still be readable while held.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("lock file unreadable while held: %v", err)
	}
	if strings.TrimSpace(string(data)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock file = %q, want this pid", data)
	}
	if !lockOwnerAlive(path) {
		t.Fatal("lockOwnerAlive = false for this process")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := acquireLock(path)
	if err != nil {
		t.Fatalf("lock not released on close: %v", err)
	}
	again.Close()
}

func TestWindowsLockOwnerDead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	for name, content := range map[string]string{"missing pid": "", "garbage": "not a pid", "no such process": "2147483000\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if lockOwnerAlive(path) {
			t.Errorf("%s: lockOwnerAlive = true", name)
		}
	}
}
