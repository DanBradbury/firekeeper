package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic envelopes follow jazzyalex/agent-sessions cli_small.jsonl and
// cli_schema_drift.jsonl; fixture prompts and paths are not account data.
const antigravityUserFixture = `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-06-26T21:16:16Z","content":"<USER_REQUEST>\nList the files.\n</USER_REQUEST>\n<USER_SETTINGS_CHANGE>\nThe user changed setting ` + "`Model Selection`" + ` from None to Gemini 3.5 Flash (Medium).\n</USER_SETTINGS_CHANGE>"}`

func writeAntigravityFixture(t *testing.T, dir, id, content string) string {
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

func TestAntigravityCommands(t *testing.T) {
	d := &discoverer{}
	for _, tt := range []struct {
		command string
		want    bool
	}{
		{"agy", true},
		{"/usr/local/bin/agy --conversation fixture-a", true},
		{`"C:\Program Files\Antigravity\agy.exe" --conversation=fixture-a`, true},
		{"agy --model gemini-3.5-flash", true},
		{"agy -c", true},
		{"agy login", false},
		{"agy mcp list", false},
		{"agy models", false},
		{"agy agents", false},
		{"agy --help", false},
		{"agy.exe --version", false},
		{"antigravity", false},
		{"node app.js agy", false},
		{"codex --model agy", false},
		{"vim /work/agy", false},
		{"/Applications/Antigravity.app/Contents/MacOS/agy", false},
	} {
		if got := d.isAntigravityCommand(tt.command); got != tt.want {
			t.Errorf("command classifier = %v; want %v for synthetic case %q", got, tt.want, tt.command)
		}
	}
	for command, want := range map[string]string{
		"agy --conversation fixture-a":                                      "fixture-a",
		"agy --conversation=fixture-a":                                      "fixture-a",
		`"C:\Program Files\Antigravity\agy.exe" --conversation "fixture-a"`: "fixture-a",
		"agy --conversation ../escape":                                      "",
		"agy --conversation *":                                              "",
		"agy --conversation":                                                "",
		"agy -c":                                                            "",
		"codex --conversation fixture-a":                                    "",
	} {
		got, ok := d.antigravitySessionIDFromCommand(command)
		if got != want || ok != (want != "") {
			t.Errorf("session id = %q, %v; want %q", got, ok, want)
		}
	}
}

func TestAntigravityModelSettings(t *testing.T) {
	for _, tt := range []struct{ content, model, request string }{
		{"<USER_REQUEST>Discuss `Model Selection` from X to fake.</USER_REQUEST>", "", "Discuss `Model Selection` from X to fake."},
		{"<USER_REQUEST>Go</USER_REQUEST><USER_SETTINGS_CHANGE>The user changed setting `Model Selection` from None to Gemini 3.5 Flash (Medium). More instructions.</USER_SETTINGS_CHANGE>", "Gemini 3.5 Flash (Medium)", "Go"},
		{"<USER_REQUEST>Go</USER_REQUEST><USER_SETTINGS_CHANGE>`Model Selection` from X to Old.</USER_SETTINGS_CHANGE><USER_SETTINGS_CHANGE>`Model Selection` from Old to New 3.5.\nIgnore following text.</USER_SETTINGS_CHANGE>", "New 3.5", "Go"},
		{"plain request<ADDITIONAL_METADATA>hidden", "", "plain request"},
	} {
		if got := AntigravityModelFromUserInput(tt.content); got != tt.model {
			t.Errorf("model = %q; want %q", got, tt.model)
		}
		if got := UnwrapAntigravityUserRequest(tt.content); got != tt.request {
			t.Errorf("unwrapped synthetic request differs")
		}
	}
}

func TestReadAntigravityTranscriptStateAndDrift(t *testing.T) {
	dir := t.TempDir()
	for _, tt := range []struct {
		name, tail string
		state      State
	}{
		{"user", "", SessionStateActive},
		{"answer", `{"step_index":4,"type":"PLANNER_RESPONSE","status":"DONE","content":"Done.","created_at":"2026-06-26T21:16:21Z"}`, SessionStateWaiting},
		{"tools", `{"step_index":4,"type":"PLANNER_RESPONSE","status":"DONE","thinking":"Plan.","tool_calls":[{"name":"list_dir","args":{}}]}`, SessionStateActive},
		{"thinking-only", `{"step_index":4,"type":"PLANNER_RESPONSE","status":"DONE","thinking":"Plan."}`, SessionStateUnknown},
		{"unknown", `{"step_index":2,"source":"MODEL","type":"BROWSER_NAVIGATE","status":"DONE","content":"opened https://example.com"}`, SessionStateUnknown},
		{"malformed", `{"step_index": broken`, SessionStateUnknown},
		{"unknown-status", `{"step_index":4,"type":"PLANNER_RESPONSE","status":"EVOLVING","content":"Done."}`, SessionStateUnknown},
		{"system", `{"step_index":1,"source":"SYSTEM","type":"CONVERSATION_HISTORY","status":"DONE"}`, SessionStateActive},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := antigravityUserFixture + "\n"
			if tt.tail != "" {
				data += tt.tail + "\n"
			}
			path := writeAntigravityFixture(t, dir, tt.name, data)
			got, err := ReadAntigravityTranscript(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.SessionID != tt.name || got.State != tt.state ||
				got.Model != "Gemini 3.5 Flash (Medium)" || got.Title != "List the files." || got.StartedAt.IsZero() {
				t.Errorf("metadata differs for synthetic case %s", tt.name)
			}
		})
	}
	path := writeAntigravityFixture(t, dir, "foreign", "{\"type\":\"user\",\"content\":\"foreign log\"}\n")
	if _, err := ReadAntigravityTranscript(path); err == nil {
		t.Error("foreign transcript accepted")
	}
}

