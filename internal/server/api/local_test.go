package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
)

func TestAccountLocalStatus(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	calls := 0
	h := auth.New(s).Wrap(Handler(s, WithLocal(func(context.Context) *LocalStatus {
		calls++
		return &LocalStatus{MachineID: "m1", Daemon: DaemonNotInstalled, Server: "http://127.0.0.1:7777", Detected: []string{"codex"}, Command: "firekeeper daemon install"}
	})))

	get := func(cookie string) map[string]json.RawMessage {
		t.Helper()
		r := httptest.NewRequest("GET", "/v1/account", nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		if rr.Code != 200 {
			t.Fatalf("GET /v1/account: %d %s", rr.Code, rr.Body)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	body := get("")
	var local LocalStatus
	if err := json.Unmarshal(body["local"], &local); err != nil {
		t.Fatalf("local = %s: %v", body["local"], err)
	}
	if local.MachineID != "m1" || local.Daemon != DaemonNotInstalled || local.Command == "" || calls != 1 {
		t.Fatalf("local = %+v, calls = %d", local, calls)
	}

	// Once an account exists the server is no longer local, and a signed-in
	// account never sees the machine's status.
	a, err := s.CreateAccount(ctx, "a@example.com", "$argon2id$stub", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cookie, _, err := s.CreateWebSession(ctx, a.ID, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if body := get(cookie); body["local"] != nil {
		t.Fatalf("a real account got local status: %s", body["local"])
	}
	if calls != 1 {
		t.Fatalf("local probe ran for a real account")
	}
}
