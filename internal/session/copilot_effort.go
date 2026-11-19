package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// CopilotSessionEffort returns the reasoning effort a Copilot CLI session ran
// at: the one its latest session.model_change event names. Reading a whole
// events file for every session in a list would be slow, so only the start of
// the file and its last CopilotEventTailBytes are read, and a model change in
// the middle of a very long session can be missed. It returns "" when the id is
// not a session id, the file cannot be read, or no effort is recorded.
func CopilotSessionEffort(home, id string) string {
	if !ValidUUID(id) {
		return ""
	}
	file, err := os.Open(filepath.Join(home, "session-state", id, "events.jsonl"))
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	if info.Size() <= CopilotEffortHeadBytes+CopilotEventTailBytes {
		effort, _ := scanCopilotEffort(file)
		return effort
	}
	effort, _ := scanCopilotEffort(io.LimitReader(file, CopilotEffortHeadBytes))
	if _, err := file.Seek(info.Size()-CopilotEventTailBytes, io.SeekStart); err != nil {
		return effort
	}
	reader := bufio.NewReader(file)
	_, _ = reader.ReadBytes('\n') // the first line may be cut off
	if tail, found := scanCopilotEffort(reader); found {
		return tail
	}
	return effort
}

// scanCopilotEffort returns the effort of the last session.model_change event
// and whether there was one. A change that names no effort gives "", true: the
// model has no effort setting.
func scanCopilotEffort(reader io.Reader) (string, bool) {
	var effort string
	var found bool
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, []byte(`"session.model_change"`)) {
			continue
		}
		var event CopilotEvent
		if json.Unmarshal(line, &event) != nil || event.Type != "session.model_change" {
			continue
		}
		effort, found = effortLabel(event.Data.ReasoningEffort), true
	}
	return effort, found
}
