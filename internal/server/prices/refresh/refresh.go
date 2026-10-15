// Package refresh updates the default price table from the providers'
// pricing pages. It is run by the scheduled "Refresh default prices"
// workflow through cmd/refresh-prices, never by the server, which makes no
// network calls for prices.
//
// A refresh rereads the rates of every model in the table, rewriting only
// their input, output, cache and as_of lines, so comments stay as they
// are. A model whose row is gone from its page, or can no longer be read,
// is removed, because a price that cannot be confirmed is left out. A model
// a page lists that the table does not is added when the page gives it a
// clean price and a model ID (see discover). A page that cannot be fetched
// or whose layout is not recognized fails the whole refresh rather than
// keeping a guess.
package refresh

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/prices"
)

// Fetch returns the body of url.
type Fetch func(ctx context.Context, url string) ([]byte, error)

// Guardrails. A refresh that trips one still produces the new table, but
// Report.Review says why a person should look before it merges.
const (
	// MaxChange is the largest relative change in any one rate that may
	// merge unreviewed: 0.5 allows from half to one and a half times the
	// old rate.
	MaxChange = 0.5
	// MaxRate is the largest believable rate, USD per million tokens.
	MaxRate = 1000
	// MaxRemoved is the largest share of one page's entries a refresh may
	// remove. More looks like a layout or naming change, not retirements,
	// and fails the refresh instead.
	MaxRemoved = 0.5
	// MaxAdded is the most models one refresh may add without review. More
	// at once suggests a parser read the wrong table.
	MaxAdded = 5
	// RestampAge is how old the table's as_of may get before a refresh
	// that found no changes still rewrites the dates. Without it, every
	// weekly run would open a dates-only pull request.
	RestampAge = 28 * 24 * time.Hour
)

const dateLayout = "2006-01-02"

// Change is one rate that moved.
type Change struct {
	Model    string
	Field    string // "input", "output" or "cache"
	Old, New float64
}

// Report says what a refresh found.
type Report struct {
	AsOf    string   // the new as_of, YYYY-MM-DD
	Sources []string // the pages read
	Changes []Change
	// Removed lists the models dropped because their page no longer gives
	// a readable price for them, with the reason.
	Removed []string
	// Added lists the models the pages list that the table did not.
	Added []Added
	// Skipped lists rows on the pages that were not added, with why. It
	// leaves out rows for models the table already has or excludes.
	Skipped []string
	// Review lists why the result should not merge without a person,
	// such as a rate that moved more than MaxChange. Empty means none.
	Review []string
	// Restamped means only the as_of dates changed.
	Restamped bool
}

// Added is a model a refresh added to the table.
type Added struct {
	prices.Entry
}

// Changed reports whether the table text changed.
func (r Report) Changed() bool {
	return len(r.Changes) > 0 || len(r.Removed) > 0 || len(r.Added) > 0 || r.Restamped
}

