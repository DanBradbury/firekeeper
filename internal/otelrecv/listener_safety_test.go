package otelrecv

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// escapePayload sets the clipboard (OSC 52) and the window title (OSC 0) when
// written to a terminal.
const escapePayload = "\x1b]52;c;cHduZWQ=\x07\x1b]0;pwned\x07"

func logsWith(attrs ...*commonpb.KeyValue) *logsv1.LogsData {
	base := []*commonpb.KeyValue{
		attr("event.name", "api_request"),
		attr("session.id", testSession),
		{Key: "input_tokens", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 7}}},
	}
	return &logsv1.LogsData{ResourceLogs: []*logsv1.ResourceLogs{{ScopeLogs: []*logsv1.ScopeLogs{{
		LogRecords: []*logsv1.LogRecord{{Attributes: append(base, attrs...)}},
	}}}}}
}

func logsBody(t *testing.T, data *logsv1.LogsData) []byte {
	t.Helper()
	body, err := protojson.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestHandlerChecksHostAndOrigin(t *testing.T) {
	for _, tt := range []struct {
		name, host, origin string
		want               int
	}{
		{"exporter at 127.0.0.1", "127.0.0.1:4318", "", http.StatusOK},
		{"exporter at localhost", "localhost:4318", "", http.StatusOK},
		{"localhost in capitals", "LOCALHOST:4318", "", http.StatusOK},
		{"no port", "127.0.0.1", "", http.StatusOK},
		{"null origin", "127.0.0.1:4318", "null", http.StatusOK},
		{"loopback origin", "127.0.0.1:4318", "http://127.0.0.1:4318", http.StatusOK},
		{"DNS rebinding", "attacker.example:4318", "", http.StatusForbidden},
		{"DNS rebinding with origin", "attacker.example:4318", "http://attacker.example:4318", http.StatusForbidden},
		{"look-alike host", "127.0.0.1.attacker.example:4318", "", http.StatusForbidden},
		{"localhost suffix", "localhost.attacker.example:4318", "", http.StatusForbidden},
		{"other loopback spelling", "127.1:4318", "", http.StatusForbidden},
		{"empty host", "", "", http.StatusForbidden},
		{"web page origin", "127.0.0.1:4318", "https://attacker.example", http.StatusForbidden},
		{"look-alike origin", "127.0.0.1:4318", "https://127.0.0.1.attacker.example", http.StatusForbidden},
		{"non-web origin", "127.0.0.1:4318", "file://127.0.0.1", http.StatusForbidden},
		{"garbage origin", "127.0.0.1:4318", "%zz", http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var file, stderr bytes.Buffer
			h := newHandler(newJournal(&nopSync{&file}, nil), &stderr)
			req := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(logsBody(t, logsWith())))
			req.Host = tt.host
			req.Header.Set("Content-Type", "application/json")
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d", rr.Code, tt.want)
			}
			if tt.want != http.StatusOK && file.Len() != 0 {
				t.Fatalf("refused request was recorded: %s", file.String())
			}
			if tt.want == http.StatusOK && file.Len() == 0 {
				t.Fatal("accepted request was not recorded")
			}
		})
	}
}

