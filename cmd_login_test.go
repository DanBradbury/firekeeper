package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/config"
	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/server"
	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// syncBuffer is a bytes.Buffer safe to read while a command is writing.
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

const (
	e2ePassword = "correct horse battery"
	e2eIngest   = `{"machine":{"id":"%s","name":"x"},"sessions":[{"meta":{"session_id":"s1","provider":"codex","project":"p","state":"ACTIVE"},"events":[{"machine_id":"%s","session_id":"s1","provider":"codex","seq":0,"role":"user","text":"hi","raw":{}}]}]}`
)

// linkEnv is a dashboard server with two accounts and a private home for
// the CLI under test.
type linkEnv struct {
	t       *testing.T
	base    string
	out     *syncBuffer // everything the server printed
	home    string
	cfgPath string
}

func newLinkEnv(t *testing.T) *linkEnv {
	t.Helper()
	hash, err := auth.HashPassword(e2ePassword)
	if err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(t.TempDir(), "d", "dashboard.db")
	os.MkdirAll(filepath.Dir(db), 0o700)
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"alice@example.com", "bob@example.com"} {
		if _, err := s.CreateAccount(context.Background(), email, hash, "", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	e := &linkEnv{t: t, out: &syncBuffer{}, home: t.TempDir()}
	e.cfgPath = config.DefaultPath(e.home)
	prevHome, prevInterval := testConfigHome, linkPollInterval
	testConfigHome, linkPollInterval = e.home, 20*time.Millisecond
	t.Cleanup(func() { testConfigHome, linkPollInterval = prevHome, prevInterval })

	urls := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Run(ctx, server.Config{Listen: "127.0.0.1:0", DB: db, Out: e.out, Err: e.out,
			OnListen: func(u string) { urls <- u }})
	}()
	select {
	case u := <-urls:
		e.base = strings.TrimSuffix(u, "/")
	case err := <-done:
		t.Fatalf("server exited: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server did not start")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(server.ShutdownGrace + 5*time.Second):
			t.Error("server did not stop")
		}
	})
	return e
}

// browser is a signed-in browser session.
type browser struct {
	t    *testing.T
	base string
	c    *http.Client
	csrf string
}

func (e *linkEnv) signIn(email string) *browser {
	e.t.Helper()
	jar, _ := cookiejar.New(nil)
	b := &browser{t: e.t, base: e.base, c: &http.Client{Jar: jar}}
	status, body := b.do("POST", "/v1/auth/login", map[string]string{"email": email, "password": e2ePassword})
	if status != http.StatusOK {
		e.t.Fatalf("login %s: %d %s", email, status, body)
	}
	var r struct {
		CSRFToken string `json:"csrf_token"`
	}
	json.Unmarshal(body, &r)
	b.csrf = r.CSRFToken
	return b
}

