package cli

import (
	"bytes"
	"context"
	"runtime"
	"slices"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/daemon"
	"github.com/DanBradbury/firekeeper/internal/server"
	"github.com/DanBradbury/firekeeper/internal/server/api"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

const localURL = "http://127.0.0.1:7777/"

func TestSameServer(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"http://127.0.0.1:7777", "http://127.0.0.1:7777/", true},
		{"http://localhost:7777", "http://127.0.0.1:7777/", true},
		{"http://[::1]:7777", "http://localhost:7777", true},
		{"http://127.0.0.1:7778", "http://127.0.0.1:7777", false},
		{"https://127.0.0.1:7777", "http://127.0.0.1:7777", false},
		{"https://dash.example.com", "https://dash.example.com:443/x", true},
		{"https://dash.example.com", "http://127.0.0.1:7777", false},
		{"http://10.0.0.2:7777", "http://127.0.0.1:7777", false},
		{"", "http://127.0.0.1:7777", false},
		{"not a url", "http://127.0.0.1:7777", false},
	} {
		if got := sameServer(tc.a, tc.b); got != tc.want {
			t.Errorf("sameServer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestDisplayServer(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:secret@dash.example.com/path?q=1": "https://dash.example.com",
		"http://127.0.0.1:7777":                         "http://127.0.0.1:7777",
		"nonsense":                                      "",
	} {
		if got := displayServer(in); got != want {
			t.Errorf("displayServer(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildLocalStatus(t *testing.T) {
	codex := []transcript.Provider{transcript.ProviderCodex}
	for _, tc := range []struct {
		name         string
		rep          daemon.Reporting
		configServer string
		url          string
		daemon       string
		here         bool
		command      string
	}{
		{"not installed", daemon.Reporting{Server: "http://127.0.0.1:7777"}, "", localURL,
			api.DaemonNotInstalled, true, "firekeeper daemon install"},
		{"reporting here", daemon.Reporting{Installed: true, Running: true, Server: "http://localhost:7777", Providers: codex}, "", localURL,
			api.DaemonRunning, true, ""},
		{"reporting elsewhere", daemon.Reporting{Installed: true, Running: true, Server: "https://dash.example.com", Providers: codex}, "https://dash.example.com", localURL,
			api.DaemonRunning, false, "firekeeper daemon install --server http://127.0.0.1:7777"},
		{"stopped", daemon.Reporting{Installed: true, Server: "http://127.0.0.1:7777"}, "", localURL,
			api.DaemonStopped, true, "firekeeper daemon install"},
		{"other port", daemon.Reporting{Server: "http://127.0.0.1:7777"}, "", "http://127.0.0.1:9000/",
			api.DaemonNotInstalled, false, "firekeeper daemon install --server http://127.0.0.1:9000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := buildLocalStatus(tc.rep, codex, tc.configServer, tc.url)
			if st.Daemon != tc.daemon || st.ReportsHere != tc.here || st.Command != tc.command {
				t.Fatalf("status = %+v", st)
			}
			if !slices.Equal(st.Detected, []string{"codex"}) {
				t.Fatalf("detected = %q", st.Detected)
			}
		})
	}
}

func TestHumanList(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{nil, "no providers"},
		{[]string{"codex"}, "Codex"},
		{[]string{"codex", "claude"}, "Codex and Claude Code"},
		{[]string{"codex", "copilot", "kimi"}, "Codex, Copilot CLI and Kimi Code"},
		{[]string{"opencode", "gemini"}, "OpenCode and Gemini CLI"},
		{[]string{"codex", "someday"}, "Codex and someday"},
	} {
		if got := humanList(tc.in); got != tc.want {
			t.Errorf("humanList(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func stubDetected(t *testing.T, ps ...transcript.Provider) {
	t.Helper()
	previous := detectDaemonProviders
	detectDaemonProviders = func() ([]transcript.Provider, error) { return ps, nil }
	t.Cleanup(func() { detectDaemonProviders = previous })
}

func TestServeConfiguresLocalMode(t *testing.T) {
	var got server.Config
	previous := serveRun
	serveRun = func(_ context.Context, cfg server.Config) error { got = cfg; return nil }
	t.Cleanup(func() { serveRun = previous })
	withConfigFile(t, "")
	var out, errb bytes.Buffer
	if code := runServe(nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr %q", code, errb.String())
	}
	if got.LocalMachine == nil || got.LocalMachine.ID == "" || got.LocalMachine.OS != runtime.GOOS {
		t.Fatalf("LocalMachine = %+v", got.LocalMachine)
	}
	if got.Local == nil || got.OnLocal == nil {
		t.Fatal("local hooks not set")
	}
	// Without a terminal, OnLocal asks nothing.
	prevInteractive := serveInteractive
	serveInteractive = func() bool { return false }
	t.Cleanup(func() { serveInteractive = prevInteractive })
	got.OnLocal(localURL)
	if out.Len() != 0 {
		t.Fatalf("stdout = %q", out.String())
	}
}
