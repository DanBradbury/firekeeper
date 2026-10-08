package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/redact"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	_ "github.com/DanBradbury/firekeeper/internal/transcript/claude"
	_ "github.com/DanBradbury/firekeeper/internal/transcript/codex"
	_ "github.com/DanBradbury/firekeeper/internal/transcript/copilot"
)

// fixtureHome is the home directory the files fixtures are written under;
// redaction rewrites it to "~" as the reporter does.
const fixtureHome = "/home/fixture"

// ingestFixture reads a provider fixture through its real transcript
// source, redacts each event and the cwd as the reporter does, and ingests
// the result in one batch.
func ingestFixture(t *testing.T, s *Store, provider transcript.Provider, path string) SessionBatch {
	t.Helper()
	src, ok := transcript.For(provider)
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
