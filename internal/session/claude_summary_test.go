package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const summaryPromptMarker = "synthetic-prompt-marker"

var summaryT0 = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func summaryTranscript(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), claudeIDA+".jsonl")
}

func writeSummaryAgent(t *testing.T, transcript, agentID, body, meta string) {
	t.Helper()
	dir := filepath.Join(strings.TrimSuffix(transcript, ".jsonl"), "subagents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-"+agentID+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if meta == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-"+agentID+".meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
}

func summaryUsageLine(t *testing.T, at time.Time, id string, input, cacheRead, cacheCreation int64) string {
	t.Helper()
	return claudeLine(t, map[string]any{
		"type": "assistant", "timestamp": at.UTC().Format(time.RFC3339Nano),
		"message": map[string]any{
			"id": id, "role": "assistant", "model": "claude-test",
			"content": []any{map[string]any{"type": "text", "text": summaryPromptMarker}},
			"usage": map[string]any{
				"input_tokens": input, "output_tokens": 1,
				"cache_read_input_tokens": cacheRead, "cache_creation_input_tokens": cacheCreation,
			},
		},
	})
}

func summaryStamp(t *testing.T, at time.Time) string {
	t.Helper()
	return claudeLine(t, map[string]any{
		"type": "user", "timestamp": at.UTC().Format(time.RFC3339Nano),
		"message": map[string]any{"role": "user", "content": summaryPromptMarker},
	})
}

// alphaBody is one subagent whose transcript span ends at T0+140s.
// Context samples: 1000, 2000, 4000, 8000, 16000. The message id at +10s is
// repeated, which must not create a second sample. A malformed line sits in
// the middle.
func alphaBody(t *testing.T) string {
	t.Helper()
	return summaryStamp(t, summaryT0) +
		summaryUsageLine(t, summaryT0.Add(10*time.Second), "msg_10", 100, 800, 100) +
		summaryUsageLine(t, summaryT0.Add(10*time.Second), "msg_10", 100, 800, 100) +
		"not json " + summaryPromptMarker + "\n" +
		summaryUsageLine(t, summaryT0.Add(40*time.Second), "msg_40", 200, 1600, 200) +
		summaryUsageLine(t, summaryT0.Add(70*time.Second), "msg_70", 400, 3200, 400) +
		summaryUsageLine(t, summaryT0.Add(100*time.Second), "msg_100", 800, 6400, 800) +
		summaryUsageLine(t, summaryT0.Add(140*time.Second), "msg_140", 1600, 12800, 1600)
}

func alphaMeta(end time.Time) string {
	return `{"status":"completed","startedAt":"` + summaryT0.Format(time.RFC3339Nano) +
		`","endedAt":"` + end.Format(time.RFC3339Nano) +
		`","description":"` + summaryPromptMarker + `"}`
}

func requireEstimate(t *testing.T, est *AgentSummaryEstimate, calls, tokens int64, source string) {
	t.Helper()
	if est == nil {
		t.Fatalf("estimate = nil, want %d calls and %d tokens (%s)", calls, tokens, source)
	}
	if est.Calls != calls || est.Tokens != tokens || est.CacheRead != tokens || est.Source != source {
		t.Fatalf("estimate calls=%d tokens=%d cache=%d source=%s, want calls=%d tokens=%d source=%s",
			est.Calls, est.Tokens, est.CacheRead, est.Source, calls, tokens, source)
	}
	if strings.Contains(est.Label, summaryPromptMarker) {
		t.Fatal("estimate label retained transcript text")
	}
	encoded, err := json.Marshal(est)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), summaryPromptMarker) {
		t.Fatal("estimate JSON retained transcript text")
	}
}

