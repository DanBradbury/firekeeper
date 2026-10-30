package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/serverinstall"
)

// The serve install and update tests run the server they install. On Unix
// that fake server is a shell script. Windows runs no scripts by file name,
// so there the fake is a copy of this test binary with a trailer appended
// (Windows ignores data after the PE image): fakeServerTrailer and a JSON
// fakeServerSpec. runFakeServerFromTrailer, called first by TestMain, makes
// such a copy act as the server.

const fakeServerTrailer = "\n--firekeeper-cli-fake-server--\n"

// fakeServerSpec is what a Windows fake server does. With Banner empty it
// echoes "fake-server: <args>" for everything; otherwise `version` prints
// Banner, `license` prints License and exits 3, and anything else echoes.
type fakeServerSpec struct {
	Banner  string `json:"banner,omitempty"`
	License string `json:"license,omitempty"`
}

var (
	testExeOnce  sync.Once
	testExeBytes []byte
	testExeErr   error
)

// fakeServerBody is the file content of a fake server doing spec.
func fakeServerBody(t *testing.T, spec fakeServerSpec) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		if spec.Banner == "" {
			return "#!/bin/sh\necho \"fake-server: $*\"\nexit 0\n"
		}
		return "#!/bin/sh\n" +
			"case \"$1\" in\n" +
			"  version) echo '" + spec.Banner + "'; exit 0;;\n" +
			"  license) echo '" + spec.License + "'; exit 3;;\n" +
			"esac\n" +
			"echo \"fake-server: $*\"\n"
	}
	testExeOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			testExeErr = err
			return
		}
		testExeBytes, testExeErr = os.ReadFile(exe)
	})
	if testExeErr != nil {
		t.Fatal(testExeErr)
	}
	j, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return string(testExeBytes) + fakeServerTrailer + string(j)
}

// runFakeServerFromTrailer exits, acting as the fake server, when this
// executable carries a fake-server trailer. Only Windows fakes have one.
func runFakeServerFromTrailer() {
	if runtime.GOOS != "windows" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	f, err := os.Open(exe)
	if err != nil {
		return
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return
	}
	const tail = 64 << 10
	off := max(st.Size()-tail, 0)
	b := make([]byte, st.Size()-off)
	_, err = f.ReadAt(b, off)
	f.Close()
	if err != nil && err != io.EOF {
		return
	}
	i := bytes.LastIndex(b, []byte(fakeServerTrailer))
	if i < 0 {
		return
	}
	var spec fakeServerSpec
	if json.Unmarshal(b[i+len(fakeServerTrailer):], &spec) != nil {
		return
	}
	args := os.Args[1:]
	if spec.Banner != "" && len(args) > 0 {
		switch args[0] {
		case "version":
			os.Stdout.WriteString(spec.Banner + "\n")
			os.Exit(0)
		case "license":
			os.Stdout.WriteString(spec.License + "\n")
			os.Exit(3)
		}
	}
	os.Stdout.WriteString("fake-server: " + strings.Join(args, " ") + "\n")
	os.Exit(0)
}

// serverArchive packs body as this platform's release archive (tar.gz with
// firekeeper-server, or on Windows a zip with firekeeper-server.exe, each with
// a LICENSE) and returns it with its asset name for tag.
func serverArchive(t *testing.T, tag, body string) (archive []byte, asset string) {
	t.Helper()
	var buf bytes.Buffer
	entries := [][2]string{{"LICENSE", "lic"}, {serverinstall.ExecutableName(runtime.GOOS), body}}
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		for _, e := range entries {
			// Stored, not deflated: the fake is a copy of the test binary.
			w, err := zw.CreateHeader(&zip.FileHeader{Name: e[0], Method: zip.Store})
			if err != nil {
				t.Fatal(err)
			}
			w.Write([]byte(e[1]))
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for _, e := range entries {
			tw.WriteHeader(&tar.Header{Name: e[0], Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(e[1]))})
			tw.Write([]byte(e[1]))
		}
		tw.Close()
		gz.Close()
	}
	return buf.Bytes(), serverinstall.AssetName(tag, runtime.GOOS, runtime.GOARCH)
}
