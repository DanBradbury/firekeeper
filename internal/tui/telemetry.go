package tui

import (
	"slices"
	"strings"

	"github.com/DanBradbury/firekeeper/internal/otelrecv"
	"github.com/DanBradbury/firekeeper/internal/session"
)

// annotateClaudeTelemetry attaches exact totals when a session has local
// telemetry, and the subagent undercount warning when it does not.
func annotateClaudeTelemetry(groups []processGroup, supplements map[string]otelrecv.Supplement) []processGroup {
	for gi := range groups {
		if groups[gi].tool != "Claude" {
			continue
		}
		for si := range groups[gi].sessions {
			s := &groups[gi].sessions[si]
			s.undercountWarning = session.ClaudeUndercountWarningFor(s.rolloutPath)
			s.telemetryHint = true
			sup, ok := supplements[s.id]
			if !ok || !sup.Covered {
				continue
			}
			s.telemetryOn = true
			s.telemetryHint = false
			s.undercountWarning = ""
			s.transcriptTokens = sup.Transcript
			s.exactTokens = sup.Exact
			s.telemetryLine = backgroundCallLine(sup)
		}
	}
	return groups
}

func backgroundCallLine(sup otelrecv.Supplement) string {
	if sup.Added <= 0 {
		return ""
	}
	labels := []struct{ key, label string }{
		{"agent_summary", "summaries"},
		{"prompt_suggestion", "prompt suggestions"},
		{"generate_session_title", "titles"},
		{"compact", "compaction"},
	}
	var parts []string
	used := map[string]bool{}
	for _, item := range labels {
		if n := sup.BySource[item.key]; n > 0 {
			parts = append(parts, item.label+" "+formatTokenCount(n))
			used[item.key] = true
		}
	}
	var rest []string
	for name, n := range sup.BySource {
		if n > 0 && !used[name] {
			rest = append(rest, name)
		}
	}
	slices.Sort(rest)
	for _, name := range rest {
		parts = append(parts, name+" "+formatTokenCount(sup.BySource[name]))
	}
	return "Background calls (not in transcripts): " + formatTokenCount(sup.Added) + " (" + strings.Join(parts, ", ") + ")"
}