// Refresh reads every model in table from its source page and returns the
// rewritten table text. It fails when a source has no parser, a page
// cannot be fetched or parsed, or a model's row is missing or unreadable.
// With nothing changed and the table younger than RestampAge it returns
// table unchanged.
func Refresh(ctx context.Context, table []byte, fetch Fetch, today time.Time) ([]byte, Report, error) {
	var rep Report
	t, err := prices.Parse(table)
	if err != nil {
		return nil, rep, err
	}
	rep.AsOf = today.UTC().Format(dateLayout)

	pages := map[string]map[string]row{}
	ids := map[string]map[string]modelID{}
	var failures []string
	for _, e := range t.Models {
		if _, ok := pages[e.Source]; ok {
			continue
		}
		p, err := providerFor(e.Source)
		if err != nil {
			failures = append(failures, err.Error())
			pages[e.Source] = nil
			continue
		}
		body, err := fetch(ctx, markdownURL(e.Source))
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: fetch %s: %v", p.name, markdownURL(e.Source), err))
			pages[e.Source] = nil
			continue
		}
		rows, err := p.parse(string(body))
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s: page layout not recognized: %v", p.name, markdownURL(e.Source), err))
			pages[e.Source] = nil
			continue
		}
		if p.idsURL != "" {
			body, err := fetch(ctx, p.idsURL)
			if err == nil {
				ids[e.Source], err = p.parseIDs(string(body))
			}
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: model IDs from %s: %v", p.name, p.idsURL, err))
				pages[e.Source] = nil
				continue
			}
		}
		pages[e.Source] = rows
		rep.Sources = append(rep.Sources, e.Source)
	}

	rates := map[string]Rates{}
	perSource, removedPerSource := map[string]int{}, map[string]int{}
	for _, e := range t.Models {
		rows := pages[e.Source]
		if rows == nil {
			continue // its page already failed
		}
		perSource[e.Source]++
		r, ok := rows[e.PageLabel()]
		reason := ""
		switch {
		case !ok:
			reason = fmt.Sprintf("no row %q on the page any more", e.PageLabel())
		case r.err != nil:
			reason = fmt.Sprintf("row %q cannot be read: %v", e.PageLabel(), r.err)
		}
		if reason != "" {
			removedPerSource[e.Source]++
			rep.Removed = append(rep.Removed, fmt.Sprintf("`%s`: %s", e.Name, reason))
			continue
		}
		rates[e.Name] = r.rates
		old := Rates{e.Input, e.Output, e.Cache}
		for _, f := range []struct {
			name     string
			old, new float64
		}{{"input", old.Input, r.rates.Input}, {"output", old.Output, r.rates.Output}, {"cache", old.Cache, r.rates.Cache}} {
			if f.old == f.new {
				continue
			}
			rep.Changes = append(rep.Changes, Change{Model: e.Name, Field: f.name, Old: f.old, New: f.new})
			if reason := guard(f.old, f.new); reason != "" {
				rep.Review = append(rep.Review, fmt.Sprintf("%s %s %s → %s: %s", e.Name, f.name, num(f.old), num(f.new), reason))
			}
		}
	}
	for src, n := range removedPerSource {
		if float64(n) > MaxRemoved*float64(perSource[src]) {
			failures = append(failures, fmt.Sprintf("%s: %d of %d entries have no readable row; the page's model names or layout probably changed: %s",
				src, n, perSource[src], strings.Join(rep.Removed, "; ")))
		}
	}
	if len(rep.Removed) > 0 {
		rep.Review = append(rep.Review, fmt.Sprintf("%d model(s) removed because their page no longer gives a readable price; check that none was only renamed", len(rep.Removed)))
	}
	rep.Added, rep.Skipped = discover(t, pages, ids, rep.AsOf)
	for _, a := range rep.Added {
		for _, f := range []struct {
			name string
			v    float64
		}{{"input", a.Input}, {"output", a.Output}, {"cache", a.Cache}} {
			switch {
			case f.v > MaxRate:
				rep.Review = append(rep.Review, fmt.Sprintf("new model %s %s %s: above %d per million tokens", a.Name, f.name, num(f.v), MaxRate))
			case f.v == 0:
				rep.Review = append(rep.Review, fmt.Sprintf("new model %s %s is zero", a.Name, f.name))
			}
		}
	}
	if len(rep.Added) > MaxAdded {
		rep.Review = append(rep.Review, fmt.Sprintf("%d models added at once, more than %d; check that they come from the right table", len(rep.Added), MaxAdded))
	}
	if len(failures) > 0 {
		slices.Sort(failures)
		return nil, rep, errors.New(strings.Join(failures, "\n"))
	}

	asOf, _ := time.Parse(dateLayout, t.AsOf)
	if len(rep.Changes) == 0 && len(rep.Removed) == 0 && len(rep.Added) == 0 {
		if today.Sub(asOf) < RestampAge {
			return table, rep, nil
		}
		rep.Restamped = true
	}
	out, err := rewrite(table, rates, rep.Added, rep.AsOf)
	if err != nil {
		return nil, rep, err
	}
	// The rewrite must parse, pass Validate, and hold exactly the rates read.
	nt, err := prices.Parse(out)
	if err != nil {
		return nil, rep, fmt.Errorf("rewritten table: %w", err)
	}
	if len(nt.Models) != len(rates)+len(rep.Added) {
		return nil, rep, fmt.Errorf("rewritten table: %d models, want %d", len(nt.Models), len(rates)+len(rep.Added))
	}
	for _, a := range rep.Added {
		rates[a.Name] = Rates{a.Input, a.Output, a.Cache}
	}
	for _, e := range nt.Models {
		if r := rates[e.Name]; (Rates{e.Input, e.Output, e.Cache}) != r || e.AsOf != rep.AsOf {
			return nil, rep, fmt.Errorf("rewritten table: %s does not hold the rates read", e.Name)
		}
	}
	return out, rep, nil
}

