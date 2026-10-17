// Package e2e holds end-to-end tests that run the reporter against a real
// dashboard server over HTTP, using a synthetic home directory.
package e2e

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/redact"
	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/server/api"
	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/testenv"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

const (
	codexID   = "0199e2e0-0000-7000-8000-00000000c0de"
	copilotID = "0199e2e0-0000-7000-8000-0000000c0b17"
	kimiID    = "kimi-e2e-session"
	// openCodeID's session holds messages at gapped seqs, as OpenCode's
	// durable event sequence leaves them.
	openCodeID = "ses_e2eSmokeOpenCode0001"
	geminiID   = "0199e2e0-0000-7000-8000-00000000a001"
	cursorID   = "0199e2e0-0000-7000-8000-0000000c0501"
)

// fakeSecret looks like an API key to internal/redact. It is assembled at
// run time so no key-shaped literal sits in the repository.
var fakeSecret = "sk-" + strings.Repeat("e2eFake0", 3)

var apiKeyMarker = redact.Marker(redact.KindAPIKey)

// home is a synthetic home directory with one transcript per provider, all
// running in the same Git repository.
type home struct {
	dir      string
	repo     string
	rollout  string // Codex
	events   string // Copilot
	kimiWire string // Kimi Code main agent wire log
	openCode string // OpenCode database
	gemini   string // Gemini CLI session log
	cursor   string // Cursor CLI agent transcript
	chat     string // Cursor CLI chat database
}

