//go:build windows

package reporter

import (
	"path/filepath"
	"testing"
)

func TestWindowsTryLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")
	unlock, ok, err := tryLock(path)
	if err != nil || !ok {
		t.Fatalf("first lock = %v, %v", ok, err)
	}
	if _, ok, err := tryLock(path); ok || err != nil {
		t.Fatalf("second lock = %v, %v; want it refused without an error", ok, err)
	}
	unlock()
	unlock2, ok, err := tryLock(path)
	if err != nil || !ok {
		t.Fatalf("lock not released: %v, %v", ok, err)
	}
	unlock2()
}
