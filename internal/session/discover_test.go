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
	"sync/atomic"
	"testing"
	"time"

	"github.com/firekeeper-ai/firekeeper/internal/parallel"
	"github.com/firekeeper-ai/firekeeper/pkg/event"
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
	// The kimi process (etime 00:03) started just before the session was
	// last updated, so the session is fresh for it.
	setKimiNow(t, time.Date(2026, 10, 7, 12, 0, 2, 0, time.UTC))
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
				// Run is called from the linking goroutines, where a test
				// may not call Fatalf.
				t.Errorf("database command is not read-only: %v", args)
				return nil, errors.New("database command is not read-only")
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
			t.Errorf("unexpected command %s", name)
			return nil, errors.New("unexpected command")
		}
	}
	metas, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 4 {
		t.Fatalf("got %d sessions, want four runtimes", len(metas))
	}
	expected := map[event.Provider]struct {
		id    string
		state State
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
	// Provider is typed, but its JSON is the plain id, and it decodes back.
	if string(got["provider"]) != `"codex"` {
		t.Errorf("unexpected wire provider %s", got["provider"])
	}
	var back Meta
	if err := json.Unmarshal(encoded, &back); err != nil || back.Provider != event.ProviderCodex {
		t.Errorf("decoded provider %q, %v; want codex", back.Provider, err)
	}
	// Decoding does not validate: an unknown id is kept as is, for the
	// checks that already handle data from other programs to reject.
	if err := json.Unmarshal([]byte(`{"provider":"zed"}`), &back); err != nil || back.Provider != "zed" {
		t.Errorf("decoded unknown provider %q, %v; want it kept", back.Provider, err)
	}
	var decoded Meta
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(meta, decoded) {
		t.Error("metadata does not round-trip")
	}
	for _, state := range []State{SessionStateActive, SessionStateWaiting, SessionStateNeedsInput, SessionStateEnded, SessionStateUnknown} {
		b, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		var again State
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

func TestClassifyIgnoresFirekeeperProcesses(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{"/home/u/.local/bin/firekeeper daemon --quiet --server http://127.0.0.1:7777 --provider codex --provider copilot --provider claude", ""},
		{"firekeeper report --provider copilot --dry-run", ""},
		{"firekeeper", ""},
		{"/tmp/go-build123/b001/exe/firekeeper serve", ""},
		{"copilot --resume", "Copilot"},
		// Step 0 row 3: the fixture names the real npm entry point.
		{"node /opt/homebrew/lib/node_modules/@github/copilot/index.js --log-dir /home/u/firekeeper-logs", "Copilot"},
		{"node /opt/copilot/index.js --log-dir /home/u/firekeeper-logs", ""},
		{"codex resume", "Codex"},
	}
	for _, tt := range tests {
		if got := classifyLine(tt.command); got != tt.want {
			t.Errorf("classify(%q) = %q, want %q", tt.command, got, tt.want)
		}
	}
}

// Tools are never linked and never emitted (invariants 6 and 8): a Kimi
// tool in the same directory as a Kimi session neither gets the session nor
// appears, and a Claude tool doesn't appear.
func TestDiscoverNeverLinksOrEmitsTools(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "project")
	sessionDir := filepath.Join(home, "kimi", "sessions", "wd_project_0123456789ab", "session_abc")
	fixtureFile(t, filepath.Join(sessionDir, "state.json"), fmt.Sprintf(`{"id":"session_abc","cwd":%q,"createdAt":1791460800000,"updatedAt":1791460860000,"title":"Kimi now"}`, cwd))
	// Session updated at 1791460860000 ms; the kimi process (etime 00:01)
	// started a second before.
	setKimiNow(t, time.UnixMilli(1791460860000))
	// Run is called from the linking goroutines, so the list is guarded.
	var mu sync.Mutex
	var kimiPIDs []string
	metas, err := Discover(context.Background(), Options{Home: home, KimiHome: filepath.Join(home, "kimi"), ClaudeHome: filepath.Join(home, "claude"), Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ps":
			return []byte("30 1 ttys001 00:01 kimi\n31 1 ttys002 00:01 kimi export\n32 1 ? 00:01 claude mcp list\n"), nil
		case "lsof":
			for i, a := range args {
				if a == "-p" && i+1 < len(args) && strings.Contains(args[len(args)-4], "cwd") {
					mu.Lock()
					kimiPIDs = append(kimiPIDs, args[i+1])
					mu.Unlock()
				}
			}
			return []byte("p30\nn" + cwd + "\np31\nn" + cwd + "\n"), nil
		default:
			return nil, errors.New("not a repository")
		}
	}})
	var warning *Warning
	if err != nil && !errors.As(err, &warning) {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].PID != 30 || metas[0].ID != "session_abc" {
		t.Fatalf("metas = %+v, want only the Kimi session 30 linked to session_abc", metas)
	}
	for _, pids := range kimiPIDs {
		if strings.Contains(pids, "31") {
			t.Fatalf("Kimi's Link looked up the tool's directory: -p %s", pids)
		}
	}
}

