package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/testenv"
)

const testCopilotSessionID = "dc315f47-bf71-4447-b252-5ba9cbad8b51"

func TestClassifyCopilot(t *testing.T) {
	d := &discoverer{}
	tests := []struct {
		command string
		want    string
	}{
		{"copilot", "Copilot"},
		{"node /opt/homebrew/lib/node_modules/@github/copilot/index.js --resume", "Copilot"},
		{"/Applications/GitHub Copilot.app/Contents/MacOS/GitHub Copilot", ""},
		{"/Applications/GitHub Copilot.app/Contents/Frameworks/GitHub Copilot Helper (Renderer).app/Contents/MacOS/GitHub Copilot Helper (Renderer) --type=renderer", ""},
		{"/Applications/GitHub Copilot.app/Contents/Resources/copilot --server", ""},
		{"node /Applications/Copilot.app/Contents/Resources/app/index.js", ""},
		{"/Library/Caches/github-copilot-sdk/cli/1.0.0/copilot --stdio", ""},
		{"/Applications/Microsoft Teams.app/Contents/Helpers/Microsoft Teams WebView.app/Contents/MacOS/Microsoft Teams WebView --embedded-browser-webview=1 --isolate-origins=https://copilot.teams.cloud.microsoft,https://m365copilotapp.svc.cloud.microsoft --user-data-dir=/Users/test/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView", ""},
	}
	for _, tt := range tests {
		if got := d.classifyProcess(tt.command); got != tt.want {
			t.Errorf("classifyProcess(%q) = %q, want %q", tt.command, got, tt.want)
		}
	}
}

func TestCopilotSessionIDFromStatePath(t *testing.T) {
	d := &discoverer{}
	path := "/Users/test/.copilot/session-state/" + testCopilotSessionID + "/events.jsonl"
	id, ok := d.copilotSessionIDFromStatePath(path)
	if !ok || id != testCopilotSessionID {
		t.Fatalf("session ID = %q, ok=%v", id, ok)
	}
	if _, ok := d.copilotSessionIDFromStatePath("/Users/test/.copilot/session-state/not-a-uuid/events.jsonl"); ok {
		t.Fatal("invalid session-state path accepted")
	}
}

func TestCopilotSessionIDFromCommand(t *testing.T) {
	d := &discoverer{}
	commands := []string{
		"copilot --resume=" + testCopilotSessionID,
		"copilot --resume " + testCopilotSessionID,
		"copilot --session-id=" + testCopilotSessionID,
		"copilot -r " + testCopilotSessionID,
	}
	for _, command := range commands {
		id, ok := d.copilotSessionIDFromCommand(command)
		if !ok || id != testCopilotSessionID {
			t.Fatalf("%q produced ID %q, ok=%v", command, id, ok)
		}
	}
	if _, ok := d.copilotSessionIDFromCommand("copilot --continue"); ok {
		t.Fatal("command without explicit session ID produced one")
	}
}

func TestScanCopilotLogSessionIDUsesLatestMarker(t *testing.T) {
	older := "3bbbd84c-893d-4f44-bffd-8107e060ed7e"
	log := strings.Join([]string{
		"2026-08-01T01:00:00Z [INFO] Workspace initialized: " + older + " (checkpoints: 0)",
		"2026-08-01T01:01:00Z [INFO] Registering foreground session: " + testCopilotSessionID,
	}, "\n")
	if id := (&discoverer{}).scanCopilotLogSessionID(strings.NewReader(log)); id != testCopilotSessionID {
		t.Fatalf("log session ID = %q", id)
	}
}