func newHome(t *testing.T) *home {
	t.Helper()
	h := &home{dir: t.TempDir()}
	h.repo = filepath.Join(h.dir, "src", "smoke")
	mkdir(t, filepath.Join(h.repo, ".git"))

	codexHome := filepath.Join(h.dir, ".codex")
	h.rollout = filepath.Join(codexHome, "sessions", "2026", "10", "07", "rollout-2026-10-07T12-00-00-"+codexID+".jsonl")
	write(t, h.rollout, jsonl(
		map[string]any{"timestamp": "2026-10-07T12:00:00.000Z", "type": "session_meta", "payload": map[string]any{
			"id": codexID, "timestamp": "2026-10-07T12:00:00.000Z", "cwd": h.repo, "originator": "codex_cli", "cli_version": "0.0.0-e2e",
		}},
		map[string]any{"timestamp": "2026-10-07T12:00:01.000Z", "type": "turn_context", "payload": map[string]any{"cwd": h.repo, "model": "gpt-e2e"}},
		codexMessage("user", "input_text", "Deploy with OPENAI key "+fakeSecret+" please."),
		codexMessage("assistant", "output_text", "I will not echo that key."),
		map[string]any{"timestamp": "2026-10-07T12:00:04.000Z", "type": "event_msg", "payload": map[string]any{"type": "task_complete"}},
	))
	// Discovery only stats the Codex state database; its rows come from the
	// fake sqlite3 below.
	write(t, filepath.Join(codexHome, "state_5.sqlite"), "")

	copilotDir := filepath.Join(h.dir, ".copilot", "session-state", copilotID)
	write(t, filepath.Join(copilotDir, "workspace.yaml"), fmt.Sprintf(
		"id: %s\ncwd: %s\ngit_root: %s\nbranch: main\nname: Copilot smoke\ncreated_at: 2026-10-07T12:00:00.000Z\nupdated_at: 2026-10-07T12:05:00.000Z\n",
		copilotID, h.repo, h.repo))
	h.events = filepath.Join(copilotDir, "events.jsonl")
	write(t, h.events, jsonl(
		map[string]any{"type": "session.start", "id": "e0", "timestamp": "2026-10-07T12:00:00.000Z", "data": map[string]any{
			"sessionId": copilotID, "version": 1, "producer": "copilot-agent", "startTime": "2026-10-07T12:00:00.000Z", "context": map[string]any{"cwd": h.repo},
		}},
		map[string]any{"type": "user.message", "id": "e1", "timestamp": "2026-10-07T12:00:01.000Z", "data": map[string]any{
			"content": "Use " + fakeSecret + " for the call.", "messageId": "m1", "turnId": "0",
		}},
		map[string]any{"type": "assistant.message", "id": "e2", "timestamp": "2026-10-07T12:00:02.000Z", "data": map[string]any{
			"messageId": "m2", "model": "gpt-e2e", "content": "Done.", "turnId": "0",
		}},
	))

	// Kimi Code writes state.json times as epoch milliseconds.
	kimiDir := filepath.Join(h.dir, ".kimi-code", "sessions", "wd_smoke_0123456789ab", kimiID)
	write(t, filepath.Join(kimiDir, "state.json"), fmt.Sprintf(
		`{"id":%q,"version":2,"title":"Kimi smoke","cwd":%q,"createdAt":1791892800000,"updatedAt":1791892860000,"archived":false}`, kimiID, h.repo))
	h.kimiWire = session.KimiWirePath(kimiDir)
	write(t, h.kimiWire, jsonl(
		map[string]any{"type": "metadata", "protocol_version": "1.5", "created_at": 1791892800000},
		map[string]any{"type": "context.append_message", "agentId": "main", "time": 1791892801000, "message": map[string]any{
			"role": "user", "content": []map[string]string{{"type": "text", "text": "Key " + fakeSecret + " goes here."}}, "toolCalls": []any{}, "origin": map[string]string{"kind": "user"},
		}},
		map[string]any{"type": "llm.request", "agentId": "main", "time": 1791892802000, "kind": "loop", "provider": "e2e", "model": "kimi-e2e"},
		kimiReply("Stored it.", 1791892803000),
		map[string]any{"type": "usage.record", "agentId": "main", "time": 1791892803500, "model": "__kimi_env_model__", "usageScope": "turn",
			"usage": map[string]int{"inputOther": 10, "output": 5, "inputCacheRead": 100, "inputCacheCreation": 0}},
	))

	// OpenCode keeps every session in one SQLite database.
	h.openCode = filepath.Join(h.dir, ".local", "share", "opencode", "opencode.db")
	mkdir(t, filepath.Dir(h.openCode))
	h.openCodeSQL(t,
		`CREATE TABLE session_v2 (id text PRIMARY KEY, project_id text NOT NULL, parent_id text,
			slug text NOT NULL, directory text NOT NULL, title text, version text NOT NULL,
			tokens_input integer DEFAULT 0 NOT NULL, tokens_output integer DEFAULT 0 NOT NULL,
			tokens_reasoning integer DEFAULT 0 NOT NULL, tokens_cache_read integer DEFAULT 0 NOT NULL,
			tokens_cache_write integer DEFAULT 0 NOT NULL, model text,
			time_created integer NOT NULL, time_updated integer NOT NULL)`,
		`CREATE TABLE session_message (id text PRIMARY KEY, session_id text NOT NULL, type text NOT NULL,
			seq integer NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL)`,
		fmt.Sprintf(`INSERT INTO session_v2 (id, project_id, slug, directory, title, version, model, time_created, time_updated)
			VALUES ('%s', 'prj', 'smoke', %s, 'OpenCode smoke', '2', '{"id":"model-e2e","providerID":"e2e"}', 1791892800000, 1791892803000)`,
			openCodeID, sqlString(h.repo)),
	)
	h.openCodeMessage(t, 1, "user", map[string]any{"text": "Use " + fakeSecret + " here."})
	h.openCodeMessage(t, 3, "assistant", map[string]any{
		"model":   map[string]any{"id": "model-e2e", "providerID": "e2e"},
		"content": []map[string]any{{"type": "text", "text": "Done."}},
		"tokens":  map[string]any{"input": 1, "output": 2, "reasoning": 0, "cache": map[string]any{"read": 3, "write": 0}},
		"time":    map[string]any{"created": 1791892802000, "completed": 1791892803000},
	})
	h.openCodeMessage(t, 4, "idle", map[string]any{"outcome": "succeeded"})

	// Gemini CLI names each project's directory in projects.json and logs
	// sessions under it.
	geminiDir := filepath.Join(h.dir, ".gemini")
	write(t, filepath.Join(geminiDir, "projects.json"), fmt.Sprintf(`{"projects":{%q:"smoke"}}`, h.repo))
	write(t, filepath.Join(geminiDir, "tmp", "smoke", ".project_root"), h.repo)
	h.gemini = filepath.Join(geminiDir, "tmp", "smoke", "chats", "session-2026-10-07T12-00-0199e2e0.jsonl")
	write(t, h.gemini, jsonl(
		map[string]any{"sessionId": geminiID, "projectHash": session.GeminiProjectHash(h.repo), "startTime": "2026-10-07T12:00:00.000Z", "lastUpdated": "2026-10-07T12:00:00.000Z", "kind": "main"},
		map[string]any{"id": "u1", "timestamp": "2026-10-07T12:00:01.000Z", "type": "user", "content": []map[string]string{{"text": "Call it with " + fakeSecret + " now."}}},
		geminiReply("Called."),
		map[string]any{"$set": map[string]any{"summary": "Gemini smoke", "lastUpdated": "2026-10-07T12:00:03.000Z"}},
	))

	// Cursor CLI names each project's directory after its workspace path,
	// and keeps the chat's title in a database under the path's md5.
	cursorDir := filepath.Join(h.dir, ".cursor")
	project := strings.TrimLeft(strings.ReplaceAll(filepath.ToSlash(h.repo), "/", "-"), "-")
	h.cursor = filepath.Join(cursorDir, "projects", project, "agent-transcripts", cursorID, cursorID+".jsonl")
	write(t, h.cursor, jsonl(
		map[string]any{"role": "user", "message": map[string]any{"content": []map[string]string{{"type": "text", "text": "<user_query>Call it with " + fakeSecret + " now.</user_query>"}}}},
		map[string]any{"role": "assistant", "message": map[string]any{"content": []map[string]string{{"type": "text", "text": "Called."}}}},
	))
	h.chat = filepath.Join(session.CursorChatsDir(cursorDir, h.repo), cursorID, "store.db")
	mkdir(t, filepath.Dir(h.chat))
	testenv.NeedTool(t, "sqlite3")
	meta := hex.EncodeToString([]byte(`{"agentId":"` + cursorID + `","name":"Cursor smoke","createdAt":1791892800000,"lastUsedModel":"cursor-e2e"}`))
	if out, err := exec.Command("sqlite3", h.chat, "CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT); INSERT INTO meta VALUES ('0', '"+meta+"');").CombinedOutput(); err != nil {
		t.Fatalf("cursor chat database: %v: %s", err, out)
	}
	return h
}

