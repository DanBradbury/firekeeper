//go:build linux

package session

import (
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// /proc/<pid>/cmdline returns a stand-in process's argument list, including
// a space in the program path.
func TestReadProcessArgsRealProcessLinux(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my dir")
	cmd := startHelper(t, dir, "kimi", "--foo", "a b", "web")
	want := []string{filepath.Join(dir, "kimi"), "--foo", "a b", "web"}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := readProcessArgs(cmd.Process.Pid)
		if err == nil && slices.Equal(got, want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("readProcessArgs = %q, %v; want %q", got, err, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := readProcessArgs(-1); err == nil {
		t.Fatal("readProcessArgs(-1) succeeded")
	}
}
