package otelrecv

import "slices"

// Supplement is the telemetry that transcripts do not already cover.
// Exact is the transcript total plus those requests. A query source that
// is the main thread, or a subagent source with no unmatched request id,
// is left to the transcript so it is not counted twice.
type Supplement struct {
	Transcript int64
	Added      int64
	Exact      int64
	BySource   map[string]int64
	// Covered is true when at least one telemetry request was seen for the
	// session, even if every request was already in a transcript.
	Covered bool
}

// supplementalSources are Claude Code query sources that are not written
// into the session or subagent JSONL. compact is included only when its
// request id is not already in a transcript.
var supplementalSources = map[string]bool{
	"agent_summary":          true,
	"prompt_suggestion":      true,
	"generate_session_title": true,
}

// Merge adds telemetry requests that the transcript does not already count.
// records are one session. usage may be the zero value when no transcript
// file was found; then Exact is the sum of every telemetry request.
func Merge(usage SessionUsage, records []Request) Supplement {
	out := Supplement{
		Transcript: usage.Total(),
		BySource:   map[string]int64{},
		Covered:    len(records) > 0,
	}
	if !usage.Found && len(records) > 0 {
		var all int64
		for _, rec := range records {
			all += rec.Tokens()
		}
		out.Exact = all
		out.Added = all
		for _, rec := range records {
			out.BySource[rec.QuerySource] += rec.Tokens()
		}
		return out
	}
	for _, rec := range records {
		if !includeTelemetry(rec, usage.RequestIDs) {
			continue
		}
		tokens := rec.Tokens()
		out.Added += tokens
		out.BySource[rec.QuerySource] += tokens
	}
	out.Exact = out.Transcript + out.Added
	return out
}

func includeTelemetry(rec Request, ids map[string]bool) bool {
	if rec.RequestID != "" && ids[rec.RequestID] {
		return false
	}
	if isMainQuerySource(rec.QuerySource) {
		return false
	}
	if supplementalSources[rec.QuerySource] {
		return true
	}
	if rec.QuerySource == "compact" {
		return true
	}
	// Unknown non-main sources, including subagent names such as
	// agent:builtin:general-purpose, are added only when a request id
	// proves they are not in a transcript. Without one, the subagent
	// file is the source.
	return rec.RequestID != ""
}

// Sources returns query sources that contributed tokens, in a stable order
// with the known background sources first.
func (s Supplement) Sources() []string {
	order := []string{"agent_summary", "prompt_suggestion", "generate_session_title", "compact"}
	var out []string
	seen := map[string]bool{}
	for _, name := range order {
		if s.BySource[name] > 0 {
			out = append(out, name)
			seen[name] = true
		}
	}
	var rest []string
	for name, tokens := range s.BySource {
		if tokens > 0 && !seen[name] {
			rest = append(rest, name)
		}
	}
	slices.Sort(rest)
	return append(out, rest...)
}
