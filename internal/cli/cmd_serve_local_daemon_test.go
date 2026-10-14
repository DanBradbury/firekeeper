//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/daemon"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// The offer installs a daemon service, which exists only where
// daemon.Supported reports true; the build constraint matches it.

func TestOfferLocalReporting(t *testing.T) {
	if !daemon.Supported(runtime.GOOS) {
		t.Fatalf("daemon.Supported(%q) is false; update this file's build constraint", runtime.GOOS)
	}
	withConfigFile(t, "")

	t.Run("yes installs for detected providers", func(t *testing.T) {
		gotArgs, _, _ := stubService(t, runtime.GOOS)
		stubDetected(t, transcript.ProviderCodex, transcript.ProviderClaude)
		var out, errb bytes.Buffer
		offerLocalReporting(context.Background(), &localProbe{}, localURL, strings.NewReader("y\n"), &out, &errb)
		want := []string{"--quiet", "--server", "http://127.0.0.1:7777", "--provider", "codex", "--provider", "claude"}
		if !slices.Equal(*gotArgs, want) {
			t.Fatalf("install args = %q, want %q; stderr %q", *gotArgs, want, errb.String())
		}
		if !strings.Contains(out.String(), "Report Codex and Claude Code sessions") || !strings.Contains(out.String(), "now reports") {
			t.Fatalf("stdout = %q", out.String())
		}
	})

	t.Run("no installs nothing", func(t *testing.T) {
		_, runner, _ := stubService(t, runtime.GOOS)
		stubDetected(t, transcript.ProviderCodex)
		var out, errb bytes.Buffer
		offerLocalReporting(context.Background(), &localProbe{}, localURL, strings.NewReader("\n"), &out, &errb)
		for _, c := range runner.calls {
			if !strings.Contains(c, " show ") && !strings.HasPrefix(c, "launchctl print") {
				t.Fatalf("ran %q after a no", c)
			}
		}
		if !strings.Contains(out.String(), "run firekeeper daemon install whenever") {
			t.Fatalf("stdout = %q", out.String())
		}
	})

	t.Run("switching keeps the daemon's providers", func(t *testing.T) {
		gotArgs, runner, _ := stubService(t, runtime.GOOS)
		runner.reply = func(line string) (string, error) {
			if strings.HasPrefix(line, "launchctl print") {
				return "state = running\npid = 42\n", nil
			}
			return "LoadState=loaded\nActiveState=active\nSubState=running\n", nil
		}
		stubDetected(t, transcript.ProviderCodex, transcript.ProviderClaude)
		svc, err := newService([]string{"--quiet", "--server", "https://dash.example.com", "--provider", "copilot"}, &bytes.Buffer{})
		if err != nil {
			t.Fatal(err)
		}
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
		var out, errb bytes.Buffer
		offerLocalReporting(context.Background(), &localProbe{}, localURL, strings.NewReader("yes\n"), &out, &errb)
		if !strings.Contains(out.String(), "reports to https://dash.example.com, not this dashboard") {
			t.Fatalf("stdout = %q", out.String())
		}
		want := []string{"--quiet", "--server", "http://127.0.0.1:7777", "--provider", "copilot"}
		if !slices.Equal(*gotArgs, want) {
			t.Fatalf("install args = %q, want %q", *gotArgs, want)
		}
	})

	t.Run("nothing detected asks nothing", func(t *testing.T) {
		_, _, _ = stubService(t, runtime.GOOS)
		stubDetected(t)
		var out, errb bytes.Buffer
		offerLocalReporting(context.Background(), &localProbe{}, localURL, strings.NewReader("y\n"), &out, &errb)
		if strings.Contains(out.String(), "[y/N]") || !strings.Contains(out.String(), "nothing to report") {
			t.Fatalf("stdout = %q", out.String())
		}
	})
}
