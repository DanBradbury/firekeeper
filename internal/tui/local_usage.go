package tui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/DanBradbury/firekeeper/internal/antigravityusage"
	"github.com/DanBradbury/firekeeper/internal/config"
	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
	tea "github.com/charmbracelet/bubbletea"
)

type localUsageSnapshot struct {
	Sessions             []usageSession
	Input, Output, Cache int64
	Daily                map[string]int64
}

type localUsageState struct {
	Snapshot  localUsageSnapshot
	Loading   bool
	Err       string
	Refreshed time.Time
	Cursor    int
}

type localUsageResultMsg struct {
	provider  usageProvider
	snapshot  localUsageSnapshot
	refreshed time.Time
	err       error
}

func (m *model) localUsage(provider usageProvider) *localUsageState {
	switch provider {
	case grokProvider:
		return &m.grokUsage
	case antigravityProvider:
		return &m.antigravityUsage
	default:
		return nil
	}
}

func localUsageIdentity(provider usageProvider) (string, transcript.Provider) {
	switch provider {
	case grokProvider:
		return "Grok Build", transcript.ProviderGrok
	case antigravityProvider:
		return "Antigravity CLI", transcript.ProviderAntigravity
	default:
		return "", ""
	}
}

func refreshLocalUsage(provider usageProvider) tea.Cmd {
	return func() tea.Msg {
		_, name := localUsageIdentity(provider)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cfg, err := config.Load(config.Options{Getenv: os.Getenv})
		if err != nil {
			return localUsageResultMsg{provider: provider, refreshed: time.Now(), err: errors.New("read privacy configuration")}
		}
		source, ok := reader.EnumeratorFor(name)
		if !ok {
			return localUsageResultMsg{provider: provider, refreshed: time.Now(), err: errors.New("session reader unavailable")}
		}
		snapshot, err := fetchLocalUsage(ctx, name, source, func(meta session.Meta) bool {
			return reporter.ExclusionReason(meta, cfg.Exclude) != ""
		})
		return localUsageResultMsg{provider: provider, snapshot: snapshot, refreshed: time.Now(), err: err}
	}
}

