package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/server/api"
	"github.com/DanBradbury/firekeeper/internal/server/store"
)

func getJSON(t *testing.T, url, token string, v any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

// localConfig returns a Config with every local-mode hook set, recording
// what each was called with.
func localConfig(db string) (Config, *localCalls) {
	c := &localCalls{}
	return Config{
		DB:           db,
		LocalMachine: &store.Machine{ID: "m-local", Name: "laptop", Hostname: "laptop", OS: "linux", Version: "1.2.3"},
		Local: func(_ context.Context, dashboardURL string) *api.LocalStatus {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.localURL = dashboardURL
			return &api.LocalStatus{MachineID: "m-local", Daemon: api.DaemonNotInstalled, Command: "firekeeper daemon install"}
		},
		OnLocal: func(url string) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.onLocal = url
		},
	}, c
}

type localCalls struct {
	mu                sync.Mutex
	localURL, onLocal string
}

func (c *localCalls) get() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.localURL, c.onLocal
}

func TestRunLocalMode(t *testing.T) {
	cfg, calls := localConfig("")
	base, stop := start(t, cfg)
	defer stop()

	var machines struct{ Machines []store.MachineInfo }
	if code := getJSON(t, base+"/v1/machines", "", &machines); code != 200 {
		t.Fatalf("GET /v1/machines: %d", code)
	}
	if len(machines.Machines) != 1 || machines.Machines[0].ID != "m-local" || machines.Machines[0].LastHeartbeatAt != nil {
		t.Fatalf("machines = %+v", machines.Machines)
	}
	var acct struct {
		SingleUser bool             `json:"single_user"`
		Local      *api.LocalStatus `json:"local"`
	}
	if code := getJSON(t, base+"/v1/account", "", &acct); code != 200 {
		t.Fatalf("GET /v1/account: %d", code)
	}
	if !acct.SingleUser || acct.Local == nil || acct.Local.MachineID != "m-local" {
		t.Fatalf("account = %+v", acct)
	}
	if localURL, onLocal := calls.get(); localURL != base+"/" || onLocal != base+"/" {
		t.Fatalf("Local got %q, OnLocal got %q, want %q", localURL, onLocal, base)
	}
}

// A server that already has a token is not local: it neither registers the
// machine nor reports on it, even on loopback.
func TestRunNotLocalWithToken(t *testing.T) {
	var secret string
	db := seedDB(t, func(s *store.Store) {
		var err error
		if _, secret, err = s.CreateToken(context.Background(), store.DefaultAccountID, "r", store.ScopeRead, ""); err != nil {
			t.Fatal(err)
		}
	})
	cfg, calls := localConfig(db)
	base, stop := start(t, cfg)
	defer stop()

	var machines struct{ Machines []store.MachineInfo }
	if code := getJSON(t, base+"/v1/machines", secret, &machines); code != 200 || len(machines.Machines) != 0 {
		t.Fatalf("GET /v1/machines: %d %+v", code, machines.Machines)
	}
	var acct map[string]json.RawMessage
	if code := getJSON(t, base+"/v1/account", secret, &acct); code != 200 || acct["local"] != nil {
		t.Fatalf("GET /v1/account: %d local=%s", code, acct["local"])
	}
	if localURL, onLocal := calls.get(); localURL != "" || onLocal != "" {
		t.Fatalf("local hooks ran: %q %q", localURL, onLocal)
	}
}
