package tui

import (
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

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
			s.backgroundTokens = sup.Added
			s.backgroundCalls = backgroundCallParts(sup)
		}
	}
	return groups
}

func backgroundCallLine(sup otelrecv.Supplement) string {
	if sup.Added <= 0 {
		return ""
	}
	return "Background calls (not in transcripts): " + formatTokenCount(sup.Added) + " (" + strings.Join(backgroundCallParts(sup), ", ") + ")"
}

// backgroundCallParts lists each background call source with its tokens, the
// known sources first under readable names and the rest by name.
func backgroundCallParts(sup otelrecv.Supplement) []string {
	if sup.Added <= 0 {
		return nil
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
		// Source names come from whatever posted to the telemetry listener.
		parts = append(parts, sanitizeProcessCommand(name)+" "+formatTokenCount(sup.BySource[name]))
	}
	return parts
}

// wrapItems packs items into lines of at most width cells, separated by sep.
// An item wider than width gets a line of its own and is cut to fit.
func wrapItems(items []string, sep string, width int) []string {
	var lines []string
	var line string
	for _, item := range items {
		item = ansi.Truncate(item, width, "…")
		switch {
		case line == "":
			line = item
		case ansi.StringWidth(line+sep+item) <= width:
			line += sep + item
		default:
			lines = append(lines, line)
			line = item
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// marqueeInterval is how often a marquee line moves one column.
const marqueeInterval = 150 * time.Millisecond

// marqueeGap separates the end of a scrolling line from its next pass.
const marqueeGap = "   ·   "

type marqueeTickMsg struct{}

func tickMarquee() tea.Cmd {
	return tea.Tick(marqueeInterval, func(time.Time) tea.Msg { return marqueeTickMsg{} })
}

// marquee returns text unchanged when it fits width, and otherwise the width
// cells of a loop of it that frame has scrolled to, so a long line moves
// through its panel instead of being cut off.
func marquee(text string, width, frame int) string {
	runes := []rune(text)
	if width < 1 || len(runes) <= width {
		return text
	}
	loop := append(runes, []rune(marqueeGap)...)
	start := frame % len(loop)
	out := make([]rune, width)
	for i := range out {
		out[i] = loop[(start+i)%len(loop)]
	}
	return string(out)
}