// A browser that rebinds its own domain to 127.0.0.1 reaches the real
// listener but is refused, and nothing reaches the file.
func TestListenRefusesRebindingHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	ctx, cancel := context.WithCancel(context.Background())
	addr := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Listen(ctx, Options{Path: path, OnListen: func(a string) { addr <- a }})
	}()
	bound := <-addr
	send := func(host string) int {
		req, err := http.NewRequest(http.MethodPost, "http://"+bound+"/v1/logs", bytes.NewReader(logsBody(t, logsWith(attr("request_id", "req_"+host)))))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := send("attacker.example:" + strings.Split(bound, ":")[1]); code != http.StatusForbidden {
		t.Errorf("rebinding host: status %d", code)
	}
	if code := send(bound); code != http.StatusOK {
		t.Errorf("loopback host: status %d", code)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	recs, _, err := ReadFile(path)
	if err != nil || len(recs) != 1 || recs[0].RequestID != "req_"+bound {
		t.Fatalf("records = %+v, %v", recs, err)
	}
}

func TestCleanString(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5-20250929"},
		{"agent:builtin:general-purpose", "agent:builtin:general-purpose"},
		{"claude-sonnet-4@20250514", "claude-sonnet-4@20250514"},
		{"arn:aws:bedrock:us-east-1:1:inference-profile/us.anthropic.claude", "arn:aws:bedrock:us-east-1:1:inference-profile/us.anthropic.claude"},
		{"2026-10-29T15:04:05.123Z", "2026-10-29T15:04:05.123Z"},
		{"modèle ünïcode", "modèle ünïcode"},
		{"", ""},
		{"agent" + escapePayload, ""},
		{"a\x1b[31mred", ""},
		{"c1\u009b31m", ""},
		{"tab\there", ""},
		{"line\nbreak", ""},
		{"nul\x00", ""},
		{"del\x7f", ""},
		{"bidi‮gnp.exe", ""},
		{"zero​width", ""},
		{"bad\xffutf8", ""},
		{strings.Repeat("a", maxAttrLen), strings.Repeat("a", maxAttrLen)},
		{strings.Repeat("a", maxAttrLen+1), ""},
	} {
		if got := cleanString(tt.in); got != tt.want {
			t.Errorf("cleanString(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Escape sequences sent to the listener never reach the file, and records
// written before this check are cleaned when read.
func TestEscapeSequencesAreDropped(t *testing.T) {
	var file, stderr bytes.Buffer
	h := newHandler(newJournal(&nopSync{&file}, nil), &stderr)
	body := logsBody(t, logsWith(
		attr("query_source", "agent"+escapePayload),
		attr("model", "claude"+escapePayload),
		attr("request_id", "req_1"),
	))
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4318/v1/logs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if strings.ContainsAny(file.String(), "\x1b\x07") || strings.Contains(file.String(), `\u001b`) || strings.Contains(file.String(), `\u0007`) {
		t.Fatalf("escape sequence written to the file: %q", file.String())
	}
	recs, _, err := Read(&file)
	if err != nil || len(recs) != 1 || recs[0].QuerySource != "" || recs[0].Model != "" || recs[0].InputTokens != 7 {
		t.Fatalf("records = %+v, %v", recs, err)
	}

	// A line an older version wrote, with the sequences JSON-escaped.
	old := `{"session_id":"` + testSession + `","query_source":"agent\u001b]52;c;cHduZWQ=\u0007","model":"m\u001b]0;x\u0007","request_id":"req_2","input_tokens":3,"output_tokens":0,"cache_read_tokens":0,"cache_creation_tokens":0,"cost_usd":0}` + "\n"
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	recs, _, err = ReadFile(path)
	if err != nil || len(recs) != 1 || recs[0].QuerySource != "" || recs[0].Model != "" || recs[0].InputTokens != 3 {
		t.Fatalf("old records = %+v, %v", recs, err)
	}
	var summary bytes.Buffer
	if err := WriteSummary(&summary, recs); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(summary.String(), "\x1b\x07") {
		t.Fatalf("summary prints an escape sequence: %q", summary.String())
	}
}

func FuzzCleanString(f *testing.F) {
	for _, seed := range []string{"agent_summary", "claude-sonnet-4@20250514", escapePayload, "a‮b", "\xff", strings.Repeat("x", maxAttrLen+1)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := cleanString(in)
		if got != "" && got != in {
			t.Fatalf("cleanString changed %q to %q instead of keeping or dropping it", in, got)
		}
		if len(got) > maxAttrLen || !utf8.ValidString(got) {
			t.Fatalf("kept %q", got)
		}
		for _, r := range got {
			if !unicode.IsPrint(r) {
				t.Fatalf("kept non-printable %U in %q", r, got)
			}
		}
	})
}

func FuzzHostAndOrigin(f *testing.F) {
	for _, seed := range []string{"127.0.0.1:4318", "localhost", "attacker.example:4318", "[::1]:4318", "127.0.0.1.attacker.example", "http://127.0.0.1:4318", "https://attacker.example", "null", "%zz", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if loopbackHostHeader(value) {
			host := value
			if name, _, err := net.SplitHostPort(value); err == nil {
				host = name
			}
			if host != "127.0.0.1" && !strings.EqualFold(host, "localhost") {
				t.Fatalf("Host %q accepted", value)
			}
		}
		if allowedOrigin(value) && value != "" && value != "null" {
			u, err := url.Parse(value)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !loopbackHostHeader(u.Host) {
				t.Fatalf("Origin %q accepted", value)
			}
		}
	})
}
