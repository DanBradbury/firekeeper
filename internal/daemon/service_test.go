package daemon

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fakeRunner records commands and answers them from replies, keyed by the
// joined command line. Unknown commands succeed with no output. It fails
// the test on anything but launchctl or systemctl, which also catches sudo.
type fakeRunner struct {
	t       *testing.T
	calls   []string
	replies map[string][]reply
}

type reply struct {
	out string
	err error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "launchctl" && name != "systemctl" {
		f.t.Fatalf("ran %q; only launchctl and systemctl are allowed", name)
	}
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	queue := f.replies[line]
	if len(queue) == 0 {
		return nil, nil
	}
	r := queue[0]
	if len(queue) > 1 {
		f.replies[line] = queue[1:]
	}
	return []byte(r.out), r.err
}

func (f *fakeRunner) reply(line string, replies ...reply) {
	if f.replies == nil {
		f.replies = map[string][]reply{}
	}
	f.replies[line] = replies
}

var notLoaded = reply{err: &CommandError{Name: "launchctl", Code: 113, Output: "Could not find service"}}

func testService(t *testing.T, goos string) (*Service, *fakeRunner, *bytes.Buffer) {
	t.Helper()
	runner := &fakeRunner{t: t}
	out := &bytes.Buffer{}
	return &Service{
		GOOS:       goos,
		Home:       t.TempDir(),
		UID:        501,
		Executable: "/usr/local/bin/firekeeper",
		Args:       []string{"--quiet", "--server", "http://127.0.0.1:7777", "--provider", "codex", "--interval", "15s"},
		Env:        map[string]string{"PATH": "/usr/bin:/bin"},
		Runner:     runner,
		Out:        out,
		Sleep:      func(time.Duration) {},
	}, runner, out
}

func TestServiceFilesGolden(t *testing.T) {
	base := Service{
		Home:       "/home/dev",
		UID:        501,
		Executable: "/usr/local/bin/firekeeper",
		Args:       []string{"--quiet", "--server", "http://127.0.0.1:7777", "--provider", "codex", "--provider", "copilot", "--interval", "15s"},
		Env: map[string]string{
			"PATH":       "/opt/homebrew/bin:/usr/bin:/bin",
			"CODEX_HOME": "/home/dev/.codex",
		},
	}
	// Characters that need escaping in XML or in a unit file.
	awkward := base
	awkward.Executable = "/home/dev/My Tools/fire&keeper"
	awkward.Args = []string{"--quiet", "--server", `http://h:1/a?b=1&c="%x$y\z`, "--provider", "kimi", "--interval", "1m0s"}
	awkward.Env = map[string]string{"COPILOT_HOME": `/home/dev/<co>pilot 100%$HOME`}
	bare := base
	bare.Env = nil

	tests := []struct {
		name string
		goos string
		svc  Service
	}{
		{"launchd.plist", "darwin", base},
		{"launchd_escaped.plist", "darwin", awkward},
		{"launchd_noenv.plist", "darwin", bare},
		{"systemd.service", "linux", base},
		{"systemd_escaped.service", "linux", awkward},
		{"systemd_noenv.service", "linux", bare},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := tt.svc
			svc.GOOS = tt.goos
			got, err := svc.File()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", "service", tt.name+".golden")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden file (run go test ./internal/daemon -update): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s differs from golden file; run go test ./internal/daemon -update and review the diff\n--- got\n%s", tt.name, got)
			}
		})
	}
}

func TestServicePaths(t *testing.T) {
	svc := Service{GOOS: "darwin", Home: "/Users/dev"}
	if got := svc.Path(); got != "/Users/dev/Library/LaunchAgents/dev.firekeeper.daemon.plist" {
		t.Fatalf("darwin path = %q", got)
	}
	svc.GOOS = "linux"
	if got := svc.Path(); got != "/Users/dev/.config/systemd/user/firekeeper.service" {
		t.Fatalf("linux path = %q", got)
	}
	svc.Env = map[string]string{"XDG_CONFIG_HOME": "/xdg"}
	if got := svc.Path(); got != "/xdg/systemd/user/firekeeper.service" {
		t.Fatalf("linux XDG path = %q", got)
	}
	// A relative XDG_CONFIG_HOME is invalid per the spec and ignored.
	svc.Env = map[string]string{"XDG_CONFIG_HOME": "rel"}
	if got := svc.Path(); got != "/Users/dev/.config/systemd/user/firekeeper.service" {
		t.Fatalf("linux relative XDG path = %q", got)
	}
}

