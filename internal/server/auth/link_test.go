package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
)

const sessionIngest = `{"machine":{"id":"laptop-1","name":"x"},"sessions":[{"meta":{"session_id":"s1","provider":"codex","project":"p","state":"ACTIVE"},"events":[{"machine_id":"laptop-1","session_id":"s1","provider":"codex","seq":0,"role":"user","text":"hi","raw":{}}]}]}`

func decodeBody(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rr.Body, err)
	}
}

// startLink runs the machine's first call and returns its two codes.
func (st *stack) startLink(machine string) (device, user string) {
	st.t.Helper()
	b, _ := json.Marshal(map[string]string{"machine_id": machine})
	rr := st.do(req{method: "POST", path: "/v1/link/start", body: string(b)})
	code(st.t, rr, http.StatusCreated)
	var out struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		ExpiresIn  int    `json:"expires_in"`
		Interval   int    `json:"interval"`
	}
	decodeBody(st.t, rr, &out)
	if out.ExpiresIn != 600 || out.Interval <= 0 || out.DeviceCode == "" || out.UserCode == "" {
		st.t.Fatalf("start = %+v", out)
	}
	return out.DeviceCode, out.UserCode
}

func (st *stack) approve(l login, user, name string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]string{"user_code": user, "machine_name": name})
	return st.do(req{method: "POST", path: "/v1/link/approve", body: string(b), cookie: l.cookie, csrf: l.csrf})
}

func (st *stack) poll(device string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]string{"device_code": device})
	return st.do(req{method: "POST", path: "/v1/link/poll", body: string(b)})
}

// link runs the whole flow for l and returns the ingest token.
func (st *stack) link(l login, machine, name string) string {
	st.t.Helper()
	device, user := st.startLink(machine)
	code(st.t, st.approve(l, user, name), http.StatusOK)
	rr := st.poll(device)
	code(st.t, rr, http.StatusOK)
	var out struct{ Status, Token string }
	decodeBody(st.t, rr, &out)
	if out.Status != "approved" || !strings.HasPrefix(out.Token, store.TokenPrefix) {
		st.t.Fatalf("poll = %s", rr.Body)
	}
	return out.Token
}

func TestLinkFlowEndsInWorkingIngest(t *testing.T) {
	st := newStack(t, auth.SignupOpen)
	alice := st.signup("alice@example.com", "")
	bob := st.signup("bob@example.com", "198.51.100.2")

	device, user := st.startLink("laptop-1")
	// Nothing to collect until someone approves.
	rr := st.poll(device)
	code(t, rr, http.StatusOK)
	if !strings.Contains(rr.Body.String(), `"pending"`) || strings.Contains(rr.Body.String(), "fk_") {
		t.Fatalf("pending poll = %s", rr.Body)
	}
	rr = st.approve(alice, strings.ToLower(user), "Alice's laptop")
	code(t, rr, http.StatusOK)
	rr = st.poll(device)
	code(t, rr, http.StatusOK)
	var got struct {
		Status, Token, TokenID string `json:"-"`
		Account                struct{ Email string }
		Machine                struct{ ID, Name string }
	}
	decodeBody(t, rr, &got)
	var raw map[string]any
	decodeBody(t, rr, &raw)
	tok, _ := raw["token"].(string)
	if tok == "" || got.Account.Email != "alice@example.com" || got.Machine.ID != "laptop-1" || got.Machine.Name != "Alice's laptop" {
		t.Fatalf("poll = %s", rr.Body)
	}

	// The stored token ingests for its own machine only.
	code(t, st.do(req{method: "POST", path: "/v1/ingest", body: sessionIngest, bear: tok}), http.StatusOK)
	other := strings.ReplaceAll(sessionIngest, "laptop-1", "someone-else")
	code(t, st.do(req{method: "POST", path: "/v1/ingest", body: other, bear: tok}), http.StatusForbidden)
	// It cannot read or manage anything.
	code(t, st.do(req{method: "GET", path: "/v1/sessions", bear: tok}), http.StatusForbidden)
	code(t, st.do(req{method: "GET", path: "/v1/tokens", bear: tok}), http.StatusForbidden)

	// Tenancy: the data is Alice's alone.
	rr = st.do(req{method: "GET", path: "/v1/sessions", cookie: alice.cookie})
	code(t, rr, http.StatusOK)
	if !strings.Contains(rr.Body.String(), "laptop-1:s1") {
		t.Fatalf("alice sees %s", rr.Body)
	}
	rr = st.do(req{method: "GET", path: "/v1/sessions", cookie: bob.cookie})
	code(t, rr, http.StatusOK)
	if strings.Contains(rr.Body.String(), "laptop-1") {
		t.Fatalf("bob sees alice's data: %s", rr.Body)
	}

	// whoami: any scope may ask who it is.
	rr = st.do(req{method: "GET", path: "/v1/account", bear: tok})
	code(t, rr, http.StatusOK)
	var who struct {
		Email string
		Token struct {
			Name, Scope string
			MachineID   string `json:"machine_id"`
		} `json:"token"`
	}
	decodeBody(t, rr, &who)
	if who.Email != "alice@example.com" || who.Token.Name != "Alice's laptop" || who.Token.Scope != "ingest" || who.Token.MachineID != "laptop-1" {
		t.Fatalf("account = %s", rr.Body)
	}
	if strings.Contains(rr.Body.String(), tok) {
		t.Fatal("account response repeats the token")
	}
}

