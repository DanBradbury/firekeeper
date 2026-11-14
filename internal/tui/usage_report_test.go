package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/claudelimits"
)

func TestBuildUsageReport(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	codex := codexUsageSnapshot{RateLimits: map[string]codexRateLimit{
		"codex": {
			LimitName: "codex", PlanType: "plus",
			Primary:   &codexQuotaWindow{UsedPercent: 12, WindowDurationMin: 300, ResetsAt: reset.Unix()},
			Secondary: &codexQuotaWindow{UsedPercent: 3, WindowDurationMin: 10080},
			Credits:   &codexQuotaCredits{HasCredits: true, Balance: "5.00"},
		},
	}}
	limits := claudelimits.Limits{
		FiveHour:        &claudelimits.Window{UsedPercent: 40, ResetsAt: reset},
		SevenDay:        &claudelimits.Window{UsedPercent: 9},
		SubscriptionTag: "max",
	}

	t.Run("happy", func(t *testing.T) {
		r := buildUsageReport(now, usageSources{codex: codex, limits: limits, limitsOn: true})
		if len(r.Providers) != 7 {
			t.Fatalf("providers = %d, want 7", len(r.Providers))
		}
		claude, cx := r.Providers[0], r.Providers[1]
		if claude.Status != usageStatusOK || claude.Plan != "max" || len(claude.Windows) != 2 {
			t.Fatalf("claude = %+v", claude)
		}
		if claude.Windows[0].Label != "5-hour" || claude.Windows[0].ResetsAt == nil || !claude.Windows[0].ResetsAt.Equal(reset) {
			t.Fatalf("claude 5-hour window = %+v", claude.Windows[0])
		}
		if claude.Windows[1].ResetsAt != nil {
			t.Fatalf("zero reset must be null, got %v", claude.Windows[1].ResetsAt)
		}
		if cx.Plan != "plus" || cx.Credits != "5.00" || len(cx.Windows) != 2 {
			t.Fatalf("codex = %+v", cx)
		}
		if cx.Windows[0].Label != "5-hour" || cx.Windows[1].Label != "Weekly" || cx.Windows[1].ResetsAt != nil {
			t.Fatalf("codex windows = %+v", cx.Windows)
		}
	})

	t.Run("errors", func(t *testing.T) {
		cases := []struct {
			name      string
			on        bool
			err       error
			want      string
			codexErr  error
			wantCodex string
		}{
			{"disabled", false, nil, usageStatusDisabled, nil, usageStatusOK},
			{"no sign-in", true, claudelimits.ErrNoToken, usageStatusNoSignIn, errors.New("codex not found"), usageStatusUnavailable},
			{"failure", true, errors.New("boom"), usageStatusUnavailable, nil, usageStatusOK},
			{"rate limited", true, fmt.Errorf("fetch: %w", claudelimits.ErrRateLimited), usageStatusRateLimited, nil, usageStatusOK},
		}
		for _, c := range cases {
			r := buildUsageReport(now, usageSources{codexErr: c.codexErr, limitsOn: c.on, limitsErr: c.err})
			if r.Providers[0].Status != c.want || r.Providers[1].Status != c.wantCodex {
				t.Errorf("%s: statuses = %s/%s, want %s/%s", c.name, r.Providers[0].Status, r.Providers[1].Status, c.want, c.wantCodex)
			}
			if r.Providers[0].Windows == nil || r.Providers[1].Windows == nil {
				t.Errorf("%s: windows must encode as [], not null", c.name)
			}
		}
	})
}

func TestRunUsageArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"extra"}, {"--bogus"}} {
		var out, errOut bytes.Buffer
		if code := RunUsage(args, &out, &errOut); code != 2 || out.Len() != 0 {
			t.Errorf("RunUsage(%v) = %d, stdout %q; want 2 and no output", args, code, out.String())
		}
	}
}

func TestUsageReportJSONShape(t *testing.T) {
	r := buildUsageReport(time.Unix(0, 0), usageSources{codexErr: errors.New("x")})
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"provider":"claude"`, `"status":"disabled"`, `"windows":[]`, `"stats":[]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON %s missing %s", b, want)
		}
	}
}

func TestOtherProviderReports(t *testing.T) {
	reset := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	plan := copilotPlanDetails{Available: true, Name: "pro", UsedCredits: 481, TotalCredits: 1000, ResetAt: reset}
	kimi := kimiUsageSnapshot{Summary: kimiUsageSummary{LifetimeTokens: 2_500_000, Sessions: 42}}

	t.Run("happy", func(t *testing.T) {
		r := buildUsageReport(time.Unix(0, 0), usageSources{copilotPlan: plan, kimi: kimi})
		byName := map[string]ProviderUsageReport{}
		for _, p := range r.Providers {
			byName[p.Provider] = p
		}
		cp := byName["copilot"]
		if cp.Status != usageStatusOK || cp.Plan != "pro" || len(cp.Windows) != 1 {
			t.Fatalf("copilot = %+v", cp)
		}
		w := cp.Windows[0]
		if w.Label != "AI credits" || w.UsedPercent != 48.1 || w.Detail != "481 / 1,000" || w.ResetsAt == nil || !w.ResetsAt.Equal(reset) {
			t.Fatalf("copilot window = %+v", w)
		}
		k := byName["kimi"]
		if k.Status != usageStatusOK || len(k.Windows) != 0 || len(k.Stats) != 2 || k.Stats[0].Value != "2.5M" || k.Stats[1].Value != "42" {
			t.Fatalf("kimi = %+v", k)
		}
		for _, name := range []string{"gemini", "cursor", "opencode"} {
			if p := byName[name]; p.Status != usageStatusNoQuota || p.Windows == nil || p.Stats == nil {
				t.Fatalf("%s = %+v", name, p)
			}
		}
	})

	t.Run("errors", func(t *testing.T) {
		r := buildUsageReport(time.Unix(0, 0), usageSources{copilotErr: copilotTokenMissing, kimiErr: errors.New("find Kimi session history")})
		if got := r.Providers[2].Status; got != usageStatusNoSignIn {
			t.Errorf("copilot without token = %s", got)
		}
		if got := r.Providers[3].Status; got != usageStatusUnavailable {
			t.Errorf("kimi without history = %s", got)
		}
		r = buildUsageReport(time.Unix(0, 0), usageSources{copilotErr: "Copilot plan request returned HTTP 500"})
		if got := r.Providers[2]; got.Status != usageStatusUnavailable || got.Message == "" || len(got.Windows) != 0 {
			t.Errorf("copilot plan failure = %+v", got)
		}
		r = buildUsageReport(time.Unix(0, 0), usageSources{}) // zero plan: not Available
		if got := r.Providers[2].Status; got != usageStatusUnavailable {
			t.Errorf("copilot zero plan = %s", got)
		}
	})
}

func TestCompactCount(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1.0k", 1234: "1.2k", 2_500_000: "2.5M", -5: "-5"} {
		if got := compactCount(n); got != want {
			t.Errorf("compactCount(%d) = %q, want %q", n, got, want)
		}
	}
}