// guard says why a rate change needs a person, or "" when it may merge.
func guard(old, new float64) string {
	switch {
	case new > MaxRate:
		return fmt.Sprintf("above %d per million tokens", MaxRate)
	case old == 0 || new == 0:
		return "a rate changed to or from zero"
	case math.Abs(new-old)/old > MaxChange:
		return fmt.Sprintf("moved %+.0f%%, more than %.0f%%", (new-old)/old*100, MaxChange*100)
	}
	return ""
}

var (
	nameLineRE = regexp.MustCompile(`^name = "([^"]*)"$`)
	keyLineRE  = regexp.MustCompile(`^(input|output|cache|as_of) = `)
	asOfLineRE = regexp.MustCompile(`^as_of = "[^"]*"$`)
)

// rewrite sets every model's rates and as_of, and the table's as_of, by
// replacing those lines only. An entry not in rates is removed with the
// comment lines directly above it.
func rewrite(table []byte, rates map[string]Rates, added []Added, asOf string) ([]byte, error) {
	lines, err := dropEntries(strings.Split(string(table), "\n"), rates)
	if err != nil {
		return nil, err
	}
	var (
		inModel  bool
		name     string
		topAsOf  bool
		replaced = map[string]int{}
	)
	for i, l := range lines {
		switch {
		case l == "[[model]]":
			inModel, name = true, ""
			continue
		case !inModel:
			if asOfLineRE.MatchString(l) {
				lines[i], topAsOf = fmt.Sprintf("as_of = %q", asOf), true
			}
			continue
		}
		if m := nameLineRE.FindStringSubmatch(l); m != nil {
			name = m[1]
			continue
		}
		m := keyLineRE.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		r, ok := rates[name]
		if !ok {
			return nil, fmt.Errorf("line %d: %s line before the entry's name line, or no rates for %q", i+1, m[1], name)
		}
		// A rate line is rewritten only when its value changed, so "0.20"
		// does not churn into "0.2".
		setRate := func(v float64) {
			cur, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(l, m[0])), 64)
			if err != nil || cur != v {
				lines[i] = m[1] + " = " + num(v)
			}
		}
		switch m[1] {
		case "input":
			setRate(r.Input)
		case "output":
			setRate(r.Output)
		case "cache":
			setRate(r.Cache)
		case "as_of":
			lines[i] = fmt.Sprintf("as_of = %q", asOf)
		}
		replaced[name]++
	}
	if !topAsOf {
		return nil, errors.New("no top-level as_of line")
	}
	for n := range rates {
		if replaced[n] != 4 {
			return nil, fmt.Errorf("%s: expected one input, output, cache and as_of line each, one per line as `key = value`", n)
		}
	}
	return []byte(strings.Join(insertEntries(lines, added), "\n")), nil
}

