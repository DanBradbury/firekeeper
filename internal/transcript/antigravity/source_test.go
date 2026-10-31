package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// Verified envelopes from jazzyalex/agent-sessions cli_small.jsonl and
// cli_schema_drift.jsonl. Values below are synthetic.
const userLine = `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-06-26T21:16:16Z","content":"<USER_REQUEST>\nGo\n</USER_REQUEST>\n<USER_SETTINGS_CHANGE>The user changed setting ` + "`Model Selection`" + ` from None to Gemini 3.5 Flash (Medium).</USER_SETTINGS_CHANGE>","promptVariant":"experimental"}`

const responseLine = `{"step_index":0,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-06-26T21:16:17Z","thinking":"Planning.","tool_calls":[{"name":"list_dir","args":{"DirectoryPath":"\"/work/repo\""}}],"input_tokens":100,"output_tokens":10,"cache_read_tokens":20,"telemetry":{"latencyMs":12}}`

func writeTranscript(t *testing.T, dir, id, content string) string {
	t.Helper()
	path := filepath.Join(dir, "brain", id, ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadOneEventPerLineAndSchemaDrift(t *testing.T) {
	lines := []string{
		userLine,
		responseLine,
		`{"step_index":2,"source":"MODEL","type":"LIST_DIRECTORY","status":"DONE","content":"a.txt","truncated_fields":["content"]}`,
		`{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"Done."}`,
		`{"step_index":2,"source":"MODEL","type":"BROWSER_NAVIGATE","status":"DONE","content":"opened https://example.com"}`,
		`{"step_index":3,"source":"SYSTEM","type":"SYSTEM_MESSAGE","status":"DONE","content":"system note"}`,
		`{"step_index":0,"source":"MODEL","type":"GENERIC","status":"DONE","content":"tool output"}`,
		`{"step_index":0,"source":"MODEL","type":"GREP_SEARCH","status":"DONE","content":"search result"}`,
		`{"step_index":broken`,
		`null`,
		`{"other":"unknown envelope"}`,
		"",
	}
	data := strings.Join(lines, "\n") + "\n"
	path := writeTranscript(t, t.TempDir(), "fixture-a", data)
	events, end, err := (&Source{}).Read(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(lines) || end != int64(len(data)) {
		t.Fatal("source lines were dropped or expanded")
	}
	roles := []transcript.Role{transcript.RoleUser, transcript.RoleAssistant, transcript.RoleToolResult, transcript.RoleAssistant,
		transcript.RoleMeta, transcript.RoleMeta, transcript.RoleToolResult, transcript.RoleToolResult,
		transcript.RoleMeta, transcript.RoleMeta, transcript.RoleMeta, transcript.RoleMeta}
	for i, e := range events {
		if e.Seq != int64(i) || e.SessionID != "fixture-a" || e.Role != roles[i] ||
			!json.Valid(e.Raw) || e.Provider != transcript.ProviderAntigravity {
			t.Errorf("normalized event shape differs at source index %d", i)
		}
	}
	if events[0].Text != "Go" || events[1].Text != "Planning." ||
		events[1].Model == nil || *events[1].Model != "Gemini 3.5 Flash (Medium)" ||
		events[1].Tokens != (transcript.Tokens{Input: 100, Output: 10, Cache: 20}) ||
		!strings.Contains(string(events[1].Raw), `"tool_calls"`) || events[1].ToolName != nil {
		t.Error("user wrapper, assistant tools, model, or per-response tokens differ")
	}
	if !strings.HasSuffix(events[2].Text, "[content truncated by Antigravity CLI]") {
		t.Error("upstream truncation not disclosed")
	}
	if events[3].Tokens != (transcript.Tokens{}) {
		t.Error("session totals were copied into another event")
	}
	var malformed string
	if json.Unmarshal(events[8].Raw, &malformed) != nil || malformed != lines[8] {
		t.Error("malformed raw was lost")
	}
	if events[0].TS == nil || events[0].TS.Year() != 2026 {
		t.Error("timestamp lost")
	}
}

func TestReadSplitAndIncompleteTail(t *testing.T) {
	dir := t.TempDir()
	part := userLine + "\n" + responseLine + "\n"
	tail := `{"step_index":0,"type":"PLANNER_RESPONSE","status":"DONE","content":"Done."}`
	path := writeTranscript(t, dir, "fixture-a", part+tail[:10])
	s := &Source{}
	first, offset, err := s.Read(path, 0)
	if err != nil || len(first) != 2 || offset != int64(len(part)) {
		t.Fatal("incomplete tail was consumed")
	}
	if err := os.WriteFile(path, []byte(part+tail+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, end, err := s.Read(path, offset)
	if err != nil || len(second) != 1 || second[0].Seq != 2 || second[0].Model == nil {
		t.Fatal("split read lost seq or model")
	}
	all, _, err := s.Read(path, 0)
	if err != nil || !reflect.DeepEqual(append(first, second...), all) {
		t.Error("split read differs from whole read")
	}
	if _, _, err := s.Read(path, offset-1); err == nil {
		t.Error("non-boundary offset accepted")
	}
	if _, _, err := s.Read(path, end+1); err == nil {
		t.Error("past-end offset accepted")
	}
	if events, got, err := s.Read(path, end); err != nil || len(events) != 0 || got != end {
		t.Error("EOF read changed cursor")
	}
	if _, _, err := s.Read(path, -1); err == nil {
		t.Error("negative offset accepted")
	}
}

func TestToolTextTruncatesOnlyDisplayAndNotThinking(t *testing.T) {
	dir := t.TempDir()
	output := strings.Repeat("é", maxToolText)
	raw, _ := json.Marshal(map[string]any{"step_index": 0, "type": "RUN_COMMAND", "status": "DONE", "content": output})
	path := writeTranscript(t, dir, "fixture-a", string(raw)+"\n"+`{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","thinking":"Thinking.","truncated_fields":["content"]}`+"\n")
	events, _, err := (&Source{}).Read(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(events[0].Text, "[truncated 65536 bytes]") || len(events[0].Raw) <= maxToolText {
		t.Error("tool output truncation affected raw or byte count")
	}
	if events[1].Text != "Thinking." {
		t.Error("content truncation incorrectly marked thinking fallback")
	}
}

func TestLocateValidatesIDsAndAvoidsDuplicateTranscript(t *testing.T) {
	dir := t.TempDir()
	path := writeTranscript(t, dir, "fixture-a", userLine+"\n")
	duplicate := filepath.Join(filepath.Dir(path), "transcript_full.jsonl")
	if err := os.WriteFile(duplicate, []byte(userLine+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Source{Dir: dir}
	for _, meta := range []session.Meta{
		{ID: "fixture-a"},
		{ID: "fixture-a", RolloutPath: duplicate},
		{ID: "fixture-a", RolloutPath: writeTranscript(t, dir, "fixture-b", userLine+"\n")},
	} {
		paths, err := s.Locate(meta)
		if err != nil || !reflect.DeepEqual(paths, []string{path}) {
			t.Error("Locate used mismatching rollout or duplicate")
		}
	}
	for _, meta := range []session.Meta{{ID: "../escape"}, {ID: "*"}, {ID: "fixture-a", Provider: "gemini"}, {ID: "missing"}} {
		if paths, err := s.Locate(meta); err != nil || len(paths) != 0 {
			t.Error("invalid or foreign session located")
		}
	}
}

func TestEnumeratePrivacyFiltersAndCorruption(t *testing.T) {
	dir := t.TempDir()
	old := writeTranscript(t, dir, "old", userLine+"\n")
	recent := writeTranscript(t, dir, "recent", userLine+"\n")
	ignored := writeTranscript(t, dir, "ignored", "malformed private fixture\n")
	bad := writeTranscript(t, dir, "bad", "not JSON\n")
	if err := os.MkdirAll(filepath.Join(dir, "cache"), 0700); err != nil {
		t.Fatal(err)
	}
	cache, _ := json.Marshal(map[string]string{"/work/ignored": "ignored", "/work/recent": "recent"})
	if err := os.WriteFile(filepath.Join(dir, "cache", "last_conversations.json"), cache, 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for path, stamp := range map[string]time.Time{old: now.Add(-2 * time.Hour), recent: now, ignored: now, bad: now} {
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(recent), "transcript_full.jsonl"), []byte(userLine+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	s := &Source{Dir: dir}
	metas, err := s.Enumerate(context.Background(), reader.EnumerateOptions{
		ModifiedAfter: now.Add(-time.Hour),
		Skip: func(meta session.Meta) bool {
			calls[meta.ID]++
			// Metadata is from the cache; the malformed ignored log must not
			// have been read, or it would contribute to the warning count.
			return meta.CWD == "/work/ignored"
		},
	})
	var warning *session.Warning
	if !errors.As(err, &warning) || !strings.Contains(warning.Message, "1 empty") {
		t.Error("excluded transcript was read or corrupt count differs")
	}
	if len(metas) != 1 || metas[0].ID != "recent" || metas[0].CWD != "/work/recent" ||
		metas[0].State != session.SessionStateEnded || metas[0].Input != 0 || metas[0].EventCount != 0 {
		t.Error("enumeration metadata or filters differ")
	}
	for _, n := range calls {
		if n != 1 {
			t.Error("Skip called more than once")
		}
	}
	if calls["old"] != 0 {
		t.Error("ModifiedAfter filter ran after transcript read")
	}
	metas, _ = s.Enumerate(context.Background(), reader.EnumerateOptions{Skip: func(m session.Meta) bool { return m.ID == "ignored" }})
	if len(metas) != 2 || metas[0].ID != "recent" || metas[1].ID != "old" {
		t.Error("newest-first ordering differs")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Enumerate(ctx, reader.EnumerateOptions{}); !errors.Is(err, context.Canceled) {
		t.Error("enumeration ignored cancellation")
	}
}

func TestEnumerateUnreadableWorkspaceMetadataFailsClosed(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "private", "malformed fixture\n")
	if err := os.MkdirAll(filepath.Join(dir, "cache"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cache", "last_conversations.json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	metas, err := (&Source{Dir: dir}).Enumerate(context.Background(), reader.EnumerateOptions{})
	if err == nil || len(metas) != 0 {
		t.Error("unavailable workspace metadata did not fail closed")
	}
}
