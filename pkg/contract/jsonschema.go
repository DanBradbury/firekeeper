package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// This file is a deliberately small JSON Schema validator: just enough of
// draft 2020-12 to check docs/event.schema.json, with no dependency outside
// the standard library. It supports type (single or list), enum, required,
// properties, additionalProperties (boolean), minimum and format "date-time".
// A schema using any other validation keyword is rejected when it is
// compiled, so extending the schema without extending this validator fails a
// test instead of silently validating less.

// annotations never affect validation.
var annotations = map[string]bool{
	"$schema": true, "$id": true, "title": true, "description": true,
}

var supported = map[string]bool{
	"type": true, "enum": true, "required": true, "properties": true,
	"additionalProperties": true, "minimum": true, "format": true,
}

type schemaNode struct {
	types      []string
	enum       []any
	hasEnum    bool
	required   []string
	properties map[string]*schemaNode
	// closed is true when additionalProperties is false.
	closed     bool
	minimum    *float64
	dateTime   bool
	hasMinimum bool
}

func compileSchema(data []byte) (*schemaNode, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("schema is not a JSON object: %w", err)
	}
	return compileNode("#", raw)
}

func compileNode(path string, raw map[string]any) (*schemaNode, error) {
	n := &schemaNode{}
	for key, value := range raw {
		if annotations[key] {
			continue
		}
		if !supported[key] {
			return nil, fmt.Errorf("%s: unsupported schema keyword %q", path, key)
		}
		switch key {
		case "type":
			switch v := value.(type) {
			case string:
				n.types = []string{v}
			case []any:
				for _, item := range v {
					s, ok := item.(string)
					if !ok {
						return nil, fmt.Errorf("%s: type list must hold strings", path)
					}
					n.types = append(n.types, s)
				}
			default:
				return nil, fmt.Errorf("%s: bad type", path)
			}
		case "enum":
			list, ok := value.([]any)
			if !ok {
				return nil, fmt.Errorf("%s: enum must be a list", path)
			}
			n.enum, n.hasEnum = list, true
		case "required":
			list, ok := value.([]any)
			if !ok {
				return nil, fmt.Errorf("%s: required must be a list", path)
			}
			for _, item := range list {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("%s: required must hold strings", path)
				}
				n.required = append(n.required, s)
			}
		case "properties":
			props, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s: properties must be an object", path)
			}
			n.properties = map[string]*schemaNode{}
			for name, sub := range props {
				subMap, ok := sub.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%s.%s: property schema must be an object", path, name)
				}
				child, err := compileNode(path+"."+name, subMap)
				if err != nil {
					return nil, err
				}
				n.properties[name] = child
			}
		case "additionalProperties":
			b, ok := value.(bool)
			if !ok {
				return nil, fmt.Errorf("%s: additionalProperties must be a boolean", path)
			}
			n.closed = !b
		case "minimum":
			f, ok := value.(float64)
			if !ok {
				return nil, fmt.Errorf("%s: minimum must be a number", path)
			}
			n.minimum, n.hasMinimum = &f, true
		case "format":
			if value != "date-time" {
				return nil, fmt.Errorf("%s: unsupported format %v", path, value)
			}
			n.dateTime = true
		}
	}
	return n, nil
}

// decodeJSON parses exactly one JSON value, keeping numbers exact.
func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("malformed JSON: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("malformed JSON: trailing data")
	}
	return v, nil
}

// validate returns one message per violation. Messages name paths and rules,
// never the offending values, so a transcript never leaks into an error.
func (n *schemaNode) validate(path string, v any) []string {
	var problems []string
	if len(n.types) > 0 {
		ok := false
		for _, t := range n.types {
			if matchesType(t, v) {
				ok = true
				break
			}
		}
		if !ok {
			return []string{fmt.Sprintf("%s: want type %s, got %s", path, strings.Join(n.types, "|"), jsonKind(v))}
		}
	}
	if n.hasEnum {
		found := false
		for _, e := range n.enum {
			if e == v {
				found = true
				break
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf("%s: not one of the allowed values", path))
		}
	}
	switch val := v.(type) {
	case string:
		if n.dateTime {
			if _, err := time.Parse(time.RFC3339, val); err != nil {
				problems = append(problems, fmt.Sprintf("%s: not an RFC 3339 date-time", path))
			}
		}
	case json.Number:
		if n.hasMinimum {
			if f, err := val.Float64(); err != nil || f < *n.minimum {
				problems = append(problems, fmt.Sprintf("%s: below minimum %v", path, *n.minimum))
			}
		}
	case map[string]any:
		for _, name := range n.required {
			if _, ok := val[name]; !ok {
				problems = append(problems, fmt.Sprintf("%s: missing required property %q", path, name))
			}
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if child, ok := n.properties[k]; ok {
				problems = append(problems, child.validate(path+"."+k, val[k])...)
			} else if n.closed {
				problems = append(problems, fmt.Sprintf("%s: unknown property %q", path, k))
			}
		}
	}
	return problems
}

func matchesType(t string, v any) bool {
	switch t {
	case "null":
		return v == nil
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "integer":
		num, ok := v.(json.Number)
		if !ok {
			return false
		}
		if _, err := strconv.ParseInt(num.String(), 10, 64); err == nil {
			return true
		}
		f, err := num.Float64()
		return err == nil && f == math.Trunc(f) && !math.IsInf(f, 0)
	}
	return false
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	return "unknown"
}
