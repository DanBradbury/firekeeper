package daemon

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const windowsExe = `C:\Users\dev\AppData\Local\Programs\firekeeper\firekeeper.exe`

func windowsService(t *testing.T) (*Service, *fakeRunner, *bytes.Buffer) {
	t.Helper()
	svc, runner, out := testService(t, "windows")
	svc.Executable = windowsExe
	svc.User = `DESKTOP-1\dev`
	svc.Env = map[string]string{"SystemRoot": `C:\Windows`}
	return svc, runner, out
}

var taskNotRegistered = reply{err: &CommandError{Name: "schtasks", Code: 1, Output: "ERROR: The system cannot find the file specified."}}

func TestTaskXMLGolden(t *testing.T) {
	plain, _, _ := windowsService(t)
	awkward, _, _ := windowsService(t)
	awkward.Executable = `C:\Users\dev & co\My Tools\fire<keeper>.exe`
	awkward.User = `CORP\o'brien`
	awkward.Args = []string{"--quiet", "--server", `http://h:1/a?b=1&c="x`, "--provider", "kimi"}
	for name, svc := range map[string]*Service{"task.xml": plain, "task_escaped.xml": awkward} {
		t.Run(name, func(t *testing.T) {
			got, err := svc.File()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", "service", name+".golden")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden file (run go test ./internal/daemon -update): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s differs from golden file; run go test ./internal/daemon -update and review the diff\n--- got\n%s", name, got)
			}
			// The file must be well-formed XML apart from its UTF-16 declaration.
			dec := xml.NewDecoder(bytes.NewReader(got))
			dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
			for {
				if _, err := dec.Token(); err != nil {
					if !errors.Is(err, io.EOF) {
						t.Fatalf("task file is not well-formed: %v", err)
					}
					break
				}
			}
		})
	}
}

func TestTaskRunsHiddenAsTheUserWithoutElevation(t *testing.T) {
	svc, _, _ := windowsService(t)
	content, err := svc.File()
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, want := range []string{
		`<LogonTrigger>`, `<LogonType>InteractiveToken</LogonType>`, `<RunLevel>LeastPrivilege</RunLevel>`,
		`<Command>C:\Windows\System32\conhost.exe</Command>`, `--headless`, `<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>`,
		`<RestartOnFailure>`, `<UserId>DESKTOP-1\dev</UserId>`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("task file lacks %q", want)
		}
	}
	for _, banned := range []string{"HighestAvailable", "<Password>", "S4U"} {
		if strings.Contains(text, banned) {
			t.Errorf("task file contains %q, which would need elevation or a stored password", banned)
		}
	}
}

func TestWindowsInstallWritesUTF16AndReadsFlagsBack(t *testing.T) {
	svc, runner, out := windowsService(t)
	svc.Args = []string{"--quiet", "--server", `https://dash.example/a b?x="1"`, "--provider", "codex", "--provider", "claude", "--interval", "30s"}
	if err := svc.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(svc.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 2 || raw[0] != 0xFF || raw[1] != 0xFE {
		t.Fatalf("task file lacks a UTF-16 byte order mark: % x", raw[:min(4, len(raw))])
	}
	if filepath.Base(svc.Path()) != "daemon-task.xml" || filepath.Dir(svc.Path()) != svc.DataDir() {
		t.Errorf("task file = %s", svc.Path())
	}
	got, err := svc.InstalledArgs()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, svc.Args) {
		t.Fatalf("InstalledArgs = %q, want %q", got, svc.Args)
	}
	wantCalls := []string{
		"schtasks /End /TN Firekeeper Daemon",
		"schtasks /Create /TN Firekeeper Daemon /XML " + svc.Path() + " /F",
		"schtasks /Run /TN Firekeeper Daemon",
	}
	if !slices.Equal(runner.calls, wantCalls) {
		t.Fatalf("calls = %q, want %q", runner.calls, wantCalls)
	}
	if !strings.Contains(out.String(), "started") {
		t.Errorf("output = %q", out.String())
	}
}

func TestWindowsInstallIgnoresTaskNotRunning(t *testing.T) {
	svc, runner, _ := windowsService(t)
	runner.reply("schtasks /End /TN Firekeeper Daemon", reply{err: &CommandError{Name: "schtasks", Code: 1}})
	if err := svc.Install(context.Background()); err != nil {
		t.Fatalf("a task that was not running must not fail install: %v", err)
	}
}

