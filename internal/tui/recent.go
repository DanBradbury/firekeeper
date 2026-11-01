package tui

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
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
	tool     string
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

func recentToolLabel(provider transcript.Provider) string {
	switch provider {
	case transcript.ProviderCodex:
		return "Codex"
	case transcript.ProviderCopilot:
		return "Copilot"
	case transcript.ProviderClaude:
		return "Claude"
	case transcript.ProviderOpenCode:
		return "OpenCode"
	case transcript.ProviderKimi:
		return "Kimi"
	case transcript.ProviderGemini:
		return "Gemini"
	case transcript.ProviderCursor:
		return "Cursor"
	default:
		return string(provider)
	}
}

// loadRecentSessions enumerates every provider concurrently. A provider that
// fails or has nothing is left out so one adapter never blocks the rest.
func loadRecentSessions(ctx context.Context, now time.Time) []recentSession {
	providers := reader.Registered()
	results := make([][]recentSession, len(providers))
	var wg sync.WaitGroup
	for index, provider := range providers {
		enumerator, ok := reader.EnumeratorFor(provider)
		if !ok {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			metas, _ := enumerator.Enumerate(ctx, reader.EnumerateOptions{ModifiedAfter: now.Add(-recentSessionAge)})
			if len(metas) > recentSessionLimit {
				metas = metas[:recentSessionLimit]
			}
			for _, meta := range metas {
				results[index] = append(results[index], newRecentSession(provider, meta))
			}
		}()
	}
	wg.Wait()
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
		tool:     recentToolLabel(provider),
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
		open: true,
		tool: recent.tool,
		meta: session.Meta{
			ID:          recent.id,
			Provider:    string(recent.provider),
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
	pad := strings.Repeat(" ", max((m.width-fixed-titleWidth)/2, 0))
	heading := "Recent sessions"
	lines := []string{strings.Repeat(" ", max((m.width-len(heading))/2, 0)) + dashPaint(heading, usageBrightColor, true)}
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
			dashPaint(padText(recent.tool, 9), dashToolColor(recent.tool), true)+
			dashPaint(padText(sanitizeProcessCommand(emptyFallback(recent.project, "—")), 16), usageTextColor, false)+
			dashPaint(padText(usageAgo(recent.last, now), 5), usageMutedColor, false)+
			dashPaint(ansi.Truncate(recentTitle(recent), titleWidth, "…"), usageMutedColor, false))
	}
	return lines
}

func recentTitle(recent recentSession) string {
	return sanitizeProcessCommand(emptyFallback(recent.title, dashShortID(recent.id)))
}
