package web

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/server/auth"
)

func gated(st auth.PageState, err error) http.Handler {
	return Handler(WithGate(func(*http.Request) (auth.PageState, error) { return st, err }))
}

func TestPageGate(t *testing.T) {
	multi := auth.PageState{Required: true, Accounts: true, Signup: auth.SignupInvite}
	signedIn := multi
	signedIn.SignedIn = true
	tokenOnly := auth.PageState{Required: true, Signup: auth.SignupClosed}
	single := auth.PageState{Signup: auth.SignupClosed}

	tests := []struct {
		name     string
		h        http.Handler
		path     string
		code     int
		location string
		contains string
	}{
		{"signed out page redirects", gated(multi, nil), "/", http.StatusSeeOther, "/login", ""},
		{"signed out index.html redirects", gated(multi, nil), "/index.html", http.StatusSeeOther, "/login", ""},
		{"assets stay public", gated(multi, nil), "/app.js", http.StatusOK, "", "toLogin"},
		{"login page is public", gated(multi, nil), "/login", http.StatusOK, "", `data-signup="invite" data-accounts="true"`},
		{"signup page is public", gated(multi, nil), "/signup", http.StatusOK, "", `data-signup="invite"`},
		{"mock page is never gated", gated(multi, nil), "/?mock=1", http.StatusOK, "", "app.js"},
		{"mock login simulates open signup", gated(single, nil), "/login?mock=1", http.StatusOK, "", `data-signup="open" data-accounts="true"`},
		{"signed in page loads", gated(signedIn, nil), "/", http.StatusOK, "", "app.js"},
		{"signed in login bounces home", gated(signedIn, nil), "/login", http.StatusSeeOther, "/", ""},
		{"single-user page loads", gated(single, nil), "/", http.StatusOK, "", "app.js"},
		{"single-user login bounces home", gated(single, nil), "/login", http.StatusSeeOther, "/", ""},
		{"token-only page loads for the token form", gated(tokenOnly, nil), "/", http.StatusOK, "", "app.js"},
		{"token-only login offers tokens", gated(tokenOnly, nil), "/login", http.StatusOK, "", `data-accounts="false"`},
		{"gate error fails closed", gated(single, errors.New("db down")), "/", http.StatusSeeOther, "/login", ""},
		{"gate error on login is a 500", gated(single, errors.New("db down")), "/login", http.StatusInternalServerError, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := serve(t, tt.h, "GET", tt.path)
			if rr.Code != tt.code {
				t.Fatalf("status %d, want %d", rr.Code, tt.code)
			}
			if tt.location != "" && rr.Header().Get("Location") != tt.location {
				t.Fatalf("location %q, want %q", rr.Header().Get("Location"), tt.location)
			}
			if !strings.Contains(rr.Body.String(), tt.contains) {
				t.Fatalf("body missing %q", tt.contains)
			}
			if csp := rr.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
				t.Fatalf("CSP %q", csp)
			}
		})
	}
}