func TestWindowsInstallReportsCreateFailure(t *testing.T) {
	svc, runner, _ := windowsService(t)
	runner.reply("schtasks /Create /TN Firekeeper Daemon /XML "+svc.Path()+" /F", reply{err: &CommandError{Name: "schtasks", Code: 1, Output: "ERROR: Access is denied."}})
	err := svc.Install(context.Background())
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) || !strings.Contains(err.Error(), "Access is denied") {
		t.Fatalf("err = %v", err)
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "schtasks /Run") {
			t.Fatal("started a task that failed to register")
		}
	}
}

func TestWindowsDryRunCommandsMatchInstall(t *testing.T) {
	svc, runner, _ := windowsService(t)
	var dry []string
	for _, c := range svc.Commands() {
		dry = append(dry, strings.Join(c, " "))
	}
	if err := svc.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(dry, runner.calls) {
		t.Fatalf("dry run %q differs from install %q", dry, runner.calls)
	}
}

func TestWindowsUninstall(t *testing.T) {
	t.Run("registered", func(t *testing.T) {
		svc, runner, _ := windowsService(t)
		if err := svc.Install(context.Background()); err != nil {
			t.Fatal(err)
		}
		runner.calls = nil
		if err := svc.Uninstall(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		want := []string{
			"schtasks /Query /TN Firekeeper Daemon",
			"schtasks /End /TN Firekeeper Daemon",
			"schtasks /Delete /TN Firekeeper Daemon /F",
		}
		if !slices.Equal(runner.calls, want) {
			t.Fatalf("calls = %q, want %q", runner.calls, want)
		}
		if _, err := os.Stat(svc.Path()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("task file still exists: %v", err)
		}
	})
	t.Run("never installed", func(t *testing.T) {
		svc, runner, _ := windowsService(t)
		runner.reply("schtasks /Query /TN Firekeeper Daemon", taskNotRegistered)
		if err := svc.Uninstall(context.Background(), false); !errors.Is(err, ErrNotInstalled) {
			t.Fatalf("err = %v, want ErrNotInstalled", err)
		}
	})
	t.Run("file left behind", func(t *testing.T) {
		svc, runner, _ := windowsService(t)
		runner.reply("schtasks /Query /TN Firekeeper Daemon", taskNotRegistered)
		if err := os.MkdirAll(svc.DataDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(svc.Path(), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := svc.Uninstall(context.Background(), false); err != nil {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(svc.Path()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("task file still exists: %v", err)
		}
	})
}

func TestWindowsStatus(t *testing.T) {
	query := "schtasks /Query /TN Firekeeper Daemon /FO CSV /NH /V"
	csvRow := `"DESKTOP-1","\Firekeeper Daemon","N/A","Running","Interactive only","10/17/2026 9:00:00 AM","267009","dev"`
	t.Run("running", func(t *testing.T) {
		svc, runner, _ := windowsService(t)
		runner.reply(query, reply{out: csvRow})
		if err := os.MkdirAll(svc.DataDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		lock := filepath.Join(svc.DataDir(), "daemon.lock")
		if err := os.WriteFile(lock, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := svc.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !st.Loaded || !st.Running || st.PID != os.Getpid() || st.State != "running" || st.LastExit != "" {
			t.Fatalf("status = %+v", st)
		}
	})
	t.Run("registered but stopped", func(t *testing.T) {
		svc, runner, _ := windowsService(t)
		runner.reply(query, reply{out: `"H","\Firekeeper Daemon","N/A","Ready","Interactive only","10/17/2026","1","dev"`})
		st, err := svc.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !st.Loaded || st.Running || st.State != "ready" || st.LastExit != "1" {
			t.Fatalf("status = %+v", st)
		}
	})
	t.Run("not registered", func(t *testing.T) {
		svc, runner, _ := windowsService(t)
		runner.reply(query, taskNotRegistered)
		st, err := svc.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if st.Loaded || st.Running {
			t.Fatalf("status = %+v", st)
		}
	})
}

func TestTaskLastResult(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"success", `"H","\T","N/A","Ready","x","d","0","a"`, "0"},
		{"failure", `"H","\T","N/A","Ready","x","d","1","a"`, "1"},
		{"still active", `"H","\T","N/A","Running","x","d","267009","a"`, ""},
		{"hresult", `"H","\T","N/A","Ready","x","d","-2147020576","a"`, "0x800710e0"},
		{"too short", `"H","\T"`, ""},
		{"not a number", `"H","\T","N/A","Ready","x","d","never","a"`, ""},
		{"empty", ``, ""},
	}
	for _, tt := range tests {
		if got := taskLastResult(tt.in); got != tt.want {
			t.Errorf("%s: taskLastResult = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestWindowsCommandLineRoundTrip(t *testing.T) {
	tests := [][]string{
		{`C:\Program Files\firekeeper\firekeeper.exe`, "daemon", "--quiet"},
		{"a", "", "b"},
		{`say "hi"`, `back\slash`, `trailing\`, `trailing space\ `, `\\server\share`},
		{`quote\"mid`, `ends in quote"`, "tab\there", "ünïcode ✓"},
	}
	for _, args := range tests {
		line := joinWindowsArgs(args)
		if got := splitWindowsCommandLine(line); !slices.Equal(got, args) {
			t.Errorf("round trip of %q via %q gave %q", args, line, got)
		}
	}
	// Plain arguments stay unquoted, so the task shows readable flags.
	if got := joinWindowsArgs([]string{"daemon", "--provider", "codex"}); got != "daemon --provider codex" {
		t.Errorf("plain args = %q", got)
	}
}

func TestSplitWindowsCommandLineMatchesCommandLineToArgv(t *testing.T) {
	// Cases from the CommandLineToArgvW documentation.
	tests := []struct {
		in   string
		want []string
	}{
		{`"abc" d e`, []string{"abc", "d", "e"}},
		{`a\\\b d"e f"g h`, []string{`a\\\b`, `de fg`, "h"}},
		{`a\\\"b c d`, []string{`a\"b`, "c", "d"}},
		{`a\\\\"b c" d e`, []string{`a\\b c`, "d", "e"}},
		{`  leading   spaces  `, []string{"leading", "spaces"}},
		{``, nil},
	}
	for _, tt := range tests {
		if got := splitWindowsCommandLine(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("split(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestUTF16RoundTrip(t *testing.T) {
	in := []byte("<a>ünï ✓ 𝄞</a>\r\n")
	enc := encodeUTF16(in)
	if enc[0] != 0xFF || enc[1] != 0xFE {
		t.Fatalf("no BOM: % x", enc[:2])
	}
	got, err := decodeText(enc)
	if err != nil || got != string(in) {
		t.Fatalf("decode = %q, %v", got, err)
	}
	if got, _ := decodeText([]byte("\xef\xbb\xbfplain")); got != "plain" {
		t.Errorf("UTF-8 BOM not stripped: %q", got)
	}
	if _, err := decodeText([]byte{0xFF, 0xFE, 0x61}); err == nil {
		t.Error("odd-length UTF-16 accepted")
	}
}

func TestTaskProgramArgsRejectsOtherFiles(t *testing.T) {
	for name, data := range map[string][]byte{
		"not xml":   []byte("hello"),
		"no exec":   []byte(`<?xml version="1.0"?><Task><Actions/></Task>`),
		"bad utf16": {0xFF, 0xFE, 0x61},
	} {
		if _, err := taskProgramArgs(data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestIsAbsolute(t *testing.T) {
	for _, tt := range []struct {
		goos, path string
		want       bool
	}{
		{"windows", `C:\Users\dev\firekeeper.exe`, true},
		{"windows", `c:/Users/dev/firekeeper.exe`, true},
		{"windows", `\\server\share\firekeeper.exe`, true},
		{"windows", `firekeeper.exe`, false},
		{"windows", `\Users\dev`, false},
		{"windows", `1:\x`, false},
		{"linux", "/usr/bin/firekeeper", true},
		{"linux", "firekeeper", false},
	} {
		if got := isAbsolute(tt.goos, tt.path); got != tt.want {
			t.Errorf("isAbsolute(%q, %q) = %v, want %v", tt.goos, tt.path, got, tt.want)
		}
	}
}
