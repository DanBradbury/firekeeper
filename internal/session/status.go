package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"

	"io"
	"os"
	"strings"
)

func (d *discoverer) readRolloutParentThreadID(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	head, err := io.ReadAll(io.LimitReader(file, RolloutMetaHeadBytes))
	if err != nil {
		return ""
	}
	line := head
	if index := bytes.IndexByte(head, '\n'); index >= 0 {
		line = head[:index]
	}
	var record RolloutSessionMetaRecord
	if json.Unmarshal(line, &record) != nil || record.Type != "session_meta" {
		return ""
	}
	if !d.validUUID(record.Payload.ParentThreadID) {
		return ""
	}
	return record.Payload.ParentThreadID
}

func (d *discoverer) readRolloutSessionState(path string) (SessionState, error) {
	file, err := os.Open(path)
	if err != nil {
		return SessionStateUnknown, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return SessionStateUnknown, err
	}
	start := max(info.Size()-RolloutStatusTailBytes, 0)
	if start > 0 {
		if _, err := file.Seek(start, io.SeekStart); err != nil {
			return SessionStateUnknown, err
		}
		reader := bufio.NewReader(file)

		if _, err := reader.ReadBytes('\n'); err != nil && !errors.Is(err, io.EOF) {
			return SessionStateUnknown, err
		}
		state, err := d.scanRolloutSessionState(reader)
		if err == nil && state != SessionStateUnknown {
			return state, nil
		}
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return SessionStateUnknown, err
	}
	return d.scanRolloutSessionState(bufio.NewReader(file))
}

func (d *discoverer) scanRolloutSessionState(reader *bufio.Reader) (SessionState, error) {
	state := SessionStateUnknown
	pendingInput := make(map[string]bool)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var event RolloutStatusEvent
			if json.Unmarshal(line, &event) == nil {
				switch {
				case event.Type == "event_msg" && event.Payload.Type == "task_started":
					state = SessionStateActive
					clear(pendingInput)
				case event.Type == "event_msg" && event.Payload.Type == "task_complete":
					state = SessionStateWaiting
					clear(pendingInput)
				case event.Type == "response_item" &&
					(event.Payload.Type == "function_call" || event.Payload.Type == "custom_tool_call") &&
					event.Payload.Name == "request_user_input":
					pendingInput[event.Payload.CallID] = true
					state = SessionStateNeedsInput
				case event.Type == "response_item" &&
					(event.Payload.Type == "function_call_output" || event.Payload.Type == "custom_tool_call_output"):
					delete(pendingInput, event.Payload.CallID)
					if state == SessionStateNeedsInput && len(pendingInput) == 0 {
						state = SessionStateActive
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

func (s SessionState) String() string {
	switch s {
	case SessionStateActive:
		return "ACTIVE"
	case SessionStateWaiting:
		return "WAITING"
	case SessionStateNeedsInput:
		return "NEEDS INPUT"
	case SessionStateEnded:
		return "ENDED"
	default:
		return "UNKNOWN"
	}
}

// MarshalJSON uses the reporting contract while String preserves TUI labels.
func (s SessionState) MarshalJSON() ([]byte, error) {
	return json.Marshal(strings.ReplaceAll(s.String(), " ", "_"))
}

func (s *SessionState) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return err
	}
	switch name {
	case "ACTIVE":
		*s = SessionStateActive
	case "WAITING":
		*s = SessionStateWaiting
	case "NEEDS_INPUT":
		*s = SessionStateNeedsInput
	case "ENDED":
		*s = SessionStateEnded
	case "UNKNOWN":
		*s = SessionStateUnknown
	default:
		return errors.New("invalid session state")
	}
	return nil
}
