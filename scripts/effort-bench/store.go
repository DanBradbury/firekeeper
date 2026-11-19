package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The store is a directory that accumulates runs across batches, windows
// and days:
//
//	runs.jsonl  one Result per line, append only
//	plan.json   the current batch: its settings and every planned run
//
// Nothing in it is uploaded. Errors are redacted before they are stored;
// session ids are kept in full so runs can be deduplicated and matched.

const (
	runsFile = "runs.jsonl"
	planFile = "plan.json"
)

// Plan is one batch: a fixed matrix that --resume continues.
type Plan struct {
	Batch     string    `json:"batch"`
	CreatedAt time.Time `json:"created_at"`
	Harnesses []string  `json:"harnesses"`
	Model     string    `json:"model"`
	Efforts   []string  `json:"efforts"`
	Reps      int       `json:"reps"`
	Seed      uint64    `json:"seed"`
	Order     string    `json:"order"`
	Prompt    string    `json:"prompt"`
	PromptID  string    `json:"prompt_id"`
	Items     []Planned `json:"items"`
}

func promptID(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:4])
}

// newBatchID is the start time plus a seed-derived suffix, so two batches
// started in the same second still differ.
func newBatchID(now time.Time, seed uint64) string {
	return fmt.Sprintf("%s-%04x", now.UTC().Format("20060102T150405Z"), (seed^uint64(now.UnixNano()))&0xffff)
}

func loadPlan(store string) (*Plan, error) {
	b, err := os.ReadFile(filepath.Join(store, planFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("read %s: %w", planFile, err)
	}
	return &p, nil
}

func savePlan(store string, p *Plan) error {
	if err := os.MkdirAll(store, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(store, planFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(store, planFile))
}

// loadRuns reads every stored run, oldest first, keeping the first record
// for each harness and session id. Lines that do not decode are skipped.
func loadRuns(store string) ([]Result, error) {
	f, err := os.Open(filepath.Join(store, runsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Result
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var r Result
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Harness != "" {
			out = append(out, r)
		}
	}
	return dedupe(out), sc.Err()
}

// dedupe drops repeated harness+session ids, keeping the first. Records
// without a session id (nothing ran) are kept as they are.
func dedupe(rs []Result) []Result {
	seen := map[string]bool{}
	out := rs[:0:0]
	for _, r := range rs {
		if r.SessionID != "" {
			key := r.Harness + "\x00" + r.SessionID
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// appendRun adds r unless its session is already stored. It reports
// whether r was written.
func appendRun(store string, r Result) (bool, error) {
	if r.SessionID != "" {
		existing, err := loadRuns(store)
		if err != nil {
			return false, err
		}
		for _, e := range existing {
			if e.Harness == r.Harness && e.SessionID == r.SessionID {
				return false, nil
			}
		}
	}
	if err := os.MkdirAll(store, 0o700); err != nil {
		return false, err
	}
	f, err := os.OpenFile(filepath.Join(store, runsFile), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return false, err
	}
	b, err := json.Marshal(r)
	if err != nil {
		f.Close()
		return false, err
	}
	_, werr := f.Write(append(b, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr == nil, werr
}

// remaining lists the plan items that have no stored run with a session
// (an item whose harness never started, for example a missing binary, is
// run again).
func remaining(p *Plan, runs []Result) []Planned {
	done := map[int]bool{}
	for _, r := range runs {
		if r.Batch == p.Batch && r.SessionID != "" {
			done[r.PlanIndex] = true
		}
	}
	var out []Planned
	for _, it := range p.Items {
		if !done[it.Index] {
			out = append(out, it)
		}
	}
	return out
}
