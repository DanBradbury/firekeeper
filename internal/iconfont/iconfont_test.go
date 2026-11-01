package iconfont

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedFontIsTrueType(t *testing.T) {
	if len(data) < 1000 || !bytes.HasPrefix(data, []byte{0, 1, 0, 0}) {
		t.Fatalf("embedded font is not a TrueType file (%d bytes)", len(data))
	}
}

func TestDataReturnsCopy(t *testing.T) {
	copyOf := Data()
	copyOf[0] = 0xff
	if data[0] == 0xff {
		t.Fatal("Data exposes the embedded slice")
	}
}

func TestUserDir(t *testing.T) {
	for _, test := range []struct {
		goos, home, xdg, want string
		err                   error
	}{
		{"darwin", "/h", "", filepath.Join("/h", "Library", "Fonts"), nil},
		{"linux", "/h", "", filepath.Join("/h", ".local", "share", "fonts"), nil},
		{"linux", "/h", "/x", filepath.Join("/x", "fonts"), nil},
		{"windows", "/h", "", "", ErrUnsupported},
	} {
		got, err := UserDir(test.goos, test.home, test.xdg)
		if got != test.want || !errors.Is(err, test.err) {
			t.Errorf("UserDir(%q,%q,%q) = %q, %v; want %q, %v", test.goos, test.home, test.xdg, got, err, test.want, test.err)
		}
	}
	if _, err := UserDir("darwin", "", ""); err == nil {
		t.Error("empty home accepted")
	}
}

func TestInstallLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fonts", "nested")
	if Installed(dir) {
		t.Fatal("installed before install")
	}
	path, changed, err := Install(dir)
	if err != nil || !changed || filepath.Base(path) != FileName {
		t.Fatalf("Install = %q, %v, %v", path, changed, err)
	}
	if !Installed(dir) {
		t.Fatal("not installed after install")
	}
	if _, changed, err := Install(dir); err != nil || changed {
		t.Fatalf("second Install changed=%v err=%v", changed, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}

	// A stale copy is replaced.
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Installed(dir) {
		t.Fatal("stale copy counted as installed")
	}
	if _, changed, err := Install(dir); err != nil || !changed || !Installed(dir) {
		t.Fatalf("stale replace: changed=%v err=%v", changed, err)
	}

	if removed, err := Remove(dir); err != nil || !removed {
		t.Fatalf("Remove = %v, %v", removed, err)
	}
	if removed, err := Remove(dir); err != nil || removed {
		t.Fatalf("second Remove = %v, %v", removed, err)
	}
}
