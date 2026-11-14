package tui

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/DanBradbury/firekeeper/internal/claudelimits"
)

// Status values of a provider in the usage report.
const (
	usageStatusOK          = "ok"
	usageStatusUnavailable = "unavailable"
	usageStatusDisabled    = "disabled"
	usageStatusNoSignIn    = "not_signed_in"
	usageStatusRateLimited = "rate_limited"
	usageStatusNoQuota     = "no_quota"
)

// UsageWindowReport is one quota window, as the Usage tab shows it.
type UsageWindowReport struct {
	Label         string     `json:"label"`
	UsedPercent   float64    `json:"used_percent"`
	WindowMinutes int64      `json:"window_minutes,omitempty"`
	Detail        string     `json:"detail,omitempty"`
	ResetsAt      *time.Time `json:"resets_at"`
}

// UsageStat is a plain figure for a provider that has no quota, such as the
// token total in its local history.
type UsageStat struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ProviderUsageReport is the quota state of one provider.
type ProviderUsageReport struct {
	Provider string              `json:"provider"`
	Name     string              `json:"name"`
	Status   string              `json:"status"`
	Message  string              `json:"message,omitempty"`
	Plan     string              `json:"plan,omitempty"`
	Credits  string              `json:"credits,omitempty"`
	Windows  []UsageWindowReport `json:"windows"`
	Stats    []UsageStat         `json:"stats"`
}

// UsageReport is the output of `firekeeper usage --json`: provider quota
// windows and when they reset. Every supported provider appears. Claude, Codex
// and Copilot report quota; Kimi reports local history totals only; Gemini,
// Cursor and OpenCode have no supported source and say so (status no_quota).
type UsageReport struct {
	GeneratedAt time.Time             `json:"generated_at"`
	Providers   []ProviderUsageReport `json:"providers"`
}

// RunUsage implements the usage subcommand. It makes the same short-lived
// requests as the Usage tab (Codex app-server, Claude limits) and prints
// nothing sensitive: windows, percentages and reset times only.
func RunUsage(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("firekeeper usage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print quota windows as JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "firekeeper usage: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if !*asJSON {
		fmt.Fprintln(stderr, "firekeeper usage: only --json output is implemented")
		return 2
	}

	var (
		wg  sync.WaitGroup
		src usageSources
	)
	wg.Add(4)
	go func() { defer wg.Done(); src.codex, src.codexErr = fetchCodexUsage() }()
	go func() { defer wg.Done(); src.limits, src.limitsOn, src.limitsErr = claudeLimitsSource() }()
	go func() { defer wg.Done(); src.copilotPlan, src.copilotErr = fetchCopilotPlan() }()
	go func() { defer wg.Done(); src.kimi, src.kimiErr = fetchKimiUsage() }()
	wg.Wait()

	report := buildUsageReport(time.Now(), src)
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintf(stderr, "firekeeper usage: write JSON: %v\n", err)
		return 1
	}
	return 0
}

// usageSources is everything the report is built from, so buildUsageReport is
// pure and testable without any provider.
type usageSources struct {
	codex       codexUsageSnapshot
	codexErr    error
	limits      claudelimits.Limits
	limitsOn    bool
	limitsErr   error
	copilotPlan copilotPlanDetails
	copilotErr  string // fetchCopilotPlan's message; empty on success
	kimi        kimiUsageSnapshot
	kimiErr     error
}

func buildUsageReport(now time.Time, src usageSources) UsageReport {
	return UsageReport{
		GeneratedAt: now.UTC(),
		Providers: []ProviderUsageReport{
			claudeUsageReport(src.limits, src.limitsOn, src.limitsErr),
			codexUsageReport(src.codex, src.codexErr),
			copilotUsageReport(src.copilotPlan, src.copilotErr),
			kimiUsageReport(src.kimi, src.kimiErr),
			noQuotaReport("gemini", "Gemini CLI"),
			noQuotaReport("cursor", "Cursor CLI"),
			noQuotaReport("opencode", "OpenCode"),
		},
	}
}

func newProviderReport(provider, name string) ProviderUsageReport {
	return ProviderUsageReport{Provider: provider, Name: name, Status: usageStatusOK, Windows: []UsageWindowReport{}, Stats: []UsageStat{}}
}

// noQuotaReport is for a provider with no supported quota or usage source.
func noQuotaReport(provider, name string) ProviderUsageReport {
	r := newProviderReport(provider, name)
	r.Status = usageStatusNoQuota
	r.Message = "No usage source for " + name
	return r
}

