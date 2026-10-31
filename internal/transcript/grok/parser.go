package grok

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

const maxToolText = 64 << 10

type update struct {
	Kind       string          `json:"sessionUpdate"`
	Content    json.RawMessage `json:"content"`
	ToolID     string          `json:"toolCallId"`
	Title      string          `json:"title"`
	Status     string          `json:"status"`
	Input      json.RawMessage `json:"rawInput"`
	Output     json.RawMessage `json:"rawOutput"`
	Model      string          `json:"model"`
	ModelID    string          `json:"model_id"`
	NewModelID string          `json:"new_model_id"`
	Summary    string          `json:"session_summary"`
	Result     string          `json:"agent_result"`
	Usage      *promptUsage    `json:"usage"`
	Meta       struct {
		Tool struct {
			Name string `json:"name"`
		} `json:"x.ai/tool"`
	} `json:"_meta"`
}

type promptUsage struct {
	Input  int64 `json:"inputTokens"`
	Output int64 `json:"outputTokens"`
	Read   int64 `json:"cachedReadTokens"`
	Write  int64 `json:"cacheCreationTokens"`
}

type parser struct {
	sessionID, model string
	seq              int64
	tools            map[string]string
}

func (p *parser) parse(line []byte) transcript.Event {
	e := transcript.Event{Provider: transcript.ProviderGrok, SessionID: p.sessionID, Seq: p.seq, Role: transcript.RoleMeta, Raw: rawJSON(line)}
	u, err := session.DecodeGrokUpdate(line)
	if err != nil || (p.sessionID != "" && !strings.EqualFold(u.SessionID, p.sessionID)) {
		return e
	}
	if p.sessionID == "" {
		p.sessionID = u.SessionID
	}
	e.SessionID = p.sessionID
	if !u.Timestamp.IsZero() {
		ts := u.Timestamp
		e.TS = &ts
	}
	var v update
	if json.Unmarshal(u.Update, &v) != nil {
		return e
	}
	isXai := u.Method == "_x.ai/session/update"
	if isXai {
		switch v.Kind {
		case "response_started":
			if v.Model != "" {
				p.model = v.Model
			}
		case "model_changed":
			if v.ModelID != "" {
				p.model = v.ModelID
			}
		case "model_auto_switched":
			if v.NewModelID != "" {
				p.model = v.NewModelID
			}
		case "session_summary_generated":
			e.Text = v.Summary
		case "turn_completed":
			// Usage on intermediate response/turn_usage events overlaps this
			// authoritative bill. Keep those records but count only this one.
			if v.Usage != nil {
				usage := v.Usage
				if usage.Input < 0 || usage.Output < 0 || usage.Read < 0 || usage.Write < 0 ||
					usage.Read > usage.Input || usage.Write > usage.Input-usage.Read ||
					usage.Input > math.MaxInt64-usage.Output {
					e.Text = "[invalid token usage recorded by Grok]"
				} else {
					cache := usage.Read + usage.Write
					e.Tokens = transcript.Tokens{Input: usage.Input - cache, Output: usage.Output, Cache: cache}
				}
			}
			// agent_result may repeat previously streamed assistant text;
			// this terminal notification remains one metadata event.
			if e.Text == "" {
				e.Text = v.Result
			}
		case "plan_kept":
			e.Text = contentText(v.Content)
		}
		e.Model = optional(p.model)
		return e
	}
	switch v.Kind {
	case "user_message_chunk":
		e.Role, e.Text = transcript.RoleUser, contentText(v.Content)
	case "agent_message_chunk":
		e.Role, e.Text = transcript.RoleAssistant, contentText(v.Content)
	case "agent_thought_chunk":
		e.Text = contentText(v.Content)
	case "tool_call", "tool_call_update":
		if name := v.Meta.Tool.Name; name != "" && v.ToolID != "" {
			p.tools[v.ToolID] = name
		}
		e.ToolName = optional(p.tools[v.ToolID])
		switch {
		case v.Kind == "tool_call_update" && (v.Status == "completed" || v.Status == "failed" || present(v.Output) || present(v.Content)):
			e.Role = transcript.RoleToolResult
			e.Text = contentText(v.Content)
			if e.Text == "" && present(v.Output) {
				e.Text = jsonText(v.Output)
			}
			e.Text = truncate(e.Text)
		case v.Kind == "tool_call":
			e.Role = transcript.RoleToolCall
			if present(v.Input) {
				e.Text = jsonText(v.Input)
			} else {
				e.Text = v.Title
			}
		}
	case "plan":
		var plan struct {
			Entries []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"entries"`
		}
		if json.Unmarshal(u.Update, &plan) == nil {
			var lines []string
			for _, entry := range plan.Entries {
				lines = append(lines, strings.TrimSpace(entry.Status+": "+entry.Content))
			}
			e.Text = strings.Join(lines, "\n")
		}
	}
	e.Model = optional(p.model)
	return e
}

func present(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte("[]"))
}

// ACP ContentBlock, ToolCallContent and their arrays share these text/diff
// projections. Non-text media and terminal references stay intact in Raw.
func contentText(raw json.RawMessage) string {
	if !present(raw) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		var texts []string
		for _, block := range list {
			if text := contentText(block); text != "" {
				texts = append(texts, text)
			}
		}
		return strings.Join(texts, "\n")
	}
	var block struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
		Path    string          `json:"path"`
		OldText string          `json:"oldText"`
		NewText string          `json:"newText"`
	}
	if json.Unmarshal(raw, &block) != nil {
		return ""
	}
	switch block.Type {
	case "text":
		return block.Text
	case "content":
		return contentText(block.Content)
	case "diff":
		return strings.TrimSpace(strings.Join([]string{block.Path, block.OldText, block.NewText}, "\n"))
	}
	return ""
}

func jsonText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var out bytes.Buffer
	if json.Compact(&out, raw) == nil {
		return out.String()
	}
	return ""
}

func optional(text string) *string {
	if text == "" {
		return nil
	}
	return &text
}

func truncate(text string) string {
	if len(text) <= maxToolText {
		return text
	}
	cut := maxToolText
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return fmt.Sprintf("%s\n[truncated %d bytes]", text[:cut], len(text)-cut)
}
