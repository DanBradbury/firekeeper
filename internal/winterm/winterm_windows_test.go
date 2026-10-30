//go:build windows

package winterm

import (
	"errors"
	"os"
	"testing"
)

func TestSnapshotListsThisProcess(t *testing.T) {
	snapshot, err := snapshotProcesses()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range snapshot {
		if p.PID == os.Getpid() {
			if p.Exe == "" || p.PPID <= 0 {
				t.Fatalf("this process = %+v", p)
			}
			return
		}
	}
	t.Fatal("this process is not in the snapshot")
}

// A CI runner has no interactive desktop, so there is no window to raise.
// Focus must say so instead of crashing or raising something unrelated.
func TestFocusWithNoWindowReportsIt(t *testing.T) {
	if _, err := Focus([]int{0x7ffffff0}); !errors.Is(err, ErrNoWindow) {
		t.Fatalf("Focus of a missing process = %v, want ErrNoWindow", err)
	}
}
