package serverinstall

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeScript is the body of the fake server in test archives. Install runs
// it with `version`, so it must be runnable on the host: a shell script on
// Unix, and on Windows a copy of this test binary, which TestMain turns into
// a fake server when it runs under an installer temp-file name.
var fakeScript = "#!/bin/sh\necho \"$@\"\nexit 0\n"

func TestMain(m *testing.M) {
	if strings.HasPrefix(filepath.Base(os.Args[0]), ".firekeeper-server-") {
		fmt.Println("firekeeper-server 1.0.0 (fake)")
		os.Exit(0)
	}
	if runtime.GOOS == "windows" {
		exe, err := os.Executable()
		if err == nil {
			var b []byte
			if b, err = os.ReadFile(exe); err == nil {
				fakeScript = string(b)
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "fake server:", err)
			os.Exit(2)
		}
	}
	os.Exit(m.Run())
}

type entry struct {
	name, body string
	typeflag   byte
	link       string
	size       int64 // header size override when > 0
}

func makeArchive(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		tf := e.typeflag
		if tf == 0 {
			tf = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Typeflag: tf, Mode: 0o755, Linkname: e.link}
		if tf == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if tf == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// makeZip builds a Windows release archive. typeflag tar.TypeSymlink and
// tar.TypeDir map to the zip mode bits for those.
func makeZip(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		switch e.typeflag {
		case tar.TypeSymlink:
			hdr.SetMode(fs.ModeSymlink | 0o777)
		case tar.TypeDir:
			hdr.SetMode(fs.ModeDir | 0o755)
		default:
			hdr.SetMode(0o755)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		body := e.body
		if e.typeflag == tar.TypeSymlink {
			body = e.link
		}
		if e.typeflag != tar.TypeDir {
			w.Write([]byte(body))
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func goodZip(t *testing.T) []byte {
	return makeZip(t, entry{name: "LICENSE", body: "license"}, entry{name: "firekeeper-server.exe", body: fakeScript})
}

func goodArchive(t *testing.T) []byte {
	return makeArchive(t, entry{name: "LICENSE", body: "license"}, entry{name: "firekeeper-server", body: fakeScript})
}

type release struct {
	tag      string
	archive  []byte
	checksum string // checksums.txt body; "" = computed from archive
	noSums   bool
	goos     string // "" = linux
	goarch   string // "" = amd64
}

func serve(t *testing.T, r release) *Installer {
	t.Helper()
	if r.goos == "" {
		r.goos = "linux"
	}
	if r.goarch == "" {
		r.goarch = "amd64"
	}
	osName, arch, err := Platform(r.goos, r.goarch)
	if err != nil {
		t.Fatal(err)
	}
	ext := ".tar.gz"
	if r.goos == "windows" {
		ext = ".zip"
	}
	asset := "firekeeper-server_" + strings.TrimPrefix(r.tag, "v") + "_" + osName + "_" + arch + ext
	sum := sha256.Sum256(r.archive)
	sums := r.checksum
	if sums == "" {
		sums = hex.EncodeToString(sum[:]) + "  " + asset + "\n" + strings.Repeat("0", 64) + "  other.tar.gz\n"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rel/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"tag_name":"` + r.tag + `"}`))
	})
	mux.HandleFunc("/rel/download/"+r.tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		if r.noSums {
			http.NotFound(w, nil)
			return
		}
		w.Write([]byte(sums))
	})
	mux.HandleFunc("/rel/download/"+r.tag+"/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		w.Write(r.archive)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return &Installer{Source: Source{Base: srv.URL + "/rel"}, Client: srv.Client(), GOOS: r.goos, GOARCH: r.goarch}
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var n []string
	for _, d := range des {
		n = append(n, d.Name())
	}
	return n
}

func TestInstallOK(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "bin")
	in := serve(t, release{tag: "v1.2.3", archive: goodArchive(t)})
	var log bytes.Buffer
	in.Log = &log
	p, err := in.Install(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(dir, "firekeeper-server") {
		t.Errorf("path %q", p)
	}
	st, err := os.Stat(p)
	if err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o755) {
		t.Fatalf("stat %v %v", st, err)
	}
	if got := entries(t, dir); len(got) != 1 {
		t.Errorf("dir has %v, want only the server (no LICENSE, no temp files)", got)
	}
}

func TestInstallReplacesExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "firekeeper-server"), []byte("old"), 0o755)
	in := serve(t, release{tag: "v1.0.0", archive: goodArchive(t)})
	if _, err := in.Install(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "firekeeper-server"))
	if string(b) != fakeScript {
		t.Errorf("not replaced: %q", b)
	}
}

func TestChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	bad := strings.Repeat("a", 64) + "  firekeeper-server_1.0.0_linux_amd64.tar.gz\n"
	in := serve(t, release{tag: "v1.0.0", archive: goodArchive(t), checksum: bad})
	_, err := in.Install(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("left behind %v", got)
	}
}

func TestMissingChecksumLine(t *testing.T) {
	dir := t.TempDir()
	in := serve(t, release{tag: "v1.0.0", archive: goodArchive(t), checksum: strings.Repeat("a", 64) + "  something-else.tar.gz\n"})
	_, err := in.Install(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "no checksum") {
		t.Fatalf("err = %v", err)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("left behind %v", got)
	}
}

func TestMissingChecksumsFile(t *testing.T) {
	dir := t.TempDir()
	in := serve(t, release{tag: "v1.0.0", archive: goodArchive(t), noSums: true})
	if _, err := in.Install(context.Background(), dir); err == nil {
		t.Fatal("installed without checksums.txt")
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("left behind %v", got)
	}
}

func TestMaliciousArchives(t *testing.T) {
	cases := map[string][]entry{
		"traversal":    {{name: "../firekeeper-server", body: fakeScript}},
		"nested":       {{name: "sub/firekeeper-server", body: fakeScript}},
		"dot prefix":   {{name: "./firekeeper-server", body: fakeScript}},
		"absolute":     {{name: "/tmp/firekeeper-server", body: fakeScript}},
		"symlink":      {{name: "firekeeper-server", typeflag: tar.TypeSymlink, link: "/bin/sh"}},
		"hardlink":     {{name: "firekeeper-server", typeflag: tar.TypeLink, link: "LICENSE"}},
		"extra entry":  {{name: "firekeeper-server", body: fakeScript}, {name: "evil.sh", body: "x"}},
		"duplicate":    {{name: "firekeeper-server", body: fakeScript}, {name: "firekeeper-server", body: fakeScript}},
		"directory":    {{name: "firekeeper-server/", typeflag: tar.TypeDir}},
		"no server":    {{name: "LICENSE", body: "x"}},
		"empty":        {},
		"license link": {{name: "LICENSE", typeflag: tar.TypeSymlink, link: "/etc/passwd"}, {name: "firekeeper-server", body: fakeScript}},
	}
	for name, es := range cases {
		t.Run(name, func(t *testing.T) {
			outside := t.TempDir()
			dir := filepath.Join(outside, "bin")
			in := serve(t, release{tag: "v1.0.0", archive: makeArchive(t, es...)})
			if _, err := in.Install(context.Background(), dir); err == nil {
				t.Fatal("malicious archive accepted")
			}
			if got := entries(t, dir); len(got) != 0 {
				t.Errorf("left behind %v", got)
			}
			if got := entries(t, outside); len(got) > 1 || (len(got) == 1 && got[0] != "bin") {
				t.Errorf("wrote outside the install dir: %v", got)
			}
		})
	}
}

func TestOversizedEntry(t *testing.T) {
	dir := t.TempDir()
	in := serve(t, release{tag: "v1.0.0", archive: makeArchive(t, entry{name: "firekeeper-server", body: strings.Repeat("x", 4096)})})
	in.MaxEntry = 1024
	if _, err := in.Install(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("left behind %v", got)
	}
}

func TestOversizedDownload(t *testing.T) {
	dir := t.TempDir()
	in := serve(t, release{tag: "v1.0.0", archive: goodArchive(t)})
	in.MaxDownload = 10
	if _, err := in.Install(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
}

func TestSanityCheckFailureInstallsNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	in := serve(t, release{tag: "v1.0.0", archive: makeArchive(t, entry{name: "firekeeper-server", body: "#!/bin/sh\nexit 1\n"})})
	if _, err := in.Install(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("err = %v", err)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("left behind %v", got)
	}
}

func TestBadTag(t *testing.T) {
	for _, tag := range []string{"../x", "v1/2", "", "a b"} {
		in := serve(t, release{tag: "v1.0.0", archive: goodArchive(t)})
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"tag_name":"` + tag + `"}`))
		}))
		in.Source = Source{Base: srv.URL}
		in.Client = srv.Client()
		if _, err := in.Install(context.Background(), t.TempDir()); err == nil {
			t.Errorf("tag %q accepted", tag)
		}
		srv.Close()
	}
}

