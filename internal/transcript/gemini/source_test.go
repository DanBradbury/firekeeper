package gemini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
	"github.com/DanBradbury/firekeeper/internal/transcript/transcripttest"
)

const (
	fixtureDir  = "testdata/runtime"
	fixtureID   = "0199c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d"
	fixturePath = fixtureDir + "/tmp/demo/chats/session-2026-10-08T00-00-0199c3d4.jsonl"
	legacyID    = "1a2b3c4d-0000-4000-8000-000000000001"
	legacyPath  = fixtureDir + "/tmp/7542a27b500a3fbe093358d093a035e71a027fbcede398ac18d18a8174935c2b/chats/session-2026-03-01T10-00-1a2b3c4d.json"
	subagentID  = "5e6f7a8b-0000-4000-8000-000000000002"
	malformed   = "testdata/malformed.jsonl"
	model       = "gemini-3-pro-test"
)

type want struct {
	role   transcript.Role
	text   string
	tool   string
	model  string
	tokens transcript.Tokens
}

func TestReadTranscript(t *testing.T) {
	events, end, err := (&Source{}).Read(fixturePath, 0)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(fixturePath)
	if end != info.Size() {
		t.Fatalf("end offset = %d, want file size %d", end, info.Size())
	}
	expected := []want{
		0: {role: transcript.RoleMeta},
		1: {role: transcript.RoleUser, text: "List the files."},
		2: {role: transcript.RoleMeta},
		// Thinking alone is meta; the response's usage arrives with it.
		3: {role: transcript.RoleMeta, text: "Planning\nLook at the tree.", model: model, tokens: transcript.Tokens{Input: 20, Output: 15, Cache: 100}},
		4: {role: transcript.RoleMeta},
		// The message is written again with its tool call; tokens are not
		// counted twice.
		5:  {role: transcript.RoleToolCall, text: `{"dir_path":"."}`, tool: "list_directory", model: model},
		6:  {role: transcript.RoleToolResult, text: "README.md", tool: "list_directory"},
		7:  {role: transcript.RoleAssistant, text: "I'll fix the typo.", model: model},
		8:  {role: transcript.RoleMeta, model: model, tokens: transcript.Tokens{Input: 54, Output: 20, Cache: 150}},
		9:  {role: transcript.RoleToolCall, text: `{"file_path":"README.md","old_string":"teh","new_string":"the"}`, tool: "replace", model: model},
		10: {role: transcript.RoleToolResult, text: "File not found.", tool: "replace"},
		11: {role: transcript.RoleAssistant, text: "Done editing.", model: model},
		12: {role: transcript.RoleSystem, text: "Request cancelled."},
		// What the user typed, not the expanded @file content.
		13: {role: transcript.RoleUser, text: "/review @README.md"},
		14: {role: transcript.RoleSystem, text: "<session_context>\nWorkspace: /work/demo\n</session_context>"},
		15: {role: transcript.RoleMeta},
		16: {role: transcript.RoleMeta},
		17: {role: transcript.RoleMeta},
		// A response for a call whose result was already emitted.
		18: {role: transcript.RoleMeta},
		19: {role: transcript.RoleMeta},
		20: {role: transcript.RoleSystem, text: "Quota exceeded."},
	}
	checkEvents(t, events, expected, fixtureID)
	for _, seq := range []int{0, 1, 3, 5, 6, 13, 17, 20} {
		if events[seq].TS == nil {
			t.Errorf("seq %d: missing timestamp", seq)
		}
	}
	if got, want := events[17].TS, time.Date(2026, 10, 8, 0, 5, 0, 0, time.UTC); got == nil || !got.Equal(want) {
		t.Errorf("$set timestamp = %v, want lastUpdated %v", got, want)
	}
	for _, seq := range []int{15, 16} {
		if events[seq].TS != nil {
			t.Errorf("seq %d: patch and rewind records carry no time", seq)
		}
	}
	var raw string
	if json.Unmarshal(events[19].Raw, &raw) != nil || raw != `{"id": broken` {
		t.Error("an undecodable line must be kept, quoted, in raw")
	}
	if got := transcript.TouchedFiles(events[9]); len(got) != 1 || got[0] != "README.md" {
		t.Errorf("TouchedFiles(replace call) = %q", got)
	}
}

