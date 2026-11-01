package claudecloud

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

const (
	testToken = "test-token-not-a-real-secret"
	testOrg   = "11111111-2222-3333-4444-555555555555"
)

// fakeCloud serves the cloud sessions API from memory. Everything in it is
// synthetic.
type fakeCloud struct {
	mu       sync.Mutex
	sessions []map[string]any
	events   map[string][]map[string]any
	// pageSize caps events per page, below the client's requested limit.
	pageSize int
	// failEvents makes the events endpoint of these sessions answer with
	// the given HTTP status instead of events.
	failEvents map[string]int
	// retryAfter, when set, is sent as Retry-After with every failure.
	retryAfter string
	// eventCalls counts requests to each session's events endpoint.
	eventCalls map[string]int
	// sessionCalls counts requests to the list endpoint.
	sessionCalls int
	// badAuth counts requests that lacked the expected credentials.
	badAuth int
}

func newFakeCloud(t *testing.T) (*fakeCloud, *Client) {
	t.Helper()
	f := &fakeCloud{events: map[string][]map[string]any{}, failEvents: map[string]int{}, eventCalls: map[string]int{}, pageSize: 2}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := NewClient(Credentials{token: testToken, OrgUUID: testOrg})
	c.BaseURL = srv.URL + "/v1"
	return f, c
}

func (f *fakeCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+testToken || r.Header.Get("x-organization-uuid") != testOrg ||
		r.Header.Get("anthropic-beta") != betaHeader || r.Header.Get("anthropic-version") != anthropicVersion {
		f.badAuth++
		w.WriteHeader(http.StatusNotFound)
		return
	}
	path := r.URL.Path
	switch {
	case path == "/v1/sessions":
		f.sessionCalls++
		writePage(w, f.sessions, r.URL.Query().Get("after_id"), f.pageSize, "id")
	case len(path) > len("/v1/sessions/") && path[len(path)-len("/events"):] == "/events":
		id := path[len("/v1/sessions/") : len(path)-len("/events")]
		f.eventCalls[id]++
		if status := f.failEvents[id]; status != 0 {
			if f.retryAfter != "" {
				w.Header().Set("Retry-After", f.retryAfter)
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte("internal secret detail"))
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > f.pageSize {
			limit = f.pageSize
		}
		writePage(w, f.events[id], r.URL.Query().Get("after_id"), limit, "uuid")
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// writePage serves items after the one whose key equals after, limit at a
// time, with the API's has_more and last_id fields.
func writePage(w http.ResponseWriter, items []map[string]any, after string, limit int, key string) {
	start := 0
	if after != "" {
		for i, it := range items {
			if it[key] == after {
				start = i + 1
			}
		}
	}
	end := min(start+limit, len(items))
	data := items[start:end]
	out := map[string]any{"data": data, "has_more": end < len(items)}
	if len(data) > 0 {
		out["first_id"] = data[0][key]
		out["last_id"] = data[len(data)-1][key]
	}
	_ = json.NewEncoder(w).Encode(out)
}

func cloudSession(id, updated string) map[string]any {
	return map[string]any{
		"id": id, "title": "Fix the thing", "session_status": "idle",
		"created_at": "2026-10-01T10:00:00.000000Z", "updated_at": updated,
		// As the real API: no cwd, the repository in sources and outcomes.
		"session_context": map[string]any{"cwd": "", "model": "claude-sonnet-5-5",
			"sources":  []map[string]any{{"type": "git_repository", "url": "https://github.com/octo/demo"}},
			"outcomes": []map[string]any{{"type": "git_repository", "git_info": map[string]any{"type": "github", "repo": "octo/demo", "branches": []string{"claude/other"}}}}},
		"external_metadata": map[string]any{"current_branches": map[string]string{"": "feature/x"}},
	}
}

// conversation is a short synthetic cloud session: a prompt, a tool call and
// its result, a reply, a sub-agent message, and plumbing events to drop.
func conversation(sid string) []map[string]any {
	usage := map[string]any{"input_tokens": 10, "output_tokens": 5, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 2}
	return []map[string]any{
		{"type": "user", "uuid": "e1", "session_id": sid, "created_at": "2026-10-01T10:00:00.000000Z",
			"message": map[string]any{"role": "user", "content": "hello prompt"}, "parent_tool_use_id": nil, "client_platform": "web"},
		{"type": "control_request", "uuid": "e2", "session_id": sid, "created_at": "2026-10-01T10:00:01.000000Z", "request": map[string]any{"subtype": "x"}},
		{"type": "assistant", "uuid": "e3", "session_id": sid, "created_at": "2026-10-01T10:00:02.000000Z", "timestamp": "2026-10-01T10:00:02.500Z",
			"message": map[string]any{"id": "m1", "role": "assistant", "model": "claude-sonnet-5-5", "usage": usage,
				"content": []map[string]any{{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "ls"}}}}, "parent_tool_use_id": nil},
		{"type": "user", "uuid": "e4", "session_id": sid, "created_at": "2026-10-01T10:00:03.000000Z", "timestamp": "2026-10-01T10:00:03.500Z",
			"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": "file.txt"}}}, "parent_tool_use_id": nil},
		{"type": "system", "uuid": "e5", "session_id": sid, "created_at": "2026-10-01T10:00:04.000000Z", "subtype": "status"},
		{"type": "assistant", "uuid": "e6", "session_id": sid, "created_at": "2026-10-01T10:00:05.000000Z", "timestamp": "2026-10-01T10:00:05.500Z",
			"message": map[string]any{"id": "m2", "role": "assistant", "model": "claude-sonnet-5-5", "usage": usage,
				"content": []map[string]any{{"type": "text", "text": "all done"}}}, "parent_tool_use_id": nil},
		{"type": "assistant", "uuid": "e7", "session_id": sid, "created_at": "2026-10-01T10:00:06.000000Z", "timestamp": "2026-10-01T10:00:06.500Z",
			"message": map[string]any{"id": "m3", "role": "assistant", "model": "claude-sonnet-5-5",
				"content": []map[string]any{{"type": "text", "text": "sub-agent note"}}}, "parent_tool_use_id": "t1"},
	}
}

// totalEventCalls counts requests to every session's events endpoint.
func (f *fakeCloud) totalEventCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.eventCalls {
		n += c
	}
	return n
}
