package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
)

// syncBuffer is a bytes.Buffer safe to read while Run writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func seedDB(t *testing.T, seed func(*store.Store)) string {
	t.Helper()
	db := filepath.Join(t.TempDir(), "d", "dashboard.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		seed(s)
	}
	s.Close()
	return db
}

func TestRunPrintsMode(t *testing.T) {
	tests := []struct {
		name string
		seed func(*store.Store)
		want string
	}{
		{"single-user", nil, "mode: single-user: no accounts or tokens"},
		{"token-only", func(s *store.Store) {
			if _, _, err := s.CreateToken(context.Background(), store.DefaultAccountID, "r", store.ScopeRead, ""); err != nil {
				t.Fatal(err)
			}
		}, "mode: single-user, token-protected"},
		{"multi-user", func(s *store.Store) {
			if _, err := s.CreateAccount(context.Background(), "a@example.com", "$argon2id$stub", "", time.Now()); err != nil {
				t.Fatal(err)
			}
		}, "mode: multi-user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out syncBuffer
			_, stop := start(t, Config{DB: seedDB(t, tt.seed), Out: &out})
			stop()
			if !strings.Contains(out.String(), tt.want) {
				t.Fatalf("output %q lacks %q", out.String(), tt.want)
			}
		})
	}
}

// A browser on a multi-user server is sent to /login, signs in, sees the
// page, and after logout is sent back to /login.
func TestRunPageRedirectsAndLogin(t *testing.T) {
	const pw = "correct horse battery"
	hash, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	db := seedDB(t, func(s *store.Store) {
		if _, err := s.CreateAccount(context.Background(), "a@example.com", hash, "", time.Now()); err != nil {
			t.Fatal(err)
		}
	})
	base, _ := start(t, Config{DB: db})
	jar, _ := cookiejar.New(nil)
	var redirects []string
	client := &http.Client{Jar: jar, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		redirects = append(redirects, r.URL.Path)
		return nil
	}}
	fetch := func(method, path, body, csrf string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if csrf != "" {
			req.Header.Set(auth.CSRFHeader, csrf)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	redirects = nil
	if resp, body := fetch("GET", "/", "", ""); resp.Request.URL.Path != "/login" || !strings.Contains(body, "login.js") {
		t.Fatalf("signed out: landed on %s", resp.Request.URL.Path)
	}
	if resp, _ := fetch("POST", "/v1/auth/login", `{"email":"a@example.com","password":"wrong password!"}`, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	resp, body := fetch("POST", "/v1/auth/login", `{"email":"a@example.com","password":"`+pw+`"}`, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	csrf := body[strings.Index(body, `"csrf_token":"`)+len(`"csrf_token":"`):]
	csrf = csrf[:strings.Index(csrf, `"`)]

	if resp, body := fetch("GET", "/", "", ""); resp.Request.URL.Path != "/" || !strings.Contains(body, "app.js") {
		t.Fatalf("signed in: landed on %s", resp.Request.URL.Path)
	}
	if resp, _ := fetch("GET", "/login", "", ""); resp.Request.URL.Path != "/" {
		t.Fatalf("signed in /login: landed on %s", resp.Request.URL.Path)
	}
	if resp, _ := fetch("POST", "/v1/auth/logout", "", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("logout without CSRF: %d", resp.StatusCode)
	}
	if resp, _ := fetch("POST", "/v1/auth/logout", "", csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp, _ := fetch("GET", "/", "", ""); resp.Request.URL.Path != "/login" {
		t.Fatalf("after logout: landed on %s", resp.Request.URL.Path)
	}
	if resp, _ := fetch("GET", "/app.css", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("assets must stay public: %d", resp.StatusCode)
	}
}
