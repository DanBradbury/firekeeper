// Package web serves the dashboard's browser UI. The assets are plain HTML,
// CSS, and JavaScript modules embedded in the binary, with no build step and
// no external network requests.
//
// The UI talks to the v1 API on the same origin. Mount Handler at "/" next to
// the API handler at "/v1/". Opening the page with ?mock=1 swaps the API for
// synthetic fixtures under static/mock so the UI works without a server.
package web

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/DanBradbury/firekeeper/internal/server/auth"
)

//go:embed static
var static embed.FS

// csp keeps the page on its own origin: scripts, styles, images, and
// fetches may only load from the serving host.
const csp = "default-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"

// Gate reports what a page load may see. Nil means the server never needs
// credentials, as in single-user mode.
type Gate func(r *http.Request) (auth.PageState, error)

// Option configures Handler.
type Option func(*options)

type options struct{ gate Gate }

// WithGate makes page loads follow the server's sign-in state: once an
// account exists, the dashboard page redirects to /login without a browser
// session.
func WithGate(g Gate) Option { return func(o *options) { o.gate = g } }

// loginPage is the sign-in and sign-up page. Its body carries the server's
// signup mode and whether accounts exist as data attributes, filled in per
// request, since the CSP forbids inline scripts.
const loginPage = "static/login.html"

// linkPage approves a machine that ran `firekeeper login`. It is served at
// /link and, like the dashboard, needs a browser session once accounts exist.
const linkPage = "static/link.html"

// Handler serves the UI. Views are routed by URL fragment, so unknown paths
// return 404 rather than the index and API typos are not masked by HTML.
// Static assets and the sign-in pages need no credentials; with a gate,
// the dashboard page itself needs a browser session once accounts exist.
// "?mock=1" pages are never gated: they show only synthetic fixtures, and
// the mock UI simulates sign-in itself.
func Handler(opts ...Option) http.Handler {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	loginHTML, err := static.ReadFile(loginPage)
	if err != nil {
		panic(err)
	}
	linkHTML, err := static.ReadFile(linkPage)
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// No directory listings; "/" serves index.html.
		if r.URL.Path != "/" && strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		// Assets change with the binary; revalidate rather than cache stale
		// modules across upgrades.
		h.Set("Cache-Control", "no-cache")

		mock := r.URL.Query().Get("mock") == "1"
		switch r.URL.Path {
		case "/", "/index.html":
			if o.gate != nil && !mock {
				st, err := o.gate(r)
				if err != nil || (st.Accounts && !st.SignedIn) {
					redirect(w, r, "/login")
					return
				}
			}
		case "/link":
			if o.gate != nil && !mock {
				st, err := o.gate(r)
				if err != nil || (st.Accounts && !st.SignedIn) {
					// The browser carries the URL fragment (the code) across.
					redirect(w, r, "/login?next=link")
					return
				}
			}
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Cache-Control", "no-store")
			if r.Method != http.MethodHead {
				w.Write(linkHTML)
			}
			return
		case "/login", "/signup":
			st := auth.PageState{Required: true, Signup: auth.SignupClosed}
			if o.gate != nil && !mock {
				var err error
				if st, err = o.gate(r); err != nil {
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
				// Nothing to sign in to, or already signed in.
				if !st.Required || (st.Accounts && st.SignedIn) {
					if r.URL.Query().Get("next") == "link" {
						redirect(w, r, "/link")
						return
					}
					redirect(w, r, "/")
					return
				}
			}
			if mock {
				st = auth.PageState{Required: true, Accounts: true, Signup: auth.SignupOpen}
			}
			page := strings.Replace(string(loginHTML), `data-signup="" data-accounts=""`,
				fmt.Sprintf(`data-signup=%q data-accounts=%q`, string(st.Signup), strconv.FormatBool(st.Accounts)), 1)
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Cache-Control", "no-store")
			if r.Method == http.MethodHead {
				return
			}
			io.WriteString(w, page)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// redirect sends a 303 to path. Browsers carry the URL
// fragment (the dashboard's route) across the redirect.
func redirect(w http.ResponseWriter, r *http.Request, path string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, path, http.StatusSeeOther)
}