func TestLinkCodesExpireAndWorkOnce(t *testing.T) {
	st := newStack(t, auth.SignupOpen)
	alice := st.signup("alice@example.com", "")
	bob := st.signup("bob@example.com", "198.51.100.2")

	t.Run("expired", func(t *testing.T) {
		device, user := st.startLink("m-exp")
		st.advance(10*time.Minute + time.Second)
		rr := st.poll(device)
		code(t, rr, http.StatusGone)
		if !strings.Contains(rr.Body.String(), `"link_expired"`) {
			t.Fatalf("body %s", rr.Body)
		}
		rr = st.approve(alice, user, "late")
		code(t, rr, http.StatusNotFound)
	})
	t.Run("approved but never collected", func(t *testing.T) {
		device, user := st.startLink("m-late")
		code(t, st.approve(alice, user, "m"), http.StatusOK)
		st.advance(11 * time.Minute)
		code(t, st.poll(device), http.StatusGone)
		toks, _ := st.s.ListAccountTokens(t.Context(), alice.id)
		for _, tk := range toks {
			if tk.MachineID == "m-late" {
				t.Fatal("a token was minted for an expired link")
			}
		}
	})
	t.Run("reused", func(t *testing.T) {
		device, user := st.startLink("m-reuse")
		code(t, st.approve(alice, user, "m"), http.StatusOK)
		rr := st.poll(device)
		code(t, rr, http.StatusOK)
		rr = st.poll(device)
		code(t, rr, http.StatusNotFound)
		if strings.Contains(rr.Body.String(), "fk_") {
			t.Fatalf("second poll leaked a token: %s", rr.Body)
		}
		code(t, st.approve(alice, user, "again"), http.StatusNotFound)
	})
	t.Run("wrong account", func(t *testing.T) {
		device, user := st.startLink("m-mine")
		code(t, st.approve(alice, user, "alice-box"), http.StatusOK)
		code(t, st.approve(bob, user, "bob-box"), http.StatusNotFound)
		rr := st.poll(device)
		code(t, rr, http.StatusOK)
		var out struct{ Token string }
		decodeBody(t, rr, &out)
		who := st.do(req{method: "GET", path: "/v1/account", bear: out.Token})
		if !strings.Contains(who.Body.String(), "alice@example.com") || strings.Contains(who.Body.String(), "bob") {
			t.Fatalf("token belongs to %s", who.Body)
		}
		// Bob cannot poll Alice's device code to collect for himself, either:
		// the device code is the only thing that collects.
		device2, user2 := st.startLink("m-bobs")
		code(t, st.approve(bob, user2, "bob-box"), http.StatusOK)
		code(t, st.poll(device2), http.StatusOK)
	})
	t.Run("unknown", func(t *testing.T) {
		code(t, st.poll("fkd_unknown"), http.StatusNotFound)
		code(t, st.approve(alice, "ZZZZ-ZZZZ", "m"), http.StatusNotFound)
	})
}

