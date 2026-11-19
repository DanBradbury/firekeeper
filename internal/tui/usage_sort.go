package tui

import (
	"cmp"
	"slices"
	"strings"
)

// usage_sort.go orders the Usage tab's session list by its TOKENS or AGO
// column. t and a (or a click on the column header) cycle that column through
// descending, ascending and back to the provider's own order, newest first.
// One order applies to every provider's list, and the list's title and the
// column header say which is applied.

// usageSortColumn is the session list column the rows are ordered by.
type usageSortColumn int

const (
	// usageSortNone keeps the provider's order, newest first.
	usageSortNone usageSortColumn = iota
	usageSortTokens
	usageSortUpdated
)

// usageSort is the applied order. The first press of a column sorts it
// descending: most tokens, or most recent first.
type usageSort struct {
	column    usageSortColumn
	ascending bool
}

// next is the order after pressing column's key or clicking its header.
func (s usageSort) next(column usageSortColumn) usageSort {
	switch {
	case column != usageSortTokens && column != usageSortUpdated:
		return s
	case s.column != column:
		return usageSort{column: column}
	case !s.ascending:
		return usageSort{column: column, ascending: true}
	default:
		return usageSort{}
	}
}

// label names the order for the list title; the provider order has none.
func (s usageSort) label() string {
	switch s.column {
	case usageSortTokens:
		if s.ascending {
			return "fewest tokens"
		}
		return "most tokens"
	case usageSortUpdated:
		if s.ascending {
			return "oldest"
		}
		return "newest"
	case usageSortNone:
		return ""
	default:
		return ""
	}
}

// arrow marks a sorted column header with the direction its values run.
// Most recent first means the AGO values grow down the list.
func (s usageSort) arrow(column usageSortColumn) string {
	if s.column != column || column == usageSortNone {
		return ""
	}
	descending := !s.ascending
	if column == usageSortUpdated {
		descending = !descending
	}
	if descending {
		return "▼"
	}
	return "▲"
}

// sortUsageSessions returns sessions in order s, leaving the input as is.
// Ties keep the more recent session first, then sort by id.
func sortUsageSessions(sessions []usageSession, s usageSort) []usageSession {
	if s.column != usageSortTokens && s.column != usageSortUpdated {
		return sessions
	}
	sorted := slices.Clone(sessions)
	slices.SortStableFunc(sorted, func(a, b usageSession) int {
		order := 0
		switch s.column {
		case usageSortTokens:
			order = cmp.Compare(a.Tokens, b.Tokens)
		case usageSortUpdated:
			order = a.UpdatedAt.Compare(b.UpdatedAt)
		case usageSortNone:
		}
		if !s.ascending {
			order = -order
		}
		if order != 0 {
			return order
		}
		if recent := b.UpdatedAt.Compare(a.UpdatedAt); recent != 0 {
			return recent
		}
		return strings.Compare(a.ID, b.ID)
	})
	return sorted
}

// cycleUsageSort advances column's order and keeps the selected session
// selected. Other providers' lists start again at the top.
func (m *model) cycleUsageSort(column usageSortColumn) {
	next := m.usageSort.next(column)
	if next == m.usageSort {
		return
	}
	selected := ""
	if page := m.usagePage(); len(page.Sessions) > 0 {
		selected = page.Sessions[page.Cursor].ID
	}
	m.usageSort = next
	m.codexHistoryCursor, m.copilotHistoryCursor, m.kimiHistoryCursor, m.claudeHistoryCursor = 0, 0, 0, 0
	for index, session := range m.usagePage().Sessions {
		if session.ID == selected {
			*m.usageCursorRef() = index
			break
		}
	}
}

// usageSortKey is the key that cycles column, as the header click replays it.
func usageSortKey(column usageSortColumn) rune {
	switch column {
	case usageSortTokens:
		return 't'
	case usageSortUpdated:
		return 'a'
	case usageSortNone:
		return 0
	default:
		return 0
	}
}