func TestFileRejectsBadSetups(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Service)
		want string
	}{
		{"windows", func(s *Service) { s.GOOS = "windows" }, "not supported on windows"},
		{"relative", func(s *Service) { s.Executable = "firekeeper" }, "not absolute"},
		{"go run", func(s *Service) { s.Executable = "/Users/dev/Library/Caches/go-build/ab/exe/firekeeper" }, "temporary build"},
		{"temp dir", func(s *Service) { s.Executable = filepath.Join(os.TempDir(), "go-run-1", "firekeeper") }, "temporary build"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, runner, _ := testService(t, "darwin")
			tt.edit(svc)
			if _, err := svc.File(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("File error = %v, want %q", err, tt.want)
			}
			if err := svc.Install(context.Background()); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Install error = %v, want %q", err, tt.want)
			}
			if len(runner.calls) > 0 {
				t.Fatalf("ran %v", runner.calls)
			}
			if _, err := os.Stat(svc.Path()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("service file written: %v", err)
			}
		})
	}
}

func TestInstallLaunchdFresh(t *testing.T) {
	svc, runner, out := testService(t, "darwin")
	runner.reply("launchctl print gui/501/dev.firekeeper.daemon", notLoaded)
	if err := svc.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"launchctl print gui/501/dev.firekeeper.daemon",
		"launchctl enable gui/501/dev.firekeeper.daemon",
		"launchctl bootstrap gui/501 " + svc.Path(),
	}
	if !slices.Equal(runner.calls, want) {
		t.Fatalf("calls = %q, want %q", runner.calls, want)
	}
	assertServiceFile(t, svc)
	if info, err := os.Stat(svc.DataDir()); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("data dir = %v, %v", info, err)
	}
	if !strings.Contains(out.String(), "started") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestInstallLaunchdReplacesLoadedAndRetriesBootstrap(t *testing.T) {
	svc, runner, _ := testService(t, "darwin")
	if err := os.MkdirAll(filepath.Dir(svc.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(svc.Path(), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	busy := reply{err: &CommandError{Name: "launchctl", Code: 5, Output: "Bootstrap failed: 5: Input/output error"}}
	bootstrap := "launchctl bootstrap gui/501 " + svc.Path()
	runner.reply(bootstrap, busy, busy, reply{})
	var slept []time.Duration
	svc.Sleep = func(d time.Duration) { slept = append(slept, d) }
	if err := svc.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"launchctl print gui/501/dev.firekeeper.daemon",
		"launchctl bootout gui/501/dev.firekeeper.daemon",
		"launchctl enable gui/501/dev.firekeeper.daemon",
		bootstrap, bootstrap, bootstrap,
	}
	if !slices.Equal(runner.calls, want) {
		t.Fatalf("calls = %q, want %q", runner.calls, want)
	}
	if len(slept) != 2 {
		t.Fatalf("slept = %v", slept)
	}
	assertServiceFile(t, svc)
}

func TestInstallLaunchdGivesUp(t *testing.T) {
	svc, runner, _ := testService(t, "darwin")
	runner.reply("launchctl print gui/501/dev.firekeeper.daemon", notLoaded)
	busy := reply{err: &CommandError{Name: "launchctl", Code: 5, Output: "Bootstrap failed: 5: Input/output error\nmore"}}
	runner.reply("launchctl bootstrap gui/501 "+svc.Path(), busy)
	err := svc.Install(context.Background())
	if err == nil || err.Error() != "launchctl exited with status 5: Bootstrap failed: 5: Input/output error" {
		t.Fatalf("err = %v", err)
	}
	if n := len(runner.calls); n != 2+bootstrapRetries {
		t.Fatalf("calls = %q", runner.calls)
	}
}

func TestInstallSystemd(t *testing.T) {
	svc, runner, _ := testService(t, "linux")
	if err := svc.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable firekeeper.service",
		"systemctl --user restart firekeeper.service",
	}
	if !slices.Equal(runner.calls, want) {
		t.Fatalf("calls = %q, want %q", runner.calls, want)
	}
	if !strings.HasPrefix(svc.Path(), filepath.Join(svc.Home, ".config", "systemd", "user")) {
		t.Fatalf("path = %q", svc.Path())
	}
	assertServiceFile(t, svc)
}

func TestInstallSystemdStopsOnFailure(t *testing.T) {
	svc, runner, _ := testService(t, "linux")
	runner.reply("systemctl --user daemon-reload", reply{err: &CommandError{Name: "systemctl", Code: 1, Output: "Failed to connect to bus"}})
	if err := svc.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %q", runner.calls)
	}
}

func TestCommandsMatchInstall(t *testing.T) {
	svc, _, _ := testService(t, "linux")
	got := svc.Commands()
	if len(got) != 3 || strings.Join(got[2], " ") != "systemctl --user restart firekeeper.service" {
		t.Fatalf("linux commands = %q", got)
	}
	svc.GOOS = "darwin"
	got = svc.Commands()
	if len(got) != 3 || strings.Join(got[2], " ") != "launchctl bootstrap gui/501 "+svc.Path() {
		t.Fatalf("darwin commands = %q", got)
	}
}

