package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Claude Code asks a model for a 3–5 word status line for each running
// subagent (query source agent_summary). The timer is server-configured and
// clamped to 30–240s; the default, and the only period Firekeeper assumes
// unless overridden, is 30s. The requests are not written to the session
// transcript, so measured token totals miss them.
//
// Assumptions. On Dan's Mac (Claude Code 2.1.308, one subagent, about 160s)
// the first fork was about 30s after the subagent started, and there were 5
// forks. That is floor(runtime / interval) with the first tick one interval
// in, not at the start (see the Flamerix gist linked from the reference):
//
//   - The first timer fires one full interval after the subagent starts, not
//     at the start. A trailing partial interval does not fire. A tick exactly
//     at the end time does. The count is floor(runtime / interval).
//   - Runtime starts at the earlier of the first transcript timestamp and a
//     usable meta start. It ends at the later of the meta end and the last
//     transcript timestamp when the meta says the agent has finished. A
//     running status extends the end to now (the live dashboard) or to the
//     clock the caller passes (history uses the transcript modification
//     time). With no usable meta, the window is only the first and last
//     transcript timestamps, so a quiet subagent is estimated through its
//     last saved message.
//   - Tokens for a call are the context size at that moment: input_tokens +
//     cache_read_input_tokens + cache_creation_input_tokens on the latest
//     assistant message at or before the tick. Usage repeated on later
//     content-block lines of the same message id is counted once. The whole
//     amount is recorded as cache read. Measured calls are about 98% cache
//     re-read; the short status line and the small uncached tail are not
//     modeled, and neither is the cheaper "short" mode (off by default since
//     Claude Code 2.1.310).
//   - Each subagent has its own timer. Overlapping agents are summed.
//   - The estimate is never added to measured token totals.
//
// When the Claude process was started with --debug-file, Forking lines in
// that log replace the interval count for agents the log names. Tokens are
// still the context size at each logged fork.
const (
	ClaudeAgentSummaryInterval    = 30 * time.Second
	ClaudeAgentSummaryIntervalMin = 30 * time.Second
	ClaudeAgentSummaryIntervalMax = 240 * time.Second

	// EnvClaudeSummaryInterval overrides claude_summary_interval in
	// config.toml. The value is a Go duration with a unit ("45s", "2m")
	// and must be from 30s to 240s. Config loading rejects anything else.
	EnvClaudeSummaryInterval = "FIREKEEPER_CLAUDE_SUMMARY_INTERVAL"

	AgentSummarySourceTranscript = "transcript"
	AgentSummarySourceDebug      = "debug"
)

// maxSummaryCalls caps a single subagent so a corrupt timestamp span cannot
// build an unbounded slice. Thirty seconds over this cap is more than a
// month of continuous runtime.
const maxSummaryCalls = 100_000

// maxSummaryLineBytes skips a single transcript or debug line above this
// size. The estimator keeps timestamps and token counts only.
const maxSummaryLineBytes = 4 << 20

// AgentSummaryEstimate is a labeled guess at Claude Code agent_summary calls
// that never land in the transcript. It is not part of measured token totals.
type AgentSummaryEstimate struct {
	Calls           int64  `json:"calls"`
	Tokens          int64  `json:"tokens"`
	CacheRead       int64  `json:"cache_read"`
	IntervalSeconds int    `json:"interval_seconds"`
	Source          string `json:"source"`
	// Label is the sentence the API shows, with exact integer token counts.
	// The dashboard abbreviates the same numbers.
	Label string `json:"label"`
}

// ParseClaudeSummaryInterval parses a summary period. raw must be a Go
// duration from 30s to 240s inclusive. The error names the problem and the
// duration text, never transcript contents.
func ParseClaudeSummaryInterval(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q is not a positive duration such as \"30s\"", raw)
	}
	if d < ClaudeAgentSummaryIntervalMin || d > ClaudeAgentSummaryIntervalMax {
		return 0, fmt.Errorf("%q is outside 30s-240s", raw)
	}
	return d, nil
}

// ClaudeSummaryInterval is the period from EnvClaudeSummaryInterval, or 30s
// when that variable is unset. Callers that loaded config pass
// Config.ClaudeSummaryInterval instead, so the file and the environment are
// applied in one place. An invalid environment value is returned as an error.
func ClaudeSummaryInterval() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(EnvClaudeSummaryInterval))
	if raw == "" {
		return ClaudeAgentSummaryInterval, nil
	}
	return ParseClaudeSummaryInterval(raw)
}

