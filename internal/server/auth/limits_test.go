package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/server/api"
	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// bigBody is an ingest body with n events of size bytes of text each.
func bigBody(machine, session string, from, n, size int) string {
	type ev struct {
		Seq      int    `json:"seq"`
		Provider string `json:"provider"`
		Role     string `json:"role"`
		Text     string `json:"text"`
		Raw      any    `json:"raw"`
	}
	var evs []ev
	for i := from; i < from+n; i++ {
		evs = append(evs, ev{i, "codex", "user", strings.Repeat("x", size), map[string]int{}})
	}
	b, _ := json.Marshal(map[string]any{
		"machine":  map[string]string{"id": machine},
		"sessions": []any{map[string]any{"meta": map[string]string{"session_id": session, "provider": "codex"}, "events": evs}},
	})
	return string(b)
}

func errCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct{ Error, Code string }
	if err := json.Unmarshal(rr.Body.Bytes(), &e); err != nil || e.Error == "" || e.Code == "" {
		t.Fatalf("not a JSON error: %q (%v)", rr.Body, err)
	}
	return e.Code
}

type usage struct {
	Usage  store.AccountStats `json:"usage"`
	Limits api.Limits         `json:"limits"`
}

func (st *stack) usage(l login) usage {
	st.t.Helper()
	rr := st.do(req{method: "GET", path: "/v1/account", cookie: l.cookie})
	code(st.t, rr, 200)
	var u usage
	if err := json.Unmarshal(rr.Body.Bytes(), &u); err != nil {
		st.t.Fatal(err)
	}
	return u
}

func TestStorageLimitHTTP(t *testing.T) {
	st := newStack(t, auth.SignupOpen, api.WithLimits(api.Limits{MaxBytes: 1000}))
	a := st.signup("a@example.com", "")
	b := st.signup("b@example.com", "10.0.0.2")
	ing := st.ingestToken(a.id, "m1")
	ingB := st.ingestToken(b.id, "m1")

	// 4 events of 200+2 bytes = 808.
	code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: ing, body: bigBody("m1", "s1", 0, 4, 200)}), 200)
	if u := st.usage(a); u.Usage.StoredBytes != 808 || u.Limits.MaxBytes != 1000 {
		t.Fatalf("usage = %+v", u)
	}

	// The request that crosses the limit is refused whole, as a JSON 413.
	rr := st.do(req{method: "POST", path: "/v1/ingest", bear: ing, body: bigBody("m1", "s1", 4, 2, 200)})
	code(t, rr, 413)
	if c := errCode(t, rr); c != "storage_limit" {
		t.Fatalf("code %q", c)
	}
	if u := st.usage(a); u.Usage.StoredBytes != 808 || u.Usage.Events != 4 {
		t.Fatalf("rejected batch changed usage: %+v", u)
	}
	rr = st.do(req{method: "GET", path: "/v1/sessions/" + "m1:s1/events", cookie: a.cookie})
	if strings.Count(rr.Body.String(), `"seq"`) != 4 {
		t.Fatalf("rejected batch stored events: %s", rr.Body)
	}
	// Re-sending what is stored still succeeds at the limit.
	rr = st.do(req{method: "POST", path: "/v1/ingest", bear: ing, body: bigBody("m1", "s1", 0, 4, 200)})
	code(t, rr, 200)
	if !strings.Contains(rr.Body.String(), `"duplicates":4`) {
		t.Fatalf("resend = %s", rr.Body)
	}
	// Another account has its own allowance.
	code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: ingB, body: bigBody("m1", "s1", 0, 4, 200)}), 200)
}