// geminiReply is one Gemini CLI model message.
func geminiReply(text string, extra ...map[string]any) map[string]any {
	reply := map[string]any{"id": "g1", "timestamp": "2026-10-07T12:00:02.000Z", "type": "gemini", "content": text, "model": "gemini-e2e",
		"tokens": map[string]int{"input": 110, "output": 5, "cached": 100, "thoughts": 0, "tool": 0, "total": 115}}
	for _, fields := range extra {
		for k, v := range fields {
			reply[k] = v
		}
	}
	return reply
}

// openCodeSQL runs statements against h's OpenCode database.
func (h *home) openCodeSQL(t *testing.T, statements ...string) {
	t.Helper()
	testenv.NeedTool(t, "sqlite3")
	if out, err := exec.Command("sqlite3", h.openCode, strings.Join(statements, ";\n")+";").CombinedOutput(); err != nil {
		t.Fatalf("opencode database: %v: %s", err, out)
	}
}

// openCodeMessage adds one message to the OpenCode session.
func (h *home) openCodeMessage(t *testing.T, seq int, kind string, data map[string]any) {
	t.Helper()
	ms := int64(1791892800000 + seq*1000)
	if _, ok := data["time"]; !ok {
		data["time"] = map[string]any{"created": ms}
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	h.openCodeSQL(t, fmt.Sprintf(`INSERT INTO session_message VALUES ('msg_e2e_%d', '%s', '%s', %d, %d, %d, %s)`,
		seq, openCodeID, kind, seq, ms, ms, sqlString(string(encoded))))
}

// kimiReply is one assistant text part in a Kimi Code wire log.
func kimiReply(text string, ms int64) map[string]any {
	return map[string]any{"type": "context.append_loop_event", "agentId": "main", "time": ms, "event": map[string]any{
		"type": "content.part", "stepUuid": "s1", "part": map[string]string{"type": "text", "text": text},
	}}
}

func sqlString(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }

func codexMessage(role, kind, text string) map[string]any {
	return map[string]any{"timestamp": "2026-10-07T12:00:02.000Z", "type": "response_item", "payload": map[string]any{
		"type": "message", "role": role, "content": []map[string]string{{"type": kind, "text": text}},
	}}
}

// run answers discovery's ps, lsof, sqlite3, and git calls as if one Codex,
// one Copilot, one Kimi, one OpenCode, one Gemini CLI, and one Cursor CLI
// process were running in h.repo.
// OpenCode and Cursor chat queries run against the real databases with
// sqlite3.
func (h *home) run(_ context.Context, name string, args ...string) ([]byte, error) {
	switch name {
	case "ps":
		return []byte("100 1 ttys001 00:10 codex\n200 1 ttys002 00:10 copilot --resume " + copilotID + "\n300 1 ttys003 00:10 kimi\n400 1 ttys004 00:10 opencode\n500 1 ttys005 00:10 gemini\n600 1 ttys006 00:10 cursor-agent\n"), nil
	case "lsof":
		switch {
		case slices.Equal(args, []string{"-Fn", "-p", "100"}):
			return []byte("p100\nn" + h.rollout + "\n"), nil
		case slices.Equal(args, []string{"-a", "-d", "cwd", "-Fn", "-p", "300"}):
			return []byte("p300\nn" + h.repo + "\n"), nil
		case slices.Equal(args, []string{"-a", "-d", "cwd", "-Fn", "-p", "400"}):
			return []byte("p400\nn" + h.repo + "\n"), nil
		case slices.Equal(args, []string{"-a", "-d", "cwd", "-Fn", "-p", "500"}):
			return []byte("p500\nn" + h.repo + "\n"), nil
		case slices.Equal(args, []string{"-a", "-d", "cwd", "-Fn", "-p", "600"}):
			return []byte("p600\nn" + h.repo + "\n"), nil
		}
	case "sqlite3":
		if len(args) == 4 && args[0] == "-readonly" && args[1] == "-json" && filepath.Base(args[2]) == "state_5.sqlite" {
			return json.Marshal([]session.ThreadMetadataRow{{
				ID: codexID, Name: "Codex smoke", CWD: h.repo, Model: "gpt-e2e", GitBranch: "main",
				UpdatedAtMS: time.Date(2026, 10, 7, 12, 0, 4, 0, time.UTC).UnixMilli(),
			}})
		}
		if len(args) == 4 && (args[2] == h.openCode || args[2] == h.chat) {
			return exec.Command(name, args...).Output()
		}
	case "git":
		if len(args) == 4 && args[0] == "-C" && args[2] == "rev-parse" {
			return []byte(h.repo + "\n"), nil
		}
	}
	return nil, errors.New("unavailable in e2e")
}

// config runs the real discovery and transcript readers against h. The
// provider homes are also set in the environment for the readers, which
// take no home from the reporter.
func (h *home) config(t *testing.T, server string) reporter.Config {
	t.Setenv("HOME", h.dir)
	t.Setenv("USERPROFILE", h.dir) // the home directory on Windows
	t.Setenv("CODEX_HOME", filepath.Join(h.dir, ".codex"))
	t.Setenv("COPILOT_HOME", filepath.Join(h.dir, ".copilot"))
	t.Setenv("KIMI_CODE_HOME", filepath.Join(h.dir, ".kimi-code"))
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("OPENCODE_DB", "")
	t.Setenv("GEMINI_CLI_HOME", h.dir)
	t.Setenv("CURSOR_CONFIG_DIR", filepath.Join(h.dir, ".cursor"))
	return reporter.Config{
		Server:    server,
		Providers: []transcript.Provider{transcript.ProviderCodex, transcript.ProviderCopilot, transcript.ProviderKimi, transcript.ProviderOpenCode, transcript.ProviderGemini, transcript.ProviderCursor},
		Home:      h.dir,
		Version:   "e2e",
		Discover: func(ctx context.Context, opts session.Options) ([]session.Meta, error) {
			opts.CodexHome = filepath.Join(h.dir, ".codex")
			opts.CopilotHome = filepath.Join(h.dir, ".copilot")
			opts.KimiHome = filepath.Join(h.dir, ".kimi-code")
			opts.OpenCodeDB = h.openCode
			opts.GeminiDir = filepath.Join(h.dir, ".gemini")
			opts.CursorDir = filepath.Join(h.dir, ".cursor")
			opts.Run = h.run
			return session.Discover(ctx, opts)
		},
	}
}

func TestSmoke(t *testing.T) {
	ctx := context.Background()
	h := newHome(t)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "firekeeper.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler(st))
	t.Cleanup(func() {
		srv.Close()
		st.Close()
	})
	cfg := h.config(t, srv.URL)

	// First pass uploads everything.
	first := runOnce(t, cfg)
	machineID := readMachineID(t, h.dir)
	openCodePath := session.OpenCodeTranscriptPath(h.openCode, openCodeID)
	want := map[string]int{
		"codex":    expectedEvents(t, transcript.ProviderCodex, h.rollout),
		"copilot":  expectedEvents(t, transcript.ProviderCopilot, h.events),
		"kimi":     expectedEvents(t, transcript.ProviderKimi, h.kimiWire),
		"opencode": expectedEvents(t, transcript.ProviderOpenCode, openCodePath),
		"gemini":   expectedEvents(t, transcript.ProviderGemini, h.gemini),
		"cursor":   expectedEvents(t, transcript.ProviderCursor, h.cursor),
	}
	ids := map[string]string{"codex": codexID, "copilot": copilotID, "kimi": kimiID, "opencode": openCodeID, "gemini": geminiID, "cursor": cursorID}
	for provider, n := range want {
		if first[provider].Events != n {
			t.Errorf("%s: first pass read %d events, want %d", provider, first[provider].Events, n)
		}
	}

	sessions := listSessions(t, srv.URL)
	if len(sessions) != len(want) {
		t.Fatalf("server has %d sessions, want %d", len(sessions), len(want))
	}
	for _, s := range sessions {
		n, ok := want[s.Provider]
		if !ok {
			t.Errorf("unexpected %s session on server", s.Provider)
			continue
		}
		if s.MachineID != machineID || s.SessionID != ids[s.Provider] || s.Project != "smoke" || s.EventCount != int64(n) {
			t.Errorf("%s session metadata: machine ok %v, id %q, project %q, event_count %d (want %d)",
				s.Provider, s.MachineID == machineID, s.SessionID, s.Project, s.EventCount, n)
		}
	}
	for provider, n := range want {
		events := listEvents(t, srv.URL, machineID, ids[provider])
		if provider == "opencode" {
			assertSeqList(t, provider, events, []int64{1, 3, 4})
		} else {
			assertSeqs(t, provider, events, n)
		}
		assertRedacted(t, provider, events)
	}
	machines := listMachines(t, srv.URL)
	if len(machines) != 1 || machines[0].ID != machineID || machines[0].SessionCount != int64(len(want)) {
		t.Errorf("machines: got %d, want one with %d sessions", len(machines), len(want))
	}

	// Second pass finds nothing new.
	second := runOnce(t, cfg)
	for provider := range want {
		if r := second[provider]; r.Events != 0 || r.Batches != 0 {
			t.Errorf("%s: second pass sent %d events in %d batches, want none", provider, r.Events, r.Batches)
		}
	}

	// Appending to the Codex rollout, the Kimi wire log, the OpenCode
	// session, the Gemini CLI log, and the Cursor CLI transcript sends only
	// the new events.
	appendFile(t, h.rollout, jsonl(
		codexMessage("user", "input_text", "One more thing."),
		codexMessage("assistant", "output_text", "Appended reply."),
	))
	h.openCodeMessage(t, 7, "user", map[string]any{"text": "Next task."})
	appendFile(t, h.kimiWire, jsonl(kimiReply("Appended Kimi reply.", 1791892804000)))
	// Gemini CLI writes the reply again when it gains a tool call; only the
	// call is new.
	appendFile(t, h.gemini, jsonl(geminiReply("Called.", map[string]any{"toolCalls": []map[string]any{{
		"id": "c1", "name": "write_file", "args": map[string]string{"file_path": "notes.md", "content": "x"}, "status": "success",
	}}})))
	appendFile(t, h.cursor, jsonl(map[string]any{"role": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "tool_use", "id": "t1", "name": "Write", "input": map[string]string{"path": "notes.md"}},
	}}}))
	third := runOnce(t, cfg)
	if r := third["cursor"]; r.Events != 1 || r.Duplicates != 0 {
		t.Errorf("cursor: third pass sent %d events (%d duplicates), want 1 new", r.Events, r.Duplicates)
	}
	cursorEvents := listEvents(t, srv.URL, machineID, cursorID)
	assertSeqs(t, "cursor", cursorEvents, want["cursor"]+1)
	if last := cursorEvents[len(cursorEvents)-1]; last.Role != transcript.RoleToolCall || last.ToolName == nil || *last.ToolName != "Write" {
		t.Errorf("cursor: appended event (seq %d) is not the new tool call", last.Seq)
	}
	if r := third["gemini"]; r.Events != 1 || r.Duplicates != 0 {
		t.Errorf("gemini: third pass sent %d events (%d duplicates), want 1 new", r.Events, r.Duplicates)
	}
	geminiEvents := listEvents(t, srv.URL, machineID, geminiID)
	assertSeqs(t, "gemini", geminiEvents, want["gemini"]+1)
	if last := geminiEvents[len(geminiEvents)-1]; last.Role != transcript.RoleToolCall || last.ToolName == nil || *last.ToolName != "write_file" || last.Tokens != (transcript.Tokens{}) {
		t.Errorf("gemini: appended event (seq %d) is not the new tool call alone", last.Seq)
	}
	if r := third["kimi"]; r.Events != 1 || r.Duplicates != 0 {
		t.Errorf("kimi: third pass sent %d events (%d duplicates), want 1 new", r.Events, r.Duplicates)
	}
	kimiEvents := listEvents(t, srv.URL, machineID, kimiID)
	assertSeqs(t, "kimi", kimiEvents, want["kimi"]+1)
	if last := kimiEvents[len(kimiEvents)-1]; last.Text != "Appended Kimi reply." || last.Role != transcript.RoleAssistant {
		t.Errorf("kimi: appended event (seq %d) does not match what was appended", last.Seq)
	}
	if r := third["opencode"]; r.Events != 1 || r.Duplicates != 0 {
		t.Errorf("opencode: third pass sent %d events (%d duplicates), want 1 new", r.Events, r.Duplicates)
	}
	openCodeEvents := listEvents(t, srv.URL, machineID, openCodeID)
	assertSeqList(t, "opencode", openCodeEvents, []int64{1, 3, 4, 7})
	if last := openCodeEvents[len(openCodeEvents)-1]; last.Text != "Next task." || last.Role != transcript.RoleUser {
		t.Errorf("opencode: appended event (seq %d) does not match what was added", last.Seq)
	}
	if r := third["codex"]; r.Events != 2 || r.Duplicates != 0 {
		t.Errorf("codex: third pass sent %d events (%d duplicates), want 2 new", r.Events, r.Duplicates)
	}
	if r := third["copilot"]; r.Events != 0 {
		t.Errorf("copilot: third pass sent %d events, want none", r.Events)
	}
	codexEvents := listEvents(t, srv.URL, machineID, codexID)
	assertSeqs(t, "codex", codexEvents, want["codex"]+2)
	tail := codexEvents[want["codex"]:]
	if tail[0].Text != "One more thing." || tail[1].Text != "Appended reply." {
		t.Errorf("codex: appended events (seq %d, %d) do not match what was appended", tail[0].Seq, tail[1].Seq)
	}
	assertSeqs(t, "copilot", listEvents(t, srv.URL, machineID, copilotID), want["copilot"])
}

