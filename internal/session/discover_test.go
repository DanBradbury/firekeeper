package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixtureFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverFixtureTree(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	cwd := filepath.Join(project, "src")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	codexID := "11111111-1111-4111-8111-111111111111"
	copilotID := "22222222-2222-4222-8222-222222222222"
	rollout := filepath.Join(home, "codex", "sessions", "2026", "10", "07", "rollout-2026-10-07-"+codexID+".jsonl")
	fixtureFile(t, rollout, "{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_complete\"}}\n")
	fixtureFile(t, filepath.Join(home, "codex", "state_5.sqlite"), "")
	fixtureFile(t, filepath.Join(home, "copilot", "session-state", copilotID, "workspace.yaml"), fmt.Sprintf("id: %s\ncwd: %s\nname: fixture\n", copilotID, cwd))
	fixtureFile(t, filepath.Join(home, "copilot", "session-state", copilotID, "events.jsonl"), "{\"type\":\"assistant.turn_start\",\"data\":{\"model\":\"fixture-model\"}}\nmalformed\n")
	fixtureFile(t, filepath.Join(home, "kimi", "sessions", "work", "kimi-id", "state.json"), fmt.Sprintf(`{"title":"Kimi fixture","workDir":%q,"updatedAt":"2026-10-07T12:00:00Z"}`, cwd))
	fixtureFile(t, filepath.Join(home, "kimi", "sessions", "work", "broken", "state.json"), "{")
	opts := Options{Home: home, CodexHome: filepath.Join(home, "codex"), CopilotHome: filepath.Join(home, "copilot"), KimiHome: filepath.Join(home, "kimi")}
	opts.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch name {
		case "ps":
			return []byte("10 1 pts/0 00:01 codex\n11 10 pts/0 00:01 codex\n20 1 pts/1 00:02 copilot --resume " + copilotID + "\n30 1 pts/2 00:03 kimi\n40 1 ? 00:04 opencode\nmalformed\n"), nil
		case "lsof":
			if reflect.DeepEqual(args, []string{"-Fn", "-p", "10"}) {
				return []byte("p10\nn" + rollout + "\n"), nil
			}
			return []byte("p30\nn" + cwd + "\n"), nil
		case "sqlite3":
			if len(args) < 4 || args[0] != "-readonly" || args[1] != "-json" {
				t.Fatalf("database command is not read-only: %v", args)
			}
			if args[2] == filepath.Join(home, "codex", "state_5.sqlite") {
				b, _ := json.Marshal([]ThreadMetadataRow{{ID: codexID, Name: "Codex fixture", CWD: cwd, Model: "codex-model", TokensUsed: 123}})
				return b, nil
			}
			return nil, errors.New("optional store unavailable")
		case "git":
			if !reflect.DeepEqual(args, []string{"-C", cwd, "rev-parse", "--show-toplevel"}) {
				t.Fatalf("unexpected git arguments: %v", args)
			}
			return []byte(project + "\n"), nil
		default:
			t.Fatalf("unexpected command %s", name)
			return nil, nil
		}
	}
	metas, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 4 {
		t.Fatalf("got %d sessions, want four runtimes", len(metas))
	}
	expected := map[string]struct {
		id    string
		state SessionState
	}{"codex": {codexID, SessionStateWaiting}, "copilot": {copilotID, SessionStateActive}, "kimi": {"kimi-id", SessionStateActive}, "opencode": {"", SessionStateUnknown}}
	for _, m := range metas {
		want := expected[m.Provider]
		if m.ID != want.id || m.State != want.state || m.Project != filepath.Base(project) || m.CWD != cwd {
			t.Errorf("unexpected %s metadata: %+v", m.Provider, m)
		}
		if !ValidUUID(m.MachineID) {
			t.Errorf("invalid machine identity %q", m.MachineID)
		}
		if m.Provider == "codex" && (len(m.Runtime.Processes) != 2 || m.TokensUsed != 123) {
			t.Error("Codex grouping or enrichment lost")
		}
	}
	again, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(metas, again) {
		t.Error("discovery or machine identity is unstable")
	}
	stored, err := os.ReadFile(filepath.Join(home, ".firekeeper", "machine-id"))
	if err != nil || strings.TrimSpace(string(stored)) != metas[0].MachineID {
		t.Fatal("machine identity was not persisted")
	}
}

func TestDiscoverFailureAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
	}{{"process failure", false}, {"cancelled", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			metas, err := Discover(ctx, Options{Home: t.TempDir(), Run: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("fixture failure") }})
			if err == nil || metas != nil {
				t.Fatalf("got %v, %v", metas, err)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}

func TestDiscoverPartialMetadataAndProjectFallback(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "plain")
	metas, err := Discover(context.Background(), Options{Home: home, KimiHome: filepath.Join(home, "missing"), Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ps":
			return []byte("30 1 ? 00:01 kimi\n40 1 ? 00:01 opencode"), nil
		case "lsof":
			return []byte("p30\nn" + cwd), nil
		default:
			return nil, errors.New("not a repository")
		}
	}})
	var warning *Warning
	if !errors.As(err, &warning) || len(metas) != 2 {
		t.Fatalf("partial scan lost: %v, %v", metas, err)
	}
	for _, m := range metas {
		if m.Project != filepath.Base(cwd) || m.State != SessionStateUnknown {
			t.Errorf("bad fallback: %+v", m)
		}
	}
}

func TestMachineIDConcurrentAndInvalid(t *testing.T) {
	home := t.TempDir()
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := machineID(home)
			if err != nil {
				t.Error(err)
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Error("concurrent identities differ")
		}
	}
	path := filepath.Join(home, ".firekeeper", "machine-id")
	fixtureFile(t, path, "invalid")
	if _, err := machineID(home); err == nil {
		t.Error("invalid persisted identity accepted")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "invalid" {
		t.Error("existing identity overwritten")
	}
}

func TestMetaReportingContract(t *testing.T) {
	started := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	meta := Meta{ID: "fixture", MachineID: "machine", Provider: "codex", Project: "project", Title: "fixture title", Branch: "main", State: SessionStateNeedsInput, StartedAt: optionalTime(started), LastActivityAt: optionalTime(started), EventCount: 4, Input: 10, Output: 20, Cache: 3}
	encoded, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"machine_id", "session_id", "provider", "cwd", "project", "branch", "model", "state", "title", "started_at", "last_activity_at", "event_count", "input", "output", "cache"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing contract field %s", key)
		}
	}
	if string(got["state"]) != `"NEEDS_INPUT"` {
		t.Errorf("unexpected wire state %s", got["state"])
	}
	var decoded Meta
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(meta, decoded) {
		t.Error("metadata does not round-trip")
	}
	for _, state := range []SessionState{SessionStateActive, SessionStateWaiting, SessionStateNeedsInput, SessionStateEnded, SessionStateUnknown} {
		b, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		var again SessionState
		if err := json.Unmarshal(b, &again); err != nil || again != state {
			t.Error("state does not round-trip")
		}
	}
	if SessionStateNeedsInput.String() != "NEEDS INPUT" {
		t.Error("TUI label changed")
	}
	if optionalTime(time.Time{}) != nil {
		t.Error("unavailable timestamp should be null")
	}
}
