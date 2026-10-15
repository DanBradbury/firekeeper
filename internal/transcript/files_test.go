package transcript

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTouchedFiles(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
		raw      string
		want     []string
	}{
		{"empty raw", ProviderClaude, ``, nil},
		{"not json", ProviderClaude, `"tool_use" {`, nil},
		{"raw is a string", ProviderCodex, `"*** Add File: a.go"`, nil},
		{"claude string content", ProviderClaude, `{"message":{"content":"tool_use Edit"}}`, nil},
		{"claude edit missing path", ProviderClaude, `{"message":{"content":[{"type":"tool_use","name":"Edit","input":{}}]}}`, nil},
		{"claude path not a string", ProviderClaude, `{"message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":7}}]}}`, nil},
		{"claude read is not a change", ProviderClaude, `{"message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}`, nil},
		{"claude several blocks deduped", ProviderClaude,
			`{"message":{"content":[{"type":"text","text":"x"},{"type":"tool_use","name":"Edit","input":{"file_path":"a.go"}},{"type":"tool_use","name":"Write","input":{"file_path":"a.go"}},{"type":"tool_use","name":"Write","input":{"file_path":"b.go"}}]}}`,
			[]string{"a.go", "b.go"}},
		{"codex patch in output is not a change", ProviderCodex,
			`{"type":"response_item","payload":{"type":"function_call_output","output":"*** Update File: a.go"}}`, nil},
		{"codex patch outside response_item", ProviderCodex,
			`{"type":"event_msg","payload":{"type":"custom_tool_call","input":"*** Update File: a.go"}}`, nil},
		{"codex CRLF and blank header", ProviderCodex,
			`{"type":"response_item","payload":{"type":"custom_tool_call","input":"*** Begin Patch\r\n*** Add File:   \r\n*** Update File: a.go\r\n*** End Patch"}}`,
			[]string{"a.go"}},
		{"codex undecodable arguments", ProviderCodex,
			`{"type":"response_item","payload":{"type":"function_call","arguments":"{*** Add File: a.go"}}`, nil},
		{"codex patch nested in arguments", ProviderCodex,
			`{"type":"response_item","payload":{"type":"function_call","arguments":"{\"cmd\":[\"bash\",\"-lc\",\"apply_patch <<EOF\\n*** Update File: a.go\\nEOF\"]}"}}`,
			[]string{"a.go"}},
		{"copilot arguments not an object", ProviderCopilot,
			`{"type":"tool.execution_start","data":{"toolName":"edit","arguments":"a.go"}}`, nil},
		{"copilot complete is not a start", ProviderCopilot,
			`{"type":"tool.execution_complete","data":{"toolName":"edit","arguments":{"path":"a.go"},"note":"tool.execution_start"}}`, nil},
		{"copilot file_path key", ProviderCopilot,
			`{"type":"tool.execution_start","data":{"toolName":"write","arguments":{"file_path":"a.go"}}}`, []string{"a.go"}},
		{"opencode edit, write, and patch", ProviderOpenCode,
			`{"type":"assistant","data":{"content":[{"type":"text","text":"x"},{"type":"tool","name":"edit","state":{"input":{"path":"a.go"}}},{"type":"tool","name":"write","state":{"input":{"filePath":"b.go"}}},{"type":"tool","name":"patch","state":{"input":{"patchText":"*** Begin Patch\n*** Update File: c.go\n*** End Patch"}}}]}}`,
			[]string{"a.go", "b.go", "c.go"}},
		{"opencode read is not a change", ProviderOpenCode,
			`{"type":"assistant","data":{"content":[{"type":"tool","name":"read","state":{"input":{"path":"a.go"}}}]}}`, nil},
		{"opencode tool in a user message", ProviderOpenCode,
			`{"type":"user","data":{"content":[{"type":"tool","name":"edit","state":{"input":{"path":"a.go"}}}]}}`, nil},
		{"opencode input still streaming", ProviderOpenCode,
			`{"type":"assistant","data":{"content":[{"type":"tool","name":"edit","state":{"status":"streaming","input":"{\"path\":\"a."}}]}}`, nil},
		{"kimi claude-shaped record", ProviderKimi, `{"message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"a.go"}}]}}`, nil},
		{"kimi edit loop event", ProviderKimi,
			`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"Edit:1","name":"Edit","args":{"path":"a.go","old_string":"x","new_string":"y"}}}`,
			[]string{"a.go"}},
		{"kimi write loop event", ProviderKimi,
			`{"type":"context.append_loop_event","event":{"type":"tool.call","name":"Write","args":{"path":"b.go","content":"x"}}}`, []string{"b.go"}},
		{"kimi patch through bash", ProviderKimi,
			`{"type":"context.append_loop_event","event":{"type":"tool.call","name":"Bash","args":{"command":"apply_patch <<EOF\n*** Update File: c.go\nEOF"}}}`,
			[]string{"c.go"}},
		{"kimi read is not a change", ProviderKimi,
			`{"type":"context.append_loop_event","event":{"type":"tool.call","name":"Read","args":{"path":"a.go"}}}`, nil},
		{"kimi tool result is not a change", ProviderKimi,
			`{"type":"context.append_loop_event","event":{"type":"tool.result","toolCallId":"Edit:1","name":"Edit","result":{"output":"tool.call Edit"}}}`, nil},
		{"kimi edit path not a string", ProviderKimi,
			`{"type":"context.append_loop_event","event":{"type":"tool.call","name":"Edit","args":{"path":7}}}`, nil},
		{"kimi migrated assistant tool calls", ProviderKimi,
			`{"type":"context.append_message","message":{"role":"assistant","content":[],"toolCalls":[{"id":"1","name":"Write","arguments":"{\"path\":\"d.go\",\"content\":\"x\"}"},{"id":"2","name":"Edit","arguments":"{\"path\":\"d.go\"}"},{"id":"3","name":"Read","arguments":"{\"path\":\"e.go\"}"}]}}`,
			[]string{"d.go"}},
		{"kimi user message tool calls", ProviderKimi,
			`{"type":"context.append_message","message":{"role":"user","toolCalls":[{"name":"Write","arguments":"{\"path\":\"a.go\"}"}]}}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Event{Provider: tt.provider, Raw: json.RawMessage(tt.raw)}
			if got := TouchedFiles(e); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
