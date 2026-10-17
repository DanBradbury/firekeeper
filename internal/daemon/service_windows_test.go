//go:build windows

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/testenv"
)

// TestTaskSchedulerRoundTrip registers a real Task Scheduler task under a
// test-only name, and removes it. It runs only in CI: on a developer's
// machine it would still be harmless, but a task created in the middle of a
// test run is not something to do unasked.
func TestTaskSchedulerRoundTrip(t *testing.T) {
	if !testenv.InCI() {
		t.Skip("registers a real scheduled task; runs in CI")
	}
	exe := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	svc, err := NewService([]string{"--quiet", "--provider", "codex"}, os.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	svc.Home = t.TempDir()
	// A harmless program: cmd.exe given "daemon" as a command exits at once,
	// so installing never starts a real daemon.
	svc.Executable = exe
	svc.TaskName = "Firekeeper Daemon CI Test"
	ctx := context.Background()
	t.Cleanup(func() { _ = svc.Uninstall(ctx, true) })

	if err := svc.Install(ctx); err != nil {
		t.Fatalf("Install: %v", err)
	}
	st, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || !st.Loaded {
		t.Fatalf("status after install = %+v", st)
	}
	args, err := svc.InstalledArgs()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 3 || args[0] != "--quiet" || args[2] != "codex" {
		t.Fatalf("InstalledArgs = %q", args)
	}
	// Installing again replaces the task rather than failing.
	if err := svc.Install(ctx); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if err := svc.Uninstall(ctx, true); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	st, err = svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Loaded || st.Installed {
		t.Fatalf("status after uninstall = %+v", st)
	}
}
