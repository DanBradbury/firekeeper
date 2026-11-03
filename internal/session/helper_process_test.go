package session

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// helperEnv makes the test binary act as a stand-in provider process: it
// waits until its stdin closes, then exits. No real provider is ever run.
const helperEnv = "FIREKEEPER_SESSION_TEST_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "wait" {
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Minute):
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// startHelper copies the test binary to dir/name and runs it with args as a
// waiting stand-in process. The process is stopped when the test ends.
func startHelper(t *testing.T, dir, name string, args ...string) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		name += ".exe"
	}
	program := filepath.Join(dir, name)
	if _, err := os.Stat(program); err != nil {
		data, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(program, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(program, args...)
	cmd.Env = append(os.Environ(), helperEnv+"=wait")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}
