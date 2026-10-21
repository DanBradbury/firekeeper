//go:build unix

package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func lookupFor(env map[string]string, exe string, pathDirs ...string) serverLookup {
	return serverLookup{
		getenv:     func(k string) string { return env[k] },
		executable: func() (string, error) { return exe, nil },
		lookPath: func(name string) (string, error) {
			for _, d := range pathDirs {
				if p := filepath.Join(d, name); serverUsable(p) {
					return p, nil
				}
			}
			return "", exec.ErrNotFound
		},
	}
}

func TestResolveServerOrder(t *testing.T) {
	root := t.TempDir()
	envBin := fakeBinary(t, filepath.Join(root, "custom"), "my-server")
	sibling := fakeBinary(t, filepath.Join(root, "install", "bin"), serverBinName)
	onPath := fakeBinary(t, filepath.Join(root, "pathdir"), serverBinName)
	exe := filepath.Join(root, "install", "bin", "firekeeper")
	pathDirs := []string{filepath.Join(root, "pathdir")}
	setEnv := map[string]string{ServerBinEnv: envBin}

	tests := []struct {
		name string
		l    serverLookup
		want string
	}{
		{"env beats sibling and PATH", lookupFor(setEnv, exe, pathDirs...), envBin},
		{"sibling beats PATH", lookupFor(nil, exe, pathDirs...), sibling},
		{"PATH when no sibling", lookupFor(nil, filepath.Join(root, "elsewhere", "firekeeper"), pathDirs...), onPath},
		{"env value may be a bare name found on PATH", lookupFor(map[string]string{ServerBinEnv: serverBinName}, exe, pathDirs...), onPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveServer(tt.l)
			if err != nil || got != tt.want {
				t.Fatalf("resolveServer = %q, %v; want %q", got, err, tt.want)
			}
		})
	}

	t.Run("nothing found", func(t *testing.T) {
		_, err := resolveServer(lookupFor(nil, filepath.Join(root, "elsewhere", "firekeeper")))
		if !errors.Is(err, errServerNotFound) {
			t.Fatalf("err = %v, want errServerNotFound", err)
		}
	})

	t.Run("a bad FIREKEEPER_SERVER_BIN is an error, not a fall-through", func(t *testing.T) {
		notExec := filepath.Join(root, "custom", "not-executable")
		if err := os.WriteFile(notExec, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{notExec, filepath.Join(root, "custom", "missing"), filepath.Join(root, "custom")} {
			_, err := resolveServer(lookupFor(map[string]string{ServerBinEnv: bad}, exe, pathDirs...))
			if err == nil || errors.Is(err, errServerNotFound) || !strings.Contains(err.Error(), ServerBinEnv) {
				t.Errorf("env %q: err = %v", bad, err)
			}
		}
	})

	t.Run("a sibling that is not executable is skipped", func(t *testing.T) {
		dir := filepath.Join(root, "noexec", "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, serverBinName), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := resolveServer(lookupFor(nil, filepath.Join(dir, "firekeeper"), pathDirs...))
		if err != nil || got != onPath {
			t.Fatalf("resolveServer = %q, %v; want the PATH copy", got, err)
		}
	})
}

// A Homebrew-style layout: bin/firekeeper is a link into a Cellar directory
// that also holds firekeeper-server, which is not linked into bin.
func TestResolveServerThroughSymlink(t *testing.T) {
	root := t.TempDir()
	real := fakeBinary(t, filepath.Join(root, "Cellar", "firekeeper", "1.0", "bin"), "firekeeper")
	sibling := fakeBinary(t, filepath.Join(root, "Cellar", "firekeeper", "1.0", "bin"), serverBinName)
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bin, "firekeeper")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := resolveServer(lookupFor(nil, link))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(sibling)
	if g, _ := filepath.EvalSymlinks(got); g != want {
		t.Fatalf("resolveServer = %q, want the Cellar sibling %q", got, sibling)
	}

	// A server linked into the same bin directory as the link is found too.
	root2 := t.TempDir()
	real2 := fakeBinary(t, filepath.Join(root2, "opt"), "firekeeper")
	bin2 := filepath.Join(root2, "bin")
	if err := os.MkdirAll(bin2, 0o755); err != nil {
		t.Fatal(err)
	}
	link2 := filepath.Join(bin2, "firekeeper")
	if err := os.Symlink(real2, link2); err != nil {
		t.Fatal(err)
	}
	beside := fakeBinary(t, bin2, serverBinName)
	if got, err := resolveServer(lookupFor(nil, link2)); err != nil || got != beside {
		t.Fatalf("resolveServer = %q, %v; want %q", got, err, beside)
	}
}

