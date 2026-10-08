package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/server/api"
	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
)

type env struct {
	h                       http.Handler
	s                       *store.Store
	ingest, read, otherMach string
}

func setup(t *testing.T) env {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	_, ing, err := s.CreateToken(ctx, "ing", store.ScopeIngest, "m1")
	if err != nil {
		t.Fatal(err)
	}
	_, rd, err := s.CreateToken(ctx, "rd", store.ScopeRead, "")
	if err != nil {
		t.Fatal(err)
	}
	return env{h: auth.New(s).Wrap(api.Handler(s)), s: s, ingest: ing, read: rd}
}

func do(h http.Handler, method, path, header, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.7:1234"
	if header != "" {
		r.Header.Set("Authorization", header)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr
}

func bearer(tok string) string { return "Bea" + "rer " + tok }

const ingestBody = `{"machine":{"id":"m1"},"sessions":[]}`

func TestTokenChecks(t *testing.T) {
	e := setup(t)
	tests := []struct {
		name, method, path, header, body string
		want                             int
	}{
		{"missing", "GET", "/v1/machines", "", "", 401},
		{"wrong scheme", "GET", "/v1/machines", "Basic abc", "", 401},
		{"empty token", "GET", "/v1/machines", "Bea" + "rer ", "", 401},
		{"malformed", "GET", "/v1/machines", "Bea" + "rer a b", "", 401},
		{"unknown", "GET", "/v1/machines", bearer("fk_nope"), "", 401},
		{"read ok", "GET", "/v1/machines", bearer(e.read), "", 200},
		{"ingest cannot read", "GET", "/v1/machines", bearer(e.ingest), "", 403},
		{"ingest cannot stream", "GET", "/v1/stream", bearer(e.ingest), "", 403},
		{"read cannot ingest", "POST", "/v1/ingest", bearer(e.read), ingestBody, 403},
		{"read cannot heartbeat", "POST", "/v1/heartbeat", bearer(e.read), ingestBody, 403},
		{"ingest ok", "POST", "/v1/ingest", bearer(e.ingest), ingestBody, 200},
		{"heartbeat ok", "POST", "/v1/heartbeat", bearer(e.ingest), ingestBody, 200},
		{"wrong machine", "POST", "/v1/ingest", bearer(e.ingest), `{"machine":{"id":"m2"},"sessions":[]}`, 403},
		{"wrong machine heartbeat", "POST", "/v1/heartbeat", bearer(e.ingest), `{"machine":{"id":"m2"},"sessions":[]}`, 403},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if rr := do(e.h, tc.method, tc.path, tc.header, tc.body); rr.Code != tc.want {
				t.Fatalf("got %d (%s), want %d", rr.Code, rr.Body, tc.want)
			}
		})
	}
}

func TestRevokedToken(t *testing.T) {
	e := setup(t)
	if n, err := e.s.RevokeToken(context.Background(), "rd"); err != nil || n != 1 {
		t.Fatalf("revoke = %d, %v", n, err)
	}
	if rr := do(e.h, "GET", "/v1/machines", bearer(e.read), ""); rr.Code != 401 {
		t.Fatalf("revoked token: %d", rr.Code)
	}
}

func TestStoresOnlyHash(t *testing.T) {
	e := setup(t)
	toks, _ := e.s.ListTokens(context.Background(), false)
	for _, tk := range toks {
		if tk.Hash == e.read || tk.Hash == e.ingest || tk.Hash != store.HashToken(e.read) && tk.Hash != store.HashToken(e.ingest) {
			t.Fatalf("token %s does not store a SHA-256 hash", tk.ID)
		}
	}
}

func TestRateLimit(t *testing.T) {
	e := setup(t)
	for i := 0; i < auth.MaxFailures; i++ {
		if rr := do(e.h, "GET", "/v1/machines", bearer("fk_bad"), ""); rr.Code != 401 {
			t.Fatalf("attempt %d: %d", i, rr.Code)
		}
	}
	if rr := do(e.h, "GET", "/v1/machines", bearer("fk_bad"), ""); rr.Code != 429 {
		t.Fatalf("after limit: %d", rr.Code)
	}
	if rr := do(e.h, "GET", "/v1/machines", bearer(e.read), ""); rr.Code != 429 {
		t.Fatalf("limited IP with valid token: %d", rr.Code)
	}
	r := httptest.NewRequest("GET", "/v1/machines", nil)
	r.RemoteAddr = "192.0.2.8:1"
	r.Header.Set("Authorization", bearer(e.read))
	rr := httptest.NewRecorder()
	e.h.ServeHTTP(rr, r)
	if rr.Code != 200 {
		t.Fatalf("other IP: %d", rr.Code)
	}
}
