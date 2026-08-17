package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestResolveHerdrTargetPrefersNativeSessionIdentity(t *testing.T) {
	run := fakeHerdrRunner(map[string]string{
		"session list --json":          `{"sessions":[{"name":"default","default":true,"running":true},{"name":"work","running":true}]}`,
		"--session default agent list": `{"result":{"agents":[{"agent":"codex","pane_id":"1-1","cwd":"/workspace/shared","agent_session":{"agent":"codex","kind":"id","value":"other"}}]}}`,
		"--session work agent list":    `{"result":{"agents":[{"agent":"codex","pane_id":"2-3","cwd":"/workspace/shared","agent_session":{"agent":"codex","kind":"id","value":"session-123"}}]}}`,
	})

	got, ok, err := resolveHerdrTarget(context.Background(), terminalSwitchTarget{
		provider:  "Codex",
		cwd:       "/workspace/shared",
		sessionID: "session-123",
	}, run)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.session != "work" || got.paneID != "2-3" {
		t.Fatalf("resolved target = %#v, %t", got, ok)
	}
}

func TestResolveHerdrTargetMatchesForegroundProcessPID(t *testing.T) {
	run := fakeHerdrRunner(map[string]string{
		"session list --json":                            `{"sessions":[{"name":"default","default":true,"running":true}]}`,
		"--session default agent list":                   `{"result":{"agents":[{"agent":"codex","pane_id":"1-1","cwd":"/workspace/shared"},{"agent":"codex","pane_id":"1-2","cwd":"/workspace/shared"}]}}`,
		"--session default pane process-info --pane 1-1": `{"result":{"process_info":{"foreground_processes":[{"pid":100}]}}}`,
		"--session default pane process-info --pane 1-2": `{"result":{"process_info":{"foreground_processes":[{"pid":202},{"pid":203}]}}}`,
	})

	got, ok, err := resolveHerdrTarget(context.Background(), terminalSwitchTarget{
		provider:    "Codex",
		cwd:         "/workspace/shared",
		processPIDs: []int{201, 202},
	}, run)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.paneID != "1-2" {
		t.Fatalf("resolved target = %#v, %t", got, ok)
	}
}

func TestResolveHerdrTargetUsesOnlyUniqueWorkingDirectory(t *testing.T) {
	tests := []struct {
		name      string
		agents    string
		wantPane  string
		wantFound bool
	}{
		{
			name:      "unique",
			agents:    `{"result":{"agents":[{"agent":"copilot","pane_id":"1-1","foreground_cwd":"/workspace/api"},{"agent":"copilot","pane_id":"1-2","cwd":"/workspace/web"}]}}`,
			wantPane:  "1-1",
			wantFound: true,
		},
		{
			name:      "ambiguous",
			agents:    `{"result":{"agents":[{"agent":"copilot","pane_id":"1-1","foreground_cwd":"/workspace/api"},{"agent":"copilot","pane_id":"1-2","cwd":"/workspace/api"}]}}`,
			wantFound: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := fakeHerdrRunner(map[string]string{
				"session list --json":          `{"sessions":[{"name":"default","running":true}]}`,
				"--session default agent list": test.agents,
			})
			got, ok, err := resolveHerdrTarget(context.Background(), terminalSwitchTarget{
				provider: "Copilot",
				cwd:      "/workspace/api/.",
			}, run)
			if err != nil {
				t.Fatal(err)
			}
			if ok != test.wantFound || got.paneID != test.wantPane {
				t.Fatalf("resolved target = %#v, %t", got, ok)
			}
		})
	}
}

func TestListHerdrAgentsHandlesPartialAndMalformedResponses(t *testing.T) {
	tests := []struct {
		name    string
		outputs map[string]string
		want    int
		wantErr bool
	}{
		{
			name: "skips stopped and malformed named session",
			outputs: map[string]string{
				"session list --json":          `{"sessions":[{"name":"default","running":true},{"name":"broken","running":true},{"name":"stopped","running":false}]}`,
				"--session default agent list": `{"result":{"agents":[{"agent":"codex","pane_id":"1-1"}]}}`,
				"--session broken agent list":  `{malformed`,
			},
			want: 1,
		},
		{
			name: "malformed session list",
			outputs: map[string]string{
				"session list --json": `{malformed`,
			},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := listHerdrAgents(context.Background(), fakeHerdrRunner(test.outputs))
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %t", err, test.wantErr)
			}
			if len(got) != test.want {
				t.Fatalf("agent count = %d, want %d", len(got), test.want)
			}
		})
	}
}