func (b *browser) do(method, path string, body any) (int, []byte) {
	b.t.Helper()
	var rd io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, b.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.csrf != "" {
		req.Header.Set(auth.CSRFHeader, b.csrf)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// bearer sends a request with a token, like a reporter would.
func (e *linkEnv) bearer(method, path, token, body string) (int, string) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

var userCodeRE = regexp.MustCompile(`code=([A-Z0-9]{4}-[A-Z0-9]{4})`)

// login runs `firekeeper login` and, once it prints its code, approves it
// as approver the way the /link page does.
func (e *linkEnv) login(approver *browser, machineName string, args ...string) (code int, stdout, stderr string) {
	e.t.Helper()
	var so, se syncBuffer
	exit := make(chan int, 1)
	go func() {
		exit <- runLogin(append([]string{"--server", e.base, "--name", machineName}, args...), &so, &se)
	}()
	if approver != nil {
		deadline := time.Now().Add(10 * time.Second)
		var m []string
		for m == nil {
			if m = userCodeRE.FindStringSubmatch(so.String()); m == nil {
				if time.Now().After(deadline) {
					e.t.Fatalf("login printed no code: %q %q", so.String(), se.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		if status, body := approver.do("POST", "/v1/link/approve", map[string]string{"user_code": m[1], "machine_name": machineName}); status != http.StatusOK {
			e.t.Fatalf("approve: %d %s", status, body)
		}
	}
	select {
	case code = <-exit:
	case <-time.After(15 * time.Second):
		e.t.Fatal("login did not finish")
	}
	return code, so.String(), se.String()
}

func (e *linkEnv) storedConfig() config.Config {
	e.t.Helper()
	c, err := config.Load(config.Options{Home: e.home, Getenv: func(string) string { return "" }})
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func TestLoginDeviceFlowEndToEnd(t *testing.T) {
	e := newLinkEnv(t)
	alice := e.signIn("alice@example.com")
	bob := e.signIn("bob@example.com")

	// Existing settings survive, and linking enables no provider.
	if err := os.MkdirAll(filepath.Dir(e.cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(e.cfgPath, []byte("# mine\nexclude = [\"~/clients\"]\n"), 0o644)

	code, stdout, stderr := e.login(alice, "Alice's laptop")
	if code != 0 {
		t.Fatalf("login exit %d: %s %s", code, stdout, stderr)
	}
	var token string
	{
		c := e.storedConfig()
		token = c.Token
		if c.Server != e.base || !strings.HasPrefix(token, "fk_") {
			t.Fatalf("stored server=%q token=%q", c.Server, config.MaskToken(c.Token))
		}
		if len(c.Providers) != 0 {
			t.Fatalf("login enabled providers: %v", c.Providers)
		}
		if !slices.Contains(c.Exclude, filepath.Join(e.home, "clients")) {
			t.Fatalf("login dropped existing settings: %+v", c.Exclude)
		}
	}
	raw, _ := os.ReadFile(e.cfgPath)
	if !strings.Contains(string(raw), "# mine") {
		t.Fatalf("login rewrote the file: %s", raw)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(e.cfgPath); info.Mode().Perm() != 0o600 {
			t.Fatalf("config mode %o, want 600", info.Mode().Perm())
		}
	}
	if !strings.Contains(stdout, "nothing is uploaded yet") {
		t.Errorf("login does not say uploading is still opt-in: %s", stdout)
	}

	// A working ingest from the stored token, for this machine only.
	machineID, _ := os.ReadFile(filepath.Join(e.home, ".firekeeper", "machine-id"))
	id := strings.TrimSpace(string(machineID))
	if status, body := e.bearer("POST", "/v1/ingest", token, strings.ReplaceAll(e2eIngest, "%s", id)); status != http.StatusOK || !strings.Contains(body, `"accepted":1`) {
		t.Fatalf("ingest with stored token: %d %s", status, body)
	}
	if status, _ := e.bearer("POST", "/v1/ingest", token, strings.ReplaceAll(e2eIngest, "%s", "another-machine")); status != http.StatusForbidden {
		t.Fatalf("token ingested for another machine: %d", status)
	}
	// Alice sees it; Bob does not.
	if _, body := alice.do("GET", "/v1/sessions", nil); !strings.Contains(string(body), id+":s1") {
		t.Fatalf("alice does not see the session: %s", body)
	}
	if _, body := bob.do("GET", "/v1/sessions", nil); strings.Contains(string(body), id) {
		t.Fatalf("bob sees alice's session: %s", body)
	}

	// whoami: server, account email, machine name, and never the token.
	var wo, we bytes.Buffer
	if code := runWhoami(nil, &wo, &we); code != 0 {
		t.Fatalf("whoami exit %d: %s", code, we.String())
	}
	for _, want := range []string{e.base, "alice@example.com", "Alice's laptop"} {
		if !strings.Contains(wo.String(), want) {
			t.Errorf("whoami lacks %q:\n%s", want, wo.String())
		}
	}

	// The Tokens page sees it, with a last-used time.
	_, body := alice.do("GET", "/v1/tokens", nil)
	var list struct {
		Tokens []struct {
			ID         string  `json:"id"`
			Name       string  `json:"name"`
			LastUsedAt *string `json:"last_used_at"`
		} `json:"tokens"`
	}
	json.Unmarshal(body, &list)
	if len(list.Tokens) != 1 || list.Tokens[0].Name != "Alice's laptop" || list.Tokens[0].LastUsedAt == nil {
		t.Fatalf("tokens = %s", body)
	}

	// report and daemon use the stored server and token with no flags, and
	// still upload nothing without a provider.
	rcfg := stubReport(t, reporter.Summary{}, nil)
	var ro, re bytes.Buffer
	if code := runReport(nil, &ro, &re); code != 0 || rcfg.Server != e.base || rcfg.Token != token || len(rcfg.Providers) != 0 || rcfg.Uploading() {
		t.Fatalf("report: code=%d server=%q providers=%v uploading=%v", code, rcfg.Server, rcfg.Providers, rcfg.Uploading())
	}
	if code := runReport([]string{"--provider", "codex"}, &ro, &re); code != 0 || rcfg.Server != e.base || rcfg.Token != token || !slices.Equal(rcfg.Providers, []transcript.Provider{"codex"}) {
		t.Fatalf("report --provider codex: code=%d cfg=%+v", code, rcfg.Server)
	}
	dcfg := stubDaemon(t, nil)
	if code := runDaemon([]string{"--provider", "codex", "--quiet"}, &ro, &re); code != 0 || dcfg.Reporter.Server != e.base || dcfg.Reporter.Token != token {
		t.Fatalf("daemon: code=%d server=%q", code, dcfg.Reporter.Server)
	}
	svcArgs, _, _ := stubService(t, "linux")
	if code := runDaemon([]string{"install", "--provider", "codex"}, &ro, &re); code != 0 {
		t.Fatalf("daemon install: %d %s", code, re.String())
	}
	for _, a := range *svcArgs {
		if strings.Contains(a, token) || a == "--server" {
			t.Fatalf("daemon install copied credentials into the service definition: %v", *svcArgs)
		}
	}

	// logout revokes on the server and removes the token locally.
	var lo, le bytes.Buffer
	if code := runLogout(nil, &lo, &le); code != 0 {
		t.Fatalf("logout exit %d: %s", code, le.String())
	}
	if status, _ := e.bearer("POST", "/v1/ingest", token, strings.ReplaceAll(e2eIngest, "%s", id)); status != http.StatusUnauthorized {
		t.Fatalf("revoked token still ingests: %d", status)
	}
	if c := e.storedConfig(); c.Token != "" || c.Server != e.base {
		t.Fatalf("after logout server=%q token=%q", c.Server, config.MaskToken(c.Token))
	}
	if raw, _ := os.ReadFile(e.cfgPath); !strings.Contains(string(raw), "# mine") || strings.Contains(string(raw), token) {
		t.Fatalf("config after logout:\n%s", raw)
	}
	if _, body := alice.do("GET", "/v1/tokens", nil); !strings.Contains(string(body), `"revoked_at":"`) {
		t.Fatalf("token not shown as revoked: %s", body)
	}
	var wo2, we2 bytes.Buffer
	if code := runWhoami(nil, &wo2, &we2); code != 1 || !strings.Contains(we2.String(), "not logged in") {
		t.Fatalf("whoami after logout: %d %q", code, we2.String())
	}
	lo.Reset()
	if code := runLogout(nil, &lo, &le); code != 0 || !strings.Contains(lo.String(), "Not logged in") {
		t.Fatalf("second logout: %d %q", code, lo.String())
	}

	// The token appears in no output of any command or of the server.
	for name, text := range map[string]string{
		"login stdout": stdout, "login stderr": stderr, "whoami": wo.String() + we.String(), "report": ro.String() + re.String(),
		"logout": lo.String() + le.String(), "after logout": wo2.String() + we2.String(), "server": e.out.String(),
	} {
		if strings.Contains(text, token) || strings.Contains(text, strings.TrimPrefix(token, "fk_")) {
			t.Errorf("%s output contains the token", name)
		}
	}
}

func TestLoginWithRevokedTokenAndOtherFailures(t *testing.T) {
	e := newLinkEnv(t)
	alice := e.signIn("alice@example.com")
	if code, _, stderr := e.login(alice, "box"); code != 0 {
		t.Fatalf("login: %d %s", code, stderr)
	}
	token := e.storedConfig().Token

	// Revoked from the Tokens page: whoami says so, without the token, and
	// logout still cleans up locally.
	_, body := alice.do("GET", "/v1/tokens", nil)
	var list struct{ Tokens []struct{ ID string } }
	json.Unmarshal(body, &list)
	if status, b := alice.do("DELETE", "/v1/tokens/"+list.Tokens[0].ID, nil); status != http.StatusOK {
		t.Fatalf("revoke: %d %s", status, b)
	}
	var wo, we bytes.Buffer
	if code := runWhoami(nil, &wo, &we); code != 1 || !strings.Contains(we.String(), "revoked") || strings.Contains(we.String(), token) {
		t.Fatalf("whoami with a revoked token: %d %q", code, we.String())
	}
	var lo, le bytes.Buffer
	if code := runLogout(nil, &lo, &le); code != 0 || e.storedConfig().Token != "" {
		t.Fatalf("logout with a revoked token: %d %q token=%q", code, le.String(), config.MaskToken(e.storedConfig().Token))
	}

	t.Run("plain http to a remote server", func(t *testing.T) {
		var so, se bytes.Buffer
		if code := runLogin([]string{"--server", "http://firekeeper.example.com"}, &so, &se); code != 1 || !strings.Contains(se.String(), "https") {
			t.Fatalf("login over http: %d %q", code, se.String())
		}
	})
	t.Run("server without accounts", func(t *testing.T) {
		db := filepath.Join(t.TempDir(), "d", "dashboard.db")
		urls := make(chan string, 1)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- server.Run(ctx, server.Config{Listen: "127.0.0.1:0", DB: db, OnListen: func(u string) { urls <- u }})
		}()
		base := strings.TrimSuffix(<-urls, "/")
		defer func() { cancel(); <-done }()
		var so, se bytes.Buffer
		if code := runLogin([]string{"--server", base}, &so, &se); code != 1 || !strings.Contains(se.String(), "no accounts") {
			t.Fatalf("login to an accountless server: %d %q", code, se.String())
		}
	})
}

// A login that fails must not create or change the config file.
func TestFailedLoginLeavesConfigUntouched(t *testing.T) {
	e := newLinkEnv(t)
	done := make(chan int, 1)
	var so, se syncBuffer
	go func() {
		// Nothing listens on port 1, so the first call fails.
		done <- runLogin([]string{"--server", "http://127.0.0.1:1"}, &so, &se)
	}()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("login hung")
	}
	if _, err := os.Stat(e.cfgPath); err == nil {
		t.Fatal("a failed login created a config file")
	}
}
