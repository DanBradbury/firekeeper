package reporter

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// maxConsecutiveFailures stops a backfill after this many failed requests in
// a row, so a down server is not retried for every remaining session.
const maxConsecutiveFailures = 5

// Backoff bounds between retries of a failed request.
const (
	backoffBase = time.Second
	backoffMax  = 30 * time.Second
)

// estimatedRequestBytes is the share of a request's byte limit a planned
// transcript byte is assumed to take once encoded. Events carry both text
// and the raw record, so encoding grows them.
const estimatedRequestBytes = maxBatchBytes / 2

// BackfillConfig configures one backfill pass. Config.Since and After both
// narrow it by transcript modification time; the later cutoff wins.
type BackfillConfig struct {
	Config
	// After drops transcripts not modified after it. Zero keeps all.
	After time.Time
	// Limit uploads at most this many sessions, newest first. Zero means no
	// limit.
	Limit int
	// Confirm is called after the plan is written to Out and before anything
	// is uploaded. Returning false uploads nothing. Nil uploads without
	// asking.
	Confirm func(Plan) bool
	// Progress receives one line per uploaded session. Nil discards them.
	Progress io.Writer

	// Enumerators and Sleep replace transcript.EnumeratorFor and a
	// context-aware sleep in tests.
	Enumerators func(transcript.Provider) (transcript.Enumerator, bool)
	Sleep       func(context.Context, time.Duration) error
}

// Plan is what a backfill would upload. It holds counts and dates only,
// never transcript text.
type Plan struct {
	Providers []ProviderPlan
	// Notes explain providers or rules the plan could not apply.
	Notes []string
	// Uploading reports whether the pass will upload once confirmed.
	Uploading bool
	// Skipped lists the sessions excluded from the plan and why, without
	// their transcripts having been opened.
	Skipped []SkippedSession

	sessions []plannedSession
}

// SkippedSession is one session a backfill will not read.
type SkippedSession struct {
	Provider  transcript.Provider
	SessionID string
	Project   string
	Reason    string
}

// ProviderPlan totals one provider's share of a plan.
type ProviderPlan struct {
	Provider transcript.Provider
	Sessions int
	Files    int
	// Bytes is the transcript data not yet uploaded.
	Bytes int64
	// Events is the number of transcript records not yet uploaded.
	Events int64
	// Requests estimates the ingest requests the upload takes.
	Requests int
	// Oldest and Newest are the range of the sessions' last activity.
	Oldest, Newest time.Time
	// Excluded counts sessions dropped by .firekeeper-ignore, Config.Exclude,
	// or an unknown working directory; UpToDate those already fully uploaded; OverLimit
	// those left for a later pass by Limit.
	Excluded, UpToDate, OverLimit int
}

type plannedSession struct {
	meta     session.Meta
	activity time.Time
	files    int
	bytes    int64
	events   int64
	requests int
}

// Total sums the plan over every provider.
func (p Plan) Total() ProviderPlan {
	var t ProviderPlan
	for _, pp := range p.Providers {
		t.Sessions += pp.Sessions
		t.Files += pp.Files
		t.Bytes += pp.Bytes
		t.Events += pp.Events
		t.Requests += pp.Requests
		t.Excluded += pp.Excluded
		t.UpToDate += pp.UpToDate
		t.OverLimit += pp.OverLimit
		if !pp.Oldest.IsZero() && (t.Oldest.IsZero() || pp.Oldest.Before(t.Oldest)) {
			t.Oldest = pp.Oldest
		}
		if pp.Newest.After(t.Newest) {
			t.Newest = pp.Newest
		}
	}
	return t
}

// Write prints the plan as one line per provider and a total.
func (p Plan) Write(w io.Writer) {
	if len(p.Providers) == 0 {
		fmt.Fprintln(w, "backfill plan: no providers to enumerate")
	}
	for _, pp := range p.Providers {
		fmt.Fprintf(w, "%-8s %s\n", pp.Provider, pp.line())
	}
	if len(p.Providers) > 1 {
		fmt.Fprintf(w, "%-8s %s\n", "total", p.Total().line())
	}
	for _, s := range p.Skipped {
		project := s.Project
		if project == "" {
			project = "-"
		}
		fmt.Fprintf(w, "skipped %s %s %s: %s\n", s.Provider, s.SessionID, project, s.Reason)
	}
	for _, note := range p.Notes {
		fmt.Fprintf(w, "note: %s\n", note)
	}
}

