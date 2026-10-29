package otelrecv

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
)

func TestHandlerRecordsAPIRequestAndDropsPrompt(t *testing.T) {
	var file bytes.Buffer
	journal := newJournal(&nopSync{&file}, nil)
	var stderr bytes.Buffer
	srv := httptest.NewServer(newHandler(journal, &stderr))
	t.Cleanup(srv.Close)

	body := handlerFixture(t)
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	post(t, srv.URL+"/v1/logs", "application/json", nil, body)
	post(t, srv.URL+"/v1/logs", "application/json", map[string]string{"Content-Encoding": "gzip"}, compressed.Bytes())
	post(t, srv.URL+"/v1/metrics", "application/json", nil, []byte(`{"prompt":"`+testSecret+`"}`))

	resp, err := http.Post(srv.URL+"/v1/logs", "text/plain", strings.NewReader(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if strings.Contains(stderr.String(), testSecret) || strings.Contains(file.String(), testSecret) {
		t.Fatalf("prompt leaked into stderr or jsonl\nstderr=%s\nfile=%s", stderr.String(), file.String())
	}
	recs, skipped, err := Read(&file)
	if err != nil || skipped != 0 || len(recs) != 1 {
		t.Fatalf("read = %d skipped %d err %v", len(recs), skipped, err)
	}
	if recs[0].QuerySource != "agent_summary" || recs[0].Tokens() != 1205 {
		t.Fatalf("record = %+v", recs[0])
	}
	if !strings.Contains(stderr.String(), "recorded 1 api_request event(s)") || !strings.Contains(stderr.String(), "1 duplicate ignored") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func TestListenSummaryOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout, stderr bytes.Buffer
	ready := make(chan string, 1)
	done := make(chan error, 1)
	path := t.TempDir() + "/otel.jsonl"
	go func() {
		done <- Listen(ctx, Options{
			Port: 0, Path: path, Stdout: &stdout, Stderr: &stderr,
			OnListen: func(addr string) { ready <- addr },
		})
	}()
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("listen ended early: %v\n%s", err, stderr.String())
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not start")
	}
	var resp *http.Response
	var err error
	for range 50 {
		resp, err = http.Post("http://"+addr+"/v1/logs", "application/json", bytes.NewReader(handlerFixture(t)))
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listen did not return")
	}
	if strings.Contains(stdout.String(), testSecret) || strings.Contains(stderr.String(), testSecret) {
		t.Fatal("prompt leaked into listener output")
	}
	for _, want := range []string{"session " + testSession, "agent_summary", "agent_summary requests: 1", "other non-main requests: 0", "CLAUDE_CODE_ENABLE_TELEMETRY", "127.0.0.1"} {
		if !strings.Contains(stdout.String()+stderr.String(), want) {
			t.Fatalf("output missing %q\nstdout=%s\nstderr=%s", want, stdout.String(), stderr.String())
		}
	}
	if strings.Contains(stderr.String(), "0.0.0.0") || strings.Contains(stderr.String(), "[::]") {
		t.Fatalf("stderr suggests a non-loopback bind:\n%s", stderr.String())
	}
}

func TestLoopbackHostPort(t *testing.T) {
	got, err := LoopbackHostPort(4318)
	if err != nil || got != "127.0.0.1:4318" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := LoopbackHostPort(-1); err == nil {
		t.Fatal("accepted a negative port")
	}
	if _, err := LoopbackHostPort(65536); err == nil {
		t.Fatal("accepted a port past 65535")
	}
}

func TestRejectedPayloadDoesNotEchoBody(t *testing.T) {
	var stderr bytes.Buffer
	journal := newJournal(&nopSync{&bytes.Buffer{}}, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader(`{"prompt":"`+testSecret))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	newHandler(journal, &stderr).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), testSecret) || strings.Contains(stderr.String(), testSecret) {
		t.Fatalf("body echoed: %s %s", rr.Body.String(), stderr.String())
	}
}

func post(t *testing.T, url, contentType string, header map[string]string, body []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	for key, value := range header {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: status %d", url, resp.StatusCode)
	}
}

func handlerFixture(t *testing.T) []byte {
	t.Helper()
	body, err := protojson.Marshal(sampleLogs())
	if err != nil {
		t.Fatal(err)
	}
	return body
}

type nopSync struct{ io.Writer }

func (n *nopSync) Sync() error { return nil }

func encodeRequest(w io.Writer, rec Request) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(rec)
}
