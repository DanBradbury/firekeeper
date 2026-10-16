package opencode

import (
	"encoding/json"
	"strings"
)

// legacyRow converts a row of the legacy layout, where a message's content is
// in separate part rows, into the shape the parser reads for the current
// layout. The message role becomes the type, and a derived text, model, and
// content (text, reasoning, and tool parts) are added to data. The original
// part rows are kept in data.parts, so Raw loses nothing.
func legacyRow(r row) row {
	var data map[string]json.RawMessage
	if json.Unmarshal([]byte(r.Data), &data) != nil {
		return r // not JSON: the parser emits it as meta
	}
	var role string
	_ = json.Unmarshal(data["role"], &role)
	r.Type = role

	var parts []json.RawMessage
	_ = json.Unmarshal([]byte(r.Parts), &parts)
	var content []map[string]any
	var texts []string
	for _, raw := range parts {
		var p legacyPart
		if json.Unmarshal(raw, &p) != nil {
			continue
		}
		switch p.Type {
		case "text":
			if p.Text != "" && !p.Ignored {
				texts = append(texts, p.Text)
			}
			content = append(content, map[string]any{"type": "text", "text": p.Text})
		case "reasoning":
			content = append(content, map[string]any{"type": "reasoning", "text": p.Text})
		case "tool":
			state := map[string]any{"status": p.State.Status, "input": p.State.Input}
			if p.State.Output != "" {
				state["content"] = []map[string]string{{"type": "text", "text": p.State.Output}}
			}
			if msg := errorText(p.State.Error); msg != "" {
				state["error"] = map[string]string{"message": msg}
			}
			content = append(content, map[string]any{"type": "tool", "name": p.Tool, "state": state})
		}
	}

	set := func(key string, value any) {
		if b, err := json.Marshal(value); err == nil {
			data[key] = b
		}
	}
	if role == "user" {
		set("text", strings.Join(texts, "\n"))
		var m struct {
			ModelID string `json:"modelID"`
		}
		_ = json.Unmarshal(data["model"], &m)
		set("model", map[string]string{"id": m.ModelID})
	} else {
		var id string
		_ = json.Unmarshal(data["modelID"], &id)
		set("model", map[string]string{"id": id})
	}
	set("content", content)
	data["parts"] = json.RawMessage(r.Parts)
	if out, err := json.Marshal(data); err == nil {
		r.Data = string(out)
	}
	r.Parts = ""
	return r
}

type legacyPart struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Ignored bool   `json:"ignored"`
	Tool    string `json:"tool"`
	State   struct {
		Status string          `json:"status"`
		Input  json.RawMessage `json:"input"`
		Output string          `json:"output"`
		Error  json.RawMessage `json:"error"`
	} `json:"state"`
}

// errorText reads a tool error, a string in the legacy layout.
func errorText(raw json.RawMessage) string {
	var text string
	if len(raw) > 0 && json.Unmarshal(raw, &text) == nil {
		return text
	}
	return ""
}
