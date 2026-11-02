package codex

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

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
