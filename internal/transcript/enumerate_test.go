package transcript

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
)

type stubEnumerator struct{ stubSource }

func (stubEnumerator) Enumerate(context.Context, EnumerateOptions) ([]session.Meta, error) {
	return nil, nil
}

func TestEnumeratorFor(t *testing.T) {
	Register("enumerate-test", stubEnumerator{})
	Register("enumerate-test-plain", stubSource{})
	if _, ok := EnumeratorFor("enumerate-test"); !ok {
		t.Error("enumerating source not found")
	}
	if _, ok := EnumeratorFor("enumerate-test-plain"); ok {
		t.Error("source without Enumerate reported as an enumerator")
	}
	if _, ok := EnumeratorFor("enumerate-test-missing"); ok {
		t.Error("unregistered provider reported as an enumerator")
	}
}

func TestProjectName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0700); err != nil {
		t.Fatal(err)
	}
	for cwd, want := range map[string]string{
		"":                                 "",
		root:                               "repo",
		filepath.Join(root, "a", "b"):      "repo",
		filepath.Join(t.TempDir(), "gone"): "gone",
	} {
		if got := ProjectName(cwd); got != want {
			t.Errorf("ProjectName(%q) = %q, want %q", cwd, got, want)
		}
	}
}

func TestSortNewestFirst(t *testing.T) {
	at := func(d int) *time.Time { v := time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC); return &v }
	metas := []session.Meta{
		{ID: "old", LastActivityAt: at(1)},
		{ID: "mtime", RolloutPath: "/m"},
		{ID: "b", LastActivityAt: at(3)},
		{ID: "a", LastActivityAt: at(3)},
	}
	SortNewestFirst(metas, map[string]time.Time{"/m": *at(2)})
	var ids []string
	for _, m := range metas {
		ids = append(ids, m.ID)
	}
	if want := []string{"a", "b", "mtime", "old"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("order = %v, want %v", ids, want)
	}
}
