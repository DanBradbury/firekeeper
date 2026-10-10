package auth

import (
	"errors"
	"net/http"

	"github.com/DanBradbury/firekeeper/internal/server/store"
)

// Machine linking: the device-code flow behind `firekeeper login`.
//
//	POST /v1/link/start    machine, no credentials: get a device and user code
//	POST /v1/link/poll     machine, device code: wait for approval, get a token
//	POST /v1/link/approve  signed-in browser session: approve a user code
//
// The token is created by the poll that consumes the approval, so it
// reaches only the machine holding the device code.

// Link limits.
const (
	// MaxLinkStarts is how many links one IP may start per Window.
	MaxLinkStarts = 10
	// LinkPollInterval is the polling interval the server asks for, in seconds.
	LinkPollInterval = 2
)

type linkStartRequest struct {
	MachineID string `json:"machine_id"`
}

type linkStartResponse struct {
	DeviceCode string `json:"device_code"`
	UserCode   string `json:"user_code"`
	ExpiresIn  int    `json:"expires_in"`
	Interval   int    `json:"interval"`
}

// LinkStart serves POST /v1/link/start. It needs no credentials, so it is
// limited per IP, and it refuses when the server has no account that could
// approve the code.
func (m *Middleware) LinkStart() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req linkStartRequest
		if !readCredentials(w, r, &req) {
			return
		}
		now := m.now()
		ip := "ip:" + m.clientIP(r)
		if m.linkStarts.limited(ip, now) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many link attempts")
			return
		}
		m.linkStarts.record(ip, now)
		st, err := m.state(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		if !st.Accounts {
			writeErr(w, http.StatusConflict, "accounts_required", "this server has no accounts, so no one can approve a link")
			return
		}
		ls, err := m.store.StartLink(r.Context(), req.MachineID, now)
		switch {
		case errors.Is(err, store.ErrInvalid):
			writeErr(w, http.StatusBadRequest, "invalid_request", "machine_id must be 1 to 128 printable characters")
			return
		case errors.Is(err, store.ErrLinkBusy):
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusServiceUnavailable, "unavailable", "too many pending links; try again shortly")
			return
		case err != nil:
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, http.StatusCreated, linkStartResponse{
			DeviceCode: ls.DeviceCode, UserCode: ls.UserCode,
			ExpiresIn: int(ls.ExpiresAt.Sub(now).Seconds()), Interval: LinkPollInterval,
		})
	})
}

type linkPollResponse struct {
	Status  string       `json:"status"`
	Token   string       `json:"token,omitempty"`
	TokenID string       `json:"token_id,omitempty"`
	Account *AccountInfo `json:"account,omitempty"`
	Machine *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"machine,omitempty"`
}

// LinkPoll serves POST /v1/link/poll. Waiting is not a failure; an unknown
// or reused device code counts against the client IP.
func (m *Middleware) LinkPoll() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			DeviceCode string `json:"device_code"`
		}
		if !readCredentials(w, r, &req) {
			return
		}
		now := m.now()
		ip := "ip:" + m.clientIP(r)
		if m.linkFails.limited(ip, now) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many failed attempts")
			return
		}
		res, err := m.store.PollLink(r.Context(), req.DeviceCode, now)
		switch {
		case errors.Is(err, store.ErrLinkExpired):
			writeErr(w, http.StatusGone, "link_expired", "the link code expired; run login again")
			return
		case errors.Is(err, store.ErrLinkInvalid):
			m.linkFails.record(ip, now)
			writeErr(w, http.StatusNotFound, "link_invalid", "unknown or already used link code")
			return
		case err != nil:
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		if !res.Approved {
			writeJSON(w, http.StatusOK, linkPollResponse{Status: "pending"})
			return
		}
		out := linkPollResponse{Status: "approved", Token: res.Secret, TokenID: res.Token.ID,
			Account: &AccountInfo{ID: res.Account.ID, Email: res.Account.Email}}
		out.Machine = &struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}{res.Token.MachineID, res.Token.Name}
		writeJSON(w, http.StatusOK, out)
	})
}

// LinkApprove serves POST /v1/link/approve. Mount it behind Wrap, which
// checks the browser session, its CSRF token and the origin. Only a browser
// session may approve: a bearer token cannot add machines.
func (m *Middleware) LinkApprove() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := From(r.Context())
		if !ok || p.SessionID == "" {
			writeErr(w, http.StatusForbidden, "session_required", "approving a machine needs a signed-in browser session")
			return
		}
		var req struct {
			UserCode    string `json:"user_code"`
			MachineName string `json:"machine_name"`
		}
		if !readCredentials(w, r, &req) {
			return
		}
		now := m.now()
		keys := []string{"ip:" + m.clientIP(r), "account:" + p.AccountID}
		for _, k := range keys {
			if m.linkFails.limited(k, now) {
				w.Header().Set("Retry-After", "60")
				writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many failed attempts")
				return
			}
		}
		a, err := m.store.ApproveLink(r.Context(), p.AccountID, req.UserCode, req.MachineName, now)
		switch {
		case errors.Is(err, store.ErrInvalid):
			writeErr(w, http.StatusBadRequest, "invalid_request", "machine name must be 1 to 64 printable characters")
			return
		case errors.Is(err, store.ErrLinkInvalid):
			for _, k := range keys {
				m.linkFails.record(k, now)
			}
			writeErr(w, http.StatusNotFound, "link_invalid", "that code is unknown, expired, or already used")
			return
		case err != nil:
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "machine": map[string]string{"id": a.MachineID, "name": a.MachineName},
		})
	})
}