func TestSessionLimitHTTP(t *testing.T) {
	st := newStack(t, auth.SignupOpen, api.WithLimits(api.Limits{MaxSessions: 2}))
	a := st.signup("a@example.com", "")
	ing := st.ingestToken(a.id, "m1")
	code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: ing, body: bigBody("m1", "s1", 0, 1, 5)}), 200)
	code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: ing, body: bigBody("m1", "s2", 0, 1, 5)}), 200)
	rr := st.do(req{method: "POST", path: "/v1/ingest", bear: ing, body: bigBody("m1", "s3", 0, 1, 5)})
	code(t, rr, 413)
	if c := errCode(t, rr); c != "session_limit" {
		t.Fatalf("code %q", c)
	}
	if u := st.usage(a); u.Usage.Sessions != 2 || u.Usage.Events != 2 {
		t.Fatalf("usage = %+v", u)
	}
	code(t, st.do(req{method: "GET", path: "/v1/sessions/m1:s3", cookie: a.cookie}), 404)
	code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: ing, body: bigBody("m1", "s1", 1, 1, 5)}), 200)
}

func TestIngestRateLimitHTTP(t *testing.T) {
	st := newStack(t, auth.SignupOpen, api.WithLimits(api.Limits{IngestPerMinute: 3}))
	a := st.signup("a@example.com", "")
	b := st.signup("b@example.com", "10.0.0.2")
	ing, ingB := st.ingestToken(a.id, "m1"), st.ingestToken(b.id, "m1")

	for i := 0; i < 3; i++ {
		code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: ing, body: bigBody("m1", "s1", i, 1, 5)}), 200)
	}
	rr := st.do(req{method: "POST", path: "/v1/ingest", bear: ing, body: bigBody("m1", "s1", 3, 1, 5)})
	code(t, rr, 429)
	if c := errCode(t, rr); c != "rate_limited" {
		t.Fatalf("code %q", c)
	}
	if ra := rr.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("Retry-After %q", ra)
	}
	if u := st.usage(a); u.Usage.Events != 3 {
		t.Fatalf("limited request was applied: %+v", u)
	}
	// Heartbeats are not ingest, and other accounts are counted apart.
	code(t, st.do(req{method: "POST", path: "/v1/heartbeat", bear: ing, body: `{"machine":{"id":"m1"},"sessions":[]}`}), 200)
	code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: ingB, body: bigBody("m1", "s1", 0, 1, 5)}), 200)
}

// The default account owns single-user data and is never limited.
func TestDefaultAccountIsNotLimited(t *testing.T) {
	st := newStack(t, auth.SignupOpen, api.WithLimits(api.Limits{MaxBytes: 10, MaxSessions: 1, IngestPerMinute: 1}))
	for i := 0; i < 3; i++ {
		code(t, st.do(req{method: "POST", path: "/v1/ingest", body: bigBody("m1", fmt.Sprintf("s%d", i), 0, 2, 100)}), 200)
	}
	var u usage
	rr := st.do(req{method: "GET", path: "/v1/account"})
	json.Unmarshal(rr.Body.Bytes(), &u)
	if u.Usage.Sessions != 3 || u.Limits != (api.Limits{}) {
		t.Fatalf("default account usage/limits = %+v", u)
	}
}

