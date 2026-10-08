package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/api"
	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

func serve(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, path, nil))
	return rr
}

func TestHandlerServesAssets(t *testing.T) {
	h := Handler()
	for _, tc := range []struct {
		path, ctype, contains string
	}{
		{"/", "text/html", `<script type="module" src="app.js">`},
		{"/app.js", "javascript", "renderText"},
		{"/api.js", "javascript", "v1/sessions"},
		{"/api.js", "javascript", "v1/usage"},
		{"/app.js", "javascript", "usageView"},
		{"/api.js", "javascript", "v1/auth/login"},
		{"/app.js", "javascript", "authView"},
		{"/mock.js", "javascript", "createMockAPI"},
		{"/app.css", "text/css", ".event"},
		{"/favicon.svg", "image/svg+xml", "<svg"},
		{"/mock/sessions.json", "application/json", `"sessions"`},
	} {
		rr := serve(t, h, "GET", tc.path)
		if rr.Code != http.StatusOK {
			t.Errorf("%s: status %d", tc.path, rr.Code)
			continue
		}
		if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, tc.ctype) {
			t.Errorf("%s: content type %q, want %q", tc.path, ct, tc.ctype)
		}
		if !strings.Contains(rr.Body.String(), tc.contains) {
			t.Errorf("%s: body missing %q", tc.path, tc.contains)
		}
		csp := rr.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("%s: CSP %q does not restrict to self", tc.path, csp)
		}
		if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", tc.path)
		}
	}
}

func TestHandlerRejects(t *testing.T) {
	h := Handler()
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/mock/", http.StatusNotFound},
		{"GET", "/nope.js", http.StatusNotFound},
		{"POST", "/", http.StatusMethodNotAllowed},
		{"DELETE", "/app.js", http.StatusMethodNotAllowed},
	} {
		if rr := serve(t, h, tc.method, tc.path); rr.Code != tc.want {
			t.Errorf("%s %s: status %d, want %d", tc.method, tc.path, rr.Code, tc.want)
		}
	}
}

