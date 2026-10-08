// Package e2e holds end-to-end tests that run the reporter against a real
// dashboard server over HTTP, using a synthetic home directory.
package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

const (
	codexID   = "0199e2e0-0000-7000-8000-00000000c0de"
	copilotID = "0199e2e0-0000-7000-8000-0000000c0b17"
	kimiID    = "kimi-e2e-session"
)

// fakeSecret looks like an API key to internal/redact. It is assembled at
// run time so no key-shaped literal sits in the repository.
var fakeSecret = "sk-" + strings.Repeat("e2eFake0", 3)

var apiKeyMarker = redact.Marker(redact.KindAPIKey)

// home is a synthetic home directory with one transcript per provider, all
// running in the same Git repository.
type home struct {
	dir     string
	repo    string
	rollout string // Codex
	events  string // Copilot
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

	// Kimi has session discovery but no transcript reader yet. The context
	// file stands in for a transcript so the test notices when one lands.
	kimiDir := filepath.Join(h.dir, ".kimi-code", "sessions", "work", kimiID)
	write(t, filepath.Join(kimiDir, "state.json"), fmt.Sprintf(
		`{"title":"Kimi smoke","workDir":%q,"createdAt":"2026-10-07T12:00:00Z","updatedAt":"2026-10-07T12:01:00Z"}`, h.repo))
	write(t, filepath.Join(kimiDir, "context.jsonl"), jsonl(map[string]any{"role": "user", "content": "Key " + fakeSecret}))
	return h
}

func codexMessage(role, kind, text string) map[string]any {
	return map[string]any{"timestamp": "2026-10-07T12:00:02.000Z", "type": "response_item", "payload": map[string]any{
		"type": "message", "role": role, "content": []map[string]string{{"type": kind, "text": text}},
	}}
}

// run answers discovery's ps, lsof, sqlite3, and git calls as if one Codex,
// one Copilot, and one Kimi process were running in h.repo.
func (h *home) run(_ context.Context, name string, args ...string) ([]byte, error) {
	switch name {
	case "ps":
		return []byte("100 1 ttys001 00:10 codex\n200 1 ttys002 00:10 copilot --resume " + copilotID + "\n300 1 ttys003 00:10 kimi\n"), nil
	case "lsof":
		switch {
		case slices.Equal(args, []string{"-Fn", "-p", "100"}):
			return []byte("p100\nn" + h.rollout + "\n"), nil
		case slices.Equal(args, []string{"-a", "-d", "cwd", "-Fn", "-p", "300"}):
			return []byte("p300\nn" + h.repo + "\n"), nil
		}
	case "sqlite3":
		if len(args) == 4 && args[0] == "-readonly" && args[1] == "-json" && filepath.Base(args[2]) == "state_5.sqlite" {
			return json.Marshal([]session.ThreadMetadataRow{{
				ID: codexID, Name: "Codex smoke", CWD: h.repo, Model: "gpt-e2e", GitBranch: "main",
				UpdatedAtMS: time.Date(2026, 10, 7, 12, 0, 4, 0, time.UTC).UnixMilli(),
			}})
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
	t.Setenv("CODEX_HOME", filepath.Join(h.dir, ".codex"))
	t.Setenv("COPILOT_HOME", filepath.Join(h.dir, ".copilot"))
	t.Setenv("KIMI_CODE_HOME", filepath.Join(h.dir, ".kimi-code"))
	return reporter.Config{
		Server:    server,
		Providers: []transcript.Provider{transcript.ProviderCodex, transcript.ProviderCopilot, transcript.ProviderKimi},
		Home:      h.dir,
		Version:   "e2e",
		Discover: func(ctx context.Context, opts session.Options) ([]session.Meta, error) {
			opts.CodexHome = filepath.Join(h.dir, ".codex")
			opts.CopilotHome = filepath.Join(h.dir, ".copilot")
			opts.KimiHome = filepath.Join(h.dir, ".kimi-code")
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
	if r := first["kimi"]; r.Skipped != "no transcript reader for provider" {
		t.Errorf("kimi: got skipped %q, events %d; a Kimi reader may have landed, so extend this test", r.Skipped, r.Events)
	}
	machineID := readMachineID(t, h.dir)
	want := map[string]int{
		"codex":   expectedEvents(t, transcript.ProviderCodex, h.rollout),
		"copilot": expectedEvents(t, transcript.ProviderCopilot, h.events),
	}
	ids := map[string]string{"codex": codexID, "copilot": copilotID}
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
		assertSeqs(t, provider, events, n)
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

	// Appending to the Codex rollout sends only the new events.
	appendFile(t, h.rollout, jsonl(
		codexMessage("user", "input_text", "One more thing."),
		codexMessage("assistant", "output_text", "Appended reply."),
	))
	third := runOnce(t, cfg)
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
	for _, p := range []string{"codex", "copilot", "kimi"} {
		if _, ok := results[p]; !ok {
			t.Fatalf("%s: session not discovered", p)
		}
	}
	return results
}

// expectedEvents counts the events the provider's reader finds in path.
func expectedEvents(t *testing.T, provider transcript.Provider, path string) int {
	t.Helper()
	source, ok := transcript.For(provider)
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
