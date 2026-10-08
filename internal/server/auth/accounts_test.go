package auth_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/api"
	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
)

const pw = "correct horse battery"

// stack mirrors how server.Run mounts the pieces.
type stack struct {
	t   *testing.T
	s   *store.Store
	m   *auth.Middleware
	h   http.Handler
	now time.Time
	mu  sync.Mutex
}

func newStack(t *testing.T, mode auth.SignupMode, opts ...api.Option) *stack {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	st := &stack{t: t, s: s, now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	st.m = auth.New(s, auth.WithSignup(mode))
	st.m.SetClock(func() time.Time { st.mu.Lock(); defer st.mu.Unlock(); return st.now })
	mux := http.NewServeMux()
	mux.Handle("POST /v1/auth/login", st.m.Login())
	mux.Handle("POST /v1/auth/signup", st.m.Signup())
	mux.Handle("POST /v1/link/start", st.m.LinkStart())
	mux.Handle("POST /v1/link/poll", st.m.LinkPoll())
	mux.Handle("POST /v1/link/approve", st.m.Wrap(st.m.LinkApprove()))
	mux.Handle("/v1/", st.m.Wrap(api.Handler(s, opts...)))
	st.h = mux
	return st
}

func (st *stack) advance(d time.Duration) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.now = st.now.Add(d)
}

// req describes one request.
type req struct {
	method, path, body string
	ip                 string
	cookie, csrf, bear string
	headers            map[string]string
	raw                bool // do not default Content-Type to JSON
}

