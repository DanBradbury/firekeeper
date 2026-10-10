package auth_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/server/api"
	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustProxies(t *testing.T, specs ...string) *auth.Middleware {
	t.Helper()
	p, err := auth.ParseTrustedProxies(specs)
	if err != nil {
		t.Fatal(err)
	}
	return auth.New(openStore(t), auth.WithTrustedProxies(p))
}

func TestParseTrustedProxies(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		ok   bool
	}{
		{"none", nil, true},
		{"address", []string{"10.0.0.5"}, true},
		{"cidr", []string{"172.29.77.0/24", "fd00::/8"}, true},
		{"ipv6 address", []string{"::1"}, true},
		{"spaces", []string{"  10.0.0.5 "}, true},
		{"mapped", []string{"::ffff:10.0.0.0/104"}, true},
		{"empty", []string{""}, false},
		{"garbage", []string{"proxy.example"}, false},
		{"bad mask", []string{"10.0.0.0/33"}, false},
		{"all ipv4", []string{"0.0.0.0/0"}, false},
		{"all ipv6", []string{"::/0"}, false},
		{"all mapped", []string{"::ffff:0.0.0.0/96"}, false},
		{"one bad of two", []string{"10.0.0.1", "nope"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := auth.ParseTrustedProxies(tc.in)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestClientIP(t *testing.T) {
	proxies := []string{"10.0.0.0/8", "fd00::/8"}
	for _, tc := range []struct {
		name    string
		proxies []string
		remote  string
		xff     []string
		want    string
	}{
		{"no proxies configured ignores header", nil, "192.0.2.7:1234", []string{"198.51.100.9"}, "192.0.2.7"},
		{"no port in remote", nil, "192.0.2.7", nil, "192.0.2.7"},
		{"untrusted peer cannot forge", proxies, "192.0.2.7:1234", []string{"198.51.100.9"}, "192.0.2.7"},
		{"untrusted peer cannot forge a proxy hop", proxies, "192.0.2.7:1234", []string{"198.51.100.9, 10.0.0.2"}, "192.0.2.7"},
		{"trusted peer, one client", proxies, "10.0.0.2:5000", []string{"198.51.100.9"}, "198.51.100.9"},
		{"trusted peer, no header", proxies, "10.0.0.2:5000", nil, "10.0.0.2"},
		{"trusted peer, empty header", proxies, "10.0.0.2:5000", []string{" "}, "10.0.0.2"},
		{"client-supplied prefix is not believed", proxies, "10.0.0.2:5000", []string{"203.0.113.1, 198.51.100.9"}, "198.51.100.9"},
		{"two trusted hops", proxies, "10.0.0.2:5000", []string{"198.51.100.9, 10.0.0.3"}, "198.51.100.9"},
		{"whitespace", proxies, "10.0.0.2:5000", []string{"  203.0.113.1 ,\t198.51.100.9 , 10.0.0.3  "}, "198.51.100.9"},
		{"several header lines", proxies, "10.0.0.2:5000", []string{"203.0.113.1", "198.51.100.9, 10.0.0.3"}, "198.51.100.9"},
		{"client with a port", proxies, "10.0.0.2:5000", []string{"198.51.100.9:44321"}, "198.51.100.9"},
		{"every hop trusted", proxies, "10.0.0.2:5000", []string{"10.0.0.8, 10.0.0.3"}, "10.0.0.2"},
		{"malformed last hop", proxies, "10.0.0.2:5000", []string{"198.51.100.9, not-an-ip"}, "10.0.0.2"},
		{"malformed hop behind a proxy", proxies, "10.0.0.2:5000", []string{"garbage, 10.0.0.3"}, "10.0.0.2"},
		{"empty hop", proxies, "10.0.0.2:5000", []string{"198.51.100.9,,10.0.0.3"}, "10.0.0.2"},
		{"ipv6 client", proxies, "10.0.0.2:5000", []string{"2001:db8:1:2:3:4:5:6"}, "2001:db8:1:2::/64"},
		{"ipv6 client in brackets with port", proxies, "10.0.0.2:5000", []string{"[2001:db8:1:2::9]:443"}, "2001:db8:1:2::/64"},
		{"same /64 shares a key", proxies, "10.0.0.2:5000", []string{"2001:db8:1:2:ffff::1"}, "2001:db8:1:2::/64"},
		{"ipv6 trusted peer", proxies, "[fd00::5]:5000", []string{"198.51.100.9"}, "198.51.100.9"},
		{"ipv6 untrusted peer", proxies, "[2001:db8::5]:5000", []string{"198.51.100.9"}, "2001:db8::/64"},
		{"ipv4-mapped peer is a trusted ipv4 proxy", proxies, "[::ffff:10.0.0.2]:5000", []string{"198.51.100.9"}, "198.51.100.9"},
		{"ipv4-mapped client", proxies, "10.0.0.2:5000", []string{"::ffff:198.51.100.9"}, "198.51.100.9"},
		{"zone is dropped", proxies, "10.0.0.2:5000", []string{"fe80::1%eth0"}, "fe80::/64"},
		{"unparsable remote", proxies, "pipe", []string{"198.51.100.9"}, "pipe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := mustProxies(t, tc.proxies...)
			r := httptest.NewRequest("GET", "/v1/machines", nil)
			r.RemoteAddr = tc.remote
			for _, h := range tc.xff {
				r.Header.Add("X-Forwarded-For", h)
			}
			if got := m.ClientIP(r); got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// proxied sends a request that reached the server from peer with the given
// X-Forwarded-For, as a reverse proxy would deliver it.
func proxied(h http.Handler, peer, xff, path, authz string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, strings.NewReader(""))
	r.RemoteAddr = peer + ":4321"
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	if authz != "" {
		r.Header.Set("Authorization", authz)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr
}

func proxyEnv(t *testing.T, specs ...string) (h http.Handler, readTok string) {
	t.Helper()
	s := openStore(t)
	_, tok, err := s.CreateToken(context.Background(), store.DefaultAccountID, "rd", store.ScopeRead, "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := auth.ParseTrustedProxies(specs)
	if err != nil {
		t.Fatal(err)
	}
	return auth.New(s, auth.WithTrustedProxies(p)).Wrap(api.Handler(s)), tok
}

func TestForwardedClientsDoNotShareALimit(t *testing.T) {
	h, tok := proxyEnv(t, "10.0.0.0/8")
	const proxy = "10.0.0.2"
	for i := 0; i < auth.MaxFailures; i++ {
		if rr := proxied(h, proxy, "198.51.100.9", "/v1/machines", bearer("fk_bad")); rr.Code != 401 {
			t.Fatalf("attacker attempt %d: %d", i, rr.Code)
		}
	}
	if rr := proxied(h, proxy, "198.51.100.9", "/v1/machines", bearer("fk_bad")); rr.Code != 429 {
		t.Fatalf("attacker after limit: %d", rr.Code)
	}
	if rr := proxied(h, proxy, "198.51.100.9", "/v1/machines", bearer(tok)); rr.Code != 429 {
		t.Fatalf("attacker with a valid token while limited: %d", rr.Code)
	}
	// Another visitor through the same proxy is unaffected.
	if rr := proxied(h, proxy, "203.0.113.4", "/v1/machines", bearer(tok)); rr.Code != 200 {
		t.Fatalf("other client behind the proxy: %d", rr.Code)
	}
	// So is an unauthenticated one, and it is not counted either.
	if rr := proxied(h, proxy, "203.0.113.5", "/v1/machines", ""); rr.Code != 401 {
		t.Fatalf("other client without credentials: %d", rr.Code)
	}
	// The attacker cannot dodge the limit by prepending addresses.
	if rr := proxied(h, proxy, "203.0.113.4, 198.51.100.9", "/v1/machines", bearer("fk_bad")); rr.Code != 429 {
		t.Fatalf("attacker forging a prefix: %d", rr.Code)
	}
}

func TestUntrustedPeerCannotRotateForwardedAddress(t *testing.T) {
	h, tok := proxyEnv(t, "10.0.0.0/8")
	for i := 0; i < auth.MaxFailures; i++ {
		// A different spoofed address each time, from an untrusted peer.
		proxied(h, "192.0.2.7", fmt.Sprintf("198.51.100.%d", i+1), "/v1/machines", bearer("fk_bad"))
	}
	if rr := proxied(h, "192.0.2.7", "203.0.113.77", "/v1/machines", bearer(tok)); rr.Code != 429 {
		t.Fatalf("spoofing from an untrusted peer evaded the limit: %d", rr.Code)
	}
}

func TestDefaultIgnoresForwardedFor(t *testing.T) {
	h, tok := proxyEnv(t)
	for i := 0; i < auth.MaxFailures; i++ {
		proxied(h, "192.0.2.7", "198.51.100.9", "/v1/machines", bearer("fk_bad"))
	}
	// Same peer, different header: still limited, as before the flag existed.
	if rr := proxied(h, "192.0.2.7", "203.0.113.4", "/v1/machines", bearer(tok)); rr.Code != 429 {
		t.Fatalf("default config honoured X-Forwarded-For: %d", rr.Code)
	}
}

func TestMissingCredentialsAreNotCounted(t *testing.T) {
	h, tok := proxyEnv(t)
	for i := 0; i < 3*auth.MaxFailures; i++ {
		rr := proxied(h, "192.0.2.7", "", "/v1/machines", "")
		if rr.Code != 401 || rr.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("attempt %d: %d", i, rr.Code)
		}
	}
	if rr := proxied(h, "192.0.2.7", "", "/v1/machines", bearer(tok)); rr.Code != 200 {
		t.Fatalf("valid token after credential-less requests: %d", rr.Code)
	}
}

func TestStaleCookieIsNotCounted(t *testing.T) {
	h, tok := proxyEnv(t)
	for i := 0; i < 3*auth.MaxFailures; i++ {
		r := httptest.NewRequest("GET", "/v1/machines", nil)
		r.RemoteAddr = "192.0.2.7:1"
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "stale"})
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		if rr.Code != 401 {
			t.Fatalf("attempt %d: %d", i, rr.Code)
		}
	}
	if rr := proxied(h, "192.0.2.7", "", "/v1/machines", bearer(tok)); rr.Code != 200 {
		t.Fatalf("valid token after stale cookies: %d", rr.Code)
	}
}

func TestPresentedBadCredentialsStillCount(t *testing.T) {
	for _, tc := range []struct{ name, header string }{
		{"unknown token", bearer("fk_bad")},
		{"malformed header", "Basic abc"},
		{"bearer without token", "Bearer "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := proxyEnv(t)
			for i := 0; i < auth.MaxFailures; i++ {
				if rr := proxied(h, "192.0.2.7", "", "/v1/machines", tc.header); rr.Code != 401 {
					t.Fatalf("attempt %d: %d", i, rr.Code)
				}
			}
			if rr := proxied(h, "192.0.2.7", "", "/v1/machines", tc.header); rr.Code != 429 {
				t.Fatalf("after limit: %d", rr.Code)
			}
		})
	}
}
