package session

import (
	"os"
	"path/filepath"
	"strings"
)

// ClaudeUndercountWarning is shown for a Claude Code session that has
// subagent transcripts. Claude Code sends background agent_summary calls
// that are not written to the transcript, so measured token totals can be
// low. The text has no counts. Exact accounting is investigated separately.
const ClaudeUndercountWarning = "Has subagents: Claude Code makes background summary calls that aren't logged, so token totals are likely undercounted."

// ClaudeUndercountWarningFor returns ClaudeUndercountWarning when
// transcriptPath has at least one <session>/subagents/agent-*.jsonl file.
// It does not read file contents, and it returns "" when there are none.
func ClaudeUndercountWarningFor(transcriptPath string) string {
	dir := strings.TrimSuffix(transcriptPath, ".jsonl")
	if dir == transcriptPath {
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
