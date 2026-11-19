package window

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/firekeeper-ai/firekeeper/internal/session"
	"github.com/firekeeper-ai/firekeeper/internal/transcript"
	"github.com/firekeeper-ai/firekeeper/internal/transcript/reader"
)

type fakeSource struct {
	metas  []session.Meta
	events map[string][]transcript.Event
}

func (f *fakeSource) Locate(m session.Meta) ([]string, error) { return []string{m.ID}, nil }
func (f *fakeSource) Read(path string, _ int64) ([]transcript.Event, int64, error) {
	return f.events[path], 0, nil
}
func (f *fakeSource) Enumerate(context.Context, reader.EnumerateOptions) ([]session.Meta, error) {
	return f.metas, nil
}

// shared is registered once: the reader registry panics on a second Register.
var shared = func() *fakeSource {
	f := &fakeSource{}
	reader.Register(transcript.ProviderClaude, f)
	return f
}()

var now = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func ev(at time.Time, role transcript.Role, model string, in, out, cache int64) transcript.Event {
	e := transcript.Event{TS: &at, Role: role, Tokens: transcript.Tokens{Input: in, Output: out, Cache: cache}}
	if model != "" {
		e.Model = &model
	}
	return e
}

func TestBounds(t *testing.T) {
	tests := []struct {
		name  string
		reset time.Time
		basis Basis
		end   time.Time
	}{
		{"no reset", time.Time{}, BasisRolling, now},
		{"reset in window", now.Add(2 * time.Hour), BasisLimits, now.Add(2 * time.Hour)},
		{"reset in past", now.Add(-time.Hour), BasisRolling, now},
		{"reset absurdly far", now.Add(48 * time.Hour), BasisRolling, now},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, basis := Bounds(PeriodFiveHour, now, tt.reset)
			if basis != tt.basis || !end.Equal(tt.end) || end.Sub(start) != Length {
				t.Fatalf("Bounds = %v %v %v", start, end, basis)
			}
		})
	}
}

