package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

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
	// Usage is what the account stores now; Limits is what it may store.
	// Zero limits mean none. The default account is never limited.
	Usage  store.AccountStats `json:"usage"`
	Limits Limits             `json:"limits"`
}

// account serves GET /v1/account: who the caller is.
func account(s *store.Store, lim Limits) http.HandlerFunc {
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
		if resp.Usage, err = s.AccountStats(r.Context(), a.ID); err != nil {
			storeErr(w, err)
			return
		}
		if !resp.SingleUser {
			resp.Limits = lim
		}
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

type deleteAccountRequest struct {
	Confirm string `json:"confirm"`
}

// deleteAccount serves DELETE /v1/account: it removes the caller's account
// and everything it owns. It needs a browser session (so the CSRF check has
// run) and the account's email as confirmation. Tokens, including read
// tokens, cannot delete an account, and neither can the built-in default
// account's single-user mode, which has no owner to sign in as.
func deleteAccount(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.From(r.Context())
		if !ok || p.SessionID == "" {
			writeErr(w, http.StatusForbidden, "forbidden", "deleting an account needs a signed-in browser session")
			return
		}
		a, err := s.GetAccount(r.Context(), p.AccountID)
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not_found", "account not found")
			return
		}
		if err != nil {
			storeErr(w, err)
			return
		}
		if a.ID == store.DefaultAccountID {
			writeErr(w, http.StatusForbidden, "forbidden", "the built-in account cannot be deleted")
			return
		}
		var req deleteAccountRequest
		if !decode(w, r, &req) {
			return
		}
		if confirm, err := store.NormalizeEmail(req.Confirm); err != nil || confirm != a.Email {
			writeErr(w, http.StatusBadRequest, "invalid_request", "confirm must be the account's email address")
			return
		}
		if _, err := s.DeleteAccount(r.Context(), a.ID); err != nil {
			storeErr(w, err)
			return
		}
		auth.ClearCookie(w, r)
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	}
}

// exportAccount serves GET /v1/account/export: everything stored for the
// caller's account as JSON Lines, one record per line, each with a "type"
// (account, machine, token, session, file, event). Events are the same
// objects GET /v1/sessions/{uid}/events returns, so they can be re-ingested.
func exportAccount(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acct := accountID(r)
		if _, err := s.GetAccount(r.Context(), acct); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeErr(w, http.StatusNotFound, "not_found", "account not found")
				return
			}
			storeErr(w, err)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "application/x-ndjson")
		h.Set("Content-Disposition", `attachment; filename="firekeeper-export.jsonl"`)
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		// A large export outlives any server-wide write timeout.
		_ = rc.SetWriteDeadline(time.Time{})

		n := 0
		err := s.ExportAccount(r.Context(), acct, func(typ string, v any) error {
			b, err := json.Marshal(v)
			if err != nil || len(b) < 2 || b[0] != '{' {
				return errors.New("export: record is not a JSON object")
			}
			line := make([]byte, 0, len(b)+len(typ)+16)
			line = append(line, `{"type":`...)
			line = strconv.AppendQuote(line, typ)
			if len(b) > 2 {
				line = append(line, ',')
			}
			line = append(line, b[1:]...)
			line = append(line, '\n')
			if _, err := w.Write(line); err != nil {
				return err
			}
			if n++; n%200 == 0 {
				return rc.Flush()
			}
			return nil
		})
		if err != nil && r.Context().Err() == nil {
			// The status is already sent; mark the truncation in the body so
			// a consumer does not mistake a partial export for a whole one.
			fmt.Fprint(w, `{"type":"error","error":"export interrupted; it is incomplete"}`+"\n")
		}
	}
}
