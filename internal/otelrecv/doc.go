// Package otelrecv is an experimental, opt-in OTLP/HTTP receiver for Claude
// Code api_request usage events.
//
// It listens on 127.0.0.1 only, writes token and cost fields to a local JSONL
// file, and never records prompt or transcript text. Nothing in this package
// opens an outbound connection or starts unless a caller invokes it. The
// dashboard, report, and daemon do not.
package otelrecv
