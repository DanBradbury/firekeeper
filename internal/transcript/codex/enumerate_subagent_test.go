package codex

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// TestEnumerateSubagents checks that a Codex subagent thread's parent, agent
// name and role reach session.Meta, from the state database's source column
// or, for a thread the database does not list, from the rollout's
// session_meta, and that top-level threads carry none.
func TestEnumerateSubagents(t *testing.T) {
	const (
		parent     = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		child      = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		grandchild = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		review     = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		self       = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	)
	home := t.TempDir()
	rollout := func(id string) string {
		return filepath.Join(home, "sessions", "2026", "01", "02", "rollout-2026-01-02T03-04-05-"+id+".jsonl")
	}
	for _, id := range []string{parent, child, review, self} {
		writeFile(t, rollout(id), sessionMeta("/work/repo", "main")+turnContext("gpt-5"), day(3))
	}
	// The grandchild has no row in the database, so the rollout head names
	// its parent: session_meta.source is an object there, not a string.
	writeFile(t, rollout(grandchild),
		`{"timestamp":"2026-01-02T03:04:05.000Z","type":"session_meta","payload":{"id":"`+grandchild+`","timestamp":"2026-01-02T03:04:05.000Z","cwd":"/work/repo","source":{"subagent":{"thread_spawn":{"parent_thread_id":"`+child+`","depth":2,"agent_path":null,"agent_nickname":"Noether","agent_role":null}}},"git":{"branch":"main"}}}`+"\n"+turnContext("gpt-5"),
		day(4))
	writeFile(t, filepath.Join(home, "state_5.sqlite"), "", day(1))

	spawn := func(parentID, nickname, role string) string {
		data, _ := json.Marshal(map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{
			"parent_thread_id": parentID, "depth": 1, "agent_path": nil, "agent_nickname": nickname, "agent_role": role,
		}}})
		return string(data)
	}
	rows := []threadRow{
		{ID: parent, CWD: "/work/repo", Model: "gpt-5", Branch: "main", CreatedMS: day(2).UnixMilli(), Source: "cli"},
		{ID: child, CWD: "/work/repo", Model: "gpt-5", Branch: "main", CreatedMS: day(2).UnixMilli(), Source: spawn(parent, "Galileo", "explorer")},
		{ID: review, CWD: "/work/repo", Model: "gpt-5", Branch: "main", CreatedMS: day(2).UnixMilli(), Source: `{"subagent":"review"}`},
		{ID: self, CWD: "/work/repo", Model: "gpt-5", Branch: "main", CreatedMS: day(2).UnixMilli(), Source: spawn(self, "Loop", "")},
	}
	var query string
	source := &Source{CodexHome: home, run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.HasPrefix(args[3], "PRAGMA") {
			return []byte(`[{"name":"id"},{"name":"title"},{"name":"cwd"},{"name":"model"},{"name":"git_branch"},{"name":"created_at_ms"},{"name":"updated_at_ms"},{"name":"source"},{"name":"thread_source"}]`), nil
		}
		query = args[3]
		return json.Marshal(rows)
	}}
	metas, err := source.Enumerate(context.Background(), reader.EnumerateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "NULLIF(source, '')") {
		t.Errorf("query does not read the source column: %s", query)
	}
	byID := map[string]session.Meta{}
	for _, m := range metas {
		byID[m.ID] = m
	}
	for _, tc := range []struct {
		id, parent, name, role string
	}{
		{parent, "", "", ""},
		{child, parent, "Galileo", "explorer"},
		{grandchild, child, "Noether", ""},
		{review, "", "", ""},
		{self, "", "", ""},
	} {
		m, ok := byID[tc.id]
		if !ok {
			t.Fatalf("%s not enumerated", tc.id)
		}
		if m.ParentID != tc.parent || m.AgentName != tc.name || m.AgentRole != tc.role {
			t.Errorf("%s: parent %q name %q role %q, want %q %q %q", tc.id, m.ParentID, m.AgentName, m.AgentRole, tc.parent, tc.name, tc.role)
		}
	}
}

