package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
	"github.com/DanBradbury/firekeeper/internal/transcript/transcripttest"
)

const (
	fixtureID   = "0199c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d"
	fixturePath = "testdata/" + fixtureID + ".jsonl"
	malformed   = "testdata/malformed.jsonl"
)

type want struct {
	role  transcript.Role
	text  string
	tool  string
	model string
}

func TestRegistered(t *testing.T) {
	if _, ok := reader.For(transcript.ProviderCursor); !ok {
		t.Fatal("cursor source not registered")
	}
	if _, ok := reader.EnumeratorFor(transcript.ProviderCursor); !ok {
		t.Fatal("cursor source cannot enumerate")
	}
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
		// Only the prompt inside <user_query> is the user's text.
		0: {role: transcript.RoleUser, text: "List the files."},
		1: {role: transcript.RoleMeta, text: "I should list the directory."},
		2: {role: transcript.RoleToolCall, text: "Listing files.\n" + `{"command":"ls","working_directory":"/workspace/demo"}`, tool: "Shell"},
		// The result names only the call id; the tool comes from the call.
		3: {role: transcript.RoleToolResult, text: "README.md", tool: "Shell"},
		4: {role: transcript.RoleToolCall, text: `{"path":"README.md"}`, tool: "read_file"},
		5: {role: transcript.RoleToolResult, text: "# Demo", tool: "read_file"},
		6: {role: transcript.RoleAssistant, text: "There is one file: README.md.", model: "cursor-test"},
		7: {role: transcript.RoleMeta},
		8: {role: transcript.RoleSystem, text: "Connection lost"},
		9: {role: transcript.RoleUser, text: "Thanks"},
		// An unknown block's visible text still reads as the reply.
		10: {role: transcript.RoleAssistant, text: "A block type added later."},
	}
	checkEvents(t, events, expected, fixtureID)
	for _, e := range events {
		if e.TS != nil {
			t.Errorf("seq %d: Cursor records carry no timestamp", e.Seq)
		}
	}
}

func TestReadMalformed(t *testing.T) {
	events, _, err := (&Source{}).Read(malformed, 0)
	if err != nil {
		t.Fatal(err)
	}
	expected := []want{
		0:  {role: transcript.RoleMeta},
		1:  {role: transcript.RoleMeta},
		2:  {role: transcript.RoleMeta},
		3:  {role: transcript.RoleMeta},
		4:  {role: transcript.RoleAssistant, text: "plain string"},
		5:  {role: transcript.RoleUser},
		6:  {role: transcript.RoleToolResult},
		7:  {role: transcript.RoleMeta},
		8:  {role: transcript.RoleMeta},
		9:  {role: transcript.RoleSystem, text: "System note."},
		10: {role: transcript.RoleUser, text: "no tags here"},
	}
	// The file name is not a session id, so events carry none.
	checkEvents(t, events, expected, "")
	var quoted string
	if json.Unmarshal(events[0].Raw, &quoted) != nil {
		t.Error("seq 0: invalid JSON line should be kept as a JSON string in raw")
	}
}