func TestClaudeAgentSummaryNoSubagents(t *testing.T) {
	path := summaryTranscript(t)
	if est := EstimateClaudeAgentSummary(path, summaryT0, ClaudeAgentSummaryInterval, ""); est != nil {
		t.Fatalf("missing subagent dir calls=%d", est.Calls)
	}
	writeSummaryAgent(t, path, "empty", "", "")
	// agent-empty.jsonl exists but has no timestamps, so there is no window.
	if est := EstimateClaudeAgentSummary(path, summaryT0, ClaudeAgentSummaryInterval, ""); est != nil {
		t.Fatalf("empty subagent calls=%d", est.Calls)
	}
}

func TestClaudeAgentSummaryOneSubagent(t *testing.T) {
	path := summaryTranscript(t)
	writeSummaryAgent(t, path, "alpha", alphaBody(t), alphaMeta(summaryT0.Add(150*time.Second)))
	est := EstimateClaudeAgentSummary(path, summaryT0.Add(time.Hour), ClaudeAgentSummaryInterval, "")
	requireEstimate(t, est, 5, 31000, AgentSummarySourceTranscript)
	if est.IntervalSeconds != 30 || est.Label != "~5 hidden summary calls, est. 31000 tokens" {
		t.Fatalf("interval=%d label set=%v", est.IntervalSeconds, est.Label != "")
	}
}

func TestClaudeAgentSummaryMissingAndMalformedMeta(t *testing.T) {
	// Without a meta end, the window stops at the last transcript timestamp
	// (T0+140s): floor(140/30) = 4 calls, and the +150s tick is not included.
	wantCalls, wantTokens := int64(4), int64(15000)
	t.Run("missing", func(t *testing.T) {
		path := summaryTranscript(t)
		writeSummaryAgent(t, path, "alpha", alphaBody(t), "")
		est := EstimateClaudeAgentSummary(path, summaryT0.Add(time.Hour), ClaudeAgentSummaryInterval, "")
		requireEstimate(t, est, wantCalls, wantTokens, AgentSummarySourceTranscript)
	})
	t.Run("malformed", func(t *testing.T) {
		path := summaryTranscript(t)
		writeSummaryAgent(t, path, "alpha", alphaBody(t), "not json "+summaryPromptMarker)
		est := EstimateClaudeAgentSummary(path, summaryT0.Add(time.Hour), ClaudeAgentSummaryInterval, "")
		requireEstimate(t, est, wantCalls, wantTokens, AgentSummarySourceTranscript)
	})
	t.Run("unix milliseconds", func(t *testing.T) {
		path := summaryTranscript(t)
		end := summaryT0.Add(150 * time.Second)
		meta := `{"state":"completed","started_at":` + strconv.FormatInt(summaryT0.UnixMilli(), 10) +
			`,"ended_at":` + strconv.FormatInt(end.UnixMilli(), 10) + `}`
		writeSummaryAgent(t, path, "alpha", alphaBody(t), meta)
		est := EstimateClaudeAgentSummary(path, summaryT0, ClaudeAgentSummaryInterval, "")
		requireEstimate(t, est, 5, 31000, AgentSummarySourceTranscript)
	})
}

func TestClaudeAgentSummaryOverlappingSubagents(t *testing.T) {
	path := summaryTranscript(t)
	writeSummaryAgent(t, path, "alpha", alphaBody(t), alphaMeta(summaryT0.Add(150*time.Second)))
	beta := summaryStamp(t, summaryT0.Add(60*time.Second)) +
		summaryUsageLine(t, summaryT0.Add(60*time.Second), "beta_1", 50, 450, 0) +
		summaryStamp(t, summaryT0.Add(120*time.Second))
	writeSummaryAgent(t, path, "beta", beta, `{"status":"completed","startedAt":"`+
		summaryT0.Add(60*time.Second).Format(time.RFC3339Nano)+`","endedAt":"`+
		summaryT0.Add(120*time.Second).Format(time.RFC3339Nano)+`"}`)
	est := EstimateClaudeAgentSummary(path, summaryT0, ClaudeAgentSummaryInterval, "")
	requireEstimate(t, est, 7, 32000, AgentSummarySourceTranscript)
}