func TestUnsupportedPlatform(t *testing.T) {
	for _, p := range [][2]string{{"windows", "386"}, {"freebsd", "amd64"}, {"linux", "386"}, {"linux", "riscv64"}} {
		in := &Installer{Source: Source{Base: "https://example.invalid"}, GOOS: p[0], GOARCH: p[1]}
		_, err := in.Install(context.Background(), t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "no firekeeper-server build for "+p[0]+"/"+p[1]) {
			t.Errorf("%v: err = %v", p, err)
		}
	}
	for _, p := range [][2]string{{"darwin", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"}} {
		if _, _, err := Platform(p[0], p[1]); err != nil {
			t.Error(err)
		}
	}
}

func TestRedirectToHTTPRefused(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	defer plain.Close()
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer tlsSrv.Close()
	in := &Installer{Source: Source{Base: tlsSrv.URL}, Client: tlsSrv.Client(), GOOS: "linux", GOARCH: "amd64"}
	_, err := in.Install(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "refusing redirect") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveRules(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == EnvURL {
				return v
			}
			return ""
		}
	}
	old := ReleasesURL
	t.Cleanup(func() { ReleasesURL = old })

	ReleasesURL = ""
	if _, ok, err := Resolve(env("")); ok || err != nil {
		t.Errorf("default must be disabled: %v %v", ok, err)
	}
	for _, v := range []string{"https://example.com/r", "http://127.0.0.1:8080/r", "http://localhost:1/r", "http://[::1]:1/r"} {
		if s, ok, err := Resolve(env(v)); !ok || err != nil || !s.AllowLoopbackHTTP {
			t.Errorf("%s: %v %v %v", v, s, ok, err)
		}
	}
	for _, v := range []string{"http://example.com/r", "http://10.0.0.1/r", "ftp://x/r", "file:///tmp", "https://u:p@example.com", "https://", "http://127.0.0.1.evil.com/"} {
		if _, ok, err := Resolve(env(v)); !ok || err == nil {
			t.Errorf("%s accepted (ok=%v err=%v)", v, ok, err)
		}
	}
	// A build-time value never gets the loopback http exception.
	ReleasesURL = "http://127.0.0.1:1/r"
	if _, _, err := Resolve(env("")); err == nil {
		t.Error("build-time http accepted")
	}
	ReleasesURL = ProductionBase
	s, ok, err := Resolve(env(""))
	if !ok || err != nil || s.AllowLoopbackHTTP {
		t.Errorf("build-time https: %v %v %v", s, ok, err)
	}
	if s.latestURL() != "https://api.github.com/repos/DanBradbury/firekeeper-server-releases/releases/latest" ||
		s.downloadURL("v1", "a.tar.gz") != "https://github.com/DanBradbury/firekeeper-server-releases/releases/download/v1/a.tar.gz" {
		t.Errorf("production URLs: %s %s", s.latestURL(), s.downloadURL("v1", "a.tar.gz"))
	}
	// The env value overrides the build-time one.
	if s, _, _ := Resolve(env("https://example.com/x/")); s.Base != "https://example.com/x" {
		t.Errorf("override: %v", s)
	}
}