func (pp ProviderPlan) line() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s, %d %s, %s, ~%d events, ~%d %s",
		pp.Sessions, plural(pp.Sessions, "session", "sessions"),
		pp.Files, plural(pp.Files, "file", "files"), formatBytes(pp.Bytes),
		pp.Events, pp.Requests, plural(pp.Requests, "request", "requests"))
	if !pp.Oldest.IsZero() {
		fmt.Fprintf(&b, ", %s to %s", pp.Oldest.Local().Format("2006-01-02"), pp.Newest.Local().Format("2006-01-02"))
	}
	var skipped []string
	if pp.UpToDate > 0 {
		skipped = append(skipped, fmt.Sprintf("%d already uploaded", pp.UpToDate))
	}
	if pp.Excluded > 0 {
		skipped = append(skipped, fmt.Sprintf("%d excluded", pp.Excluded))
	}
	if pp.OverLimit > 0 {
		skipped = append(skipped, fmt.Sprintf("%d over --limit", pp.OverLimit))
	}
	if len(skipped) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(skipped, ", "))
	}
	return b.String()
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// BackfillResult is the outcome of Backfill.
type BackfillResult struct {
	Plan Plan
	// Sessions holds one result per session uploaded, in upload order.
	Sessions []SessionResult
	// Declined reports that Confirm returned false.
	Declined bool
	// Warning holds enumeration and discovery failures that did not stop
	// the pass.
	Warning error
}

// Failed reports whether any session hit an error.
func (r BackfillResult) Failed() bool {
	return Summary{Sessions: r.Sessions}.Failed()
}

// ErrTooManyFailures stops a backfill after maxConsecutiveFailures failed
// requests in a row.
var ErrTooManyFailures = fmt.Errorf("stopped after %d consecutive failed requests", maxConsecutiveFailures)

