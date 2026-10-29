package otelrecv

import (
	"fmt"
	"io"
	"slices"

	"github.com/DanBradbury/firekeeper/internal/session"
)

// WriteReconcile compares telemetry totals with transcript-derived totals.
// load reads one session. A false Found means the transcript is not on disk.
// The report does not include prompt or transcript text.
func WriteReconcile(w io.Writer, records []Request, load func(sessionID string) (SessionUsage, error)) error {
	if len(records) == 0 {
		_, err := fmt.Fprintln(w, "no api_request events in the file")
		return err
	}
	grouped := map[string][]Request{}
	for _, rec := range records {
		grouped[rec.SessionID] = append(grouped[rec.SessionID], rec)
	}
	for _, session := range aggregate(records) {
		if err := writeSessionReconcile(w, session, grouped[session.id], load); err != nil {
			return err
		}
	}
	return nil
}

func writeSessionReconcile(w io.Writer, agg sessionAgg, records []Request, load func(sessionID string) (SessionUsage, error)) error {
	if _, err := fmt.Fprintf(w, "session %s\n", sessionLabel(agg.id)); err != nil {
		return err
	}
	sum := agg.total()
	if _, err := fmt.Fprintf(w, "  telemetry: %d tokens, %d requests, cost_usd %.6f\n", sum.Tokens(), sum.Requests, sum.CostUSD); err != nil {
		return err
	}
	names := make([]string, 0, len(agg.sources))
	for name := range agg.sources {
		names = append(names, name)
	}
	slices.Sort(names)
	agentSummary := 0
	otherNonMain := 0
	for _, name := range names {
		totals := agg.sources[name]
		shown := name
		if shown == "" {
			shown = "(none)"
		}
		if _, err := fmt.Fprintf(w, "    %s: %d tokens, %s\n", shown, totals.Tokens(), requestCount(totals.Requests)); err != nil {
			return err
		}
		switch {
		case name == "agent_summary":
			agentSummary += totals.Requests
		case !isMainQuerySource(name):
			otherNonMain += totals.Requests
		}
	}

	switch {
	case !session.ValidUUID(agg.id):
		if _, err := fmt.Fprintln(w, "  transcript: not read (session id is not a UUID)"); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, "  modelUsage: not read"); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, "  gap telemetry-transcript: unknown"); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, "  gap telemetry-modelUsage: unknown"); err != nil {
			return err
		}
	case load == nil:
		if _, err := fmt.Fprintln(w, "  transcript: not read"); err != nil {
			return err
		}
	default:
		usage, err := load(agg.id)
		if err != nil {
			if _, werr := fmt.Fprintln(w, "  transcript: unreadable"); werr != nil {
				return werr
			}
			if _, werr := fmt.Fprintln(w, "  gap telemetry-transcript: unknown"); werr != nil {
				return werr
			}
		} else if !usage.Found {
			if _, werr := fmt.Fprintln(w, "  transcript: not found"); werr != nil {
				return werr
			}
			if _, werr := fmt.Fprintln(w, "  modelUsage: not found"); werr != nil {
				return werr
			}
			if _, werr := fmt.Fprintln(w, "  gap telemetry-transcript: unknown"); werr != nil {
				return werr
			}
			if _, werr := fmt.Fprintln(w, "  gap telemetry-modelUsage: unknown"); werr != nil {
				return werr
			}
		} else {
			if _, werr := fmt.Fprintf(w, "  transcript: %d tokens, %d messages, %d subagent files (input %d, output %d, cache_read %d, cache_write %d)\n",
				usage.Total(), usage.Messages, usage.SubagentFiles, usage.Input, usage.Output, usage.CacheRead, usage.CacheWrite); werr != nil {
				return werr
			}
			gap := Merge(usage, records)
			if _, werr := fmt.Fprintf(w, "  gap telemetry-transcript: %d\n", gap.Added); werr != nil {
				return werr
			}
			for _, name := range gap.Sources() {
				if _, werr := fmt.Fprintf(w, "    untranscribed %s: %d tokens\n", name, gap.BySource[name]); werr != nil {
					return werr
				}
			}
			if usage.ModelUsageFound {
				if _, werr := fmt.Fprintf(w, "  modelUsage: %d tokens (input %d, output %d, cache_read %d, cache_write %d)\n",
					usage.ModelTotal(), usage.ModelInput, usage.ModelOutput, usage.ModelCacheRead, usage.ModelCacheWrite); werr != nil {
					return werr
				}
				if _, werr := fmt.Fprintf(w, "  gap telemetry-modelUsage: %d\n", sum.Tokens()-usage.ModelTotal()); werr != nil {
					return werr
				}
			} else {
				if _, werr := fmt.Fprintln(w, "  modelUsage: no cost-state record"); werr != nil {
					return werr
				}
				if _, werr := fmt.Fprintln(w, "  gap telemetry-modelUsage: unknown"); werr != nil {
					return werr
				}
			}
		}
	}
	if _, err := fmt.Fprintf(w, "  agent_summary requests: %d\n", agentSummary); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "  other non-main requests: %d\n", otherNonMain)
	return err
}

func requestCount(n int) string {
	if n == 1 {
		return "1 request"
	}
	return fmt.Sprintf("%d requests", n)
}
