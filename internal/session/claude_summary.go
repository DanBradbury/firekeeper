package session

import (
	"os"
	"path/filepath"
	"strings"
)

// ClaudeUndercountWarning is shown when a Claude Code session has subagent
// transcripts and Firekeeper has no local telemetry for it. The background
// summary calls are not in those files, so a transcript-only total can be low.
const ClaudeUndercountWarning = "Has subagents: Claude Code makes background summary calls that aren't logged, so token totals are likely undercounted."

// ClaudeUndercountWarningFor returns ClaudeUndercountWarning when
// transcriptPath has at least one <session>/subagents/agent-*.jsonl file.
// It does not read file contents.
func ClaudeUndercountWarningFor(transcriptPath string) string {
	dir := strings.TrimSuffix(transcriptPath, ".jsonl")
	if dir == transcriptPath || dir == "" {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(dir, "subagents", "agent-*.jsonl"))
	if err != nil {
		return ""
	}
	for _, match := range matches {
		info, err := os.Stat(match)
		if err == nil && info.Mode().IsRegular() {
			return ClaudeUndercountWarning
		}
	}
	return ""
}