// runOnce runs one reporter pass and indexes the results by provider.
func runOnce(t *testing.T, cfg reporter.Config) map[string]reporter.SessionResult {
	t.Helper()
	summary, err := reporter.RunOnce(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if summary.Warning != nil {
		t.Fatalf("discovery warning: %v", summary.Warning)
	}
	results := map[string]reporter.SessionResult{}
	for _, r := range summary.Sessions {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Provider, r.Err)
		}
		if _, dup := results[r.Provider]; dup {
			t.Fatalf("%s: more than one session reported", r.Provider)
		}
		results[r.Provider] = r
	}
	for _, p := range []string{"codex", "copilot", "kimi", "opencode", "gemini", "cursor"} {
		if _, ok := results[p]; !ok {
			t.Fatalf("%s: session not discovered", p)
		}
	}
	return results
}

// expectedEvents counts the events the provider's reader finds in path.
func expectedEvents(t *testing.T, provider transcript.Provider, path string) int {
	t.Helper()
	source, ok := reader.For(provider)
	if !ok {
		t.Fatalf("%s: no transcript reader registered", provider)
	}
	events, _, err := source.Read(path, 0)
	if err != nil {
		t.Fatalf("%s: read fixture: %v", provider, err)
	}
	if len(events) == 0 {
		t.Fatalf("%s: fixture produced no events", provider)
	}
	return len(events)
}

