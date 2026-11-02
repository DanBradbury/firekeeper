package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// overview.go renders the Overview tab: one panel per provider that has data,
// with a meter and reset countdown for every quota Firekeeper can read, and
// token totals for today, the last seven days and the lifetime. Providers
// that expose no quota locally say where to find it instead of faking a meter.

const (
	overviewLabelWidth = 12
	overviewBarMin     = 8
	overviewBarMax     = 30
	overviewWeekDays   = 7
)

// overviewMeter is one quota window: how much of it is used and when it resets.
type overviewMeter struct {
	Label   string
	Percent float64
	// Detail is an optional absolute figure such as "300 / 1,500".
	Detail  string
	ResetAt time.Time
}

// overviewProvider is a provider reduced to what the Overview panel shows.
type overviewProvider struct {
	Name    string
	Accent  string
	Loading bool
	Err     string
	HasData bool

	Meters []overviewMeter
	// Alerts are red lines such as a reached limit.
	Alerts []string
	// Tokens are today, last seven days and lifetime; Lifetime < 0 is unknown.
	Today, Week, Lifetime int64
	Sessions              int64
	// Notes are muted lines, such as where to find the quota.
	Notes []string
}

// visible reports whether the provider gets a panel: it has data, is loading
// for the first time, or failed (a failure is worth surfacing).
func (p overviewProvider) visible() bool {
	return p.HasData || p.Loading || p.Err != ""
}

func (m model) overviewProviders(now time.Time) []overviewProvider {
	return []overviewProvider{
		m.codexOverview(now),
		m.copilotOverview(now),
		m.claudeOverview(now),
		m.kimiOverview(now),
	}
}

func (m model) codexOverview(now time.Time) overviewProvider {
	snapshot := m.codexUsage
	provider := overviewProvider{
		Name: "Codex", Accent: dashBlue,
		Loading:  m.codexUsageLoading && !snapshot.hasData(),
		Err:      m.codexUsageErr,
		HasData:  snapshot.hasData(),
		Lifetime: -1,
	}
	for _, limit := range snapshot.sortedRateLimits() {
		name := limit.LimitName
		if name == "" {
			name = limit.LimitID
		}
		for _, window := range []*codexQuotaWindow{limit.Primary, limit.Secondary} {
			if window == nil {
				continue
			}
			meter := overviewMeter{Label: quotaWindowTitle(name, window.WindowDurationMin), Percent: window.UsedPercent}
			if window.ResetsAt > 0 {
				meter.ResetAt = time.Unix(window.ResetsAt, 0)
			}
			provider.Meters = append(provider.Meters, meter)
		}
		if limit.RateLimitReachedType != "" {
			provider.Alerts = append(provider.Alerts, "LIMIT REACHED "+sanitizeProcessCommand(limit.RateLimitReachedType))
		}
	}
	dates, tokens := []string{}, []int64{}
	for _, day := range snapshot.Daily {
		dates, tokens = append(dates, day.StartDate), append(tokens, day.Tokens)
	}
	if len(dates) == 0 {
		for _, entry := range snapshot.DailyByModel {
			dates, tokens = append(dates, entry.StartDate), append(tokens, entry.Tokens)
		}
	}
	provider.Today, provider.Week = overviewRecentTokens(dates, tokens, now)
	if snapshot.Summary.LifetimeTokens != nil {
		provider.Lifetime = *snapshot.Summary.LifetimeTokens
	}
	if len(provider.Meters) == 0 && provider.HasData {
		provider.Notes = append(provider.Notes, "No ChatGPT-backed quota data")
	}
	return provider
}

