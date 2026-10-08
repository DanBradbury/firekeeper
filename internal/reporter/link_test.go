package reporter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCleanServer(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"https://firekeeper.example.com", "https://firekeeper.example.com", true},
		{"https://firekeeper.example.com/", "https://firekeeper.example.com", true},
		{" https://fk.example.com:8443/base/ ", "https://fk.example.com:8443/base", true},
		{"http://127.0.0.1:7777", "http://127.0.0.1:7777", true},
		{"http://localhost:7777/", "http://localhost:7777", true},
		{"http://[::1]:7777", "http://[::1]:7777", true},
		{"http://firekeeper.example.com", "", false}, // a token would cross the network in the clear
		{"ftp://fk.example.com", "", false},
		{"https://user:pass@fk.example.com", "", false},
		{"https://fk.example.com?x=1", "", false},
		{"https://fk.example.com#frag", "", false},
		{"fk.example.com", "", false},
		{"", "", false},
	} {
		got, err := CleanServer(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("CleanServer(%q) = %q, %v; want %q ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

// fakeLink is a server that approves on the nth poll.
func fakeLink(t *testing.T, approveAt int32, secret string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/link/start", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			MachineID string `json:"machine_id"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.MachineID != "machine-7" {
			http.Error(w, `{"error":"bad machine","code":"invalid_request"}`, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"device_code":"fkd_DEVICE-SECRET","user_code":"ABCD-EFGH","expires_in":600,"interval":1}`))
	})
	mux.HandleFunc("POST /v1/link/poll", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DeviceCode string `json:"device_code"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.DeviceCode != "fkd_DEVICE-SECRET" {
			http.Error(w, `{"error":"unknown","code":"link_invalid"}`, http.StatusNotFound)
			return
		}
		if n := polls.Add(1); n < approveAt {
			w.Write([]byte(`{"status":"pending"}`))
			return
		}
		w.Write([]byte(`{"status":"approved","token":"` + secret + `","account":{"email":"me@example.com"},"machine":{"id":"machine-7","name":"Laptop"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &polls
}

func TestLinkPollsUntilApproved(t *testing.T) {
	const secret = "fk_TOKEN-SECRET-VALUE"
	srv, polls := fakeLink(t, 3, secret)
	var out bytes.Buffer
	res, err := Link(context.Background(), LinkOptions{
		Server: srv.URL, MachineID: "machine-7", MachineName: "My Laptop", Out: &out, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Token != secret || res.AccountEmail != "me@example.com" || res.MachineName != "Laptop" || res.MachineID != "machine-7" || res.Server != srv.URL {
		t.Fatalf("result %+v", res)
	}
	if polls.Load() != 3 {
		t.Fatalf("%d polls, want 3", polls.Load())
	}
	text := out.String()
	for _, want := range []string{srv.URL + "/link#", "code=ABCD-EFGH", "name=My+Laptop", "10 minutes"} {
		if !strings.Contains(text, want) {
			t.Errorf("instructions lack %q:\n%s", want, text)
		}
	}
	// The fragment keeps the code out of server logs; the secrets never print.
	for _, secretText := range []string{secret, "DEVICE-SECRET"} {
		if strings.Contains(text, secretText) {
			t.Errorf("instructions print a secret: %s", text)
		}
	}
}

func TestLinkFailures(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("POST /v1/link/start", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"device_code":"d","user_code":"AAAA-BBBB","expires_in":600,"interval":1}`))
		})
		mux.HandleFunc("POST /v1/link/poll", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusGone)
			w.Write([]byte(`{"error":"expired","code":"link_expired"}`))
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()
		_, err := Link(context.Background(), LinkOptions{Server: srv.URL, MachineID: "m", Interval: time.Millisecond})
		if !errors.Is(err, ErrLinkExpired) {
			t.Fatalf("err = %v, want ErrLinkExpired", err)
		}
	})
	t.Run("server refuses start", func(t *testing.T) {
		srv, _ := fakeLink(t, 1, "fk_x")
		_, err := Link(context.Background(), LinkOptions{Server: srv.URL, MachineID: "other", Interval: time.Millisecond})
		if err == nil || !strings.Contains(err.Error(), "bad machine") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		srv, _ := fakeLink(t, 1<<30, "fk_x")
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := Link(ctx, LinkOptions{Server: srv.URL, MachineID: "machine-7", Interval: 5 * time.Millisecond})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("plain http to a remote host", func(t *testing.T) {
		_, err := Link(context.Background(), LinkOptions{Server: "http://fk.example.com", MachineID: "m"})
		if err == nil || !strings.Contains(err.Error(), "https") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("redirect is not followed", func(t *testing.T) {
		var hit atomic.Bool
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Store(true) }))
		defer target.Close()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
		}))
		defer srv.Close()
		_, err := Link(context.Background(), LinkOptions{Server: srv.URL, MachineID: "m"})
		if err == nil || hit.Load() {
			t.Fatalf("followed a redirect (err=%v, hit=%v)", err, hit.Load())
		}
		if err := RevokeSelf(context.Background(), srv.URL, "fk_SECRET", nil); err == nil || hit.Load() || strings.Contains(err.Error(), "fk_SECRET") {
			t.Fatalf("RevokeSelf followed a redirect or leaked the token (err=%v, hit=%v)", err, hit.Load())
		}
	})
}

func TestWhoamiAndRevokeSelf(t *testing.T) {
	const secret = "fk_TOKEN-SECRET-VALUE"
	var revoked atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/account", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret || revoked.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid or revoked token","code":"unauthorized"}`))
			return
		}
		w.Write([]byte(`{"id":"a","email":"me@example.com","single_user":false,"token":{"name":"Laptop","scope":"ingest","machine_id":"m7"}}`))
	})
	mux.HandleFunc("DELETE /v1/tokens/current", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret || revoked.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid or revoked token","code":"unauthorized"}`))
			return
		}
		revoked.Store(true)
		w.Write([]byte(`{"ok":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	id, err := Whoami(context.Background(), srv.URL, secret, nil)
	if err != nil || id.AccountEmail != "me@example.com" || id.Name != "Laptop" || id.Scope != "ingest" || id.MachineID != "m7" {
		t.Fatalf("whoami = %+v, %v", id, err)
	}
	if _, err := Whoami(context.Background(), srv.URL, "fk_wrong", nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong token = %v", err)
	}
	if err := RevokeSelf(context.Background(), srv.URL, secret, nil); err != nil || !revoked.Load() {
		t.Fatalf("revoke = %v (revoked=%v)", err, revoked.Load())
	}
	// Revoking a token the server already rejects is fine: it is gone.
	if err := RevokeSelf(context.Background(), srv.URL, secret, nil); err != nil {
		t.Fatalf("second revoke = %v", err)
	}
	// An unreachable server is an error that names the host but not the token.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	err = RevokeSelf(context.Background(), url, secret, nil)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unreachable = %v", err)
	}
}
