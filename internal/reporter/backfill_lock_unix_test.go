//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package reporter

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Elsewhere tryLock is a no-op (lock_other.go), so the lock cannot time out;
// the build constraint matches lock_unix.go.

func TestStateLockTimesOut(t *testing.T) {
	f := newHistoryFixture(t, 1)
	unlock, ok, err := tryLock(filepath.Join(f.home, ".firekeeper", "state.lock"))
	if err != nil || !ok {
		t.Fatalf("state lock unavailable: ok %v, err %v", ok, err)
	}
	defer unlock()

	cfg := f.runOnceConfig("http://127.0.0.1:1", f.metas[0])
	cfg.lockWait = 100 * time.Millisecond
	if _, err := RunOnce(context.Background(), cfg); !errors.Is(err, ErrStateLocked) {
		t.Fatalf("RunOnce err = %v, want ErrStateLocked", err)
	}
	bcfg := f.config("http://127.0.0.1:1")
	bcfg.lockWait = 100 * time.Millisecond
	if _, err := Backfill(context.Background(), bcfg); !errors.Is(err, ErrStateLocked) {
		t.Fatalf("Backfill err = %v, want ErrStateLocked", err)
	}
	// A dry run saves nothing, so it does not wait for the lock.
	bcfg.DryRun = true
	if _, err := Backfill(context.Background(), bcfg); err != nil {
		t.Fatalf("dry run err = %v", err)
	}
}