func TestHerdrClientTTYFromProcessListMatchesSession(t *testing.T) {
	output := strings.Join([]string{
		"100 1 ttys001 /usr/local/bin/herdr",
		"101 1 ?? /usr/local/bin/herdr server",
		"102 1 ttys002 /usr/local/bin/herdr --session work",
		"103 1 ttys003 /usr/local/bin/herdr session attach other",
		"104 1 ttys004 /usr/local/bin/herdr --session work agent list",
	}, "\n")
	if got := herdrClientTTYFromProcessList(output, "default"); got != "ttys001" {
		t.Fatalf("default client TTY = %q", got)
	}
	if got := herdrClientTTYFromProcessList(output, "work"); got != "ttys002" {
		t.Fatalf("named client TTY = %q", got)
	}
	if got := herdrClientTTYFromProcessList(output, "missing"); got != "" {
		t.Fatalf("missing client TTY = %q", got)
	}
}

func TestFocusHerdrTargetSelectsPaneAndOuterClientTTY(t *testing.T) {
	outputs := map[string]string{
		"session list --json":            `{"sessions":[{"name":"work","running":true}]}`,
		"--session work agent list":      `{"result":{"agents":[{"agent":"codex","pane_id":"2-3","agent_session":{"agent":"codex","kind":"id","value":"session-123"}}]}}`,
		"--session work agent focus 2-3": `{"result":{"type":"agent_focused"}}`,
	}
	var commands []string
	run := func(_ context.Context, arguments ...string) ([]byte, error) {
		key := strings.Join(arguments, " ")
		commands = append(commands, key)
		output, ok := outputs[key]
		if !ok {
			return nil, errors.New("unexpected Herdr command: " + key)
		}
		return []byte(output), nil
	}
	resolved, found, err := focusHerdrTargetWith(
		context.Background(),
		terminalSwitchTarget{provider: "Codex", sessionID: "session-123"},
		run,
		func(context.Context) ([]byte, error) {
			return []byte("200 1 ttys009 /usr/local/bin/herdr --session work"), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !found || resolved.session != "work" || resolved.paneID != "2-3" || resolved.clientTTY != "ttys009" {
		t.Fatalf("focused target = %#v, %t", resolved, found)
	}
	if got := commands[len(commands)-1]; got != "--session work agent focus 2-3" {
		t.Fatalf("last Herdr command = %q", got)
	}
}

func TestNewTerminalSwitchTargetIncludesSessionAndProcessIdentity(t *testing.T) {
	target := newTerminalSwitchTarget(processGroup{
		tool: "Codex",
		root: processInfo{pid: 100, tty: "ttys001"},
		processes: []processInfo{
			{pid: 100},
			{pid: 101},
		},
		sessions: []sessionInfo{{id: "session-1", cwd: "/workspace/firekeeper"}},
	}, 0)
	if target.provider != "Codex" || target.sessionID != "session-1" || target.cwd != "/workspace/firekeeper" {
		t.Fatalf("terminal target = %#v", target)
	}
	if len(target.processPIDs) != 2 || target.processPIDs[0] != 100 || target.processPIDs[1] != 101 {
		t.Fatalf("terminal target PIDs = %v", target.processPIDs)
	}
}

func fakeHerdrRunner(outputs map[string]string) herdrCommandRunner {
	return func(_ context.Context, arguments ...string) ([]byte, error) {
		key := strings.Join(arguments, " ")
		output, ok := outputs[key]
		if !ok {
			return nil, errors.New("unexpected Herdr command: " + key)
		}
		return []byte(output), nil
	}
}
