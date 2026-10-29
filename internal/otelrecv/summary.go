package otelrecv

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

type sourceTotals struct {
	Requests   int
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	CostUSD    float64
}

func (s sourceTotals) Tokens() int64 {
	return s.Input + s.Output + s.CacheRead + s.CacheWrite
}

func (s sourceTotals) add(rec Request) sourceTotals {
	s.Requests++
	s.Input += rec.InputTokens
	s.Output += rec.OutputTokens
	s.CacheRead += rec.CacheReadTokens
	s.CacheWrite += rec.CacheCreationTokens
	s.CostUSD += rec.CostUSD
	return s
}

type sessionAgg struct {
	id      string
	sources map[string]sourceTotals
}

func (s sessionAgg) total() sourceTotals {
	var sum sourceTotals
	for _, source := range s.sources {
		sum.Requests += source.Requests
		sum.Input += source.Input
		sum.Output += source.Output
		sum.CacheRead += source.CacheRead
		sum.CacheWrite += source.CacheWrite
		sum.CostUSD += source.CostUSD
	}
	return sum
}

func aggregate(records []Request) []sessionAgg {
	byID := map[string]*sessionAgg{}
	var order []string
	for _, rec := range records {
		id := rec.SessionID
		agg := byID[id]
		if agg == nil {
			agg = &sessionAgg{id: id, sources: map[string]sourceTotals{}}
			byID[id] = agg
			order = append(order, id)
		}
		source := rec.QuerySource
		agg.sources[source] = agg.sources[source].add(rec)
	}
	slices.Sort(order)
	out := make([]sessionAgg, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}

// WriteSummary prints per-session token totals by query source.
// It counts agent_summary and every other source that is not the main thread.
func WriteSummary(w io.Writer, records []Request) error {
	if len(records) == 0 {
		_, err := fmt.Fprintln(w, "no api_request events recorded")
		return err
	}
	for _, session := range aggregate(records) {
		if err := writeSessionSummary(w, session); err != nil {
			return err
		}
	}
	return nil
}

func writeSessionSummary(w io.Writer, session sessionAgg) error {
	label := session.id
	if label == "" {
		label = "(none)"
	}
	if _, err := fmt.Fprintf(w, "session %s\n", label); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  %-20s %8s %10s %8s %12s %12s %10s %12s\n",
		"query_source", "requests", "input", "output", "cache_read", "cache_write", "total", "cost_usd"); err != nil {
		return err
	}
	names := make([]string, 0, len(session.sources))
	for name := range session.sources {
		names = append(names, name)
	}
	slices.Sort(names)
	agentSummary := 0
	otherNonMain := 0
	for _, name := range names {
		totals := session.sources[name]
		shown := name
		if shown == "" {
			shown = "(none)"
		}
		if _, err := fmt.Fprintf(w, "  %-20s %8d %10d %8d %12d %12d %10d %12.6f\n",
			fit(shown, 20), totals.Requests, totals.Input, totals.Output, totals.CacheRead, totals.CacheWrite, totals.Tokens(), totals.CostUSD); err != nil {
			return err
		}
		switch {
		case name == "agent_summary":
			agentSummary += totals.Requests
		case !isMainQuerySource(name):
			otherNonMain += totals.Requests
		}
	}
	sum := session.total()
	if _, err := fmt.Fprintf(w, "  %-20s %8d %10d %8d %12d %12d %10d %12.6f\n",
		"total", sum.Requests, sum.Input, sum.Output, sum.CacheRead, sum.CacheWrite, sum.Tokens(), sum.CostUSD); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  agent_summary requests: %d\n", agentSummary); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "  other non-main requests: %d\n", otherNonMain)
	return err
}

func fit(value string, width int) string {
	if len(value) <= width {
		return value
	}
	if width <= 3 {
		return value[:width]
	}
	return value[:width-3] + "..."
}

func sessionLabel(id string) string {
	if strings.TrimSpace(id) == "" {
		return "(none)"
	}
	return id
}
