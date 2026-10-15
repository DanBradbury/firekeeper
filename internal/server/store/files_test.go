package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/redact"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	_ "github.com/DanBradbury/firekeeper/internal/transcript/claude"
	_ "github.com/DanBradbury/firekeeper/internal/transcript/codex"
	_ "github.com/DanBradbury/firekeeper/internal/transcript/copilot"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// fixtureHome is the home directory the files fixtures are written under;
// redaction rewrites it to "~" as the reporter does.
const fixtureHome = "/home/fixture"

// ingestFixture reads a provider fixture through its real transcript
// source, redacts each event and the cwd as the reporter does, and ingests
// the result in one batch.
func ingestFixture(t *testing.T, s *Store, provider transcript.Provider, path string) SessionBatch {
	t.Helper()
	src, ok := reader.For(provider)
	if !ok {
		t.Fatalf("no source for %s", provider)
	}
	events, _, err := src.Read(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	opts := redact.Options{HomeDir: fixtureHome}
	cwd, _ := redact.String(fixtureHome+"/src/demo", opts)
	b := SessionBatch{Meta: SessionMeta{SessionID: "s-" + string(provider), Provider: string(provider), CWD: cwd}}
	for _, e := range events {
		e.MachineID, e.SessionID = "m1", b.Meta.SessionID
		e, _ = redact.Event(e, opts)
		b.Events = append(b.Events, e)
	}
	if _, _, err := s.Ingest(context.Background(), DefaultAccountID, Machine{ID: "m1"}, []SessionBatch{b}); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSessionFilesFromFixtures(t *testing.T) {
	tests := []struct {
		provider transcript.Provider
		fixture  string
		want     []SessionFile
	}{
		{transcript.ProviderClaude, "testdata/files/claude.jsonl", []SessionFile{
			{Path: "/etc/hosts", Absolute: true, FirstSeq: 7, LastSeq: 7, Changes: 1},
			{Path: "keys/[REDACTED:api_key].txt", FirstSeq: 6, LastSeq: 6, Changes: 1},
			{Path: "main.go", FirstSeq: 2, LastSeq: 4, Changes: 2},
			{Path: "nb/analysis.ipynb", FirstSeq: 5, LastSeq: 5, Changes: 1},
			{Path: "~/.zshrc", Absolute: true, FirstSeq: 3, LastSeq: 3, Changes: 1},
		}},
		{transcript.ProviderCodex, "testdata/files/codex.jsonl", []SessionFile{
			{Path: "docs/new.md", FirstSeq: 2, LastSeq: 2, Changes: 1},
			{Path: "main.go", FirstSeq: 2, LastSeq: 6, Changes: 2},
			{Path: "old.txt", FirstSeq: 2, LastSeq: 2, Changes: 1},
			{Path: "src/a.go", FirstSeq: 4, LastSeq: 4, Changes: 1},
			{Path: "src/b.go", FirstSeq: 4, LastSeq: 4, Changes: 1},
			{Path: "~/src/sibling/lib.go", Absolute: true, FirstSeq: 4, LastSeq: 4, Changes: 1},
		}},
		{transcript.ProviderCopilot, "testdata/files/copilot.jsonl", []SessionFile{
			{Path: "/tmp/scratch.txt", Absolute: true, FirstSeq: 8, LastSeq: 8, Changes: 1},
			{Path: "cmd/tool/main.go", FirstSeq: 3, LastSeq: 3, Changes: 1},
			{Path: "go.mod", FirstSeq: 6, LastSeq: 6, Changes: 1},
			{Path: "main.go", FirstSeq: 2, LastSeq: 2, Changes: 1},
			{Path: "notes/todo.md", FirstSeq: 7, LastSeq: 7, Changes: 1},
		}},
	}
	for _, tt := range tests {
		t.Run(string(tt.provider), func(t *testing.T) {
			ctx := context.Background()
			s := open(t)
			b := ingestFixture(t, s, tt.provider, tt.fixture)
			got, err := s.ListFiles(ctx, DefaultAccountID, "m1", b.Meta.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("files:\n got %+v\nwant %+v", got, tt.want)
			}
			for _, f := range got {
				if strings.Contains(f.Path, fixtureHome) || strings.Contains(f.Path, "sk-") {
					t.Errorf("stored path %q was not redacted", f.Path)
				}
			}

			// Re-sending the same events stores nothing new.
			if _, _, err := s.Ingest(ctx, DefaultAccountID, Machine{ID: "m1"}, []SessionBatch{b}); err != nil {
				t.Fatal(err)
			}
			again, err := s.ListFiles(ctx, DefaultAccountID, "m1", b.Meta.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(again, tt.want) {
				t.Fatalf("after re-ingest:\n got %+v\nwant %+v", again, tt.want)
			}
		})
	}
}

func TestRelPath(t *testing.T) {
	tests := []struct{ cwd, path, want string }{
		{"~/src/demo", "~/src/demo/a/b.go", "a/b.go"},
		{"~/src/demo/", "~/src/demo/a.go", "a.go"},
		{"/work/demo", "a/../b.go", "b.go"},
		{"/work/demo", "./a.go", "a.go"},
		{"/work/demo", "../other/x.go", "/work/other/x.go"},
		{"/work/demo", "/work/demo-two/x.go", "/work/demo-two/x.go"},
		{"/work/demo", "/work/demo", "/work/demo"},
		{"/work/demo", "~/x.go", "~/x.go"},
		{"/", "/etc/hosts", "etc/hosts"},
		{"", "/abs/x.go", "/abs/x.go"},
		{"", "rel/x.go", "rel/x.go"},
		{"relative/cwd", "x.go", "x.go"},
		{`C:/work/demo`, `C:\work\demo\src\x.go`, "src/x.go"},
		{"/work/demo", "  ", ""},
	}
	for _, tt := range tests {
		if got := relPath(tt.cwd, tt.path); got != tt.want {
			t.Errorf("relPath(%q, %q) = %q, want %q", tt.cwd, tt.path, got, tt.want)
		}
	}
}

func TestCommitFirstValueKept(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	m := Machine{ID: "m1"}
	for _, commit := range []string{"", "aaaa", "bbbb", ""} {
		b := SessionBatch{Meta: SessionMeta{SessionID: "s1", Provider: "codex", Commit: commit}}
		if _, _, err := s.Ingest(ctx, DefaultAccountID, m, []SessionBatch{b}); err != nil {
			t.Fatal(err)
		}
	}
	se, err := s.GetSession(ctx, DefaultAccountID, "m1", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if se.Commit != "aaaa" {
		t.Fatalf("commit = %q, want the first non-empty value", se.Commit)
	}
}

func TestFilesUseStoredCwd(t *testing.T) {
	// A later batch without a cwd still resolves against the stored one.
	ctx := context.Background()
	s := open(t)
	m := Machine{ID: "m1"}
	first := SessionBatch{Meta: SessionMeta{SessionID: "s1", Provider: "claude", CWD: "/work/demo"}}
	if _, _, err := s.Ingest(ctx, DefaultAccountID, m, []SessionBatch{first}); err != nil {
		t.Fatal(err)
	}
	raw := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/work/demo/x.go"}}]}}`
	second := SessionBatch{Meta: SessionMeta{SessionID: "s1", Provider: "claude"}, Events: []transcript.Event{
		{Provider: transcript.ProviderClaude, Seq: 0, Role: transcript.RoleToolCall, Raw: json.RawMessage(raw)},
	}}
	if _, _, err := s.Ingest(ctx, DefaultAccountID, m, []SessionBatch{second}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListFiles(ctx, DefaultAccountID, "m1", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "x.go" {
		t.Fatalf("files = %+v", got)
	}
}

func TestGitFiles(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	m := Machine{ID: "m1"}
	ingest := func(t *testing.T, meta SessionMeta, events ...transcript.Event) {
		t.Helper()
		meta.SessionID, meta.Provider = "s1", "claude"
		if _, _, err := s.Ingest(ctx, DefaultAccountID, m, []SessionBatch{{Meta: meta, Events: events}}); err != nil {
			t.Fatal(err)
		}
	}
	list := func(t *testing.T) (FilesSource, []string) {
		t.Helper()
		se, err := s.GetSession(ctx, DefaultAccountID, "m1", "s1")
		if err != nil {
			t.Fatal(err)
		}
		files, err := s.ListFiles(ctx, DefaultAccountID, "m1", "s1")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range files {
			out = append(out, fmt.Sprintf("%s:%d:%d:%d", f.Path, f.FirstSeq, f.LastSeq, f.Changes))
		}
		return se.FilesSource, out
	}

	raw := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"~/src/demo/a.go"}}]}}`
	ingest(t, SessionMeta{CWD: "~/src/demo"}, transcript.Event{Provider: transcript.ProviderClaude, Seq: 3, Role: transcript.RoleToolCall, Raw: json.RawMessage(raw)})
	if src, got := list(t); src != FilesFromToolCalls || !reflect.DeepEqual(got, []string{"a.go:3:3:1"}) {
		t.Fatalf("tool calls only: (%q, %q)", src, got)
	}

	// A Git list replaces the tool-call list and keeps its counts.
	ingest(t, SessionMeta{FilesSource: FilesFromGit, Files: []string{"~/src/demo/a.go", "~/src/demo/b.go", "~/other/c.go", "~/src/demo/b.go"}})
	want := []string{"a.go:3:3:1", "b.go:-1:-1:0", "~/other/c.go:-1:-1:0"}
	if src, got := list(t); src != FilesFromGit || !reflect.DeepEqual(got, want) {
		t.Fatalf("git list: (%q, %q), want (git, %q)", src, got, want)
	}

	// Batches without a Git list, from a reporter that could not run git
	// or predates Git lists, leave it alone.
	ingest(t, SessionMeta{})
	ingest(t, SessionMeta{FilesSource: FilesFromToolCalls})
	if src, got := list(t); src != FilesFromGit || !reflect.DeepEqual(got, want) {
		t.Fatalf("after batches without a list: (%q, %q)", src, got)
	}

	// The newest Git list wins, even an empty one.
	ingest(t, SessionMeta{FilesSource: FilesFromGit, Files: []string{"~/src/demo/b.go"}})
	if _, got := list(t); !reflect.DeepEqual(got, []string{"b.go:-1:-1:0"}) {
		t.Fatalf("replaced list = %q", got)
	}
	ingest(t, SessionMeta{FilesSource: FilesFromGit})
	if src, got := list(t); src != FilesFromGit || got != nil {
		t.Fatalf("empty list = (%q, %q)", src, got)
	}
}

func TestGitFilesValidation(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	long := strings.Repeat("a", maxGitPathBytes+1)
	tooMany := make([]string, MaxGitFiles+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("f%d.go", i)
	}
	tests := []struct {
		name string
		meta SessionMeta
	}{
		{"unknown source", SessionMeta{FilesSource: "svn"}},
		{"files without git source", SessionMeta{Files: []string{"a.go"}}},
		{"files with tool_calls source", SessionMeta{FilesSource: FilesFromToolCalls, Files: []string{"a.go"}}},
		{"blank path", SessionMeta{FilesSource: FilesFromGit, Files: []string{" "}}},
		{"long path", SessionMeta{FilesSource: FilesFromGit, Files: []string{long}}},
		{"too many files", SessionMeta{FilesSource: FilesFromGit, Files: tooMany}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.meta.SessionID, tt.meta.Provider = "s1", "codex"
			_, _, err := s.Ingest(ctx, DefaultAccountID, Machine{ID: "m1"}, []SessionBatch{{Meta: tt.meta}})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	if _, err := s.GetSession(ctx, DefaultAccountID, "m1", "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a rejected batch stored a session: %v", err)
	}
}

func TestParseFilesSource(t *testing.T) {
	for in, want := range map[string]FilesSource{"": FilesFromToolCalls, "tool_calls": FilesFromToolCalls, "git": FilesFromGit} {
		if got, err := ParseFilesSource(in); err != nil || got != want {
			t.Errorf("ParseFilesSource(%q) = (%q, %v), want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"GIT", "svn", " git"} {
		if _, err := ParseFilesSource(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseFilesSource(%q) err = %v, want ErrInvalid", in, err)
		}
	}
}

func TestGitFilesTenancy(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	acct, err := s.CreateAccount(ctx, "other@example.com", "$argon2id$stub", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other := acct.ID
	b := SessionBatch{Meta: SessionMeta{SessionID: "s1", Provider: "codex", CWD: "/w", FilesSource: FilesFromGit, Files: []string{"/w/secret.go"}}}
	if _, _, err := s.Ingest(ctx, DefaultAccountID, Machine{ID: "m1"}, []SessionBatch{b}); err != nil {
		t.Fatal(err)
	}
	// The same ids in another account see none of the first one's files.
	files, err := s.ListFiles(ctx, other, "m1", "s1")
	if err != nil || len(files) != 0 {
		t.Fatalf("other account files = (%+v, %v)", files, err)
	}
	b.Meta.Files = []string{"/w/mine.go"}
	if _, _, err := s.Ingest(ctx, other, Machine{ID: "m1"}, []SessionBatch{b}); err != nil {
		t.Fatal(err)
	}
	for acct, want := range map[string]string{DefaultAccountID: "secret.go", other: "mine.go"} {
		files, err := s.ListFiles(ctx, acct, "m1", "s1")
		if err != nil || len(files) != 1 || files[0].Path != want {
			t.Fatalf("%s files = (%+v, %v), want %s", acct, files, err, want)
		}
	}
	if _, err := s.DeleteAccount(ctx, other); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_git_files WHERE account_id = ?`, other).Scan(&n); err != nil || n != 0 {
		t.Fatalf("deleted account left %d git files (%v)", n, err)
	}
	if files, _ := s.ListFiles(ctx, DefaultAccountID, "m1", "s1"); len(files) != 1 {
		t.Fatalf("deleting another account removed this one's files: %+v", files)
	}
}