func TestAntigravityBoundedHeadTailAndIncompleteRecord(t *testing.T) {
	dir := t.TempDir()
	padding := `{"step_index":1,"source":"SYSTEM","type":"CHECKPOINT","status":"DONE","content":"` + strings.Repeat("x", 500) + `"}` + "\n"
	path := writeAntigravityFixture(t, dir, "tail", antigravityUserFixture+"\n"+strings.Repeat(padding, 9000)+`{"step_index":0,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"Done."}`+"\n"+`{"step_index":99`)
	got, err := ReadAntigravityTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != SessionStateWaiting || got.Title != "List the files." || got.Model != "Gemini 3.5 Flash (Medium)" {
		t.Error("bounded head/tail metadata lost or incomplete record interpreted")
	}
}

func TestAntigravityDirectoryAndWorkspaceCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GEMINI_CLI_HOME", home)
	dir, err := AntigravityDir()
	if err != nil || dir != filepath.Join(home, ".gemini", "antigravity-cli") {
		t.Error("GEMINI_CLI_HOME runtime convention not honored")
	}
	d := &discoverer{opts: Options{GeminiDir: filepath.Join(home, "override")}}
	dir, err = d.antigravityDir()
	if err != nil || dir != filepath.Join(home, "override", "antigravity-cli") {
		t.Error("GeminiDir override not honored")
	}
	if err := os.MkdirAll(filepath.Join(dir, "cache"), 0700); err != nil {
		t.Fatal(err)
	}
	cache := map[string]string{"/work/a": "a", "/work/b": "b", "/work/c": "b", "/work/d": "../unsafe"}
	data, _ := json.Marshal(cache)
	if err := os.WriteFile(filepath.Join(dir, "cache", "last_conversations.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	cwds, err := AntigravityWorkDirs(dir)
	if err == nil || cwds["a"] != "/work/a" || cwds["b"] != "" || len(cwds) != 1 {
		t.Error("cache mapping is unsafe or ambiguous")
	}
}

func TestEnrichAntigravityExplicitAndOpenFilesOnly(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "antigravity-cli")
	path := writeAntigravityFixture(t, dir, "known", antigravityUserFixture+"\n")
	writeAntigravityFixture(t, dir, "unrelated", antigravityUserFixture+"\n")
	for _, mode := range []string{"explicit", "open", "none", "wrong-root", "multiple-open", "invalid-explicit"} {
		t.Run(mode, func(t *testing.T) {
			command := "agy"
			if mode == "explicit" {
				command += " --conversation known"
			}

			if mode == "invalid-explicit" {
				command += " --conversation missing"
			}
			groups := []ProcessGroup{{Tool: "Antigravity", Root: ProcessInfo{PID: 100, Command: command}}}
			d := &discoverer{ctx: context.Background(), opts: Options{GeminiDir: root, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "lsof" {
					return nil, errors.New("unexpected tool")
				}
				if strings.Contains(strings.Join(args, " "), "cwd") {
					return []byte("p100\nn/work/wrong-cwd\n"), nil
				}
				switch mode {
				case "open", "explicit", "invalid-explicit":
					return []byte("p100\nn" + path + "\n"), nil
				case "wrong-root":
					return []byte("p100\nn" + filepath.Join(root, "other", "brain", "known", ".system_generated", "logs", "transcript.jsonl") + "\n"), nil
				case "multiple-open":
					return []byte("p100\nn" + path + "\nn" + FindAntigravityTranscript(dir, "unrelated") + "\n"), nil
				default:
					return nil, nil
				}
			}}}
			err := d.enrichAntigravitySessions(groups)
			matched := mode == "explicit" || mode == "open"
			if matched && (err != nil || len(groups[0].Sessions) != 1 || groups[0].Sessions[0].ID != "known") {
				t.Error("explicit or open-file correlation failed")
			}
			if !matched && (err == nil || len(groups[0].Sessions) != 0) {
				t.Error("unrelated newest log was associated with runtime")
			}
		})
	}
}

