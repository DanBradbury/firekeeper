package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRelease is a local directory laid out like GitHub release downloads,
// so install.sh runs end to end with no network.
type fakeRelease struct {
	root    string // contains latest.json and download/<tag>/...
	bin     string // uname shim, first on PATH
	home    string
	install string
}

func newFakeRelease(t *testing.T) *fakeRelease {
	t.Helper()
	for _, tool := range []string{"sh", "curl", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	dir := t.TempDir()
	r := &fakeRelease{
		root:    filepath.Join(dir, "releases"),
		bin:     filepath.Join(dir, "bin"),
		home:    filepath.Join(dir, "home"),
		install: filepath.Join(dir, "home", ".local", "bin"),
	}
	for _, d := range []string{r.root, r.bin, r.home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// uname reports whatever FAKE_UNAME_S and FAKE_UNAME_M say.
	shim := "#!/bin/sh\ncase \"$1\" in\n-s) echo \"$FAKE_UNAME_S\" ;;\n-m) echo \"$FAKE_UNAME_M\" ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(r.bin, "uname"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	return r
}

// publish writes one release for darwin/linux × amd64/arm64 plus its
// checksums file, and optionally marks it latest.
func (r *fakeRelease) publish(t *testing.T, tag string, latest bool) {
	t.Helper()
	version := strings.TrimPrefix(tag, "v")
	dir := filepath.Join(r.root, "download", tag)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var sums strings.Builder
	for _, osName := range []string{"darwin", "linux"} {
		for _, arch := range []string{"amd64", "arm64"} {
			name := fmt.Sprintf("firekeeper_%s_%s_%s.tar.gz", version, osName, arch)
			script := fmt.Sprintf("#!/bin/sh\necho 'firekeeper %s %s/%s'\n", version, osName, arch)
			data := tarGz(t, map[string]string{"firekeeper": script, "README.md": "readme\n"})
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "firekeeper_"+version+"_checksums.txt"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if latest {
		body := fmt.Sprintf("{\n  \"url\": \"x\",\n  \"tag_name\": \"%s\",\n  \"name\": \"%s\"\n}\n", tag, tag)
		if err := os.WriteFile(filepath.Join(r.root, "latest.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// run executes install.sh with the fake release and returns its exit error
// and combined output.
func (r *fakeRelease) run(t *testing.T, uname [2]string, extraPath string, env []string, args ...string) (string, error) {
	t.Helper()
	script, err := filepath.Abs("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	path := r.bin + string(os.PathListSeparator) + os.Getenv("PATH")
	if extraPath != "" {
		path = extraPath + string(os.PathListSeparator) + path
	}
	cmd := exec.Command("sh", append([]string{script}, args...)...)
	cmd.Dir = r.home
	cmd.Env = append([]string{
		"HOME=" + r.home,
		"PATH=" + path,
		"FAKE_UNAME_S=" + uname[0],
		"FAKE_UNAME_M=" + uname[1],
		"FIREKEEPER_RELEASES_API=file://" + filepath.Join(r.root, "latest.json"),
		"FIREKEEPER_DOWNLOAD_URL=file://" + filepath.Join(r.root, "download"),
	}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *fakeRelease) installed(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command(filepath.Join(dir, "firekeeper")).Output()
	if err != nil {
		t.Fatalf("run installed binary: %v", err)
	}
	return strings.TrimSpace(string(out))
}

var appleSilicon = [2]string{"Darwin", "arm64"}

func TestInstallScriptLatest(t *testing.T) {
	r := newFakeRelease(t)
	r.publish(t, "v0.1.0", false)
	r.publish(t, "v0.2.0", true)
	out, err := r.run(t, appleSilicon, "", nil)
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	if got := r.installed(t, r.install); got != "firekeeper 0.2.0 darwin/arm64" {
		t.Fatalf("installed %q", got)
	}
	if !strings.Contains(out, "is not on your PATH") || !strings.Contains(out, `export PATH="`+r.install+`:$PATH"`) {
		t.Fatalf("missing PATH hint:\n%s", out)
	}
	if strings.Contains(out, "sudo") {
		t.Fatalf("installer mentions sudo:\n%s", out)
	}
}

func TestInstallScriptPinnedVersionAndDir(t *testing.T) {
	r := newFakeRelease(t)
	r.publish(t, "v0.1.0", false)
	r.publish(t, "v0.2.0", true)
	dir := filepath.Join(r.home, "custom bin")
	tests := []struct {
		name  string
		uname [2]string
		args  []string
		want  string
	}{
		{"flag", [2]string{"Linux", "x86_64"}, []string{"--version", "v0.1.0"}, "firekeeper 0.1.0 linux/amd64"},
		{"equals without v", [2]string{"Linux", "aarch64"}, []string{"--version=0.1.0"}, "firekeeper 0.1.0 linux/arm64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := r.run(t, tt.uname, dir, []string{"FIREKEEPER_INSTALL_DIR=" + dir}, tt.args...)
			if err != nil {
				t.Fatalf("install failed: %v\n%s", err, out)
			}
			if got := r.installed(t, dir); got != tt.want {
				t.Fatalf("installed %q, want %q", got, tt.want)
			}
			if strings.Contains(out, "is not on your PATH") {
				t.Fatalf("unexpected PATH hint when dir is on PATH:\n%s", out)
			}
		})
	}
}

func TestInstallScriptChecksumMismatchAborts(t *testing.T) {
	r := newFakeRelease(t)
	r.publish(t, "v0.2.0", true)
	archive := filepath.Join(r.root, "download", "v0.2.0", "firekeeper_0.2.0_darwin_arm64.tar.gz")
	tampered := tarGz(t, map[string]string{"firekeeper": "#!/bin/sh\necho tampered\n"})
	if err := os.WriteFile(archive, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.run(t, appleSilicon, "", nil)
	if err == nil {
		t.Fatalf("install succeeded despite checksum mismatch:\n%s", out)
	}
	if !strings.Contains(out, "checksum mismatch") {
		t.Fatalf("output does not mention the mismatch:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(r.install, "firekeeper")); !os.IsNotExist(statErr) {
		t.Fatalf("binary was installed after a checksum mismatch (stat err %v)", statErr)
	}
}

func TestInstallScriptFailures(t *testing.T) {
	r := newFakeRelease(t)
	r.publish(t, "v0.2.0", true)
	tests := []struct {
		name  string
		uname [2]string
		args  []string
		want  string
	}{
		{"unsupported os", [2]string{"MINGW64_NT-10.0", "x86_64"}, nil, "unsupported operating system 'MINGW64_NT-10.0'"},
		{"unsupported arch", [2]string{"Linux", "riscv64"}, nil, "unsupported architecture 'riscv64'"},
		{"missing release", appleSilicon, []string{"--version", "v9.9.9"}, "could not download firekeeper_9.9.9_darwin_arm64.tar.gz"},
		{"missing version value", appleSilicon, []string{"--version"}, "--version needs a value"},
		{"unknown argument", appleSilicon, []string{"--bogus"}, "unknown argument '--bogus'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := r.run(t, tt.uname, "", nil, tt.args...)
			if err == nil {
				t.Fatalf("install succeeded:\n%s", out)
			}
			if !strings.Contains(out, tt.want) {
				t.Fatalf("output %q does not contain %q", out, tt.want)
			}
			if _, statErr := os.Stat(filepath.Join(r.install, "firekeeper")); !os.IsNotExist(statErr) {
				t.Fatalf("binary was installed (stat err %v)", statErr)
			}
		})
	}
}