func assertSeqs(t *testing.T, provider string, events []transcript.Event, n int) {
	t.Helper()
	if len(events) != n {
		t.Fatalf("%s: server has %d events, want %d", provider, len(events), n)
	}
	for i, e := range events {
		if e.Seq != int64(i) {
			t.Fatalf("%s: event %d has seq %d", provider, i, e.Seq)
		}
	}
}

// assertSeqList checks the server holds exactly the events at seqs, for
// providers whose seqs have gaps.
func assertSeqList(t *testing.T, provider string, events []transcript.Event, seqs []int64) {
	t.Helper()
	got := make([]int64, len(events))
	for i, e := range events {
		got[i] = e.Seq
	}
	if !slices.Equal(got, seqs) {
		t.Fatalf("%s: server has seqs %v, want %v", provider, got, seqs)
	}
}

// assertRedacted checks that the planted secret never reached the server
// and that at least one event shows the marker in both text and raw.
func assertRedacted(t *testing.T, provider string, events []transcript.Event) {
	t.Helper()
	inText, inRaw := false, false
	for _, e := range events {
		if strings.Contains(e.Text, fakeSecret) || strings.Contains(string(e.Raw), fakeSecret) {
			t.Errorf("%s: event %d reached the server unredacted", provider, e.Seq)
		}
		if strings.Contains(e.Text, apiKeyMarker) && strings.Contains(string(e.Raw), apiKeyMarker) {
			inText, inRaw = true, true
		}
	}
	if !inText || !inRaw {
		t.Errorf("%s: no event carries %s in both text and raw", provider, apiKeyMarker)
	}
}

