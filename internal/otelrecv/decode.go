package otelrecv

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ErrUnsupportedType is returned when the body is not OTLP JSON or protobuf.
var ErrUnsupportedType = errors.New("unsupported content type")

// errInvalidPayload is returned for a body that is not an OTLP logs export.
// The underlying decode error is dropped: protobuf and JSON errors can quote
// the payload, and the payload may contain a prompt.
var errInvalidPayload = errors.New("invalid otlp logs payload")

const (
	attrEventName      = "event.name"
	attrEventTimestamp = "event.timestamp"
	attrEventSequence  = "event.sequence"
	attrSessionID      = "session.id"
	attrModel          = "model"
	attrQuerySource    = "query_source"
	attrInputTokens    = "input_tokens"
	attrOutputTokens   = "output_tokens"
	attrCacheRead      = "cache_read_tokens"
	attrCacheCreation  = "cache_creation_tokens"
	attrCostUSD        = "cost_usd"
	attrCostUSDMicros  = "cost_usd_micros"
	attrRequestID      = "request_id"
)

// DecodeLogs extracts api_request usage from an OTLP/HTTP logs export.
// contentType is the media type without parameters: application/json,
// application/x-protobuf, or application/protobuf.
//
// The body is decoded as logs/v1 LogsData. That message's first field is the
// same repeated ResourceLogs field as the collector ExportLogsServiceRequest,
// so an export POST unmarshals without linking the collector gRPC service.
// Only allowlisted attribute keys are copied. The log body is never read.
func DecodeLogs(contentType string, body []byte) ([]Request, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, nil
	}
	msg := &logsv1.LogsData{}
	switch contentType {
	case "application/json":
		opts := protojson.UnmarshalOptions{DiscardUnknown: true}
		if err := opts.Unmarshal(body, msg); err != nil {
			return nil, errInvalidPayload
		}
	case "application/x-protobuf", "application/protobuf":
		if err := proto.Unmarshal(body, msg); err != nil {
			return nil, errInvalidPayload
		}
	default:
		return nil, ErrUnsupportedType
	}
	return fromLogs(msg), nil
}

func fromLogs(msg *logsv1.LogsData) []Request {
	var out []Request
	for _, rl := range msg.GetResourceLogs() {
		var resource []*commonpb.KeyValue
		if rl.GetResource() != nil {
			resource = rl.GetResource().GetAttributes()
		}
		for _, scope := range rl.GetScopeLogs() {
			for _, rec := range scope.GetLogRecords() {
				if req, ok := requestFrom(rec, resource); ok {
					out = append(out, req)
				}
			}
		}
	}
	return out
}

func requestFrom(rec *logsv1.LogRecord, resource []*commonpb.KeyValue) (Request, bool) {
	if rec == nil {
		return Request{}, false
	}
	attrs := rec.GetAttributes()
	name, _ := shortString(findAttr(attrs, resource, attrEventName))
	eventName := rec.GetEventName()
	if len(eventName) > maxAttrLen || strings.ContainsAny(eventName, "\r\n\x00") {
		eventName = ""
	}
	switch {
	case name == "api_request":
	case name == "" && (eventName == "api_request" || eventName == "claude_code.api_request"):
	default:
		return Request{}, false
	}

	req := Request{
		SessionID:           strAttr(findAttr(attrs, resource, attrSessionID)),
		Model:               strAttr(findAttr(attrs, resource, attrModel)),
		QuerySource:         strAttr(findAttr(attrs, resource, attrQuerySource)),
		InputTokens:         nonNeg(intAttr(findAttr(attrs, nil, attrInputTokens))),
		OutputTokens:        nonNeg(intAttr(findAttr(attrs, nil, attrOutputTokens))),
		CacheReadTokens:     nonNeg(intAttr(findAttr(attrs, nil, attrCacheRead))),
		CacheCreationTokens: nonNeg(intAttr(findAttr(attrs, nil, attrCacheCreation))),
		RequestID:           strAttr(findAttr(attrs, nil, attrRequestID)),
	}
	if seq, ok := intAttrOK(findAttr(attrs, nil, attrEventSequence)); ok {
		req.EventSequence = &seq
	}
	if micros, ok := intAttrOK(findAttr(attrs, nil, attrCostUSDMicros)); ok {
		if micros > 0 {
			req.CostUSD = float64(micros) / 1_000_000
		}
	} else if cost, ok := floatAttr(findAttr(attrs, nil, attrCostUSD)); ok && cost > 0 {
		req.CostUSD = cost
	}
	if ts, ok := eventTime(rec, attrs); ok {
		req.TS = ts.UTC().Format(time.RFC3339Nano)
	}
	return req.clean(), true
}

