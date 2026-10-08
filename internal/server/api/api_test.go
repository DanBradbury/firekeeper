package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

func post(t *testing.T, h http.Handler, path string, v any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(v)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", path, bytes.NewReader(b)))
	return rr
}

func req(n int) ingestRequest {
	b := store.SessionBatch{Meta: store.SessionMeta{SessionID: "s", Provider: "codex"}}
	for i := 0; i < n; i++ {
		b.Events = append(b.Events, transcript.Event{Provider: "codex", Seq: int64(i), Role: "user", Text: "x", Raw: json.RawMessage(`{}`)})
	}
	return ingestRequest{Machine: store.Machine{ID: "m"}, Sessions: []store.SessionBatch{b}}
}

func TestIngestLimitsAndIdempotency(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := Handler(s)

	var first, second map[string]int
	rr := post(t, h, "/v1/ingest", req(500))
	json.Unmarshal(rr.Body.Bytes(), &first)
	if rr.Code != 200 || first["accepted"] != 500 {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	rr = post(t, h, "/v1/ingest", req(500))
	json.Unmarshal(rr.Body.Bytes(), &second)
	if second["duplicates"] != first["accepted"] {
		t.Fatalf("%v", second)
	}
	if rr := post(t, h, "/v1/ingest", req(600)); rr.Code != 413 {
		t.Fatalf("600 events: %d", rr.Code)
	}
	big := req(1)
	big.Sessions[0].Events[0].Text = string(bytes.Repeat([]byte("a"), MaxBodyBytes))
	if rr := post(t, h, "/v1/ingest", big); rr.Code != 413 {
		t.Fatalf("big body: %d", rr.Code)
	}
	if rr := post(t, h, "/v1/ingest", ingestRequest{}); rr.Code != 400 {
		t.Fatalf("no machine: %d", rr.Code)
	}
	rr = post(t, h, "/v1/heartbeat", heartbeatRequest{Machine: store.Machine{ID: "m"}, Sessions: []store.Heartbeat{{SessionID: "s", State: "ACTIVE"}}})
	if rr.Code != 200 {
		t.Fatalf("heartbeat %d %s", rr.Code, rr.Body)
	}
}
