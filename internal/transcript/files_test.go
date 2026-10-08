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
		{"kimi has no extractor", ProviderKimi, `{"message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"a.go"}}]}}`, nil},
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