func (st *stack) do(r req) *httptest.ResponseRecorder {
	st.t.Helper()
	hr := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	hr.RemoteAddr = "192.0.2.7:1234"
	if r.ip != "" {
		hr.RemoteAddr = r.ip + ":1234"
	}
	if r.body != "" && !r.raw {
		hr.Header.Set("Content-Type", "application/json")
	}
	if r.cookie != "" {
		hr.AddCookie(&http.Cookie{Name: auth.CookieName, Value: r.cookie})
	}
	if r.csrf != "" {
		hr.Header.Set(auth.CSRFHeader, r.csrf)
	}
	if r.bear != "" {
		hr.Header.Set("Authorization", "Bea"+"rer "+r.bear)
	}
	for k, v := range r.headers {
		hr.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	st.h.ServeHTTP(rr, hr)
	return rr
}

type login struct {
	cookie, csrf, id string
}

func cookieOf(rr *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	return nil
}

func creds(email, password string) string {
	b, _ := json.Marshal(map[string]string{"email": email, "password": password})
	return string(b)
}

// signup creates an account over HTTP and returns its session.
func (st *stack) signup(email, ip string) login {
	st.t.Helper()
	rr := st.do(req{method: "POST", path: "/v1/auth/signup", body: creds(email, pw), ip: ip})
	if rr.Code != http.StatusCreated {
		st.t.Fatalf("signup %s: %d %s", email, rr.Code, rr.Body)
	}
	return st.sessionFrom(rr)
}

func (st *stack) sessionFrom(rr *httptest.ResponseRecorder) login {
	st.t.Helper()
	c := cookieOf(rr)
	var body struct {
		Account   struct{ ID string }
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || c == nil || body.CSRFToken == "" {
		st.t.Fatalf("no session in response: %v %v %s", err, c, rr.Body)
	}
	return login{cookie: c.Value, csrf: body.CSRFToken, id: body.Account.ID}
}

func code(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("status %d (%s), want %d", rr.Code, strings.TrimSpace(rr.Body.String()), want)
	}
}

func sessionBody(machine, session, text string) string {
	return `{"machine":{"id":"` + machine + `"},"sessions":[{"meta":{"session_id":"` + session +
		`","provider":"codex","state":"ACTIVE","last_activity_at":"2026-01-01T12:00:00Z"},"events":[` +
		`{"seq":0,"provider":"codex","role":"user","text":"` + text + `","tokens":{"input":10,"output":5,"cache":0},"ts":"2026-01-01T12:00:00Z","raw":{}}]}]}`
}

func (st *stack) ingestToken(accountID, machine string) string {
	st.t.Helper()
	_, sec, err := st.s.CreateToken(context.Background(), accountID, "ing-"+accountID, store.ScopeIngest, machine)
	if err != nil {
		st.t.Fatal(err)
	}
	return sec
}

func sessionUIDs(t *testing.T, rr *httptest.ResponseRecorder) []string {
	t.Helper()
	var body struct{ Sessions []struct{ UID string } }
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range body.Sessions {
		out = append(out, s.UID)
	}
	return out
}

// TestTenantIsolation is the core guarantee: two accounts, even with the
// same machine and session ids, cannot see or touch each other's data.
func TestTenantIsolation(t *testing.T) {
	st := newStack(t, auth.SignupOpen)
	a := st.signup("alice@example.com", "")
	b := st.signup("bob@example.com", "")
	if a.id == b.id {
		t.Fatal("accounts share an id")
	}
	// Both machines are called "m1" and both have a session "shared".
	aTok, bTok := st.ingestToken(a.id, "m1"), st.ingestToken(b.id, "m1")
	code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: aTok, body: sessionBody("m1", "shared", "alice-shared needle")}), 200)
	code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: aTok, body: sessionBody("m1", "a-only", "alice-private")}), 200)
	code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: bTok, body: sessionBody("m1", "shared", "bob-shared")}), 200)
	code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: bTok, body: sessionBody("m1", "b-only", "bob-secret needle")}), 200)

	// List: each sees exactly its own sessions.
	listA := st.do(req{method: "GET", path: "/v1/sessions", cookie: a.cookie})
	code(t, listA, 200)
	if got := strings.Join(sessionUIDs(t, listA), ","); got != "m1:shared,m1:a-only" && got != "m1:a-only,m1:shared" {
		t.Fatalf("alice sessions = %s", got)
	}
	listB := st.do(req{method: "GET", path: "/v1/sessions", cookie: b.cookie})
	if strings.Contains(listB.Body.String(), "a-only") || strings.Contains(listB.Body.String(), "alice") {
		t.Fatalf("bob's list leaks alice: %s", listB.Body)
	}

	// Read by guessing a uid that exists only in the other account.
	for _, path := range []string{"/v1/sessions/m1:b-only", "/v1/sessions/m1:b-only/events"} {
		code(t, st.do(req{method: "GET", path: path, cookie: a.cookie}), 404)
		code(t, st.do(req{method: "GET", path: path, bear: st.readToken(a.id)}), 404)
	}
	code(t, st.do(req{method: "GET", path: "/v1/sessions/m1:b-only", cookie: b.cookie}), 200)

	// A uid both accounts hold resolves to the caller's own copy.
	ev := st.do(req{method: "GET", path: "/v1/sessions/m1:shared/events", cookie: a.cookie})
	code(t, ev, 200)
	if !strings.Contains(ev.Body.String(), "alice-shared") || strings.Contains(ev.Body.String(), "bob-shared") {
		t.Fatalf("alice reads wrong copy of shared: %s", ev.Body)
	}

	// Search, machines and usage are scoped too.
	q := st.do(req{method: "GET", path: "/v1/sessions?q=needle", cookie: a.cookie})
	code(t, q, 200)
	if strings.Contains(q.Body.String(), "bob-secret") || strings.Contains(q.Body.String(), "b-only") {
		t.Fatalf("search leaks bob: %s", q.Body)
	}
	if got := strings.Join(sessionUIDs(t, q), ""); got != "m1:shared" {
		t.Fatalf("alice search = %q, want only m1:shared", got)
	}
	mach := st.do(req{method: "GET", path: "/v1/machines", cookie: a.cookie})
	var machines struct {
		Machines []struct {
			SessionCount int64 `json:"session_count"`
		}
	}
	json.Unmarshal(mach.Body.Bytes(), &machines)
	if len(machines.Machines) != 1 || machines.Machines[0].SessionCount != 2 {
		t.Fatalf("alice machines = %s", mach.Body)
	}
	use := st.do(req{method: "GET", path: "/v1/usage?from=2026-01-01&to=2026-01-01", cookie: a.cookie})
	var usage struct {
		Totals struct{ Tokens struct{ Input int64 } }
	}
	json.Unmarshal(use.Body.Bytes(), &usage)
	if usage.Totals.Tokens.Input != 20 { // alice's two events, not bob's
		t.Fatalf("alice usage = %s", use.Body)
	}

	// Ingest: alice writing to the ids bob uses creates her own rows and
	// leaves bob's untouched.
	code(t, st.do(req{method: "POST", path: "/v1/ingest", bear: aTok, body: sessionBody("m1", "b-only", "alice-overwrite")}), 200)
	ev = st.do(req{method: "GET", path: "/v1/sessions/m1:b-only/events", cookie: b.cookie})
	if strings.Contains(ev.Body.String(), "alice-overwrite") || !strings.Contains(ev.Body.String(), "bob-secret") {
		t.Fatalf("alice's ingest reached bob: %s", ev.Body)
	}
	ctx := context.Background()
	if st1, _ := st.s.Stats(ctx, b.id, "m1", "b-only"); st1.EventCount != 1 {
		t.Fatalf("bob's b-only has %d events", st1.EventCount)
	}
	// A browser session cannot ingest at all, with or without CSRF.
	code(t, st.do(req{method: "POST", path: "/v1/ingest", cookie: a.cookie, csrf: a.csrf, body: sessionBody("m1", "x", "y")}), 403)
	code(t, st.do(req{method: "POST", path: "/v1/ingest", cookie: a.cookie, body: sessionBody("m1", "x", "y")}), 403)
}

