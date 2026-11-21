// Package window answers "where did my 5-hour or weekly window go": it sums
// the token use of every session that was active inside one window, from the local
// transcripts only. Nothing is uploaded and no provider file is written.
package window

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/firekeeper-ai/firekeeper/internal/session"
	"github.com/firekeeper-ai/firekeeper/internal/transcript"
	"github.com/firekeeper-ai/firekeeper/internal/transcript/reader"
)

// Period is which provider limit window a report covers.
type Period string

const (
	PeriodFiveHour Period = "5h"
	PeriodWeek     Period = "week"
)

// ParsePeriod rejects anything but a known period.
func ParsePeriod(s string) (Period, error) {
	switch p := Period(s); p {
	case PeriodFiveHour, PeriodWeek:
		return p, nil
	}
	return "", fmt.Errorf("window: unknown period %q", s)
}

// Length is the rolling window providers meter against for p. An unknown
// period is treated as the 5-hour window.
func (p Period) Length() time.Duration {
	switch p {
	case PeriodWeek:
		return 7 * 24 * time.Hour
	case PeriodFiveHour:
		return Length
	default:
		return Length
	}
}

// bucket is the size of one row in the over-time breakdown.
func (p Period) bucket() time.Duration {
	switch p {
	case PeriodWeek:
		return 24 * time.Hour
	case PeriodFiveHour:
		return time.Hour
	default:
		return time.Hour
	}
}

// Label is the human name of the window.
func (p Period) Label() string {
	switch p {
	case PeriodWeek:
		return "weekly"
	case PeriodFiveHour:
		return "5-hour"
	default:
		return "5-hour"
	}
}

// Length is the 5-hour window providers meter against.
const Length = 5 * time.Hour

// ErrNoReader means the provider has no transcript reader to enumerate.
var ErrNoReader = errors.New("window: provider has no transcript reader")

// Basis says how the window bounds were chosen.
type Basis string

const (
	// BasisLimits: the provider reported when the window resets, so the
	// window is the Length ending at that reset.
	BasisLimits Basis = "provider_reset"
	// BasisRolling: no reset time was known, so the window is the last Length.
	BasisRolling Basis = "rolling"
)

// Tokens are summed token counts. Total is Input+Output+Cache.
type Tokens struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	Cache  int64 `json:"cache"`
	Total  int64 `json:"total"`
}

func (t *Tokens) add(e transcript.Tokens) {
	t.Input += e.Input
	t.Output += e.Output
	t.Cache += e.Cache
	t.Total += e.Input + e.Output + e.Cache
}

// Session is one session's share of the window.
type Session struct {
	SessionID string    `json:"session_id"`
	Title     string    `json:"title"`
	Project   string    `json:"project"`
	CWD       string    `json:"cwd"`
	FirstAt   time.Time `json:"first_at"`
	LastAt    time.Time `json:"last_at"`
	Turns     int       `json:"turns"` // assistant events in the window
	ToolCalls int       `json:"tool_calls"`
	Tokens    Tokens    `json:"tokens"`
	SharePct  float64   `json:"share_percent"` // of the window's total tokens
	ByModel   []Group   `json:"by_model"`
	// Activity has the session's tokens per hour (5-hour window) or day
	// (weekly), in the same buckets as the report; empty ones are left out.
	Activity []Bucket `json:"activity"`
}

// Group is a total for one model or project.
type Group struct {
	Name   string `json:"name"`
	Tokens Tokens `json:"tokens"`
}

// Bucket is one hour of the window.
type Bucket struct {
	Start  time.Time `json:"start"`
	Tokens Tokens    `json:"tokens"`
}

