package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

const (
	idStore    = "11111111-1111-4111-8111-111111111111"
	idHead     = "22222222-2222-4222-8222-222222222222"
	idExcluded = "33333333-3333-4333-8333-333333333333"
	idArchived = "44444444-4444-4444-8444-444444444444"
	idEmpty    = "55555555-5555-4555-8555-555555555555"
	idCorrupt  = "66666666-6666-4666-8666-666666666666"
	idLarge    = "77777777-7777-4777-8777-777777777777"
)

func writeFile(t *testing.T, path, contents string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func day(d int) time.Time { return time.Date(2026, 1, d, 12, 0, 0, 0, time.UTC) }

func sessionMeta(cwd, branch string) string {
	return `{"timestamp":"2026-01-02T03:04:05.000Z","type":"session_meta","payload":{"id":"x","timestamp":"2026-01-02T03:04:05.000Z","cwd":"` + cwd + `","git":{"branch":"` + branch + `"}}}` + "\n"
}

func turnContext(model string) string {
	return `{"timestamp":"2026-01-02T03:04:06.000Z","type":"turn_context","payload":{"cwd":"/ignored","model":"` + model + `"}}` + "\n"
}

// fixture builds a CODEX_HOME with nested date directories, an archived
// rollout, an empty and a corrupt rollout, a rollout far larger than the head
// limit, and two sessions in one date directory.
func fixture(t *testing.T) (home string, paths map[string]string) {
	home = t.TempDir()
	paths = map[string]string{
		idStore:    filepath.Join(home, "sessions", "2026", "01", "02", "rollout-2026-01-02T03-04-05-"+idStore+".jsonl"),
		idHead:     filepath.Join(home, "sessions", "2026", "01", "02", "rollout-2026-01-02T09-00-00-"+idHead+".jsonl"),
		idExcluded: filepath.Join(home, "sessions", "2025", "12", "31", "rollout-2025-12-31T00-00-00-"+idExcluded+".jsonl"),
		idArchived: filepath.Join(home, "archived_sessions", "rollout-2025-11-01T00-00-00-"+idArchived+".jsonl"),
		idEmpty:    filepath.Join(home, "sessions", "2026", "01", "03", "rollout-2026-01-03T00-00-00-"+idEmpty+".jsonl"),
		idCorrupt:  filepath.Join(home, "sessions", "2026", "01", "03", "rollout-2026-01-03T01-00-00-"+idCorrupt+".jsonl"),
		idLarge:    filepath.Join(home, "sessions", "2026", "01", "04", "rollout-2026-01-04T00-00-00-"+idLarge+".jsonl"),
	}
	writeFile(t, paths[idStore], sessionMeta("/wrong", "wrong")+turnContext("wrong"), day(5))
	writeFile(t, paths[idHead], sessionMeta("/work/head", "feature")+turnContext("head-model"), day(6))
	writeFile(t, paths[idExcluded], sessionMeta("/excluded/repo", "main"), day(7))
	writeFile(t, paths[idArchived], sessionMeta("/work/archived", "old-branch")+turnContext("archived-model"), day(1))
	writeFile(t, paths[idEmpty], "", day(8))
	writeFile(t, paths[idCorrupt], "not json\n{{{{\n", day(8))

	var large strings.Builder
	large.WriteString(sessionMeta("/work/large", "big"))
	large.WriteString(turnContext("head-model"))
	filler := `{"timestamp":"2026-01-04T00:00:00.000Z","type":"event_msg","payload":{"type":"filler","text":"` + strings.Repeat("x", 1000) + `"}}` + "\n"
	for large.Len() < 4*transcript.HeadBytes {
		large.WriteString(filler)
	}
	large.WriteString(turnContext("tail-model"))
	writeFile(t, paths[idLarge], large.String(), day(4))

	writeFile(t, filepath.Join(home, "sessions", "2026", "01", "02", "rollout-no-uuid.jsonl"), sessionMeta("/x", "y"), day(9))
	writeFile(t, filepath.Join(home, "state_5.sqlite"), "", day(1))
	return home, paths
}

// storeRows are the threads rows the fake sqlite3 returns. The first row is
// complete, so its rollout never needs opening; the excluded one names its
// directory but no model; the archived one lacks a branch.
var storeRows = []threadRow{
	{ID: idStore, Title: "Stored title", CWD: "/work/store", Model: "store-model", Branch: "main", CreatedMS: day(2).UnixMilli(), UpdatedMS: day(5).UnixMilli()},
	{ID: idExcluded, Title: "Excluded", CWD: "/excluded/repo"},
	{ID: idArchived, Title: "Archived", CWD: "/work/archived", Model: "store-archived-model", CreatedMS: day(1).UnixMilli() - 1000, UpdatedMS: day(1).UnixMilli()},
}

func fakeSQLite(t *testing.T, home string, fail bool) func(context.Context, string, ...string) ([]byte, error) {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "sqlite3" || len(args) != 4 || args[0] != "-readonly" || args[1] != "-json" || args[2] != filepath.Join(home, "state_5.sqlite") {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		if fail {
			return nil, errors.New("database is locked")
		}
		if strings.HasPrefix(args[3], "PRAGMA") {
			return []byte(`[{"name":"id"},{"name":"title"},{"name":"name"},{"name":"cwd"},{"name":"model"},{"name":"git_branch"},{"name":"created_at"},{"name":"created_at_ms"},{"name":"updated_at"},{"name":"updated_at_ms"},{"name":"preview"},{"name":"first_user_message"}]`), nil
		}
		if strings.Contains(args[3], "preview") || strings.Contains(args[3], "first_user_message") {
			t.Errorf("query reads conversation columns: %s", args[3])
		}
		return json.Marshal(storeRows)
	}
}

// opens records each transcript opened and how many bytes were read from it.
type opens struct {
	mu    sync.Mutex
	bytes map[string]int64
}

func (o *opens) open(name string) (io.ReadCloser, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.bytes == nil {
		o.bytes = map[string]int64{}
	}
	o.bytes[name] += 0
	return &countingReader{file, o, name}, nil
}

type countingReader struct {
	*os.File
	o    *opens
	name string
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.File.Read(p)
	c.o.mu.Lock()
	c.o.bytes[c.name] += int64(n)
	c.o.mu.Unlock()
	return n, err
}

func (o *opens) opened(path string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.bytes[path]
	return ok
}

func TestEnumerate(t *testing.T) {
	home, paths := fixture(t)
	var o opens
	source := &Source{CodexHome: home, open: o.open, run: fakeSQLite(t, home, false)}
	skipCalls := map[string]int{}
	skip := func(m session.Meta) bool {
		skipCalls[m.ID]++
		return strings.HasPrefix(m.CWD, "/excluded")
	}

	metas, err := source.Enumerate(context.Background(), transcript.EnumerateOptions{Skip: skip})
	var warning *session.Warning
	if !errors.As(err, &warning) || !strings.Contains(warning.Message, "skipped 2 ") {
		t.Fatalf("err = %v, want warning counting the empty and corrupt rollouts", err)
	}
	var ids []string
	for _, m := range metas {
		ids = append(ids, m.ID)
	}
	if want := []string{idHead, idStore, idLarge, idArchived}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for id, calls := range skipCalls {
		if calls != 1 {
			t.Errorf("Skip called %d times for %s", calls, id)
		}
	}
	if skipCalls[idExcluded] != 1 {
		t.Error("Skip not called for the excluded session")
	}

	// The store supplied the excluded session's directory, so its rollout is
	// never opened; the complete store row needs no rollout either.
	if o.opened(paths[idExcluded]) {
		t.Error("excluded session's rollout was opened")
	}
	if o.opened(paths[idStore]) {
		t.Error("rollout opened although the store supplied every field")
	}
	if o.opened(paths[idEmpty]) {
		t.Error("empty rollout was opened")
	}
	if n := o.bytes[paths[idLarge]]; n == 0 || n > transcript.HeadBytes {
		t.Errorf("read %d bytes of the large rollout, want 1..%d", n, transcript.HeadBytes)
	}

	byID := map[string]session.Meta{}
	for _, m := range metas {
		byID[m.ID] = m
		if m.State != session.SessionStateEnded || m.Provider != "codex" || m.EventCount != 0 || m.TokensUsed != 0 {
			t.Errorf("%s: state %v provider %q counts %d/%d", m.ID, m.State, m.Provider, m.EventCount, m.TokensUsed)
		}
		if m.RolloutPath != paths[m.ID] {
			t.Errorf("%s: rollout path = %q", m.ID, m.RolloutPath)
		}
	}

	stored := byID[idStore]
	if stored.CWD != "/work/store" || stored.Project != "store" || stored.Model != "store-model" || stored.Branch != "main" ||
		stored.Title != "Stored title" || stored.Name != "Stored title" || stored.GitBranch != "main" ||
		!stored.StartedAt.Equal(day(2)) || !stored.LastActivityAt.Equal(day(5)) {
		t.Errorf("store-backed meta = %+v", stored)
	}
	headed := byID[idHead]
	if headed.CWD != "/work/head" || headed.Branch != "feature" || headed.Model != "head-model" || headed.Title != "" ||
		headed.StartedAt == nil || !headed.StartedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) || headed.LastActivityAt != nil {
		t.Errorf("head-backed meta = %+v", headed)
	}
	archived := byID[idArchived]
	if archived.Model != "store-archived-model" || archived.Branch != "old-branch" || archived.Title != "Archived" {
		t.Errorf("archived meta = %+v; store fields must win and the head fill only gaps", archived)
	}
	if large := byID[idLarge]; large.Model != "head-model" {
		t.Errorf("large rollout model = %q, want the head's model", large.Model)
	}

	// Enumerated metas round-trip through Locate, by path and by id alone.
	for _, m := range metas {
		got, err := source.Locate(m)
		if err != nil || !reflect.DeepEqual(got, []string{m.RolloutPath}) {
			t.Errorf("Locate(%s) = %v, %v", m.ID, got, err)
		}
		got, err = source.Locate(session.Meta{ID: m.ID, Provider: m.Provider})
		if err != nil || !reflect.DeepEqual(got, []string{m.RolloutPath}) {
			t.Errorf("Locate by id(%s) = %v, %v", m.ID, got, err)
		}
	}
}

