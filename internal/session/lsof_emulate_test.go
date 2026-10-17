package session

import (
	"strings"
	"testing"
	"time"
)

func TestEmulateLsofCurrentDirectories(t *testing.T) {
	dirs := map[int]string{10: `C:\work\a`, 20: "", 30: "C:\\work\\multi\nline"}
	cwd := func(pid int) string { return dirs[pid] }

	got := string(emulateLsof([]string{"-a", "-d", "cwd", "-Fn", "-p", "10,20,30,notapid,-4"}, cwd))
	want := "p10\nnC:\\work\\a\np30\nnC:\\work\\multi line\n"
	if got != want {
		t.Fatalf("emulateLsof = %q, want %q", got, want)
	}
	// The output must read back the way discovery reads real lsof output.
	d := standalone()
	var parsed []string
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "n") {
			parsed = append(parsed, d.sanitizeProcessCommand(line[1:]))
		}
	}
	if len(parsed) != 2 || parsed[0] != `C:\work\a` {
		t.Fatalf("parsed = %q", parsed)
	}
}

func TestEmulateLsofOpenFilesAreUnavailable(t *testing.T) {
	cwd := func(int) string { t.Fatal("cwd consulted for an open-files request"); return "" }
	if got := emulateLsof([]string{"-Fn", "-p", "10,20"}, cwd); len(got) != 0 {
		t.Fatalf("emulateLsof = %q, want nothing", got)
	}
	if got := emulateLsof(nil, cwd); len(got) != 0 {
		t.Fatalf("emulateLsof(nil) = %q", got)
	}
}

func TestFormatElapsedMatchesPS(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "00:00"},
		{0, "00:00"},
		{59 * time.Second, "00:59"},
		{61 * time.Second, "01:01"},
		{time.Hour, "01:00:00"},
		{26*time.Hour + 3*time.Minute + 4*time.Second, "1-02:03:04"},
	}
	for _, tt := range tests {
		if got := formatElapsed(tt.d); got != tt.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestPSLineParsesLikePS(t *testing.T) {
	d := standalone()
	line := psLine(4242, 100, 90*time.Second, "\"C:\\Program Files\\nodejs\\node.exe\" C:\\npm\\node_modules\\@openai\\codex\\bin\\codex.js\r\n --model gpt")
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
		t.Fatalf("line = %q, want exactly one trailing newline", line)
	}
	procs := d.parseProcesses(line)
	if len(procs) != 1 {
		t.Fatalf("parsed %d processes from %q", len(procs), line)
	}
	p := procs[0]
	if p.PID != 4242 || p.PPID != 100 || p.Tool != "Codex" || p.TTY != "" || p.Elapsed != "01:30" {
		t.Fatalf("process = %+v", p)
	}
}

func TestTrimWindowsDir(t *testing.T) {
	for in, want := range map[string]string{
		`C:\work\proj\`: `C:\work\proj`,
		`C:\work\proj`:  `C:\work\proj`,
		`C:\`:           `C:\`,
		``:              ``,
	} {
		if got := trimWindowsDir(in); got != want {
			t.Errorf("trimWindowsDir(%q) = %q, want %q", in, got, want)
		}
	}
}
