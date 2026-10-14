package session

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rolloutState scans a rollout for its latest state, ignoring token usage.
func rolloutState(t *testing.T, lines string) State {
	t.Helper()
	state, _, err := (&discoverer{}).scanRolloutStatus(bufio.NewReader(strings.NewReader(lines)))
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRolloutSessionStateActive(t *testing.T) {
	state := rolloutState(t, strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"task_complete"}}`,
		`{"type":"event_msg","payload":{"type":"task_started"}}`,
		`{"type":"response_item","payload":{"type":"message"}}`,
	}, "\n"))
	if state != SessionStateActive {
		t.Fatalf("session state = %s, want ACTIVE", state)
	}
}

func TestRolloutSessionStateWaiting(t *testing.T) {
	state := rolloutState(t, strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"task_started"}}`,
		`{"type":"event_msg","payload":{"type":"task_complete"}}`,
	}, "\n"))
	if state != SessionStateWaiting {
		t.Fatalf("session state = %s, want WAITING", state)
	}
}

func TestRolloutSessionStateTracksUserInputRequest(t *testing.T) {
	pending := strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"task_started"}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call-1"}}`,
	}, "\n")
	if state := rolloutState(t, pending); state != SessionStateNeedsInput {
		t.Fatalf("pending input state = %s, want NEEDS INPUT", state)
	}

	resolved := pending + "\n" + `{"type":"response_item","payload":{"type":"function_call_output","call_id":"call-1"}}`
	if state := rolloutState(t, resolved); state != SessionStateActive {
		t.Fatalf("resolved input state = %s, want ACTIVE", state)
	}
}

func TestRolloutSessionStateIgnoresMarkersInsideMessages(t *testing.T) {
	state := rolloutState(t, `{"type":"response_item","payload":{"type":"message","text":"task_started and task_complete"}}`)
	if state != SessionStateUnknown {
		t.Fatalf("message-only state = %s, want UNKNOWN", state)
	}
}

func writeTestRollout(t *testing.T, dir, id, parent string) string {
	t.Helper()
	payload := `{"id":"` + id + `"`
	if parent != "" {
		payload += `,"parent_thread_id":"` + parent + `"`
	}
	payload += "}"
	meta := `{"timestamp":"2026-08-22T08:00:00Z","type":"session_meta","payload":` + payload + `}`
	path := filepath.Join(dir, "rollout-2026-08-22T00-58-39-"+id+".jsonl")
	if err := os.WriteFile(path, []byte(meta+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadRolloutParentThreadID(t *testing.T) {
	d := &discoverer{}
	dir := t.TempDir()
	child := writeTestRollout(t, dir, "019fb33c-c5f4-75f3-b987-228eb484c6ec", "019fb33c-c5f4-75f3-b987-111111111111")
	if got := d.readRolloutParentThreadID(child); got != "019fb33c-c5f4-75f3-b987-111111111111" {
		t.Fatalf("parent thread id = %q", got)
	}

	root := writeTestRollout(t, dir, "019fb33c-c5f4-75f3-b987-222222222222", "")
	if got := d.readRolloutParentThreadID(root); got != "" {
		t.Fatalf("root parent thread id = %q, want empty", got)
	}
	if got := d.readRolloutParentThreadID(filepath.Join(dir, "missing.jsonl")); got != "" {
		t.Fatalf("missing rollout parent thread id = %q, want empty", got)
	}
}

func TestDropSupersededForkRollouts(t *testing.T) {
	d := &discoverer{}
	dir := t.TempDir()
	rootID := "019fb33c-c5f4-75f3-b987-111111111111"
	forkID := "019fb33c-c5f4-75f3-b987-222222222222"
	leafID := "019fb33c-c5f4-75f3-b987-333333333333"
	otherID := "019fb33c-c5f4-75f3-b987-444444444444"

	root := writeTestRollout(t, dir, rootID, "")
	fork := writeTestRollout(t, dir, forkID, rootID)
	leaf := writeTestRollout(t, dir, leafID, forkID)
	other := writeTestRollout(t, dir, otherID, "")

	kept := d.dropSupersededForkRollouts([]string{root, fork})
	if len(kept) != 1 || kept[0] != fork {
		t.Fatalf("fork pair kept = %#v, want only the fork", kept)
	}

	kept = d.dropSupersededForkRollouts([]string{root, fork, leaf})
	if len(kept) != 1 || kept[0] != leaf {
		t.Fatalf("fork chain kept = %#v, want only the leaf", kept)
	}

	kept = d.dropSupersededForkRollouts([]string{root, other})
	if len(kept) != 2 {
		t.Fatalf("independent rollouts kept = %#v, want both", kept)
	}

	if got := d.dropSupersededForkRollouts([]string{root}); len(got) != 1 || got[0] != root {
		t.Fatalf("single rollout kept = %#v", got)
	}
}