func checkEvents(t *testing.T, events []transcript.Event, expected []want, sessionID string) {
	t.Helper()
	if len(events) != len(expected) {
		t.Fatalf("got %d events, want %d", len(events), len(expected))
	}
	for i, e := range events {
		w := expected[i]
		if err := e.Validate(); err != nil {
			t.Errorf("seq %d: %v", i, err)
		}
		if e.Seq != int64(i) || e.Role != w.role || e.Text != w.text || deref(e.ToolName) != w.tool || deref(e.Model) != w.model {
			t.Errorf("seq %d: got seq %d role %s tool %q model %q text len %d; want role %s tool %q model %q text len %d",
				i, e.Seq, e.Role, deref(e.ToolName), deref(e.Model), len(e.Text), w.role, w.tool, w.model, len(w.text))
		}
		if e.SessionID != sessionID || e.Provider != transcript.ProviderCursor {
			t.Errorf("seq %d: session %q provider %q", i, e.SessionID, e.Provider)
		}
		if e.Tokens != (transcript.Tokens{}) {
			t.Errorf("seq %d: Cursor records carry no token counts", i)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestSeqStability(t *testing.T) {
	transcripttest.RunSeqStability(t, &Source{}, []transcripttest.SeqCase{
		{Name: "transcript", Path: fixturePath},
		{Name: "malformed", Path: malformed},
	})
}

// A split read must recover tool names from calls before the offset.
func TestReadFromOffsetRestoresToolNames(t *testing.T) {
	source := &Source{}
	data, _ := os.ReadFile(fixturePath)
	offset := 0
	for range 3 {
		offset += strings.IndexByte(string(data[offset:]), '\n') + 1
	}
	tail, _, err := source.Read(fixturePath, int64(offset))
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) == 0 || tail[0].Seq != 3 || deref(tail[0].ToolName) != "Shell" {
		t.Fatal("tool result read from an offset lost its tool name")
	}
}

func TestReadIncompleteTrailingLine(t *testing.T) {
	data, _ := os.ReadFile(fixturePath)
	lines := strings.SplitAfter(string(data), "\n")
	path := filepath.Join(t.TempDir(), filepath.Base(fixturePath))
	complete := lines[0] + lines[1]
	if err := os.WriteFile(path, []byte(complete+lines[2][:10]), 0o600); err != nil {
		t.Fatal(err)
	}
	events, offset, err := (&Source{}).Read(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || offset != int64(len(complete)) {
		t.Fatalf("got %d events at offset %d; want 2 at %d", len(events), offset, len(complete))
	}
	if err := os.WriteFile(path, []byte(complete+lines[2]), 0o600); err != nil {
		t.Fatal(err)
	}
	events, _, err = (&Source{}).Read(path, offset)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Seq != 2 || events[0].Role != transcript.RoleToolCall {
		t.Fatalf("resumed read gave %d events", len(events))
	}
}

func TestReadErrors(t *testing.T) {
	source := &Source{}
	dir := t.TempDir()
	if _, _, err := source.Read(filepath.Join(dir, "missing.jsonl"), 0); err == nil || strings.Contains(err.Error(), dir) {
		t.Errorf("missing file: %v", err)
	}
	if _, _, err := source.Read(fixturePath, -1); err == nil {
		t.Error("negative offset accepted")
	}
	if _, _, err := source.Read(fixturePath, 5); err == nil || !strings.Contains(err.Error(), "line boundary") {
		t.Errorf("mid-line offset: %v", err)
	}
	if _, _, err := source.Read(fixturePath, 1<<30); err == nil || !strings.Contains(err.Error(), "past end") {
		t.Errorf("offset past end: %v", err)
	}
}

func TestReadTruncatesLongToolOutput(t *testing.T) {
	long := strings.Repeat("é", maxToolText)
	line, _ := json.Marshal(map[string]any{"role": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "t", "content": long},
	}}})
	path := filepath.Join(t.TempDir(), fixtureID+".jsonl")
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	events, _, err := (&Source{}).Read(path, 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("got %d events, %v", len(events), err)
	}
	if text := events[0].Text; len(text) > maxToolText+64 || !strings.Contains(text, "[truncated ") {
		t.Fatalf("tool output not truncated: %d bytes", len(text))
	}
	if !strings.Contains(string(events[0].Raw), long) {
		t.Fatal("raw should keep the full output")
	}
}

func TestLocate(t *testing.T) {
	dir := t.TempDir()
	source := &Source{Dir: dir}
	transcripts := filepath.Join(dir, "projects", "home-me-demo", "agent-transcripts")
	path := filepath.Join(transcripts, fixtureID, fixtureID+".jsonl")
	write(t, path, "{}\n")
	// Subagent transcripts are never returned.
	write(t, filepath.Join(transcripts, fixtureID, "subagents", "1a2b3c4d-0000-4000-8000-000000000001.jsonl"), "{}\n")

	for _, tt := range []struct {
		name string
		meta session.Meta
		want []string
	}{
		{"by id", session.Meta{ID: fixtureID, Provider: "cursor"}, []string{path}},
		{"rollout path", session.Meta{ID: fixtureID, RolloutPath: path}, []string{path}},
		{"stale rollout path", session.Meta{ID: fixtureID, RolloutPath: filepath.Join(dir, "gone", fixtureID+".jsonl")}, []string{path}},
		{"other provider", session.Meta{ID: fixtureID, Provider: "claude"}, nil},
		{"invalid id", session.Meta{ID: "../" + fixtureID}, nil},
		{"glob id", session.Meta{ID: "*"}, nil},
		{"subagent id", session.Meta{ID: "1a2b3c4d-0000-4000-8000-000000000001"}, nil},
	} {
		got, err := source.Locate(tt.meta)
		if err != nil || strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("%s: got %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}
}

func TestSourceHonorsCursorConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CURSOR_CONFIG_DIR", dir)
	path := filepath.Join(dir, "projects", "p", "agent-transcripts", fixtureID+".jsonl")
	write(t, path, "{}\n")
	got, err := (&Source{}).Locate(session.Meta{ID: fixtureID})
	if err != nil || len(got) != 1 || got[0] != path {
		t.Fatalf("got %q, %v", got, err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