func fetchLocalUsage(ctx context.Context, provider transcript.Provider, source reader.Enumerator, skip func(session.Meta) bool) (localUsageSnapshot, error) {
	snapshot := localUsageSnapshot{Daily: map[string]int64{}}
	metas, err := source.Enumerate(ctx, reader.EnumerateOptions{Skip: skip})
	var warning *session.Warning
	if err != nil && !errors.As(err, &warning) {
		return snapshot, err
	}
	failures := 0
	for _, meta := range metas {
		if err := ctx.Err(); err != nil {
			return snapshot, err
		}
		input, output, cache := meta.Input, meta.Output, meta.Cache
		daily := map[string]int64{}
		note := ""
		addDay := func(date string, input, output, cache int64) error {
			if input < 0 || output < 0 || cache < 0 || input > math.MaxInt64-output ||
				input+output > math.MaxInt64-cache || daily[date] > math.MaxInt64-(input+output+cache) {
				return errors.New("local daily token totals overflow")
			}
			daily[date] += input + output + cache
			return nil
		}
		if provider == transcript.ProviderGrok {
			usage, usageErr := session.ReadGrokUsage(meta.RolloutPath)
			if usageErr != nil {
				failures++
				note = "Token counts unavailable"
			} else {
				input, output, cache = usage.Tokens.Input, usage.Tokens.Output, usage.Tokens.Cache()
				if usage.Incomplete {
					note = "Provider marks usage incomplete"
				}
				for _, turn := range usage.Turns {
					if !turn.EndedAt.IsZero() {
						if err := addDay(turn.EndedAt.Local().Format("2006-01-02"), turn.Tokens.Input, turn.Tokens.Output, turn.Tokens.Cache()); err != nil {
							return snapshot, err
						}
					}
				}
			}
		}
		if provider == transcript.ProviderAntigravity {
			// The primary log is brain/<id>/.system_generated/logs/transcript.jsonl.
			root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(meta.RolloutPath)))))
			usage, usageErr := antigravityusage.Read(ctx, filepath.Join(root, "conversations", meta.ID+".db"))
			if usageErr != nil {
				failures++
				note = "Token counts unavailable"
			} else {
				input, output, cache = usage.Input, usage.Output, usage.Cache
				if usage.Skipped > 0 {
					failures++
					note = "Token history contains unreadable generations"
				}
				for _, turn := range usage.Turns {
					if turn.TS != nil {
						if err := addDay(turn.TS.Local().Format("2006-01-02"), turn.Input, turn.Output, turn.Cache); err != nil {
							return snapshot, err
						}
					}
				}
			}
		}
		if input < 0 || output < 0 || cache < 0 ||
			input > math.MaxInt64-output || input+output > math.MaxInt64-cache ||
			snapshot.Input > math.MaxInt64-input || snapshot.Output > math.MaxInt64-output ||
			snapshot.Cache > math.MaxInt64-cache {
			return snapshot, errors.New("local token totals overflow")
		}
		total := input + output + cache
		// Check the combined total too; each individual bucket may still fit.
		current := snapshot.Input + snapshot.Output + snapshot.Cache
		if total > math.MaxInt64-current {
			return snapshot, errors.New("local token totals overflow")
		}
		snapshot.Input += input
		snapshot.Output += output
		snapshot.Cache += cache
		for date, tokens := range daily {
			if snapshot.Daily[date] > math.MaxInt64-tokens {
				return snapshot, errors.New("local daily token totals overflow")
			}
			snapshot.Daily[date] += tokens
		}
		snapshot.Sessions = append(snapshot.Sessions, usageSession{
			ID: meta.ID, Title: meta.Title, WorkDir: meta.CWD, Branch: meta.Branch,
			Model: meta.Model, Tokens: total, UpdatedAt: meta.UpdatedAt,
			Details: []usageField{
				{Name: "Input", Value: formatTokenCount(input)},
				{Name: "Output", Value: formatTokenCount(output)},
				{Name: "Cache", Value: formatTokenCount(cache)},
				{Name: "Note", Value: note},
			},
		})
	}
	if failures > 0 {
		err = errors.Join(err, fmt.Errorf("token history unavailable for %d session(s)", failures))
	}
	return snapshot, err
}

func (m model) localUsagePage() usagePage {
	state := m.localUsage(m.usageProvider)
	label, provider := localUsageIdentity(m.usageProvider)
	snapshot := state.Snapshot
	total := snapshot.Input + snapshot.Output + snapshot.Cache
	return usagePage{
		Label: label, Provider: provider, Loading: state.Loading,
		LoadingText: "Reading local " + label + " session history",
		HasData:     len(snapshot.Sessions) > 0, HistoryErr: state.Err, Err: state.Err,
		Sessions: snapshot.Sessions, Cursor: state.Cursor,
		Cards: []usageCard{
			{"local history", dashYellow, func(int) []string {
				return []string{usageStat("Sessions", fmt.Sprint(len(snapshot.Sessions)), ""), usageStat("Recorded tokens", formatTokenCount(total), dashYellow)}
			}},
			{"tokens", dashBlue, func(int) []string {
				return []string{usageStat("Input", formatTokenCount(snapshot.Input), ""), usageStat("Output", formatTokenCount(snapshot.Output), ""), usageStat("Cache", formatTokenCount(snapshot.Cache), "")}
			}},
			{"source", dashPurple, func(int) []string {
				return []string{usageNote("Local records, not account quota"), usageNote("Missing counts remain unknown"), usageNote("Model labels are session metadata")}
			}},
		},
		Days: func(count, inner int) (string, []string) {
			if len(snapshot.Daily) == 0 {
				return "", []string{usageNote("No dated per-turn token records; session totals are not assigned to a day.")}
			}
			dates, tokens := make([]string, count), make([]int64, count)
			now := time.Now()
			for i := range count {
				dates[i] = now.AddDate(0, 0, i-count+1).Format("2006-01-02")
				tokens[i] = snapshot.Daily[dates[i]]
			}
			return "", usageTotalDays(dates, tokens, inner)
		},
	}
}
