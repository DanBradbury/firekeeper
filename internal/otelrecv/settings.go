package otelrecv

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ManagedEnv are the Claude Code settings env keys this command owns.
// User settings at ~/.claude/settings.json are honored for telemetry.
// Project settings cannot turn it on. See
// https://code.claude.com/docs/en/monitoring-usage
func ManagedEnv(port int) map[string]string {
	if port <= 0 {
		port = DefaultPort
	}
	return map[string]string{
		"CLAUDE_CODE_ENABLE_TELEMETRY": "1",
		"OTEL_LOGS_EXPORTER":           "otlp",
		"OTEL_METRICS_EXPORTER":        "none",
		"OTEL_EXPORTER_OTLP_PROTOCOL":  "http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT":  fmt.Sprintf("http://127.0.0.1:%d", port),
		"OTEL_LOGS_EXPORT_INTERVAL":    "5000",
	}
}

// ManagedEnvKeys is the stable list of keys Enable writes and Disable removes.
func ManagedEnvKeys() []string {
	return []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY",
		"OTEL_LOGS_EXPORTER",
		"OTEL_METRICS_EXPORTER",
		"OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_LOGS_EXPORT_INTERVAL",
	}
}

// EnableClaudeEnv writes ManagedEnv into dir/settings.json, preserving every
// other key. The first change saves settings.json.firekeeper-backup. A
// second call with the same values does not rewrite the file.
func EnableClaudeEnv(dir string, port int) (changed bool, err error) {
	return editClaudeEnv(dir, func(env map[string]json.RawMessage) bool {
		want := ManagedEnv(port)
		same := true
		for key, value := range want {
			raw, mErr := json.Marshal(value)
			if mErr != nil {
				return false
			}
			if string(env[key]) != string(raw) {
				same = false
			}
			env[key] = raw
		}
		return !same
	})
}

// DisableClaudeEnv removes only the keys EnableClaudeEnv wrote.
func DisableClaudeEnv(dir string) (changed bool, err error) {
	return editClaudeEnv(dir, func(env map[string]json.RawMessage) bool {
		changed := false
		for _, key := range ManagedEnvKeys() {
			if _, ok := env[key]; ok {
				delete(env, key)
				changed = true
			}
		}
		return changed
	})
}

// ClaudeEnv reports whether every managed key is set to the values for port.
func ClaudeEnv(dir string, port int) (bool, error) {
	doc, err := readSettings(filepath.Join(dir, "settings.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	env, err := envMap(doc["env"])
	if err != nil {
		return false, err
	}
	want := ManagedEnv(port)
	for key, value := range want {
		raw, err := json.Marshal(value)
		if err != nil {
			return false, err
		}
		if string(env[key]) != string(raw) {
			return false, nil
		}
	}
	return true, nil
}

func editClaudeEnv(dir string, mutate func(map[string]json.RawMessage) bool) (bool, error) {
	if dir == "" {
		return false, errors.New("otel: no Claude config directory")
	}
	path := filepath.Join(dir, "settings.json")
	doc, err := readSettings(path)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return false, err
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	env, err := envMap(doc["env"])
	if err != nil {
		return false, err
	}
	if !mutate(env) {
		return false, nil
	}
	if !missing {
		if err := backupOnce(path); err != nil {
			return false, err
		}
	}
	if len(env) == 0 {
		delete(doc, "env")
	} else {
		raw, err := json.Marshal(env)
		if err != nil {
			return false, err
		}
		doc["env"] = raw
	}
	if missing && len(doc) == 0 {
		return false, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, errors.New("otel: create Claude config directory")
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, err
	}
	body = append(body, '\n')
	tmp, err := os.CreateTemp(dir, ".settings-*.json")
	if err != nil {
		return false, errors.New("otel: write Claude settings")
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return false, errors.New("otel: write Claude settings")
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return false, errors.New("otel: write Claude settings")
	}
	if err := tmp.Close(); err != nil {
		return false, errors.New("otel: write Claude settings")
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, errors.New("otel: write Claude settings")
	}
	return true, nil
}

func readSettings(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, errors.New("otel: Claude settings are not a JSON object")
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	return doc, nil
}

func envMap(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, errors.New("otel: Claude settings env is not an object")
	}
	if env == nil {
		env = map[string]json.RawMessage{}
	}
	return env, nil
}

func backupOnce(path string) error {
	backup := path + ".firekeeper-backup"
	if _, err := os.Stat(backup); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("otel: read Claude settings backup")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("otel: read Claude settings")
	}
	if err := os.WriteFile(backup, data, 0o600); err != nil {
		return errors.New("otel: write Claude settings backup")
	}
	return nil
}
