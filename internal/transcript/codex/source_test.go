package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/transcripttest"
)

const (
	fixtureID   = "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	fixturePath = "testdata/rollout-2026-01-02T03-04-05-" + fixtureID + ".jsonl"
	malformed   = "testdata/malformed.jsonl"
)

type want struct {
	role   transcript.Role
	text   string
	tool   string
	model  string
	tokens transcript.Tokens
}

func TestReadRollout(t *testing.T) {
	events, end, err := (&Source{}).Read(fixturePath, 0)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(fixturePath)
	if end != info.Size() {
		t.Fatalf("end offset = %d, want file size %d", end, info.Size())
	}
	expected := []want{
		0:  {role: transcript.RoleMeta},
		1:  {role: transcript.RoleMeta},
		2:  {role: transcript.RoleSystem, text: "Follow the sandbox rules."},
		3:  {role: transcript.RoleUser, text: "<environment_context>cwd=/work/demo</environment_context>"},
		4:  {role: transcript.RoleMeta},
		5:  {role: transcript.RoleMeta, model: "gpt-test"},
		6:  {role: transcript.RoleUser, text: "List the files."},
		7:  {role: transcript.RoleMeta},
		8:  {role: transcript.RoleAssistant, text: "Listing files.", model: "gpt-test"},
		9:  {role: transcript.RoleToolCall, text: "ls", tool: "exec"},
		10: {role: transcript.RoleMeta},
		11: {role: transcript.RoleMeta},
		12: {role: transcript.RoleToolResult, text: "exit 0\nREADME.md", tool: "exec"},
		13: {role: transcript.RoleMeta, model: "gpt-test", tokens: transcript.Tokens{Input: 200, Output: 40, Cache: 1000}},
		14: {role: transcript.RoleToolCall, text: `{"question":"Continue?"}`, tool: "request_user_input_async"},
		15: {role: transcript.RoleToolResult, text: "yes", tool: "request_user_input_async"},
		16: {role: transcript.RoleMeta},
		17: {role: transcript.RoleAssistant, text: "There is one file: README.md.", model: "gpt-test"},
		18: {role: transcript.RoleMeta, model: "gpt-test", tokens: transcript.Tokens{Input: 300, Output: 25, Cache: 1200}},
		19: {role: transcript.RoleMeta},
	}
	checkEvents(t, events, expected, fixtureID)
	for _, e := range events {
		if e.TS == nil {
			t.Errorf("seq %d: missing timestamp", e.Seq)
		}
	}
}

func TestReadMalformed(t *testing.T) {
	events, _, err := (&Source{}).Read(malformed, 0)
	if err != nil {
		t.Fatal(err)
	}
	expected := []want{
		0: {role: transcript.RoleMeta},
		1: {role: transcript.RoleMeta},
		2: {role: transcript.RoleUser, text: "hello"},
		3: {role: transcript.RoleMeta},
		4: {role: transcript.RoleMeta},
		5: {role: transcript.RoleMeta},
		6: {role: transcript.RoleMeta},
		7: {role: transcript.RoleMeta},
		8: {role: transcript.RoleMeta},
		9: {role: transcript.RoleToolResult},
	}
	// The file name carries no thread id, so session_meta supplies it.
	checkEvents(t, events, expected, "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c")
	if events[2].TS != nil {
		t.Error("seq 2: unparseable timestamp should be null")
	}
	var quoted string
	if json.Unmarshal(events[7].Raw, &quoted) != nil {
		t.Error("seq 7: invalid JSON line should be kept as a JSON string in raw")
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
		if e.Seq != int64(i) || e.Role != w.role || e.Text != w.text || deref(e.ToolName) != w.tool ||
			deref(e.Model) != w.model || e.Tokens != w.tokens {
			t.Errorf("seq %d: got seq %d role %s tool %q model %q tokens %+v text len %d; want role %s tool %q model %q tokens %+v text len %d",
				i, e.Seq, e.Role, deref(e.ToolName), deref(e.Model), e.Tokens, len(e.Text), w.role, w.tool, w.model, w.tokens, len(w.text))
		}
		if e.SessionID != sessionID || e.Provider != transcript.ProviderCodex {
			t.Errorf("seq %d: session %q provider %q", i, e.SessionID, e.Provider)
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
		{Name: "rollout", Path: fixturePath},
		{Name: "malformed", Path: malformed},
	})
}