func (m model) copilotOverview(now time.Time) overviewProvider {
	snapshot := m.copilotUsage
	provider := overviewProvider{
		Name: "Copilot", Accent: dashPurple,
		Loading:  m.copilotUsageLoading && !snapshot.hasData(),
		Err:      m.copilotUsageErr,
		HasData:  snapshot.hasData(),
		Sessions: snapshot.Summary.Sessions,
		Lifetime: snapshot.Summary.LifetimeTokens,
	}
	dates, tokens := []string{}, []int64{}
	for _, day := range snapshot.Daily {
		dates, tokens = append(dates, day.StartDate), append(tokens, day.Tokens)
	}
	provider.Today, provider.Week = overviewRecentTokens(dates, tokens, now)
	plan := snapshot.Plan
	if plan.Available && plan.TotalCredits > 0 {
		meter := overviewMeter{
			Label:   "AI credits",
			Percent: plan.UsedCredits / plan.TotalCredits * 100,
			Detail:  formatAICredits(plan.UsedCredits) + " / " + formatAICredits(plan.TotalCredits),
			ResetAt: plan.ResetAt,
		}
		provider.Meters = append(provider.Meters, meter)
	} else if provider.HasData {
		provider.Notes = append(provider.Notes, "Allowance: GitHub billing")
	}
	return provider
}

func (m model) claudeOverview(now time.Time) overviewProvider {
	snapshot := m.claudeUsage
	provider := overviewProvider{
		Name: "Claude", Accent: dashOrange,
		Loading:  m.claudeUsageLoading && !snapshot.hasData(),
		Err:      m.claudeUsageErr,
		HasData:  snapshot.hasData(),
		Sessions: snapshot.Summary.Sessions,
		Lifetime: snapshot.Summary.LifetimeTokens,
	}
	dates, tokens := []string{}, []int64{}
	for _, day := range snapshot.Daily {
		dates, tokens = append(dates, day.StartDate), append(tokens, day.Tokens)
	}
	provider.Today, provider.Week = overviewRecentTokens(dates, tokens, now)
	for _, window := range claudeLimitWindows(snapshot.Limits) {
		provider.Meters = append(provider.Meters, overviewMeter{Label: window.Label, Percent: window.UsedPercent, ResetAt: window.ResetsAt})
	}
	switch {
	case !snapshot.LimitsOn && provider.HasData:
		provider.Notes = append(provider.Notes, "Limits: firekeeper claude-limits enable")
	case snapshot.LimitsErr != "":
		provider.Notes = append(provider.Notes, "Limits unavailable: "+snapshot.LimitsErr)
	}
	if snapshot.CloudSyncWarn != "" {
		provider.Notes = append(provider.Notes, "Cloud sync: "+snapshot.CloudSyncWarn)
	}
	return provider
}

func (m model) kimiOverview(now time.Time) overviewProvider {
	snapshot := m.kimiUsage
	provider := overviewProvider{
		Name: "Kimi", Accent: dashTeal,
		Loading:  m.kimiUsageLoading && !snapshot.hasData(),
		Err:      m.kimiUsageErr,
		HasData:  snapshot.hasData(),
		Sessions: snapshot.Summary.Sessions,
		Lifetime: snapshot.Summary.LifetimeTokens,
	}
	dates, tokens := []string{}, []int64{}
	for _, day := range snapshot.Daily {
		dates, tokens = append(dates, day.StartDate), append(tokens, day.Tokens)
	}
	provider.Today, provider.Week = overviewRecentTokens(dates, tokens, now)
	if provider.HasData {
		provider.Notes = append(provider.Notes, "Quota: Kimi Code /usage")
	}
	return provider
}

// overviewRecentTokens sums tokens dated today and within the last seven days
// (today included). Dates are local YYYY-MM-DD, as in the daily buckets.
func overviewRecentTokens(dates []string, tokens []int64, now time.Time) (today, week int64) {
	todayDate := now.Format("2006-01-02")
	weekStart := startOfDay(now).AddDate(0, 0, -(overviewWeekDays - 1)).Format("2006-01-02")
	for index, date := range dates {
		if date == todayDate {
			today += tokens[index]
		}
		if date >= weekStart && date <= todayDate {
			week += tokens[index]
		}
	}
	return today, week
}