func TestClaudeAgentSummaryRunningExtendsToNow(t *testing.T) {
	path := summaryTranscript(t)
	body := summaryStamp(t, summaryT0) +
		summaryUsageLine(t, summaryT0.Add(10*time.Second), "a", 100, 800, 100) +
		summaryUsageLine(t, summaryT0.Add(40*time.Second), "b", 200, 1600, 200)
	writeSummaryAgent(t, path, "alpha", body, `{"status":"running","startedAt":"`+summaryT0.Format(time.RFC3339Nano)+`"}`)
	now := summaryT0.Add(100 * time.Second)
	est := EstimateClaudeAgentSummary(path, now, ClaudeAgentSummaryInterval, "")
	requireEstimate(t, est, 3, 5000, AgentSummarySourceTranscript)
}

func TestClaudeAgentSummaryDebugFile(t *testing.T) {
	path := summaryTranscript(t)
	writeSummaryAgent(t, path, "alpha", alphaBody(t), alphaMeta(summaryT0.Add(150*time.Second)))
	debug := filepath.Join(t.TempDir(), "claude.debug")
	forks := "" +
		summaryT0.Add(30*time.Second).Format(time.RFC3339) + " [AgentSummary] Timer fired for agent alpha\n" +
		summaryT0.Add(30*time.Second).Format(time.RFC3339) + " [AgentSummary] Forking for summary, 4 messages in context\n" +
		summaryT0.Add(70*time.Second).Format(time.RFC3339) + " [AgentSummary] Timer fired for agent other\n" +
		summaryT0.Add(70*time.Second).Format(time.RFC3339) + " [AgentSummary] Forking for summary, 9 messages in context\n" +
		summaryT0.Add(70*time.Second).Format(time.RFC3339) + " [AgentSummary] Timer fired for agent alpha\n" +
		summaryT0.Add(70*time.Second).Format(time.RFC3339) + " [AgentSummary] Forking for summary, 8 messages in context\n"
	if err := os.WriteFile(debug, []byte(forks), 0o600); err != nil {
		t.Fatal(err)
	}
	est := EstimateClaudeAgentSummary(path, summaryT0, ClaudeAgentSummaryInterval, debug)
	requireEstimate(t, est, 2, 5000, AgentSummarySourceDebug)
	if !strings.Contains(est.Label, "debug log") || strings.HasPrefix(est.Label, "~") {
		t.Fatal("debug count was labeled as an interval estimate")
	}

	// A log that names this agent and never forks replaces the estimate.
	if err := os.WriteFile(debug, []byte(summaryT0.Format(time.RFC3339)+" [AgentSummary] Timer fired for agent alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if est := EstimateClaudeAgentSummary(path, summaryT0, ClaudeAgentSummaryInterval, debug); est != nil {
		t.Fatalf("zero debug forks still estimated %d calls", est.Calls)
	}

	// A log that never mentions this session leaves the interval estimate.
	if err := os.WriteFile(debug, []byte(summaryT0.Format(time.RFC3339)+" [AgentSummary] Timer fired for agent other\n"+
		summaryT0.Format(time.RFC3339)+" [AgentSummary] Forking for summary, 1 messages in context\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	est = EstimateClaudeAgentSummary(path, summaryT0, ClaudeAgentSummaryInterval, debug)
	requireEstimate(t, est, 5, 31000, AgentSummarySourceTranscript)

	// A missing debug file falls back the same way.
	est = EstimateClaudeAgentSummary(path, summaryT0, ClaudeAgentSummaryInterval, filepath.Join(t.TempDir(), "absent.debug"))
	requireEstimate(t, est, 5, 31000, AgentSummarySourceTranscript)
}

func TestClaudeAgentSummaryIntervalOverride(t *testing.T) {
	path := summaryTranscript(t)
	writeSummaryAgent(t, path, "alpha", alphaBody(t), alphaMeta(summaryT0.Add(150*time.Second)))
	// 45s ticks at +45, +90, +135. Contexts: 2000, 4000, 8000.
	est := EstimateClaudeAgentSummary(path, summaryT0, 45*time.Second, "")
	requireEstimate(t, est, 3, 14000, AgentSummarySourceTranscript)
	if est.IntervalSeconds != 45 {
		t.Fatalf("interval seconds = %d", est.IntervalSeconds)
	}
	if _, err := ParseClaudeSummaryInterval("10s"); err == nil {
		t.Fatal("accepted 10s")
	}
	if _, err := ParseClaudeSummaryInterval("5m"); err == nil {
		t.Fatal("accepted 5m")
	}
	got, err := ParseClaudeSummaryInterval("45s")
	if err != nil || got != 45*time.Second {
		t.Fatalf("ParseClaudeSummaryInterval(45s) = %s, %v", got, err)
	}
	t.Setenv(EnvClaudeSummaryInterval, "45s")
	got, err = ClaudeSummaryInterval()
	if err != nil || got != 45*time.Second {
		t.Fatalf("env interval = %s, %v", got, err)
	}
	t.Setenv(EnvClaudeSummaryInterval, "10s")
	if _, err := ClaudeSummaryInterval(); err == nil {
		t.Fatal("env 10s was accepted")
	}
}

func TestClaudeAgentSummaryMeasuredTokensStaySeparate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, claudeIDA+".jsonl")
	writeClaudeTranscript(t, path, "/work/demo", "Measured")
	writeSummaryAgent(t, path, "alpha", alphaBody(t), alphaMeta(summaryT0.Add(150*time.Second)))
	info, err := (&discoverer{}).readClaudeTranscriptMetadata(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if info.TokensUsed != 10 || info.Tokens.Total() != 10 {
		t.Fatalf("measured tokens = %d breakdown %d", info.TokensUsed, info.Tokens.Total())
	}
	requireEstimate(t, info.AgentSummary, 5, 31000, AgentSummarySourceTranscript)

	meta := Meta{Input: 10, Output: 20, Cache: 3, AgentSummary: info.AgentSummary}
	encoded, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got["input"] != float64(10) || got["output"] != float64(20) || got["cache"] != float64(3) {
		t.Fatalf("measured totals changed: input=%v output=%v cache=%v", got["input"], got["output"], got["cache"])
	}
	raw, _ := got["agent_summary_estimate"].(map[string]any)
	if raw == nil || raw["calls"] != float64(5) || raw["tokens"] != float64(31000) || raw["cache_read"] != float64(31000) {
		t.Fatal("snapshot JSON omitted the separate estimate")
	}
	if _, ok := raw["label"].(string); !ok {
		t.Fatal("snapshot JSON omitted the estimate label")
	}
}

func TestClaudeDebugFileFlag(t *testing.T) {
	d := &discoverer{}
	group := ProcessGroup{Root: ProcessInfo{Command: "claude --debug-file=/tmp/claude.debug --session-id " + claudeIDA}}
	if got := d.claudeDebugFileFromGroup(group); got != "/tmp/claude.debug" {
		t.Fatalf("debug file = %q", got)
	}
	// ps already splits the command on whitespace, so the flag value is one field.
	// Quotes around that field are trimmed.
	group.Root.Command = "claude --session-id " + claudeIDA + " --debug-file '/tmp/claude.debug'"
	if got := d.claudeDebugFileFromGroup(group); got != "/tmp/claude.debug" {
		t.Fatalf("quoted debug file = %q", got)
	}
	group.Root.Command = "claude"
	group.Processes = []ProcessInfo{{Command: "claude --debug-file /tmp/from-child.debug"}}
	if got := d.claudeDebugFileFromGroup(group); got != "/tmp/from-child.debug" {
		t.Fatalf("child debug file = %q", got)
	}
}