// TestNoExternalReferences keeps the page offline-capable: no asset may name
// another origin. The SVG namespace URI is an identifier, not a request.
func TestNoExternalReferences(t *testing.T) {
	urlRe := regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.-]+\.[a-z]{2,}`)
	cssRe := regexp.MustCompile(`(?i)@import|url\(\s*['"]?(https?:|//)`)
	err := fs.WalkDir(static, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := static.ReadFile(path)
		if err != nil {
			return err
		}
		s := strings.ReplaceAll(string(b), `xmlns="http://www.w3.org/2000/svg"`, "")
		s = strings.ReplaceAll(s, `const SVG_NS = "http://www.w3.org/2000/svg";`, "")
		if m := urlRe.FindString(s); m != "" {
			t.Errorf("%s references external URL %q", path, m)
		}
		if m := cssRe.FindString(s); m != "" {
			t.Errorf("%s has external CSS reference %q", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func decodeStrict(t *testing.T, name string, v any) {
	t.Helper()
	b, err := static.ReadFile("static/mock/" + name)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s does not match the API shape: %v", name, err)
	}
}

// TestMockFixturesMatchAPI keeps ?mock=1 honest: fixtures must decode into
// the same types the real API serves.
func TestMockFixturesMatchAPI(t *testing.T) {
	var sessions struct {
		Sessions []store.Session `json:"sessions"`
	}
	decodeStrict(t, "sessions.json", &sessions)
	if len(sessions.Sessions) == 0 {
		t.Fatal("no mock sessions")
	}
	uids := map[string]store.Session{}
	for _, s := range sessions.Sessions {
		if s.UID != store.UID(s.MachineID, s.SessionID) {
			t.Errorf("session %q: uid does not join machine and session id", s.UID)
		}
		if !transcript.Provider(s.Provider).Valid() {
			t.Errorf("session %q: invalid provider %q", s.UID, s.Provider)
		}
		uids[s.UID] = s
	}

	var machines struct {
		Machines []store.MachineInfo `json:"machines"`
	}
	decodeStrict(t, "machines.json", &machines)
	known := map[string]bool{}
	for _, m := range machines.Machines {
		known[m.ID] = true
	}
	for _, s := range sessions.Sessions {
		if !known[s.MachineID] {
			t.Errorf("session %q: machine %q not in machines.json", s.UID, s.MachineID)
		}
	}

	var events map[string][]transcript.Event
	decodeStrict(t, "events.json", &events)
	for uid, list := range events {
		s, ok := uids[uid]
		if !ok {
			t.Errorf("events for unknown session %q", uid)
		}
		for i, ev := range list {
			ev.Provider = transcript.Provider(s.Provider)
			if err := ev.Validate(); err != nil {
				t.Errorf("%s event %d: %v", uid, i, err)
			}
			if ev.Seq != int64(i) {
				t.Errorf("%s event %d: seq %d, want %d", uid, i, ev.Seq, i)
			}
		}
	}
}

// TestLongSessionPaging mounts the UI beside the real API and walks a
// 5,000-event session the way the transcript view does: pages of 200 by
// after_seq until has_more is false.
func TestLongSessionPaging(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	const total = 5000
	last := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for start := 0; start < total; start += api.MaxIngestEvents {
		b := store.SessionBatch{Meta: store.SessionMeta{
			SessionID: "long", Provider: "codex", Project: "p", State: "ACTIVE", LastActivityAt: &last,
		}}
		for i := start; i < start+api.MaxIngestEvents; i++ {
			role := []transcript.Role{transcript.RoleUser, transcript.RoleAssistant, transcript.RoleToolCall, transcript.RoleToolResult}[i%4]
			b.Events = append(b.Events, transcript.Event{
				Provider: transcript.ProviderCodex, Seq: int64(i), Role: role,
				Text: fmt.Sprintf("event %d", i), Raw: json.RawMessage(`{}`),
			})
		}
		if _, _, err := s.Ingest(ctx, store.DefaultAccountID, store.Machine{ID: "m1"}, []store.SessionBatch{b}); err != nil {
			t.Fatal(err)
		}
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/", api.Handler(s))
	mux.Handle("/", Handler())

	if rr := serve(t, mux, "GET", "/"); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "app.js") {
		t.Fatalf("index through mux: status %d", rr.Code)
	}

	var list struct {
		Sessions []store.Session `json:"sessions"`
	}
	rr := serve(t, mux, "GET", "/v1/sessions?limit=50")
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil || len(list.Sessions) != 1 {
		t.Fatalf("session list: %d %v", rr.Code, err)
	}
	if got := list.Sessions[0].EventCount; got != total {
		t.Fatalf("event_count %d, want %d", got, total)
	}

	uid := url.PathEscape(list.Sessions[0].UID)
	after, pages, seen := int64(-1), 0, 0
	for {
		var page struct {
			Events  []transcript.Event `json:"events"`
			HasMore bool               `json:"has_more"`
		}
		rr := serve(t, mux, "GET", fmt.Sprintf("/v1/sessions/%s/events?after_seq=%d&limit=200", uid, after))
		if rr.Code != http.StatusOK {
			t.Fatalf("page %d: status %d", pages, rr.Code)
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		pages++
		for _, ev := range page.Events {
			if ev.Seq != after+1 {
				t.Fatalf("page %d: seq %d after %d", pages, ev.Seq, after)
			}
			after = ev.Seq
			seen++
		}
		if !page.HasMore {
			break
		}
		if pages > total/200+1 {
			t.Fatal("paging did not terminate")
		}
	}
	if seen != total || pages != total/200 {
		t.Fatalf("saw %d events in %d pages, want %d in %d", seen, pages, total, total/200)
	}
}
