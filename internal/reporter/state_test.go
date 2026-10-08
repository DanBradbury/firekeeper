package reporter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	state, err := LoadState(path)
	if err != nil || len(state.Files) != 0 {
		t.Fatalf("missing state = %+v, %v", state, err)
	}
	mtime := time.Date(2026, 10, 8, 1, 2, 3, 0, time.UTC)
	state.Files["/x/a.jsonl"] = FileState{Offset: 10, Size: 12, Mtime: mtime, LastSeq: 4}
	if err := state.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %v, %v", info.Mode(), err)
	}
	loaded, err := LoadState(path)
	if err != nil || loaded.Files["/x/a.jsonl"] != state.Files["/x/a.jsonl"] {
		t.Fatalf("loaded = %+v, %v", loaded, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("temporary files left behind: %d entries", len(entries))
	}
}

func TestLoadStateErrorsOmitPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	for _, data := range []string{"{", `{"files": null}`} {
		os.WriteFile(path, []byte(data), 0o600)
		state, err := LoadState(path)
		if data == "{" {
			if err == nil || strings.Contains(err.Error(), dir) {
				t.Fatalf("invalid JSON err = %v", err)
			}
			continue
		}
		if err != nil || state.Files == nil {
			t.Fatalf("null files = %+v, %v", state, err)
		}
	}
	if _, err := LoadState(dir); err == nil || strings.Contains(err.Error(), dir) {
		t.Fatalf("directory err = %v", err)
	}
}