func (st *stack) readToken(accountID string) string {
	st.t.Helper()
	_, sec, err := st.s.CreateToken(context.Background(), accountID, "rd-"+accountID, store.ScopeRead, "")
	if err != nil {
		st.t.Fatal(err)
	}
	return sec
}

type frame struct{ event, data string }

// readFrames parses server-sent events from r until it closes.
func readFrames(r io.Reader) <-chan frame {
	out := make(chan frame, 64)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(r)
		var f frame
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.data = strings.TrimPrefix(line, "data: ")
			case line == "" && f.event != "":
				out <- f
				f = frame{}
			}
		}
	}()
	return out
}

func TestStreamIsTenantScoped(t *testing.T) {
	st := newStack(t, auth.SignupOpen)
	a := st.signup("alice@example.com", "")
	b := st.signup("bob@example.com", "")
	srv := httptest.NewServer(st.h)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hr, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/stream", nil)
	hr.AddCookie(&http.Cookie{Name: auth.CookieName, Value: a.cookie})
	resp, err := http.DefaultClient.Do(hr)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("stream: %v %v", resp, err)
	}
	frames := readFrames(resp.Body)

	post := func(tok, body string) {
		r, _ := http.NewRequest("POST", srv.URL+"/v1/ingest", strings.NewReader(body))
		r.Header.Set("Authorization", "Bea"+"rer "+tok)
		r.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(r)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("ingest: %v %v", res, err)
		}
		res.Body.Close()
	}
	// Bob first, then alice: if bob's changes leaked they would arrive
	// before alice's.
	post(st.ingestToken(b.id, "m1"), sessionBody("m1", "b-only", "x"))
	post(st.ingestToken(a.id, "m1"), sessionBody("m1", "a-only", "x"))
	deadline := time.After(5 * time.Second)
	for sawA := false; !sawA; {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatal("stream closed early")
			}
			if strings.Contains(f.data, "b-only") {
				t.Fatalf("alice's stream received bob's change: %s %s", f.event, f.data)
			}
			sawA = strings.Contains(f.data, "a-only")
		case <-deadline:
			t.Fatal("alice never received her own change")
		}
	}
}

