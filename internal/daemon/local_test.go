package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

const systemdShow = "systemctl --user show firekeeper.service --property=LoadState,ActiveState,SubState,MainPID,ExecMainStatus"

func writeServiceFile(t *testing.T, svc *Service) {
	t.Helper()
	content, err := svc.File()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(svc.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(svc.Path(), content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInstalledArgsRoundTrip(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		for _, args := range [][]string{
			{"--quiet", "--server", "http://127.0.0.1:7777", "--provider", "codex"},
			{"--quiet", "--server", `http://h:1/a?b=1&c="%x$y\z`, "--provider", "kimi", "--interval", "1m0s"},
			{"--quiet", "--provider", "two words", "--provider", "line\nbreak"},
		} {
			t.Run(goos, func(t *testing.T) {
				svc, _, _ := testService(t, goos)
				svc.Executable = "/home/dev/My Tools/fire&keeper"
				svc.Args = args
				svc.Env = map[string]string{"COPILOT_HOME": `/home/dev/<co>pilot 100%$HOME`}
				writeServiceFile(t, svc)
				got, err := svc.InstalledArgs()
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(got, args) {
					t.Fatalf("InstalledArgs = %q, want %q", got, args)
				}
			})
		}
	}
}

func TestInstalledArgsMissingAndMalformed(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		svc, _, _ := testService(t, goos)
		if _, err := svc.InstalledArgs(); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: missing file: err = %v, want ErrNotExist", goos, err)
		}
		for _, content := range []string{"", "<plist><dict><key>ProgramArguments</key>", "ExecStart=\"/bin/x\" \"daemon", "ExecStart=/bin/other run"} {
			if err := os.MkdirAll(filepath.Dir(svc.Path()), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(svc.Path(), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if args, err := svc.InstalledArgs(); err == nil {
				t.Fatalf("%s: %q: InstalledArgs = %q, want an error", goos, content, args)
			}
		}
	}
}

func TestFlagValues(t *testing.T) {
	args := []string{"--quiet", "--server", "a", "-server=b", "--server=c", "-provider", "codex", "--", "--server", "d"}
	if got := flagValues(args, "server"); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("server = %q", got)
	}
	if got := flagValues(args, "provider"); !slices.Equal(got, []string{"codex"}) {
		t.Fatalf("provider = %q", got)
	}
	if got := flagValues([]string{"--server"}, "server"); got != nil {
		t.Fatalf("dangling flag = %q", got)
	}
}

func TestInspect(t *testing.T) {
	ctx := context.Background()
	codex := []transcript.Provider{transcript.ProviderCodex}

	t.Run("not installed uses config", func(t *testing.T) {
		svc, _, _ := testService(t, "linux")
		r := svc.Inspect(ctx, "https://dash.example.com", codex)
		if r.Installed || r.Running || r.Server != "https://dash.example.com" || !slices.Equal(r.Providers, codex) {
			t.Fatalf("Inspect = %+v", r)
		}
	})

	t.Run("not installed, no config server", func(t *testing.T) {
		svc, _, _ := testService(t, "linux")
		if r := svc.Inspect(ctx, "", nil); r.Server != reporter.DefaultServer || r.Providers != nil {
			t.Fatalf("Inspect = %+v", r)
		}
	})

	t.Run("installed service flags win", func(t *testing.T) {
		svc, runner, _ := testService(t, "linux")
		svc.Args = []string{"--quiet", "--server", "http://127.0.0.1:8080", "--provider", "claude", "--provider", "copilot"}
		writeServiceFile(t, svc)
		runner.reply(systemdShow, reply{out: "LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=42\n"})
		r := svc.Inspect(ctx, "https://dash.example.com", codex)
		want := []transcript.Provider{transcript.ProviderClaude, transcript.ProviderCopilot}
		if !r.Installed || !r.Running || r.Foreground || r.Server != "http://127.0.0.1:8080" || !slices.Equal(r.Providers, want) {
			t.Fatalf("Inspect = %+v", r)
		}
	})

	t.Run("installed without server flag uses config", func(t *testing.T) {
		svc, _, _ := testService(t, "darwin")
		svc.Args = []string{"--quiet", "--provider", "codex"}
		writeServiceFile(t, svc)
		r := svc.Inspect(ctx, "https://dash.example.com", nil)
		if !r.Installed || r.Running || r.Server != "https://dash.example.com" || !slices.Equal(r.Providers, codex) {
			t.Fatalf("Inspect = %+v", r)
		}
	})

	t.Run("foreground daemon holds the lock", func(t *testing.T) {
		svc, _, _ := testService(t, "linux")
		if err := os.MkdirAll(svc.DataDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		lock := filepath.Join(svc.DataDir(), "daemon.lock")
		if err := os.WriteFile(lock, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if r := svc.Inspect(ctx, "", nil); !r.Running || !r.Foreground || r.Installed {
			t.Fatalf("Inspect = %+v", r)
		}
		for _, stale := range []string{"", "nope\n", "0\n", "-1\n"} {
			if err := os.WriteFile(lock, []byte(stale), 0o600); err != nil {
				t.Fatal(err)
			}
			if r := svc.Inspect(ctx, "", nil); r.Running {
				t.Fatalf("lock %q: Inspect = %+v", stale, r)
			}
		}
	})
}
