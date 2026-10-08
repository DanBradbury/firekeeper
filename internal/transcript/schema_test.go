package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

const schemaPath = "../../docs/event.schema.json"

var (
	timeType     = reflect.TypeOf(time.Time{})
	rawType      = reflect.TypeOf(json.RawMessage{})
	providerType = reflect.TypeOf(Provider(""))
	roleType     = reflect.TypeOf(Role(""))
)

func loadSchema(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.FromSlash(schemaPath))
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	return schema
}

func TestEventMatchesJSONSchema(t *testing.T) {
	schema := loadSchema(t)
	if got := schema["$schema"]; got != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %v, want draft 2020-12", got)
	}
	for _, problem := range schemaDrift("event", reflect.TypeOf(Event{}), schema) {
		t.Error(problem)
	}
}

func TestSchemaDriftDetected(t *testing.T) {
	mutations := map[string]func(schema map[string]any){
		"missing property": func(s map[string]any) {
			delete(props(s), "model")
		},
		"extra property": func(s map[string]any) {
			props(s)["cost"] = map[string]any{"type": "number"}
		},
		"wrong type": func(s map[string]any) {
			props(s)["seq"].(map[string]any)["type"] = "string"
		},
		"not nullable": func(s map[string]any) {
			props(s)["ts"].(map[string]any)["type"] = "string"
		},
		"role enum": func(s map[string]any) {
			props(s)["role"].(map[string]any)["enum"] = []any{"user", "assistant"}
		},
		"provider enum": func(s map[string]any) {
			props(s)["provider"].(map[string]any)["enum"] = []any{"codex", "copilot", "kimi", "claude", "gemini"}
		},
		"nested tokens": func(s map[string]any) {
			delete(props(props(s)["tokens"].(map[string]any)), "cache")
		},
		"not required": func(s map[string]any) {
			s["required"] = []any{"machine_id", "session_id"}
		},
		"open object": func(s map[string]any) {
			s["additionalProperties"] = true
		},
		"raw typed": func(s map[string]any) {
			props(s)["raw"].(map[string]any)["type"] = "object"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			schema := loadSchema(t)
			mutate(schema)
			if problems := schemaDrift("event", reflect.TypeOf(Event{}), schema); len(problems) == 0 {
				t.Fatal("drift not detected")
			}
		})
	}
}

func TestEventJSONKeysMatchSchema(t *testing.T) {
	model := "gpt-test"
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	data, err := json.Marshal(Event{Provider: ProviderCodex, Role: RoleUser, TS: &ts, Model: &model, Raw: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var encoded map[string]any
	if err := json.Unmarshal(data, &encoded); err != nil {
		t.Fatal(err)
	}
	if got, want := sortedKeys(encoded), sortedKeys(props(loadSchema(t))); !reflect.DeepEqual(got, want) {
		t.Fatalf("encoded keys %v, schema properties %v", got, want)
	}
	if encoded["tool_name"] != nil {
		t.Errorf("nil tool_name encoded as %v, want null", encoded["tool_name"])
	}
	if encoded["ts"] != "2026-01-02T03:04:05Z" {
		t.Errorf("ts encoded as %v, want RFC3339", encoded["ts"])
	}
}

func TestEventValidate(t *testing.T) {
	valid := Event{Provider: ProviderKimi, Role: RoleMeta, Raw: json.RawMessage(`{"a":1}`)}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid event: %v", err)
	}
	tests := map[string]func(e *Event){
		"provider": func(e *Event) { e.Provider = "other" },
		"role":     func(e *Event) { e.Role = "bot" },
		"seq":      func(e *Event) { e.Seq = -1 },
		"tokens":   func(e *Event) { e.Tokens.Output = -1 },
		"raw":      func(e *Event) { e.Raw = json.RawMessage(`{`) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			e := valid
			mutate(&e)
			if err := e.Validate(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// schemaDrift compares the JSON encoding of typ with a schema node and returns
// one message per mismatch. Descriptions and titles are not compared.
func schemaDrift(path string, typ reflect.Type, node map[string]any) []string {
	var problems []string
	fail := func(format string, args ...any) {
		problems = append(problems, path+": "+fmt.Sprintf(format, args...))
	}

	nullable := false
	if typ.Kind() == reflect.Pointer {
		nullable = true
		typ = typ.Elem()
	}

	if typ == rawType {
		if _, ok := node["type"]; ok {
			fail("raw must accept any JSON value, schema sets type %v", node["type"])
		}
		return problems
	}

	var wantType string
	switch {
	case typ == timeType:
		wantType = "string"
		if node["format"] != "date-time" {
			fail("format = %v, want date-time", node["format"])
		}
	case typ.Kind() == reflect.String:
		wantType = "string"
	case typ.Kind() == reflect.Int64:
		wantType = "integer"
		if node["minimum"] != float64(0) {
			fail("minimum = %v, want 0", node["minimum"])
		}
	case typ.Kind() == reflect.Struct:
		wantType = "object"
	default:
		fail("no schema mapping for Go type %s; extend schemaDrift", typ)
		return problems
	}
	var want any = wantType
	if nullable {
		want = []any{wantType, "null"}
	}
	if !reflect.DeepEqual(node["type"], want) {
		fail("type = %v, want %v", node["type"], want)
	}

	switch typ {
	case providerType:
		var values []any
		for _, p := range Providers() {
			values = append(values, string(p))
		}
		if !reflect.DeepEqual(node["enum"], values) {
			fail("enum = %v, want %v", node["enum"], values)
		}
	case roleType:
		var values []any
		for _, r := range Roles() {
			values = append(values, string(r))
		}
		if !reflect.DeepEqual(node["enum"], values) {
			fail("enum = %v, want %v", node["enum"], values)
		}
	default:
		if _, ok := node["enum"]; ok {
			fail("unexpected enum %v", node["enum"])
		}
	}

	if typ.Kind() != reflect.Struct || typ == timeType {
		return problems
	}
	if node["additionalProperties"] != false {
		fail("additionalProperties = %v, want false", node["additionalProperties"])
	}
	properties := props(node)
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name, opts, _ := strings.Cut(field.Tag.Get("json"), ",")
		if !field.IsExported() || name == "-" {
			continue
		}
		if name == "" || opts != "" {
			fail("field %s needs a plain json tag so every key is always present", field.Name)
			continue
		}
		fields = append(fields, name)
		child, ok := properties[name].(map[string]any)
		if !ok {
			fail("schema missing property %q", name)
			continue
		}
		problems = append(problems, schemaDrift(path+"."+name, field.Type, child)...)
	}
	sort.Strings(fields)
	if got := sortedKeys(properties); !reflect.DeepEqual(got, fields) {
		fail("schema properties %v, Go fields %v", got, fields)
	}
	var required []string
	if list, ok := node["required"].([]any); ok {
		for _, item := range list {
			required = append(required, fmt.Sprint(item))
		}
	}
	sort.Strings(required)
	if !reflect.DeepEqual(required, fields) {
		fail("required %v, want every field %v", required, fields)
	}
	return problems
}

func props(node map[string]any) map[string]any {
	properties, _ := node["properties"].(map[string]any)
	return properties
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