func findAttr(primary, fallback []*commonpb.KeyValue, key string) *commonpb.AnyValue {
	if value := findOne(primary, key); value != nil {
		return value
	}
	return findOne(fallback, key)
}

func findOne(kvs []*commonpb.KeyValue, key string) *commonpb.AnyValue {
	for _, kv := range kvs {
		if kv != nil && kv.GetKey() == key {
			return kv.GetValue()
		}
	}
	return nil
}

func shortString(value *commonpb.AnyValue) (string, bool) {
	text := value.GetStringValue()
	if value == nil || value.GetValue() == nil {
		return "", false
	}
	if _, ok := value.GetValue().(*commonpb.AnyValue_StringValue); !ok {
		return "", false
	}
	if len(text) > maxAttrLen || strings.ContainsAny(text, "\r\n\x00") {
		return "", false
	}
	return text, true
}

func strAttr(value *commonpb.AnyValue) string {
	text, ok := shortString(value)
	if !ok {
		return ""
	}
	return text
}

func intAttr(value *commonpb.AnyValue) int64 {
	n, _ := intAttrOK(value)
	return n
}

func intAttrOK(value *commonpb.AnyValue) (int64, bool) {
	if value == nil || value.GetValue() == nil {
		return 0, false
	}
	switch typed := value.GetValue().(type) {
	case *commonpb.AnyValue_IntValue:
		return typed.IntValue, true
	case *commonpb.AnyValue_DoubleValue:
		if math.IsNaN(typed.DoubleValue) || math.IsInf(typed.DoubleValue, 0) {
			return 0, false
		}
		return int64(math.Round(typed.DoubleValue)), true
	case *commonpb.AnyValue_StringValue:
		if len(typed.StringValue) == 0 || len(typed.StringValue) > 32 {
			return 0, false
		}
		n, err := strconv.ParseInt(strings.TrimSpace(typed.StringValue), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func floatAttr(value *commonpb.AnyValue) (float64, bool) {
	if value == nil || value.GetValue() == nil {
		return 0, false
	}
	switch typed := value.GetValue().(type) {
	case *commonpb.AnyValue_DoubleValue:
		if math.IsNaN(typed.DoubleValue) || math.IsInf(typed.DoubleValue, 0) {
			return 0, false
		}
		return typed.DoubleValue, true
	case *commonpb.AnyValue_IntValue:
		return float64(typed.IntValue), true
	case *commonpb.AnyValue_StringValue:
		if len(typed.StringValue) == 0 || len(typed.StringValue) > 32 {
			return 0, false
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(typed.StringValue), 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func nonNeg(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

func eventTime(rec *logsv1.LogRecord, attrs []*commonpb.KeyValue) (time.Time, bool) {
	if text, ok := shortString(findAttr(attrs, nil, attrEventTimestamp)); ok {
		if ts, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return ts, true
		}
	}
	if ts, ok := unixNano(rec.GetTimeUnixNano()); ok {
		return ts, true
	}
	return unixNano(rec.GetObservedTimeUnixNano())
}

func unixNano(n uint64) (time.Time, bool) {
	if n == 0 || n > uint64(math.MaxInt64) {
		return time.Time{}, false
	}
	return time.Unix(0, int64(n)).UTC(), true
}
