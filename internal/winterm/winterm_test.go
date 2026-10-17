package winterm

import (
	"slices"
	"testing"
)

func TestCandidatesWalkUpToTheTerminal(t *testing.T) {
	// WindowsTerminal.exe > OpenConsole.exe > pwsh.exe > node.exe (the agent) > node.exe (a child)
	snapshot := []Process{
		{PID: 4, PPID: 0, Exe: "System"},
		{PID: 100, PPID: 4, Exe: "explorer.exe"},
		{PID: 200, PPID: 100, Exe: "WindowsTerminal.exe"},
		{PID: 210, PPID: 200, Exe: "OpenConsole.exe"},
		{PID: 220, PPID: 210, Exe: "pwsh.exe"},
		{PID: 230, PPID: 220, Exe: "node.exe"},
		{PID: 240, PPID: 230, Exe: "node.exe"},
	}
	got := Candidates(snapshot, []int{230, 240})
	want := []int{230, 220, 210, 200, 240}
	if !slices.Equal(got, want) {
		t.Fatalf("Candidates = %v, want %v", got, want)
	}
	for _, pid := range got {
		if pid == 100 {
			t.Fatal("explorer.exe is not a terminal and must never be raised")
		}
	}
}

func TestCandidatesIncludeConsoleHostChildren(t *testing.T) {
	// A classic console: conhost.exe is a child of the program, not its parent.
	snapshot := []Process{
		{PID: 100, PPID: 4, Exe: "explorer.exe"},
		{PID: 300, PPID: 100, Exe: "cmd.exe"},
		{PID: 310, PPID: 300, Exe: "conhost.exe"},
		{PID: 320, PPID: 300, Exe: "codex.exe"},
		{PID: 330, PPID: 300, Exe: "notepad.exe"},
	}
	got := Candidates(snapshot, []int{320})
	want := []int{320, 300, 310}
	if !slices.Equal(got, want) {
		t.Fatalf("Candidates = %v, want %v (notepad.exe must not be a candidate)", got, want)
	}
}

func TestCandidatesSurviveCyclesAndMissingParents(t *testing.T) {
	cyclic := []Process{{PID: 1, PPID: 2, Exe: "a.exe"}, {PID: 2, PPID: 1, Exe: "b.exe"}}
	if got := Candidates(cyclic, []int{1}); len(got) != 2 {
		t.Errorf("cyclic tree = %v", got)
	}
	self := []Process{{PID: 7, PPID: 7, Exe: "a.exe"}}
	if got := Candidates(self, []int{7}); !slices.Equal(got, []int{7}) {
		t.Errorf("self parent = %v", got)
	}
	if got := Candidates(nil, []int{7, 0, -1}); len(got) != 0 {
		t.Errorf("unknown pids = %v", got)
	}
	deep := []Process{}
	for i := 1; i <= 100; i++ {
		deep = append(deep, Process{PID: i, PPID: i + 1, Exe: "x.exe"})
	}
	if got := Candidates(deep, []int{1}); len(got) != maxDepth {
		t.Errorf("walk length = %d, want it capped at %d", len(got), maxDepth)
	}
}

func TestAppName(t *testing.T) {
	for exe, want := range map[string]string{
		"WindowsTerminal.exe": "Windows Terminal",
		"conhost.exe":         "Console",
		"OpenConsole.exe":     "Console",
		"Code.exe":            "VS Code",
		"wezterm-gui.exe":     "wezterm-gui",
		"":                    "terminal",
	} {
		if got := AppName(exe); got != want {
			t.Errorf("AppName(%q) = %q, want %q", exe, got, want)
		}
	}
}
