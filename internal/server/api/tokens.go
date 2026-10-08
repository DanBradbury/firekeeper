package api

import (
	"net/http"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
)

// tokenView is a token as the API reports it. The secret is never part of
// it; only creation returns the secret, once.
type tokenView struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Scope      string  `json:"scope"`
	MachineID  string  `json:"machine_id"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
	RevokedAt  *string `json:"revoked_at"`
}

func viewOf(t store.Token) tokenView {
	f := func(p *time.Time) *string {
		if p == nil {
			return nil
		}
		s := p.UTC().Format(time.RFC3339)
		return &s
	}
	return tokenView{ID: t.ID, Name: t.Name, Scope: t.Scope, MachineID: t.MachineID,
		CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339), LastUsedAt: f(t.LastUsedAt), RevokedAt: f(t.RevokedAt)}
}

// sessionOnly reports whether the caller is a signed-in browser session.
// Token management is an account action: a leaked bearer token must not be
// able to list, mint or revoke other tokens.
func sessionOnly(w http.ResponseWriter, r *http.Request) bool {
	if p, ok := auth.From(r.Context()); ok && p.SessionID != "" {
		return true
	}
	writeErr(w, http.StatusForbidden, "session_required", "managing tokens needs a signed-in browser session")
	return false
}

func listTokens(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sessionOnly(w, r) {
			return
		}
		toks, err := s.ListAccountTokens(r.Context(), accountID(r))
		if err != nil {
			storeErr(w, err)
			return
		}
		out := make([]tokenView, 0, len(toks))
		for i := len(toks) - 1; i >= 0; i-- { // newest first
			out = append(out, viewOf(toks[i]))
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
	}
}

type createTokenRequest struct {
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	MachineID string `json:"machine_id"`
}

type createTokenResponse struct {
	tokenView
	// Token is the secret. It is shown here once and cannot be recovered.
	Token string `json:"token"`
}

func createToken(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sessionOnly(w, r) {
			return
		}
		var req createTokenRequest
		if !decode(w, r, &req) {
			return
		}
		t, secret, err := s.CreateToken(r.Context(), accountID(r), req.Name, req.Scope, req.MachineID)
		if err != nil {
			storeErr(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusCreated, createTokenResponse{tokenView: viewOf(t), Token: secret})
	}
}

// revokeToken serves DELETE /v1/tokens/{id}. A token of another account is
// not found.
func revokeToken(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sessionOnly(w, r) {
			return
		}
		ok, err := s.RevokeAccountToken(r.Context(), accountID(r), r.PathValue("id"))
		if err != nil {
			storeErr(w, err)
			return
		}
		if !ok {
			writeErr(w, http.StatusNotFound, "not_found", "no active token with that id")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// revokeCurrent serves DELETE /v1/tokens/current: the token making the
// request revokes itself, which is how `firekeeper logout` cleans up.
func revokeCurrent(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.From(r.Context())
		if !ok || p.TokenID == "" {
			writeErr(w, http.StatusBadRequest, "bad_request", "this request is not authenticated with a token")
			return
		}
		if _, err := s.RevokeAccountToken(r.Context(), p.AccountID, p.TokenID); err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