func TestDeleteAccountHTTP(t *testing.T) {
	st := newStack(t, auth.SignupOpen)
	ctx := context.Background()
	a := st.signup("a@example.com", "")
	b := st.signup("b@example.com", "10.0.0.2")
	ing, ingB := st.ingestToken(a.id, "m1"), st.ingestToken(b.id, "m1")
	rd := st.readToken(a.id)
	for _, tok := range []string{ing, ingB} {
		code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: tok, body: sessionBody("m1", "s1", "needle haystack")}), 200)
	}
	del := func(r req) *httptest.ResponseRecorder {
		r.method, r.path = "DELETE", "/v1/account"
		return st.do(r)
	}
	body := `{"confirm":"a@example.com"}`

	// Only a browser session with a CSRF token and the right email deletes.
	code(t, del(req{body: body}), 401)
	code(t, del(req{bear: rd, body: body}), 403)
	code(t, del(req{bear: ing, body: body}), 403)
	code(t, del(req{cookie: a.cookie, body: body}), 403) // no CSRF token
	code(t, del(req{cookie: a.cookie, csrf: a.csrf, body: body, headers: map[string]string{"Origin": "https://evil.example"}}), 403)
	code(t, del(req{cookie: a.cookie, csrf: b.csrf, body: body}), 403) // another session's token
	code(t, del(req{cookie: a.cookie, csrf: a.csrf, body: `{"confirm":"b@example.com"}`}), 400)
	code(t, del(req{cookie: a.cookie, csrf: a.csrf, body: `{}`}), 400)
	if _, err := st.s.GetAccount(ctx, a.id); err != nil {
		t.Fatalf("a refused delete removed the account: %v", err)
	}

	rr := del(req{cookie: a.cookie, csrf: a.csrf, body: `{"confirm":" A@Example.com "}`})
	code(t, rr, 200)
	if c := cookieOf(rr); c == nil || c.Value != "" || c.MaxAge >= 0 {
		t.Fatalf("session cookie not cleared: %+v", c)
	}

	// The account, its sign-in, its cookie and its tokens are all gone.
	if _, err := st.s.GetAccount(ctx, a.id); err != store.ErrNotFound {
		t.Fatalf("account still stored: %v", err)
	}
	code(t, st.do(req{method: "GET", path: "/v1/sessions", cookie: a.cookie}), 401)
	code(t, st.do(req{method: "POST", path: "/v1/auth/login", body: creds("a@example.com", pw)}), 401)
	code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: ing, body: sessionBody("m1", "s9", "x")}), 401)
	code(t, st.do(req{method: "GET", path: "/v1/sessions", bear: rd}), 401)
	if hits, _ := st.s.SearchText(ctx, a.id, "needle", 10); len(hits) != 0 {
		t.Fatalf("deleted account still searchable: %v", hits)
	}
	// The other account is untouched, and the email can sign up again.
	rr = st.do(req{method: "GET", path: "/v1/sessions", cookie: b.cookie})
	if got := sessionUIDs(t, rr); len(got) != 1 || got[0] != "m1:s1" {
		t.Fatalf("other account sessions = %v", got)
	}
	if hits, _ := st.s.SearchText(ctx, b.id, "needle", 10); len(hits) != 1 {
		t.Fatalf("other account search = %v", hits)
	}
	st.signup("a@example.com", "10.0.0.3")
}

func TestDeleteAccountSingleUserRefused(t *testing.T) {
	st := newStack(t, auth.SignupOpen)
	code(t, st.do(req{method: "POST", path: "/v1/ingest", body: sessionBody("m1", "s1", "x")}), 200)
	code(t, st.do(req{method: "DELETE", path: "/v1/account", body: `{"confirm":""}`}), 403)
	if st2, err := st.s.AccountStats(context.Background(), store.DefaultAccountID); err != nil || st2.Events != 1 {
		t.Fatalf("single-user data changed: %+v %v", st2, err)
	}
}

