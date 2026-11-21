package tui

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/firekeeper-ai/firekeeper/internal/parallel"
	"github.com/firekeeper-ai/firekeeper/internal/session"
	"github.com/firekeeper-ai/firekeeper/internal/transcript"
	"github.com/firekeeper-ai/firekeeper/internal/transcript/reader"
)

// recent.go lists recently active sessions across providers in the idle
// Processes view. They are read from each provider's transcript enumerator,
// loaded only while nothing is running, and open in the chat view.

const (
	recentSessionLimit = 10
	recentSessionAge   = 30 * 24 * time.Hour
	recentRefreshEvery = 30 * time.Second
	recentPage         = 5
)

type recentSession struct {
	provider transcript.Provider
	id       string
	project  string
	title    string
	cwd      string
	path     string
	last     time.Time
}

type recentSessionsMsg struct {
	sessions []recentSession
	loadedAt time.Time
}

// recentSessionsSource loads recent sessions; tests replace it.
var recentSessionsSource = loadRecentSessions

// loadRecentSessions enumerates every provider concurrently. A provider that
// fails or has nothing is left out so one adapter never blocks the rest.
func loadRecentSessions(ctx context.Context, now time.Time) []recentSession {
	providers := reader.Registered()
	results := make([][]recentSession, len(providers))
	parallel.Each(len(providers), func(index int) {
		provider := providers[index]
		enumerator, ok := reader.EnumeratorFor(provider)
		if !ok {
			return
		}
		metas, _ := enumerator.Enumerate(ctx, reader.EnumerateOptions{ModifiedAfter: now.Add(-recentSessionAge)})
		if len(metas) > recentSessionLimit {
			metas = metas[:recentSessionLimit]
		}
		for _, meta := range metas {
			results[index] = append(results[index], newRecentSession(provider, meta))
		}
	})
	var all []recentSession
	for _, part := range results {
		all = append(all, part...)
	}
	sortRecentSessions(all)
	if len(all) > recentSessionLimit {
		all = all[:recentSessionLimit]
	}
	return all
}

func newRecentSession(provider transcript.Provider, meta session.Meta) recentSession {
	recent := recentSession{
		provider: provider,
		id:       meta.ID,
		project:  reader.ProjectName(meta.CWD),
		title:    meta.Title,
		cwd:      meta.CWD,
		path:     meta.RolloutPath,
	}
	switch {
	case meta.LastActivityAt != nil:
		recent.last = *meta.LastActivityAt
	case meta.RolloutPath != "":
		if info, err := os.Stat(meta.RolloutPath); err == nil {
			recent.last = info.ModTime()
		}
	}
	return recent
}

func sortRecentSessions(sessions []recentSession) {
	for i := 1; i < len(sessions); i++ {
		for j := i; j > 0 && recentBefore(sessions[j-1], sessions[j]); j-- {
			sessions[j-1], sessions[j] = sessions[j], sessions[j-1]
		}
	}
}

// recentBefore reports whether a sorts after b (older, ties by id).
func recentBefore(a, b recentSession) bool {
	if !a.last.Equal(b.last) {
		return a.last.Before(b.last)
	}
	return a.id > b.id
}

func loadRecentSessionsCmd() tea.Cmd {
	return func() tea.Msg {
		now := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return recentSessionsMsg{sessions: recentSessionsSource(ctx, now), loadedAt: now}
	}
}

// maybeLoadRecent starts a load when the idle view needs fresh data.
func (m *model) maybeLoadRecent() tea.Cmd {
	if len(m.processGroups) != 0 || m.processErr != "" || m.recentLoading {
		return nil
	}
	if !m.recentLoadedAt.IsZero() && time.Since(m.recentLoadedAt) < recentRefreshEvery {
		return nil
	}
	m.recentLoading = true
	return loadRecentSessionsCmd()
}

func (m *model) applyRecentSessions(msg recentSessionsMsg) {
	selected := ""
	if m.recentCursor < len(m.recentSessions) {
		selected = m.recentSessions[m.recentCursor].id
	}
	m.recentLoading = false
	m.recentLoadedAt = msg.loadedAt
	m.recentSessions = msg.sessions
	m.recentCursor = 0
	for index, recent := range msg.sessions {
		if recent.id == selected {
			m.recentCursor = index
			break
		}
	}
}

// idleListActive reports whether the idle recent list takes the cursor keys.
func (m model) idleListActive() bool {
	return m.activeTab == processesTab && len(m.processGroups) == 0 && len(m.recentSessions) > 0
}

