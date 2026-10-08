package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/store"
)

// SignupMode says who may create an account.
type SignupMode string

const (
	// SignupClosed refuses every signup. It is the default.
	SignupClosed SignupMode = "closed"
	// SignupInvite needs a valid, unused invite code.
	SignupInvite SignupMode = "invite"
	// SignupOpen lets anyone who can reach the server sign up.
	SignupOpen SignupMode = "open"
)

// ParseSignupMode validates a --signup value.
func ParseSignupMode(s string) (SignupMode, error) {
	switch m := SignupMode(s); m {
	case SignupClosed, SignupInvite, SignupOpen:
		return m, nil
	}
	return "", fmt.Errorf("signup mode must be closed, invite, or open, not %q", s)
}

// MaxSignups is the signup attempts allowed per IP and per email in Window.
// Every attempt counts, not only failures, because a success costs the
// server an account.
const MaxSignups = 5

// CookieName is the browser session cookie.
const CookieName = "fk_session"

// maxAuthBody bounds login and signup bodies.
const maxAuthBody = 8 << 10

// AccountInfo is the account as login, signup and /v1/account report it.
type AccountInfo struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// sessionResponse is the body of a successful login or signup. The CSRF
// token must accompany every later write.
type sessionResponse struct {
	Account   AccountInfo `json:"account"`
	CSRFToken string      `json:"csrf_token"`
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, value string, expires, now time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: value, Path: "/", Expires: expires,
		MaxAge:   int(expires.Sub(now).Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureRequest(r),
	})
}

// ClearCookie tells the browser to drop the session cookie.
func ClearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureRequest(r),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// readCredentials decodes a login or signup body. Requiring JSON and the
// same origin keeps another site from posting a form that logs the victim
// in as the attacker.
func readCredentials(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "csrf_failed", "cross-origin request refused")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "bad_request", "unexpected data after JSON body")
		return false
	}
	return true
}

// emailKey is the limiter key for an email: normalized when valid, else
// the trimmed lowercase text, so garbage cannot dodge the limit.
func emailKey(email string) string {
	if n, err := store.NormalizeEmail(email); err == nil {
		return "email:" + n
	}
	e := strings.ToLower(strings.TrimSpace(email))
	if len(e) > store.MaxEmailLen {
		e = e[:store.MaxEmailLen]
	}
	return "email:" + e
}

func (m *Middleware) startSession(w http.ResponseWriter, r *http.Request, status int, a store.Account, now time.Time) {
	cookie, ws, err := m.store.CreateWebSession(r.Context(), a.ID, SessionTTL, now)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	setSessionCookie(w, r, cookie, ws.ExpiresAt, now)
	writeJSON(w, status, sessionResponse{Account: AccountInfo{ID: a.ID, Email: a.Email}, CSRFToken: ws.CSRFToken})
}

// Login serves POST /v1/auth/login. A failed attempt counts against the
// client IP and against the email, and every failure looks the same
// whether or not the email has an account.
func (m *Middleware) Login() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if !readCredentials(w, r, &req) {
			return
		}
		now := m.now()
		ipKey, eKey := "ip:"+clientIP(r), emailKey(req.Email)
		if m.loginFails.limited(ipKey, now) || m.loginFails.limited(eKey, now) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many failed attempts")
			return
		}
		fail := func() {
			m.loginFails.record(ipKey, now)
			m.loginFails.record(eKey, now)
			writeErr(w, http.StatusUnauthorized, "invalid_credentials", "incorrect email or password")
		}
		if len(req.Password) > store.MaxPasswordLen {
			fail()
			return
		}
		a, err := m.store.AccountByEmail(r.Context(), req.Email)
		if errors.Is(err, store.ErrNotFound) {
			burnPassword(req.Password)
			fail()
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		if !VerifyPassword(a.PasswordHash, req.Password) || a.Disabled {
			fail()
			return
		}
		m.accounts.Store(true)
		m.startSession(w, r, http.StatusOK, a, now)
	})
}

// Signup serves POST /v1/auth/signup according to the signup mode.
func (m *Middleware) Signup() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.signup == SignupClosed {
			writeErr(w, http.StatusForbidden, "signup_closed", "signup is closed")
			return
		}
		var req struct {
			Email      string `json:"email"`
			Password   string `json:"password"`
			InviteCode string `json:"invite_code"`
		}
		if !readCredentials(w, r, &req) {
			return
		}
		now := m.now()
		ipKey, eKey := "ip:"+clientIP(r), emailKey(req.Email)
		if m.signups.limited(ipKey, now) || m.signups.limited(eKey, now) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many signup attempts")
			return
		}
		m.signups.record(ipKey, now)
		m.signups.record(eKey, now)

		email, err := store.NormalizeEmail(req.Email)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", "a valid email is required")
			return
		}
		if n := len([]rune(req.Password)); n < store.MinPasswordLen || len(req.Password) > store.MaxPasswordLen {
			writeErr(w, http.StatusBadRequest, "invalid_request",
				fmt.Sprintf("password must be %d to %d characters", store.MinPasswordLen, store.MaxPasswordLen))
			return
		}
		invite := ""
		if m.signup == SignupInvite {
			if invite = strings.TrimSpace(req.InviteCode); invite == "" {
				writeErr(w, http.StatusForbidden, "invite_required", "an invite code is required")
				return
			}
		}
		hash, err := HashPassword(req.Password)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		a, err := m.store.CreateAccount(r.Context(), email, hash, invite, now)
		switch {
		case errors.Is(err, store.ErrInviteInvalid):
			writeErr(w, http.StatusForbidden, "invite_invalid", "invite code is invalid, used, or expired")
			return
		case errors.Is(err, store.ErrEmailTaken):
			writeErr(w, http.StatusConflict, "email_taken", "an account with that email already exists")
			return
		case err != nil:
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		m.accounts.Store(true)
		m.startSession(w, r, http.StatusCreated, a, now)
	})
}
