package fakeapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/pkg/contract"
)

func call(t *testing.T, h http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func wantShape(t *testing.T, what string, rec *httptest.ResponseRecorder, status int, fixture string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("%s: status %d, want %d: %s", what, rec.Code, status, rec.Body)
	}
	if err := contract.MatchShape(contract.Fixture(fixture), rec.Body.Bytes()); err != nil {
		t.Fatalf("%s does not match fixture %s: %v", what, fixture, err)
	}
}

func wantError(t *testing.T, what string, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	for _, c := range contract.ErrorCases() {
		if c.Code != code {
			continue
		}
		if rec.Code != c.Status {
			t.Fatalf("%s: status %d, want %d", what, rec.Code, c.Status)
		}
		if got, want := bytes.TrimSpace(rec.Body.Bytes()), bytes.TrimSpace(contract.Fixture(c.Fixture())); !bytes.Equal(got, want) {
			t.Fatalf("%s: body %s, want the golden fixture %s", what, got, want)
		}
		return
	}
	t.Fatalf("%s: %q is not a documented error code", what, code)
}

// TestResponsesMatchFixtures walks the client's whole flow, link to logout,
// and compares every success body with its golden fixture.
func TestResponsesMatchFixtures(t *testing.T) {
	api := New()
	api.RequireToken = true
	h := api.Handler()
	eps := map[string]contract.Endpoint{}
	for _, ep := range contract.Endpoints() {
		eps[ep.Method+" "+ep.Path] = ep
	}

	start := eps["POST /v1/link/start"]
	rec := call(t, h, start.Method, start.Path, "", contract.Fixture(start.Request))
	wantShape(t, "link start", rec, start.Status, start.Responses[0])
	var started struct{ DeviceCode, UserCode string }
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	started.DeviceCode, started.UserCode = raw["device_code"].(string), raw["user_code"].(string)

	poll := eps["POST /v1/link/poll"]
	pollBody, _ := json.Marshal(map[string]string{"device_code": started.DeviceCode})
	wantShape(t, "pending poll", call(t, h, poll.Method, poll.Path, "", pollBody), poll.Status, "link_poll.response.pending")
	if err := api.Approve(started.UserCode, "Alice's laptop", "you@example.com"); err != nil {
		t.Fatal(err)
	}
	rec = call(t, h, poll.Method, poll.Path, "", pollBody)
	wantShape(t, "approved poll", rec, poll.Status, "link_poll.response.approved")
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	token := raw["token"].(string)

	acct := eps["GET /v1/account"]
	wantShape(t, "account", call(t, h, acct.Method, acct.Path, token, nil), acct.Status, acct.Responses[0])
	ing := eps["POST /v1/ingest"]
	wantShape(t, "ingest", call(t, h, ing.Method, ing.Path, token, contract.Fixture(ing.Request)), ing.Status, ing.Responses[0])
	hb := eps["POST /v1/heartbeat"]
	wantShape(t, "heartbeat", call(t, h, hb.Method, hb.Path, token, contract.Fixture(hb.Request)), hb.Status, hb.Responses[0])
	del := eps["DELETE /v1/tokens/current"]
	wantShape(t, "logout", call(t, h, del.Method, del.Path, token, nil), del.Status, del.Responses[0])
	wantError(t, "account after logout", call(t, h, acct.Method, acct.Path, token, nil), "unauthorized")
}

