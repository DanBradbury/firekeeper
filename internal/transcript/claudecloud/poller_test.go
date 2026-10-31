package claudecloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/testenv/fakeapi"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// pollHarness wires a Poller to the fake cloud API, the fake dashboard, the
// real reporter, and a hand-moved clock. Nothing reaches the network.
type pollHarness struct {
	t     *testing.T
	cloud *fakeCloud
	api   *fakeapi.Server
	home  string
	now   time.Time
	logs  []string
	creds func(context.Context) (Credentials, error)
	p     *Poller
}

func newPollHarness(t *testing.T, mutate func(*PollerConfig)) *pollHarness {
	t.Helper()
	f, c := newFakeCloud(t)
	h := &pollHarness{t: t, cloud: f, api: fakeapi.New(), home: t.TempDir(),
		now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	h.creds = func(context.Context) (Credentials, error) {
		return Credentials{Token: testToken, OrgUUID: testOrg}, nil
	}
	if err := os.MkdirAll(filepath.Join(h.home, ".firekeeper"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.home, ".firekeeper", "machine-id"), []byte(testMachine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dashboard := httptest.NewServer(h.api.Handler())
	t.Cleanup(dashboard.Close)
	cfg := PollerConfig{
		Reporter: reporter.Config{
			Server:    dashboard.URL,
			Providers: []transcript.Provider{transcript.ProviderCodex, transcript.ProviderClaude},
			Home:      h.home,
			// The daemon's own discovery must not leak into cloud passes.
			Discover: func(context.Context, session.Options) ([]session.Meta, error) {
				t.Fatal("the cloud pass ran local discovery")
				return nil, nil
			},
		},
		BaseURL:     c.BaseURL,
		Credentials: func(ctx context.Context) (Credentials, error) { return h.creds(ctx) },
		Now:         func() time.Time { return h.now },
		Logf:        func(format string, args ...any) { h.logs = append(h.logs, fmt.Sprintf(format, args...)) },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	p, err := NewPoller(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.p = p
	return h
}

func (h *pollHarness) statusPath() string {
	return filepath.Join(h.home, ".firekeeper", StatusFile)
}

func (h *pollHarness) status() Status {
	h.t.Helper()
	s, ok := ReadStatus(h.statusPath())
	if !ok {
		h.t.Fatal("no status file")
	}
	return s
}

func TestPollerImportsIncrementallyWithoutDuplicates(t *testing.T) {
	h := newPollHarness(t, nil)
	h.cloud.sessions = []map[string]any{cloudSession(sid, "2026-10-01T11:00:00Z")}
	h.cloud.events[sid] = conversation(sid)

	first := h.p.Poll(context.Background())
	if !first.Ran || first.Err != nil || first.Events != 5 || first.Sync.Fetched != 1 {
		t.Fatalf("first pass = %+v", first)
	}
	if got := h.api.Events(testMachine, sid); len(got) != 5 {
		t.Fatalf("server holds %d events, want 5", len(got))
	}
	if stored, ok := h.api.Session(testMachine, sid); !ok || stored.Origin != "cloud" || stored.CWD != "/claude-cloud/octo/demo" {
		t.Fatalf("stored = %+v", stored)
	}
	if !first.Next.Equal(h.now.Add(DefaultPollInterval)) {
		t.Fatalf("next = %v, want one interval later", first.Next)
	}

	// Not due yet: nothing is asked of the API.
	h.now = h.now.Add(time.Minute)
	listCalls := h.cloud.sessionCalls
	if again := h.p.Poll(context.Background()); again.Ran || h.cloud.sessionCalls != listCalls {
		t.Fatalf("a pass ran before it was due: %+v", again)
	}

	// Due, but nothing changed: the list is read, no events are downloaded
	// and nothing is uploaded.
	h.now = h.now.Add(DefaultPollInterval)
	second := h.p.Poll(context.Background())
	if !second.Ran || second.Err != nil || second.Events != 0 || second.Sync.Current != 1 || h.cloud.eventCalls[sid] != 4 {
		t.Fatalf("second pass = %+v, event calls %d", second, h.cloud.eventCalls[sid])
	}

	// The session continues in the cloud; only the new event uploads.
	h.cloud.sessions = []map[string]any{cloudSession(sid, "2026-10-01T12:30:00Z")}
	h.cloud.events[sid] = append(h.cloud.events[sid], map[string]any{"type": "assistant", "uuid": "e8", "session_id": sid,
		"created_at": "2026-10-01T12:20:00.000000Z", "timestamp": "2026-10-01T12:20:00.500Z",
		"message": map[string]any{"id": "m4", "role": "assistant", "model": "claude-sonnet-5-5", "content": []map[string]any{{"type": "text", "text": "more"}}}})
	h.now = h.now.Add(DefaultPollInterval)
	third := h.p.Poll(context.Background())
	if third.Err != nil || third.Events != 1 {
		t.Fatalf("third pass = %+v, want one new event", third)
	}
	if got := h.api.Events(testMachine, sid); len(got) != 6 {
		t.Fatalf("server holds %d events, want 6", len(got))
	}
	if s := h.status(); s.Warning != "" || !s.LastSuccess.Equal(h.now) {
		t.Fatalf("status = %+v", s)
	}
	if h.cloud.badAuth != 0 {
		t.Fatal("a request lacked the credentials")
	}
}

func TestPollerSkipsSessionsBeforeLookback(t *testing.T) {
	h := newPollHarness(t, nil)
	h.cloud.sessions = []map[string]any{cloudSession(sid, "2026-09-01T11:00:00Z")}
	h.cloud.events[sid] = conversation(sid)
	res := h.p.Poll(context.Background())
	if res.Err != nil || res.Sync.Skipped != 1 || res.Events != 0 || h.cloud.eventCalls[sid] != 0 {
		t.Fatalf("pass = %+v; sessions older than the lookback are left to backfill", res)
	}
}

func TestPollerBacksOffAndWarns(t *testing.T) {
	h := newPollHarness(t, nil)
	h.creds = func(context.Context) (Credentials, error) { return Credentials{}, ErrTokenExpired }

	want := []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 6 * time.Hour, 6 * time.Hour}
	for i, wait := range want {
		res := h.p.Poll(context.Background())
		if !res.Ran || !errors.Is(res.Err, ErrTokenExpired) {
			t.Fatalf("pass %d = %+v", i, res)
		}
		if got := res.Next.Sub(h.now); got != wait {
			t.Fatalf("pass %d waits %s, want %s", i, got, wait)
		}
		if s := h.status(); !strings.Contains(s.Warning, "run `claude` once") {
			t.Fatalf("status warning = %q", s.Warning)
		}
		// Polling again before the backoff ends does nothing.
		if again := h.p.Poll(context.Background()); again.Ran {
			t.Fatal("a pass ran during backoff")
		}
		h.now = res.Next
	}
	if h.cloud.sessionCalls != 0 {
		t.Fatal("the API was called without credentials")
	}
	full := 0
	for _, line := range h.logs {
		if strings.Contains(line, "run `claude` once") {
			full++
		}
	}
	if full != 1 {
		t.Fatalf("the same warning was logged in full %d times, want once: %q", full, h.logs)
	}

	// The user ran claude; the next pass recovers and clears the warning.
	h.creds = func(context.Context) (Credentials, error) {
		return Credentials{Token: testToken, OrgUUID: testOrg}, nil
	}
	res := h.p.Poll(context.Background())
	if res.Err != nil || res.Next.Sub(h.now) != DefaultPollInterval {
		t.Fatalf("recovery = %+v", res)
	}
	if s := h.status(); s.Warning != "" || s.LastSuccess.IsZero() {
		t.Fatalf("status after recovery = %+v", s)
	}
	if !strings.Contains(strings.Join(h.logs, "\n"), "recovered after 7 failed passes") {
		t.Fatalf("logs = %q", h.logs)
	}
}

func TestPollerSurfacesAPIRefusalAndShapeChange(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"401":          {http.StatusUnauthorized, `{"error":"secret detail"}`, "sign in again"},
		"403":          {http.StatusForbidden, `{}`, "sign in again"},
		"shape change": {http.StatusOK, `{"data": "not a list"}`, "may have changed"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			h := newPollHarness(t, func(c *PollerConfig) { c.BaseURL = srv.URL })
			res := h.p.Poll(context.Background())
			warning := h.status().Warning
			if res.Err == nil || !strings.Contains(warning, tc.want) {
				t.Fatalf("pass = %+v, warning %q", res, warning)
			}
			if strings.Contains(warning, "secret detail") || strings.Contains(warning, testToken) {
				t.Fatalf("warning leaks the response or token: %q", warning)
			}
			if res.Next.Sub(h.now) != DefaultPollInterval {
				t.Fatalf("first failure waits %s", res.Next.Sub(h.now))
			}
		})
	}
}

func TestPollerLeavesWorkForASoonerPass(t *testing.T) {
	h := newPollHarness(t, func(c *PollerConfig) { c.MaxFetch = 1 })
	other := "session_01TESTBBBBBBBBBBBBBBBBBB"
	h.cloud.sessions = []map[string]any{cloudSession(sid, "2026-10-01T11:00:00Z"), cloudSession(other, "2026-10-01T11:30:00Z")}
	h.cloud.events[sid], h.cloud.events[other] = conversation(sid), conversation(other)
	res := h.p.Poll(context.Background())
	if res.Err != nil || res.Sync.Fetched != 1 || res.Sync.Remaining != 1 || res.Next.Sub(h.now) != MoreWorkDelay {
		t.Fatalf("pass = %+v", res)
	}
	h.now = res.Next
	res = h.p.Poll(context.Background())
	if res.Err != nil || res.Sync.Fetched != 1 || res.Sync.Remaining != 0 || res.Next.Sub(h.now) != DefaultPollInterval {
		t.Fatalf("second pass = %+v", res)
	}
	if len(h.api.Events(testMachine, sid)) != 5 || len(h.api.Events(testMachine, other)) != 5 {
		t.Fatal("both sessions should be uploaded")
	}
}

func TestNewPollerChecks(t *testing.T) {
	base := reporter.Config{Home: t.TempDir(), Providers: []transcript.Provider{transcript.ProviderClaude}}
	if _, err := NewPoller(PollerConfig{Reporter: base, Interval: 5 * time.Minute}); err == nil {
		t.Fatal("an interval under the minimum was accepted")
	}
	codexOnly := base
	codexOnly.Providers = []transcript.Provider{transcript.ProviderCodex}
	if _, err := NewPoller(PollerConfig{Reporter: codexOnly}); !errors.Is(err, ErrClaudeNotAllowed) {
		t.Fatalf("err = %v; the provider allowlist must include claude", err)
	}
	p, err := NewPoller(PollerConfig{Reporter: base})
	if err != nil || p.Interval() != DefaultPollInterval {
		t.Fatalf("p = %v, err = %v", p, err)
	}
}
