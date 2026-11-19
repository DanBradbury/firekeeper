package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/sqlitejson"
)

// harness is one agent CLI that can run the task headless with a chosen
// OpenAI model, and whose local records hold per-request token usage.
type harness interface {
	Name() string
	// Provider is the `firekeeper window --provider` name for the cross-check.
	Provider() string
	// Command returns the argv and stdin for one run in repo.
	Command(o options, effort, repo, prompt string) (argv []string, stdin string)
	// Env is extra environment for the run.
	Env(o options) []string
	// Collect finds the run's session from its stdout and reads its usage.
	Collect(o options, stdout []byte, start time.Time) (RunData, error)
	// NotLogged names the fields this harness does not record, shown as n/a.
	NotLogged() []string
}

// Field names used in NotLogged and the n/a cells.
const (
	fieldEffort     = "effort"
	fieldCacheWrite = "cache_write"
	fieldReasoning  = "reasoning"
	fieldWindow     = "window"
)

func harnessFor(name string) (harness, error) {
	switch name {
	case "codex":
		return codexHarness{}, nil
	case "opencode":
		return opencodeHarness{}, nil
	case "cursor":
		// Verified in Cursor's CLI docs and Firekeeper's Cursor notes:
		// `cursor-agent -p --model <m> --output-format json|stream-json` runs
		// headless, but its JSON carries no token usage and Cursor's local
		// transcripts and chat databases hold no token counts either, so there
		// is nothing to measure. TODO: add it if Cursor starts logging usage.
		return nil, errors.New("--harness cursor is not supported: Cursor CLI runs headless (-p) but neither its JSON output nor its local transcripts record token usage, so there is nothing to measure")
	}
	return nil, fmt.Errorf("unknown harness %q (codex or opencode)", name)
}

// codexHarness runs `codex exec`. Verified flags (developers.openai.com/codex
// CLI reference and non-interactive docs): --json, -m, -c key=value,
// --sandbox, --ignore-user-config, -C, and "-" to read the prompt on stdin.
type codexHarness struct{}

func (codexHarness) Name() string        { return "codex" }
func (codexHarness) Provider() string    { return "codex" }
func (codexHarness) NotLogged() []string { return nil }

func (codexHarness) Command(o options, effort, repo, prompt string) ([]string, string) {
	argv := []string{o.codex, "exec", "--json"}
	if o.model != "" {
		argv = append(argv, "-m", o.model)
	}
	argv = append(argv, "-c", "model_reasoning_effort="+effort, "--sandbox", "workspace-write")
	if !o.userConfig {
		argv = append(argv, "--ignore-user-config")
	}
	argv = append(argv, o.codexArgs...)
	return append(argv, "-C", repo, "-"), prompt
}

func (codexHarness) Env(o options) []string { return []string{"CODEX_HOME=" + o.codexHome} }

func (codexHarness) Collect(o options, stdout []byte, start time.Time) (RunData, error) {
	id, _ := ThreadIDFromExecJSON(bytes.NewReader(stdout))
	path, err := FindRollout(o.codexHome, id)
	if err != nil {
		path, err = newestRollout(o.codexHome, start)
	}
	if err != nil {
		return RunData{}, fmt.Errorf("find rollout: %w", err)
	}
	return ParseRolloutFile(path)
}

// newestRollout is the fallback when the thread id is unknown: the newest
// rollout written since the run started.
func newestRollout(codexHome string, since time.Time) (string, error) {
	matches, _ := filepath.Glob(filepath.Join(codexHome, "sessions", "*", "*", "*", "rollout-*.jsonl"))
	best, bestAt := "", since.Add(-time.Second)
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.ModTime().After(bestAt) {
			best, bestAt = m, info.ModTime()
		}
	}
	if best == "" {
		return "", errors.New("no rollout written during the run")
	}
	return best, nil
}

// opencodeHarness runs `opencode run`. Verified in OpenCode's source for the
// v2 CLI (anomalyco/opencode v2.3.2, packages/cli/src/commands/commands.ts
// and run/): --format json prints JSON events that each carry sessionID,
// --model takes provider/model#variant (the variant is the reasoning effort),
// --auto approves permissions that are not explicitly denied, and the run
// uses the current directory. Usage is read from the session_message rows of
// OpenCode's database (the session_v2 schema Firekeeper's OpenCode reader
// supports), via Firekeeper's database locator and sqlite3 runner.
type opencodeHarness struct{}

func (opencodeHarness) Name() string     { return "opencode" }
func (opencodeHarness) Provider() string { return "opencode" }

// OpenCode does not log Codex's 5-hour window.
func (opencodeHarness) NotLogged() []string { return []string{fieldWindow} }