// linkAdapter wraps a real adapter and replaces its Link, so a test decides
// what linking does and when it returns.
type linkAdapter struct {
	providerAdapter
	link func(groups []*ProcessGroup) error
}

func (a linkAdapter) Link(_ *discoverer, groups []*ProcessGroup) error { return a.link(groups) }

// replaceLinks swaps every adapter's Link for link, for the length of the
// test. index is the adapter's place in adapters.
func replaceLinks(t *testing.T, link func(index int, provider event.Provider, groups []*ProcessGroup) error) {
	t.Helper()
	saved := adapters
	t.Cleanup(func() { adapters = saved })
	wrapped := make([]providerAdapter, len(saved))
	for index, a := range saved {
		wrapped[index] = linkAdapter{providerAdapter: a, link: func(groups []*ProcessGroup) error {
			return link(index, a.Provider(), groups)
		}}
	}
	adapters = wrapped
}

// everyProviderPS lists one session for each provider.
const everyProviderPS = "10 1 ttys001 00:01 codex\n20 1 ttys002 00:01 kimi\n30 1 ttys003 00:01 claude\n40 1 ttys004 00:01 opencode\n50 1 ttys005 00:01 gemini\n60 1 ttys006 00:01 cursor-agent\n70 1 ttys007 00:01 copilot\n"

// requireEveryProvider fails the test when everyProviderPS has no session for
// a registered provider, so a provider added later is reported as a missing
// fixture line and not as a linking failure.
func requireEveryProvider(t *testing.T) {
	t.Helper()
	d := &discoverer{ctx: context.Background(), opts: Options{Run: psOnly(everyProviderPS)}}
	seen := map[event.Provider]bool{}
	for _, g := range d.groupProcesses(d.parseProcesses(everyProviderPS)) {
		if g.Role == RoleSession {
			seen[g.Provider] = true
		}
	}
	for _, a := range adapters {
		if !seen[a.Provider()] {
			t.Fatalf("everyProviderPS has no session for %s: add one", a.Provider())
		}
	}
}

// psOnly answers ps with the fixture list and fails every other command.
func psOnly(ps string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "ps" {
			return []byte(ps), nil
		}
		return nil, os.ErrNotExist
	}
}

// linkWait bounds how long a fixture Link waits for another one. Only a
// regression reaches it.
const linkWait = 2 * time.Second

// Providers are linked at the same time: every Link here waits until all of
// them have started, which never happens when they run one after another.
func TestDiscoverLinksProvidersConcurrently(t *testing.T) {
	requireEveryProvider(t)
	var started sync.WaitGroup
	started.Add(len(adapters))
	all := make(chan struct{})
	go func() {
		started.Wait()
		close(all)
	}()
	var alone atomic.Int32
	replaceLinks(t, func(int, event.Provider, []*ProcessGroup) error {
		started.Done()
		if alone.Load() > 0 {
			return nil // already failed; do not wait again
		}
		select {
		case <-all:
		case <-time.After(linkWait):
			alone.Add(1)
		}
		return nil
	})
	metas, err := Discover(context.Background(), Options{Home: t.TempDir(), Run: psOnly(everyProviderPS)})
	if err != nil {
		t.Fatal(err)
	}
	if alone.Load() > 0 {
		t.Fatal("a provider's Link ran without the others having started: linking is not concurrent")
	}
	if len(metas) != len(adapters) {
		t.Fatalf("got %d sessions, want one for each of the %d providers", len(metas), len(adapters))
	}
}

