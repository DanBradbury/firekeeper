// Package auth guards the v1 API with bearer tokens.
package auth

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/store"
)

// Failed-attempt limit per client IP.
const (
	MaxFailures = 10
	Window      = time.Minute
)

// Principal is the authenticated token.
type Principal struct {
	Scope     string
	MachineID string
}

const challenge = "Bearer realm=\"firekeeper\""

type ctxKey struct{}

// From returns the principal the middleware attached, if any.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// Middleware requires a valid bearer token on every request to next.
type Middleware struct {
	store *store.Store
	now   func() time.Time

	mu       sync.Mutex
	failures map[string][]time.Time
}

// New returns a Middleware backed by s.
func New(s *store.Store) *Middleware {
	return &Middleware{store: s, now: time.Now, failures: map[string][]time.Time{}}
}

// Wrap returns next guarded by token authentication. Ingest and heartbeat
// need an ingest token; every other route needs a read token.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if m.limited(ip) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many failed attempts")
			return
		}
		secret, ok := bearer(r.Header.Get("Authorization"))
		if !ok {
			m.fail(ip)
			w.Header().Set("WWW-Authenticate", challenge)
			writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or malformed bearer token")
			return
		}
		tok, err := m.lookup(r.Context(), secret)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		if tok == nil {
			m.fail(ip)
			w.Header().Set("WWW-Authenticate", challenge)
			writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid or revoked token")
			return
		}
		if tok.Scope != requiredScope(r) {
			writeErr(w, http.StatusForbidden, "forbidden", "token scope does not permit this request")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, Principal{Scope: tok.Scope, MachineID: tok.MachineID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requiredScope(r *http.Request) string {
	if r.URL.Path == "/v1/ingest" || r.URL.Path == "/v1/heartbeat" {
		return store.ScopeIngest
	}
	return store.ScopeRead
}

// lookup compares the secret's hash against every active token in constant
// time per comparison, without short-circuiting on the first match.
func (m *Middleware) lookup(ctx context.Context, secret string) (*store.Token, error) {
	toks, err := m.store.ListTokens(ctx, true)
	if err != nil {
		return nil, err
	}
	want := []byte(store.HashToken(secret))
	var found *store.Token
	for i := range toks {
		if subtle.ConstantTimeCompare(want, []byte(toks[i].Hash)) == 1 {
			found = &toks[i]
		}
	}
	return found, nil
}

func bearer(h string) (string, bool) {
	scheme, rest, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if rest == "" || strings.ContainsAny(rest, " \t") {
		return "", false
	}
	return rest, true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (m *Middleware) prune(ip string, now time.Time) []time.Time {
	var keep []time.Time
	for _, t := range m.failures[ip] {
		if now.Sub(t) < Window {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(m.failures, ip)
	} else {
		m.failures[ip] = keep
	}
	return keep
}

func (m *Middleware) limited(ip string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.prune(ip, m.now())) >= MaxFailures
}

func (m *Middleware) fail(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if len(m.failures) > 10000 {
		for k := range m.failures {
			m.prune(k, now)
		}
	}
	m.failures[ip] = append(m.prune(ip, now), now)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write([]byte(`{"error":"` + msg + `","code":"` + code + `"}` + "\n"))
}