// Report is the breakdown of one window.
type Report struct {
	GeneratedAt time.Time           `json:"generated_at"`
	Provider    transcript.Provider `json:"provider"`
	Period      Period              `json:"period"`
	Basis       Basis               `json:"basis"`
	Start       time.Time           `json:"window_start"`
	End         time.Time           `json:"window_end"`
	// UsedPercent is the provider's own figure when it reported one.
	UsedPercent *float64  `json:"used_percent"`
	Total       Tokens    `json:"tokens"`
	Sessions    []Session `json:"sessions"`
	ByModel     []Group   `json:"by_model"`
	ByProject   []Group   `json:"by_project"`
	// Hourly is filled for the 5-hour window, Daily for the weekly one.
	Hourly []Bucket `json:"hourly,omitempty"`
	Daily  []Bucket `json:"daily,omitempty"`
	// Notes carry limitations, such as sessions that could not be read.
	Notes []string `json:"notes"`
}

// Options picks the provider and window.
type Options struct {
	Provider transcript.Provider
	// Period defaults to the 5-hour window.
	Period Period
	// ResetsAt, when non-zero, ends the window there; otherwise it ends at
	// now. UsedPercent is passed through to the report.
	ResetsAt    time.Time
	UsedPercent *float64
}

// Bounds returns the window for now and an optional reset time.
func Bounds(p Period, now, resetsAt time.Time) (start, end time.Time, basis Basis) {
	length := p.Length()
	if !resetsAt.IsZero() && resetsAt.After(now) && resetsAt.Before(now.Add(length+time.Minute)) {
		return resetsAt.Add(-length), resetsAt, BasisLimits
	}
	return now.Add(-length), now, BasisRolling
}

// Build reads the provider's transcripts and sums what fell inside the window.
func Build(ctx context.Context, now time.Time, opts Options) (Report, error) {
	source, ok := reader.For(opts.Provider)
	enumerator, canList := reader.EnumeratorFor(opts.Provider)
	if !ok || !canList {
		return Report{}, ErrNoReader
	}
	if opts.Period == "" {
		opts.Period = PeriodFiveHour
	}
	start, end, basis := Bounds(opts.Period, now, opts.ResetsAt)
	report := newReport(now, opts.Provider, opts.Period, basis, start, end)
	report.UsedPercent = opts.UsedPercent

	metas, err := enumerator.Enumerate(ctx, reader.EnumerateOptions{ModifiedAfter: start})
	var warning *session.Warning
	if err != nil && !errors.As(err, &warning) {
		return Report{}, err
	}
	if warning != nil {
		report.Notes = append(report.Notes, "some transcripts could not be read: "+warning.Error())
	}

	unreadable := 0
	acc := newAccumulator(&report)
	for _, meta := range metas {
		if ctx.Err() != nil {
			return Report{}, ctx.Err()
		}
		paths, err := source.Locate(meta)
		if err != nil {
			unreadable++
			continue
		}
		var events []transcript.Event
		for _, path := range paths {
			part, _, err := source.Read(path, 0)
			if err != nil {
				unreadable++
				continue
			}
			events = append(events, part...)
		}
		acc.session(meta, events)
	}
	if unreadable > 0 {
		report.Notes = append(report.Notes, "some session files could not be read and are not counted")
	}
	acc.finish()
	return report, nil
}

func newReport(now time.Time, provider transcript.Provider, period Period, basis Basis, start, end time.Time) Report {
	r := Report{
		GeneratedAt: now.UTC(), Provider: provider, Period: period, Basis: basis,
		Start: start.UTC(), End: end.UTC(),
		Sessions: []Session{}, ByModel: []Group{}, ByProject: []Group{}, Notes: []string{},
	}
	size := period.bucket()
	var buckets []Bucket
	for at := r.Start; at.Before(r.End); at = at.Add(size) {
		buckets = append(buckets, Bucket{Start: at})
	}
	if period == PeriodWeek {
		r.Daily = buckets
	} else {
		r.Hourly = buckets
	}
	return r
}

// buckets is the over-time breakdown for the report's period.
func (r *Report) buckets() []Bucket {
	if r.Period == PeriodWeek {
		return r.Daily
	}
	return r.Hourly
}

