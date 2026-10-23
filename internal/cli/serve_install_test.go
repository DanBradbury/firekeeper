package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/serverinstall"
)

type fakeRelease struct {
	srv      *httptest.Server
	requests *int
}

// newFakeRelease serves one release whose server is a shell script that
// echoes its arguments. It counts every request.
func newFakeRelease(t *testing.T, sumOverride string) fakeRelease {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "#!/bin/sh\necho \"fake-server: $*\"\nexit 0\n"
	for _, e := range [][2]string{{"LICENSE", "lic"}, {"firekeeper-server", body}} {
		tw.WriteHeader(&tar.Header{Name: e[0], Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(e[1]))})
		tw.Write([]byte(e[1]))
	}
	tw.Close()
	gz.Close()
	sum := sha256.Sum256(buf.Bytes())
	asset := "firekeeper-server_1.0.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	sums := hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	if sumOverride != "" {
		sums = sumOverride
	}
	n := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) { n++; w.Write([]byte(`{"tag_name":"v1.0.0"}`)) })
	mux.HandleFunc("/download/v1.0.0/checksums.txt", func(w http.ResponseWriter, _ *http.Request) { n++; w.Write([]byte(sums)) })
	mux.HandleFunc("/download/v1.0.0/"+asset, func(w http.ResponseWriter, _ *http.Request) { n++; w.Write(buf.Bytes()) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return fakeRelease{srv: srv, requests: &n}
}

// installSetup isolates everything an offer reads. exeDir is where the fake
// firekeeper executable "lives".
// resolvedTempDir is t.TempDir() with symlinks resolved. offerInstall resolves
// the executable's path, and on macOS the temp dir sits under /var, a symlink
// to /private/var, so paths built from the unresolved name would never match.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

type installSetup struct {
	env    map[string]string
	exeDir string
	home   string
	stdin  string
	tty    bool
	handed *[]string
	hPath  *string
}

func setupInstall(t *testing.T, url, stdin string) *installSetup {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("installer supports darwin and linux")
	}
	s := &installSetup{
		env:    map[string]string{"PATH": "/usr/bin"},
		exeDir: resolvedTempDir(t),
		home:   t.TempDir(),
		stdin:  stdin,
		tty:    true,
	}
	if url != "" {
		s.env[serverinstall.EnvURL] = url
	}
	oldLookup, oldStart, oldOffer, oldURL := defaultLookup, startServer, installOffer, serverinstall.ReleasesURL
	t.Cleanup(func() {
		defaultLookup, startServer, installOffer, serverinstall.ReleasesURL = oldLookup, oldStart, oldOffer, oldURL
	})
	serverinstall.ReleasesURL = ""
	exe := filepath.Join(s.exeDir, "firekeeper")
	os.WriteFile(exe, []byte("x"), 0o755)
	defaultLookup = serverLookup{
		getenv:     func(k string) string { return s.env[k] },
		executable: func() (string, error) { return exe, nil },
		lookPath:   func(string) (string, error) { return "", exec.ErrNotFound },
	}
	installOffer = installEnv{
		interactive: func() bool { return s.tty },
		stdin:       strings.NewReader(stdin),
		userHome:    func() (string, error) { return s.home, nil },
		newInstaller: func(src serverinstall.Source, log io.Writer) *serverinstall.Installer {
			return &serverinstall.Installer{Source: src, Log: log}
		},
		runBrew: func(string, string, io.Writer) error { t.Fatal("brew run unexpectedly"); return nil },
	}
	var argv []string
	var path string
	s.handed, s.hPath = &argv, &path
	startServer = func(p string, a, _ []string, _ io.Writer) int {
		path, argv = p, a
		// Really run the installed script so its output is checked too.
		out, err := exec.Command(p, a[1:]...).CombinedOutput()
		if err != nil {
			return 9
		}
		if !strings.HasPrefix(string(out), "fake-server: ") {
			return 8
		}
		return 0
	}
	return s
}

func assertMissingMessage(t *testing.T, stderr string) {
	t.Helper()
	if !strings.Contains(stderr, "separate commercial binary") {
		t.Errorf("not the usual instructions:\n%s", stderr)
	}
}

func TestInstallDisabledByDefault(t *testing.T) {
	s := setupInstall(t, "", "y\n")
	installOffer.interactive = func() bool { t.Fatal("asked about the terminal while disabled"); return true }
	installOffer.newInstaller = func(serverinstall.Source, io.Writer) *serverinstall.Installer {
		t.Fatal("installer built while disabled")
		return nil
	}
	var stderr bytes.Buffer
	if code := runServe(nil, nil, &stderr); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(stderr.String(), "[y/N]") {
		t.Error("prompted while disabled")
	}
	assertMissingMessage(t, stderr.String())
	_ = s
}