func TestLinkApproveNeedsABrowserSession(t *testing.T) {
	st := newStack(t, auth.SignupOpen)
	alice := st.signup("alice@example.com", "")
	_, rd, err := st.s.CreateToken(t.Context(), alice.id, "reader", store.ScopeRead, "")
	if err != nil {
		t.Fatal(err)
	}
	_, ing, err := st.s.CreateToken(t.Context(), alice.id, "ingester", store.ScopeIngest, "m")
	if err != nil {
		t.Fatal(err)
	}
	_, user := st.startLink("m1")
	body := `{"user_code":"` + user + `","machine_name":"x"}`

	code(t, st.do(req{method: "POST", path: "/v1/link/approve", body: body}), http.StatusUnauthorized)
	code(t, st.do(req{method: "POST", path: "/v1/link/approve", body: body, cookie: alice.cookie}), http.StatusForbidden) // no CSRF
	code(t, st.do(req{method: "POST", path: "/v1/link/approve", body: body, cookie: alice.cookie, csrf: "wrong"}), http.StatusForbidden)
	code(t, st.do(req{method: "POST", path: "/v1/link/approve", body: body, cookie: alice.cookie, csrf: alice.csrf,
		headers: map[string]string{"Origin": "https://evil.example"}}), http.StatusForbidden)
	// A token, even a read token, cannot add machines.
	rr := st.do(req{method: "POST", path: "/v1/link/approve", body: body, bear: rd})
	code(t, rr, http.StatusForbidden)
	if !strings.Contains(rr.Body.String(), "session_required") {
		t.Fatalf("body %s", rr.Body)
	}
	code(t, st.do(req{method: "POST", path: "/v1/link/approve", body: body, bear: ing}), http.StatusForbidden)
	// None of the refusals burned the code.
	code(t, st.approve(alice, user, "x"), http.StatusOK)

	// Bad names are a 400 and keep the code usable.
	_, user = st.startLink("m2")
	code(t, st.approve(alice, user, "  "), http.StatusBadRequest)
	code(t, st.approve(alice, user, "ok"), http.StatusOK)
}

func TestLinkStartGuards(t *testing.T) {
	t.Run("no accounts", func(t *testing.T) {
		st := newStack(t, auth.SignupClosed)
		rr := st.do(req{method: "POST", path: "/v1/link/start", body: `{"machine_id":"m"}`})
		code(t, rr, http.StatusConflict)
		if !strings.Contains(rr.Body.String(), "accounts_required") {
			t.Fatalf("body %s", rr.Body)
		}
	})
	t.Run("input and rate", func(t *testing.T) {
		st := newStack(t, auth.SignupOpen)
		st.signup("alice@example.com", "")
		code(t, st.do(req{method: "POST", path: "/v1/link/start", body: `{"machine_id":""}`}), http.StatusBadRequest)
		code(t, st.do(req{method: "POST", path: "/v1/link/start", body: `{"machine_id":"m"}`, raw: true}), http.StatusUnsupportedMediaType)
		code(t, st.do(req{method: "POST", path: "/v1/link/start", body: `{"machine_id":"m"}`,
			headers: map[string]string{"Origin": "https://evil.example", "Content-Type": "application/json"}}), http.StatusForbidden)
		for i := 0; i < auth.MaxLinkStarts-1; i++ {
			st.startLink("m")
		}
		code(t, st.do(req{method: "POST", path: "/v1/link/start", body: `{"machine_id":"m"}`}), http.StatusTooManyRequests)
		// Another client is unaffected, and the window passes.
		code(t, st.do(req{method: "POST", path: "/v1/link/start", body: `{"machine_id":"m"}`, ip: "198.51.100.9"}), http.StatusCreated)
		st.advance(auth.Window + time.Second)
		code(t, st.do(req{method: "POST", path: "/v1/link/start", body: `{"machine_id":"m"}`}), http.StatusCreated)
	})
	t.Run("guessing", func(t *testing.T) {
		st := newStack(t, auth.SignupOpen)
		alice := st.signup("alice@example.com", "")
		for i := 0; i < auth.MaxFailures; i++ {
			code(t, st.approve(alice, "ZZZZ-ZZZZ", "m"), http.StatusNotFound)
		}
		_, user := st.startLink("m")
		// Even the right code is refused while the account is locked out.
		code(t, st.approve(alice, user, "m"), http.StatusTooManyRequests)
		st.advance(auth.Window + time.Second)
		code(t, st.approve(alice, user, "m"), http.StatusOK)

		for i := 0; i < auth.MaxFailures; i++ {
			code(t, st.poll("fkd_guess"), http.StatusNotFound)
		}
		code(t, st.poll("fkd_guess"), http.StatusTooManyRequests)
	})
}