// Backfill uploads the sessions already on this machine, including ended
// ones, in one pass. It enumerates transcripts with each provider's
// transcript.Enumerator, drops excluded sessions and transcripts already
// fully uploaded, and builds a Plan. Without upload (DryRun, or no provider
// allowlisted) it stops there and opens no network connection. Otherwise,
// once Confirm agrees, it uploads newest sessions first through the same
// redact, batch, and ingest path as RunOnce, sharing its offsets in
// state.json, so a rerun resumes and a later RunOnce sends only new events.
// Sessions still running keep the state discovery reports for them.
func Backfill(ctx context.Context, cfg BackfillConfig) (BackfillResult, error) {
	r, err := newRun(ctx, cfg.Config)
	if err != nil {
		return BackfillResult{}, err
	}
	var warnings []string
	plan, err := r.plan(ctx, cfg, &warnings)
	result := BackfillResult{Plan: plan}
	defer func() { result.Warning = joinWarnings(warnings) }()
	if err != nil {
		return result, err
	}
	if cfg.Out != nil {
		plan.Write(cfg.Out)
	}
	if !r.upload || len(plan.sessions) == 0 {
		return result, nil
	}
	if cfg.Confirm != nil && !cfg.Confirm(plan) {
		result.Declined = true
		return result, nil
	}

	metas, err := r.withLive(ctx, plan.sessions, &warnings)
	if err != nil {
		return result, err
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	failures := 0
	var lastErr error
	r.send = func(ctx context.Context, machineID string, meta ingestMeta, events []transcript.Event) (int, error) {
		for {
			dups, err := r.post(ctx, machineID, meta, events)
			if err == nil {
				failures = 0
				return dups, nil
			}
			if ctx.Err() != nil {
				return 0, err
			}
			failures++
			lastErr = err
			if failures >= maxConsecutiveFailures || !retryable(err) {
				return 0, err
			}
			wait := min(backoffBase<<(failures-1), backoffMax)
			if err := sleep(ctx, wait); err != nil {
				return 0, err
			}
		}
	}
	for i, meta := range metas {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		res := r.session(ctx, meta)
		result.Sessions = append(result.Sessions, res)
		if cfg.Progress != nil {
			fmt.Fprintf(cfg.Progress, "[%d/%d] %s\n", i+1, len(metas), r.status(res))
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if failures >= maxConsecutiveFailures {
			return result, fmt.Errorf("%w; last error: %v", ErrTooManyFailures, lastErr)
		}
	}
	return result, nil
}

// retryable reports whether a failed request may succeed if sent again.
// Client errors other than timeouts and rate limits will not.
func retryable(err error) bool {
	var status *StatusError
	if errors.As(err, &status) {
		return status.Status >= 500 || status.Status == http.StatusRequestTimeout || status.Status == http.StatusTooManyRequests
	}
	return true
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func joinWarnings(warnings []string) error {
	if len(warnings) == 0 {
		return nil
	}
	return &session.Warning{Message: strings.Join(warnings, "; ")}
}

// plan enumerates every allowlisted provider, or every provider when none
// is, and keeps the sessions with transcript data not yet uploaded.
func (r *run) plan(ctx context.Context, cfg BackfillConfig, warnings *[]string) (Plan, error) {
	enumerators := cfg.Enumerators
	if enumerators == nil {
		enumerators = transcript.EnumeratorFor
	}
	providers := cfg.Providers
	if len(providers) == 0 {
		providers = transcript.Registered()
	}
	cutoff := cfg.After
	if cfg.Since > 0 {
		if since := r.now().Add(-cfg.Since); since.After(cutoff) {
			cutoff = since
		}
	}
	plan := Plan{Uploading: r.upload}

	var all []plannedSession
	byProvider := map[transcript.Provider]*ProviderPlan{}
	var order []transcript.Provider
	for _, provider := range providers {
		enumerator, ok := enumerators(provider)
		if !ok {
			plan.Notes = append(plan.Notes, fmt.Sprintf("%s: no historical enumeration; use report or daemon for its live sessions", provider))
			continue
		}
		source, ok := r.sources(provider)
		if !ok {
			plan.Notes = append(plan.Notes, fmt.Sprintf("%s: no transcript reader", provider))
			continue
		}
		pp := &ProviderPlan{Provider: provider}
		byProvider[provider] = pp
		order = append(order, provider)
		metas, err := enumerator.Enumerate(ctx, transcript.EnumerateOptions{
			ModifiedAfter: cutoff,
			Skip: func(meta session.Meta) bool {
				reason := r.skipReason(meta)
				if reason == "" {
					return false
				}
				pp.Excluded++
				plan.Skipped = append(plan.Skipped, SkippedSession{Provider: provider, SessionID: meta.ID, Project: meta.Project, Reason: reason})
				return true
			},
		})
		if err != nil {
			if ctx.Err() != nil {
				return plan, ctx.Err()
			}
			var warning *session.Warning
			if !errors.As(err, &warning) {
				*warnings = append(*warnings, fmt.Sprintf("%s: %v", provider, err))
				continue
			}
			*warnings = append(*warnings, fmt.Sprintf("%s: %v", provider, warning))
		}
		for _, meta := range metas {
			if err := ctx.Err(); err != nil {
				return plan, err
			}
			ps, err := r.planSession(ctx, source, meta)
			if err != nil {
				*warnings = append(*warnings, fmt.Sprintf("%s %s: %v", provider, meta.ID, err))
				continue
			}
			if ps.files == 0 {
				pp.UpToDate++
				continue
			}
			all = append(all, ps)
		}
	}

	sortNewestFirst(all)
	for i, ps := range all {
		pp := byProvider[transcript.Provider(ps.meta.Provider)]
		if cfg.Limit > 0 && i >= cfg.Limit {
			pp.OverLimit++
			continue
		}
		plan.sessions = append(plan.sessions, ps)
		pp.Sessions++
		pp.Files += ps.files
		pp.Bytes += ps.bytes
		pp.Events += ps.events
		pp.Requests += ps.requests
		if !ps.activity.IsZero() {
			if pp.Oldest.IsZero() || ps.activity.Before(pp.Oldest) {
				pp.Oldest = ps.activity
			}
			if ps.activity.After(pp.Newest) {
				pp.Newest = ps.activity
			}
		}
	}
	for _, provider := range order {
		plan.Providers = append(plan.Providers, *byProvider[provider])
	}
	return plan, nil
}

// planSession sizes the transcript data of meta not yet uploaded. A file
// whose size and modification time match its saved position has been read
// to the end and is left out.
func (r *run) planSession(ctx context.Context, source transcript.TranscriptSource, meta session.Meta) (plannedSession, error) {
	ps := plannedSession{meta: meta}
	if meta.LastActivityAt != nil {
		ps.activity = *meta.LastActivityAt
	}
	paths, err := source.Locate(meta)
	if err != nil {
		return ps, err
	}
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			return ps, errors.New("resolve transcript path")
		}
		info, err := os.Stat(abs)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return ps, fmt.Errorf("stat transcript: %w", cause(err))
		}
		if meta.LastActivityAt == nil && info.ModTime().After(ps.activity) {
			ps.activity = info.ModTime()
		}
		pos, known := r.state.Files[abs]
		if known && pos.Size == info.Size() && pos.Mtime.Equal(info.ModTime()) {
			continue
		}
		from := int64(0)
		if known && info.Size() >= pos.Size && pos.Offset <= info.Size() {
			from = pos.Offset
		}
		lines, err := countLines(ctx, abs, from)
		if err != nil {
			return ps, err
		}
		if lines == 0 && from > 0 {
			continue // only a partial trailing record past the offset
		}
		bytes := info.Size() - from
		ps.files++
		ps.bytes += bytes
		ps.events += lines
		ps.requests += int(max((lines+maxBatchEvents-1)/maxBatchEvents, (bytes+estimatedRequestBytes-1)/estimatedRequestBytes, 1))
	}
	return ps, nil
}

