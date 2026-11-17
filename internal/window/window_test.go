package window

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
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
			start, end, basis := Bounds(now, tt.reset)
			if basis != tt.basis || !end.Equal(tt.end) || end.Sub(start) != Length {
				t.Fatalf("Bounds = %v %v %v", start, end, basis)
			}
		})
	}
}

func TestBuildSumsOnlyEventsInWindow(t *testing.T) {
	src := &fakeSource{
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
	reader.Register(transcript.ProviderClaude, src)

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
	r := newReport(now, transcript.ProviderClaude, BasisRolling, now.Add(-Length), now)
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
