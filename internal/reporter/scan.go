package reporter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// Scan defaults. A pass reads at most DefaultScanLimit sessions found only
// on disk and spends at most DefaultScanBudget on them, so a first start or
// a burst of new transcripts cannot starve live sessions or flood the
// server. Sessions left over are read by later passes, newest first.
const (
	DefaultScanLookback = 24 * time.Hour
	DefaultScanLimit    = 50
	DefaultScanBudget   = 10 * time.Second
)

// ScanSummary counts what a pass found in the transcript folders. It holds
// counts only, never paths or transcript text.
type ScanSummary struct {
	// Ran reports that the pass scanned at all.
	Ran bool
	// Found counts sessions with transcript data not yet uploaded that
	// discovery did not see running.
	Found int
	// Read counts the sessions read this pass, Deferred those left for a
	// later pass by the limit, the time budget, or a stop.
	Read, Deferred int
	// Excluded counts sessions dropped by the exclusion rules before their
	// transcripts were opened.
	Excluded int
	// Complete reports that every changed session in the window was read
	// without error, so a caller may move the window forward.
	Complete bool
}

// scan reads the sessions in the transcript folders of every allowlisted
// provider that changed after the scan window opened and that discovery did
// not see running. Each is read through the same path as a live session,
// with the state its enumerator reports (ENDED). Exclusion is decided in
// the enumerator's Skip, before the transcript is opened, and again in
// session as for any session. Only an error from ctx stops the pass.
func (r *run) scan(ctx context.Context, live map[string]bool, summary *Summary) error {
	cfg := r.cfg
	enumerators := cfg.ScanEnumerators
	if enumerators == nil {
		enumerators = reader.EnumeratorFor
	}
	limit := cfg.ScanLimit
	if limit <= 0 {
		limit = DefaultScanLimit
	}
	budget := cfg.ScanBudget
	if budget <= 0 {
		budget = DefaultScanBudget
	}
	lookback := cfg.ScanLookback
	if lookback <= 0 {
		lookback = DefaultScanLookback
	}
	start := r.now()
	deadline := start.Add(budget)
	after := cfg.ScanAfter
	if after.IsZero() {
		after = start.Add(-lookback)
	}
	if cfg.Since > 0 {
		if since := start.Add(-cfg.Since); since.After(after) {
			after = since
		}
	}

	scan := ScanSummary{Ran: true, Complete: true}
	defer func() { summary.Scan = scan }()
	var warnings []string
	defer func() {
		if len(warnings) == 0 {
			return
		}
		if summary.Warning != nil {
			warnings = append([]string{summary.Warning.Error()}, warnings...)
		}
		summary.Warning = joinWarnings(warnings)
	}()

	var candidates []session.Meta
	activity := map[string]time.Time{}
	seen := map[string]bool{}
	done := map[transcript.Provider]bool{}
	for _, provider := range cfg.Providers {
		if done[provider] || !r.allowed[provider] {
			continue
		}
		done[provider] = true
		if r.now().After(deadline) {
			scan.Complete = false
			break
		}
		enumerator, ok := enumerators(provider)
		if !ok {
			continue
		}
		source, ok := r.sources(provider)
		if !ok {
			continue
		}
		metas, err := enumerator.Enumerate(ctx, reader.EnumerateOptions{
			ModifiedAfter:         after,
			ClaudeSummaryInterval: r.cfg.ClaudeSummaryInterval,
			Skip: func(meta session.Meta) bool {
				if live[scanKey(provider, meta.ID)] {
					return true
				}
				if r.skipReason(meta) != "" {
					scan.Excluded++
					return true
				}
				return false
			},
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var warning *session.Warning
			if !errors.As(err, &warning) {
				// Nothing usable came back, so the window must not move past
				// what this provider holds.
				scan.Complete = false
				warnings = append(warnings, fmt.Sprintf("scan %s: %v", provider, err))
				continue
			}
			warnings = append(warnings, fmt.Sprintf("scan %s: %v", provider, warning))
		}
		for _, meta := range metas {
			if err := ctx.Err(); err != nil {
				return err
			}
			if meta.ID == "" || live[scanKey(provider, meta.ID)] {
				continue
			}
			meta.Provider = string(provider)
			changed, mtime, err := r.changed(source, meta)
			if err != nil {
				scan.Complete = false
				warnings = append(warnings, fmt.Sprintf("scan %s %s: %v", provider, meta.ID, err))
				continue
			}
			if !changed {
				continue
			}
			key := scanKey(provider, meta.ID)
			if seen[key] {
				continue
			}
			seen[key] = true
			activity[meta.RolloutPath] = mtime
			candidates = append(candidates, meta)
		}
	}
	reader.SortNewestFirst(candidates, activity)
	scan.Found = len(candidates)
	if len(candidates) == 0 {
		return nil
	}

	// Only an upload needs the machine id; a dry run leaves it unset, as
	// session does not ask for it then.
	machineID := summary.MachineID
	if machineID == "" && r.upload {
		id, err := session.MachineID(r.home)
		if err != nil {
			scan.Complete = false
			scan.Deferred = len(candidates)
			warnings = append(warnings, fmt.Sprintf("scan: %v", err))
			return nil
		}
		machineID = id
	}
	for i, meta := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i >= limit || r.now().After(deadline) || r.stopping() {
			scan.Deferred = len(candidates) - i
			scan.Complete = false
			break
		}
		meta.MachineID = machineID
		result := r.session(ctx, meta)
		if result.Err != nil {
			scan.Complete = false
		}
		scan.Read++
		summary.Sessions = append(summary.Sessions, result)
		r.print(result)
	}
	if summary.MachineID == "" {
		summary.MachineID = machineID
	}
	return nil
}

// changed reports whether any of meta's transcripts differ in size or
// modification time from their saved positions, and when the newest one
// last changed. It stats only and never opens a transcript.
func (r *run) changed(source reader.Source, meta session.Meta) (bool, time.Time, error) {
	paths, err := source.Locate(meta)
	if err != nil {
		return false, time.Time{}, err
	}
	changed := false
	var newest time.Time
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			return false, time.Time{}, errors.New("resolve transcript path")
		}
		info, err := reader.Stat(source, abs)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false, time.Time{}, fmt.Errorf("stat transcript: %w", cause(err))
		}
		if info.ModTime.After(newest) {
			newest = info.ModTime
		}
		pos, known := r.state.Files[abs]
		if !known || pos.Size != info.Size || !pos.Mtime.Equal(info.ModTime) {
			changed = true
		}
	}
	return changed, newest, nil
}

// scanKey matches liveKey for a session of provider.
func scanKey(provider transcript.Provider, id string) string {
	return string(provider) + "\x00" + strings.ToLower(id)
}
