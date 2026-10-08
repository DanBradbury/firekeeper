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
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var static embed.FS

// csp keeps the page on its own origin: scripts, styles, images, and
// fetches may only load from the serving host.
const csp = "default-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"

// Handler serves the UI. Views are routed by URL fragment, so unknown paths
// return 404 rather than the index and API typos are not masked by HTML.
func Handler() http.Handler {
	sub, err := fs.Sub(static, "static")
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
		files.ServeHTTP(w, r)
	})
}
