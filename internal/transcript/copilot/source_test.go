package copilot

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
	fixtureID   = "0199b2c3-d4e5-7f60-8a9b-1c2d3e4f5a6b"
	fixtureHome = "testdata"
	fixturePath = "testdata/session-state/" + fixtureID + "/events.jsonl"
	malformed   = "testdata/malformed.jsonl"
)

type want struct {
	role   transcript.Role
	text   string
	tool   string
	model  string
	tokens transcript.Tokens
}

func TestReadEvents(t *testing.T) {
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
		1:  {role: transcript.RoleMeta, model: "gpt-test"},
		2:  {role: transcript.RoleSystem, text: "Follow the sandbox rules."},
		3:  {role: transcript.RoleUser, text: "List the files."},
		4:  {role: transcript.RoleMeta},
		5:  {role: transcript.RoleAssistant, text: "Listing files.", model: "gpt-test"},
		6:  {role: transcript.RoleMeta},
		7:  {role: transcript.RoleMeta},
		8:  {role: transcript.RoleToolCall, text: `{"command":"ls","description":"List files"}`, tool: "bash", model: "gpt-test"},
		9:  {role: transcript.RoleToolResult, text: "README.md", tool: "bash", model: "gpt-test"},
		10: {role: transcript.RoleAssistant, model: "gpt-test"},
		// The failed call never started; its name comes from toolRequests.
		11: {role: transcript.RoleToolResult, text: "file not found", tool: "view", model: "gpt-test"},
		12: {role: transcript.RoleMeta, model: "gpt-mini-test", tokens: transcript.Tokens{Input: 150, Output: 14, Cache: 20}},
		13: {role: transcript.RoleAssistant, text: "There is one file: README.md.", model: "gpt-test"},
		14: {role: transcript.RoleMeta},
		15: {role: transcript.RoleMeta, tokens: transcript.Tokens{Input: 300, Output: 65, Cache: 1200}},
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
		4: {role: transcript.RoleAssistant},
		5: {role: transcript.RoleToolCall, text: "raw args"},
		6: {role: transcript.RoleMeta},
		7: {role: transcript.RoleMeta},
		8: {role: transcript.RoleMeta},
		9: {role: transcript.RoleToolResult},
	}
	// The path has no session directory, so session.start supplies the id.
	checkEvents(t, events, expected, "0199b2c3-d4e5-7f60-8a9b-1c2d3e4f5a6c")
	if events[2].TS != nil {
		t.Error("seq 2: unparseable timestamp should be null")
	}
	var quoted string
	if json.Unmarshal(events[7].Raw, &quoted) != nil {
		t.Error("seq 7: invalid JSON line should be kept as a JSON string in raw")
	}
}

func TestSessionIDFromWorkspace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "copied-session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "0199b2c3-d4e5-7f60-8a9b-1c2d3e4f5a6d"
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("id: "+id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, eventsFile)
	if err := os.WriteFile(path, []byte(`{"type":"user.message","data":{"content":"hi"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	events, _, err := (&Source{}).Read(path, 0)
	if err != nil || len(events) != 1 || events[0].SessionID != id {
		t.Fatalf("got %d events, err %v; want session id from workspace.yaml", len(events), err)
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
		if e.SessionID != sessionID || e.Provider != transcript.ProviderCopilot {
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
	// The default partial copy loses the session directory, so give it one.
	partialDir := filepath.Join(t.TempDir(), "session-state", fixtureID)
	if err := os.MkdirAll(partialDir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(fixturePath)
	lines := strings.SplitAfter(string(data), "\n")
	partial := filepath.Join(partialDir, eventsFile)
	if err := os.WriteFile(partial, []byte(strings.Join(lines[:len(lines)/2], "")), 0o600); err != nil {
		t.Fatal(err)
	}
	transcripttest.RunSeqStability(t, &Source{}, []transcripttest.SeqCase{
		{Name: "events", Path: fixturePath, Partial: partial},
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
	for split := 1; split < len(whole); split++ {
		offset := 0
		for range split {
			offset += strings.IndexByte(string(data[offset:]), '\n') + 1
		}
		tail, _, err := source.Read(fixturePath, int64(offset))
		if err != nil {
			t.Fatal(err)
		}
		if len(tail) != len(whole)-split {
			t.Fatalf("split %d: got %d events from offset, want %d", split, len(tail), len(whole)-split)
		}
		for i, e := range tail {
			w := whole[split+i]
			if e.Seq != w.Seq || deref(e.Model) != deref(w.Model) || deref(e.ToolName) != deref(w.ToolName) || e.SessionID != w.SessionID {
				t.Errorf("split %d: seq %d differs between split and whole reads", split, w.Seq)
			}
		}
	}
}

func TestReadIncompleteTrailingLine(t *testing.T) {
	data, _ := os.ReadFile(fixturePath)
	lines := strings.SplitAfter(string(data), "\n")
	dir := filepath.Join(t.TempDir(), "session-state", fixtureID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, eventsFile)
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
		"type":      "tool.execution_complete",
		"timestamp": "2026-01-02T03:04:05.000Z",
		"data":      map[string]any{"toolCallId": "c", "success": true, "result": map[string]any{"content": big}},
	})
	path := filepath.Join(t.TempDir(), eventsFile)
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
	newSession := func(id string, withEvents bool) string {
		dir := filepath.Join(home, "session-state", id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("id: "+id+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, eventsFile)
		if withEvents {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	live := newSession(fixtureID, true)
	other := newSession("0199b2c3-d4e5-7f60-8a9b-1c2d3e4f5a6e", true)
	newSession("0199b2c3-d4e5-7f60-8a9b-1c2d3e4f5a6f", false)
	source := &Source{CopilotHome: home}
	tests := []struct {
		name string
		meta session.Meta
		want []string
	}{
		{"events path from discovery", session.Meta{Provider: "copilot", ID: fixtureID, RolloutPath: live}, []string{live}},
		{"search by id", session.Meta{Provider: "copilot", ID: fixtureID}, []string{live}},
		{"upper-case id", session.Meta{ID: strings.ToUpper(fixtureID)}, []string{live}},
		{"stale events path falls back to search", session.Meta{ID: fixtureID, RolloutPath: filepath.Join(home, "gone", "session-state", fixtureID, eventsFile)}, []string{live}},
		{"events path for another id is ignored", session.Meta{ID: fixtureID, RolloutPath: other}, []string{live}},
		{"no transcript yet", session.Meta{ID: "0199b2c3-d4e5-7f60-8a9b-1c2d3e4f5a6f"}, nil},
		{"unknown session", session.Meta{ID: "0199b2c3-d4e5-7f60-8a9b-1c2d3e4f5a70"}, nil},
		{"no id", session.Meta{Provider: "copilot"}, nil},
		{"other provider", session.Meta{Provider: "codex", ID: fixtureID}, nil},
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

func TestLocateHonorsCopilotHome(t *testing.T) {
	abs, err := filepath.Abs(fixtureHome)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("COPILOT_HOME", abs)
	got, err := (&Source{Home: t.TempDir()}).Locate(session.Meta{ID: fixtureID})
	if err != nil || len(got) != 1 || got[0] != filepath.Join(abs, "session-state", fixtureID, eventsFile) {
		t.Fatalf("Locate = %v, %v", got, err)
	}
}

func TestRegistered(t *testing.T) {
	if _, ok := transcript.For(transcript.ProviderCopilot); !ok {
		t.Fatal("copilot source not registered")
	}
}