// The warning lists providers in registry order, whichever Link finishes
// first: here they finish in reverse.
func TestDiscoverLinkWarningsKeepAdapterOrder(t *testing.T) {
	requireEveryProvider(t)
	var want []string
	for _, a := range adapters {
		want = append(want, ToolName(a.Provider())+": fixture "+string(a.Provider())+" failure")
	}
	count := len(adapters)
	done := make([]chan struct{}, count+1)
	for i := range done {
		done[i] = make(chan struct{})
	}
	close(done[count])
	var mu sync.Mutex
	var finished []int
	replaceLinks(t, func(index int, provider event.Provider, _ []*ProcessGroup) error {
		// Finish only after the provider registered next has finished.
		select {
		case <-done[index+1]:
		case <-time.After(linkWait):
		}
		mu.Lock()
		finished = append(finished, index)
		mu.Unlock()
		close(done[index])
		return fmt.Errorf("fixture %s failure", provider)
	})
	metas, err := Discover(context.Background(), Options{Home: t.TempDir(), Run: psOnly(everyProviderPS)})
	var warning *Warning
	if !errors.As(err, &warning) {
		t.Fatalf("err = %v, want a warning", err)
	}
	for i, index := range finished {
		if index != count-1-i {
			t.Fatalf("Links finished in order %v, want the reverse of registry order", finished)
		}
	}
	if got := strings.Join(want, "; "); warning.Message != got {
		t.Errorf("warning = %q, want %q", warning.Message, got)
	}
	if len(metas) != count {
		t.Errorf("got %d sessions, want all %d despite the warnings", len(metas), count)
	}
}

// One provider failing leaves the others linked, and what each Link wrote to
// its groups is in the result.
func TestDiscoverLinkFailureKeepsOtherProviders(t *testing.T) {
	requireEveryProvider(t)
	replaceLinks(t, func(_ int, provider event.Provider, groups []*ProcessGroup) error {
		if provider == event.ProviderClaude {
			return errors.New("fixture failure")
		}
		for _, g := range groups {
			// Replace the whole group, as linkValues does for the real
			// adapters: under -race this fails if Discover still reads any
			// group while a Link is running.
			linked := *g
			linked.Sessions = []Info{{ID: string(provider) + "-session", State: SessionStateActive}}
			*g = linked
		}
		return nil
	})
	metas, err := Discover(context.Background(), Options{Home: t.TempDir(), Run: psOnly(everyProviderPS)})
	var warning *Warning
	if !errors.As(err, &warning) || warning.Message != "Claude: fixture failure" {
		t.Fatalf("err = %v, want only the Claude warning", err)
	}
	if len(metas) != len(adapters) {
		t.Fatalf("got %d sessions, want %d", len(metas), len(adapters))
	}
	for _, m := range metas {
		want, state := string(m.Provider)+"-session", SessionStateActive
		if m.Provider == event.ProviderClaude {
			want, state = "", SessionStateUnknown
		}
		if m.ID != want || m.State != state {
			t.Errorf("%s: id %q state %s, want id %q state %s", m.Provider, m.ID, m.State, want, state)
		}
	}
}

