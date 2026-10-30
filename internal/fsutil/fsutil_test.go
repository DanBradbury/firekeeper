package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path)
	if err != nil || string(got) != "hello" {
		t.Fatalf("ReadFile = %q, %v", got, err)
	}
	if _, err := ReadFile(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file err = %v, want os.ErrNotExist", err)
	}
}

// A reader must not stop the writer from renaming or deleting the file,
// which is what Windows does to a file opened with os.Open. (Renaming a new
// file over one that is open is refused on Windows whatever the share mode,
// so the state writer retries instead.)
func TestOpenedFileCanBeRenamedAndDeleted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("rename while open: %v", err)
	}
	if err := os.Remove(path + ".1"); err != nil {
		t.Fatalf("delete while open: %v", err)
	}
}
