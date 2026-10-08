package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type seed struct {
	machine, session, provider, project, state, title, text string
	activity                                                *time.Time
	events                                                  int
}

func ingestSeed(t *testing.T, s *store.Store, sd seed) {
	t.Helper()
	b := store.SessionBatch{Meta: store.SessionMeta{
		SessionID: sd.session, Provider: sd.provider, Project: sd.project,
		State: sd.state, Title: sd.title, LastActivityAt: sd.activity,
	}}
	for i := 0; i < sd.events; i++ {
		b.Events = append(b.Events, transcript.Event{
			Provider: transcript.Provider(sd.provider), Seq: int64(i), Role: transcript.RoleUser,
			Text: sd.text, Tokens: transcript.Tokens{Input: 1}, Raw: json.RawMessage(`{}`),
		})
	}
	if _, _, err := s.Ingest(context.Background(), store.DefaultAccountID, store.Machine{ID: sd.machine, Name: sd.machine + "-name"}, []store.SessionBatch{b}); err != nil {
		t.Fatal(err)
	}
}

func at(min int) *time.Time {
	t := time.Date(2026, 10, 7, 12, min, 0, 0, time.UTC)
	return &t
}

func get(t *testing.T, h http.Handler, path string, out any) int {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
	if out != nil && rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: decode: %v", path, err)
		}
	}
	return rr.Code
}

type sessionsPage struct {
	Sessions   []store.Session `json:"sessions"`
	NextCursor string          `json:"next_cursor"`
}

func uids(ss []store.Session) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.UID
	}
	return out
}

func TestListSessionsPagination(t *testing.T) {
	s := newStore(t)
	h := Handler(s)
	// Two sessions share an activity time to exercise the tiebreak, and one
	// has none so it sorts last.
	ingestSeed(t, s, seed{machine: "m1", session: "a", provider: "codex", activity: at(1)})
	ingestSeed(t, s, seed{machine: "m1", session: "b", provider: "codex", activity: at(3)})
	ingestSeed(t, s, seed{machine: "m2", session: "c", provider: "codex", activity: at(3)})
	ingestSeed(t, s, seed{machine: "m1", session: "d", provider: "codex", activity: at(2)})
	ingestSeed(t, s, seed{machine: "m1", session: "e", provider: "codex"})
	want := []string{"m2:c", "m1:b", "m1:d", "m1:a", "m1:e"}

	for _, limit := range []int{1, 2, 4, 5, 6} {
		var got []string
		cursor := ""
		pages := 0
		for {
			path := fmt.Sprintf("/v1/sessions?limit=%d", limit)
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			var page sessionsPage
			if code := get(t, h, path, &page); code != 200 {
				t.Fatalf("limit %d: status %d", limit, code)
			}
			pages++
			if len(page.Sessions) > limit {
				t.Fatalf("limit %d: page of %d", limit, len(page.Sessions))
			}
			got = append(got, uids(page.Sessions)...)
			if page.NextCursor == "" {
				break
			}
			if pages > 10 {
				t.Fatal("pagination did not terminate")
			}
			cursor = page.NextCursor
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("limit %d: got %v, want %v", limit, got, want)
		}
		// An exact multiple must not leave a trailing empty page.
		if wantPages := (len(want) + limit - 1) / limit; pages != wantPages {
			t.Fatalf("limit %d: %d pages, want %d", limit, pages, wantPages)
		}
	}

	var page sessionsPage
	if get(t, h, "/v1/sessions", &page); len(page.Sessions) != 5 || page.NextCursor != "" {
		t.Fatalf("default limit: %v %q", uids(page.Sessions), page.NextCursor)
	}
	if get(t, h, "/v1/sessions?limit=100000", &page); len(page.Sessions) != 5 {
		t.Fatalf("clamped limit: %d", len(page.Sessions))
	}
	for _, bad := range []string{"limit=0", "limit=-1", "limit=x", "cursor=%21%21", "cursor=e30", "state=BOGUS", "provider=nope"} {
		if code := get(t, h, "/v1/sessions?"+bad, nil); code != 400 {
			t.Errorf("%s: status %d, want 400", bad, code)
		}
	}
}