func listSessions(t *testing.T, base string) []store.Session {
	t.Helper()
	var page struct {
		Sessions []store.Session `json:"sessions"`
	}
	getJSON(t, base+"/v1/sessions?limit=500", &page)
	return page.Sessions
}

func listEvents(t *testing.T, base, machineID, sessionID string) []transcript.Event {
	t.Helper()
	uid := url.PathEscape(machineID + ":" + sessionID)
	var all []transcript.Event
	after := int64(-1)
	for {
		var page struct {
			Events  []transcript.Event `json:"events"`
			HasMore bool               `json:"has_more"`
		}
		getJSON(t, fmt.Sprintf("%s/v1/sessions/%s/events?limit=1000&after_seq=%d", base, uid, after), &page)
		all = append(all, page.Events...)
		if !page.HasMore || len(page.Events) == 0 {
			return all
		}
		after = page.Events[len(page.Events)-1].Seq
	}
}

func listMachines(t *testing.T, base string) []store.MachineInfo {
	t.Helper()
	var page struct {
		Machines []store.MachineInfo `json:"machines"`
	}
	getJSON(t, base+"/v1/machines", &page)
	return page.Machines
}

func getJSON(t *testing.T, u string, v any) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", u, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("GET %s: decode: %v", u, err)
	}
}

