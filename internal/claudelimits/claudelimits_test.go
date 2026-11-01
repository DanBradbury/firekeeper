package claudelimits

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const fakeToken = "sk-ant-oat01-synthetic-test-token"

var testNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func writeCreds(t *testing.T, dir string, expires time.Time) {
	t.Helper()
	body := `{"claudeAiOauth":{"accessToken":"` + fakeToken + `","expiresAt":` +
		strconv.FormatInt(expires.UnixMilli(), 10) + `,"subscriptionType":"max"}}`
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func clientFor(dir, url string) Client {
	return Client{
		URL:    url,
		Home:   dir,
		GOOS:   "linux",
		Now:    func() time.Time { return testNow },
		Getenv: func(key string) string { return map[string]string{"CLAUDE_CONFIG_DIR": dir}[key] },
	}
}

func TestFetchReadsFileTokenAndDecodesWindows(t *testing.T) {
	dir := t.TempDir()
	writeCreds(t, dir, testNow.Add(time.Hour))
	var gotAuth, gotBeta string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotBeta = r.Header.Get("Authorization"), r.Header.Get("anthropic-beta")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":37.5,"resets_at":"2026-10-31T15:00:00Z"},
			"seven_day":{"utilization":120,"resets_at":null},"seven_day_opus":null,"extra":1}`))
	}))
	defer server.Close()

	limits, err := clientFor(dir, server.URL).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+fakeToken || gotBeta == "" {
		t.Fatalf("headers auth=%q beta=%q", gotAuth, gotBeta)
	}
	if limits.FiveHour == nil || limits.FiveHour.UsedPercent != 37.5 || !limits.FiveHour.ResetsAt.Equal(time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("five hour = %+v", limits.FiveHour)
	}
	if limits.SevenDay == nil || limits.SevenDay.UsedPercent != 100 || !limits.SevenDay.ResetsAt.IsZero() {
		t.Fatalf("seven day = %+v (percent must clamp, null reset is zero)", limits.SevenDay)
	}
	if limits.SevenDayOpus != nil || limits.SubscriptionTag != "max" {
		t.Fatalf("opus = %+v tag = %q", limits.SevenDayOpus, limits.SubscriptionTag)
	}
}

func TestFetchTokenSourcesAndPrecedence(t *testing.T) {
	dir := t.TempDir()
	writeCreds(t, dir, testNow.Add(time.Hour))
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":1}}`))
	}))
	defer server.Close()

	c := clientFor(dir, server.URL)
	c.GOOS = "darwin"
	c.Keychain = func() ([]byte, error) {
		return []byte(`{"claudeAiOauth":{"accessToken":"from-keychain"}}`), nil
	}
	if _, err := c.Fetch(context.Background()); err != nil || gotAuth != "Bearer from-keychain" {
		t.Fatalf("keychain: err=%v auth=%q", err, gotAuth)
	}
	c.Keychain = func() ([]byte, error) { return nil, errors.New("not found") }
	if _, err := c.Fetch(context.Background()); err != nil || gotAuth != "Bearer "+fakeToken {
		t.Fatalf("file fallback: err=%v auth=%q", err, gotAuth)
	}
	base := c.Getenv
	c.Getenv = func(key string) string {
		if key == "CLAUDE_CODE_OAUTH_TOKEN" {
			return "from-env"
		}
		return base(key)
	}
	if _, err := c.Fetch(context.Background()); err != nil || gotAuth != "Bearer from-env" {
		t.Fatalf("env: err=%v auth=%q", err, gotAuth)
	}
}

func TestFetchNeverReadsKeychainOffMacOS(t *testing.T) {
	dir := t.TempDir()
	c := clientFor(dir, "http://127.0.0.1:1")
	c.Keychain = func() ([]byte, error) {
		t.Fatal("keychain read on linux")
		return nil, nil
	}
	if _, err := c.Fetch(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
	if c.HasToken() {
		t.Fatal("HasToken with no credentials")
	}
}

func TestFetchErrorsAreClassifiedAndNeverLeakTheToken(t *testing.T) {
	dir := t.TempDir()
	writeCreds(t, dir, testNow.Add(time.Hour))
	statuses := map[int]error{http.StatusUnauthorized: ErrUnauthorized, http.StatusForbidden: ErrUnauthorized}
	for status, want := range statuses {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		_, err := clientFor(dir, server.URL).Fetch(context.Background())
		server.Close()
		if !errors.Is(err, want) {
			t.Fatalf("status %d: err = %v", status, err)
		}
	}
	for name, body := range map[string]string{"garbage": "not json", "empty": "{}"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		_, err := clientFor(dir, server.URL).Fetch(context.Background())
		server.Close()
		if err == nil || strings.Contains(err.Error(), fakeToken) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	_, err := clientFor(dir, server.URL).Fetch(context.Background())
	server.Close()
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || strings.Contains(err.Error(), fakeToken) {
		t.Fatalf("500: err = %v", err)
	}
	if _, err := clientFor(dir, server.URL).Fetch(context.Background()); err == nil || strings.Contains(err.Error(), fakeToken) {
		t.Fatalf("closed server: err = %v", err)
	}
}

func TestFetchExpiredTokenMakesNoRequest(t *testing.T) {
	dir := t.TempDir()
	writeCreds(t, dir, testNow.Add(-time.Minute))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request made with an expired token")
	}))
	defer server.Close()
	if _, err := clientFor(dir, server.URL).Fetch(context.Background()); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("err = %v, want ErrTokenExpired", err)
	}
}

func TestMalformedCredentialFileIsNoToken(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := clientFor(dir, "http://127.0.0.1:1").Fetch(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	for in, want := range map[string]time.Duration{"120": 120 * time.Second, " 7 ": 7 * time.Second, "": 0, "0": 0, "-3": 0, "Wed, 21 Oct 2026 07:28:00 GMT": 0} {
		if got := parseRetryAfter(in); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