func TestListSessionsFilters(t *testing.T) {
	s := newStore(t)
	h := Handler(s)
	ingestSeed(t, s, seed{machine: "m1", session: "a", provider: "codex", project: "fk", state: "ACTIVE", text: "refactor the parser", events: 2, activity: at(1)})
	ingestSeed(t, s, seed{machine: "m1", session: "b", provider: "copilot", project: "fk", state: "WAITING", title: "Fix Parser bug", activity: at(2)})
	ingestSeed(t, s, seed{machine: "m2", session: "c", provider: "codex", project: "other", state: "ACTIVE", text: "unrelated", events: 1, activity: at(3)})

	cases := []struct {
		query string
		want  []string
	}{
		{"machine=m1", []string{"m1:b", "m1:a"}},
		{"provider=codex", []string{"m2:c", "m1:a"}},
		{"project=fk", []string{"m1:b", "m1:a"}},
		{"state=ACTIVE", []string{"m2:c", "m1:a"}},
		{"provider=codex&project=fk", []string{"m1:a"}},
		{"q=parser", []string{"m1:a", "m1:b"}},  // transcript match outranks title-only match
		{"q=" + url.QueryEscape(`"x" OR`), nil}, // FTS syntax is quoted, not parsed
		{"machine=nobody", nil},
	}
	for _, c := range cases {
		var page sessionsPage
		if code := get(t, h, "/v1/sessions?"+c.query, &page); code != 200 {
			t.Fatalf("%s: status %d", c.query, code)
		}
		if fmt.Sprint(uids(page.Sessions)) != fmt.Sprint(c.want) && !(len(c.want) == 0 && len(page.Sessions) == 0) {
			t.Errorf("%s: got %v, want %v", c.query, uids(page.Sessions), c.want)
		}
	}
}

func TestGetSessionAndEvents(t *testing.T) {
	s := newStore(t)
	h := Handler(s)
	ingestSeed(t, s, seed{machine: "m:1", session: "s/1", provider: "codex", state: "ACTIVE", text: "hi", events: 5, activity: at(1)})
	uid := url.PathEscape(store.UID("m:1", "s/1"))

	// A machine id containing ':' is ambiguous; the uid splits at the first
	// colon, so this one resolves to machine "m" and is not found.
	if code := get(t, h, "/v1/sessions/"+uid, nil); code != 404 {
		t.Fatalf("ambiguous uid: %d", code)
	}

	ingestSeed(t, s, seed{machine: "m1", session: "s/1", provider: "codex", state: "ACTIVE", text: "hi", events: 5, activity: at(1)})
	uid = url.PathEscape(store.UID("m1", "s/1"))
	var se store.Session
	if code := get(t, h, "/v1/sessions/"+uid, &se); code != 200 {
		t.Fatalf("get: %d", code)
	}
	if se.UID != "m1:s/1" || se.EventCount != 5 || se.Tokens.Input != 5 || se.LastActivityAt == nil || !se.LastActivityAt.Equal(*at(1)) {
		t.Fatalf("session %+v", se)
	}

	type eventsPage struct {
		Events  []transcript.Event `json:"events"`
		HasMore bool               `json:"has_more"`
	}
	cases := []struct {
		query    string
		wantSeqs []int64
		more     bool
	}{
		{"", []int64{0, 1, 2, 3, 4}, false},
		{"?limit=2", []int64{0, 1}, true},
		{"?after_seq=1&limit=2", []int64{2, 3}, true},
		{"?after_seq=2&limit=2", []int64{3, 4}, false},
		{"?after_seq=4", nil, false},
		{"?after_seq=-1&limit=5", []int64{0, 1, 2, 3, 4}, false},
		{"?limit=5000", []int64{0, 1, 2, 3, 4}, false},
	}
	for _, c := range cases {
		var page eventsPage
		if code := get(t, h, "/v1/sessions/"+uid+"/events"+c.query, &page); code != 200 {
			t.Fatalf("%q: status %d", c.query, code)
		}
		var seqs []int64
		for _, e := range page.Events {
			seqs = append(seqs, e.Seq)
			if e.MachineID != "m1" || e.SessionID != "s/1" || e.Provider != "codex" {
				t.Fatalf("%q: event %+v", c.query, e)
			}
		}
		if fmt.Sprint(seqs) != fmt.Sprint(c.wantSeqs) || page.HasMore != c.more {
			t.Errorf("%q: seqs %v more %v, want %v %v", c.query, seqs, page.HasMore, c.wantSeqs, c.more)
		}
	}

	for path, want := range map[string]int{
		"/v1/sessions/nocolon":                         400,
		"/v1/sessions/m1:missing":                      404,
		"/v1/sessions/m1:missing/events":               404,
		"/v1/sessions/" + uid + "/events?after_seq=x":  400,
		"/v1/sessions/" + uid + "/events?after_seq=-2": 400,
		"/v1/sessions/" + uid + "/events?limit=0":      400,
	} {
		if code := get(t, h, path, nil); code != want {
			t.Errorf("%s: status %d, want %d", path, code, want)
		}
	}
}