// overviewBar draws a used/total meter of width cells, coloured by how close
// the window is to its limit.
func overviewBar(percent float64, width int) string {
	percent = min(max(percent, 0), 100)
	filled := int(percent/100*float64(width) + 0.5)
	if percent > 0 && filled == 0 {
		filled = 1
	}
	filled = min(filled, width)
	return usagePaint(strings.Repeat("█", filled), usagePercentColor(percent), false, usagePanelColor) +
		usagePaint(strings.Repeat("░", width-filled), usageTrackColor, false, usagePanelColor)
}

// overviewResetText is the countdown plus, beyond a day away, the local date.
func overviewResetText(resetAt, now time.Time) string {
	if resetAt.IsZero() {
		return "reset unknown"
	}
	return codexResetText(resetAt.Unix(), now)
}

// overviewMeterLine fits one quota row to inner cells: label, bar, percent and
// reset countdown. The bar and then the detail are dropped when space is short.
func overviewMeterLine(meter overviewMeter, inner int, now time.Time) string {
	percent := fmt.Sprintf("%3.0f%%", meter.Percent)
	reset := overviewResetText(meter.ResetAt, now)
	fixed := overviewLabelWidth + 1 + ansi.StringWidth(percent) + 2 + ansi.StringWidth(reset)
	line := dashPaint(dashCell(meter.Label, overviewLabelWidth, false)+" ", usageTextColor, false)
	if bar := min(inner-fixed-1, overviewBarMax); bar >= overviewBarMin {
		line += overviewBar(meter.Percent, bar) + dashPaint(" ", usageTextColor, false)
	}
	line += dashPaint(percent, usagePercentColor(meter.Percent), true) + dashPaint("  "+reset, usageMutedColor, false)
	if meter.Detail != "" && ansi.StringWidth(line)+2+ansi.StringWidth(meter.Detail) <= inner {
		line += dashPaint("  "+meter.Detail, usageMutedColor, false)
	}
	return line
}

// overviewTokenLine is the today / 7 days / lifetime totals row.
func overviewTokenLine(provider overviewProvider) string {
	parts := []string{
		dashPaint("today ", usageMutedColor, false) + dashPaint(formatTokenCount(provider.Today), dashYellow, true),
		dashPaint("7d ", usageMutedColor, false) + dashPaint(formatTokenCount(provider.Week), dashYellow, true),
	}
	if provider.Lifetime >= 0 {
		parts = append(parts, dashPaint("lifetime ", usageMutedColor, false)+dashPaint(formatTokenCount(provider.Lifetime), dashYellow, true))
	}
	if provider.Sessions > 0 {
		parts = append(parts, dashPaint(fmt.Sprintf("%d sessions", provider.Sessions), usageMutedColor, false))
	}
	return strings.Join(parts, dashPaint("  ·  ", usageMutedColor, false))
}

// overviewBody is the panel body for one provider, quota first.
func overviewBody(provider overviewProvider, inner int, now time.Time) []string {
	if !provider.HasData {
		if provider.Err != "" {
			return []string{dashPaint("Unavailable: "+provider.Err, dashRed, false)}
		}
		return []string{dashPaint("Loading…", usageMutedColor, false)}
	}
	var lines []string
	for _, meter := range provider.Meters {
		lines = append(lines, overviewMeterLine(meter, inner, now))
	}
	for _, alert := range provider.Alerts {
		lines = append(lines, dashPaint(alert, dashRed, true))
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, overviewTokenLine(provider))
	for _, note := range provider.Notes {
		lines = append(lines, usageNote(note))
	}
	if provider.Err != "" {
		lines = append(lines, dashPaint("Refresh failed: "+provider.Err, dashRed, false))
	}
	return lines
}