func TestInstallNoPromptCases(t *testing.T) {
	for name, mod := range map[string]func(*installSetup){
		"not a tty":            func(s *installSetup) { s.tty = false },
		"CI":                   func(s *installSetup) { s.env["CI"] = "true" },
		"FIREKEEPER_NO_PROMPT": func(s *installSetup) { s.env[NoPromptEnv] = "1" },
	} {
		t.Run(name, func(t *testing.T) {
			rel := newFakeRelease(t, "")
			s := setupInstall(t, rel.srv.URL, "y\n")
			mod(s)
			var stderr bytes.Buffer
			if code := runServe(nil, nil, &stderr); code != 2 {
				t.Fatalf("exit %d", code)
			}
			if *rel.requests != 0 || strings.Contains(stderr.String(), "[y/N]") {
				t.Errorf("network or prompt used: %d requests\n%s", *rel.requests, stderr.String())
			}
			assertMissingMessage(t, stderr.String())
		})
	}
}

func TestInstallDeclined(t *testing.T) {
	for _, answer := range []string{"n\n", "\n", "", "no\n", "yep\n", "maybe\n"} {
		rel := newFakeRelease(t, "")
		s := setupInstall(t, rel.srv.URL, answer)
		var stderr bytes.Buffer
		if code := runServe(nil, nil, &stderr); code != 2 {
			t.Fatalf("%q: exit %d", answer, code)
		}
		if !strings.Contains(stderr.String(), "[y/N]") {
			t.Errorf("%q: no prompt", answer)
		}
		if *rel.requests != 0 {
			t.Errorf("%q: network used", answer)
		}
		if _, err := os.Stat(filepath.Join(s.exeDir, serverBinName)); err == nil {
			t.Errorf("%q: installed", answer)
		}
		assertMissingMessage(t, stderr.String())
	}
}

func TestInstallYesHandsOff(t *testing.T) {
	for _, answer := range []string{"y\n", "YES\n", " yes \n"} {
		rel := newFakeRelease(t, "")
		s := setupInstall(t, rel.srv.URL, answer)
		var stderr bytes.Buffer
		code := runServe([]string{"--listen", "127.0.0.1:0"}, nil, &stderr)
		if code != 0 {
			t.Fatalf("%q: exit %d\n%s", answer, code, stderr.String())
		}
		want := filepath.Join(s.exeDir, serverBinName)
		if *s.hPath != want {
			t.Errorf("handoff path %q, want %q", *s.hPath, want)
		}
		if !slices.Equal(*s.handed, []string{"firekeeper-server", "serve", "--listen", "127.0.0.1:0"}) {
			t.Errorf("argv %q", *s.handed)
		}
		wantPrompt := "firekeeper-server is not installed. Download it from " + strings.TrimPrefix(rel.srv.URL, "http://") + " and install it to " + s.exeDir + "? [y/N]"
		if !strings.Contains(stderr.String(), wantPrompt) {
			t.Errorf("prompt missing:\n%s", stderr.String())
		}
		if strings.Contains(stderr.String(), "not on your PATH") {
			t.Error("PATH hint printed for the exe dir")
		}
	}
}

func TestInstallChecksumMismatchAborts(t *testing.T) {
	rel := newFakeRelease(t, strings.Repeat("0", 64)+"  firekeeper-server_1.0.0_"+runtime.GOOS+"_"+runtime.GOARCH+".tar.gz\n")
	s := setupInstall(t, rel.srv.URL, "y\n")
	var stderr bytes.Buffer
	if code := runServe(nil, nil, &stderr); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "checksum mismatch") {
		t.Errorf("no mismatch message:\n%s", stderr.String())
	}
	got, _ := os.ReadDir(s.exeDir)
	if len(got) != 1 || got[0].Name() != "firekeeper" {
		t.Errorf("exe dir has %v", got)
	}
	if *s.hPath != "" {
		t.Error("handed off after a failed install")
	}
}

func TestInstallMissingChecksumAborts(t *testing.T) {
	rel := newFakeRelease(t, strings.Repeat("0", 64)+"  unrelated.tar.gz\n")
	s := setupInstall(t, rel.srv.URL, "y\n")
	var stderr bytes.Buffer
	if code := runServe(nil, nil, &stderr); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if got, _ := os.ReadDir(s.exeDir); len(got) != 1 {
		t.Errorf("exe dir has %v", got)
	}
}