func TestLoginAndSessionChecks(t *testing.T) {
	st := newStack(t, auth.SignupOpen)
	a := st.signup("alice@example.com", "")

	t.Run("wrong password and unknown email look alike", func(t *testing.T) {
		wrong := st.do(req{method: "POST", path: "/v1/auth/login", body: creds("alice@example.com", "not the password")})
		unknown := st.do(req{method: "POST", path: "/v1/auth/login", body: creds("nobody@example.com", pw)})
		code(t, wrong, 401)
		code(t, unknown, 401)
		if wrong.Body.String() != unknown.Body.String() {
			t.Fatalf("distinguishable: %q vs %q", wrong.Body, unknown.Body)
		}
		if cookieOf(wrong) != nil {
			t.Fatal("failed login set a cookie")
		}
	})
	t.Run("right password", func(t *testing.T) {
		rr := st.do(req{method: "POST", path: "/v1/auth/login", body: creds("ALICE@example.com ", pw)})
		code(t, rr, 200)
		l := st.sessionFrom(rr)
		code(t, st.do(req{method: "GET", path: "/v1/machines", cookie: l.cookie}), 200)
	})
	t.Run("cookie attributes", func(t *testing.T) {
		rr := st.do(req{method: "POST", path: "/v1/auth/login", body: creds("alice@example.com", pw)})
		c := cookieOf(rr)
		if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure || c.Path != "/" || c.MaxAge <= 0 {
			t.Fatalf("cookie = %+v", c)
		}
		rr = st.do(req{method: "POST", path: "/v1/auth/login", body: creds("alice@example.com", pw),
			headers: map[string]string{"X-Forwarded-Proto": "https"}})
		if c := cookieOf(rr); c == nil || !c.Secure {
			t.Fatalf("cookie over https = %+v", c)
		}
		tlsReq := httptest.NewRequest("POST", "https://example.com/v1/auth/login", strings.NewReader(creds("alice@example.com", pw)))
		tlsReq.Header.Set("Content-Type", "application/json")
		tlsReq.RemoteAddr = "192.0.2.50:1"
		trr := httptest.NewRecorder()
		st.h.ServeHTTP(trr, tlsReq)
		if c := cookieOf(trr); c == nil || !c.Secure {
			t.Fatalf("cookie over TLS = %+v", c)
		}
	})
	t.Run("login needs a JSON body from the same origin", func(t *testing.T) {
		code(t, st.do(req{method: "POST", path: "/v1/auth/login", raw: true, body: "email=alice%40example.com&password=" + pw,
			headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}}), 415)
		code(t, st.do(req{method: "POST", path: "/v1/auth/login", body: creds("alice@example.com", pw),
			headers: map[string]string{"Origin": "https://evil.example"}}), 403)
		code(t, st.do(req{method: "POST", path: "/v1/auth/login", body: creds("alice@example.com", pw),
			headers: map[string]string{"Origin": "http://example.com"}}), 200) // httptest host is example.com
	})
	t.Run("expired cookie", func(t *testing.T) {
		l := st.sessionFrom(st.do(req{method: "POST", path: "/v1/auth/login", body: creds("alice@example.com", pw)}))
		code(t, st.do(req{method: "GET", path: "/v1/machines", cookie: l.cookie}), 200)
		st.advance(auth.SessionTTL - time.Minute)
		code(t, st.do(req{method: "GET", path: "/v1/machines", cookie: l.cookie}), 200)
		st.advance(2 * time.Minute)
		rr := st.do(req{method: "GET", path: "/v1/machines", cookie: l.cookie})
		code(t, rr, 401)
		if c := cookieOf(rr); c == nil || c.MaxAge >= 0 {
			t.Fatalf("expired session did not clear the cookie: %+v", c)
		}
	})
	t.Run("unknown cookie", func(t *testing.T) {
		code(t, st.do(req{method: "GET", path: "/v1/machines", cookie: "fks_nope"}), 401)
	})
	t.Run("csrf", func(t *testing.T) {
		l := st.sessionFrom(st.do(req{method: "POST", path: "/v1/auth/login", body: creds("alice@example.com", pw)}))
		code(t, st.do(req{method: "POST", path: "/v1/auth/logout", cookie: l.cookie}), 403)                // missing
		code(t, st.do(req{method: "POST", path: "/v1/auth/logout", cookie: l.cookie, csrf: "wrong"}), 403) // wrong
		code(t, st.do(req{method: "POST", path: "/v1/auth/logout", cookie: l.cookie, csrf: a.csrf}), 403)  // another session's
		code(t, st.do(req{method: "POST", path: "/v1/auth/logout", cookie: l.cookie, csrf: l.csrf, headers: map[string]string{"Origin": "https://evil.example"}}), 403)
		code(t, st.do(req{method: "GET", path: "/v1/machines", cookie: l.cookie}), 200) // still signed in
		code(t, st.do(req{method: "POST", path: "/v1/auth/logout", cookie: l.cookie, csrf: l.csrf}), 200)
		code(t, st.do(req{method: "GET", path: "/v1/machines", cookie: l.cookie}), 401) // really gone
	})
	t.Run("account endpoint", func(t *testing.T) {
		a := st.sessionFrom(st.do(req{method: "POST", path: "/v1/auth/login", body: creds("alice@example.com", pw)}))
		rr := st.do(req{method: "GET", path: "/v1/account", cookie: a.cookie})
		code(t, rr, 200)
		var body struct {
			ID, Email  string
			SingleUser bool   `json:"single_user"`
			CSRFToken  string `json:"csrf_token"`
		}
		json.Unmarshal(rr.Body.Bytes(), &body)
		if body.Email != "alice@example.com" || body.ID == "" || body.ID == "default" || body.SingleUser || body.CSRFToken != a.csrf {
			t.Fatalf("account = %s", rr.Body)
		}
		code(t, st.do(req{method: "GET", path: "/v1/account"}), 401)
	})
	t.Run("bearer callers cannot log out", func(t *testing.T) {
		code(t, st.do(req{method: "POST", path: "/v1/auth/logout", bear: st.readToken(a.id)}), 400)
	})
}