func TestListMachines(t *testing.T) {
	s := newStore(t)
	h := Handler(s)
	var resp struct {
		Machines []store.MachineInfo `json:"machines"`
	}
	if get(t, h, "/v1/machines", &resp); resp.Machines == nil || len(resp.Machines) != 0 {
		t.Fatalf("empty: %+v", resp)
	}
	ingestSeed(t, s, seed{machine: "m1", session: "a", provider: "codex"})
	ingestSeed(t, s, seed{machine: "m1", session: "b", provider: "codex"})
	ingestSeed(t, s, seed{machine: "m2", session: "c", provider: "codex"})
	if code := get(t, h, "/v1/machines", &resp); code != 200 || len(resp.Machines) != 2 {
		t.Fatalf("%d %+v", code, resp)
	}
	m := resp.Machines[0]
	if m.ID != "m1" || m.Name != "m1-name" || m.SessionCount != 2 || m.LastHeartbeatAt == nil {
		t.Fatalf("machine %+v", m)
	}
}

func TestSearchSnippetsAndRanking(t *testing.T) {
	s := newStore(t)
	h := Handler(s)
	ingestSeed(t, s, seed{machine: "m1", session: "few", provider: "codex", text: "needle <b>hay</b>", events: 1, activity: at(5)})
	ingestSeed(t, s, seed{machine: "m1", session: "many", provider: "codex", text: "needle needle needle", events: 5, activity: at(1)})
	ingestSeed(t, s, seed{machine: "m1", session: "none", provider: "codex", text: "other", events: 1, activity: at(9)})

	var page sessionsPage
	if code := get(t, h, "/v1/sessions?q=needle", &page); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(page.Sessions) != 2 {
		t.Fatalf("got %v", uids(page.Sessions))
	}
	for _, se := range page.Sessions {
		if len(se.Snippets) == 0 || len(se.Snippets) > 3 {
			t.Errorf("%s: %d snippets", se.UID, len(se.Snippets))
		}
		if !strings.Contains(se.Snippets[0].Text, "<mark>needle</mark>") {
			t.Errorf("%s: snippet %q", se.UID, se.Snippets[0].Text)
		}
	}
	for _, se := range page.Sessions {
		if se.SessionID == "few" && !strings.Contains(se.Snippets[0].Text, "&lt;b&gt;") {
			t.Errorf("snippet not escaped: %q", se.Snippets[0].Text)
		}
	}
	var p1, p2 sessionsPage
	get(t, h, "/v1/sessions?q=needle&limit=1", &p1)
	if p1.NextCursor == "" || len(p1.Sessions) != 1 {
		t.Fatalf("page1 %v %q", uids(p1.Sessions), p1.NextCursor)
	}
	get(t, h, "/v1/sessions?q=needle&limit=1&cursor="+url.QueryEscape(p1.NextCursor), &p2)
	if len(p2.Sessions) != 1 || p2.Sessions[0].UID == p1.Sessions[0].UID || p2.NextCursor != "" {
		t.Fatalf("page2 %v", uids(p2.Sessions))
	}
}