func (m model) viewOverview(contentRows int) string {
	if contentRows < 1 {
		return ""
	}
	now := time.Now()
	var shown []overviewProvider
	hidden := 0
	for _, provider := range m.overviewProviders(now) {
		if provider.visible() {
			shown = append(shown, provider)
		} else {
			hidden++
		}
	}

	var lines []string
	if len(shown) == 0 {
		lines = dashBox("overview", []string{dashPaint("No provider usage found yet.", usageMutedColor, false)}, m.width, min(contentRows, 3), dashBlue)
	} else {
		// Wide terminals show paired cards; smaller terminals keep full-width meters.
		columns, gap := 1, 2
		if m.width >= 150 {
			columns = 2
		}
		cardWidth := (m.width - (columns-1)*gap) / columns
		inner := max(cardWidth-4, 1)
		bodies := make([][]string, len(shown))
		heights := make([]int, len(shown))
		for index, provider := range shown {
			bodies[index] = overviewBody(provider, inner, now)
			heights[index] = len(bodies[index]) + 2
		}
		// Equal-height cards per row keep borders aligned. Trim rows only when
		// needed, preserving at least one body line per visible card.
		rowHeights := make([]int, (len(shown)+columns-1)/columns)
		total := 0
		for index, height := range heights {
			row := index / columns
			rowHeights[row] = max(rowHeights[row], height)
		}
		for _, height := range rowHeights {
			total += height
		}
		spacing := 0
		if total+len(rowHeights)-1 <= contentRows {
			spacing = 1
			total += len(rowHeights) - 1
		}
		for row := len(rowHeights) - 1; total > contentRows && row >= 0; row-- {
			cut := min(total-contentRows, rowHeights[row]-3)
			rowHeights[row] -= cut
			total -= cut
		}
		for row, height := range rowHeights {
			if len(lines) > 0 && len(lines)+spacing+height > contentRows {
				break
			}
			if row > 0 && spacing > 0 {
				lines = append(lines, dashFit("", m.width, usagePanelColor))
			}
			cards := make([][]string, 0, columns)
			for index := row * columns; index < min((row+1)*columns, len(shown)); index++ {
				provider := shown[index]
				cards = append(cards, dashBox(toolLabel(provider.Name), bodies[index], cardWidth, height, provider.Accent))
			}
			for line := 0; line < height; line++ {
				parts := make([]string, len(cards))
				for index, card := range cards {
					parts[index] = card[line]
				}
				lines = append(lines, dashFit(strings.Join(parts, dashFit("", gap, usagePanelColor)), m.width, usagePanelColor))
			}
		}
		if hidden > 0 && len(lines) < contentRows {
			lines = append(lines, dashFit(dashPaint(fmt.Sprintf(" %d provider(s) with no local usage data are not shown", hidden), usageMutedColor, false), m.width, usagePanelColor))
		}
	}
	for len(lines) < contentRows {
		lines = append(lines, dashFit("", m.width, usagePanelColor))
	}
	return strings.Join(lines[:contentRows], "\n")
}

func (m model) overviewFooter() string {
	return renderFooterKeys([]footerKey{{"r", "refresh"}, {"tab", "switch"}, {"?", "help"}, {"q", "quit"}}, m.width)
}

// beginOverviewRefresh refreshes every provider that is not already loading.
func (m *model) beginOverviewRefresh() tea.Cmd {
	var cmds []tea.Cmd
	if !m.codexUsageLoading {
		m.codexUsageLoading = true
		cmds = append(cmds, refreshCodexUsage())
	}
	if !m.copilotUsageLoading {
		m.copilotUsageLoading = true
		cmds = append(cmds, refreshCopilotUsage())
	}
	if !m.claudeUsageLoading {
		m.claudeUsageLoading = true
		cmds = append(cmds, refreshClaudeUsage())
	}
	if !m.kimiUsageLoading {
		m.kimiUsageLoading = true
		cmds = append(cmds, refreshKimiUsage())
	}
	return tea.Batch(cmds...)
}