func TestExportAccountHTTP(t *testing.T) {
	st := newStack(t, auth.SignupOpen)
	a := st.signup("a@example.com", "")
	b := st.signup("b@example.com", "10.0.0.2")
	ing, ingB := st.ingestToken(a.id, "m1"), st.ingestToken(b.id, "m1")

	// Seed account a through the API with distinctive events.
	var want []transcript.Event
	for _, sid := range []string{"s2", "s1"} {
		var evs []transcript.Event
		for i := 0; i < 3; i++ {
			evs = append(evs, transcript.Event{
				MachineID: "m1", SessionID: sid, Provider: transcript.ProviderCodex, Seq: int64(i),
				Role: transcript.RoleAssistant, Text: fmt.Sprintf("%s says %d \"quoted\" \u2603", sid, i),
				Tokens: transcript.Tokens{Input: int64(i), Output: 2, Cache: 1}, Raw: json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)),
			})
		}
		reqBody, _ := json.Marshal(map[string]any{
			"machine":  map[string]string{"id": "m1", "name": "laptop"},
			"sessions": []any{map[string]any{"meta": map[string]string{"session_id": sid, "provider": "codex", "title": "t " + sid}, "events": evs}},
		})
		code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: ing, body: string(reqBody)}), 200)
		if sid == "s1" {
			want = append(evs, want...) // export order is by session id
		} else {
			want = append(want, evs...)
		}
	}
	code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: ingB, body: sessionBody("m1", "s1", "OTHER ACCOUNT SECRET")}), 200)

	code(t, st.do(req{method: "GET", path: "/v1/account/export"}), 401)
	rr := st.do(req{method: "GET", path: "/v1/account/export", cookie: a.cookie})
	code(t, rr, 200)
	if ct := rr.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("content type %q", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".jsonl") {
		t.Fatalf("content disposition %q", cd)
	}
	if strings.Contains(rr.Body.String(), "OTHER ACCOUNT SECRET") || strings.Contains(rr.Body.String(), b.id) {
		t.Fatal("export holds another account's data")
	}
	if strings.Contains(rr.Body.String(), ing) {
		t.Fatal("export holds a token secret")
	}

	counts := map[string]int{}
	var got []transcript.Event
	var types []string
	for _, line := range bytes.Split(bytes.TrimSuffix(rr.Body.Bytes(), []byte("\n")), []byte("\n")) {
		var rec struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &rec); err != nil || rec.Type == "" {
			t.Fatalf("line is not a typed JSON object: %q (%v)", line, err)
		}
		counts[rec.Type]++
		types = append(types, rec.Type)
		switch rec.Type {
		case "event":
			var e transcript.Event
			if err := json.Unmarshal(line, &e); err != nil {
				t.Fatal(err)
			}
			got = append(got, e)
		case "account":
			var acc struct{ ID, Email string }
			json.Unmarshal(line, &acc)
			if acc.ID != a.id || acc.Email != "a@example.com" {
				t.Fatalf("account record %s", line)
			}
		case "session":
			if !strings.Contains(string(line), `"title":"t s`) {
				t.Fatalf("session record %s", line)
			}
		}
	}
	if counts["account"] != 1 || counts["machine"] != 2-1 || counts["session"] != 2 || counts["event"] != 6 || counts["token"] != 1 || counts["error"] != 0 {
		t.Fatalf("records = %v", counts)
	}
	if types[0] != "account" {
		t.Fatalf("first record is %q, want account", types[0])
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Fatalf("export differs from what was ingested:\n got %s\nwant %s", gj, wj)
	}

	// A read token exports too, but only its own account.
	rr = st.do(req{method: "GET", path: "/v1/account/export", bear: st.readToken(b.id)})
	code(t, rr, 200)
	if strings.Contains(rr.Body.String(), "s says") || !strings.Contains(rr.Body.String(), "OTHER ACCOUNT SECRET") {
		t.Fatal("read token export is not scoped to its account")
	}
	// Ingest tokens read nothing.
	code(t, st.do(req{method: "GET", path: "/v1/account/export", bear: ing}), 403)
}

func TestAccountReportsUsageAndLimits(t *testing.T) {
	lim := api.Limits{MaxBytes: 5000, MaxSessions: 7, IngestPerMinute: 9}
	st := newStack(t, auth.SignupOpen, api.WithLimits(lim))
	a := st.signup("a@example.com", "")
	code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: st.ingestToken(a.id, "m1"), body: bigBody("m1", "s1", 0, 2, 10)}), 200)
	u := st.usage(a)
	if u.Limits != lim || u.Usage.Sessions != 1 || u.Usage.Events != 2 || u.Usage.StoredBytes != 24 || u.Usage.Machines != 1 || u.Usage.Tokens != 1 {
		t.Fatalf("usage = %+v", u)
	}
}