func TestBuildSumsOnlyEventsInWindow(t *testing.T) {
	*shared = fakeSource{
		metas: []session.Meta{{ID: "big", Title: "Big", CWD: "/nonexistent/app"}, {ID: "old", CWD: "/nonexistent/old"}},
		events: map[string][]transcript.Event{
			"big": {
				ev(now.Add(-6*time.Hour), transcript.RoleAssistant, "m1", 999, 999, 999), // before window
				ev(now.Add(-4*time.Hour), transcript.RoleAssistant, "m1", 10, 20, 70),
				ev(now.Add(-time.Hour), transcript.RoleToolCall, "", 0, 0, 0),
				ev(now.Add(-30*time.Minute), transcript.RoleAssistant, "m2", 5, 5, 0),
			},
			"old": {ev(now.Add(-9*time.Hour), transcript.RoleAssistant, "m1", 1, 1, 1)},
		},
	}

	report, err := Build(context.Background(), now, Options{Provider: transcript.ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	if report.Total.Total != 110 || report.Total.Cache != 70 {
		t.Fatalf("total = %+v", report.Total)
	}
	if len(report.Sessions) != 1 || report.Sessions[0].SessionID != "big" {
		t.Fatalf("sessions = %+v", report.Sessions)
	}
	s := report.Sessions[0]
	if s.Turns != 2 || s.ToolCalls != 1 || s.SharePct != 100 || s.Project != "app" {
		t.Fatalf("session = %+v", s)
	}
	if len(report.ByModel) != 2 || report.ByModel[0].Name != "m1" {
		t.Fatalf("by model = %+v", report.ByModel)
	}
	if len(report.Hourly) != 5 || report.Hourly[1].Tokens.Total != 100 || report.Hourly[4].Tokens.Total != 10 {
		t.Fatalf("hourly = %+v", report.Hourly)
	}
}

func TestBuildNoReader(t *testing.T) {
	_, err := Build(context.Background(), now, Options{Provider: transcript.Provider("nope")})
	if !errors.Is(err, ErrNoReader) {
		t.Fatalf("err = %v", err)
	}
}

func TestWriteHTMLEscapesAndOmitsCWD(t *testing.T) {
	r := newReport(now, transcript.ProviderClaude, PeriodFiveHour, BasisRolling, now.Add(-Length), now)
	r.Sessions = []Session{{SessionID: "x", Title: "<script>alert(1)</script>", CWD: "/secret/path", FirstAt: now, LastAt: now}}
	var buf bytes.Buffer
	if err := WriteHTML(&buf, r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "<script>alert") || strings.Contains(out, "/secret/path") {
		t.Fatalf("unsafe page: %s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatal("title not shown escaped")
	}
}

func TestWriteHTMLShareColumns(t *testing.T) {
	r := newReport(now, transcript.ProviderClaude, PeriodFiveHour, BasisRolling, now.Add(-Length), now)
	r.Total = Tokens{Total: 200}
	r.ByModel = []Group{{Name: "m1", Tokens: Tokens{Total: 50}}}
	r.ByProject = []Group{{Name: "p1", Tokens: Tokens{Total: 150}}}
	r.Hourly = []Bucket{{Start: now, Tokens: Tokens{Total: 100}}}
	var buf bytes.Buffer
	if err := WriteHTML(&buf, r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"25.0%", "75.0%", "50.0%"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
	// A zero total must not divide by zero.
	r.Total = Tokens{}
	buf.Reset()
	if err := WriteHTML(&buf, r); err != nil || !strings.Contains(buf.String(), "0.0%") {
		t.Fatalf("zero total: %v", err)
	}
}

func TestWriteHTMLSessionDetails(t *testing.T) {
	r := newReport(now, transcript.ProviderClaude, PeriodFiveHour, BasisRolling, now.Add(-Length), now)
	tk := Tokens{Input: 10, Output: 30, Cache: 60, Total: 100}
	r.Total = tk
	r.Sessions = []Session{{SessionID: "x", Title: "t", FirstAt: now, LastAt: now, Turns: 4, Tokens: tk,
		ByModel:  []Group{{Name: "model-a", Tokens: tk}},
		Activity: []Bucket{{Start: now, Tokens: tk}}}}
	var buf bytes.Buffer
	if err := WriteHTML(&buf, r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`class="det"`, "model-a", "60.0%", "25 tokens/turn"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	// No turns: no division by zero, no per-turn figure.
	r.Sessions[0].Turns = 0
	buf.Reset()
	if err := WriteHTML(&buf, r); err != nil || strings.Contains(buf.String(), "tokens/turn") {
		t.Fatalf("zero turns: %v", err)
	}
}

func TestParsePeriod(t *testing.T) {
	for _, in := range []string{"5h", "week"} {
		if p, err := ParsePeriod(in); err != nil || string(p) != in {
			t.Errorf("ParsePeriod(%q) = %q, %v", in, p, err)
		}
	}
	for _, in := range []string{"", "day", "WEEK", "7d"} {
		if _, err := ParsePeriod(in); err == nil {
			t.Errorf("ParsePeriod(%q) accepted", in)
		}
	}
}

func TestPeriodTable(t *testing.T) {
	tests := []struct {
		p      Period
		length time.Duration
		bucket time.Duration
		label  string
	}{
		{PeriodFiveHour, 5 * time.Hour, time.Hour, "5-hour"},
		{PeriodWeek, 7 * 24 * time.Hour, 24 * time.Hour, "weekly"},
		{Period("bogus"), 5 * time.Hour, time.Hour, "5-hour"},
	}
	for _, tt := range tests {
		if tt.p.Length() != tt.length || tt.p.bucket() != tt.bucket || tt.p.Label() != tt.label {
			t.Errorf("%q: %v %v %q", tt.p, tt.p.Length(), tt.p.bucket(), tt.p.Label())
		}
	}
}

func TestBoundsWeek(t *testing.T) {
	week := PeriodWeek.Length()
	start, end, basis := Bounds(PeriodWeek, now, now.Add(3*24*time.Hour))
	if basis != BasisLimits || end.Sub(start) != week {
		t.Fatalf("reset in week: %v %v %v", start, end, basis)
	}
	// A 5-hour-sized reset is still inside a week, but a past or far one is not.
	for _, reset := range []time.Time{now.Add(-time.Hour), now.Add(30 * 24 * time.Hour)} {
		if _, end, basis := Bounds(PeriodWeek, now, reset); basis != BasisRolling || !end.Equal(now) {
			t.Fatalf("reset %v: %v %v", reset, end, basis)
		}
	}
}

func TestBuildWeekDaily(t *testing.T) {
	*shared = fakeSource{
		metas: []session.Meta{{ID: "s", Title: "S", CWD: "/nonexistent/app"}},
		events: map[string][]transcript.Event{"s": {
			ev(now.Add(-8*24*time.Hour), transcript.RoleAssistant, "m1", 999, 999, 999), // before window
			ev(now.Add(-6*24*time.Hour), transcript.RoleAssistant, "m1", 10, 10, 0),
			ev(now.Add(-2*time.Hour), transcript.RoleAssistant, "m1", 5, 5, 0),
		}},
	}
	report, err := Build(context.Background(), now, Options{Provider: transcript.ProviderClaude, Period: PeriodWeek})
	if err != nil {
		t.Fatal(err)
	}
	if report.Total.Total != 30 || len(report.Hourly) != 0 || len(report.Daily) != 7 {
		t.Fatalf("report = total %d hourly %d daily %d", report.Total.Total, len(report.Hourly), len(report.Daily))
	}
	if report.Daily[1].Tokens.Total != 20 || report.Daily[6].Tokens.Total != 10 {
		t.Fatalf("daily = %+v", report.Daily)
	}
	var buf bytes.Buffer
	if err := WriteHTML(&buf, report); err != nil || !strings.Contains(buf.String(), "weekly window") || !strings.Contains(buf.String(), "By day") {
		t.Fatalf("html: %v", err)
	}
	buf.Reset()
	WriteText(&buf, report)
	if !strings.Contains(buf.String(), "weekly window") {
		t.Fatalf("text: %s", buf.String())
	}
}