func copilotUsageReport(plan copilotPlanDetails, errMessage string) ProviderUsageReport {
	r := newProviderReport("copilot", "GitHub Copilot")
	switch {
	case errMessage == copilotTokenMissing:
		r.Status = usageStatusNoSignIn
		r.Message = "Sign in with gh auth login"
	case errMessage != "" || !plan.Available:
		r.Status = usageStatusUnavailable
		r.Message = sanitizeProcessCommand(errMessage)
		if r.Message == "" {
			r.Message = "Plan information unavailable"
		}
	default:
		r.Plan = plan.Name
		if plan.TotalCredits > 0 {
			var resets *time.Time
			if !plan.ResetAt.IsZero() {
				t := plan.ResetAt.UTC()
				resets = &t
			}
			r.Windows = append(r.Windows, UsageWindowReport{
				Label:       "AI credits",
				UsedPercent: plan.UsedCredits / plan.TotalCredits * 100,
				Detail:      formatAICredits(plan.UsedCredits) + " / " + formatAICredits(plan.TotalCredits),
				ResetsAt:    resets,
			})
		}
	}
	return r
}

// kimiUsageReport has no quota to show: Kimi's local history is all there is.
func kimiUsageReport(snapshot kimiUsageSnapshot, err error) ProviderUsageReport {
	r := newProviderReport("kimi", "Kimi Code")
	if err != nil {
		r.Status = usageStatusUnavailable
		r.Message = sanitizeProcessCommand(err.Error())
		return r
	}
	r.Message = "Local history, not a quota"
	r.Stats = []UsageStat{
		{Label: "Lifetime tokens", Value: compactCount(snapshot.Summary.LifetimeTokens)},
		{Label: "Sessions", Value: fmt.Sprint(snapshot.Summary.Sessions)},
	}
	return r
}

// compactCount renders 1234 as 1.2k and 2500000 as 2.5M.
func compactCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprint(n)
	}
}

func claudeUsageReport(limits claudelimits.Limits, enabled bool, err error) ProviderUsageReport {
	r := newProviderReport("claude", "Claude Code")
	switch {
	case !enabled:
		r.Status = usageStatusDisabled
		r.Message = "firekeeper claude-limits enable"
	case errors.Is(err, claudelimits.ErrNoToken):
		r.Status = usageStatusNoSignIn
		r.Message = "Run claude and log in"
	case errors.Is(err, claudelimits.ErrRateLimited):
		r.Status = usageStatusRateLimited
		r.Message = "Rate limited by Anthropic"
	case err != nil:
		r.Status = usageStatusUnavailable
		r.Message = sanitizeProcessCommand(err.Error())
	default:
		r.Plan = limits.SubscriptionTag
		for _, w := range claudeLimitWindows(limits) {
			r.Windows = append(r.Windows, UsageWindowReport{Label: w.Label, UsedPercent: w.UsedPercent, ResetsAt: optionalUTC(w.ResetsAt)})
		}
	}
	return r
}

func codexUsageReport(snapshot codexUsageSnapshot, err error) ProviderUsageReport {
	r := newProviderReport("codex", "Codex")
	if err != nil {
		r.Status = usageStatusUnavailable
		r.Message = sanitizeProcessCommand(err.Error())
		return r
	}
	keys := make([]string, 0, len(snapshot.RateLimits))
	for key := range snapshot.RateLimits {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		limit := snapshot.RateLimits[key]
		if r.Plan == "" {
			r.Plan = limit.PlanType
		}
		if r.Credits == "" && limit.Credits != nil {
			switch {
			case limit.Credits.Unlimited:
				r.Credits = "unlimited"
			case limit.Credits.HasCredits:
				r.Credits = limit.Credits.Balance
			}
		}
		for _, w := range []*codexQuotaWindow{limit.Primary, limit.Secondary} {
			if w == nil {
				continue
			}
			var resets *time.Time
			if w.ResetsAt > 0 {
				t := time.Unix(w.ResetsAt, 0).UTC()
				resets = &t
			}
			r.Windows = append(r.Windows, UsageWindowReport{
				Label:         quotaWindowTitle(limit.LimitName, w.WindowDurationMin),
				UsedPercent:   w.UsedPercent,
				WindowMinutes: w.WindowDurationMin,
				ResetsAt:      resets,
			})
		}
	}
	return r
}

func optionalUTC(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
