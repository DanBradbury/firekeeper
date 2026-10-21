package version

import (
	"runtime"
	"testing"
)

func TestString(t *testing.T) {
	saved := []string{Version, Commit, Date}
	t.Cleanup(func() { Version, Commit, Date = saved[0], saved[1], saved[2] })

	Version, Commit, Date = "1.2.3", "abc1234", "2026-10-13T00:00:00Z"
	want := "firekeeper 1.2.3 (commit abc1234, built 2026-10-13T00:00:00Z, " + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if got := String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestDefaultsAreDev(t *testing.T) {
	// Tests are built without -ldflags, so the defaults must show through.
	if Version != "dev" || Commit != "dev" || Date != "dev" {
		t.Fatalf("defaults = %q, %q, %q, want dev", Version, Commit, Date)
	}
}
