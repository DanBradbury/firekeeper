package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
)

const rolloutStatusTailBytes int64 = 4 << 20

type sessionState int

const (
	sessionStateUnknown sessionState = iota
	sessionStateActive
	sessionStateWaiting
	sessionStateNeedsInput
)

func (s sessionState) String() string {
	switch s {
	case sessionStateActive:
		return "ACTIVE"
	case sessionStateWaiting:
		return "WAITING"
	case sessionStateNeedsInput:
		return "NEEDS INPUT"
	default:
		return "UNKNOWN"
	}
}

type rolloutStatusEvent struct {
	Type    string `json:"type"`
	Payload struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		CallID string `json:"call_id"`
	} `json:"payload"`
}

func readRolloutSessionState(path string) (sessionState, error) {
	file, err := os.Open(path)
	if err != nil {
		return sessionStateUnknown, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return sessionStateUnknown, err
	}
	start := max(info.Size()-rolloutStatusTailBytes, 0)
	if start > 0 {
		if _, err := file.Seek(start, io.SeekStart); err != nil {
			return sessionStateUnknown, err
		}
		reader := bufio.NewReader(file)
		// Tail starts inside an arbitrary JSONL record. Discard partial record.
		if _, err := reader.ReadBytes('\n'); err != nil && !errors.Is(err, io.EOF) {
			return sessionStateUnknown, err
		}
		state, err := scanRolloutSessionState(reader)
		if err == nil && state != sessionStateUnknown {
			return state, nil
		}
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return sessionStateUnknown, err
	}
	return scanRolloutSessionState(bufio.NewReader(file))
}

func scanRolloutSessionState(reader *bufio.Reader) (sessionState, error) {
	state := sessionStateUnknown
	pendingInput := make(map[string]bool)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var event rolloutStatusEvent
			if json.Unmarshal(line, &event) == nil {
				switch {
				case event.Type == "event_msg" && event.Payload.Type == "task_started":
					state = sessionStateActive
					clear(pendingInput)
				case event.Type == "event_msg" && event.Payload.Type == "task_complete":
					state = sessionStateWaiting
					clear(pendingInput)
				case event.Type == "response_item" &&
					(event.Payload.Type == "function_call" || event.Payload.Type == "custom_tool_call") &&
					event.Payload.Name == "request_user_input":
					pendingInput[event.Payload.CallID] = true
					state = sessionStateNeedsInput
				case event.Type == "response_item" &&
					(event.Payload.Type == "function_call_output" || event.Payload.Type == "custom_tool_call_output"):
					delete(pendingInput, event.Payload.CallID)
					if state == sessionStateNeedsInput && len(pendingInput) == 0 {
						state = sessionStateActive
					}
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return state, nil
		}
		if err != nil {
			return state, err
		}
	}
}
