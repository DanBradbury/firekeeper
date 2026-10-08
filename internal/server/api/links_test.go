package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

func TestValidateFileLink(t *testing.T) {
	for tmpl, ok := range map[string]bool{
		"https://github.com/me/{project}/blob/{ref}/{path}": true,
		"http://git.local/{project}/-/blob/{commit}/{path}": true,
		"https://github.com/me/{project}/blob/{ref}/":       false, // no {path}
		"github.com/me/{project}/{path}":                    false, // no scheme
		"javascript:alert(1)//{path}":                       false,
		"file:///{path}":                                    false,
	} {
		if err := ValidateFileLink(tmpl); (err == nil) != ok {
			t.Errorf("ValidateFileLink(%q) = %v, want ok=%v", tmpl, err, ok)
		}
	}
}

func TestFileLink(t *testing.T) {
	const tmpl = "https://host.example/{project}/blob/{ref}/{path}"
	se := store.Session{Project: "demo", Branch: "feature/x", Commit: "abc123"}
	tests := []struct {
		name string
		tmpl string
		se   store.Session
		f    store.SessionFile
		want string
	}{
		{"commit preferred", tmpl, se, store.SessionFile{Path: "src/a b.go"}, "https://host.example/demo/blob/abc123/src/a%20b.go"},
		{"branch fallback", tmpl, store.Session{Project: "demo", Branch: "feature/x"}, store.SessionFile{Path: "a.go"}, "https://host.example/demo/blob/feature%2Fx/a.go"},
		{"no ref", tmpl, store.Session{Project: "demo"}, store.SessionFile{Path: "a.go"}, ""},
		{"no project", tmpl, store.Session{Commit: "abc"}, store.SessionFile{Path: "a.go"}, ""},
		{"absolute path", tmpl, se, store.SessionFile{Path: "/etc/hosts", Absolute: true}, ""},
		{"redacted path", tmpl, se, store.SessionFile{Path: "keys/[REDACTED:api_key].txt"}, ""},
		{"no template", "", se, store.SessionFile{Path: "a.go"}, ""},
		{"unused fields ignored", "https://h.example/x/{path}", store.Session{}, store.SessionFile{Path: "a.go"}, "https://h.example/x/a.go"},
	}
	for _, tt := range tests {
		if got := fileLink(tt.tmpl, tt.se, tt.f); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestGetSessionFiles(t *testing.T) {
	s := newStore(t)
	raw := func(p string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]string{"file_path": p}},
		}}})
		return b
	}
	b := store.SessionBatch{
		Meta: store.SessionMeta{SessionID: "s1", Provider: "claude", CWD: "~/src/demo", Project: "demo", Branch: "main", Commit: "abc123"},
		Events: []transcript.Event{
			{Provider: transcript.ProviderClaude, Seq: 0, Role: transcript.RoleToolCall, Raw: raw("~/src/demo/main.go")},
			{Provider: transcript.ProviderClaude, Seq: 1, Role: transcript.RoleToolCall, Raw: raw("/etc/hosts")},
		},
	}
	if _, _, err := s.Ingest(context.Background(), store.DefaultAccountID, store.Machine{ID: "m1"}, []store.SessionBatch{b}); err != nil {
		t.Fatal(err)
	}
	ingestSeed(t, s, seed{machine: "m1", session: "empty", provider: "codex"})

	h := Handler(s, WithFileLink("https://host.example/me/{project}/blob/{ref}/{path}"))
	var got struct {
		store.Session
		Files []store.SessionFile `json:"files"`
	}
	if code := get(t, h, "/v1/sessions/m1:s1", &got); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if got.Commit != "abc123" || got.Branch != "main" {
		t.Fatalf("git context = (%q, %q)", got.Branch, got.Commit)
	}
	want := []store.SessionFile{
		{Path: "/etc/hosts", Absolute: true, FirstSeq: 1, LastSeq: 1, Changes: 1},
		{Path: "main.go", FirstSeq: 0, LastSeq: 0, Changes: 1, URL: "https://host.example/me/demo/blob/abc123/main.go"},
	}
	if len(got.Files) != len(want) {
		t.Fatalf("files = %+v", got.Files)
	}
	for i := range want {
		if got.Files[i] != want[i] {
			t.Fatalf("file %d = %+v, want %+v", i, got.Files[i], want[i])
		}
	}

	// A session with no changed files serves an empty list, not null.
	var rawResp map[string]json.RawMessage
	if code := get(t, h, "/v1/sessions/m1:empty", &rawResp); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if string(rawResp["files"]) != "[]" {
		t.Fatalf("files = %s, want []", rawResp["files"])
	}
}
