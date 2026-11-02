package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/winterm"
)

// matrixProviders is every harness the dashboard can discover. Switching must
// not depend on which one owns the selected runtime.
var matrixProviders = []string{"Codex", "Copilot", "Claude", "Kimi", "OpenCode", "Gemini", "Cursor"}

// macAdapterMarkers maps each AppleScript adapter to text unique to its script.
var macAdapterMarkers = map[string]string{
	"iTerm2":       "select terminalSession",
	"Terminal.app": "com.apple.Terminal",
	"Ghostty":      "com.mitchellh.ghostty",
}

func matrixGroup(provider, tty string) processGroup {
	return processGroup{
		tool: provider,
		root: processInfo{tool: provider, pid: 100, ppid: 1, tty: tty, command: strings.ToLower(provider)},
		processes: []processInfo{
			{tool: provider, pid: 101, ppid: 100, tty: tty, command: strings.ToLower(provider) + " helper"},
		},
	}
}

// fakeOsascript reports "matched" only for the adapter named owner and
// "not-found" for the other running terminals.
func fakeOsascript(owner string) func(string, ...string) (string, error) {
	return func(script string, _ ...string) (string, error) {
		for name, marker := range macAdapterMarkers {
			if strings.Contains(script, marker) {
				if name == owner {
					return "matched", nil
				}
				return "not-found", nil
			}
		}
		return "", errors.New("unknown adapter script")
	}
}

func TestEveryProviderSwitchesToEveryMacTerminal(t *testing.T) {
	for _, provider := range matrixProviders {
		for owner := range macAdapterMarkers {
			t.Run(provider+"/"+owner, func(t *testing.T) {
				target := newTerminalSwitchTarget(matrixGroup(provider, "ttys042"), -1)
				result := switchTerminalTargetWith(target, noHerdr, func(tty, cwd string) (string, error) {
					return focusTerminalSessionWith(tty, "/work/repo", "darwin", fakeOsascript(owner))
				})
				if result.err != nil || result.app != owner || result.tty != "ttys042" {
					t.Fatalf("switch = %#v, want %s on ttys042", result, owner)
				}
			})
		}
	}
}

func TestEveryProviderSwitchesTabsInsideOneTerminalApp(t *testing.T) {
	// Two runtimes of one provider sit in different tabs: each switch carries
	// the TTY of the selected runtime, never the other's.
	for _, provider := range matrixProviders {
		t.Run(provider, func(t *testing.T) {
			var seen []string
			run := func(script string, args ...string) (string, error) {
				if strings.Contains(script, macAdapterMarkers["iTerm2"]) {
					seen = append(seen, args[0])
					return "matched", nil
				}
				return "not-found", nil
			}
			for _, tty := range []string{"ttys001", "ttys002"} {
				target := newTerminalSwitchTarget(matrixGroup(provider, tty), -1)
				if _, err := focusTerminalSessionWith(target.tty, target.cwd, "darwin", run); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Join(seen, ",") != "/dev/ttys001,/dev/ttys002" {
				t.Fatalf("tabs targeted = %v", seen)
			}
		})
	}
}

func TestEveryProviderSwitchesGhosttyByCWDWhenOtherTerminalsAreClosed(t *testing.T) {
	for _, provider := range matrixProviders {
		t.Run(provider, func(t *testing.T) {
			var gotCWD string
			run := func(script string, args ...string) (string, error) {
				if strings.Contains(script, macAdapterMarkers["Ghostty"]) {
					gotCWD = args[1]
					return "matched:term-1", nil
				}
				return "not-running", nil
			}
			group := matrixGroup(provider, "ttys077")
			group.sessions = []sessionInfo{{id: "s1", cwd: "/work/" + provider}}
			target := newTerminalSwitchTarget(group, 0)
			app, err := focusTerminalSessionWith(target.tty, target.cwd, "darwin", run)
			if err != nil || app != "Ghostty" || gotCWD != "/work/"+provider {
				t.Fatalf("app %q cwd %q err %v", app, gotCWD, err)
			}
		})
	}
}

func TestEveryProviderWithoutTTYReportsMissingTTYOnMac(t *testing.T) {
	for _, provider := range matrixProviders {
		target := newTerminalSwitchTarget(matrixGroup(provider, ""), -1)
		_, err := focusTerminalSessionWith(target.tty, target.cwd, "darwin", fakeOsascript("none"))
		if err == nil || !strings.Contains(err.Error(), "no controlling TTY") {
			t.Errorf("%s: err = %v", provider, err)
		}
	}
}

func TestEveryProviderSwitchIsRejectedOnLinux(t *testing.T) {
	for _, provider := range matrixProviders {
		target := newTerminalSwitchTarget(matrixGroup(provider, "pts/1"), -1)
		if _, err := focusTerminalSessionWith(target.tty, target.cwd, "linux", nil); err == nil {
			t.Errorf("%s: linux switch accepted", provider)
		}
	}
}

func TestEveryProviderSwitchesToWindowsTerminalHost(t *testing.T) {
	// Windows has no TTY: the target carries PIDs and winterm walks up from
	// them to the window that hosts the runtime.
	snapshot := []winterm.Process{
		{PID: 4, PPID: 0, Exe: "System"},
		{PID: 10, PPID: 4, Exe: "explorer.exe"},
		{PID: 20, PPID: 10, Exe: "WindowsTerminal.exe"},
		{PID: 30, PPID: 20, Exe: "OpenConsole.exe"},
		{PID: 40, PPID: 30, Exe: "pwsh.exe"},
		{PID: 100, PPID: 40, Exe: "agent.exe"},
		{PID: 101, PPID: 100, Exe: "node.exe"},
	}
	for _, provider := range matrixProviders {
		t.Run(provider, func(t *testing.T) {
			target := newTerminalSwitchTarget(matrixGroup(provider, ""), -1)
			var candidates []int
			result := switchTerminalTargetWith(target, noHerdr, func(string, string) (string, error) {
				candidates = winterm.Candidates(snapshot, target.processPIDs)
				return winterm.AppName("WindowsTerminal.exe"), nil
			})
			if result.err != nil || result.app != "Windows Terminal" {
				t.Fatalf("result = %#v", result)
			}
			hasTerminal := false
			for _, pid := range candidates {
				if pid == 10 {
					t.Fatal("explorer.exe offered as a terminal")
				}
				hasTerminal = hasTerminal || pid == 20
			}
			if !hasTerminal {
				t.Fatalf("Windows Terminal not among candidates %v", candidates)
			}
		})
	}
}