func TestCopilotSessionIDFromPIDLog(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(home+"/logs", 0o700); err != nil {
		t.Fatal(err)
	}
	log := "2026-08-01T01:01:00Z [INFO] Registering foreground session: " + testCopilotSessionID
	if err := os.WriteFile(home+"/logs/process-123456789-300.log", []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	group := ProcessGroup{
		Tool:      "Copilot",
		Root:      ProcessInfo{PID: 300, Command: "copilot"},
		Processes: []ProcessInfo{{PID: 300, Command: "copilot"}},
	}
	id, ok := (&discoverer{}).copilotSessionIDFromGroupLogs(home, group)
	if !ok || id != testCopilotSessionID {
		t.Fatalf("PID log produced ID %q, ok=%v", id, ok)
	}
}

func TestReadCopilotWorkspace(t *testing.T) {
	path := t.TempDir() + "/workspace.yaml"
	contents := strings.Join([]string{
		"id: " + testCopilotSessionID,
		"cwd: /workspace/firekeeper",
		"git_root: /workspace/firekeeper",
		"repository: DanBradbury/firekeeper",
		"host_type: github",
		"branch: main",
		"client_name: github/cli",
		"name: Add Copilot metadata",
		"created_at: 2026-08-01T01:02:03.456Z",
		"updated_at: 2026-08-01T02:03:04.567Z",
	}, "\n")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	metadata, err := ReadCopilotWorkspace(path)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ID != testCopilotSessionID || metadata.CWD != "/workspace/firekeeper" {
		t.Fatalf("workspace identity = %q, %q", metadata.ID, metadata.CWD)
	}
	if metadata.Repository != "DanBradbury/firekeeper" || metadata.Branch != "main" {
		t.Fatalf("repository metadata = %q, %q", metadata.Repository, metadata.Branch)
	}
	if metadata.ClientName != "github/cli" || metadata.Name != "Add Copilot metadata" {
		t.Fatalf("session metadata = %q, %q", metadata.ClientName, metadata.Name)
	}
	if metadata.UpdatedAt.IsZero() {
		t.Fatal("updated timestamp was not parsed")
	}
}

func TestScanCopilotEventsTracksActiveWaitingAndInput(t *testing.T) {
	d := &discoverer{}
	pending := strings.Join([]string{
		`{"type":"session.start","timestamp":"2026-08-01T01:00:00Z","data":{}}`,
		`{"type":"user.message","timestamp":"2026-08-01T01:00:01Z","data":{}}`,
		`{"type":"assistant.turn_start","timestamp":"2026-08-01T01:00:02Z","data":{}}`,
		`{"type":"assistant.message","timestamp":"2026-08-01T01:00:03Z","data":{"model":"claude-sonnet-4.6","outputTokens":120}}`,
		`{"type":"permission.requested","timestamp":"2026-08-01T01:00:04Z","data":{"requestId":"permission-1"}}`,
	}, "\n")
	metadata, err := d.scanCopilotEvents(strings.NewReader(pending))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.State != SessionStateNeedsInput {
		t.Fatalf("pending state = %s", metadata.State)
	}
	if metadata.Model != "claude-sonnet-4.6" || metadata.TokensUsed != 120 {
		t.Fatalf("model/tokens = %q/%d", metadata.Model, metadata.TokensUsed)
	}

	active := pending + "\n" +
		`{"type":"permission.completed","timestamp":"2026-08-01T01:00:05Z","data":{"requestId":"permission-1"}}`
	metadata, err = d.scanCopilotEvents(strings.NewReader(active))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.State != SessionStateActive {
		t.Fatalf("resolved state = %s", metadata.State)
	}

	waiting := active + "\n" +
		`{"type":"assistant.turn_end","timestamp":"2026-08-01T01:00:06Z","data":{}}`
	metadata, err = d.scanCopilotEvents(strings.NewReader(waiting))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.State != SessionStateWaiting {
		t.Fatalf("completed state = %s", metadata.State)
	}
	if metadata.UpdatedAt != time.Date(2026, 8, 1, 1, 0, 6, 0, time.UTC) {
		t.Fatalf("updated time = %s", metadata.UpdatedAt)
	}
}

func TestCopilotStoredTokenBreakdown(t *testing.T) {
	sqlite := testenv.NeedTool(t, "sqlite3")
	for _, tt := range []struct {
		name    string
		columns string
		values  string
		want    TokenBreakdown
		total   int64
		longLog bool
	}{
		{
			name:    "full session beyond event tail",
			columns: ", cache_read_tokens INTEGER, cache_write_tokens INTEGER",
			values:  "(1, '%s', 'main-model', 1000, 50, 800, 100), (2, '%s', 'main-model', 200, 5, NULL, NULL)",
			want:    TokenBreakdown{Input: 300, Output: 55, CacheRead: 800, CacheWrite: 100},
			total:   1255,
			longLog: true,
		},
		{
			name:   "older schema without cache columns",
			values: "(1, '%s', 'main-model', 1000, 50), (2, '%s', 'main-model', 200, 5)",
			want:   TokenBreakdown{Input: 1200, Output: 55},
			total:  1255,
		},
		{
			name:    "read cache only",
			columns: ", cache_read_tokens INTEGER",
			values:  "(1, '%s', 'main-model', 1000, 50, 800), (2, '%s', 'main-model', 200, 5, NULL)",
			want:    TokenBreakdown{Input: 400, Output: 55, CacheRead: 800},
			total:   1255,
		},
		{
			name:    "null and negative counters",
			columns: ", cache_read_tokens INTEGER, cache_write_tokens INTEGER",
			values:  "(1, '%s', 'main-model', 100, 50, NULL, -10), (2, '%s', 'main-model', NULL, -5, NULL, NULL)",
			want:    TokenBreakdown{Input: 100, Output: 50},
			total:   150,
		},
		{
			name:   "no recorded usage falls back to events",
			values: "(1, '%s', 'main-model', NULL, NULL), (2, '%s', 'main-model', NULL, NULL)",
			want:   TokenBreakdown{Input: 9, Output: 1},
			total:  1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "session-store.db")
			schema := `
					CREATE TABLE sessions (
						id TEXT PRIMARY KEY, cwd TEXT, repository TEXT, host_type TEXT,
						branch TEXT, summary TEXT, created_at TEXT, updated_at TEXT
					);
					CREATE TABLE assistant_usage_events (
						id INTEGER PRIMARY KEY, session_id TEXT, model TEXT,
						input_tokens INTEGER, output_tokens INTEGER` + tt.columns + `
					);
					INSERT INTO sessions (id) VALUES ('` + testCopilotSessionID + `'), ('other-session');
					INSERT INTO assistant_usage_events VALUES ` + fmt.Sprintf(tt.values, testCopilotSessionID, testCopilotSessionID) + `;
					INSERT INTO assistant_usage_events (id, session_id, input_tokens, output_tokens)
						VALUES (3, 'other-session', 99999, 99999);
				`
			if output, err := exec.Command(sqlite, path, schema).CombinedOutput(); err != nil {
				t.Fatalf("create synthetic Copilot store: %v: %s", err, output)
			}
			directory := filepath.Join(home, "session-state", testCopilotSessionID)
			fixtureFile(t, filepath.Join(directory, "workspace.yaml"), "cwd: /workspace/test\n")
			events := `{"type":"model.model_call_success","data":{"responseChunk":{"usage":{"prompt_tokens":9,"completion_tokens":1}}}}` + "\n"
			if tt.longLog {
				events += `{"type":"system.message","data":{"content":"` + strings.Repeat("x", int(CopilotEventTailBytes)) + "\"}}\n"
			}
			events += "{\"type\":\"assistant.turn_end\",\"data\":{\"outputTokens\":1}}\n"
			fixtureFile(t, filepath.Join(directory, "events.jsonl"), events)
			d := standalone()
			stored, err := d.readCopilotStoredMetadata(home, map[string]bool{testCopilotSessionID: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(stored) != 1 {
				t.Fatalf("got %d stored sessions, want only the requested session", len(stored))
			}
			info, err := d.loadCopilotSession(home, testCopilotSessionID, stored[testCopilotSessionID])
			if err != nil {
				t.Fatal(err)
			}
			if info.Tokens != tt.want || info.TokensUsed != tt.total {
				t.Fatalf("tokens = %+v, total = %d; want %+v, total = %d", info.Tokens, info.TokensUsed, tt.want, tt.total)
			}
			if info.State != SessionStateWaiting {
				t.Fatalf("state = %s, want WAITING", info.State)
			}
		})
	}
}

func TestLoadCopilotSessionCombinesWorkspaceEventsAndStore(t *testing.T) {
	home := t.TempDir()
	directory := home + "/session-state/" + testCopilotSessionID
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := strings.Join([]string{
		"id: " + testCopilotSessionID,
		"cwd: /workspace/firekeeper",
		"repository: DanBradbury/firekeeper",
		"host_type: github",
		"branch: feature/copilot",
		"client_name: github/cli",
		"name: Copilot metadata support",
		"updated_at: 2026-08-01T02:03:04Z",
	}, "\n")
	if err := os.WriteFile(directory+"/workspace.yaml", []byte(workspace), 0o600); err != nil {
		t.Fatal(err)
	}
	events := strings.Join([]string{
		`{"type":"assistant.turn_start","timestamp":"2026-08-01T02:03:05Z","data":{}}`,
		`{"type":"assistant.message","timestamp":"2026-08-01T02:03:06Z","data":{"model":"claude-sonnet-4.6","outputTokens":50}}`,
	}, "\n")
	if err := os.WriteFile(directory+"/events.jsonl", []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := (&discoverer{}).loadCopilotSession(home, testCopilotSessionID, CopilotStoredMetadata{
		ID:         testCopilotSessionID,
		TokensUsed: 321,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "Copilot metadata support" || info.State != SessionStateActive {
		t.Fatalf("session name/state = %q/%s", info.Name, info.State)
	}
	if info.Model != "claude-sonnet-4.6" || info.TokensUsed != 321 {
		t.Fatalf("session model/tokens = %q/%d", info.Model, info.TokensUsed)
	}
	if info.Repository != "DanBradbury/firekeeper" || info.GitBranch != "feature/copilot" {
		t.Fatalf("session repository/branch = %q/%q", info.Repository, info.GitBranch)
	}
}
