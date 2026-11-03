package session

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// helperEnv makes the test binary act as a stand-in provider process. No
// real provider is ever run. Modes:
//   - wait: wait until stdin closes, then exit;
//   - hold: also keep the file named by helperHoldEnv open (lsof evidence);
//   - spawn: also start helperChildEnv's program with helperChildArgsEnv's
//     arguments (separated by \x1f) as a waiting child, stopped on exit.
const (
	helperEnv          = "FIREKEEPER_SESSION_TEST_HELPER"
	helperHoldEnv      = "FIREKEEPER_SESSION_TEST_HOLD"
	helperChildEnv     = "FIREKEEPER_SESSION_TEST_CHILD"
	helperChildArgsEnv = "FIREKEEPER_SESSION_TEST_CHILD_ARGS"
)

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "wait":
		waitForStdin()
		os.Exit(0)
	case "hold":
		f, err := os.Open(os.Getenv(helperHoldEnv))
		if err != nil {
			os.Exit(3)
		}
		waitForStdin()
		_ = f.Close()
		os.Exit(0)
	case "spawn":
		var args []string
		if raw := os.Getenv(helperChildArgsEnv); raw != "" {
			args = strings.Split(raw, "\x1f")
		}
		child := exec.Command(os.Getenv(helperChildEnv), args...)
		child.Env = append(os.Environ(), helperEnv+"=wait")
		stdin, err := child.StdinPipe()
		if err != nil || child.Start() != nil {
			os.Exit(3)
		}
		waitForStdin()
		_ = stdin.Close()
		_ = child.Process.Kill()
		_ = child.Wait()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func waitForStdin() {
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
	}
}

// helperOptions adjusts a stand-in process.
type helperOptions struct {
	dir string   // working directory; "" keeps the test's
	env []string // extra environment, such as the hold or spawn settings
}

// startHelper copies the test binary to dir/name and runs it with args as a
// waiting stand-in process. The process is stopped when the test ends.
func startHelper(t *testing.T, dir, name string, args ...string) *exec.Cmd {
	t.Helper()
	return startHelperWith(t, dir, name, helperOptions{env: []string{helperEnv + "=wait"}}, args...)
}

// helperProgram copies the test binary to dir/name once and returns its path.
func helperProgram(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		name += ".exe"
	}
	program := filepath.Join(dir, name)
	if _, err := os.Stat(program); err == nil {
		return program
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
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
	return program
}

func startHelperWith(t *testing.T, dir, name string, opts helperOptions, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(helperProgram(t, dir, name), args...)
	cmd.Dir = opts.dir
	cmd.Env = append(os.Environ(), opts.env...)
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

// fixtureDiscoverer parses synthetic ps output without ever reading a live
// process's argument list: the PIDs in fixtures may belong to real
// processes on the machine running the tests.
func fixtureDiscoverer() *discoverer {
	return &discoverer{ctx: context.Background(), opts: Options{Args: func(int) ([]string, error) {
		return nil, errTestNoArgs
	}}}
}

// setKimiNow fixes the clock Kimi linking compares process start times
// with, for the length of the test.
func setKimiNow(t *testing.T, now time.Time) {
	t.Helper()
	old := kimiNow
	kimiNow = func() time.Time { return now }
	t.Cleanup(func() { kimiNow = old })
}