// TestEnumerateForkedSubagents checks forked rollouts, which embed copies of
// the source thread's session_meta lines after their own. Only the thread's
// own session_meta may say who spawned it, and a database row whose source
// column is filled is authoritative, so a user fork of a subagent thread is
// not reported as a child of the subagent's parent (the reviewer's case).
func TestEnumerateForkedSubagents(t *testing.T) {
	const (
		root    = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		sub     = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		noRow   = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		cliRow  = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		spawned = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
		oldRow  = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	)
	meta := func(id, ts, cwd string, source string) string {
		return `{"timestamp":"` + ts + `","type":"session_meta","payload":{"id":"` + id + `","timestamp":"` + ts + `","cwd":"` + cwd + `","source":` + source + `,"git":{"branch":"main"}}}` + "\n"
	}
	subSource := `{"subagent":{"thread_spawn":{"parent_thread_id":"` + root + `","depth":1,"agent_nickname":"Galileo","agent_role":"explorer"}}}`
	ownSpawn := `{"subagent":{"thread_spawn":{"parent_thread_id":"` + sub + `","depth":2,"agent_nickname":"Noether","agent_type":"worker"}}}`
	// copied is what a fork of sub embeds after its own session_meta.
	copied := meta(sub, "2026-01-01T01:00:00.000Z", "/copied", subSource) + turnContext("gpt-5")

	home := t.TempDir()
	rollout := func(id string) string {
		return filepath.Join(home, "sessions", "2026", "01", "02", "rollout-2026-01-02T03-04-05-"+id+".jsonl")
	}
	// noRow: a user fork of sub that the database does not list.
	writeFile(t, rollout(noRow), meta(noRow, "2026-01-02T03:04:05.000Z", "/fork", `"cli"`)+copied, day(3))
	// cliRow: the same fork, listed with source "cli" but without a model,
	// so the rollout head is still read for the model.
	writeFile(t, rollout(cliRow), meta(cliRow, "2026-01-02T03:04:05.000Z", "/fork", `"cli"`)+copied, day(3))
	// spawned: a subagent spawned with forked context; its own source names
	// sub, the copied one names root.
	writeFile(t, rollout(spawned), meta(spawned, "2026-01-02T03:04:05.000Z", "/fork", ownSpawn)+copied, day(3))
	// oldRow: a row from a database without a source column, so the head is
	// the only place the spawn is recorded.
	writeFile(t, rollout(oldRow), meta(oldRow, "2026-01-02T03:04:05.000Z", "/fork", ownSpawn)+copied, day(3))
	writeFile(t, filepath.Join(home, "state_5.sqlite"), "", day(1))

	rows := []threadRow{
		{ID: cliRow, CWD: "/fork", Branch: "main", CreatedMS: day(2).UnixMilli(), Source: "cli"},
		{ID: oldRow, CWD: "/fork", Branch: "main", CreatedMS: day(2).UnixMilli()},
	}
	source := &Source{CodexHome: home, run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.HasPrefix(args[3], "PRAGMA") {
			return []byte(`[{"name":"id"},{"name":"title"},{"name":"cwd"},{"name":"model"},{"name":"git_branch"},{"name":"created_at_ms"},{"name":"updated_at_ms"},{"name":"source"}]`), nil
		}
		return json.Marshal(rows)
	}}
	metas, err := source.Enumerate(context.Background(), reader.EnumerateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]session.Meta{}
	for _, m := range metas {
		byID[m.ID] = m
	}
	for _, tc := range []struct {
		id, parent, name, role string
	}{
		{noRow, "", "", ""},
		{cliRow, "", "", ""},
		{spawned, sub, "Noether", "worker"},
		{oldRow, sub, "Noether", "worker"},
	} {
		m, ok := byID[tc.id]
		if !ok {
			t.Fatalf("%s not enumerated", tc.id)
		}
		if m.ParentID != tc.parent || m.AgentName != tc.name || m.AgentRole != tc.role {
			t.Errorf("%s: parent %q name %q role %q, want %q %q %q", tc.id, m.ParentID, m.AgentName, m.AgentRole, tc.parent, tc.name, tc.role)
		}
	}
	// The copied session_meta's cwd and start time are the source thread's,
	// not the fork's.
	if m := byID[noRow]; m.CWD != "/fork" || m.StartedAt == nil || !m.StartedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("fork head: cwd %q started %v, want /fork 2026-01-02T03:04:05Z", m.CWD, m.StartedAt)
	}
}