// num formats a rate as briefly as it round-trips.
func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// Markdown renders the report for a pull request body.
func (r Report) Markdown() string {
	var b strings.Builder
	switch {
	case len(r.Changes) == 0 && (len(r.Removed) > 0 || len(r.Added) > 0):
		fmt.Fprintf(&b, "The weekly refresh read the providers' pricing pages on %s. No rate changed.\n", r.AsOf)
	case len(r.Changes) > 0:
		fmt.Fprintf(&b, "The weekly refresh found %d changed rate(s) on the providers' pricing pages, read on %s.\n\n", len(r.Changes), r.AsOf)
		b.WriteString("| Model | Rate | Old | New | Change |\n| --- | --- | ---: | ---: | ---: |\n")
		for _, c := range r.Changes {
			change := "new"
			if c.Old != 0 {
				change = fmt.Sprintf("%+.1f%%", (c.New-c.Old)/c.Old*100)
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n", c.Model, c.Field, num(c.Old), num(c.New), change)
		}
	case r.Restamped:
		fmt.Fprintf(&b, "The weekly refresh confirmed every rate unchanged on %s, so only the `as_of` dates move.\n", r.AsOf)
	default:
		fmt.Fprintf(&b, "No rate changed, and the table is not yet due for a new date (%s).\n", r.AsOf)
	}
	if len(r.Added) > 0 {
		b.WriteString("\nAdded, because their page lists them with a price and a model ID:\n\n")
		b.WriteString("| Model | Input | Output | Cache | Page row |\n| --- | ---: | ---: | ---: | --- |\n")
		for _, a := range r.Added {
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n", a.Name, num(a.Input), num(a.Output), num(a.Cache), strings.ReplaceAll(a.PageLabel(), "|", "\\|"))
		}
	}
	if len(r.Removed) > 0 {
		b.WriteString("\nRemoved, because their price can no longer be confirmed from the page:\n\n")
		for _, v := range r.Removed {
			fmt.Fprintf(&b, "- %s\n", v)
		}
	}
	if len(r.Review) > 0 {
		b.WriteString("\n**Needs review before merging.** A guardrail tripped, so this pull request will not merge itself:\n\n")
		for _, v := range r.Review {
			fmt.Fprintf(&b, "- %s\n", v)
		}
	}
	if len(r.Skipped) > 0 {
		b.WriteString("\n<details><summary>Rows on the pages that were not added</summary>\n\n")
		for _, v := range r.Skipped {
			fmt.Fprintf(&b, "- %s\n", v)
		}
		b.WriteString("\nAdd a model to `exclude` in the table to keep it out for good.\n</details>\n")
	}
	b.WriteString("\nSources:\n\n")
	for _, s := range r.Sources {
		fmt.Fprintf(&b, "- %s (read as %s)\n", s, markdownURL(s))
	}
	b.WriteString("\nSee docs/prices.md for how the refresh works.\n")
	return b.String()
}

// dropEntries removes every [[model]] entry whose name is not in keep: its
// lines up to the next entry, except the blank and comment lines just
// before that entry, which belong to it; and the comment lines directly
// above it, which describe it. A comment separated by a blank line, such as
// a provider's section note, stays.
func dropEntries(lines []string, keep map[string]Rates) ([]string, error) {
	var starts []int
	for i, l := range lines {
		if l == "[[model]]" {
			starts = append(starts, i)
		}
	}
	drop := make([]bool, len(lines))
	for k, start := range starts {
		end := len(lines)
		if k+1 < len(starts) {
			end = starts[k+1]
		}
		name := ""
		for _, l := range lines[start:end] {
			if m := nameLineRE.FindStringSubmatch(l); m != nil {
				name = m[1]
				break
			}
		}
		if name == "" {
			return nil, fmt.Errorf("line %d: entry has no `name = \"...\"` line", start+1)
		}
		if _, ok := keep[name]; ok {
			continue
		}
		last := end - 1
		for last > start && (strings.TrimSpace(lines[last]) == "" || strings.HasPrefix(lines[last], "#")) {
			last--
		}
		if last+1 < end && strings.TrimSpace(lines[last+1]) == "" {
			last++ // one blank line goes with it
		}
		first := start
		for first > 0 && strings.HasPrefix(lines[first-1], "#") {
			first--
		}
		for i := first; i <= last; i++ {
			drop[i] = true
		}
	}
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if !drop[i] {
			out = append(out, l)
		}
	}
	return out, nil
}

