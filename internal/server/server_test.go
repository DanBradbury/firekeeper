package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/DanBradbury/firekeeper/internal/server/store"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// start runs Run on an ephemeral loopback port and returns its base URL
// and a function that cancels it and waits for a clean return.
func start(t *testing.T, cfg Config) (string, func()) {
	t.Helper()
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	if cfg.DB == "" {
		cfg.DB = filepath.Join(t.TempDir(), "state", "dashboard.db")
	}
	urls := make(chan string, 1)
	cfg.OnListen = func(u string) { urls <- u }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()

	var base string
	select {
	case base = <-urls:
	case err := <-done:
		cancel()
		t.Fatalf("Run returned before listening: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("Run did not start listening")
	}
	stopped := false
	stop := func() {
		t.Helper()
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Run = %v, want clean shutdown", err)
			}
		case <-time.After(ShutdownGrace + 5*time.Second):
			t.Fatal("Run did not return after cancel")
		}
	}
	t.Cleanup(stop)
	return strings.TrimSuffix(base, "/"), stop
}

func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func TestRunServesUIAndAPI(t *testing.T) {
	var out bytes.Buffer
	dir := t.TempDir()
	db := filepath.Join(dir, "nested", "dashboard.db")
	base, stop := start(t, Config{DB: db, Out: &out})

	if !strings.Contains(out.String(), base+"/") {
		t.Fatalf("startup output %q does not name %s/", out.String(), base)
	}
	info, err := os.Stat(filepath.Dir(db))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("database directory mode = %o, want 700", perm)
	}

	resp, body := get(t, base+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("GET / CSP = %q", csp)
	}
	if !bytes.Contains(bytes.ToLower(body), []byte("<!doctype html")) {
		t.Fatal("GET / did not serve the index page")
	}

	resp, body = get(t, base+"/v1/machines")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/machines = %d: %s", resp.StatusCode, body)
	}

	ingest := `{"machine":{"id":"m1","name":"test"},"sessions":[{"meta":{"session_id":"s1","provider":"codex","project":"demo","state":"ACTIVE"},"events":[{"machine_id":"m1","session_id":"s1","provider":"codex","seq":0,"role":"user","text":"hello","raw":{}}]}]}`
	presp, err := http.Post(base+"/v1/ingest", "application/json", strings.NewReader(ingest))
	if err != nil {
		t.Fatal(err)
	}
	pbody, _ := io.ReadAll(presp.Body)
	presp.Body.Close()
	if presp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/ingest = %d: %s", presp.StatusCode, pbody)
	}

	resp, body = get(t, base+"/v1/sessions")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/sessions = %d: %s", resp.StatusCode, body)
	}
	var list struct {
		Sessions []struct {
			UID        string `json:"uid"`
			Project    string `json:"project"`
			EventCount int64  `json:"event_count"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].UID != "m1:s1" || list.Sessions[0].Project != "demo" || list.Sessions[0].EventCount != 1 {
		t.Fatalf("sessions = %+v, want m1:s1 in demo with 1 event", list.Sessions)
	}

	stop()
	if _, err := http.Get(base + "/v1/machines"); err == nil {
		t.Fatal("server still accepting after shutdown")
	}
}

func TestRunShutdownEndsStreams(t *testing.T) {
	base, stop := start(t, Config{})
	resp, err := http.Get(base + "/v1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/stream = %d", resp.StatusCode)
	}
	if line, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil || !strings.HasPrefix(line, ": connected") {
		t.Fatalf("first stream line = %q, %v", line, err)
	}

	began := time.Now()
	stop()
	// An open stream must not hold shutdown for the whole grace period.
	if elapsed := time.Since(began); elapsed >= ShutdownGrace {
		t.Fatalf("shutdown took %v with an open stream", elapsed)
	}
}

func TestRunRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "[::]:0", "192.0.2.1:0"} {
		t.Run(addr, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "d", "dashboard.db")
			err := Run(context.Background(), Config{Listen: addr, DB: db})
			if !errors.Is(err, ErrNotLoopback) {
				t.Fatalf("Run(%q) = %v, want ErrNotLoopback", addr, err)
			}
			if _, err := os.Stat(filepath.Dir(db)); !os.IsNotExist(err) {
				t.Fatal("refused Run touched the database directory")
			}
		})
	}
}

func TestRunInsecureWarns(t *testing.T) {
	var errOut bytes.Buffer
	base, _ := start(t, Config{Listen: "0.0.0.0:0", Insecure: true, Err: &errOut})
	if !strings.Contains(errOut.String(), "no authentication") {
		t.Fatalf("stderr = %q, want a no-authentication warning", errOut.String())
	}
	port := base[strings.LastIndex(base, ":"):]
	if resp, _ := get(t, "http://127.0.0.1"+port+"/v1/machines"); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/machines = %d", resp.StatusCode)
	}
}

func TestRunNonLoopbackWithToken(t *testing.T) {
	db := filepath.Join(t.TempDir(), "d", "dashboard.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := s.CreateToken(context.Background(), "r", store.ScopeRead, "")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	base, _ := start(t, Config{Listen: "0.0.0.0:0", DB: db})
	url := "http://127.0.0.1" + base[strings.LastIndex(base, ":"):] + "/v1/machines"
	if resp, _ := get(t, url); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bea"+"rer "+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("with token: %d", resp.StatusCode)
	}
}

func TestRunRejectsBadAddress(t *testing.T) {
	err := Run(context.Background(), Config{Listen: "nonsense", DB: filepath.Join(t.TempDir(), "d.db")})
	if err == nil || errors.Is(err, ErrNotLoopback) {
		t.Fatalf("Run = %v, want an invalid-address error", err)
	}
}

func TestLoopback(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1:7777": true,
		"127.1.2.3:80":   true,
		"[::1]:7777":     true,
		"localhost:7777": true,
		"LOCALHOST:0":    true,
		":7777":          false,
		"0.0.0.0:7777":   false,
		"[::]:7777":      false,
		"10.0.0.5:7777":  false,
		"example.com:80": false,
		"127.0.0.1":      false,
	}
	for addr, want := range tests {
		if got := Loopback(addr); got != want {
			t.Errorf("Loopback(%q) = %v, want %v", addr, got, want)
		}
	}
}