func TestSignupModes(t *testing.T) {
	t.Run("closed", func(t *testing.T) {
		st := newStack(t, auth.SignupClosed)
		rr := st.do(req{method: "POST", path: "/v1/auth/signup", body: creds("a@example.com", pw)})
		code(t, rr, 403)
		if !strings.Contains(rr.Body.String(), "signup_closed") {
			t.Fatalf("body = %s", rr.Body)
		}
		if has, _ := st.s.HasAccounts(context.Background()); has {
			t.Fatal("closed signup created an account")
		}
	})
	t.Run("default mode is closed", func(t *testing.T) {
		s, _ := store.Open(context.Background(), filepath.Join(t.TempDir(), "d.db"))
		t.Cleanup(func() { s.Close() })
		rr := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/auth/signup", strings.NewReader(creds("a@example.com", pw)))
		r.Header.Set("Content-Type", "application/json")
		auth.New(s).Signup().ServeHTTP(rr, r)
		code(t, rr, 403)
	})
	t.Run("invite", func(t *testing.T) {
		st := newStack(t, auth.SignupInvite)
		ctx := context.Background()
		signup := func(email, invite string) *httptest.ResponseRecorder {
			b, _ := json.Marshal(map[string]string{"email": email, "password": pw, "invite_code": invite})
			return st.do(req{method: "POST", path: "/v1/auth/signup", body: string(b)})
		}
		code(t, signup("a@example.com", ""), 403)
		code(t, signup("a@example.com", "fki_bogus"), 403)
		invite, err := st.s.CreateInvite(ctx, store.DefaultAccountID, time.Hour, st.now)
		if err != nil {
			t.Fatal(err)
		}
		code(t, signup("a@example.com", invite), 201)
		code(t, signup("b@example.com", invite), 403) // single use
		expired, _ := st.s.CreateInvite(ctx, store.DefaultAccountID, time.Hour, st.now)
		st.advance(2 * time.Hour)
		code(t, signup("c@example.com", expired), 403)
		// A failed signup must not burn the invite.
		fresh, _ := st.s.CreateInvite(ctx, store.DefaultAccountID, time.Hour, st.now)
		code(t, signup("a@example.com", fresh), 409) // email taken
		code(t, signup("d@example.com", fresh), 201)
		var stored string
		accts, _ := st.s.ListAccounts(ctx)
		for _, a := range accts {
			stored += a.Email + " "
		}
		if strings.Contains(stored, "b@example.com") || strings.Contains(stored, "c@example.com") {
			t.Fatalf("accounts = %s", stored)
		}
	})
	t.Run("open validates input", func(t *testing.T) {
		st := newStack(t, auth.SignupOpen)
		code(t, st.do(req{method: "POST", path: "/v1/auth/signup", ip: "203.0.113.101", body: creds("not-an-email", pw)}), 400)
		code(t, st.do(req{method: "POST", path: "/v1/auth/signup", ip: "203.0.113.102", body: creds("Name <a@example.com>", pw)}), 400)
		code(t, st.do(req{method: "POST", path: "/v1/auth/signup", ip: "203.0.113.103", body: creds("a@example.com", "short")}), 400)
		code(t, st.do(req{method: "POST", path: "/v1/auth/signup", ip: "203.0.113.104", body: creds("a@example.com", strings.Repeat("x", 257))}), 400)
		code(t, st.do(req{method: "POST", path: "/v1/auth/signup", ip: "203.0.113.105", body: creds("a@example.com", pw)}), 201)
		code(t, st.do(req{method: "POST", path: "/v1/auth/signup", ip: "203.0.113.106", body: creds("A@Example.com", pw)}), 409)
		code(t, st.do(req{method: "POST", path: "/v1/auth/signup", ip: "203.0.113.107", raw: true, body: "x", headers: map[string]string{"Content-Type": "text/plain"}}), 415)
	})
	t.Run("password is stored as argon2id", func(t *testing.T) {
		st := newStack(t, auth.SignupOpen)
		st.signup("a@example.com", "")
		a, err := st.s.AccountByEmail(context.Background(), "a@example.com")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(a.PasswordHash, "$argon2id$") || strings.Contains(a.PasswordHash, pw) {
			t.Fatalf("hash = %q", a.PasswordHash)
		}
	})
}