func TestErrorsAreGoldenFixtures(t *testing.T) {
	h := New().Handler()
	wantError(t, "malformed ingest", call(t, h, "POST", "/v1/ingest", "", contract.Fixture("batch.malformed")), "bad_request")
	wantError(t, "ingest without machine", call(t, h, "POST", "/v1/ingest", "", contract.Fixture("batch.invalid.missing_machine_id")), "invalid_request")
	wantError(t, "heartbeat bad state", call(t, h, "POST", "/v1/heartbeat", "", []byte(`{"machine":{"id":"m1"},"sessions":[{"session_id":"s","state":"SLEEPING"}]}`)), "invalid_request")
	wantError(t, "unknown link code", call(t, h, "POST", "/v1/link/poll", "", contract.Fixture("link_poll.request")), "link_invalid")
	wantError(t, "link start without machine", call(t, h, "POST", "/v1/link/start", "", []byte(`{}`)), "invalid_request")

	big := bytes.Repeat([]byte(" "), MaxBodyBytes+1)
	wantError(t, "huge body", call(t, h, "POST", "/v1/ingest", "", append(contract.Fixture("ingest.request"), big...)), "payload_too_large")

	var event map[string]any
	_ = json.Unmarshal(contract.Fixture("event.valid.user"), &event)
	events := make([]any, MaxEventsPerReq+1)
	for i := range events {
		events[i] = event
	}
	many, _ := json.Marshal(map[string]any{"machine": map[string]string{"id": "m1"}, "sessions": []any{map[string]any{
		"meta": map[string]string{"session_id": "0199-abc", "provider": "codex"}, "events": events}}})
	wantError(t, "501 events", call(t, h, "POST", "/v1/ingest", "", many), "too_many_events")

	closed := New()
	closed.NoAccounts = true
	wantError(t, "no accounts", call(t, closed.Handler(), "POST", "/v1/link/start", "", contract.Fixture("link_start.request")), "accounts_required")

	strict := New()
	strict.RequireToken = true
	sh := strict.Handler()
	wantError(t, "ingest without token", call(t, sh, "POST", "/v1/ingest", "", contract.Fixture("ingest.request")), "unauthorized")
	wantError(t, "heartbeat bad token", call(t, sh, "POST", "/v1/heartbeat", "nope", contract.Fixture("heartbeat.request")), "unauthorized")
	wantError(t, "revoke bad token", call(t, sh, "DELETE", "/v1/tokens/current", "nope", nil), "unauthorized")

	// A token bound to machine m1 may not write for another machine.
	rec := call(t, sh, "POST", "/v1/link/start", "", contract.Fixture("link_start.request"))
	var started map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &started)
	if err := strict.Approve(started["user_code"].(string), "laptop", "you@example.com"); err != nil {
		t.Fatal(err)
	}
	pollBody, _ := json.Marshal(map[string]string{"device_code": started["device_code"].(string)})
	var approved map[string]any
	_ = json.Unmarshal(call(t, sh, "POST", "/v1/link/poll", "", pollBody).Body.Bytes(), &approved)
	other := strings.Replace(string(contract.Fixture("heartbeat.request")), `"m1"`, `"m2"`, 1)
	wantError(t, "other machine", call(t, sh, "POST", "/v1/heartbeat", approved["token"].(string), []byte(other)), "forbidden")
	wantError(t, "collected code", call(t, sh, "POST", "/v1/link/poll", "", pollBody), "link_invalid")

	// A code polled after 10 minutes is expired, approved or not.
	rec = call(t, sh, "POST", "/v1/link/start", "", contract.Fixture("link_start.request"))
	_ = json.Unmarshal(rec.Body.Bytes(), &started)
	pollBody, _ = json.Marshal(map[string]string{"device_code": started["device_code"].(string)})
	strict.Advance(11 * time.Minute)
	wantError(t, "expired code", call(t, sh, "POST", "/v1/link/poll", "", pollBody), "link_expired")
}

