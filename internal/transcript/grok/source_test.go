package grok

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

const testID = "0199c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d"
const otherID = "1a2b3c4d-0000-4000-8000-000000000001"

func fixture(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func transcriptPath(home, cwd, id string) string {
	return filepath.Join(home, "sessions", url.PathEscape(cwd), id, "updates.jsonl")
}

func acp(kind, fields string) string {
	return fmt.Sprintf(`{"timestamp":1793429000,"method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":%q%s}}}`, testID, kind, fields)
}

func xai(kind, fields string) string {
	return strings.Replace(acp(kind, fields), `"method":"session/update"`, `"method":"_x.ai/session/update"`, 1)
}

func TestReadEventsAndSplitReplay(t *testing.T) {
	root := t.TempDir()
	path := transcriptPath(root, "/workspace/test", testID)
	lines := []string{
		acp("user_message_chunk", `,"content":{"type":"text","text":"question"}`),
		xai("response_started", `,"model":"grok-code","input_tokens":80,"cache_read_input_tokens":20`),
		acp("agent_thought_chunk", `,"content":{"type":"text","text":"reasoning"}`),
		acp("tool_call", `,"toolCallId":"call-1","title":"Read a file","status":"in_progress","rawInput":{"path":"file.go"},"_meta":{"x.ai/tool":{"name":"read_file"}}`),
		acp("tool_call_update", `,"toolCallId":"call-1","status":"in_progress"`),
		acp("tool_call_update", `,"toolCallId":"call-1","status":"completed","content":[{"type":"content","content":{"type":"text","text":"result"}},{"type":"diff","path":"file.go","oldText":"old","newText":"new"}]`),
		acp("agent_message_chunk", `,"content":{"type":"text","text":"answer"}`),
		xai("response_completed", `,"stop_reason":"end_turn","usage":{"input_tokens":60,"output_tokens":20,"cache_read_input_tokens":30}`),
		xai("turn_usage", `,"usage":{"input_tokens":60,"output_tokens":20,"cache_read_input_tokens":30}`),
		xai("turn_completed", `,"prompt_id":"prompt-1","stop_reason":"end_turn","agent_result":"answer","usage":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":30,"cacheCreationTokens":10}`),
		acp("plan", `,"entries":[{"content":"step","status":"pending","priority":"high"}]`),
		xai("rewind_marker", `,"target_prompt_index":0,"created_at":"2026-10-30T00:00:00Z"`),
		acp("future_update", `,"future":"kept"`),
		"{malformed}",
		"",
		`null`,
		fmt.Sprintf(`{"sessionId":%q,"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"legacy"}}}`, testID),
	}
	fixture(t, path, strings.Join(lines, "\n")+"\n"+`{"incomplete":`)
	s := &Source{Home: root}
	all, offset, err := s.Read(path, 0)
	if err != nil || len(all) != len(lines) || offset != int64(len(strings.Join(lines, "\n")+"\n")) {
		t.Fatalf("read: events=%d offset=%d err=%v", len(all), offset, err)
	}
	roles := []transcript.Role{
		transcript.RoleUser, transcript.RoleMeta, transcript.RoleMeta, transcript.RoleToolCall,
		transcript.RoleMeta, transcript.RoleToolResult, transcript.RoleAssistant, transcript.RoleMeta,
		transcript.RoleMeta, transcript.RoleMeta, transcript.RoleMeta, transcript.RoleMeta,
		transcript.RoleMeta, transcript.RoleMeta, transcript.RoleMeta, transcript.RoleMeta, transcript.RoleAssistant,
	}
	for i, e := range all {
		if e.Seq != int64(i) || e.Role != roles[i] || e.SessionID != testID || !json.Valid(e.Raw) {
			t.Fatalf("event %d: seq/role/session/raw mismatch", i)
		}
	}
	if all[0].Text != "question" || all[2].Text != "reasoning" || all[3].ToolName == nil || *all[3].ToolName != "read_file" || all[5].ToolName == nil || *all[5].ToolName != "read_file" {
		t.Fatal("message or tool mapping mismatch")
	}
	if all[5].Text != "result\nfile.go\nold\nnew" || all[6].Model == nil || *all[6].Model != "grok-code" {
		t.Fatal("tool content or replay model mismatch")
	}
	if all[0].TS == nil || all[0].TS.Unix() != 1793429000 || all[16].TS != nil {
		t.Fatal("timestamp mismatch")
	}
	var total transcript.Tokens
	for _, e := range all {
		total.Input += e.Tokens.Input
		total.Output += e.Tokens.Output
		total.Cache += e.Tokens.Cache
	}
	if total != (transcript.Tokens{Input: 60, Output: 20, Cache: 40}) {
		t.Fatal("overlapping response/turn usage double counted")
	}
	var boundary int64
	for i := 0; i <= len(lines); i++ {
		split, got, err := s.Read(path, boundary)
		if err != nil || got != offset || !reflect.DeepEqual(split, all[i:]) && !(len(split) == 0 && i == len(lines)) {
			t.Fatalf("split replay differs at line %d", i)
		}
		if i < len(lines) {
			boundary += int64(len(lines[i]) + 1)
		}
	}
}

func TestReadOffsetsAppendAndSanitizedErrors(t *testing.T) {
	root := t.TempDir()
	path := transcriptPath(root, "/workspace/test", testID)
	first := acp("user_message_chunk", `,"content":{"type":"text","text":"question"}`) + "\n"
	second := acp("agent_message_chunk", `,"content":{"type":"text","text":"answer"}`)
	fixture(t, path, first+second)
	s := &Source{Home: root}
	e, offset, err := s.Read(path, 0)
	if err != nil || len(e) != 1 || offset != int64(len(first)) {
		t.Fatal("incomplete line not deferred")
	}

	for _, offset := range []int64{-1, 1, int64(len(first) + 1), int64(len(first) + len(second)), int64(len(first) + len(second) + 1)} {
		if _, _, err := s.Read(path, offset); err == nil || strings.Contains(err.Error(), root) {
			t.Fatal("invalid offset accepted or path leaked")
		}
	}
	fixture(t, path, first+second+"\n")
	e, _, err = s.Read(path, offset)
	if err != nil || len(e) != 1 || e[0].Seq != 1 {
		t.Fatal("append split read failed")
	}
	if _, _, err := s.Read(filepath.Join(root, "missing-private-path"), 0); err == nil || strings.Contains(err.Error(), root) {
		t.Fatal("missing log did not return sanitized error")
	}
}

func TestReadRejectsOversizedRecordsAndRebuiltCaches(t *testing.T) {
	root := t.TempDir()
	path := transcriptPath(root, "/workspace/test", testID)
	fixture(t, path, strings.Repeat("x", 16<<20)+"\n")
	if _, offset, err := (&Source{}).Read(path, 0); err == nil || offset != 0 {
		t.Fatal("oversized record was consumed")
	}
	cache := filepath.Join(filepath.Dir(path), "chat_history.jsonl")
	fixture(t, cache, "{}\n")
	if _, _, err := (&Source{}).Read(cache, 0); err == nil {
		t.Fatal("rewritten chat cache was accepted as a stable source")
	}
}

func TestInvalidUsageCannotOverflowNormalizedEvents(t *testing.T) {
	for _, fields := range []string{
		`"inputTokens":9223372036854775807,"cachedReadTokens":9223372036854775807,"cacheCreationTokens":1`,
		`"inputTokens":10,"cachedReadTokens":11`,
		`"inputTokens":-1`,
		`"inputTokens":9223372036854775807,"outputTokens":1`,
	} {
		root := t.TempDir()
		path := transcriptPath(root, "/workspace/test", testID)
		fixture(t, path, xai("turn_completed", `,"usage":{`+fields+`}`)+"\n")
		events, _, err := (&Source{}).Read(path, 0)
		if err != nil || len(events) != 1 || events[0].Tokens != (transcript.Tokens{}) ||
			events[0].Validate() != nil || !strings.Contains(events[0].Text, "invalid token usage") {
			t.Fatal("invalid usage did not remain a valid, explicitly marked metadata event")
		}
	}
}

func TestToolOutputTruncationAndForeignSession(t *testing.T) {
	root := t.TempDir()
	path := transcriptPath(root, "/workspace/test", testID)
	text := strings.Repeat("€", maxToolText/3+10)
	record := acp("tool_call_update", `,"toolCallId":"call-1","status":"failed","rawOutput":`+strconvJSON(text))
	foreign := strings.Replace(acp("agent_message_chunk", `,"content":{"type":"text","text":"foreign"}`), testID, otherID, 1)
	fixture(t, path, record+"\n"+foreign+"\n")
	e, _, err := (&Source{}).Read(path, 0)
	if err != nil || len(e) != 2 || e[0].Role != transcript.RoleToolResult || !utf8.ValidString(e[0].Text) || !strings.Contains(e[0].Text, "[truncated ") || !bytes.Equal(e[0].Raw, []byte(record)) {
		t.Fatal("tool truncation changed raw or broke text")
	}
	if e[1].Role != transcript.RoleMeta || e[1].SessionID != testID || e[1].Text != "" {
		t.Fatal("foreign record contaminated session")
	}
}

func strconvJSON(text string) string {
	b, _ := json.Marshal(text)
	return string(b)
}

func TestLocateAndEnumeratePrivacy(t *testing.T) {
	root := t.TempDir()
	path := transcriptPath(root, "/workspace/public", testID)
	hidden := transcriptPath(root, "/workspace/private", otherID)
	fixture(t, path, acp("user_message_chunk", `,"content":{"type":"text","text":"question"}`)+"\n")
	// Excluded corrupt logs must not contribute a warning: Skip precedes Read.
	fixture(t, hidden, "{invalid}\n")
	fixture(t, filepath.Join(filepath.Dir(path), "summary.json"), fmt.Sprintf(`{"info":{"id":%q,"cwd":"/workspace/public"},"generated_title":"title","current_model_id":"grok-code","created_at":"2026-10-01T00:00:00Z"}`, testID))
	fixture(t, filepath.Join(filepath.Dir(path), "chat_history.jsonl"), "rewritten")
	s := &Source{Home: root}
	for _, meta := range []session.Meta{
		{ID: "../escape", Provider: "grok"},
		{ID: testID, Provider: "codex"},
	} {
		if paths, err := s.Locate(meta); err != nil || len(paths) != 0 {
			t.Fatal("invalid locate boundary accepted")
		}
	}
	paths, err := s.Locate(session.Meta{ID: testID, Provider: "grok", RolloutPath: hidden})
	if err != nil || len(paths) != 1 || paths[0] != path {
		t.Fatal("mismatched rollout accepted")
	}
	paths, err = s.Locate(session.Meta{ID: testID, RolloutPath: filepath.Join(filepath.Dir(path), "chat_history.jsonl")})
	if err != nil || len(paths) != 1 || paths[0] != path {
		t.Fatal("rewritten cache selected")
	}
	t.Setenv("GROK_HOME", root)
	paths, err = (&Source{}).Locate(session.Meta{ID: testID})
	if err != nil || len(paths) != 1 || paths[0] != path {
		t.Fatal("zero-value source ignored environment")
	}
	metas, err := s.Enumerate(context.Background(), reader.EnumerateOptions{Skip: func(m session.Meta) bool { return m.CWD == "/workspace/private" }})
	if err != nil || len(metas) != 1 || metas[0].ID != testID || metas[0].State != session.SessionStateEnded || metas[0].Title != "title" {
		t.Fatal("historical metadata or early exclusion mismatch")
	}
	metas, err = s.Enumerate(context.Background(), reader.EnumerateOptions{})
	var warning *session.Warning
	if len(metas) != 1 || !errors.As(err, &warning) || strings.Contains(err.Error(), root) {
		t.Fatal("corrupt transcript warning mismatch")
	}
	if metas, err := s.Enumerate(context.Background(), reader.EnumerateOptions{ModifiedAfter: time.Now().Add(time.Hour)}); err != nil || len(metas) != 0 {
		t.Fatal("ModifiedAfter ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Enumerate(ctx, reader.EnumerateOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
}

func TestEnumerateNewestAndHashedWorkspace(t *testing.T) {
	root := t.TempDir()
	old := transcriptPath(root, "/workspace/old", testID)
	newPath := filepath.Join(root, "sessions", "long-0123456789abcdef", otherID, "updates.jsonl")
	fixture(t, old, acp("agent_message_chunk", `,"content":{"type":"text","text":"old"}`)+"\n")
	fixture(t, newPath, strings.Replace(acp("agent_message_chunk", `,"content":{"type":"text","text":"new"}`), testID, otherID, 1)+"\n")
	fixture(t, filepath.Join(filepath.Dir(filepath.Dir(newPath)), ".cwd"), "/workspace/long/path")
	older := time.Unix(1800000000, 0)
	if err := os.Chtimes(old, older, older); err != nil {
		t.Fatal(err)
	}

	newer := older.Add(time.Hour)
	if err := os.Chtimes(newPath, newer, newer); err != nil {
		t.Fatal(err)
	}
	got, err := (&Source{Home: root}).Enumerate(context.Background(), reader.EnumerateOptions{})
	if err != nil || len(got) != 2 || got[0].ID != otherID || got[0].CWD != "/workspace/long/path" {
		t.Fatal("newest order or hashed cwd mismatch")
	}
}

func TestEnumerateRelocatedWorkspaceExclusion(t *testing.T) {
	root := t.TempDir()
	path := transcriptPath(root, "/workspace/old-public", testID)
	fixture(t, path, "{invalid}\n")
	fixture(t, filepath.Join(filepath.Dir(path), "summary.json"), fmt.Sprintf(`{"info":{"id":%q,"cwd":"/workspace/private"},"generated_title":"private"}`, testID))
	got, err := (&Source{Home: root}).Enumerate(context.Background(), reader.EnumerateOptions{
		Skip: func(m session.Meta) bool { return m.CWD == "/workspace/private" },
	})
	if err != nil || len(got) != 0 {
		t.Fatal("relocated excluded workspace was scanned")
	}
}
