package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Usage is one usage object in OpenAI's shape: input_tokens includes cache
// reads and cache writes, output_tokens includes reasoning, and
// total_tokens = input_tokens + output_tokens.
type Usage struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_output_tokens"`
	Total      int64 `json:"total_tokens"`
}

func (u *Usage) add(o Usage) {
	u.Input += o.Input
	u.Cached += o.Cached
	u.CacheWrite += o.CacheWrite
	u.Output += o.Output
	u.Reasoning += o.Reasoning
	u.Total += o.Total
}

// Request is one model response's usage.
type Request struct {
	At time.Time `json:"at"`
	Usage
}

// RunData is what a harness's local records say about one session.
type RunData struct {
	SessionID string
	Version   string
	Models    []string // distinct models, in order
	Efforts   []string // distinct recorded efforts, in order
	Requests  []Request
	// Final is the harness's own running total when it keeps one (Codex);
	// it should equal the sum of Requests.
	Final     *Usage
	ToolCalls int
	// UsedPercentStart/End are the 5-hour window's used_percent at the first
	// and last report in the session; ResetsAt is when that window resets.
	UsedPercentStart, UsedPercentEnd *float64
	ResetsAt                         *time.Time
}

// Sum adds the requests up.
func (d RunData) Sum() Usage {
	var u Usage
	for _, r := range d.Requests {
		u.add(r.Usage)
	}
	return u
}

type rolloutLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type rolloutPayload struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	CLIVersion string `json:"cli_version"`
	Model      string `json:"model"`
	Effort     string `json:"effort"`
	Info       *struct {
		Total *Usage `json:"total_token_usage"`
		Last  *Usage `json:"last_token_usage"`
	} `json:"info"`
	RateLimits *struct {
		Primary *struct {
			UsedPercent float64 `json:"used_percent"`
			ResetsAt    *int64  `json:"resets_at"`
		} `json:"primary"`
	} `json:"rate_limits"`
}

// ParseRollout reads a Codex rollout JSONL file. Each token_count event with
// last_token_usage is one request, the same rule Firekeeper uses. Unknown and
// malformed lines are skipped; no text is kept.
func ParseRollout(r io.Reader) (RunData, error) {
	var d RunData
	seenModel := map[string]bool{}
	seenEffort := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var line rolloutLine
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		var p rolloutPayload
		if len(line.Payload) > 0 && json.Unmarshal(line.Payload, &p) != nil {
			continue
		}
		ts, _ := time.Parse(time.RFC3339Nano, line.Timestamp)
		switch line.Type {
		case "session_meta":
			if d.SessionID == "" {
				d.SessionID = p.ID
				d.Version = p.CLIVersion
			}
		case "turn_context":
			if p.Model != "" && !seenModel[p.Model] {
				seenModel[p.Model] = true
				d.Models = append(d.Models, p.Model)
			}
			if p.Effort != "" && !seenEffort[p.Effort] {
				seenEffort[p.Effort] = true
				d.Efforts = append(d.Efforts, p.Effort)
			}
		case "response_item":
			if p.Type == "function_call" || p.Type == "custom_tool_call" {
				d.ToolCalls++
			}
		case "event_msg":
			if p.Type != "token_count" {
				continue
			}
			if p.RateLimits != nil && p.RateLimits.Primary != nil {
				v := p.RateLimits.Primary.UsedPercent
				if d.UsedPercentStart == nil {
					d.UsedPercentStart = &v
				}
				d.UsedPercentEnd = &v
				if p.RateLimits.Primary.ResetsAt != nil {
					t := time.Unix(*p.RateLimits.Primary.ResetsAt, 0).UTC()
					d.ResetsAt = &t
				}
			}
			if p.Info == nil || p.Info.Last == nil {
				continue
			}
			d.Requests = append(d.Requests, Request{At: ts.UTC(), Usage: *p.Info.Last})
			if p.Info.Total != nil {
				t := *p.Info.Total
				d.Final = &t
			}
		}
	}
	if err := sc.Err(); err != nil {
		return d, err
	}
	if len(d.Requests) == 0 {
		return d, errors.New("rollout has no token_count usage")
	}
	return d, nil
}

// ParseRolloutFile opens and parses one rollout.
func ParseRolloutFile(path string) (RunData, error) {
	f, err := os.Open(path)
	if err != nil {
		return RunData{}, err
	}
	defer f.Close()
	return ParseRollout(f)
}

// FindRollout returns the rollout file for a thread id under codexHome
// (sessions/YYYY/MM/DD/rollout-*-<id>.jsonl, or archived_sessions).
func FindRollout(codexHome, id string) (string, error) {
	if id == "" {
		return "", errors.New("no session id")
	}
	patterns := []string{
		filepath.Join(codexHome, "sessions", "*", "*", "*", "rollout-*"+id+".jsonl"),
		filepath.Join(codexHome, "archived_sessions", "rollout-*"+id+".jsonl"),
	}
	for _, pat := range patterns {
		matches, err := filepath.Glob(pat)
		if err != nil {
			return "", err
		}
		if len(matches) > 0 {
			return matches[len(matches)-1], nil
		}
	}
	return "", fmt.Errorf("no rollout for session %s under CODEX_HOME", shortID(id))
}

// ThreadIDFromExecJSON returns the thread id from `codex exec --json`
// output (the thread.started event) and the final turn usage, if any.
func ThreadIDFromExecJSON(r io.Reader) (id string, usage *Usage) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Usage    *Usage `json:"usage"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "thread.started":
			if id == "" {
				id = ev.ThreadID
			}
		case "turn.completed":
			usage = ev.Usage
		}
	}
	return id, usage
}

// shortID keeps the last 8 characters: Codex thread ids are UUIDv7, whose
// leading characters are a timestamp shared by runs a minute apart.
func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}
