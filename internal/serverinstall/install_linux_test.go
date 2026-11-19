package serverinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// writeBusyServer writes the fake server and returns it still open for
// writing, which makes Linux refuse to exec it with ETXTBSY: what a fork in
// another goroutine does when it inherits the installer's descriptor.
func writeBusyServer(t *testing.T) (string, *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".firekeeper-server-busy")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(fakeScript); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return path, f
}

// Regression test: a parallel install failed with `text file busy` in CI.
func TestVersionCheckWaitsOutTextFileBusy(t *testing.T) {
	path, f := writeBusyServer(t)
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(100 * time.Millisecond)
		f.Close()
	}()
	if err := runVersionCheck(context.Background(), path, 5*time.Second); err != nil {
		t.Fatalf("version check did not wait for the file to be released: %v", err)
	}
	<-released
}

func TestVersionCheckGivesUpWhenTextFileStaysBusy(t *testing.T) {
	path, _ := writeBusyServer(t)
	start := time.Now()
	err := runVersionCheck(context.Background(), path, 150*time.Millisecond)
	if !errors.Is(err, syscall.ETXTBSY) {
		t.Fatalf("err = %v, want ETXTBSY", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("gave up after %v, want about the busy wait", elapsed)
	}
	// A canceled context stops the wait early too.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runVersionCheck(ctx, path, time.Minute); err == nil {
		t.Fatal("canceled check succeeded")
	}
}