// EstimateClaudeAgentSummary estimates hidden agent_summary calls for the
// session whose transcript is at transcriptPath. now is the clock used when
// a subagent meta says it is still running. debugFile, when non-empty and
// readable, is a Claude --debug-file log. The result is nil when there is
// nothing to report. It never includes transcript text.
func EstimateClaudeAgentSummary(transcriptPath string, now time.Time, interval time.Duration, debugFile string) *AgentSummaryEstimate {
	if interval <= 0 {
		interval = ClaudeAgentSummaryInterval
	}
	dir := strings.TrimSuffix(transcriptPath, ".jsonl")
	if dir == transcriptPath {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(dir, "subagents", "agent-*.jsonl"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	agents := make([]subagentRun, 0, len(matches))
	for _, path := range matches {
		if run, ok := readSubagentRun(path, now); ok {
			agents = append(agents, run)
		}
	}
	if len(agents) == 0 {
		return nil
	}
	if est, ok := estimateFromDebugFile(debugFile, agents, interval); ok {
		return est
	}
	return estimateFromInterval(agents, interval)
}

type contextSample struct {
	at      time.Time
	context int64
}

type subagentRun struct {
	id      string
	start   time.Time
	end     time.Time
	samples []contextSample
}

func estimateFromInterval(agents []subagentRun, interval time.Duration) *AgentSummaryEstimate {
	var calls, tokens int64
	for _, agent := range agents {
		for _, tick := range summaryTicks(agent.start, agent.end, interval) {
			calls++
			tokens = addTokens(tokens, contextAt(agent.samples, tick, true))
		}
	}
	return finishEstimate(calls, tokens, interval, AgentSummarySourceTranscript)
}

func estimateFromDebugFile(path string, agents []subagentRun, interval time.Duration) (*AgentSummaryEstimate, bool) {
	if path == "" {
		return nil, false
	}
	byID := make(map[string]*subagentRun, len(agents)*2)
	for i := range agents {
		byID[agents[i].id] = &agents[i]
		byID["agent-"+agents[i].id] = &agents[i]
	}
	forks, named, ok := readSummaryDebug(path, byID)
	if !ok || !named {
		return nil, false
	}
	var calls, tokens int64
	for _, fork := range forks {
		agent := byID[fork.agent]
		if agent == nil {
			continue
		}
		calls++
		tokens = addTokens(tokens, contextAt(agent.samples, fork.at, fork.hasAt))
	}
	return finishEstimate(calls, tokens, interval, AgentSummarySourceDebug), true
}

type summaryFork struct {
	agent string
	at    time.Time
	hasAt bool
}

// readSummaryDebug reports forks that belong to this session. named is true
// when the log mentions one of the session's agents, which makes the count
// authoritative even when no fork follows (the real count is zero). A log
// that never mentions the session is not authoritative.
func readSummaryDebug(path string, byID map[string]*subagentRun) (forks []summaryFork, named, ok bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, false
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, false
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64<<10)
	var current string
	var currentAt time.Time
	var currentHas bool
	foreign := false
	single := loneAgent(byID)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && len(line) <= maxSummaryLineBytes {
			kind, id, at, hasAt := classifySummaryLine(string(line))
			switch kind {
			case "timer":
				if _, ours := byID[id]; ours {
					named = true
					current, currentAt, currentHas = id, at, hasAt
				} else if id != "" {
					foreign = true
					current, currentHas = "", false
				}
			case "fork":
				agent := current
				when, has := at, hasAt
				if !has && currentHas {
					when, has = currentAt, true
				}
				switch {
				case agent != "" && byID[agent] != nil:
					named = true
					forks = append(forks, summaryFork{agent: canonicalAgentID(agent, byID), at: when, hasAt: has})
				case agent == "" && !foreign && single != "":
					// A fork with no agent id counts only when this session
					// has one subagent and the log has not named a different one.
					forks = append(forks, summaryFork{agent: single, at: when, hasAt: has})
					named = true
				}
			}
		}
		if err != nil {
			break
		}
	}
	return forks, named, true
}

func loneAgent(byID map[string]*subagentRun) string {
	var id string
	seen := map[*subagentRun]bool{}
	for _, agent := range byID {
		if seen[agent] {
			continue
		}
		seen[agent] = true
		if id != "" {
			return ""
		}
		id = agent.id
	}
	return id
}

func canonicalAgentID(id string, byID map[string]*subagentRun) string {
	if agent := byID[id]; agent != nil {
		return agent.id
	}
	return id
}

const (
	summaryTimerMarker = "[AgentSummary] Timer fired for agent "
	summaryForkMarker  = "[AgentSummary] Forking for summary"
)

