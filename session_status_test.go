package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestRolloutSessionStateActive(t *testing.T) {
	state, err := scanRolloutSessionState(bufio.NewReader(strings.NewReader(strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"task_complete"}}`,
		`{"type":"event_msg","payload":{"type":"task_started"}}`,
		`{"type":"response_item","payload":{"type":"message"}}`,
	}, "\n"))))
	if err != nil {
		t.Fatal(err)
	}
	if state != sessionStateActive {
		t.Fatalf("session state = %s, want ACTIVE", state)
	}
}

func TestRolloutSessionStateWaiting(t *testing.T) {
	state, err := scanRolloutSessionState(bufio.NewReader(strings.NewReader(strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"task_started"}}`,
		`{"type":"event_msg","payload":{"type":"task_complete"}}`,
	}, "\n"))))
	if err != nil {
		t.Fatal(err)
	}
	if state != sessionStateWaiting {
		t.Fatalf("session state = %s, want WAITING", state)
	}
}

func TestRolloutSessionStateTracksUserInputRequest(t *testing.T) {
	pending := strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"task_started"}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call-1"}}`,
	}, "\n")
	state, err := scanRolloutSessionState(bufio.NewReader(strings.NewReader(pending)))
	if err != nil {
		t.Fatal(err)
	}
	if state != sessionStateNeedsInput {
		t.Fatalf("pending input state = %s, want NEEDS INPUT", state)
	}

	resolved := pending + "\n" + `{"type":"response_item","payload":{"type":"function_call_output","call_id":"call-1"}}`
	state, err = scanRolloutSessionState(bufio.NewReader(strings.NewReader(resolved)))
	if err != nil {
		t.Fatal(err)
	}
	if state != sessionStateActive {
		t.Fatalf("resolved input state = %s, want ACTIVE", state)
	}
}

func TestRolloutSessionStateIgnoresMarkersInsideMessages(t *testing.T) {
	state, err := scanRolloutSessionState(bufio.NewReader(strings.NewReader(
		`{"type":"response_item","payload":{"type":"message","text":"task_started and task_complete"}}`,
	)))
	if err != nil {
		t.Fatal(err)
	}
	if state != sessionStateUnknown {
		t.Fatalf("message-only state = %s, want UNKNOWN", state)
	}
}
