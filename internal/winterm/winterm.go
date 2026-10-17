// Package winterm brings the terminal window that hosts a process to the
// foreground on Windows.
//
// Windows has no TTY to match, so the dashboard walks from the selected
// runtime up through the processes that launched it until one owns a visible
// window: Windows Terminal, a classic console host, or an editor's terminal.
// Tabs inside a Windows Terminal window are not addressable from outside it,
// so Focus raises the window, not the tab.
package winterm

import (
	"errors"
	"strings"
)

// ErrUnsupported is returned on platforms other than Windows.
var ErrUnsupported = errors.New("terminal switching by window is only available on Windows")

// ErrNoWindow means no process in the chain from the runtime up to the shell
// that started it owns a visible window.
var ErrNoWindow = errors.New("no terminal window found for the selected runtime")

// maxDepth bounds the walk up the process tree, which also ends at a
// process whose parent is gone.
const maxDepth = 16

// Process is one entry of a process snapshot.
type Process struct {
	PID, PPID int
	// Exe is the executable file name, such as WindowsTerminal.exe.
	Exe string
}

// stopAt names processes whose windows are never a terminal: reaching one
// means the runtime was not started from a terminal window.
var stopAt = map[string]bool{"explorer.exe": true, "services.exe": true, "svchost.exe": true, "winlogon.exe": true}

// consoleHosts are the processes that own the window of a console program.
// A classic console window belongs to the host, which is a child of the
// program rather than an ancestor.
var consoleHosts = map[string]bool{"conhost.exe": true, "openconsole.exe": true}

// Candidates lists the processes whose windows may be the terminal for pids,
// nearest first: each runtime process, the console hosts it started, then its
// ancestors with theirs. snapshot is every process in the system.
func Candidates(snapshot []Process, pids []int) []int {
	byPID := make(map[int]Process, len(snapshot))
	children := make(map[int][]Process)
	for _, p := range snapshot {
		byPID[p.PID] = p
		children[p.PPID] = append(children[p.PPID], p)
	}
	var out []int
	seen := make(map[int]bool)
	add := func(pid int) {
		if pid > 0 && !seen[pid] {
			seen[pid] = true
			out = append(out, pid)
		}
	}
	for _, start := range pids {
		pid := start
		for depth := 0; depth < maxDepth && pid > 0; depth++ {
			p, ok := byPID[pid]
			if !ok || stopAt[strings.ToLower(p.Exe)] {
				break
			}
			add(pid)
			for _, child := range children[pid] {
				if consoleHosts[strings.ToLower(child.Exe)] {
					add(child.PID)
				}
			}
			if p.PPID == pid {
				break
			}
			pid = p.PPID
		}
	}
	return out
}

// AppName turns an executable name into the name shown in the dashboard
// status line.
func AppName(exe string) string {
	name := strings.TrimSuffix(strings.ToLower(exe), ".exe")
	switch name {
	case "windowsterminal":
		return "Windows Terminal"
	case "conhost", "openconsole":
		return "Console"
	case "code", "code - insiders":
		return "VS Code"
	case "":
		return "terminal"
	}
	return strings.TrimSuffix(exe, ".exe")
}
