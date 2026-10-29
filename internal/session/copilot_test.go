package session

import (
	"os"
	"strings"
	"testing"
	"time"
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
