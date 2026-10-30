//go:build windows

package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// startSleeper runs a child that lives for a while, in dir.
func startSleeper(t *testing.T, dir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("ping", "-n", "30", "127.0.0.1")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func TestWindowsProcessListReportsChildren(t *testing.T) {
	child := startSleeper(t, t.TempDir())
	out, err := windowsProcessList(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(string(out), "\n") {
		if fields := strings.Fields(l); len(fields) >= 5 && fields[0] == strconv.Itoa(child.Process.Pid) {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("child pid %d is not in the process list", child.Process.Pid)
	}
	fields := strings.Fields(line)
	if fields[1] != strconv.Itoa(os.Getpid()) {
		t.Errorf("parent = %s, want %d", fields[1], os.Getpid())
	}
	if fields[2] != "?" {
		t.Errorf("tty = %q, want ?", fields[2])
	}
	if !strings.Contains(strings.ToLower(line), "ping") || !strings.Contains(line, "127.0.0.1") {
		t.Errorf("command line not read: %q", line)
	}
}

func TestWindowsProcessListFeedsDiscovery(t *testing.T) {
	// The ps-shaped text must parse with the same code the Unix path uses.
	out, err := nativeCommand2("ps")
	if err != nil {
		t.Fatal(err)
	}
	d := standalone()
	for _, p := range d.parseProcesses(string(out)) {
		if p.PID <= 0 || p.Command == "" {
			t.Fatalf("unparsed process %+v", p)
		}
	}
}

func nativeCommand2(name string, args ...string) ([]byte, error) {
	out, handled, err := nativeCommand(name, args)
	if !handled {
		panic("nativeCommand did not handle " + name)
	}
	return out, err
}

func TestWindowsProcessCWD(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	child := startSleeper(t, dir)
	// The child needs a moment to finish starting before its PEB is filled in.
	var got string
	for i := 0; i < 50 && got == ""; i++ {
		got = windowsProcessCWD(child.Process.Pid)
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.EqualFold(filepath.Clean(got), filepath.Clean(dir)) {
		t.Fatalf("cwd = %q, want %q", got, dir)
	}
	out, err := nativeCommand2("lsof", "-a", "-d", "cwd", "-Fn", "-p", strconv.Itoa(child.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	want := "p" + strconv.Itoa(child.Process.Pid) + "\nn"
	if !strings.HasPrefix(string(out), want) {
		t.Fatalf("lsof output = %q, want prefix %q", out, want)
	}
}

func TestWindowsProcessCWDOfMissingProcess(t *testing.T) {
	if got := windowsProcessCWD(0x7ffffff0); got != "" {
		t.Fatalf("cwd of a missing process = %q", got)
	}
}

// SEC-26-1: a UNICODE_STRING read from another process is only trusted when
// its byte length is even, non-zero, and within MaximumLength.
func TestRemoteStringUnitsRejectsBadLengths(t *testing.T) {
	var buf [4]uint16
	p := &buf[0]
	tests := []struct {
		length, max uint16
		buffer      *uint16
		want        int
		ok          bool
	}{
		{8, 8, p, 4, true},
		{2, 10, p, 1, true},
		{65534, 65534, p, 32767, true},
		{0, 8, p, 0, false},
		{1, 8, p, 0, false},
		{3, 8, p, 0, false},
		{65535, 65535, p, 0, false},
		{10, 8, p, 0, false},
		{8, 8, nil, 0, false},
	}
	for _, tt := range tests {
		got, ok := remoteStringUnits(windows.NTUnicodeString{Length: tt.length, MaximumLength: tt.max, Buffer: tt.buffer})
		if got != tt.want || ok != tt.ok {
			t.Errorf("Length %d, MaximumLength %d, buffer %v: got (%d, %v), want (%d, %v)", tt.length, tt.max, tt.buffer != nil, got, ok, tt.want, tt.ok)
		}
	}
}