func classifySummaryLine(line string) (kind, id string, at time.Time, hasAt bool) {
	at, hasAt = leadingTime(line)
	if i := strings.Index(line, summaryTimerMarker); i >= 0 {
		rest := strings.TrimSpace(line[i+len(summaryTimerMarker):])
		if fields := strings.Fields(rest); len(fields) > 0 {
			id = strings.Trim(fields[0], ".,;:)]}")
		}
		return "timer", id, at, hasAt
	}
	if strings.Contains(line, summaryForkMarker) {
		return "fork", "", at, hasAt
	}
	return "", "", at, hasAt
}

func leadingTime(line string) (time.Time, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return time.Time{}, false
	}
	if ts, err := time.Parse(time.RFC3339Nano, fields[0]); err == nil {
		return ts, true
	}
	if len(fields) >= 2 {
		combined := fields[0] + " " + fields[1]
		for _, layout := range []string{"2006-01-02 15:04:05.000", "2006-01-02 15:04:05"} {
			if ts, err := time.Parse(layout, combined); err == nil {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}

func finishEstimate(calls, tokens int64, interval time.Duration, source string) *AgentSummaryEstimate {
	if calls <= 0 {
		return nil
	}
	seconds := int(interval.Round(time.Second) / time.Second)
	if seconds <= 0 {
		seconds = int(ClaudeAgentSummaryInterval / time.Second)
	}
	est := &AgentSummaryEstimate{
		Calls:           calls,
		Tokens:          tokens,
		CacheRead:       tokens,
		IntervalSeconds: seconds,
		Source:          source,
	}
	est.Label = agentSummaryLabel(est)
	return est
}

func agentSummaryLabel(est *AgentSummaryEstimate) string {
	calls := strconv.FormatInt(est.Calls, 10)
	tokens := strconv.FormatInt(est.Tokens, 10)
	if est.Source == AgentSummarySourceDebug {
		return calls + " hidden summary calls (debug log), est. " + tokens + " tokens"
	}
	return "~" + calls + " hidden summary calls, est. " + tokens + " tokens"
}

func summaryTicks(start, end time.Time, interval time.Duration) []time.Time {
	// A zero-length window has no tick: floor(0 / interval) == 0. The first
	// tick is one interval after start, and a tick exactly at end is included.
	if interval <= 0 || start.IsZero() || !end.After(start) {
		return nil
	}
	var ticks []time.Time
	for tick := start.Add(interval); !tick.After(end); tick = tick.Add(interval) {
		if len(ticks) >= maxSummaryCalls {
			break
		}
		ticks = append(ticks, tick)
	}
	return ticks
}

func contextAt(samples []contextSample, at time.Time, hasAt bool) int64 {
	if len(samples) == 0 {
		return 0
	}
	if !hasAt {
		return samples[len(samples)-1].context
	}
	var ctx int64
	found := false
	for _, sample := range samples {
		if !sample.at.After(at) {
			ctx = sample.context
			found = true
		}
	}
	if !found {
		return 0
	}
	return ctx
}

func addTokens(sum, n int64) int64 {
	if n <= 0 {
		return sum
	}
	if sum > math.MaxInt64-n {
		return math.MaxInt64
	}
	return sum + n
}

type summaryUsage struct {
	Input         int64 `json:"input_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
}

type summaryRecord struct {
	Timestamp string `json:"timestamp"`
	Message   *struct {
		ID    string        `json:"id"`
		Usage *summaryUsage `json:"usage"`
	} `json:"message"`
}

// subagentMeta is the subset of agent-*.meta.json the estimator understands.
// Prompt text and other fields are ignored and not retained.
type subagentMeta struct {
	Status      string `json:"status"`
	State       string `json:"state"`
	StartedAt   any    `json:"startedAt"`
	StartedAtUS any    `json:"started_at"`
	StartTime   any    `json:"startTime"`
	CreatedAt   any    `json:"createdAt"`
	CreatedAtUS any    `json:"created_at"`
	EndedAt     any    `json:"endedAt"`
	EndedAtUS   any    `json:"ended_at"`
	EndTime     any    `json:"endTime"`
	CompletedAt any    `json:"completedAt"`
	CompletedUS any    `json:"completed_at"`
	StoppedAt   any    `json:"stoppedAt"`
}

func readSubagentRun(path string, now time.Time) (subagentRun, bool) {
	stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	id := strings.TrimPrefix(stem, "agent-")
	if id == "" {
		return subagentRun{}, false
	}
	samples, first, last, ok := readSubagentTranscript(path)
	meta, hasMeta := readSubagentMeta(strings.TrimSuffix(path, ".jsonl") + ".meta.json")
	start := first
	if hasMeta && !meta.start.IsZero() && (start.IsZero() || meta.start.Before(start)) {
		start = meta.start
	}
	end := last
	if hasMeta && meta.terminal && !meta.end.IsZero() && (end.IsZero() || meta.end.After(end)) {
		end = meta.end
	}
	if hasMeta && meta.running && !now.IsZero() && (end.IsZero() || now.After(end)) {
		end = now
	}
	if start.IsZero() {
		return subagentRun{}, false
	}
	if end.IsZero() {
		end = start
	}
	if end.Before(start) {
		return subagentRun{}, false
	}
	for i := range samples {
		if samples[i].at.IsZero() {
			samples[i].at = start
		}
	}
	// File order is usually chronological. Sort so a backwards timestamp
	// still yields the latest sample at or before a tick.
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].at.Before(samples[j].at) })
	if !ok && !hasMeta {
		return subagentRun{}, false
	}
	return subagentRun{id: id, start: start, end: end, samples: samples}, true
}

func readSubagentTranscript(path string) (samples []contextSample, first, last time.Time, ok bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, time.Time{}, false
	}
	defer file.Close()
	seen := map[string]int{}
	reader := bufio.NewReaderSize(file, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && len(line) <= maxSummaryLineBytes {
			var rec summaryRecord
			if json.Unmarshal(line, &rec) == nil {
				ok = true
				if ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
					if first.IsZero() || ts.Before(first) {
						first = ts
					}
					if last.IsZero() || ts.After(last) {
						last = ts
					}
				}
				if rec.Message != nil && rec.Message.Usage != nil {
					usage := rec.Message.Usage
					ctx := max(usage.Input, 0) + max(usage.CacheRead, 0) + max(usage.CacheCreation, 0)
					when, _ := time.Parse(time.RFC3339Nano, rec.Timestamp)
					id := rec.Message.ID
					if id != "" {
						if idx, exists := seen[id]; exists {
							if ctx > samples[idx].context {
								samples[idx].context = ctx
							}
							if samples[idx].at.IsZero() && !when.IsZero() {
								samples[idx].at = when
							}
						} else {
							seen[id] = len(samples)
							samples = append(samples, contextSample{at: when, context: ctx})
						}
					} else {
						samples = append(samples, contextSample{at: when, context: ctx})
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	return samples, first, last, ok
}

type parsedSubagentMeta struct {
	start, end        time.Time
	running, terminal bool
}

func readSubagentMeta(path string) (parsedSubagentMeta, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return parsedSubagentMeta{}, false
	}
	if len(data) > maxSummaryLineBytes {
		return parsedSubagentMeta{}, false
	}
	var meta subagentMeta
	if json.Unmarshal(data, &meta) != nil {
		return parsedSubagentMeta{}, false
	}
	status := strings.ToLower(strings.TrimSpace(FirstNonEmpty(meta.Status, meta.State)))
	parsed := parsedSubagentMeta{
		start:    firstMetaTime(meta.StartedAt, meta.StartedAtUS, meta.StartTime, meta.CreatedAt, meta.CreatedAtUS),
		end:      firstMetaTime(meta.EndedAt, meta.EndedAtUS, meta.EndTime, meta.CompletedAt, meta.CompletedUS, meta.StoppedAt),
		running:  subagentRunning(status),
		terminal: subagentTerminal(status),
	}
	if !parsed.end.IsZero() && !parsed.running {
		parsed.terminal = true
	}
	if parsed.start.IsZero() && parsed.end.IsZero() && status == "" {
		return parsedSubagentMeta{}, false
	}
	return parsed, true
}

func firstMetaTime(values ...any) time.Time {
	for _, value := range values {
		if ts, ok := parseMetaTime(value); ok {
			return ts
		}
	}
	return time.Time{}
}

func parseMetaTime(value any) (time.Time, bool) {
	switch v := value.(type) {
	case string:
		if ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(v)); err == nil {
			return ts, true
		}
	case float64:
		if v > 1e12 {
			return time.UnixMilli(int64(v)).UTC(), true
		}
		if v > 1e9 {
			return time.Unix(int64(v), 0).UTC(), true
		}
	}
	return time.Time{}, false
}

func subagentRunning(status string) bool {
	switch status {
	case "running", "in_progress", "in-progress", "active", "pending":
		return true
	default:
		return false
	}
}

func subagentTerminal(status string) bool {
	switch status {
	case "completed", "complete", "done", "failed", "error", "killed", "cancelled", "canceled", "ended", "success", "stopped":
		return true
	default:
		return false
	}
}
