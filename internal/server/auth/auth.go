// Package auth resolves every v1 request to a Principal: a bearer token or
// a browser session, each tied to one account. It also serves signup and
// login.
package auth

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/store"
)

// Failed-attempt limit per client IP.
const (
	MaxFailures = 10
	Window      = time.Minute
)

// SessionTTL is how long a browser session lasts. It does not slide.
const SessionTTL = 14 * 24 * time.Hour

// CSRFHeader carries the CSRF token on cookie-authenticated writes.
const CSRFHeader = "X-CSRF-Token"

// Principal is who a request acts as.
type Principal struct {
	// AccountID is the tenant every store query is scoped to.
	AccountID string
	// Scope is the token scope; browser sessions are read-only.
	Scope string
	// MachineID binds an ingest token to one machine.
	MachineID string
	// TokenID is the id of the bearer token, empty for browser sessions.
	TokenID string
	// SessionID and CSRF are set for browser sessions only. SessionID is
	// the stored session id, not the cookie value.
	SessionID string
	CSRF      string
}

const challenge = "Bearer realm=\"firekeeper\""

type ctxKey struct{}

// From returns the principal the middleware attached, if any.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// Middleware authenticates requests and serves the signup and login
// endpoints.
type Middleware struct {
	store  *store.Store
	now    func() time.Time
	signup SignupMode

	// guarded is true when the server started with active tokens. Auth is
	// then required for good, even if every token is later revoked.
	guarded atomic.Bool
	// accounts latches true once any real account exists.
	accounts atomic.Bool

	bearerFails *limiter
	loginFails  *limiter
	signups     *limiter
	linkStarts  *limiter
	linkFails   *limiter
}

// Option configures New.
type Option func(*Middleware)

// WithSignup sets who may create accounts. The default is SignupClosed.
func WithSignup(mode SignupMode) Option { return func(m *Middleware) { m.signup = mode } }

// New returns a Middleware backed by s. Authentication is required when s
// holds an active token or any account besides the default one. Otherwise
// every request acts as the default account, which keeps a fresh
// single-user server free of signup and login. The first account created
// later turns authentication on.
func New(s *store.Store, opts ...Option) *Middleware {
	m := &Middleware{
		store:       s,
		now:         time.Now,
		signup:      SignupClosed,
		bearerFails: newLimiter(MaxFailures, Window),
		loginFails:  newLimiter(MaxFailures, Window),
		signups:     newLimiter(MaxSignups, Window),
		linkStarts:  newLimiter(MaxLinkStarts, Window),
		linkFails:   newLimiter(MaxFailures, Window),
	}
	for _, o := range opts {
		o(m)
	}
	// Fail closed: if the store cannot be read, require authentication.
	toks, err := s.ListTokens(context.Background(), true)
	m.guarded.Store(err != nil || len(toks) > 0)
	return m
}

// Required reports whether requests must authenticate.
func (m *Middleware) Required(ctx context.Context) (bool, error) {
	if m.guarded.Load() || m.accounts.Load() {
		return true, nil
	}
	has, err := m.store.HasAccounts(ctx)
	if err != nil {
		return true, err
	}
	if has {
		m.accounts.Store(true)
	}
	return has, nil
}

// Wrap returns next guarded by authentication. Ingest and heartbeat need an
// ingest token; every other route needs a read token or a browser session.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := m.now()
		ip := clientIP(r)
		if m.bearerFails.limited(ip, now) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many failed attempts")
			return
		}
		required, err := m.Required(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		if !required {
			p := Principal{AccountID: store.DefaultAccountID}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
			return
		}

		var p Principal
		if h := r.Header.Get("Authorization"); h != "" {
			var ok bool
			if p, ok = m.bearerPrincipal(w, r, h, ip, now); !ok {
				return
			}
		} else if c, err := r.Cookie(CookieName); err == nil {
			var ok bool
			if p, ok = m.sessionPrincipal(w, r, c.Value, now); !ok {
				return
			}
		} else {
			m.bearerFails.record(ip, now)
			w.Header().Set("WWW-Authenticate", challenge)
			writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or malformed bearer token")
			return
		}
		if want := requiredScope(r); want != "" && p.Scope != want {
			writeErr(w, http.StatusForbidden, "forbidden", "token scope does not permit this request")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

func (m *Middleware) bearerPrincipal(w http.ResponseWriter, r *http.Request, header, ip string, now time.Time) (Principal, bool) {
	secret, ok := bearer(header)
	if !ok {
		m.bearerFails.record(ip, now)
		w.Header().Set("WWW-Authenticate", challenge)
		writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or malformed bearer token")
		return Principal{}, false
	}
	tok, err := m.lookup(r.Context(), secret)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return Principal{}, false
	}
	if tok != nil {
		acct, err := m.store.GetAccount(r.Context(), tok.AccountID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return Principal{}, false
		}
		if acct.Disabled {
			tok = nil
		}
	}
	if tok == nil {
		m.bearerFails.record(ip, now)
		w.Header().Set("WWW-Authenticate", challenge)
		writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid or revoked token")
		return Principal{}, false
	}
	// Last-used times are a convenience; a failed write must not fail the request.
	_ = m.store.TouchToken(r.Context(), tok.ID, now)
	return Principal{AccountID: tok.AccountID, Scope: tok.Scope, MachineID: tok.MachineID, TokenID: tok.ID}, true
}

// sessionPrincipal authenticates a browser session. Cookies ride along on
// cross-site requests, so every write must also carry the session's CSRF
// token and come from the same origin.
func (m *Middleware) sessionPrincipal(w http.ResponseWriter, r *http.Request, cookie string, now time.Time) (Principal, bool) {
	ws, err := m.store.LookupWebSession(r.Context(), cookie, now)
	if err == store.ErrNotFound {
		// A stale cookie is not a guessing attempt, so it is not counted.
		ClearCookie(w, r)
		writeErr(w, http.StatusUnauthorized, "unauthorized", "session expired or invalid")
		return Principal{}, false
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return Principal{}, false
	}
	if !safeMethod(r.Method) {
		if !sameOrigin(r) {
			writeErr(w, http.StatusForbidden, "csrf_failed", "cross-origin request refused")
			return Principal{}, false
		}
		got := r.Header.Get(CSRFHeader)
		if subtle.ConstantTimeCompare([]byte(got), []byte(ws.CSRFToken)) != 1 {
			writeErr(w, http.StatusForbidden, "csrf_failed", "missing or invalid CSRF token")
			return Principal{}, false
		}
	}
	return Principal{AccountID: ws.AccountID, Scope: store.ScopeRead, SessionID: ws.ID, CSRF: ws.CSRFToken}, true
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sameOrigin accepts a request with no Origin header (not a browser form or
// fetch from another site) or one whose Origin host is the request host.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host != "" && u.Host == r.Host
}

// requiredScope is the token scope a route needs. Empty means any valid
// credential: a token may ask who it is and revoke itself whatever its scope.
func requiredScope(r *http.Request) string {
	if r.URL.Path == "/v1/account" || r.URL.Path == "/v1/tokens/current" {
		return ""
	}
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

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write([]byte(`{"error":"` + msg + `","code":"` + code + `"}` + "\n"))
}
