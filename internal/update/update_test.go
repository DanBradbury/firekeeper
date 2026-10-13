package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		want            bool
	}{
		{"0.3.0", "0.2.1", true},
		{"v0.3.0", "0.2.1", true},
		{"0.2.1", "0.2.1", false},
		{"0.2.0", "0.2.1", false},
		{"0.10.0", "0.9.9", true},
		{"0.2.1", "0.2.1-rc1", true},
		{"0.2.1-rc1", "0.2.1", false},
		{"garbage", "0.2.1", false},
		{"0.3.0", "dev", false},
	}
	for _, tt := range tests {
		if got := Newer(tt.latest, tt.current); got != tt.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}

func release(t *testing.T, body string, hits *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAvailableCachesAndSkips(t *testing.T) {
	var hits int32
	srv := release(t, `{"tag_name":"v0.3.0","prerelease":false}`, &hits)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	c := &Checker{Current: "0.2.1", Dir: t.TempDir(), URL: srv.URL, Now: func() time.Time { return now }}

	if v, ok := c.Available(context.Background()); !ok || v != "0.3.0" {
		t.Fatalf("Available = %q, %v; want 0.3.0, true", v, ok)
	}
	if _, ok := c.Available(context.Background()); !ok || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("second call should use the cache; hits=%d", hits)
	}
	c.Skip("0.3.0")
	if _, ok := c.Available(context.Background()); ok {
		t.Fatal("skipped version offered again")
	}
	now = now.Add(25 * time.Hour)
	if _, ok := c.Available(context.Background()); ok || atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("stale cache should refresh once and stay skipped; hits=%d", hits)
	}
}

func TestAvailableIgnores(t *testing.T) {
	var hits int32
	tests := map[string]struct {
		current, body string
		env           bool
	}{
		"dev build":  {current: "dev", body: `{"tag_name":"v9.0.0"}`},
		"disabled":   {current: "0.2.1", body: `{"tag_name":"v9.0.0"}`, env: true},
		"prerelease": {current: "0.2.1", body: `{"tag_name":"v9.0.0","prerelease":true}`},
		"bad json":   {current: "0.2.1", body: `nope`},
		"same":       {current: "0.3.0", body: `{"tag_name":"v0.3.0"}`},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if tt.env {
				t.Setenv(EnvDisable, "1")
			}
			srv := release(t, tt.body, &hits)
			c := &Checker{Current: tt.current, Dir: t.TempDir(), URL: srv.URL}
			if v, ok := c.Available(context.Background()); ok {
				t.Fatalf("offered %q", v)
			}
		})
	}
}

func TestAvailableOffline(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := &Checker{Current: "0.2.1", Dir: t.TempDir(), URL: url}
	if _, ok := c.Available(context.Background()); ok {
		t.Fatal("offered an update while offline")
	}
}

func TestPromptChoices(t *testing.T) {
	for _, tt := range []struct {
		input   string
		skipped bool
	}{{"\n", false}, {"2\n", false}, {"3\n", true}, {"", false}} {
		c := &Checker{Current: "0.2.1", Dir: t.TempDir()}
		var out, errOut strings.Builder
		if c.Prompt(context.Background(), "0.3.0", strings.NewReader(tt.input), &out, &errOut) {
			t.Fatalf("input %q ran an update", tt.input)
		}
		if got := c.load().Skipped == "0.3.0"; got != tt.skipped {
			t.Errorf("input %q: skipped = %v, want %v", tt.input, got, tt.skipped)
		}
		if !strings.Contains(out.String(), "0.2.1 -> 0.3.0") {
			t.Errorf("prompt missing versions: %q", out.String())
		}
	}
}

func TestInstallRejectsBadVersion(t *testing.T) {
	if err := Install(context.Background(), "1.0; rm -rf", "/x/firekeeper", nil, nil); err == nil {
		t.Fatal("accepted a malformed version")
	}
}

func TestIsHomebrew(t *testing.T) {
	if !IsHomebrew("/opt/homebrew/Cellar/firekeeper/0.2.1/bin/firekeeper") {
		t.Error("Cellar path not detected")
	}
	if IsHomebrew("/home/u/.local/bin/firekeeper") {
		t.Error("plain path detected as Homebrew")
	}
}
