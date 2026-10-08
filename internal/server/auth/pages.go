package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/DanBradbury/firekeeper/internal/server/store"
)

// PageState is what the web UI needs to decide whether a page load may
// proceed. It never reports why a cookie failed.
type PageState struct {
	// Required is true when the API needs credentials at all.
	Required bool
	// Accounts is true once any real account exists: page loads then need
	// a browser session, and the sign-in pages are live.
	Accounts bool
	// SignedIn is true when the request carries a live browser session.
	SignedIn bool
	// Signup is the server's signup mode.
	Signup SignupMode
}

// Page reports the state a page load sees. A store error fails closed:
// the caller should treat the page as needing a sign-in.
func (m *Middleware) Page(r *http.Request) (PageState, error) {
	st, err := m.state(r.Context())
	if err != nil || !st.Required {
		return st, err
	}
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		_, err := m.store.LookupWebSession(r.Context(), c.Value, m.now())
		switch {
		case err == nil:
			st.SignedIn = true
		case !errors.Is(err, store.ErrNotFound):
			return st, err
		}
	}
	return st, nil
}

func (m *Middleware) state(ctx context.Context) (PageState, error) {
	closed := PageState{Required: true, Accounts: true, Signup: m.signup}
	required, err := m.Required(ctx)
	if err != nil {
		return closed, err
	}
	st := PageState{Required: required, Signup: m.signup}
	if !required {
		return st, nil
	}
	if !m.accounts.Load() {
		has, err := m.store.HasAccounts(ctx)
		if err != nil {
			return closed, err
		}
		if has {
			m.accounts.Store(true)
		}
	}
	st.Accounts = m.accounts.Load()
	return st, nil
}

// Mode describes the server's current mode for the startup message.
func (m *Middleware) Mode(ctx context.Context) string {
	st, err := m.state(ctx)
	switch {
	case err != nil:
		return "unknown (database error); requests need credentials"
	case !st.Required:
		return "single-user: no accounts or tokens, so the dashboard and API are open"
	case !st.Accounts:
		return "single-user, token-protected: no accounts yet; the API needs a bearer token"
	}
	return "multi-user: every page and API call needs a sign-in or a bearer token"
}