func (opencodeHarness) Command(o options, effort, repo, prompt string) ([]string, string) {
	model := o.model
	if !strings.Contains(model, "/") {
		model = "openai/" + model
	}
	argv := []string{o.opencode, "run", "--format", "json", "--model", model + "#" + effort, "--auto"}
	argv = append(argv, o.opencodeArgs...)
	return append(argv, strings.TrimSpace(prompt)), ""
}

func (opencodeHarness) Env(options) []string { return nil }

func (opencodeHarness) Collect(_ options, stdout []byte, _ time.Time) (RunData, error) {
	id := opencodeSessionID(stdout)
	if !session.ValidOpenCodeSessionID(id) {
		return RunData{}, errors.New("no OpenCode session id in the JSON output")
	}
	db, err := session.OpenCodeDatabase("")
	if err != nil || db == "" {
		return RunData{}, errors.New("OpenCode database not found (OPENCODE_DB is :memory:?)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	q := func(query string) ([]byte, error) { return sqlitejson.Run(ctx, "-readonly", "-json", db, query) }
	meta, err := q("SELECT model, version FROM session_v2 WHERE id = '" + id + "'")
	if err != nil {
		return RunData{}, errors.New("read OpenCode session (needs OpenCode 2.x, whose database has session_v2)")
	}
	rows, err := q("SELECT seq, time_created, data FROM session_message WHERE session_id = '" + id +
		"' AND type IN ('assistant', 'compaction') ORDER BY seq")
	if err != nil {
		return RunData{}, errors.New("read OpenCode messages")
	}
	return parseOpenCode(id, meta, rows)
}

func opencodeSessionID(stdout []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var ev struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.SessionID != "" {
			return ev.SessionID
		}
	}
	return ""
}

// parseOpenCode maps OpenCode rows onto RunData. OpenCode counts input
// without cache reads or writes and reasoning apart from output; RunData
// uses OpenAI's shape, so input = input + cache read + cache write and
// output = output + reasoning. One assistant message is one request.
func parseOpenCode(id string, metaJSON, rowsJSON []byte) (RunData, error) {
	d := RunData{SessionID: id}
	var meta []struct {
		Model   *string `json:"model"`
		Version string  `json:"version"`
	}
	if len(bytes.TrimSpace(metaJSON)) > 0 {
		if err := json.Unmarshal(metaJSON, &meta); err != nil {
			return d, errors.New("decode OpenCode session row")
		}
	}
	if len(meta) == 0 {
		return d, errors.New("OpenCode session not in the database")
	}
	d.Version = meta[0].Version
	if meta[0].Model != nil {
		var m struct {
			ID      string `json:"id"`
			Variant string `json:"variant"`
		}
		if json.Unmarshal([]byte(*meta[0].Model), &m) == nil {
			if m.ID != "" {
				d.Models = []string{m.ID}
			}
			if m.Variant != "" {
				d.Efforts = []string{m.Variant}
			}
		}
	}
	var rows []struct {
		TimeCreated int64  `json:"time_created"`
		Data        string `json:"data"`
	}
	if len(bytes.TrimSpace(rowsJSON)) > 0 {
		if err := json.Unmarshal(rowsJSON, &rows); err != nil {
			return d, errors.New("decode OpenCode message rows")
		}
	}
	for _, row := range rows {
		var msg struct {
			Model *struct {
				ID      string `json:"id"`
				Variant string `json:"variant"`
			} `json:"model"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
			Tokens *struct {
				Input     float64 `json:"input"`
				Output    float64 `json:"output"`
				Reasoning float64 `json:"reasoning"`
				Cache     struct {
					Read  float64 `json:"read"`
					Write float64 `json:"write"`
				} `json:"cache"`
			} `json:"tokens"`
		}
		if json.Unmarshal([]byte(row.Data), &msg) != nil || msg.Tokens == nil {
			continue
		}
		for _, c := range msg.Content {
			if c.Type == "tool" {
				d.ToolCalls++
			}
		}
		if msg.Model != nil && msg.Model.Variant != "" && len(d.Efforts) == 0 {
			d.Efforts = []string{msg.Model.Variant}
		}
		t := msg.Tokens
		u := Usage{
			Cached:     int64(t.Cache.Read),
			CacheWrite: int64(t.Cache.Write),
			Reasoning:  int64(t.Reasoning),
			Output:     int64(t.Output + t.Reasoning),
		}
		u.Input = int64(t.Input) + u.Cached + u.CacheWrite
		u.Total = u.Input + u.Output
		d.Requests = append(d.Requests, Request{At: time.UnixMilli(row.TimeCreated).UTC(), Usage: u})
	}
	if len(d.Requests) == 0 {
		return d, errors.New("OpenCode session has no token usage yet")
	}
	return d, nil
}