func (m *model) moveRecentCursor(delta int) {
	m.recentCursor = min(max(m.recentCursor+delta, 0), len(m.recentSessions)-1)
}

func (m *model) openRecentChat() tea.Cmd {
	if m.recentCursor >= len(m.recentSessions) {
		return nil
	}
	recent := m.recentSessions[m.recentCursor]
	m.chat = chatState{
		open:     true,
		provider: recent.provider,
		meta: session.Meta{
			ID:          recent.id,
			Provider:    recent.provider,
			CWD:         recent.cwd,
			RolloutPath: recent.path,
		},
		title:   emptyFallback(recent.project, "session") + "/" + dashShortID(recent.id),
		loading: true,
		follow:  true,
	}
	return loadChat(0, m.chat.meta, nil, nil)
}

// recentLines renders the list as one block centered in width, windowed
// around the cursor to capacity rows (one of which is the heading).
func (m model) recentLines(capacity int) []string {
	if len(m.recentSessions) == 0 || capacity < 2 {
		return nil
	}
	const fixed = 2 + 9 + 16 + 5 // marker, tool, project, age
	titleWidth := 0
	for _, recent := range m.recentSessions {
		titleWidth = max(titleWidth, ansi.StringWidth(recentTitle(recent)))
	}
	titleWidth = min(titleWidth, 32, max(m.width-fixed-2, 0))
	pad := dashPaint(strings.Repeat(" ", max((m.width-fixed-titleWidth)/2, 0)), usageTextColor, false)
	heading := "Recent sessions"
	lines := []string{dashPaint(strings.Repeat(" ", max((m.width-len(heading))/2, 0)), usageTextColor, false) + dashPaint(heading, usageBrightColor, true)}
	start, end := usageSessionWindow(len(m.recentSessions), m.recentCursor, capacity-1)
	now := time.Now()
	for index := start; index < end; index++ {
		recent := m.recentSessions[index]
		marker, markerColor := "  ", usageMutedColor
		if index == m.recentCursor {
			marker, markerColor = "▸ ", dashOrange
		}
		lines = append(lines, pad+
			dashPaint(marker, markerColor, true)+
			dashPaint(padText(toolLabel(recent.provider), 9+toolIconWidth()), providerColor(recent.provider), true)+
			dashPaint(padText(sanitizeProcessCommand(emptyFallback(recent.project, "—")), 16), usageTextColor, false)+
			dashPaint(padText(usageAgo(recent.last, now), 5), usageMutedColor, false)+
			dashPaint(ansi.Truncate(recentTitle(recent), titleWidth, "…"), usageMutedColor, false))
	}
	return lines
}

func recentTitle(recent recentSession) string {
	return sanitizeProcessCommand(emptyFallback(recent.title, dashShortID(recent.id)))
}

// RecentSessionReport is one row of `firekeeper recent --json`.
type RecentSessionReport struct {
	SessionID      string              `json:"session_id"`
	Provider       transcript.Provider `json:"provider"`
	Title          string              `json:"title"`
	Project        string              `json:"project"`
	CWD            string              `json:"cwd"`
	LastActivityAt *time.Time          `json:"last_activity_at"`
}

// RecentReport is the output of `firekeeper recent --json`: the same cross-
// provider list the idle Processes view shows (last 30 days, newest 10).
type RecentReport struct {
	GeneratedAt time.Time             `json:"generated_at"`
	Sessions    []RecentSessionReport `json:"sessions"`
}

// RunRecent implements the recent subcommand for the menu bar and scripts.
func RunRecent(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("firekeeper recent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print recent sessions as JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "firekeeper recent: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if !*asJSON {
		fmt.Fprintln(stderr, "firekeeper recent: only --json output is implemented")
		return 2
	}

	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report := buildRecentReport(now, recentSessionsSource(ctx, now))
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintf(stderr, "firekeeper recent: write JSON: %v\n", err)
		return 1
	}
	return 0
}

func buildRecentReport(now time.Time, sessions []recentSession) RecentReport {
	out := make([]RecentSessionReport, 0, len(sessions))
	for _, s := range sessions {
		row := RecentSessionReport{
			SessionID: s.id,
			Provider:  s.provider,
			Title:     emptyFallback(s.title, dashShortID(s.id)),
			Project:   s.project,
			CWD:       s.cwd,
		}
		if !s.last.IsZero() {
			last := s.last
			row.LastActivityAt = &last
		}
		out = append(out, row)
	}
	return RecentReport{GeneratedAt: now, Sessions: out}
}