func TestRateLimits(t *testing.T) {
	t.Run("login per IP", func(t *testing.T) {
		st := newStack(t, auth.SignupOpen)
		st.signup("a@example.com", "198.51.100.1")
		for i := 0; i < auth.MaxFailures; i++ {
			// A different email each time, so only the IP counter trips.
			code(t, st.do(req{method: "POST", path: "/v1/auth/login", ip: "203.0.113.9", body: creds("u"+string(rune('a'+i))+"@example.com", "x")}), 401)
		}
		rr := st.do(req{method: "POST", path: "/v1/auth/login", ip: "203.0.113.9", body: creds("a@example.com", pw)})
		code(t, rr, 429)
		if rr.Header().Get("Retry-After") == "" {
			t.Fatal("no Retry-After")
		}
		code(t, st.do(req{method: "POST", path: "/v1/auth/login", ip: "203.0.113.10", body: creds("a@example.com", pw)}), 200)
		st.advance(auth.Window + time.Second)
		code(t, st.do(req{method: "POST", path: "/v1/auth/login", ip: "203.0.113.9", body: creds("a@example.com", pw)}), 200)
	})
	t.Run("login per email", func(t *testing.T) {
		st := newStack(t, auth.SignupOpen)
		st.signup("a@example.com", "198.51.100.1")
		for i := 0; i < auth.MaxFailures; i++ {
			// A different IP each time, so only the email counter trips.
			code(t, st.do(req{method: "POST", path: "/v1/auth/login", ip: "203.0.113." + string(rune('1'+i)), body: creds("a@example.com", "x")}), 401)
		}
		code(t, st.do(req{method: "POST", path: "/v1/auth/login", ip: "203.0.113.99", body: creds("a@example.com", pw)}), 429)
		code(t, st.do(req{method: "POST", path: "/v1/auth/login", ip: "203.0.113.99", body: creds("a@example.com ", pw)}), 429) // same email, different spelling
	})
	t.Run("signup per IP and email", func(t *testing.T) {
		st := newStack(t, auth.SignupOpen)
		for i := 0; i < auth.MaxSignups; i++ {
			code(t, st.do(req{method: "POST", path: "/v1/auth/signup", ip: "203.0.113.9", body: creds("bad", pw)}), 400)
		}
		code(t, st.do(req{method: "POST", path: "/v1/auth/signup", ip: "203.0.113.9", body: creds("a@example.com", pw)}), 429)
		for i := 0; i < auth.MaxSignups; i++ {
			code(t, st.do(req{method: "POST", path: "/v1/auth/signup", ip: "203.0.113." + string(rune('1'+i)), body: creds("b@example.com", "short")}), 400)
		}
		code(t, st.do(req{method: "POST", path: "/v1/auth/signup", ip: "203.0.113.77", body: creds("b@example.com", pw)}), 429)
	})
}

