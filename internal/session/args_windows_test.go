//go:build windows

package session

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// windowsArgs follows the same rules as the system's CommandLineToArgvW.
func TestWindowsArgsMatchesCommandLineToArgvW(t *testing.T) {
	for _, command := range []string{
		`codex`,
		`"C:\Program Files\nodejs\node.exe" C:\npm\@openai\codex\bin\codex.js resume`,
		`C:\bin\kimi.exe -p "web ui" -m k2`,
		`a.exe "say \"hi\""`,
		`a.exe C:\dir\\ x`,
		`a.exe \\\"x`,
		`"C:\Users\me dev\kimi.exe" --foo "a b" web`,
	} {
		want, err := windows.DecomposeCommandLine(command)
		if err != nil {
			t.Fatal(err)
		}
		if got := windowsArgs(command); !slices.Equal(got, want) {
			t.Errorf("windowsArgs(%q) = %q, CommandLineToArgvW = %q", command, got, want)
		}
	}
}

// A stand-in process's command line, read from its own memory by the
// process listing, splits back into its arguments, space in the path
// included.
func TestProcessListCommandLineSplitsToArgsWindows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my dir")
	cmd := startHelper(t, dir, "kimi", "--foo", "a b", "web")
	pid := strconv.Itoa(cmd.Process.Pid)
	want := []string{filepath.Join(dir, "kimi.exe"), "--foo", "a b", "web"}
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, err := windowsProcessList(time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 5 && fields[0] == pid {
				got := fallbackArgs(strings.Join(fields[4:], " "))
				if slices.Equal(got, want) {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %s not listed with args %q", pid, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
