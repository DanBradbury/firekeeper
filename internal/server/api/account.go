package api

import (
	"errors"
	"net/http"

	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
)

type accountResponse struct {
	auth.AccountInfo
	// SingleUser is true when the server has no accounts and every request
	// acts as the built-in default account.
	SingleUser bool `json:"single_user"`
	// CSRFToken is set for browser sessions: send it as X-CSRF-Token on
	// every write.
	CSRFToken string `json:"csrf_token,omitempty"`
}

// account serves GET /v1/account: who the caller is.
func account(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, err := s.GetAccount(r.Context(), accountID(r))
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not_found", "account not found")
			return
		}
		if err != nil {
			storeErr(w, err)
			return
		}
		resp := accountResponse{AccountInfo: auth.AccountInfo{ID: a.ID, Email: a.Email}, SingleUser: a.ID == store.DefaultAccountID}
		if p, ok := auth.From(r.Context()); ok {
			resp.CSRFToken = p.CSRF
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, resp)
	}
}

// logout serves POST /v1/auth/logout: it deletes the caller's browser
// session and clears the cookie. The CSRF check already ran in the auth
// middleware. Callers without a browser session have nothing to end.
func logout(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.From(r.Context())
		if !ok || p.SessionID == "" {
			writeErr(w, http.StatusBadRequest, "bad_request", "not signed in with a browser session")
			return
		}
		if err := s.DeleteWebSession(r.Context(), p.SessionID); err != nil {
			storeErr(w, err)
			return
		}
		auth.ClearCookie(w, r)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
