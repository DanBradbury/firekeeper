package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
)

// TouchedFiles returns the file paths a tool call in e edits, writes, or
// patches, read from e.Raw. Paths are returned as the provider wrote them,
// absolute or relative to the session's working directory, in first-seen
// order without duplicates. Reads, searches, and shell commands other than
// apply_patch are not file changes and yield nothing, as does any record the
// provider shapes below do not match.
//
// Shapes recognized:
//   - claude: assistant tool_use blocks named Edit, MultiEdit, Write
//     (input.file_path) or NotebookEdit (input.notebook_path), including
//     sidechain records.
//   - codex: apply_patch envelopes ("*** Add File: p", "*** Update File: p",
//     "*** Delete File: p", "*** Move to: p") in any response_item tool call
//     input or arguments, which covers the apply_patch tool and apply_patch
//     run through a shell tool.
//   - copilot: tool.execution_start for edit, create, write, and
//     str_replace_editor commands other than view (arguments.path), and
//     apply_patch envelopes in any tool's arguments.
func TouchedFiles(e Event) []string {
	if len(e.Raw) == 0 {
		return nil
	}
	var paths []string
	switch e.Provider {
	case ProviderClaude:
		paths = claudeFiles(e.Raw)
	case ProviderCodex:
		paths = codexFiles(e.Raw)
	case ProviderCopilot:
		paths = copilotFiles(e.Raw)
	}
	return dedupe(paths)
}

var claudePathKeys = map[string]string{
	"Edit": "file_path", "MultiEdit": "file_path", "Write": "file_path",
	"NotebookEdit": "notebook_path",
}

func claudeFiles(raw json.RawMessage) []string {
	if !bytes.Contains(raw, []byte(`"tool_use"`)) {
		return nil
	}
	var rec struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &rec) != nil {
		return nil
	}
	var blocks []struct {
		Type  string                     `json:"type"`
		Name  string                     `json:"name"`
		Input map[string]json.RawMessage `json:"input"`
	}
	if json.Unmarshal(rec.Message.Content, &blocks) != nil {
		return nil // string content has no tool calls
	}
	var out []string
	for _, b := range blocks {
		key, ok := claudePathKeys[b.Name]
		if b.Type != "tool_use" || !ok {
			continue
		}
		if p := jsonString(b.Input[key]); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func codexFiles(raw json.RawMessage) []string {
	if !bytes.Contains(raw, []byte("*** ")) {
		return nil
	}
	var rec struct {
		Type    string `json:"type"`
		Payload struct {
			Type      string          `json:"type"`
			Input     json.RawMessage `json:"input"`
			Arguments json.RawMessage `json:"arguments"`
			Action    json.RawMessage `json:"action"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &rec) != nil || rec.Type != "response_item" {
		return nil
	}
	switch rec.Payload.Type {
	case "custom_tool_call", "function_call", "local_shell_call":
	default:
		return nil
	}
	var out []string
	for _, field := range []json.RawMessage{rec.Payload.Input, rec.Payload.Arguments, rec.Payload.Action} {
		out = append(out, patchFilesIn(field)...)
	}
	return out
}

var copilotEditTools = map[string]bool{"edit": true, "create": true, "write": true, "str_replace_editor": true}

func copilotFiles(raw json.RawMessage) []string {
	if !bytes.Contains(raw, []byte("tool.execution_start")) {
		return nil
	}
	var rec struct {
		Type string `json:"type"`
		Data struct {
			ToolName  string          `json:"toolName"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &rec) != nil || rec.Type != "tool.execution_start" {
		return nil
	}
	if !copilotEditTools[rec.Data.ToolName] {
		return patchFilesIn(rec.Data.Arguments)
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(rec.Data.Arguments, &args) != nil {
		return nil
	}
	if rec.Data.ToolName == "str_replace_editor" && jsonString(args["command"]) == "view" {
		return nil
	}
	for _, key := range []string{"path", "file_path", "filePath"} {
		if p := jsonString(args[key]); p != "" {
			return []string{p}
		}
	}
	return nil
}

// patchFilesIn finds apply_patch envelopes in every string inside v. A
// string that itself holds JSON (function_call arguments are encoded that
// way) is decoded and searched too.
func patchFilesIn(v json.RawMessage) []string {
	if len(v) == 0 {
		return nil
	}
	var decoded any
	if json.Unmarshal(v, &decoded) != nil {
		return nil
	}
	var out []string
	var walk func(any, int)
	walk = func(x any, depth int) {
		if depth > 8 {
			return
		}
		switch t := x.(type) {
		case string:
			out = append(out, patchFiles(t)...)
			if s := strings.TrimSpace(t); strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
				var inner any
				if json.Unmarshal([]byte(s), &inner) == nil {
					walk(inner, depth+1)
				}
			}
		case []any:
			for _, y := range t {
				walk(y, depth+1)
			}
		case map[string]any:
			for _, y := range t {
				walk(y, depth+1)
			}
		}
	}
	walk(decoded, 0)
	return out
}

var patchHeaders = []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "}

// patchFiles returns the paths named by apply_patch file headers in s.
func patchFiles(s string) []string {
	if !strings.Contains(s, "*** ") {
		return nil
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		for _, h := range patchHeaders {
			if p, ok := strings.CutPrefix(line, h); ok {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
				break
			}
		}
	}
	return out
}

func jsonString(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func dedupe(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(paths))
	out := paths[:0]
	for _, p := range paths {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