// countLines counts the complete records past from in a JSONL transcript,
// the events a read from there would return. It reads bytes only and keeps
// none of them.
func countLines(ctx context.Context, path string, from int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open transcript: %w", cause(err))
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek transcript: %w", cause(err))
	}
	reader := bufio.NewReaderSize(f, 64<<10)
	buf := make([]byte, 64<<10)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		k, err := reader.Read(buf)
		n += int64(bytes.Count(buf[:k], []byte{'\n'}))
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return 0, fmt.Errorf("read transcript: %w", cause(err))
		}
	}
}

func sortNewestFirst(sessions []plannedSession) {
	metas := make([]session.Meta, len(sessions))
	fallback := map[string]time.Time{}
	byKey := map[string]plannedSession{}
	for i, ps := range sessions {
		metas[i] = ps.meta
		fallback[ps.meta.RolloutPath] = ps.activity
		byKey[ps.meta.Provider+"\x00"+ps.meta.ID] = ps
	}
	transcript.SortNewestFirst(metas, fallback)
	for i, meta := range metas {
		sessions[i] = byKey[meta.Provider+"\x00"+meta.ID]
	}
}

// withLive returns the planned sessions ready to upload: each takes the
// machine id, and a session discovery still sees running keeps its live
// state and fills metadata the enumeration lacked. A discovery failure is a
// warning; the sessions then upload as ended.
func (r *run) withLive(ctx context.Context, planned []plannedSession, warnings *[]string) ([]session.Meta, error) {
	machineID, err := session.MachineID(r.cfg.Home)
	if err != nil {
		return nil, err
	}
	live := map[string]session.Meta{}
	found, err := r.discover(ctx, session.Options{Home: r.cfg.Home})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		*warnings = append(*warnings, fmt.Sprintf("live sessions: %v", err))
	}
	for _, meta := range found {
		if meta.ID != "" {
			live[liveKey(meta)] = meta
		}
	}
	metas := make([]session.Meta, len(planned))
	for i, ps := range planned {
		meta := ps.meta
		meta.MachineID = machineID
		if l, ok := live[liveKey(meta)]; ok {
			meta = mergeLive(meta, l)
		}
		metas[i] = meta
	}
	return metas, nil
}

func liveKey(meta session.Meta) string {
	return meta.Provider + "\x00" + strings.ToLower(meta.ID)
}

// mergeLive overlays a running session's discovered metadata on its
// enumerated metadata. The transcript path stays the enumerated one.
func mergeLive(meta, live session.Meta) session.Meta {
	meta.State = live.State
	if live.MachineID != "" {
		meta.MachineID = live.MachineID
	}
	if live.LastActivityAt != nil && (meta.LastActivityAt == nil || live.LastActivityAt.After(*meta.LastActivityAt)) {
		meta.LastActivityAt = live.LastActivityAt
	}
	fill := func(dst *string, src string) {
		if *dst == "" {
			*dst = src
		}
	}
	fill(&meta.CWD, live.CWD)
	fill(&meta.Project, live.Project)
	fill(&meta.Branch, live.Branch)
	fill(&meta.Model, live.Model)
	fill(&meta.Title, live.Title)
	if meta.StartedAt == nil {
		meta.StartedAt = live.StartedAt
	}
	return meta
}
