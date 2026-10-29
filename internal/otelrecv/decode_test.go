package otelrecv

import (
	"bytes"
	"strings"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	testSession = "11111111-1111-4111-8111-111111111111"
	testSecret  = "SECRET_OTEL_PROMPT"
)

func TestDecodeLogsJSONAndProtobuf(t *testing.T) {
	msg := sampleLogs()
	jsonBody, err := protojson.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	bin, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal protobuf: %v", err)
	}
	// A hand-written export uses a JSON number for one int64, which the
	// protobuf JSON mapping also accepts.
	hand := []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"session.id","value":{"stringValue":"` + testSession + `"}}]},"scopeLogs":[{"logRecords":[
		{"timeUnixNano":"1761746400000000000","attributes":[
			{"key":"event.name","value":{"stringValue":"api_request"}},
			{"key":"query_source","value":{"stringValue":"repl_main_thread"}},
			{"key":"model","value":{"stringValue":"claude-haiku"}},
			{"key":"input_tokens","value":{"intValue":80}},
			{"key":"output_tokens","value":{"intValue":"20"}},
			{"key":"cache_read_tokens","value":{"intValue":0}},
			{"key":"cache_creation_tokens","value":{"intValue":0}},
			{"key":"cost_usd","value":{"doubleValue":0.5}},
			{"key":"cost_usd_micros","value":{"intValue":250000}},
			{"key":"request_id","value":{"stringValue":"req_main"}},
			{"key":"prompt","value":{"stringValue":"` + testSecret + `"}}
		]}
	]}]}]}`)

	for _, tt := range []struct {
		name, media string
		body        []byte
		wantSource  string
		wantInput   int64
		wantCost    float64
	}{
		{name: "json", media: "application/json", body: jsonBody, wantSource: "agent_summary", wantInput: 200, wantCost: 0.25},
		{name: "protobuf", media: "application/x-protobuf", body: bin, wantSource: "agent_summary", wantInput: 200, wantCost: 0.25},
		{name: "json number", media: "application/json", body: hand, wantSource: "repl_main_thread", wantInput: 80, wantCost: 0.25},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeLogs(tt.media, tt.body)
			if err != nil {
				t.Fatalf("DecodeLogs: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("events = %d, want 1", len(got))
			}
			rec := got[0]
			if rec.QuerySource != tt.wantSource || rec.SessionID != testSession || rec.Model != "claude-haiku" || rec.InputTokens != tt.wantInput || rec.CostUSD != tt.wantCost {
				t.Fatalf("record = %+v", rec)
			}
			if strings.Contains(mustJSON(t, rec), testSecret) {
				t.Fatal("decoded record contains prompt text")
			}
		})
	}
}

func TestDecodeLogsIgnoresNonAPIRequests(t *testing.T) {
	body := []byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[
		{"attributes":[{"key":"event.name","value":{"stringValue":"user_prompt"}},{"key":"prompt","value":{"stringValue":"` + testSecret + `"}}]},
		{"attributes":[{"key":"event.name","value":{"stringValue":"api_request_body"}},{"key":"body","value":{"stringValue":"` + testSecret + `"}}]},
		{"eventName":"claude_code.api_request","attributes":[
			{"key":"model","value":{"stringValue":"claude-haiku"}},
			{"key":"input_tokens","value":{"intValue":"3"}},
			{"key":"output_tokens","value":{"intValue":1}},
			{"key":"session.id","value":{"stringValue":"` + testSession + `"}}
		]}
	]}]}]}`)
	got, err := DecodeLogs("application/json", body)
	if err != nil {
		t.Fatalf("DecodeLogs: %v", err)
	}
	if len(got) != 1 || got[0].InputTokens != 3 || got[0].QuerySource != "" {
		t.Fatalf("got %+v", got)
	}
	if strings.Contains(mustJSON(t, got[0]), testSecret) {
		t.Fatal("non-request text leaked into the record")
	}
}

func TestDecodeLogsRejectsGarbageWithoutQuotingIt(t *testing.T) {
	_, err := DecodeLogs("application/json", []byte(`{"prompt":"`+testSecret))
	if err == nil || strings.Contains(err.Error(), testSecret) {
		t.Fatalf("err = %v", err)
	}
	if _, err := DecodeLogs("text/plain", []byte(`{}`)); err != ErrUnsupportedType {
		t.Fatalf("err = %v", err)
	}
	got, err := DecodeLogs("application/json", nil)
	if err != nil || got != nil {
		t.Fatalf("empty = %v %v", got, err)
	}
}

func sampleLogs() *logsv1.LogsData {
	seq := &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 4}}
	return &logsv1.LogsData{ResourceLogs: []*logsv1.ResourceLogs{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			attr("session.id", "other-session"),
		}},
		ScopeLogs: []*logsv1.ScopeLogs{{
			LogRecords: []*logsv1.LogRecord{
				{
					TimeUnixNano: uint64(time.Date(2026, 10, 29, 15, 4, 5, 0, time.UTC).UnixNano()),
					Body:         &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: testSecret}},
					Attributes: []*commonpb.KeyValue{
						attr("event.name", "api_request"),
						attr("event.timestamp", "2026-10-29T15:04:05Z"),
						attr("session.id", testSession),
						attr("model", "claude-haiku"),
						attr("query_source", "agent_summary"),
						{Key: "input_tokens", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 200}}},
						{Key: "output_tokens", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 5}}},
						{Key: "cache_read_tokens", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 1000}}},
						{Key: "cache_creation_tokens", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 0}}},
						{Key: "cost_usd_micros", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 250000}}},
						{Key: "cost_usd", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 9}}},
						attr("request_id", "req_sum"),
						{Key: "event.sequence", Value: seq},
						attr("prompt", testSecret),
						attr("response", testSecret),
					},
				},
				{
					Attributes: []*commonpb.KeyValue{
						attr("event.name", "user_prompt"),
						attr("prompt", testSecret),
					},
				},
			},
		}},
	}}}
}

func attr(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

func mustJSON(t *testing.T, rec Request) string {
	t.Helper()
	var buf bytes.Buffer
	if err := encodeRequest(&buf, rec); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