func readMachineID(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".firekeeper", "machine-id"))
	if err != nil {
		t.Fatal("machine id was not created")
	}
	return strings.TrimSpace(string(data))
}

func jsonl(records ...map[string]any) string {
	var b strings.Builder
	for _, r := range records {
		data, err := json.Marshal(r)
		if err != nil {
			panic(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String()
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, contents string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, contents string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(contents); err != nil {
		t.Fatal(err)
	}
}

// TestSmokeScanEnded runs passes with no agent process running, so every
// session is found only in the transcript folders by the real enumerators.
func TestSmokeScanEnded(t *testing.T) {
	ctx := context.Background()
	h := newHome(t)
	// The Codex enumerator reads the state database with the real sqlite3,
	// so give it the threads table Codex keeps there.
	stateDB := filepath.Join(h.dir, ".codex", "state_5.sqlite")
	if out, err := exec.Command("sqlite3", stateDB, "CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT, cwd TEXT, model TEXT, git_branch TEXT, created_at_ms INTEGER, updated_at_ms INTEGER); "+
		"INSERT INTO threads VALUES ('"+codexID+"', 'Codex smoke', "+sqlString(h.repo)+", 'gpt-e2e', 'main', 1791892800000, 1791892804000);").CombinedOutput(); err != nil {
		t.Fatalf("codex state database: %v: %s", err, out)
	}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "firekeeper.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler(st))
	t.Cleanup(func() {
		srv.Close()
		st.Close()
	})
	cfg := h.config(t, srv.URL)
	cfg.ScanEnded = true
	// The fixture's transcripts are dated October 2026; look back far
	// enough to cover their modification times whatever the clock says.
	cfg.ScanAfter = time.Unix(1, 0)
	noProcesses := cfg
	noProcesses.Discover = func(context.Context, session.Options) ([]session.Meta, error) { return nil, nil }

	scanPass := func(cfg reporter.Config) (reporter.Summary, map[string]reporter.SessionResult) {
		t.Helper()
		summary, err := reporter.RunOnce(ctx, cfg)
		if err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		if summary.Warning != nil {
			t.Fatalf("warning: %v", summary.Warning)
		}
		results := map[string]reporter.SessionResult{}
		for _, r := range summary.Sessions {
			if r.Err != nil {
				t.Fatalf("%s: %v", r.Provider, r.Err)
			}
			if _, dup := results[r.Provider]; dup {
				t.Fatalf("%s: more than one session reported", r.Provider)
			}
			results[r.Provider] = r
		}
		return summary, results
	}

	summary, first := scanPass(noProcesses)
	machineID := readMachineID(t, h.dir)
	openCodePath := session.OpenCodeTranscriptPath(h.openCode, openCodeID)
	want := map[string]int{
		"codex":    expectedEvents(t, transcript.ProviderCodex, h.rollout),
		"copilot":  expectedEvents(t, transcript.ProviderCopilot, h.events),
		"kimi":     expectedEvents(t, transcript.ProviderKimi, h.kimiWire),
		"opencode": expectedEvents(t, transcript.ProviderOpenCode, openCodePath),
		"gemini":   expectedEvents(t, transcript.ProviderGemini, h.gemini),
		"cursor":   expectedEvents(t, transcript.ProviderCursor, h.cursor),
	}
	if summary.Scan.Found != len(want) || summary.Scan.Read != len(want) || !summary.Scan.Complete {
		t.Fatalf("scan = %+v, want all %d sessions read", summary.Scan, len(want))
	}
	for provider, n := range want {
		r, ok := first[provider]
		if !ok {
			t.Errorf("%s: session not found in the transcript folders", provider)
			continue
		}
		if r.Events != n || r.State != "ENDED" {
			t.Errorf("%s: scan read %d events with state %q, want %d and ENDED", provider, r.Events, r.State, n)
		}
	}
	for _, s := range listSessions(t, srv.URL) {
		if s.MachineID != machineID || s.State != "ENDED" || s.Project != "smoke" {
			t.Errorf("%s session: machine ok %v, state %q, project %q", s.Provider, s.MachineID == machineID, s.State, s.Project)
		}
	}

	// A second pass finds nothing changed and opens no transcript.
	if again, results := scanPass(noProcesses); again.Scan.Found != 0 || len(results) != 0 {
		t.Fatalf("second pass: scan = %+v, %d sessions", again.Scan, len(results))
	}

	// New lines are uploaded on the next pass, and only they.
	appendFile(t, h.rollout, jsonl(codexMessage("user", "input_text", "Scanned later.")))
	_, third := scanPass(noProcesses)
	if r := third["codex"]; len(third) != 1 || r.Events != 1 || r.Duplicates != 0 {
		t.Fatalf("third pass = %+v, want one new codex event", third)
	}
	assertSeqs(t, "codex", listEvents(t, srv.URL, machineID, codexID), want["codex"]+1)

	// With the processes running again, each session is read once, as a
	// live session, and the scan does not read it a second time.
	appendFile(t, h.rollout, jsonl(codexMessage("assistant", "output_text", "Live again.")))
	liveSummary, live := scanPass(cfg)
	if liveSummary.Scan.Found != 0 || len(live) != len(want) {
		t.Fatalf("live pass: scan = %+v, %d sessions", liveSummary.Scan, len(live))
	}
	if r := live["codex"]; r.Events != 1 || r.Duplicates != 0 || r.State == "ENDED" {
		t.Fatalf("live codex = %+v, want one new event from a running session", r)
	}
	assertSeqs(t, "codex", listEvents(t, srv.URL, machineID, codexID), want["codex"]+2)
}