func TestDisabledAccount(t *testing.T) {
	st := newStack(t, auth.SignupOpen)
	a := st.signup("a@example.com", "")
	tok := st.readToken(a.id)
	code(t, st.do(req{method: "GET", path: "/v1/machines", bear: tok}), 200)
	if err := st.s.SetAccountDisabled(context.Background(), a.id, true); err != nil {
		t.Fatal(err)
	}
	code(t, st.do(req{method: "GET", path: "/v1/machines", cookie: a.cookie}), 401)
	code(t, st.do(req{method: "GET", path: "/v1/machines", bear: tok}), 401)
	code(t, st.do(req{method: "POST", path: "/v1/auth/login", body: creds("a@example.com", pw)}), 401)
	if err := st.s.SetAccountDisabled(context.Background(), a.id, false); err != nil {
		t.Fatal(err)
	}
	code(t, st.do(req{method: "POST", path: "/v1/auth/login", body: creds("a@example.com", pw)}), 200)
}

// TestSingleUserMode: a server with no tokens and no accounts needs no
// login, and the first account switches authentication on.
func TestSingleUserMode(t *testing.T) {
	st := newStack(t, auth.SignupOpen)
	code(t, st.do(req{method: "GET", path: "/v1/sessions"}), 200)
	code(t, st.do(req{method: "POST", path: "/v1/ingest", body: sessionBody("m1", "s1", "hello")}), 200)
	rr := st.do(req{method: "GET", path: "/v1/account"})
	code(t, rr, 200)
	if !strings.Contains(rr.Body.String(), `"single_user":true`) || !strings.Contains(rr.Body.String(), `"id":"default"`) {
		t.Fatalf("account = %s", rr.Body)
	}
	code(t, st.do(req{method: "POST", path: "/v1/auth/logout"}), 400)
	ctx := context.Background()
	if got, _ := st.s.Stats(ctx, store.DefaultAccountID, "m1", "s1"); got.EventCount != 1 {
		t.Fatalf("single-user ingest went to the wrong account: %+v", got)
	}

	a := st.signup("a@example.com", "")
	code(t, st.do(req{method: "GET", path: "/v1/sessions"}), 401)
	// The new account does not inherit the single-user data.
	rr = st.do(req{method: "GET", path: "/v1/sessions", cookie: a.cookie})
	if len(sessionUIDs(t, rr)) != 0 {
		t.Fatalf("new account sees default account data: %s", rr.Body)
	}
}

// Tokens made before accounts existed belong to the default account and
// keep working next to real accounts.
func TestDefaultAccountTokens(t *testing.T) {
	st := newStack(t, auth.SignupOpen)
	ing := st.ingestToken(store.DefaultAccountID, "m1")
	m2 := auth.New(st.s) // a restart: now a token exists
	h := m2.Wrap(api.Handler(st.s))
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/ingest", strings.NewReader(sessionBody("m1", "s1", "x")))
	r.Header.Set("Authorization", "Bea"+"rer "+ing)
	h.ServeHTTP(rr, r)
	code(t, rr, 200)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/v1/sessions", nil))
	code(t, rr, 401)
}

func TestPasswordHash(t *testing.T) {
	h, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := auth.HashPassword(pw)
	if h == h2 {
		t.Fatal("hashes are not salted")
	}
	if !auth.VerifyPassword(h, pw) || auth.VerifyPassword(h, pw+"x") {
		t.Fatal("verify")
	}
	for _, bad := range []string{"", "plaintext", "$argon2id$", "$argon2i$v=19$m=64,t=1,p=1$YQ$YQ",
		"$argon2id$v=19$m=999999999,t=1,p=1$YQ$YQ", "$argon2id$v=19$m=64,t=0,p=1$YQ$YQ", "$argon2id$v=19$m=64,t=1,p=1$$YQ"} {
		if auth.VerifyPassword(bad, pw) {
			t.Fatalf("malformed hash %q verified", bad)
		}
	}
}
