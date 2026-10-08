package reporter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

func TestStopEndsPassAfterBatchInFlight(t *testing.T) {
	f := newFixture(t)
	ts := newTestServer(t)
	f.append(t, 0, 1200)
	stop := make(chan struct{})
	ts.intercept = func(n int, w http.ResponseWriter, r *http.Request, next http.Handler) bool {
		if n == 1 {
			close(stop) // stop while the first batch is on the wire
		}
		return false
	}
	cfg := f.config(ts.URL)
	cfg.Stop = stop
	summary, err := RunOnce(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Stopped || len(summary.Sessions) != 1 || summary.Failed() {
		t.Fatalf("summary = %+v", summary)
	}
	if r := summary.Sessions[0]; r.Batches != 1 || ts.requests != 1 {
		t.Fatalf("sent %d batches in %d requests, want only the one in flight", r.Batches, ts.requests)
	}
	if st := f.state(t); st.LastSeq != 499 || st.Offset != 0 {
		t.Fatalf("state = %+v, want the first batch confirmed and the offset unmoved", st)
	}
	assertSeqs(t, ts.stored(t), 500)

	// The next pass resumes after the confirmed batch.
	ts.intercept = nil
	cfg.Stop = nil
	r := runOnce(t, cfg)
	if r.Err != nil || r.Events != 700 || r.Duplicates != 0 {
		t.Fatalf("resumed pass = %+v", r)
	}
	assertSeqs(t, ts.stored(t), 1200)
}

func TestStoppedBeforeStartReadsNothing(t *testing.T) {
	f := newFixture(t)
	f.append(t, 0, 3)
	stop := make(chan struct{})
	close(stop)
	cfg := f.config("http://127.0.0.1:1")
	cfg.Client = &http.Client{Transport: failTransport{t}}
	cfg.Stop = stop
	summary, err := RunOnce(context.Background(), cfg)
	if err != nil || !summary.Stopped || len(summary.Sessions) != 0 {
		t.Fatalf("summary = %+v, err = %v", summary, err)
	}
}

func TestSummaryCarriesHeartbeatFields(t *testing.T) {
	f := newFixture(t)
	f.append(t, 0, 1)
	cfg := f.config("")
	cfg.DryRun = true
	summary, err := RunOnce(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if summary.MachineID != testMachine || len(summary.Sessions) != 1 || summary.Sessions[0].State != "WAITING" {
		t.Fatalf("summary = %+v", summary)
	}
}

// heartbeatCapture answers heartbeats with 200 and keeps the last body.
func heartbeatCapture(t *testing.T, status int) (*httptest.Server, *heartbeatRequest) {
	t.Helper()
	var got heartbeatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/heartbeat" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("heartbeat body: %v", err)
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			w.Write([]byte(`{"ok":true}`))
		} else {
			w.Write([]byte(`{"error":"x","code":"internal"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestHeartbeatSendsOnlySessionsThePassRead(t *testing.T) {
	srv, got := heartbeatCapture(t, http.StatusOK)
	cfg := Config{Server: srv.URL, Providers: []transcript.Provider{transcript.ProviderCodex}, Home: t.TempDir(), Version: "v"}
	summary := Summary{MachineID: testMachine, Sessions: []SessionResult{
		{Provider: "codex", SessionID: "read", State: "ACTIVE"},
		{Provider: "codex", SessionID: "failed", State: "WAITING", Err: errors.New("upload: boom")},
		{Provider: "copilot", SessionID: "not-allowed", State: "ACTIVE", Skipped: "provider not allowlisted"},
		{Provider: "codex", SessionID: "ignored", State: "ACTIVE", Skipped: ".firekeeper-ignore"},
	}}
	if err := Heartbeat(context.Background(), cfg, summary); err != nil {
		t.Fatal(err)
	}
	if got.Machine.ID != testMachine || got.Machine.Version != "v" {
		t.Fatalf("machine = %+v", got.Machine)
	}
	var ids []string
	for _, s := range got.Sessions {
		ids = append(ids, s.SessionID+"="+s.State)
	}
	if strings.Join(ids, ",") != "read=ACTIVE,failed=WAITING" {
		t.Fatalf("sessions = %v", ids)
	}
}

func TestHeartbeatWithNoSessions(t *testing.T) {
	srv, got := heartbeatCapture(t, http.StatusOK)
	home := t.TempDir()
	cfg := Config{Server: srv.URL, Providers: []transcript.Provider{transcript.ProviderCodex}, Home: home}
	if err := Heartbeat(context.Background(), cfg, Summary{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".firekeeper", "machine-id"))
	if err != nil || strings.TrimSpace(string(data)) != got.Machine.ID || !session.ValidUUID(got.Machine.ID) {
		t.Fatalf("machine id %q does not match the stored id", got.Machine.ID)
	}
	if got.Sessions == nil || len(got.Sessions) != 0 {
		t.Fatalf("sessions = %v, want an empty list", got.Sessions)
	}
}

func TestHeartbeatErrors(t *testing.T) {
	srv, _ := heartbeatCapture(t, http.StatusInternalServerError)
	cfg := Config{Server: srv.URL, Providers: []transcript.Provider{transcript.ProviderCodex}, Home: t.TempDir()}
	err := Heartbeat(context.Background(), cfg, Summary{MachineID: testMachine})
	if err == nil || err.Error() != "heartbeat: server returned 500 (internal)" {
		t.Fatalf("err = %v", err)
	}
}

func TestHeartbeatSendsNothingWithoutUpload(t *testing.T) {
	for _, cfg := range []Config{
		{Providers: nil},
		{Providers: []transcript.Provider{transcript.ProviderCodex}, DryRun: true},
	} {
		cfg.Home = t.TempDir()
		cfg.Client = &http.Client{Transport: failTransport{t}}
		if err := Heartbeat(context.Background(), cfg, Summary{MachineID: testMachine}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHeartbeatMarksMachineOnline(t *testing.T) {
	f := newFixture(t)
	ts := newTestServer(t)
	f.append(t, 0, 2)
	cfg := f.config(ts.URL)
	summary, err := RunOnce(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := Heartbeat(context.Background(), cfg, summary); err != nil {
		t.Fatal(err)
	}
	machines, err := ts.store.ListMachines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) != 1 || machines[0].ID != testMachine || machines[0].LastHeartbeatAt == nil {
		t.Fatalf("machines = %+v", machines)
	}
}
