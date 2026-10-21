package cli

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The handoff tests that start a real process use the test binary as both
// programs. Run with helperEnv set, it acts as firekeeper (when invoked under
// any name) or as a fake firekeeper-server (when invoked under that name):
//
//	client: runs RunSubcommand(os.Args[1:]) and exits with its code;
//	server: prints its argv[0], arguments and a passthrough variable, then
//	        exits with $FK_EXIT or, in sleep mode, waits for SIGTERM.
//
// No test touches the network, the real home directory or the real PATH.
const (
	helperEnv      = "FIREKEEPER_CLI_HELPER"
	passthroughEnv = "FK_PASSTHROUGH"
	exitEnv        = "FK_EXIT"
	readyEnv       = "FK_READY"
)

// runHelperProcess is called first by TestMain.
func runHelperProcess() {
	if os.Getenv(helperEnv) == "" {
		return
	}
	if filepath.Base(os.Args[0]) == serverBinName+exeSuffix() {
		os.Exit(fakeServerMain())
	}
	code, _ := RunSubcommand(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}

func fakeServerMain() int {
	if ready := os.Getenv(readyEnv); ready != "" {
		return fakeServerSleep(ready)
	}
	os.Stdout.WriteString("argv0=" + os.Args[0] + "\n")
	for _, a := range os.Args[1:] {
		os.Stdout.WriteString("arg=" + a + "\n")
	}
	os.Stdout.WriteString("passthrough=" + os.Getenv(passthroughEnv) + "\n")
	if os.Getenv(exitEnv) == "3" {
		return 3
	}
	if os.Getenv(exitEnv) == "7" {
		return 7
	}
	return 0
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// isolateServerLookup keeps every test from finding a developer's real
// firekeeper-server or from replacing the test process by running it.
func isolateServerLookup() {
	defaultLookup = serverLookup{
		getenv: func(string) string { return "" },
		executable: func() (string, error) {
			return filepath.Join(os.TempDir(), "firekeeper-no-such-dir", "firekeeper"), nil
		},
		lookPath: func(string) (string, error) { return "", exec.ErrNotFound },
	}
	startServer = func(string, []string, []string, io.Writer) int {
		panic("a test started the server without replacing startServer")
	}
}

func TestRunServeArguments(t *testing.T) {
	var gotPath string
	var gotArgv, gotEnv []string
	oldLookup, oldStart := defaultLookup, startServer
	t.Cleanup(func() { defaultLookup, startServer = oldLookup, oldStart })
	defaultLookup.getenv = func(k string) string {
		if k == ServerBinEnv {
			return fakeBinary(t, t.TempDir(), serverBinName)
		}
		return ""
	}
	startServer = func(path string, argv, env []string, _ io.Writer) int {
		gotPath, gotArgv, gotEnv = path, argv, env
		return 42
	}
	t.Setenv(passthroughEnv, "kept")

	var stdout, stderr bytes.Buffer
	code, handled := RunSubcommand([]string{"serve", "--listen", "127.0.0.1:0", "admin", "-x"}, &stdout, &stderr)
	if !handled || code != 42 {
		t.Fatalf("RunSubcommand = (%d, %v), want the server's status 42", code, handled)
	}
	if want := []string{"firekeeper-server", "serve", "--listen", "127.0.0.1:0", "admin", "-x"}; !slices.Equal(gotArgv, want) {
		t.Errorf("argv = %q, want %q", gotArgv, want)
	}
	if !strings.HasSuffix(gotPath, serverBinName) {
		t.Errorf("path = %q", gotPath)
	}
	if !slices.Contains(gotEnv, passthroughEnv+"=kept") {
		t.Errorf("environment was not passed through")
	}
	if stdout.Len()+stderr.Len() != 0 {
		t.Errorf("handoff printed %q %q", stdout.String(), stderr.String())
	}
}

func TestRunServeNotFound(t *testing.T) {
	var stderr bytes.Buffer
	if code := runServe([]string{"--listen", "x"}, nil, &stderr); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	for _, want := range []string{"separate commercial binary", "firekeeper-server", "README", "The dashboard server", ServerBinEnv} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("message lacks %q:\n%s", want, stderr.String())
		}
	}
	if n := strings.Count(stderr.String(), "\n"); n > 6 {
		t.Errorf("message is %d lines, want a short one", n)
	}
}

func TestUsageListsServe(t *testing.T) {
	var found bool
	for _, c := range subcommands {
		if c.name == "serve" {
			found = c.summary == "run the dashboard server (separate firekeeper-server binary)"
		}
		if c.name == "admin" || c.name == "account" {
			t.Errorf("%s was never a top-level command; it lives under serve", c.name)
		}
	}
	if !found {
		t.Error("serve is not listed with its handoff summary")
	}
}

// fakeBinary writes an executable script named name into dir and returns its
// path. Resolution only looks at the file's mode, so the content is a stub.
func fakeBinary(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeServerSleep is the sleep mode of the fake server: it announces itself,
// waits for SIGTERM, and exits 5 so the test can tell the signal arrived.
func fakeServerSleep(ready string) int {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
		return 1
	}
	select {
	case <-sigs:
		return 5
	case <-time.After(30 * time.Second):
		return 6
	}
}