func TestLoopbackHTTPInstall(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	arch := goodArchive(t)
	sum := sha256.Sum256(arch)
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"tag_name":"v0.1.0"}`)) })
	mux.HandleFunc("/download/v0.1.0/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(hex.EncodeToString(sum[:]) + "  firekeeper-server_0.1.0_linux_amd64.tar.gz\n"))
	})
	mux.HandleFunc("/download/v0.1.0/firekeeper-server_0.1.0_linux_amd64.tar.gz", func(w http.ResponseWriter, _ *http.Request) { w.Write(arch) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	src, _, err := Resolve(func(k string) string {
		if k == EnvURL {
			return srv.URL
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	in := &Installer{Source: src, GOOS: "linux", GOARCH: "amd64"}
	if _, err := in.Install(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	// Without the env exception, the same http base is refused.
	in.Source.AllowLoopbackHTTP = false
	if _, err := in.Install(context.Background(), t.TempDir()); err == nil {
		t.Fatal("http fetched without the loopback exception")
	}
}

func TestChecksumForForms(t *testing.T) {
	h := strings.Repeat("ab", 32)
	if got, err := checksumFor([]byte(h+" *a.tar.gz\n"), "a.tar.gz"); err != nil || got != h {
		t.Errorf("binary-mode line: %v %v", got, err)
	}
	if _, err := checksumFor([]byte("zz  a.tar.gz\n"), "a.tar.gz"); err == nil {
		t.Error("short hash accepted")
	}
	if _, err := checksumFor([]byte(h+"  a.tar.gz\n"+strings.Repeat("cd", 32)+"  a.tar.gz\n"), "a.tar.gz"); err == nil {
		t.Error("conflicting lines accepted")
	}
}

func TestChooseDir(t *testing.T) {
	home := t.TempDir()
	writable := t.TempDir()
	if d, fb, err := ChooseDir(filepath.Join(writable, "firekeeper"), home); err != nil || fb || d != writable {
		t.Errorf("writable: %v %v %v", d, fb, err)
	}
	// Root can write anywhere, and Windows ignores the read-only bit on
	// directories.
	if os.Getuid() != 0 && runtime.GOOS != "windows" {
		ro := t.TempDir()
		os.Chmod(ro, 0o555)
		t.Cleanup(func() { os.Chmod(ro, 0o755) })
		if d, fb, err := ChooseDir(filepath.Join(ro, "firekeeper"), home); err != nil || !fb || d != filepath.Join(home, ".local", "bin") {
			t.Errorf("read-only: %v %v %v", d, fb, err)
		}
	}
	cellar := filepath.Join(t.TempDir(), "Cellar", "firekeeper", "1.0", "bin")
	os.MkdirAll(cellar, 0o755)
	if d, fb, _ := ChooseDir(filepath.Join(cellar, "firekeeper"), home); !fb || d != filepath.Join(home, ".local", "bin") {
		t.Errorf("cellar: %v %v", d, fb)
	}
	if _, _, err := ChooseDir("", ""); err == nil {
		t.Error("no home and no exe accepted")
	}
}

func TestIsHomebrewAndOnPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/opt/homebrew/bin/firekeeper":                  true,
		"/usr/local/Cellar/firekeeper/1/bin/firekeeper": true,
		"/home/linuxbrew/.linuxbrew/bin/firekeeper":     true,
		"/usr/local/bin/firekeeper":                     false,
		"/home/me/bin/firekeeper":                       false,
	} {
		if IsHomebrew(p) != want {
			t.Errorf("IsHomebrew(%s) != %v", p, want)
		}
	}
	sep := string(os.PathListSeparator)
	if !OnPath("/a/b/", "/x"+sep+"/a/b"+sep+"/y") || OnPath("/a/c", "/x"+sep+"/a/b") || OnPath("/a", "") {
		t.Error("OnPath")
	}
}

func TestAssetAndExecutableNames(t *testing.T) {
	for _, c := range []struct{ tag, goos, goarch, asset, exe string }{
		{"v0.8.0", "linux", "amd64", "firekeeper-server_0.8.0_linux_amd64.tar.gz", "firekeeper-server"},
		{"v0.8.0", "darwin", "arm64", "firekeeper-server_0.8.0_darwin_arm64.tar.gz", "firekeeper-server"},
		{"v0.8.0", "windows", "amd64", "firekeeper-server_0.8.0_windows_amd64.zip", "firekeeper-server.exe"},
		{"v0.8.0", "windows", "arm64", "firekeeper-server_0.8.0_windows_arm64.zip", "firekeeper-server.exe"},
	} {
		if got := AssetName(c.tag, c.goos, c.goarch); got != c.asset {
			t.Errorf("AssetName(%s/%s) = %q, want %q", c.goos, c.goarch, got, c.asset)
		}
		if got := ExecutableName(c.goos); got != c.exe {
			t.Errorf("ExecutableName(%s) = %q, want %q", c.goos, got, c.exe)
		}
	}
}

func TestWindowsZipInstall(t *testing.T) {
	t.Parallel()
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "bin")
			in := serve(t, release{tag: "v0.8.0", archive: goodZip(t), goos: "windows", goarch: arch})
			p, err := in.Install(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			if p != filepath.Join(dir, "firekeeper-server.exe") {
				t.Errorf("path %q", p)
			}
			b, err := os.ReadFile(p)
			if err != nil || string(b) != fakeScript {
				t.Errorf("installed body wrong (%d bytes, err %v)", len(b), err)
			}
			if got := entries(t, dir); len(got) != 1 {
				t.Errorf("dir has %v, want only firekeeper-server.exe (no LICENSE, no temp files)", got)
			}
		})
	}
}

func TestWindowsZipReplacesExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "firekeeper-server.exe"), []byte("old"), 0o755)
	in := serve(t, release{tag: "v0.8.0", archive: goodZip(t), goos: "windows"})
	if _, err := in.Install(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "firekeeper-server.exe"))
	if string(b) != fakeScript {
		t.Errorf("not replaced: %d bytes", len(b))
	}
}

// A release from before Windows builds (v0.7.3 has only the darwin and linux
// tar.gz archives) gives a clear error and installs nothing.
func TestNoWindowsBuild(t *testing.T) {
	sums := ""
	for _, p := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"} {
		sums += strings.Repeat("a", 64) + "  firekeeper-server_0.7.3_" + p + ".tar.gz\n"
	}
	for _, arch := range []string{"amd64", "arm64"} {
		dir := t.TempDir()
		in := serve(t, release{tag: "v0.7.3", archive: goodArchive(t), checksum: sums, goos: "windows", goarch: arch})
		_, err := in.Install(context.Background(), dir)
		if !errors.Is(err, ErrNoWindowsBuild) {
			t.Fatalf("%s: err = %v, want ErrNoWindowsBuild", arch, err)
		}
		if want := "no Windows server build available for v0.7.3"; !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %q, want it to say %q", arch, err, want)
		}
		if got := entries(t, dir); len(got) != 0 {
			t.Errorf("%s: left behind %v", arch, got)
		}
	}
}

func TestMaliciousZips(t *testing.T) {
	exe := "firekeeper-server.exe"
	cases := map[string][]entry{
		"traversal":     {{name: "../" + exe, body: fakeScript}},
		"backslash":     {{name: "..\\" + exe, body: fakeScript}},
		"nested":        {{name: "sub/" + exe, body: fakeScript}},
		"dot prefix":    {{name: "./" + exe, body: fakeScript}},
		"absolute":      {{name: "/tmp/" + exe, body: fakeScript}},
		"drive":         {{name: "C:/" + exe, body: fakeScript}},
		"symlink":       {{name: exe, typeflag: tar.TypeSymlink, link: "C:/Windows/System32/cmd.exe"}},
		"extra entry":   {{name: exe, body: fakeScript}, {name: "evil.ps1", body: "x"}},
		"duplicate":     {{name: exe, body: fakeScript}, {name: exe, body: fakeScript}},
		"directory":     {{name: exe + "/", typeflag: tar.TypeDir}},
		"unix name":     {{name: "firekeeper-server", body: fakeScript}},
		"no server":     {{name: "LICENSE", body: "x"}},
		"empty":         {},
		"license link":  {{name: "LICENSE", typeflag: tar.TypeSymlink, link: "C:/secret"}, {name: exe, body: fakeScript}},
		"case mismatch": {{name: "FIREKEEPER-SERVER.EXE", body: fakeScript}},
	}
	for name, es := range cases {
		t.Run(name, func(t *testing.T) {
			outside := t.TempDir()
			dir := filepath.Join(outside, "bin")
			in := serve(t, release{tag: "v1.0.0", archive: makeZip(t, es...), goos: "windows"})
			if _, err := in.Install(context.Background(), dir); err == nil {
				t.Fatal("malicious archive accepted")
			}
			if got := entries(t, dir); len(got) != 0 {
				t.Errorf("left behind %v", got)
			}
			if got := entries(t, outside); len(got) > 1 || (len(got) == 1 && got[0] != "bin") {
				t.Errorf("wrote outside the install dir: %v", got)
			}
		})
	}
}

func TestWindowsTarballRefused(t *testing.T) {
	// The Windows asset name ends in .zip; a tar.gz served under it is not
	// unpacked.
	dir := t.TempDir()
	in := serve(t, release{tag: "v1.0.0", archive: goodArchive(t), goos: "windows"})
	if _, err := in.Install(context.Background(), dir); err == nil {
		t.Fatal("tar.gz accepted as the Windows zip")
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("left behind %v", got)
	}
}

func TestOversizedZipEntry(t *testing.T) {
	dir := t.TempDir()
	in := serve(t, release{tag: "v1.0.0", archive: makeZip(t, entry{name: "firekeeper-server.exe", body: strings.Repeat("x", 4096)}), goos: "windows"})
	in.MaxEntry = 1024
	if _, err := in.Install(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("left behind %v", got)
	}
}