// Cancelling while providers are being linked ends the scan with the
// context's error once every Link has returned: the other Links here are
// still running when Claude's cancels.
func TestDiscoverCancelledWhileLinking(t *testing.T) {
	requireEveryProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := make(chan struct{})
	var returned, stuck atomic.Int32
	replaceLinks(t, func(_ int, provider event.Provider, _ []*ProcessGroup) error {
		if provider == event.ProviderClaude {
			cancel()
			close(cancelled)
		} else {
			select {
			case <-cancelled:
			case <-time.After(linkWait):
				stuck.Add(1)
			}
		}
		returned.Add(1)
		return nil
	})
	metas, err := Discover(ctx, Options{Home: t.TempDir(), Run: psOnly(everyProviderPS)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if stuck.Load() > 0 {
		t.Fatal("a Link was not running when Claude's cancelled: linking is not concurrent")
	}
	if got := int(returned.Load()); got != len(adapters) {
		t.Errorf("Discover returned after %d Links, want all %d to have returned", got, len(adapters))
	}
	if len(metas) != len(adapters) {
		t.Errorf("got %d sessions, want the %d found before the cancellation", len(metas), len(adapters))
	}
}

// A panic in a provider's Link reaches Discover's caller, on the caller's
// goroutine, after the other providers have finished. It names the provider,
// keeps the original value and the stack of the goroutine that panicked, and
// with several panics it is the first provider in registry order.
func TestDiscoverLinkPanicReachesCaller(t *testing.T) {
	requireEveryProvider(t)
	cause := errors.New("fixture panic")
	first, last := adapters[0].Provider(), adapters[len(adapters)-1].Provider()
	for _, tc := range []struct {
		name      string
		panics    map[event.Provider]any
		provider  event.Provider
		unwrapped error
	}{
		{"error value", map[event.Provider]any{event.ProviderClaude: cause}, event.ProviderClaude, cause},
		{"string value", map[event.Provider]any{event.ProviderClaude: "fixture panic"}, event.ProviderClaude, nil},
		{"two providers", map[event.Provider]any{last: "fixture panic", first: "fixture panic"}, first, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var finished atomic.Int32
			replaceLinks(t, func(_ int, provider event.Provider, _ []*ProcessGroup) error {
				if value, ok := tc.panics[provider]; ok {
					panic(value)
				}
				finished.Add(1)
				return nil
			})
			var caught any
			func() {
				defer func() { caught = recover() }()
				_, _ = Discover(context.Background(), Options{Home: t.TempDir(), Run: psOnly(everyProviderPS)})
			}()
			err, ok := caught.(error)
			if !ok {
				t.Fatalf("recovered %v, want the Link's panic as an error", caught)
			}
			message := err.Error()
			if want := "panic linking " + ToolName(tc.provider) + " sessions: fixture panic"; !strings.HasPrefix(message, want) {
				t.Errorf("panic = %.80q, want it to start with %q", message, want)
			}
			// The stack is the panicking goroutine's: it has the adapter's
			// Link frame, which Discover's own goroutine never runs.
			if !strings.Contains(message, "linkAdapter.Link") {
				t.Error("panic message lost the stack of the goroutine that panicked")
			}
			if got := errors.Unwrap(err); got != tc.unwrapped {
				t.Errorf("Unwrap = %v, want %v", got, tc.unwrapped)
			}
			if got, want := int(finished.Load()), len(adapters)-len(tc.panics); got != want {
				t.Errorf("%d other providers finished before the panic was raised, want %d", got, want)
			}
		})
	}
}

// Without a panic, and with a single provider, linking is unchanged.
func TestDiscoverLinksSingleProvider(t *testing.T) {
	var linked []event.Provider
	replaceLinks(t, func(_ int, provider event.Provider, groups []*ProcessGroup) error {
		linked = append(linked, provider) // one provider, so one goroutine
		for _, g := range groups {
			g.Sessions = []Info{{ID: "only"}}
		}
		return nil
	})
	metas, err := Discover(context.Background(), Options{Home: t.TempDir(), Run: psOnly("20 1 ttys002 00:01 kimi\n")})
	if err != nil {
		t.Fatal(err)
	}
	if len(linked) != 1 || linked[0] != event.ProviderKimi {
		t.Fatalf("linked %v, want only Kimi: a provider without groups is not linked", linked)
	}
	if len(metas) != 1 || metas[0].ID != "only" {
		t.Fatalf("metas = %+v, want the Kimi session", metas)
	}
}

// With one provider, Link runs on Discover's goroutine, and a panic still
// names that provider, also when it comes from per-file work inside Link
// whose index is not the provider's.
func TestDiscoverSingleProviderLinkPanicNamesProvider(t *testing.T) {
	for _, tc := range []struct {
		name  string
		panic func()
	}{
		{"direct", func() { panic("fixture panic") }},
		{"nested", func() {
			parallel.Bounded(5, func(i int) {
				if i == 4 {
					panic("fixture panic")
				}
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replaceLinks(t, func(_ int, _ event.Provider, _ []*ProcessGroup) error {
				tc.panic()
				return nil
			})
			var caught any
			func() {
				defer func() { caught = recover() }()
				_, _ = Discover(context.Background(), Options{Home: t.TempDir(), Run: psOnly("20 1 ttys002 00:01 kimi\n")})
			}()
			lp, ok := caught.(*linkPanic)
			if !ok {
				t.Fatalf("recovered %#v, want a *linkPanic", caught)
			}
			if want := "panic linking " + ToolName(event.ProviderKimi) + " sessions: "; !strings.HasPrefix(lp.Error(), want) {
				t.Errorf("panic = %.80q, want it to start with %q", lp.Error(), want)
			}
		})
	}
}