func TestEnumerateModifiedAfter(t *testing.T) {
	home, _ := fixture(t)
	source := &Source{CodexHome: home, run: fakeSQLite(t, home, false)}
	metas, err := source.Enumerate(context.Background(), transcript.EnumerateOptions{ModifiedAfter: day(5)})
	if err != nil {
		// Only rollouts modified after day 5 remain: the head-only one, the
		// excluded one (no Skip here), and the empty and corrupt ones.
		var warning *session.Warning
		if !errors.As(err, &warning) {
			t.Fatal(err)
		}
	}
	var ids []string
	for _, m := range metas {
		ids = append(ids, m.ID)
	}
	if want := []string{idExcluded, idHead}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

func TestEnumerateWithoutStore(t *testing.T) {
	home, paths := fixture(t)
	if err := os.Remove(filepath.Join(home, "state_5.sqlite")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	var o opens
	source := &Source{open: o.open, run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("sqlite3 run without a state database")
		return nil, nil
	}}
	metas, _ := source.Enumerate(context.Background(), transcript.EnumerateOptions{
		Skip: func(m session.Meta) bool { return strings.HasPrefix(m.CWD, "/excluded") },
	})
	if len(metas) != 4 {
		t.Fatalf("got %d sessions, want 4", len(metas))
	}
	// With no store, the directory comes from the head, so the excluded
	// rollout is opened to decide, but its head only.
	if n := o.bytes[paths[idExcluded]]; n == 0 || n > transcript.HeadBytes {
		t.Errorf("read %d bytes of the excluded rollout", n)
	}
	for _, m := range metas {
		if m.ID == idStore && m.CWD != "/wrong" {
			t.Errorf("head metadata not used without a store: %+v", m)
		}
	}
}

func TestEnumerateStoreFailure(t *testing.T) {
	home, _ := fixture(t)
	source := &Source{CodexHome: home, run: fakeSQLite(t, home, true)}
	metas, err := source.Enumerate(context.Background(), transcript.EnumerateOptions{})
	var warning *session.Warning
	if !errors.As(err, &warning) || !strings.Contains(warning.Message, "state database") || strings.Contains(warning.Message, home) {
		t.Fatalf("err = %v, want a sanitized store warning", err)
	}
	if len(metas) != 5 {
		t.Fatalf("got %d sessions, want 5 from rollout heads", len(metas))
	}
}

func TestEnumerateEmptyHome(t *testing.T) {
	metas, err := (&Source{CodexHome: t.TempDir()}).Enumerate(context.Background(), transcript.EnumerateOptions{})
	if err != nil || len(metas) != 0 {
		t.Fatalf("Enumerate = %v, %v", metas, err)
	}
}

func TestEnumerateCanceled(t *testing.T) {
	home, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &Source{CodexHome: home, run: fakeSQLite(t, home, false)}
	if _, err := source.Enumerate(ctx, transcript.EnumerateOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestEnumeratorRegistered(t *testing.T) {
	if _, ok := transcript.EnumeratorFor(transcript.ProviderCodex); !ok {
		t.Fatal("codex source does not enumerate")
	}
}