// idRE is the form a model ID must have to be added: lowercase letters,
// digits, dots, dashes and underscores.
var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// discover finds the models the pages list that t does not, and decides
// which to add. A row is added only when:
//
//   - its price reads cleanly (input, cache and output), and
//   - its model ID comes from a provider page: the row label itself, less
//     any "(...)" note, or for a page labelled by display name, the
//     provider's model ID page, and
//   - every row naming that model gives the same price, so a model priced
//     in tiers (one row per prompt length) is left out, and
//   - the ID is well formed, not already a name or alias, and not excluded.
//
// Rows for models the table already has or excludes are not reported.
func discover(t prices.Table, pages map[string]map[string]row, ids map[string]map[string]modelID, asOf string) (added []Added, skipped []string) {
	known := map[string]bool{}
	labels := map[string]map[string]bool{}
	for _, e := range t.Models {
		known[e.Name] = true
		for _, a := range e.Aliases {
			known[a] = true
		}
		if labels[e.Source] == nil {
			labels[e.Source] = map[string]bool{}
		}
		labels[e.Source][e.PageLabel()] = true
	}
	for _, x := range t.Exclude {
		known[x] = true
	}

	type candidate struct {
		entry     prices.Entry
		ambiguous bool
	}
	sources := make([]string, 0, len(pages))
	for src := range pages {
		sources = append(sources, src)
	}
	slices.Sort(sources)
	for _, src := range sources {
		rows := pages[src]
		if rows == nil {
			continue
		}
		rowLabels := make([]string, 0, len(rows))
		for l := range rows {
			rowLabels = append(rowLabels, l)
		}
		slices.Sort(rowLabels)
		found := map[string]*candidate{}
		var order []string
		for _, label := range rowLabels {
			if labels[src][label] {
				continue
			}
			r := rows[label]
			base := baseLabel(label)
			e := prices.Entry{Name: base, Input: r.rates.Input, Output: r.rates.Output, Cache: r.rates.Cache, Source: src, AsOf: asOf}
			if m := ids[src]; m != nil {
				id, ok := m[base]
				if !ok {
					if !known[base] {
						skipped = append(skipped, fmt.Sprintf("%q: no model ID for it on the provider's model list", label))
					}
					continue
				}
				e.Name = id.ID
				if id.Alias != "" && !known[id.Alias] {
					e.Aliases = []string{id.Alias}
				}
			}
			if known[e.Name] {
				continue
			}
			if r.err != nil {
				skipped = append(skipped, fmt.Sprintf("%q: price not readable: %v", label, r.err))
				continue
			}
			if !idRE.MatchString(e.Name) || e.Name == "defaults" {
				skipped = append(skipped, fmt.Sprintf("%q: %q is not a model ID", label, e.Name))
				continue
			}
			if label != e.Name {
				e.Label = label
			}
			if c, dup := found[e.Name]; dup {
				if (Rates{c.entry.Input, c.entry.Output, c.entry.Cache}) != r.rates {
					c.ambiguous = true
				}
				continue
			}
			found[e.Name] = &candidate{entry: e}
			order = append(order, e.Name)
		}
		for _, name := range order {
			c := found[name]
			if c.ambiguous {
				skipped = append(skipped, fmt.Sprintf("`%s`: listed in several rows with different prices, such as by prompt length", name))
				continue
			}
			for _, a := range c.entry.Aliases {
				known[a] = true
			}
			known[name] = true
			added = append(added, Added{c.entry})
		}
	}
	return added, skipped
}

// insertEntries adds each new entry after the last entry with the same
// source, so it lands in its provider's section.
func insertEntries(lines []string, added []Added) []string {
	for _, a := range added {
		after := -1 // index of the last key line of the last same-source entry
		src := fmt.Sprintf("source = %q", a.Source)
		inSource := false
		for i, l := range lines {
			switch {
			case l == "[[model]]":
				inSource = false
			case l == src:
				inSource = true
			}
			if inSource && l != "" && !strings.HasPrefix(l, "#") {
				after = i
			}
		}
		block := []string{"", "[[model]]", fmt.Sprintf("name = %q", a.Name)}
		if a.Label != "" {
			block = append(block, fmt.Sprintf("label = %q", a.Label))
		}
		if len(a.Aliases) > 0 {
			quoted := make([]string, len(a.Aliases))
			for i, x := range a.Aliases {
				quoted[i] = fmt.Sprintf("%q", x)
			}
			block = append(block, "aliases = ["+strings.Join(quoted, ", ")+"]")
		}
		block = append(block,
			"input = "+num(a.Input), "output = "+num(a.Output), "cache = "+num(a.Cache),
			fmt.Sprintf("source = %q", a.Source), fmt.Sprintf("as_of = %q", a.AsOf))
		if after < 0 {
			// No entry from this source: append at the end.
			for len(lines) > 0 && lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
			lines = append(append(lines, block...), "")
			continue
		}
		lines = slices.Insert(lines, after+1, block...)
	}
	return lines
}