func TestIngestSamples(t *testing.T) {
	h := New().Handler()
	post := func(body []byte) *httptest.ResponseRecorder { return call(t, h, "POST", "/v1/ingest", "", body) }
	for _, name := range append(contract.Names("batch.valid."), "ingest.request") {
		if rec := post(contract.Fixture(name)); rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	for _, name := range contract.Names("event.valid.") {
		batch, _ := contract.EventBatch(contract.Fixture(name))
		if rec := post(batch); rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	for _, name := range contract.Names("batch.invalid.") {
		wantError(t, name, post(contract.Fixture(name)), "invalid_request")
	}
	for _, name := range contract.Names("event.invalid.") {
		batch, _ := contract.EventBatch(contract.Fixture(name))
		wantError(t, name, post(batch), "invalid_request")
	}
	// The strict schema also rejects what a Go decode would let through.
	for _, name := range contract.Names("event.schema_invalid.") {
		batch, _ := contract.EventBatch(contract.Fixture(name))
		if rec := post(batch); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	wantError(t, "batch.malformed", post(contract.Fixture("batch.malformed")), "bad_request")
}

// TestSessionOrigin locks the origin rule of docs/api.md, the same one the
// server's TestIngestOriginPendingContract checks: a new session is local,
// an omitted, empty or null origin keeps the stored value, a named origin
// replaces it, and an unknown one is a 400 that stores nothing.
func TestSessionOrigin(t *testing.T) {
	api := New()
	h := api.Handler()
	post := func(session, extra string) *httptest.ResponseRecorder {
		t.Helper()
		meta := `{"session_id":"` + session + `","provider":"claude"`
		if extra != "" {
			meta += "," + extra
		}
		body := `{"machine":{"id":"m1"},"sessions":[{"meta":` + meta + `},"events":[]}]}`
		return call(t, h, "POST", "/v1/ingest", "", []byte(body))
	}
	origin := func(session string) string {
		t.Helper()
		sess, ok := api.Session("m1", session)
		if !ok {
			t.Fatalf("session %s not stored", session)
		}
		// The test-only session read returns the same value.
		var got map[string]any
		rec := call(t, h, "GET", "/v1/sessions/m1:"+session, "", nil)
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["origin"] != sess.Origin {
			t.Fatalf("GET session %s: origin %v, stored %q (%v)", session, got["origin"], sess.Origin, err)
		}
		return sess.Origin
	}
	ok := func(session, extra string) {
		t.Helper()
		if rec := post(session, extra); rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", session, extra, rec.Code, rec.Body)
		}
	}

	// New sessions default to local whether origin is omitted, empty or null.
	for i, extra := range []string{"", `"origin":""`, `"origin":null`} {
		id := "new" + string(rune('a'+i))
		ok(id, extra)
		if got := origin(id); got != "local" {
			t.Errorf("new session with %q: origin %q, want local", extra, got)
		}
	}

	// A stored cloud survives later batches that carry no origin.
	ok("web", `"origin":"cloud"`)
	for _, extra := range []string{"", `"origin":""`, `"origin":null`} {
		ok("web", extra)
		if got := origin("web"); got != "cloud" {
			t.Fatalf("after %q: origin %q, want cloud kept", extra, got)
		}
	}
	// A named origin replaces it.
	ok("web", `"origin":"local"`)
	if got := origin("web"); got != "local" {
		t.Fatalf("after local: origin %q", got)
	}
	ok("web", `"origin":"cloud"`)

	// An unknown origin is invalid_request, never echoed, and stores nothing.
	// Case-variant keys, which encoding/json would fold into Origin, are
	// rejected the same way (SEC-24-1, #25).
	for _, extra := range []string{`"origin":"mars"`, `"origin":"LOCAL"`, `"origin":1`, `"origin":["cloud"]`, `"Origin":"mars"`, `"origin":"cloud","Origin":"mars"`, `"ORIGIN":"local"`} {
		rec := post("bad", extra)
		wantError(t, extra, rec, "invalid_request")
		if strings.Contains(rec.Body.String()+rec.Header().Get("X-Fakeapi-Detail"), "mars") {
			t.Errorf("%s: rejected value echoed", extra)
		}
		if _, found := api.Session("m1", "bad"); found {
			t.Fatalf("%s: rejected batch stored a session", extra)
		}
		wantError(t, extra+" on a stored session", post("web", extra), "invalid_request")
		if got := origin("web"); got != "cloud" {
			t.Fatalf("%s: stored origin changed to %q", extra, got)
		}
	}
}

// TestCheckDecodedMeta covers the second check fakeapi runs after
// ValidateBatch: the decoded struct values themselves (SEC-24-1, #25).
func TestCheckDecodedMeta(t *testing.T) {
	good := metaBody{SessionID: "s", Provider: "codex"}
	if err := checkDecodedMeta(good); err != nil {
		t.Fatalf("valid meta: %v", err)
	}
	for name, m := range map[string]metaBody{
		"provider":     {Provider: "mars"},
		"state":        {Provider: "codex", State: "bogus"},
		"origin":       {Provider: "codex", Origin: "mars"},
		"files_source": {Provider: "codex", FilesSource: "mars"},
	} {
		err := checkDecodedMeta(m)
		if err == nil || !strings.Contains(err.Error(), "meta."+name) || strings.Contains(err.Error(), "mars") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRelToWindowsPaths(t *testing.T) {
	for _, c := range []struct{ cwd, p, want string }{
		{"~/src/demo", "~/src/demo/main.go", "main.go"},
		{`~\src\demo`, `~\src\demo\gen\out.go`, "gen/out.go"},
		{`~\src\demo`, "docs/new.md", "docs/new.md"},
		{`~\src\demo`, `~\src\other\x.go`, "~/src/other/x.go"},
	} {
		if got := relTo(c.cwd, c.p); got != c.want {
			t.Errorf("relTo(%q, %q) = %q, want %q", c.cwd, c.p, got, c.want)
		}
	}
}

// TestSubagentMeta checks the optional subagent fields: they are stored,
// returned by the test-only session read, and kept by later batches that
// omit them or send empty or null values.
func TestSubagentMeta(t *testing.T) {
	api := New()
	h := api.Handler()
	post := func(extra string) {
		t.Helper()
		meta := `{"session_id":"child","provider":"codex"`
		if extra != "" {
			meta += "," + extra
		}
		body := `{"machine":{"id":"m1"},"sessions":[{"meta":` + meta + `},"events":[]}]}`
		if rec := call(t, h, "POST", "/v1/ingest", "", []byte(body)); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", extra, rec.Code, rec.Body)
		}
	}
	post(`"parent_session_id":"root","agent_name":"Galileo","agent_role":"explorer"`)
	post("")
	post(`"parent_session_id":"","agent_name":null,"agent_role":""`)
	sess, ok := api.Session("m1", "child")
	if !ok || sess.ParentSessionID != "root" || sess.AgentName != "Galileo" || sess.AgentRole != "explorer" {
		t.Fatalf("stored session = %+v", sess)
	}
	var got map[string]any
	rec := call(t, h, "GET", "/v1/sessions/m1:child", "", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["parent_session_id"] != "root" || got["agent_name"] != "Galileo" || got["agent_role"] != "explorer" {
		t.Fatalf("GET session: %v (%v)", got, err)
	}
}

// TestSubagentTree checks the tree rules docs/api.md sets for
// parent_session_id: roll-ups follow resolved links only, an unknown parent
// leaves its child top level until it arrives, a parent can change but not
// be cleared, a cycle (even one built across batches) and a chain longer
// than contract.MaxSubagentDepth fall back to top level, and a parent is
// looked up on the same machine only.
func TestSubagentTree(t *testing.T) {
	api := New()
	h := api.Handler()
	var sample map[string]any
	if err := json.Unmarshal(contract.Fixture("event.valid.user"), &sample); err != nil {
		t.Fatal(err)
	}
	post := func(machine, id, parent string, tokens int) {
		t.Helper()
		meta := map[string]any{"session_id": id, "provider": "codex"}
		if parent != "" {
			meta["parent_session_id"] = parent
		}
		events := []any{}
		if tokens > 0 {
			e := map[string]any{}
			for k, v := range sample {
				e[k] = v
			}
			e["machine_id"], e["session_id"], e["seq"] = machine, id, 0
			e["tokens"] = map[string]any{"input": tokens, "output": 0, "cache": 0}
			events = append(events, e)
		}
		body, _ := json.Marshal(map[string]any{"machine": map[string]string{"id": machine}, "sessions": []any{map[string]any{"meta": meta, "events": events}}})
		if rec := call(t, h, "POST", "/v1/ingest", "", body); rec.Code != http.StatusOK {
			t.Fatalf("%s/%s: %d %s", machine, id, rec.Code, rec.Body)
		}
	}
	wantParent := func(machine, id, want string) {
		t.Helper()
		got, ok := api.Parent(machine, id)
		if want == "" && ok || want != "" && got != want {
			t.Errorf("Parent(%s, %s) = %q %v, want %q", machine, id, got, ok, want)
		}
	}
	wantTokens := func(id string, own, child int64) {
		t.Helper()
		o, c, sum := api.Tokens("m1", id)
		if o != own || c != child || sum != own+child {
			t.Errorf("Tokens(%s) = %d %d %d, want %d %d %d", id, o, c, sum, own, child, own+child)
		}
	}

	// The child arrives before its parent: an orphan, shown top level.
	post("m1", "child", "root", 10)
	post("m1", "grandchild", "child", 1)
	wantParent("m1", "child", "")
	wantParent("m1", "grandchild", "child")
	wantTokens("child", 10, 1)
	// The parent arrives later and the roll-up includes the child.
	post("m1", "root", "", 100)
	wantParent("m1", "child", "root")
	wantTokens("root", 100, 11)

	// A parent can change; an empty value keeps it.
	post("m1", "other", "", 1000)
	post("m1", "child", "other", 0)
	post("m1", "child", "", 0)
	wantParent("m1", "child", "other")
	wantTokens("root", 100, 0)
	wantTokens("other", 1000, 11)

	// A same-id session on another machine is not a parent.
	post("m2", "x", "root", 5)
	wantParent("m2", "x", "")
	wantTokens("root", 100, 0)

	// A cycle built across batches: a -> b, then b -> a.
	post("m1", "a", "b", 1)
	post("m1", "b", "a", 2)
	wantParent("m1", "a", "")
	wantParent("m1", "b", "")
	wantTokens("a", 1, 0)
	wantTokens("b", 2, 0)

	// A chain of MaxSubagentDepth links resolves; one more does not.
	prev := "d0"
	post("m1", prev, "", 0)
	for i := 1; i <= contract.MaxSubagentDepth+1; i++ {
		id := "d" + strings.Repeat("x", i)
		post("m1", id, prev, 0)
		prev = id
	}
	wantParent("m1", "d"+strings.Repeat("x", contract.MaxSubagentDepth), "d"+strings.Repeat("x", contract.MaxSubagentDepth-1))
	wantParent("m1", prev, "")

	// A self-parent is rejected.
	body := []byte(`{"machine":{"id":"m1"},"sessions":[{"meta":{"session_id":"s","provider":"codex","parent_session_id":"s"},"events":[]}]}`)
	wantError(t, "self parent", call(t, h, "POST", "/v1/ingest", "", body), "invalid_request")
}

// TestSubagentForks checks the forked_from_session_id rule: a fork's own
// total includes the history it copied, so it is counted in no other
// session's child or combined total, while sessions below it still are. The
// field is kept when a later batch omits it or sends "" or null.
func TestSubagentForks(t *testing.T) {
	api := New()
	h := api.Handler()
	var sample map[string]any
	if err := json.Unmarshal(contract.Fixture("event.valid.user"), &sample); err != nil {
		t.Fatal(err)
	}
	post := func(id string, meta map[string]any, tokens int) {
		t.Helper()
		meta["session_id"], meta["provider"] = id, "codex"
		e := map[string]any{}
		for k, v := range sample {
			e[k] = v
		}
		e["machine_id"], e["session_id"], e["seq"] = "m1", id, 0
		e["tokens"] = map[string]any{"input": tokens, "output": 0, "cache": 0}
		body, _ := json.Marshal(map[string]any{"machine": map[string]string{"id": "m1"}, "sessions": []any{map[string]any{"meta": meta, "events": []any{e}}}})
		if rec := call(t, h, "POST", "/v1/ingest", "", body); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", id, rec.Code, rec.Body)
		}
	}
	wantTokens := func(id string, own, child int64) {
		t.Helper()
		o, c, sum := api.Tokens("m1", id)
		if o != own || c != child || sum != own+child {
			t.Errorf("Tokens(%s) = %d %d %d, want %d %d %d", id, o, c, sum, own, child, own+child)
		}
	}

	post("root", map[string]any{}, 100)
	// A plain subagent counts toward root.
	post("plain", map[string]any{"parent_session_id": "root"}, 10)
	// A subagent spawned with root's context: its 130 include root's 100
	// copied, so root must not count it (no double count).
	post("forked", map[string]any{"parent_session_id": "root", "agent_name": "Noether", "forked_from_session_id": "root"}, 130)
	// A subagent of the fork did its own work and still counts, in the fork
	// and in root.
	post("grandchild", map[string]any{"parent_session_id": "forked"}, 5)
	// A user fork of root has no parent: top level, counted nowhere else.
	post("userfork", map[string]any{"forked_from_session_id": "root"}, 120)

	if p, ok := api.Parent("m1", "forked"); !ok || p != "root" {
		t.Errorf("forked is shown under %q %v, want root", p, ok)
	}
	if _, ok := api.Parent("m1", "userfork"); ok {
		t.Error("user fork has a parent")
	}
	wantTokens("root", 100, 15)
	wantTokens("forked", 130, 5)
	wantTokens("userfork", 120, 0)

	// Keep on empty.
	post("forked", map[string]any{"forked_from_session_id": ""}, 130)
	post("forked", map[string]any{"forked_from_session_id": nil}, 130)
	post("forked", map[string]any{}, 130)
	if sess, _ := api.Session("m1", "forked"); sess.ForkedFromSessionID != "root" {
		t.Errorf("forked_from_session_id = %q after empty batches, want root", sess.ForkedFromSessionID)
	}
	var got map[string]any
	rec := call(t, h, "GET", "/v1/sessions/m1:forked", "", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["forked_from_session_id"] != "root" {
		t.Errorf("GET session: %v (%v)", got, err)
	}
	wantTokens("root", 100, 15)

	// A self-fork is rejected.
	wantError(t, "self fork", call(t, h, "POST", "/v1/ingest", "", contract.Fixture("batch.invalid.forked_from_self")), "invalid_request")
}