// TestReadHeadFirstMetaWithoutID checks that a rollout whose session_meta
// does not carry the thread's id falls back to its first session_meta.
func TestReadHeadFirstMetaWithoutID(t *testing.T) {
	const id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	path := filepath.Join(t.TempDir(), "rollout-2026-01-02T03-04-05-"+id+".jsonl")
	writeFile(t, path,
		`{"timestamp":"2026-01-02T03:04:05.000Z","type":"session_meta","payload":{"timestamp":"2026-01-02T03:04:05.000Z","cwd":"/first","source":"cli"}}`+"\n"+
			`{"timestamp":"2026-01-02T03:04:06.000Z","type":"session_meta","payload":{"id":"other","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/second","source":{"subagent":{"thread_spawn":{"parent_thread_id":"p"}}}}}`+"\n",
		day(3))
	h, err := (&Source{}).readHead(path, id)
	if err != nil {
		t.Fatal(err)
	}
	if h.cwd != "/first" || h.spawn.ParentID != "" || !h.started.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("head = %+v, want the first session_meta", h)
	}
}

// TestEnumerateForkedFrom checks that forked_from_id is read only from the
// rollout's own session_meta, also when the database row is complete and
// the rest of the head is not needed, and never from the copied headers a
// fork embeds.
func TestEnumerateForkedFrom(t *testing.T) {
	const (
		root     = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		sub      = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		spawned  = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		userFork = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		plain    = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
		older    = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	)
	line := func(id, forked, source string) string {
		f := ""
		if forked != "" {
			f = `"forked_from_id":"` + forked + `",`
		}
		return `{"timestamp":"2026-01-02T03:04:05.000Z","type":"session_meta","payload":{"id":"` + id + `",` + f + `"timestamp":"2026-01-02T03:04:05.000Z","cwd":"/w","source":` + source + `,"git":{"branch":"main"}}}` + "\n"
	}
	spawnSrc := `{"subagent":{"thread_spawn":{"parent_thread_id":"` + root + `","depth":1,"agent_nickname":"Noether","agent_role":"worker"}}}`
	// What a fork of sub embeds: sub's own header, itself a fork of older.
	copied := line(sub, older, `"cli"`) + turnContext("gpt-5")

	home := t.TempDir()
	rollout := func(id string) string {
		return filepath.Join(home, "sessions", "2026", "01", "02", "rollout-2026-01-02T03-04-05-"+id+".jsonl")
	}
	writeFile(t, rollout(spawned), line(spawned, root, spawnSrc)+line(root, "", `"cli"`)+turnContext("gpt-5"), day(3))
	writeFile(t, rollout(userFork), line(userFork, sub, `"cli"`)+copied, day(3))
	writeFile(t, rollout(plain), line(plain, "", `"cli"`)+copied, day(3))
	writeFile(t, filepath.Join(home, "state_5.sqlite"), "", day(1))
	// Every row is complete, so only the own header is read for the fork.
	full := func(id, source string) threadRow {
		return threadRow{ID: id, CWD: "/w", Model: "gpt-5", Branch: "main", CreatedMS: day(2).UnixMilli(), Source: source}
	}
	rows := []threadRow{full(spawned, spawnSrc), full(userFork, "cli"), full(plain, "cli")}
	source := &Source{CodexHome: home, run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.HasPrefix(args[3], "PRAGMA") {
			return []byte(`[{"name":"id"},{"name":"title"},{"name":"cwd"},{"name":"model"},{"name":"git_branch"},{"name":"created_at_ms"},{"name":"updated_at_ms"},{"name":"source"}]`), nil
		}
		return json.Marshal(rows)
	}}
	metas, err := source.Enumerate(context.Background(), reader.EnumerateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]session.Meta{}
	for _, m := range metas {
		byID[m.ID] = m
	}
	for _, tc := range []struct{ id, parent, forked string }{
		{spawned, root, root},
		{userFork, "", sub},
		{plain, "", ""},
	} {
		m, ok := byID[tc.id]
		if !ok {
			t.Fatalf("%s not enumerated", tc.id)
		}
		if m.ParentID != tc.parent || m.ForkedFromID != tc.forked {
			t.Errorf("%s: parent %q forked from %q, want %q %q", tc.id, m.ParentID, m.ForkedFromID, tc.parent, tc.forked)
		}
	}
}