// A split read must recover the model and tool names set before the offset;
// the stability helper does not compare models.
func TestReadFromOffsetRestoresState(t *testing.T) {
	source := &Source{}
	whole, _, err := source.Read(fixturePath, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(fixturePath)
	offset := 0
	for range 10 {
		offset += strings.IndexByte(string(data[offset:]), '\n') + 1
	}
	tail, _, err := source.Read(fixturePath, int64(offset))
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != len(whole)-10 {
		t.Fatalf("got %d events from offset, want %d", len(tail), len(whole)-10)
	}
	for i, e := range tail {
		w := whole[10+i]
		if e.Seq != w.Seq || deref(e.Model) != deref(w.Model) || deref(e.ToolName) != deref(w.ToolName) || e.SessionID != w.SessionID {
			t.Errorf("seq %d differs between split and whole reads", w.Seq)
		}
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
	// The writer finishes the line; the next read picks it up as seq 2.
	if err := os.WriteFile(path, []byte(complete+lines[2]), 0o600); err != nil {
		t.Fatal(err)
	}
	events, _, err = (&Source{}).Read(path, offset)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Seq != 2 || events[0].Role != transcript.RoleSystem {
		t.Fatalf("resumed read gave %d events", len(events))
	}
}

func TestReadBadOffsets(t *testing.T) {
	info, _ := os.Stat(fixturePath)
	for name, offset := range map[string]int64{
		"negative":      -1,
		"past end":      info.Size() + 1,
		"mid line":      5,
		"truncated end": info.Size() - 1,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := (&Source{}).Read(fixturePath, offset); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	_, _, err := (&Source{}).Read(filepath.Join(t.TempDir(), "missing.jsonl"), 0)
	if err == nil || strings.Contains(err.Error(), string(filepath.Separator)) {
		t.Fatalf("missing file error = %v; want an error without a path", err)
	}
}

func TestToolOutputTruncated(t *testing.T) {
	big := strings.Repeat("é", maxToolText) // two bytes per rune
	line, _ := json.Marshal(map[string]any{
		"timestamp": "2026-01-02T03:04:05.000Z",
		"type":      "response_item",
		"payload":   map[string]any{"type": "function_call_output", "call_id": "c", "output": big},
	})
	path := filepath.Join(t.TempDir(), "rollout-"+fixtureID+".jsonl")
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	events, _, err := (&Source{}).Read(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	text := events[0].Text
	kept, marker, ok := strings.Cut(text, "\n[truncated ")
	if !ok || len(kept) != maxToolText || marker != "65536 bytes]" {
		t.Fatalf("kept %d bytes, marker %q", len(kept), marker)
	}
	if !strings.Contains(string(events[0].Raw), big) {
		t.Fatal("raw should keep the full output")
	}
}

func TestLocate(t *testing.T) {
	home := t.TempDir()
	dayDir := filepath.Join(home, "sessions", "2026", "01", "02")
	archived := filepath.Join(home, "archived_sessions")
	for _, dir := range []string{dayDir, archived} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	live := filepath.Join(dayDir, filepath.Base(fixturePath))
	old := filepath.Join(archived, "rollout-2025-12-01T00-00-00-0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5d.jsonl")
	other := filepath.Join(dayDir, "rollout-2026-01-02T00-00-00-0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5e.jsonl")
	for _, path := range []string{live, old, other} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source := &Source{CodexHome: home}
	tests := []struct {
		name string
		meta session.Meta
		want []string
	}{
		{"open rollout from discovery", session.Meta{Provider: "codex", ID: fixtureID, RolloutPath: live}, []string{live}},
		{"search by id", session.Meta{Provider: "codex", ID: fixtureID}, []string{live}},
		{"archived", session.Meta{ID: "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5d"}, []string{old}},
		{"stale rollout path falls back to search", session.Meta{ID: fixtureID, RolloutPath: filepath.Join(home, "gone", filepath.Base(fixturePath))}, []string{live}},
		{"rollout path for another id is ignored", session.Meta{ID: fixtureID, RolloutPath: other}, []string{live}},
		{"no transcript yet", session.Meta{ID: "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5f"}, nil},
		{"no id", session.Meta{Provider: "codex"}, nil},
		{"other provider", session.Meta{Provider: "copilot", ID: fixtureID}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := source.Locate(tt.meta)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("Locate = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLocateHonorsCodexHome(t *testing.T) {
	home := t.TempDir()
	dayDir := filepath.Join(home, "sessions", "2026", "01", "02")
	if err := os.MkdirAll(dayDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dayDir, filepath.Base(fixturePath))
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	got, err := (&Source{Home: t.TempDir()}).Locate(session.Meta{ID: fixtureID})
	if err != nil || len(got) != 1 || got[0] != path {
		t.Fatalf("Locate = %v, %v", got, err)
	}
}

func TestRegistered(t *testing.T) {
	if _, ok := transcript.For(transcript.ProviderCodex); !ok {
		t.Fatal("codex source not registered")
	}
}