type accumulator struct {
	report   *Report
	models   map[string]*Tokens
	projects map[string]*Tokens
}

func newAccumulator(r *Report) *accumulator {
	return &accumulator{report: r, models: map[string]*Tokens{}, projects: map[string]*Tokens{}}
}

// session adds the events of one session that fall in the window. A session
// with no event in the window is left out.
func (a *accumulator) session(meta session.Meta, events []transcript.Event) {
	row := Session{
		SessionID: meta.ID, Title: meta.Title, CWD: meta.CWD,
		Project: reader.ProjectName(meta.CWD),
	}
	models := map[string]*Tokens{}
	var activity []Bucket
	if size := a.report.Period.bucket(); size > 0 {
		for _, b := range a.report.buckets() {
			activity = append(activity, Bucket{Start: b.Start})
		}
	}
	for _, e := range events {
		if e.TS == nil || e.TS.Before(a.report.Start) || !e.TS.Before(a.report.End) {
			continue
		}
		ts := e.TS.UTC()
		if row.FirstAt.IsZero() || ts.Before(row.FirstAt) {
			row.FirstAt = ts
		}
		if ts.After(row.LastAt) {
			row.LastAt = ts
		}
		switch e.Role {
		case transcript.RoleAssistant:
			row.Turns++
		case transcript.RoleToolCall:
			row.ToolCalls++
		case transcript.RoleUser, transcript.RoleToolResult, transcript.RoleSystem, transcript.RoleMeta:
		}
		row.Tokens.add(e.Tokens)
		a.report.Total.add(e.Tokens)
		buckets := a.report.buckets()
		if i := int(ts.Sub(a.report.Start) / a.report.Period.bucket()); i >= 0 && i < len(buckets) {
			buckets[i].Tokens.add(e.Tokens)
			activity[i].Tokens.add(e.Tokens)
		}
		if e.Tokens.Input+e.Tokens.Output+e.Tokens.Cache > 0 {
			model := "unknown"
			if e.Model != nil && *e.Model != "" {
				model = *e.Model
			}
			add(a.models, model, e.Tokens)
			add(models, model, e.Tokens)
			add(a.projects, emptyOr(row.Project, "unknown"), e.Tokens)
		}
	}
	if row.FirstAt.IsZero() {
		return
	}
	row.ByModel = groups(models)
	row.Activity = []Bucket{}
	for _, b := range activity {
		if b.Tokens.Total > 0 {
			row.Activity = append(row.Activity, b)
		}
	}
	if row.Title == "" {
		row.Title = shortID(row.SessionID)
	}
	a.report.Sessions = append(a.report.Sessions, row)
}

func (a *accumulator) finish() {
	r := a.report
	for i := range r.Sessions {
		if r.Total.Total > 0 {
			r.Sessions[i].SharePct = float64(r.Sessions[i].Tokens.Total) / float64(r.Total.Total) * 100
		}
	}
	sort.Slice(r.Sessions, func(i, j int) bool {
		if r.Sessions[i].Tokens.Total != r.Sessions[j].Tokens.Total {
			return r.Sessions[i].Tokens.Total > r.Sessions[j].Tokens.Total
		}
		return r.Sessions[i].SessionID < r.Sessions[j].SessionID
	})
	r.ByModel = groups(a.models)
	r.ByProject = groups(a.projects)
}

func add(m map[string]*Tokens, key string, t transcript.Tokens) {
	if m[key] == nil {
		m[key] = &Tokens{}
	}
	m[key].add(t)
}

func groups(m map[string]*Tokens) []Group {
	out := make([]Group, 0, len(m))
	for name, t := range m {
		out = append(out, Group{Name: name, Tokens: *t})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tokens.Total != out[j].Tokens.Total {
			return out[i].Tokens.Total > out[j].Tokens.Total
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func emptyOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
