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

// Usage is one Codex usage object. input_tokens already includes
// cached_input_tokens; total_tokens is input_tokens + output_tokens.
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

// RolloutStats is what one Codex rollout file says about its session.
type RolloutStats struct {
	SessionID  string
	CLIVersion string
	Models     []string // distinct turn_context models, in order
	Efforts    []string // distinct turn_context efforts, in order
	// Usage sums last_token_usage over token_count events, the same rule
	// Firekeeper uses, so the totals match `firekeeper window`.
	Usage Usage
	// Final is the last total_token_usage seen; it should equal Usage.
	Final Usage
	// Requests counts model responses: distinct response ids in
	// token_usage_record when present, otherwise token_count events.
	Requests int
	// First is the first response's usage, which shows whether the run
	// started cold (nothing cached) or warm.
	First Usage
	// ToolCalls counts function and custom tool calls.
	ToolCalls int
	// UsedPercentStart/End are the primary (5-hour) rate-limit window's
	// used_percent at the first and last token_count that reported one.
	UsedPercentStart, UsedPercentEnd *float64
	FirstAt, LastAt                  time.Time
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
	ResponseID string `json:"response_id"`
	Info       *struct {
		Total *Usage `json:"total_token_usage"`
		Last  *Usage `json:"last_token_usage"`
	} `json:"info"`
	RateLimits *struct {
		Primary *struct {
			UsedPercent float64 `json:"used_percent"`
		} `json:"primary"`
	} `json:"rate_limits"`
}

// ParseRollout reads a Codex rollout JSONL file. Unknown and malformed lines
// are skipped; nothing is printed, so prompts and outputs never leak.
func ParseRollout(r io.Reader) (RolloutStats, error) {
	var st RolloutStats
	seenModel := map[string]bool{}
	seenEffort := map[string]bool{}
	responses := map[string]bool{}
	tokenCounts := 0
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
		if ts, err := time.Parse(time.RFC3339Nano, line.Timestamp); err == nil {
			if st.FirstAt.IsZero() || ts.Before(st.FirstAt) {
				st.FirstAt = ts
			}
			if ts.After(st.LastAt) {
				st.LastAt = ts
			}
		}
		switch line.Type {
		case "session_meta":
			if st.SessionID == "" {
				st.SessionID = p.ID
				st.CLIVersion = p.CLIVersion
			}
		case "turn_context":
			if p.Model != "" && !seenModel[p.Model] {
				seenModel[p.Model] = true
				st.Models = append(st.Models, p.Model)
			}
			if p.Effort != "" && !seenEffort[p.Effort] {
				seenEffort[p.Effort] = true
				st.Efforts = append(st.Efforts, p.Effort)
			}
		case "token_usage_record":
			if p.ResponseID != "" {
				responses[p.ResponseID] = true
			}
		case "response_item":
			if p.Type == "function_call" || p.Type == "custom_tool_call" {
				st.ToolCalls++
			}
		case "event_msg":
			if p.Type != "token_count" {
				continue
			}
			if p.RateLimits != nil && p.RateLimits.Primary != nil {
				v := p.RateLimits.Primary.UsedPercent
				if st.UsedPercentStart == nil {
					st.UsedPercentStart = &v
				}
				st.UsedPercentEnd = &v
			}
			if p.Info == nil || p.Info.Last == nil {
				continue
			}
			if tokenCounts == 0 {
				st.First = *p.Info.Last
			}
			tokenCounts++
			st.Usage.add(*p.Info.Last)
			if p.Info.Total != nil {
				st.Final = *p.Info.Total
			}
		}
	}
	if err := sc.Err(); err != nil {
		return st, err
	}
	st.Requests = len(responses)
	if st.Requests == 0 {
		st.Requests = tokenCounts
	}
	if tokenCounts == 0 {
		return st, errors.New("rollout has no token_count usage")
	}
	return st, nil
}

// ParseRolloutFile opens and parses one rollout.
func ParseRolloutFile(path string) (RolloutStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return RolloutStats{}, err
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