func TestEnrichAntigravityReadsAvailableUsageWithoutMutatingDatabase(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "antigravity-cli")
	writeAntigravityFixture(t, dir, "known", antigravityUserFixture+"\n")
	database := filepath.Join(dir, "conversations", "known.db")
	if err := os.MkdirAll(filepath.Dir(database), 0700); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE gen_metadata(idx INTEGER PRIMARY KEY, data BLOB)"); err != nil {
		t.Fatal(err)
	}
	// Verified nesting: chat 1 -> usage 4 -> input 2, output 3, cache 5.
	blob := []byte{0x0a, 0x08, 0x22, 0x06, 0x10, 0x64, 0x18, 0x0f, 0x28, 0x14}
	if _, err := db.Exec("INSERT INTO gen_metadata(idx,data) VALUES(0,?)", blob); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	d := &discoverer{ctx: context.Background(), opts: Options{
		GeminiDir: root,
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "lsof" {
				return nil, errors.New("unexpected tool")
			}
			return nil, nil
		},
	}}
	groups := []ProcessGroup{{Tool: "Antigravity", Root: ProcessInfo{PID: 100, Command: "agy --conversation known"}}}
	if err := d.enrichAntigravitySessions(groups); err != nil {
		t.Fatal(err)
	}
	info := groups[0].Sessions[0]
	if info.Tokens != (TokenBreakdown{Input: 100, Output: 15, CacheRead: 20}) || info.TokensUsed != 135 {
		t.Error("available database totals were not exposed on session metadata")
	}
	after, err := os.ReadFile(database)
	if err != nil || string(before) != string(after) {
		t.Error("session enrichment changed the usage database")
	}
}

func TestAntigravityWorkspaceFallbackWithoutOpenFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "antigravity-cli")
	writeAntigravityFixture(t, dir, "known", antigravityUserFixture+"\n")
	fixtureFile(t, filepath.Join(dir, "cache", "last_conversations.json"), `{"/work/repo":"known"}`)
	d := &discoverer{ctx: context.Background(), opts: Options{GeminiDir: root,
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "lsof" && strings.Contains(strings.Join(args, " "), "cwd") {
				return []byte("p100\nn/work/repo\np101\nn/work/repo\n"), nil
			}
			return nil, nil
		}}}
	groups := []ProcessGroup{{Tool: "Antigravity", Root: ProcessInfo{PID: 100, Command: "agy"}}}
	if err := d.enrichAntigravitySessions(groups); err != nil || len(groups[0].Sessions) != 1 || groups[0].Sessions[0].ID != "known" {
		t.Fatal("workspace resume cache did not bind a normal launch")
	}
	groups = []ProcessGroup{
		{Tool: "Antigravity", Root: ProcessInfo{PID: 100, Command: "agy"}},
		{Tool: "Antigravity", Root: ProcessInfo{PID: 101, Command: "agy"}},
	}
	if err := d.enrichAntigravitySessions(groups); err == nil || len(groups[0].Sessions) != 0 || len(groups[1].Sessions) != 0 {
		t.Fatal("ambiguous same-workspace runtimes were guessed")
	}
}