func checkEvents(t *testing.T, events []transcript.Event, expected []want, sessionID string) {
	t.Helper()
	if len(events) != len(expected) {
		t.Fatalf("got %d events, want %d", len(events), len(expected))
	}
	for i, e := range events {
		w := expected[i]
		if e.Seq != int64(i) || e.Provider != transcript.ProviderGemini || e.SessionID != sessionID {
			t.Errorf("event %d: seq %d provider %q session %q", i, e.Seq, e.Provider, e.SessionID)
		}
		if e.Role != w.role || e.Text != w.text {
			t.Errorf("seq %d: role %q text %q, want role %q text %q", i, e.Role, e.Text, w.role, w.text)
		}
		if got := deref(e.ToolName); got != w.tool {
			t.Errorf("seq %d: tool %q, want %q", i, got, w.tool)
		}
		if got := deref(e.Model); got != w.model {
			t.Errorf("seq %d: model %q, want %q", i, got, w.model)
		}
		if e.Tokens != w.tokens {
			t.Errorf("seq %d: tokens %+v, want %+v", i, e.Tokens, w.tokens)
		}
		if err := e.Validate(); err != nil {
			t.Errorf("seq %d: %v", i, err)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestReadSeqStability(t *testing.T) {
	transcripttest.RunSeqStability(t, &Source{}, []transcripttest.SeqCase{
		{Name: "session", Path: fixturePath},
		{Name: "malformed", Path: malformed},
	})
}

// A read that starts after a message's first copy must still emit only what
// later copies add.
func TestReadFromEveryOffset(t *testing.T) {
	whole, _, err := (&Source{}).Read(fixturePath, 0)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var offset int64
	for seq, line := range strings.SplitAfter(string(body), "\n") {
		if line == "" {
			break
		}
		tail, _, err := (&Source{}).Read(fixturePath, offset)
		if err != nil {
			t.Fatalf("read from seq %d: %v", seq, err)
		}
		if len(tail) != len(whole)-seq {
			t.Fatalf("read from seq %d returned %d events, want %d", seq, len(tail), len(whole)-seq)
		}
		for i, e := range tail {
			w := whole[seq+i]
			if e.Seq != w.Seq || e.Role != w.Role || e.Text != w.Text || e.Tokens != w.Tokens || deref(e.ToolName) != deref(w.ToolName) || deref(e.Model) != deref(w.Model) {
				t.Fatalf("read from seq %d: event %d differs from the whole read", seq, w.Seq)
			}
		}
		offset += int64(len(line))
	}
}

func TestReadOffsets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-2026-10-08T00-00-0199c3d4.jsonl")
	head := `{"sessionId":"` + fixtureID + `","projectHash":"h","startTime":"2026-10-08T00:00:00Z"}` + "\n"
	partial := `{"id":"u1","type":"user","content":"hel`
	if err := os.WriteFile(path, []byte(head+partial), 0o600); err != nil {
		t.Fatal(err)
	}
	source := &Source{}
	events, offset, err := source.Read(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || offset != int64(len(head)) {
		t.Fatalf("a line without a newline must wait: %d events, offset %d", len(events), offset)
	}
	if err := os.WriteFile(path, []byte(head+partial+`lo"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	events, next, err := source.Read(path, offset)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Seq != 1 || events[0].Role != transcript.RoleUser || events[0].Text != "hello" || events[0].SessionID != fixtureID {
		t.Fatalf("completed line: %+v", events)
	}
	if events, again, err := source.Read(path, next); err != nil || len(events) != 0 || again != next {
		t.Fatalf("read at end: %d events, offset %d, err %v", len(events), again, err)
	}
	if _, _, err := source.Read(path, 5); err == nil || !strings.Contains(err.Error(), "line boundary") {
		t.Errorf("mid-line offset: err %v", err)
	}
	if _, _, err := source.Read(path, next+10); err == nil || !strings.Contains(err.Error(), "past end") {
		t.Errorf("offset past end: err %v", err)
	}
	if _, _, err := source.Read(path, -1); err == nil {
		t.Error("negative offset must fail")
	}
	_, _, err = source.Read(filepath.Join(dir, "missing.jsonl"), 0)
	if err == nil || strings.Contains(err.Error(), dir) {
		t.Errorf("missing file error must not name the path: %v", err)
	}
}

func TestReadLegacy(t *testing.T) {
	source := &Source{}
	events, end, err := source.Read(legacyPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(legacyPath)
	if end != info.Size() {
		t.Fatalf("end offset = %d, want file size %d", end, info.Size())
	}
	checkEvents(t, events, []want{
		{role: transcript.RoleUser, text: "Write hello.txt"},
		// Legacy files hold tool results only in the call record.
		{role: transcript.RoleToolCall, text: "{\"file_path\":\"hello.txt\",\"content\":\"hi\"}\nCreated hello.txt", tool: "write_file", model: "gemini-2.5-pro", tokens: transcript.Tokens{Input: 50, Output: 5}},
		{role: transcript.RoleAssistant, text: "Created hello.txt.", model: "gemini-2.5-pro"},
	}, legacyID)
	if !json.Valid(events[1].Raw) || strings.Contains(string(events[1].Raw), "\n") {
		t.Error("legacy raw must be the compact message record")
	}
	if got := transcript.TouchedFiles(events[1]); len(got) != 1 || got[0] != "hello.txt" {
		t.Errorf("TouchedFiles(legacy write_file) = %q", got)
	}
	if again, offset, err := source.Read(legacyPath, end); err != nil || len(again) != 0 || offset != end {
		t.Fatalf("read at end: %d events, offset %d, err %v", len(again), offset, err)
	}
	// Any other offset re-reads the document; seqs stay the same.
	again, _, err := source.Read(legacyPath, 3)
	if err != nil || len(again) != len(events) || again[2].Seq != 2 {
		t.Fatalf("re-read: %d events, err %v", len(again), err)
	}

	corrupt := filepath.Join(t.TempDir(), "session-x.json")
	if err := os.WriteFile(corrupt, []byte(`{"sessionId":"x","messages":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.Read(corrupt, 0); err == nil {
		t.Error("a truncated legacy document must fail")
	}
}

func TestReadMalformed(t *testing.T) {
	events, _, err := (&Source{}).Read(malformed, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 10 {
		t.Fatalf("got %d events; every line must be kept", len(events))
	}
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Errorf("seq %d: %v", e.Seq, err)
		}
		if !json.Valid(e.Raw) {
			t.Errorf("seq %d: raw is not valid JSON", e.Seq)
		}
	}
	if events[1].Role != transcript.RoleUser || events[1].Text != "" {
		t.Errorf("non-text content: role %q text %q", events[1].Role, events[1].Text)
	}
	// Negative counts are clamped.
	if e := events[3]; e.Role != transcript.RoleToolCall || deref(e.ToolName) != "run_shell_command" || e.Tokens != (transcript.Tokens{Cache: 9}) {
		t.Errorf("tool calls without ids or args: %+v", e)
	}
	if e := events[5]; e.Role != transcript.RoleToolResult || e.Text != `{"status":200}` || deref(e.ToolName) != "web_fetch" {
		t.Errorf("response without id: role %q text %q", e.Role, e.Text)
	}
	for _, seq := range []int{0, 2, 4, 6, 7, 8, 9} {
		if events[seq].Role != transcript.RoleMeta {
			t.Errorf("seq %d: role %q, want meta", seq, events[seq].Role)
		}
	}
}

func TestTruncateLongToolOutput(t *testing.T) {
	long := strings.Repeat("é", maxToolText)
	line, _ := json.Marshal(map[string]any{"id": "u1", "type": "user", "content": []any{map[string]any{
		"functionResponse": map[string]any{"id": "c1", "name": "read_file", "response": map[string]any{"output": long}},
	}}})
	e := newParser(false).parse(line)
	if !strings.HasSuffix(e.Text, "[truncated 65536 bytes]") || len(e.Text) > maxToolText+64 {
		t.Fatalf("tool output not truncated: %d bytes", len(e.Text))
	}
	if !strings.Contains(string(e.Raw), long) {
		t.Error("raw must keep the full output")
	}
}

func TestLocate(t *testing.T) {
	source := &Source{Dir: fixtureDir}
	abs, _ := filepath.Abs(fixturePath)
	tests := []struct {
		name string
		meta session.Meta
		want string
	}{
		{"rollout path", session.Meta{ID: fixtureID, Provider: "gemini", RolloutPath: abs}, abs},
		{"search by id", session.Meta{ID: fixtureID}, fixturePath},
		{"stale rollout path", session.Meta{ID: fixtureID, RolloutPath: legacyPath}, fixturePath},
		{"legacy", session.Meta{ID: legacyID, Provider: "gemini"}, legacyPath},
		{"id prefix only", session.Meta{ID: "0199c3d4-0000-4000-8000-000000000000"}, ""},
		{"subagent", session.Meta{ID: subagentID}, ""},
		{"other provider", session.Meta{ID: fixtureID, Provider: "kimi"}, ""},
		{"glob pattern", session.Meta{ID: "0199c3d4*"}, ""},
		{"path traversal", session.Meta{ID: "../demo"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths, err := source.Locate(tt.meta)
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == "" {
				if len(paths) != 0 {
					t.Fatalf("got %q, want none", paths)
				}
				return
			}
			if len(paths) != 1 || filepath.Clean(paths[0]) != filepath.Clean(tt.want) {
				t.Fatalf("got %q, want %q", paths, tt.want)
			}
		})
	}
}

// A resumed legacy session is migrated to a .jsonl beside the .json, and
// Locate must pick the log Gemini CLI is now writing.
func TestLocatePrefersJSONL(t *testing.T) {
	dir := t.TempDir()
	chats := filepath.Join(dir, "tmp", "proj", "chats")
	if err := os.MkdirAll(chats, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(chats, "session-2026-03-01T10-00-1a2b3c4d.json")
	migrated := legacy + "l"
	if err := os.WriteFile(legacy, []byte(`{"sessionId":"`+legacyID+`","projectHash":"h","messages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(migrated, []byte(`{"sessionId":"`+legacyID+`","projectHash":"h"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The .json is newer, but the .jsonl still wins.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(migrated, old, old); err != nil {
		t.Fatal(err)
	}
	paths, err := (&Source{Dir: dir}).Locate(session.Meta{ID: legacyID})
	if err != nil || len(paths) != 1 || paths[0] != migrated {
		t.Fatalf("got %q, %v; want the .jsonl", paths, err)
	}
}

func TestRegistered(t *testing.T) {
	source, ok := reader.For(transcript.ProviderGemini)
	if !ok {
		t.Fatal("gemini reader not registered")
	}
	if _, ok := source.(reader.Enumerator); !ok {
		t.Fatal("gemini reader must support backfill")
	}
}