func assertServiceFile(t *testing.T, svc *Service) {
	t.Helper()
	got, err := os.ReadFile(svc.Path())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := svc.File()
	if !bytes.Equal(got, want) {
		t.Fatalf("service file = %s", got)
	}
	info, _ := os.Stat(svc.Path())
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v", info.Mode())
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(svc.Path()), ".firekeeper-service-*"))
	if len(leftovers) > 0 {
		t.Fatalf("temp files left: %v", leftovers)
	}
}

func writeData(t *testing.T, svc *Service, names ...string) {
	t.Helper()
	if err := os.MkdirAll(svc.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(svc.DataDir(), name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func installFile(t *testing.T, svc *Service) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(svc.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(svc.Path(), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUninstallLaunchdKeepsData(t *testing.T) {
	svc, runner, out := testService(t, "darwin")
	installFile(t, svc)
	writeData(t, svc, "state.json", "machine-id", "daemon.log")
	if err := svc.Uninstall(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"launchctl print gui/501/dev.firekeeper.daemon",
		"launchctl bootout gui/501/dev.firekeeper.daemon",
	}
	if !slices.Equal(runner.calls, want) {
		t.Fatalf("calls = %q, want %q", runner.calls, want)
	}
	if _, err := os.Stat(svc.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("service file still there: %v", err)
	}
	for _, name := range []string{"state.json", "machine-id", "daemon.log"} {
		if _, err := os.Stat(filepath.Join(svc.DataDir(), name)); err != nil {
			t.Fatalf("%s removed without --purge", name)
		}
	}
	if !strings.Contains(out.String(), "--purge") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestUninstallLaunchdBootoutFailureKeepsFile(t *testing.T) {
	svc, runner, _ := testService(t, "darwin")
	installFile(t, svc)
	runner.reply("launchctl bootout gui/501/dev.firekeeper.daemon", reply{err: &CommandError{Name: "launchctl", Code: 1}})
	if err := svc.Uninstall(context.Background(), true); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(svc.Path()); err != nil {
		t.Fatalf("service file removed after failed stop: %v", err)
	}
}

func TestUninstallPurge(t *testing.T) {
	tests := []struct {
		name    string
		extra   []string
		dirGone bool
	}{
		{"only daemon files", nil, true},
		{"keeps dashboard", []string{"dashboard.db"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, runner, _ := testService(t, "linux")
			installFile(t, svc)
			daemonFiles := []string{"state.json", ".state-123.json", "machine-id", ".machine-id-9", "daemon.lock", "daemon.log", "daemon.log.1", "daemon.log.3", "daemon.stderr.log"}
			writeData(t, svc, append(daemonFiles, tt.extra...)...)
			if err := svc.Uninstall(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			want := []string{
				"systemctl --user disable --now firekeeper.service",
				"systemctl --user daemon-reload",
			}
			if !slices.Equal(runner.calls, want) {
				t.Fatalf("calls = %q, want %q", runner.calls, want)
			}
			entries, err := os.ReadDir(svc.DataDir())
			if tt.dirGone {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("data dir still there: %v %v", entries, err)
				}
				return
			}
			var left []string
			for _, e := range entries {
				left = append(left, e.Name())
			}
			if !slices.Equal(left, tt.extra) {
				t.Fatalf("left = %v, want %v", left, tt.extra)
			}
		})
	}
}

func TestUninstallNotInstalled(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			svc, runner, _ := testService(t, goos)
			runner.reply("launchctl print gui/501/dev.firekeeper.daemon", notLoaded)
			writeData(t, svc, "state.json")
			if err := svc.Uninstall(context.Background(), false); !errors.Is(err, ErrNotInstalled) {
				t.Fatalf("err = %v", err)
			}
			for _, call := range runner.calls {
				if !strings.HasPrefix(call, "launchctl print") {
					t.Fatalf("ran %q with nothing installed", call)
				}
			}
			if _, err := os.Stat(filepath.Join(svc.DataDir(), "state.json")); err != nil {
				t.Fatal("data removed without --purge")
			}
			// --purge still purges.
			if err := svc.Uninstall(context.Background(), true); !errors.Is(err, ErrNotInstalled) {
				t.Fatalf("purge err = %v", err)
			}
			if _, err := os.Stat(svc.DataDir()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("data dir not purged: %v", err)
			}
		})
	}
}

// A LaunchAgent that is loaded but whose file is gone is still stopped.
func TestUninstallLaunchdLoadedWithoutFile(t *testing.T) {
	svc, runner, _ := testService(t, "darwin")
	if err := svc.Uninstall(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 || !strings.Contains(runner.calls[1], "bootout") {
		t.Fatalf("calls = %q", runner.calls)
	}
}

const launchctlPrint = `gui/501/dev.firekeeper.daemon = {
	active count = 1
	path = /Users/dev/Library/LaunchAgents/dev.firekeeper.daemon.plist
	type = LaunchAgent
	state = running

	program = /usr/local/bin/firekeeper
	arguments = {
		/usr/local/bin/firekeeper
		daemon
	}

	pid = 4242
	immediate reason = speculative
	runs = 1
	last exit code = (never exited)

	endpoints = {
		state = active
		pid = 1
	}
}
`

func TestStatusLaunchd(t *testing.T) {
	svc, runner, _ := testService(t, "darwin")
	installFile(t, svc)
	runner.reply("launchctl print gui/501/dev.firekeeper.daemon", reply{out: launchctlPrint})
	st, err := svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Status{Installed: true, Loaded: true, Running: true, State: "running", PID: 4242, LastExit: "(never exited)"}
	if st != want {
		t.Fatalf("status = %+v, want %+v", st, want)
	}

	runner.reply("launchctl print gui/501/dev.firekeeper.daemon", notLoaded)
	st, err = svc.Status(context.Background())
	if err != nil || st != (Status{Installed: true}) {
		t.Fatalf("not loaded status = %+v, %v", st, err)
	}

	runner.reply("launchctl print gui/501/dev.firekeeper.daemon", reply{err: errors.New("run launchctl: executable file not found")})
	if _, err := svc.Status(context.Background()); err == nil {
		t.Fatal("expected error when launchctl cannot run")
	}
}

func TestStatusSystemd(t *testing.T) {
	tests := []struct {
		out  string
		want Status
	}{
		{
			"LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=99\nExecMainStatus=0\n",
			Status{Loaded: true, Running: true, State: "active (running)", PID: 99, LastExit: "0"},
		},
		{
			"LoadState=loaded\nActiveState=activating\nSubState=auto-restart\nMainPID=0\nExecMainStatus=1\n",
			Status{Loaded: true, State: "activating (auto-restart)", LastExit: "1"},
		},
		{
			"LoadState=not-found\nActiveState=inactive\nSubState=dead\nMainPID=0\nExecMainStatus=0\n",
			Status{State: "inactive (dead)", LastExit: "0"},
		},
		{"garbage", Status{}},
	}
	for _, tt := range tests {
		svc, runner, _ := testService(t, "linux")
		runner.reply("systemctl --user show firekeeper.service --property=LoadState,ActiveState,SubState,MainPID,ExecMainStatus", reply{out: tt.out})
		st, err := svc.Status(context.Background())
		if err != nil || st != tt.want {
			t.Fatalf("status(%q) = %+v, %v, want %+v", tt.out, st, err, tt.want)
		}
	}
}

func TestCommandErrorMessage(t *testing.T) {
	err := &CommandError{Name: "systemctl", Code: 4, Output: "\n  Unit not found.\nsecond line\n"}
	if got := err.Error(); got != "systemctl exited with status 4: Unit not found." {
		t.Fatalf("Error() = %q", got)
	}
	if got := (&CommandError{Name: "launchctl", Code: 3}).Error(); got != "launchctl exited with status 3" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestNewServiceCapturesProviderHomes(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/fixture/claude")
	t.Setenv("KIMI_CODE_HOME", "")
	svc, err := NewService([]string{"--provider", "claude"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if svc.Env["CLAUDE_CONFIG_DIR"] != "/fixture/claude" {
		t.Fatalf("CLAUDE_CONFIG_DIR not captured: %v", svc.Env)
	}
	if _, ok := svc.Env["KIMI_CODE_HOME"]; ok {
		t.Fatal("unset variables must not be captured")
	}
}

func TestServiceExecutableKeepsHomebrewSymlink(t *testing.T) {
	links := map[string]string{
		"/opt/homebrew/bin/firekeeper": "/opt/homebrew/Cellar/firekeeper/0.1.0/bin/firekeeper",
		"/Users/dev/.local/bin/fk":     "/Users/dev/src/firekeeper/firekeeper",
	}
	eval := func(p string) (string, error) {
		if r, ok := links[p]; ok {
			return r, nil
		}
		return "", errors.New("no such file")
	}
	tests := map[string]string{
		"/opt/homebrew/bin/firekeeper": "/opt/homebrew/bin/firekeeper",
		"/Users/dev/.local/bin/fk":     "/Users/dev/src/firekeeper/firekeeper",
		"/missing/firekeeper":          "/missing/firekeeper",
	}
	for exe, want := range tests {
		if got := serviceExecutable(exe, eval); got != want {
			t.Errorf("serviceExecutable(%q) = %q, want %q", exe, got, want)
		}
	}
}
