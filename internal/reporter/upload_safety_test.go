package reporter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// recordingTransport answers every request with one fixed reply and records
// where each request went. No request leaves the process.
type recordingTransport struct {
	mu     sync.Mutex
	urls   []string
	status int
	body   string
}

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.urls = append(rt.urls, r.URL.Scheme+"://"+r.URL.Host+r.URL.Path)
	rt.mu.Unlock()
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
	return &http.Response{
		StatusCode: rt.status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(rt.body)),
		Request:    r,
	}, nil
}

func (rt *recordingTransport) sent() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.urls...)
}

// Uploads carry the token and whole transcripts, so they follow login's rule:
// plain http only to this machine.
func TestUploadRequiresHTTPSForRemoteServers(t *testing.T) {
	for _, tt := range []struct {
		server string
		ok     bool
	}{
		{"https://dash.example.com", true},
		{"https://dash.example.com:8443/firekeeper/", true},
		{"http://127.0.0.1:7777", true},
		{"http://localhost:7777", true},
		{"http://[::1]:7777", true},
		{"http://dash.example.com", false},
		{"http://10.0.0.5:7777", false},
		{"https://user:pw@dash.example.com", false},
		{"https://dash.example.com?token=x", false},
		{"ftp://dash.example.com", false},
		{"dash.example.com", false},
	} {
		t.Run(tt.server, func(t *testing.T) {
			f := newFixture(t)
			f.append(t, 0, 1)
			rt := &recordingTransport{status: http.StatusOK, body: `{"accepted":1,"duplicates":0}`}
			cfg := f.config(tt.server)
			cfg.Token = "fk_test"
			cfg.Client = &http.Client{Transport: rt}

			summary, err := RunOnce(context.Background(), cfg)
			hbErr := Heartbeat(context.Background(), cfg, Summary{MachineID: testMachine})
			if !tt.ok {
				if err == nil || hbErr == nil {
					t.Fatalf("accepted %q: upload err %v, heartbeat err %v", tt.server, err, hbErr)
				}
				if sent := rt.sent(); len(sent) != 0 {
					t.Fatalf("sent %d request(s) to a refused server: %v", len(sent), sent)
				}
				if pos := f.state(t); pos != (FileState{}) {
					t.Fatalf("offset saved for a refused server: %+v", pos)
				}
				return
			}
			if err != nil || hbErr != nil {
				t.Fatalf("refused %q: upload err %v, heartbeat err %v", tt.server, err, hbErr)
			}
			if got := summary.Sessions[0]; got.Err != nil || got.Events != 1 {
				t.Fatalf("result = %+v", got)
			}
			want := strings.TrimRight(tt.server, "/")
			for _, u := range rt.sent() {
				if !strings.HasPrefix(u, want+"/v1/") {
					t.Fatalf("request went to %s, want under %s", u, want)
				}
			}
		})
	}
}

// A redirect is never followed: Go re-sends the body of a 307 or 308 to any
// host, and keeps the Authorization header when only the scheme or port
// changes.
func TestUploadDoesNotFollowRedirects(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, ownClient := range []bool{false, true} {
			name := fmt.Sprintf("%d/default client", status)
			if ownClient {
				name = fmt.Sprintf("%d/caller client", status)
			}
			t.Run(name, func(t *testing.T) {
				var elsewhere atomic.Int32
				other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					elsewhere.Add(1)
					t.Errorf("redirect followed: %s %s reached another server (Authorization set: %v)", r.Method, r.URL.Path, r.Header.Get("Authorization") != "")
				}))
				defer other.Close()
				dashboard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, other.URL+r.URL.Path, status)
				}))
				defer dashboard.Close()

				f := newFixture(t)
				f.append(t, 0, 2)
				cfg := f.config(dashboard.URL)
				cfg.Token = "fk_test"
				if ownClient {
					// A caller's client is used, but never with its own
					// redirect policy.
					cfg.Client = &http.Client{}
				}

				summary, err := RunOnce(context.Background(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				got := summary.Sessions[0].Err
				var se *StatusError
				if !errors.As(got, &se) || !se.Redirect() || se.Status != status {
					t.Fatalf("upload error = %v, want a %d redirect StatusError", got, status)
				}
				if !strings.Contains(got.Error(), "redirect") || !strings.Contains(got.Error(), "final URL") {
					t.Fatalf("upload error %q does not say what to do", got)
				}
				if retryable(got) {
					t.Fatal("a redirect is retried")
				}
				if pos := f.state(t); pos != (FileState{}) {
					t.Fatalf("offset advanced after a redirect: %+v", pos)
				}

				err = Heartbeat(context.Background(), cfg, Summary{MachineID: testMachine, Sessions: summary.Sessions})
				if !errors.As(err, &se) || !se.Redirect() {
					t.Fatalf("heartbeat error = %v, want a redirect StatusError", err)
				}
				if n := elsewhere.Load(); n != 0 {
					t.Fatalf("%d request(s) reached the redirect target", n)
				}
			})
		}
	}
}

func TestUploadClientKeepsCallerSettings(t *testing.T) {
	rt := &recordingTransport{status: http.StatusOK}
	base := &http.Client{Transport: rt, Timeout: 7, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	got := uploadClient(base)
	if got == base {
		t.Fatal("uploadClient changed the caller's client instead of a copy")
	}
	if got.Transport != rt || got.Timeout != 7 {
		t.Fatalf("copy lost the caller's settings: %+v", got)
	}
	if base.CheckRedirect == nil || base.CheckRedirect(nil, nil) != nil {
		t.Fatal("caller's redirect policy was modified")
	}
	if got.CheckRedirect == nil || !errors.Is(got.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Fatal("copy follows redirects")
	}
	if def := uploadClient(nil); def.Timeout == 0 || def.CheckRedirect == nil {
		t.Fatalf("default client = %+v", def)
	}
}