func TestInstallFallsBackToHomeLocalBin(t *testing.T) {
	rel := newFakeRelease(t, "")
	s := setupInstall(t, rel.srv.URL, "y\n")
	// A directory that does not exist is unwritable even for root.
	gone := filepath.Join(s.exeDir, "gone", "firekeeper")
	defaultLookup.executable = func() (string, error) { return gone, nil }
	var stderr bytes.Buffer
	if code := runServe(nil, nil, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	want := filepath.Join(s.home, ".local", "bin")
	if *s.hPath != filepath.Join(want, serverBinName) {
		t.Errorf("installed at %q, want under %q", *s.hPath, want)
	}
	if !strings.Contains(stderr.String(), "install it to "+want+"?") || !strings.Contains(stderr.String(), "not on your PATH") {
		t.Errorf("missing prompt dir or PATH hint:\n%s", stderr.String())
	}
}

func TestInstallFallbackNoHintWhenOnPath(t *testing.T) {
	rel := newFakeRelease(t, "")
	s := setupInstall(t, rel.srv.URL, "y\n")
	// Make the exe dir a Homebrew-looking one without brew present.
	cellar := filepath.Join(t.TempDir(), "homebrew", "bin")
	os.MkdirAll(cellar, 0o755)
	exe := filepath.Join(cellar, "firekeeper")
	os.WriteFile(exe, []byte("x"), 0o755)
	defaultLookup.executable = func() (string, error) { return exe, nil }
	bin := filepath.Join(s.home, ".local", "bin")
	s.env["PATH"] = "/usr/bin:" + bin
	var stderr bytes.Buffer
	if code := runServe(nil, nil, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(cellar, serverBinName)); err == nil {
		t.Error("wrote into the Homebrew directory")
	}
	if *s.hPath != filepath.Join(bin, serverBinName) || strings.Contains(stderr.String(), "not on your PATH") {
		t.Errorf("path %q\n%s", *s.hPath, stderr.String())
	}
}

func TestInstallHomebrewBranch(t *testing.T) {
	rel := newFakeRelease(t, "")
	s := setupInstall(t, rel.srv.URL, "y\n")
	brewDir := t.TempDir()
	cellar := filepath.Join(t.TempDir(), "Cellar", "firekeeper", "1.0", "bin")
	os.MkdirAll(cellar, 0o755)
	exe := filepath.Join(cellar, "firekeeper")
	os.WriteFile(exe, []byte("x"), 0o755)
	brew := fakeBinary(t, brewDir, "brew")
	defaultLookup.executable = func() (string, error) { return exe, nil }
	defaultLookup.lookPath = func(n string) (string, error) {
		if n == "brew" {
			return brew, nil
		}
		return "", exec.ErrNotFound
	}
	var ran []string
	installOffer.runBrew = func(path, _ string, _ io.Writer) error {
		ran = append(ran, path)
		// brew "installs" the server into the PATH.
		fakeBinary(t, brewDir, serverBinName)
		defaultLookup.lookPath = func(n string) (string, error) {
			return filepath.Join(brewDir, n), nil
		}
		return nil
	}
	var stderr bytes.Buffer
	if code := runServe([]string{"x"}, nil, &stderr); code == 2 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	if len(ran) != 1 || ran[0] != brew {
		t.Errorf("brew runs: %v", ran)
	}
	if !strings.Contains(stderr.String(), "brew install DanBradbury/tap/firekeeper-server") {
		t.Errorf("no brew suggestion:\n%s", stderr.String())
	}
	if *rel.requests != 0 {
		t.Error("downloaded despite the brew branch")
	}
	if !strings.HasSuffix(*s.hPath, serverBinName) {
		t.Errorf("handoff %q", *s.hPath)
	}
}

func TestInstallHomebrewDeclined(t *testing.T) {
	rel := newFakeRelease(t, "")
	setupInstall(t, rel.srv.URL, "n\n")
	cellar := filepath.Join(t.TempDir(), "Cellar", "firekeeper", "1.0", "bin")
	os.MkdirAll(cellar, 0o755)
	exe := filepath.Join(cellar, "firekeeper")
	os.WriteFile(exe, []byte("x"), 0o755)
	defaultLookup.executable = func() (string, error) { return exe, nil }
	defaultLookup.lookPath = func(n string) (string, error) { return "/fake/brew", nil }
	var stderr bytes.Buffer
	if code := runServe(nil, nil, &stderr); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "brew install DanBradbury/tap/firekeeper-server") || *rel.requests != 0 {
		t.Errorf("%s", stderr.String())
	}
}

func TestInstallUnsupportedPlatformMessage(t *testing.T) {
	rel := newFakeRelease(t, "")
	setupInstall(t, rel.srv.URL, "y\n")
	installOffer.newInstaller = func(src serverinstall.Source, log io.Writer) *serverinstall.Installer {
		return &serverinstall.Installer{Source: src, Log: log, GOOS: "windows", GOARCH: "amd64"}
	}
	var stderr bytes.Buffer
	if code := runServe(nil, nil, &stderr); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "no firekeeper-server build for windows/amd64") {
		t.Errorf("%s", stderr.String())
	}
}

func TestInstallBadOverrideURL(t *testing.T) {
	setupInstall(t, "http://example.com/x", "y\n")
	var stderr bytes.Buffer
	if code := runServe(nil, nil, &stderr); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "must use https") || strings.Contains(stderr.String(), "[y/N]") {
		t.Errorf("%s", stderr.String())
	}
}
