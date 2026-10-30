package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/serverinstall"
)

// serverScript is a fake firekeeper-server (see fakeServerBody): it answers
// `version` and `license --json` like the real one and echoes anything else.
func serverScript(t *testing.T, version, licenseJSON string) string {
	t.Helper()
	return fakeServerBody(t, fakeServerSpec{
		Banner:  "firekeeper-server " + version + " (commit abc, built 2026-10-23, linux/amd64)",
		License: licenseJSON,
	})
}

type updateRelease struct {
	srv      *httptest.Server
	requests *int
	paths    *[]string
}

// newUpdateRelease serves one release, tag, whose server is script. When
// latestFails is true the /latest lookup returns an error.
func newUpdateRelease(t *testing.T, tag, script string, latestFails bool) updateRelease {
	t.Helper()
	archive, asset := serverArchive(t, tag, script)
	buf := bytes.NewBuffer(archive)
	sum := sha256.Sum256(buf.Bytes())
	n := 0
	var paths []string
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		n++
		paths = append(paths, r.URL.Path)
		if latestFails {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"tag_name":"` + tag + `"}`))
	})
	mux.HandleFunc("/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		n++
		paths = append(paths, r.URL.Path)
		w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + asset + "\n"))
	})
	mux.HandleFunc("/download/"+tag+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		n++
		paths = append(paths, r.URL.Path)
		w.Write(buf.Bytes())
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return updateRelease{srv: srv, requests: &n, paths: &paths}
}

// setupUpdate is setupInstall with an old server already next to the client.
func setupUpdate(t *testing.T, rel updateRelease, installedVersion, stdin string) (*installSetup, string) {
	t.Helper()
	s := setupInstall(t, rel.srv.URL, stdin)
	server := filepath.Join(s.exeDir, serverBinName+exeSuffix())
	if err := os.WriteFile(server, []byte(serverScript(t, installedVersion, "")), 0o755); err != nil {
		t.Fatal(err)
	}
	return s, server
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpdateAcceptedReplacesTheServerAndRunsIt(t *testing.T) {
	rel := newUpdateRelease(t, "v0.5.0", serverScript(t, "0.5.0", ""), false)
	s, server := setupUpdate(t, rel, "0.4.0", "1\n")
	var stderr bytes.Buffer
	if code := runServe([]string{"--listen", "127.0.0.1:0"}, nil, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	out := stderr.String()
	for _, want := range []string{"firekeeper-server update available: 0.4.0 -> 0.5.0", "1) Update now", "3) Skip this version", "Updated firekeeper-server 0.4.0 -> 0.5.0."} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(readFile(t, server), "firekeeper-server 0.5.0") {
		t.Error("the server file was not replaced")
	}
	if *s.hPath != server {
		t.Errorf("ran %q, want %q", *s.hPath, server)
	}
	if _, err := os.Stat(filepath.Join(s.exeDir, ".firekeeper-server.previous")); err == nil {
		t.Error("the backup was left behind")
	}
}

func TestUpdateDeclinedChangesNothing(t *testing.T) {
	for _, answer := range []string{"2\n", "\n", "no\n", ""} {
		rel := newUpdateRelease(t, "v0.5.0", serverScript(t, "0.5.0", ""), false)
		s, server := setupUpdate(t, rel, "0.4.0", answer)
		var stderr bytes.Buffer
		if code := runServe([]string{"--listen", "127.0.0.1:0"}, nil, &stderr); code != 0 {
			t.Fatalf("%q: exit %d\n%s", answer, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "update available: 0.4.0 -> 0.5.0") {
			t.Errorf("%q: no offer:\n%s", answer, stderr.String())
		}
		if !strings.Contains(readFile(t, server), "firekeeper-server 0.4.0") || *rel.requests != 1 {
			t.Errorf("%q: replaced or downloaded (requests %d)", answer, *rel.requests)
		}
		if *s.hPath != server {
			t.Errorf("%q: ran %q", answer, *s.hPath)
		}
	}
}

func TestUpdateSkipIsRememberedUntilANewerRelease(t *testing.T) {
	rel := newUpdateRelease(t, "v0.5.0", serverScript(t, "0.5.0", ""), false)
	s, _ := setupUpdate(t, rel, "0.4.0", "3\n")
	var stderr bytes.Buffer
	runServe([]string{"--listen", "127.0.0.1:0"}, nil, &stderr)
	if !strings.Contains(stderr.String(), "update available") {
		t.Fatalf("no offer:\n%s", stderr.String())
	}
	// Second run: same release, skipped, so no prompt.
	installOffer.stdin = strings.NewReader("1\n")
	stderr.Reset()
	runServe([]string{"--listen", "127.0.0.1:0"}, nil, &stderr)
	if strings.Contains(stderr.String(), "update available") {
		t.Errorf("offered a skipped version:\n%s", stderr.String())
	}
	if st := loadServerUpdateState(s.home); st.Skipped != "v0.5.0" || st.Latest != "v0.5.0" {
		t.Errorf("state %+v", st)
	}
	// A newer release is offered again.
	newer := newUpdateRelease(t, "v0.6.0", serverScript(t, "0.6.0", ""), false)
	s.env[serverinstall.EnvURL] = newer.srv.URL
	installOffer.stdin = strings.NewReader("2\n")
	stderr.Reset()
	runServe([]string{"--listen", "127.0.0.1:0"}, nil, &stderr)
	if !strings.Contains(stderr.String(), "0.4.0 -> 0.6.0") {
		t.Errorf("newer release not offered:\n%s", stderr.String())
	}
}

func TestUpdateNotOfferedWhenCurrent(t *testing.T) {
	for _, installed := range []string{"0.5.0", "0.6.0", "dev"} {
		rel := newUpdateRelease(t, "v0.5.0", serverScript(t, "0.5.0", ""), false)
		_, _ = setupUpdate(t, rel, installed, "1\n")
		var stderr bytes.Buffer
		runServe([]string{"--listen", "127.0.0.1:0"}, nil, &stderr)
		if strings.Contains(stderr.String(), "update available") {
			t.Errorf("installed %s was offered an update:\n%s", installed, stderr.String())
		}
	}
}

func TestUpdateOffSwitches(t *testing.T) {
	for name, tc := range map[string]struct {
		env map[string]string
		tty bool
	}{
		"CI":                   {map[string]string{"CI": "1"}, true},
		"no prompt":            {map[string]string{NoPromptEnv: "1"}, true},
		"no update check":      {map[string]string{noUpdateCheckEnv: "1"}, true},
		"explicit server path": {map[string]string{ServerBinEnv: "SERVER"}, true},
		"not a terminal":       {nil, false},
	} {
		rel := newUpdateRelease(t, "v0.5.0", serverScript(t, "0.5.0", ""), false)
		s, server := setupUpdate(t, rel, "0.4.0", "1\n")
		s.tty = tc.tty
		for k, v := range tc.env {
			if v == "SERVER" {
				v = server
			}
			s.env[k] = v
		}
		var stderr bytes.Buffer
		runServe([]string{"--listen", "127.0.0.1:0"}, nil, &stderr)
		if *rel.requests != 0 || strings.Contains(stderr.String(), "update available") {
			t.Errorf("%s: contacted the release server (%d) or prompted:\n%s", name, *rel.requests, stderr.String())
		}
	}
}

func TestUpdateOffWhenNoReleaseSourceIsConfigured(t *testing.T) {
	rel := newUpdateRelease(t, "v0.5.0", serverScript(t, "0.5.0", ""), false)
	s, _ := setupUpdate(t, rel, "0.4.0", "1\n")
	delete(s.env, serverinstall.EnvURL)
	var stderr bytes.Buffer
	runServe([]string{"--listen", "127.0.0.1:0"}, nil, &stderr)
	if *rel.requests != 0 || strings.Contains(stderr.String(), "update available") {
		t.Errorf("an unconfigured build looked for updates:\n%s", stderr.String())
	}
}

func TestUpdateSkippedForOperatorCommandsAndHelp(t *testing.T) {
	for _, args := range [][]string{{"token", "list"}, {"account", "list"}, {"admin", "x"}, {"invite"}, {"prices"}, {"--help"}, {"-h"}} {
		rel := newUpdateRelease(t, "v0.5.0", serverScript(t, "0.5.0", ""), false)
		_, _ = setupUpdate(t, rel, "0.4.0", "1\n")
		var stderr bytes.Buffer
		runServe(args, nil, &stderr)
		if *rel.requests != 0 {
			t.Errorf("%v contacted the release server", args)
		}
	}
}

func TestUpdateLookupFailureContinuesQuietly(t *testing.T) {
	rel := newUpdateRelease(t, "v0.5.0", serverScript(t, "0.5.0", ""), true)
	s, server := setupUpdate(t, rel, "0.4.0", "1\n")
	var stderr bytes.Buffer
	if code := runServe([]string{"--listen", "127.0.0.1:0"}, nil, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	if stderr.Len() != 0 || *s.hPath != server {
		t.Errorf("stderr %q, ran %q", stderr.String(), *s.hPath)
	}
}

func TestUpdateChecksumMismatchKeepsTheOldServer(t *testing.T) {
	rel := newUpdateRelease(t, "v0.5.0", serverScript(t, "0.5.0", ""), false)
	// Serve a corrupted archive under the right name.
	good := rel.srv.Config.Handler
	rel.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".tar.gz") {
			w.Write([]byte("not the archive"))
			return
		}
		good.ServeHTTP(w, r)
	})
	s, server := setupUpdate(t, rel, "0.4.0", "1\n")
	var stderr bytes.Buffer
	if code := runServe([]string{"--listen", "127.0.0.1:0"}, nil, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "update failed") || !strings.Contains(stderr.String(), "Continuing with the current version") {
		t.Errorf("message:\n%s", stderr.String())
	}
	if !strings.Contains(readFile(t, server), "firekeeper-server 0.4.0") || *s.hPath != server {
		t.Error("the old server was not kept and run")
	}
}

// A build newer than the license's update period would refuse to start, so
// the update is undone.
func TestUpdateNotCoveredByTheLicenseIsRolledBack(t *testing.T) {
	notCovered := `{"status":"not covered by this build","updates_until":"2026-12-31"}`
	rel := newUpdateRelease(t, "v0.5.0", serverScript(t, "0.5.0", notCovered), false)
	s, server := setupUpdate(t, rel, "0.4.0", "1\n")
	var stderr bytes.Buffer
	if code := runServe([]string{"--listen", "127.0.0.1:0"}, nil, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	out := stderr.String()
	if !strings.Contains(out, "not covered by your license") || !strings.Contains(out, "2026-12-31") || !strings.Contains(out, "Kept version 0.4.0") {
		t.Errorf("message:\n%s", out)
	}
	if !strings.Contains(readFile(t, server), "firekeeper-server 0.4.0") || *s.hPath != server {
		t.Error("the old server was not restored and run")
	}
	if _, err := os.Stat(filepath.Join(s.exeDir, ".firekeeper-server.previous")); err == nil {
		t.Error("the backup was left behind")
	}
	info, err := os.Stat(server)
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("restored file is not executable: %v %v", info, err)
	}
}

func TestUpdateCoveredOrKeylessIsKept(t *testing.T) {
	for name, licenseJSON := range map[string]string{
		"valid":   `{"status":"valid","updates_until":"2027-12-31"}`,
		"no key":  ``,
		"garbage": `not json`,
	} {
		rel := newUpdateRelease(t, "v0.5.0", serverScript(t, "0.5.0", licenseJSON), false)
		_, server := setupUpdate(t, rel, "0.4.0", "1\n")
		var stderr bytes.Buffer
		runServe([]string{"--listen", "127.0.0.1:0"}, nil, &stderr)
		if !strings.Contains(readFile(t, server), "firekeeper-server 0.5.0") {
			t.Errorf("%s: update was undone:\n%s", name, stderr.String())
		}
	}
}

func TestUpdateReadOnlyDirectoryIsLeftAlone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	if runtime.GOOS == "windows" {
		// Windows ignores the read-only attribute on directories, so chmod
		// cannot make one unwritable; that needs an ACL change.
		t.Skip("chmod cannot make a directory unwritable on Windows")
	}
	rel := newUpdateRelease(t, "v0.5.0", serverScript(t, "0.5.0", ""), false)
	s, server := setupUpdate(t, rel, "0.4.0", "1\n")
	if err := os.Chmod(s.exeDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(s.exeDir, 0o755) })
	var stderr bytes.Buffer
	runServe([]string{"--listen", "127.0.0.1:0"}, nil, &stderr)
	if !strings.Contains(stderr.String(), "not writable") || !strings.Contains(readFile(t, server), "firekeeper-server 0.4.0") {
		t.Errorf("message:\n%s", stderr.String())
	}
}

func TestUpdateHomebrewUsesBrewUpgrade(t *testing.T) {
	rel := newUpdateRelease(t, "v0.5.0", serverScript(t, "0.5.0", ""), false)
	s := setupInstall(t, rel.srv.URL, "1\n")
	cellar := filepath.Join(t.TempDir(), "Cellar", "firekeeper-server", "0.4.0", "bin")
	os.MkdirAll(cellar, 0o755)
	server := filepath.Join(cellar, serverBinName+exeSuffix())
	os.WriteFile(server, []byte(serverScript(t, "0.4.0", "")), 0o755)
	brewDir := t.TempDir()
	brew := fakeBinary(t, brewDir, "brew")
	defaultLookup.lookPath = func(n string) (string, error) {
		switch n {
		case "brew":
			return brew, nil
		case serverBinName:
			return server, nil
		}
		return "", exec.ErrNotFound
	}
	var verbs []string
	installOffer.runBrew = func(path, verb string, _ io.Writer) error {
		verbs = append(verbs, verb)
		return os.WriteFile(server, []byte(serverScript(t, "0.5.0", "")), 0o755)
	}
	var stderr bytes.Buffer
	if code := runServe([]string{"--listen", "127.0.0.1:0"}, nil, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	if len(verbs) != 1 || verbs[0] != "upgrade" {
		t.Errorf("brew verbs %v", verbs)
	}
	if *rel.requests != 1 {
		t.Errorf("downloaded directly despite Homebrew (%d requests: %v)", *rel.requests, *rel.paths)
	}
	if *s.hPath != server || !strings.Contains(stderr.String(), "Updated firekeeper-server.") {
		t.Errorf("ran %q\n%s", *s.hPath, stderr.String())
	}
}

func TestStartsDashboard(t *testing.T) {
	for args, want := range map[string]bool{
		"": true, "--listen 127.0.0.1:1": true, "token list": false, "account": false,
		"admin x": false, "invite": false, "prices": false, "--help": false, "--listen x -h": false,
	} {
		if got := startsDashboard(strings.Fields(args)); got != want {
			t.Errorf("startsDashboard(%q) = %v, want %v", args, got, want)
		}
	}
}