// helperCmd runs this test binary as `firekeeper args...` in a controlled
// environment: an empty PATH directory unless extra says otherwise.
func helperCmd(t *testing.T, exe string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(exe, args...)
	cmd.Env = append([]string{helperEnv + "=1", "HOME=" + t.TempDir(), "PATH=" + t.TempDir()}, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		code = ee.ExitCode()
	default:
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

// copyTestBinary makes dir/name an executable copy of the running test binary.
func copyTestBinary(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(dir, name)
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestServeExecsTheServer runs the real handoff in a child process: the
// process is replaced, the arguments arrive after "serve", the environment is
// kept, and the server's exit status is the command's.
func TestServeExecsTheServer(t *testing.T) {
	dir := t.TempDir()
	client := copyTestBinary(t, dir, "firekeeper")
	server := copyTestBinary(t, dir, serverBinName)
	args := []string{"serve", "--listen", "127.0.0.1:0", "--banner", "two words", "admin"}

	out, errOut, code := helperCmd(t, client, []string{passthroughEnv + "=kept", exitEnv + "=3"}, args...)
	if code != 3 {
		t.Fatalf("exit %d, want the server's 3; stderr %q", code, errOut)
	}
	want := "argv0=firekeeper-server\narg=serve\narg=--listen\narg=127.0.0.1:0\narg=--banner\narg=two words\narg=admin\npassthrough=kept\n"
	if out != want {
		t.Fatalf("server saw:\n%s\nwant:\n%s", out, want)
	}

	// The same client reaches the server through each place it looks.
	if _, _, code := helperCmd(t, client, nil, "serve"); code != 0 {
		t.Errorf("sibling lookup: exit %d", code)
	}
	if err := os.Remove(server); err != nil {
		t.Fatal(err)
	}
	if _, _, code := helperCmd(t, client, nil, "serve"); code != 2 {
		t.Errorf("without the server anywhere: exit %d, want 2", code)
	}
	elsewhere := copyTestBinary(t, t.TempDir(), serverBinName)
	if _, _, code := helperCmd(t, client, []string{ServerBinEnv + "=" + elsewhere, exitEnv + "=7"}, "serve"); code != 7 {
		t.Errorf("env lookup: exit %d, want 7", code)
	}
	pathDir := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(pathDir, serverBinName)); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(client, "serve")
	cmd.Env = []string{helperEnv + "=1", "PATH=" + pathDir, exitEnv + "=7"}
	if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != 7 {
		t.Errorf("PATH lookup: %v", err)
	}
}

func TestServeNotFoundExit(t *testing.T) {
	client := copyTestBinary(t, t.TempDir(), "firekeeper")
	_, errOut, code := helperCmd(t, client, nil, "serve", "--listen", "x")
	if code != 2 || !strings.Contains(errOut, "separate commercial binary") || !strings.Contains(errOut, ServerBinEnv) {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	_, errOut, code = helperCmd(t, client, []string{ServerBinEnv + "=/definitely/not/here"}, "serve")
	if code != 2 || !strings.Contains(errOut, "is not an executable file") {
		t.Fatalf("bad env: exit %d, stderr %q", code, errOut)
	}
}

// TestRunChild covers the fallback for platforms without exec: the exit
// status comes back, and a signal sent to this process reaches the child.
func TestRunChild(t *testing.T) {
	server := copyTestBinary(t, t.TempDir(), serverBinName)
	env := []string{helperEnv + "=1", exitEnv + "=7"}
	var stderr bytes.Buffer
	if code := runChild(server, []string{serverBinName, "serve"}, env, &stderr); code != 7 {
		t.Fatalf("runChild = %d, want 7 (%s)", code, stderr.String())
	}
	if code := runChild(filepath.Join(t.TempDir(), "missing"), []string{"x"}, nil, &stderr); code != exitCannotExec {
		t.Fatalf("runChild of a missing file = %d, want %d", code, exitCannotExec)
	}
}

func TestRunChildForwardsSignals(t *testing.T) {
	server := copyTestBinary(t, t.TempDir(), serverBinName)
	ready := filepath.Join(t.TempDir(), "ready")
	env := []string{helperEnv + "=1", readyEnv + "=" + ready}

	// Catch the signal here too, so a late delivery cannot end the test run.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGTERM)
	defer signal.Stop(guard)

	result := make(chan int, 1)
	go func() { result <- runChild(server, []string{serverBinName}, env, io.Discard) }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-result:
		if code != 143 && code != 5 {
			t.Fatalf("runChild = %d, want the child's reply to SIGTERM (5, or 143 if it died of it)", code)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("child did not stop after SIGTERM")
	}
}