func TestTokensAPI(t *testing.T) {
	st := newStack(t, auth.SignupOpen)
	alice := st.signup("alice@example.com", "")
	bob := st.signup("bob@example.com", "198.51.100.2")
	tok := st.link(alice, "laptop-1", "Alice's laptop")

	type view struct {
		ID, Name, Scope string
		MachineID       string  `json:"machine_id"`
		CreatedAt       string  `json:"created_at"`
		LastUsedAt      *string `json:"last_used_at"`
		RevokedAt       *string `json:"revoked_at"`
	}
	list := func(l login) []view {
		t.Helper()
		rr := st.do(req{method: "GET", path: "/v1/tokens", cookie: l.cookie})
		code(t, rr, http.StatusOK)
		var out struct{ Tokens []view }
		decodeBody(t, rr, &out)
		if strings.Contains(rr.Body.String(), "fk_") || strings.Contains(rr.Body.String(), "hash") {
			t.Fatalf("listing exposes secret material: %s", rr.Body)
		}
		return out.Tokens
	}

	// Last-used is empty until the token is used, then within a minute of use.
	before := list(alice)
	if len(before) != 1 || before[0].Name != "Alice's laptop" || before[0].MachineID != "laptop-1" || before[0].Scope != "ingest" || before[0].LastUsedAt != nil {
		t.Fatalf("tokens = %+v", before)
	}
	if len(list(bob)) != 0 {
		t.Fatal("bob lists alice's tokens")
	}
	code(t, st.do(req{method: "POST", path: "/v1/ingest", body: sessionIngest, bear: tok}), http.StatusOK)
	if used := list(alice)[0].LastUsedAt; used == nil {
		t.Fatal("last_used_at not recorded")
	}

	// Create: the secret appears once, in the creation response.
	create := func(l login, body string) *httptest.ResponseRecorder {
		return st.do(req{method: "POST", path: "/v1/tokens", body: body, cookie: l.cookie, csrf: l.csrf})
	}
	rr := create(alice, `{"name":"ci","scope":"ingest","machine_id":"ci-box"}`)
	code(t, rr, http.StatusCreated)
	var created struct {
		view
		Token string `json:"token"`
	}
	decodeBody(t, rr, &created)
	if !strings.HasPrefix(created.Token, "fk_") || created.Name != "ci" || created.MachineID != "ci-box" {
		t.Fatalf("created = %s", rr.Body)
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("secret response is cacheable")
	}
	code(t, st.do(req{method: "POST", path: "/v1/ingest", body: strings.ReplaceAll(sessionIngest, "laptop-1", "ci-box"), bear: created.Token}), http.StatusOK)
	for name, body := range map[string]string{
		"no name":                `{"scope":"read"}`,
		"bad scope":              `{"name":"x","scope":"admin"}`,
		"ingest without machine": `{"name":"x","scope":"ingest"}`,
		"junk":                   `nope`,
	} {
		if rr := create(alice, body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rr.Code, rr.Body)
		}
	}
	// Writes need the session's CSRF token, and tokens cannot manage tokens.
	code(t, st.do(req{method: "POST", path: "/v1/tokens", body: `{"name":"x","scope":"read"}`, cookie: alice.cookie}), http.StatusForbidden)
	code(t, st.do(req{method: "DELETE", path: "/v1/tokens/" + created.ID, cookie: alice.cookie}), http.StatusForbidden)
	code(t, st.do(req{method: "POST", path: "/v1/tokens", body: `{"name":"x","scope":"read"}`, bear: created.Token}), http.StatusForbidden)
	code(t, st.do(req{method: "GET", path: "/v1/tokens"}), http.StatusUnauthorized)

	// Revoking another account's token is a 404 and changes nothing.
	revoke := func(l login, id string) *httptest.ResponseRecorder {
		return st.do(req{method: "DELETE", path: "/v1/tokens/" + id, cookie: l.cookie, csrf: l.csrf})
	}
	code(t, revoke(bob, created.ID), http.StatusNotFound)
	code(t, st.do(req{method: "POST", path: "/v1/ingest", body: strings.ReplaceAll(sessionIngest, "laptop-1", "ci-box"), bear: created.Token}), http.StatusOK)

	// A revoked token is rejected on its very next request.
	code(t, revoke(alice, created.ID), http.StatusOK)
	rr = st.do(req{method: "POST", path: "/v1/ingest", body: strings.ReplaceAll(sessionIngest, "laptop-1", "ci-box"), bear: created.Token})
	code(t, rr, http.StatusUnauthorized)
	if strings.Contains(rr.Body.String(), created.Token) {
		t.Fatal("error body repeats the token")
	}
	code(t, revoke(alice, created.ID), http.StatusNotFound)
	var revoked *view
	for _, v := range list(alice) {
		if v.ID == created.ID {
			v := v
			revoked = &v
		}
	}
	if revoked == nil || revoked.RevokedAt == nil {
		t.Fatalf("revoked token not listed as revoked: %+v", revoked)
	}

	// logout's call: a token revokes itself, whatever its scope.
	code(t, st.do(req{method: "DELETE", path: "/v1/tokens/current", bear: tok}), http.StatusOK)
	code(t, st.do(req{method: "POST", path: "/v1/ingest", body: sessionIngest, bear: tok}), http.StatusUnauthorized)
	code(t, st.do(req{method: "GET", path: "/v1/account", bear: tok}), http.StatusUnauthorized)
	// Browser sessions have no token to revoke.
	code(t, st.do(req{method: "DELETE", path: "/v1/tokens/current", cookie: alice.cookie, csrf: alice.csrf}), http.StatusBadRequest)
}
