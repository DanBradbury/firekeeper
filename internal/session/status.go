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
	state, _, err := d.readRolloutStatus(path)
	return state, err
}

// readRolloutStatus reads the latest state and cumulative token usage from
// the tail of a rollout, falling back to the whole file when the tail shows
// no state.
func (d *discoverer) readRolloutStatus(path string) (SessionState, TokenBreakdown, error) {
	file, err := os.Open(path)
	if err != nil {
		return SessionStateUnknown, TokenBreakdown{}, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return SessionStateUnknown, TokenBreakdown{}, err
	}
	start := max(info.Size()-RolloutStatusTailBytes, 0)
	if start > 0 {
		if _, err := file.Seek(start, io.SeekStart); err != nil {
			return SessionStateUnknown, TokenBreakdown{}, err
		}
		reader := bufio.NewReader(file)

		if _, err := reader.ReadBytes('\n'); err != nil && !errors.Is(err, io.EOF) {
			return SessionStateUnknown, TokenBreakdown{}, err
		}
		state, tokens, err := d.scanRolloutStatus(reader)
		if err == nil && state != SessionStateUnknown {
			return state, tokens, nil
		}
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return SessionStateUnknown, TokenBreakdown{}, err
	}
	return d.scanRolloutStatus(bufio.NewReader(file))
}

func (d *discoverer) scanRolloutSessionState(reader *bufio.Reader) (SessionState, error) {
	state, _, err := d.scanRolloutStatus(reader)
	return state, err
}

func (d *discoverer) scanRolloutStatus(reader *bufio.Reader) (SessionState, TokenBreakdown, error) {
	state := SessionStateUnknown
	var tokens TokenBreakdown
	pendingInput := make(map[string]bool)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var event RolloutStatusEvent
			if json.Unmarshal(line, &event) == nil {
				if event.Type == "event_msg" && event.Payload.Type == "token_count" &&
					event.Payload.Info != nil && event.Payload.Info.Total != nil {
					// Cumulative for the thread. input_tokens includes cached
					// input, which is counted as cache.
					total := event.Payload.Info.Total
					cached := max(total.Cached, 0)
					tokens = TokenBreakdown{Input: max(total.Input-cached, 0), Output: max(total.Output, 0), Cache: cached}
				}
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
			return state, tokens, nil
		}
		if err != nil {
			return state, tokens, err
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
