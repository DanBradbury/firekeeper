package refresh

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Rates are one model's list prices in USD per million tokens.
type Rates struct {
	Input, Output, Cache float64
}

// row is one model's rates as read from a page, or why they could not be.
type row struct {
	rates Rates
	err   error
}

// pageParser reads every model row from a provider's pricing page, keyed
// by the row's exact label. A row it cannot read carries an error, so a
// quirk in one row never blocks the others. The error return is for a page
// whose layout the parser no longer recognizes.
type pageParser func(md string) (map[string]row, error)

// modelID is a model's API ID and, when it differs, its API alias.
type modelID struct {
	ID, Alias string
}

// idParser reads a page mapping a provider's display names to model IDs.
type idParser func(md string) (map[string]modelID, error)

// provider is a pricing page Firekeeper knows how to read.
type provider struct {
	name  string
	parse pageParser
	// For a pricing page labelled by display name, idsURL is the Markdown
	// page that maps display names to model IDs, and parseIDs reads it.
	// Without them a row's label (less any "(...)" note) is its model ID.
	idsURL   string
	parseIDs idParser
}

// providers maps a source page's host to its parser. Each provider serves
// a Markdown version of the page at the same URL plus ".md".
var providers = map[string]provider{
	"platform.claude.com": {"Anthropic", parseAnthropic,
		"https://platform.claude.com/docs/en/about-claude/models/overview.md", parseAnthropicIDs},
	"developers.openai.com": {name: "OpenAI", parse: parseOpenAI},
	"platform.kimi.ai":      {name: "Moonshot AI (Kimi)", parse: parseKimi},
}

// providerFor returns the parser for a source URL.
func providerFor(source string) (provider, error) {
	u, err := url.Parse(source)
	if err != nil {
		return provider{}, fmt.Errorf("source %q is not a URL", source)
	}
	p, ok := providers[u.Host]
	if !ok {
		return provider{}, fmt.Errorf("no page parser for %s", u.Host)
	}
	return p, nil
}

// markdownURL is where a source page's Markdown version lives.
func markdownURL(source string) string {
	return strings.TrimSuffix(source, "/") + ".md"
}

var (
	supRE  = regexp.MustCompile(`<sup>.*?</sup>`)
	tagRE  = regexp.MustCompile(`<[^>]*>`)
	usdRE  = regexp.MustCompile(`^\$([0-9]+(?:\.[0-9]+)?)$`)
	mtokRE = regexp.MustCompile(`^\$([0-9]+(?:\.[0-9]+)?) / MTok$`)
)

// price reads a cell such as "$0.25" with re, after dropping footnote
// markers and HTML tags.
func price(cell string, re *regexp.Regexp) (float64, error) {
	c := strings.TrimSpace(tagRE.ReplaceAllString(supRE.ReplaceAllString(cell, ""), ""))
	m := re.FindStringSubmatch(c)
	if m == nil {
		return 0, fmt.Errorf("price cell %q is not in the expected form", c)
	}
	return strconv.ParseFloat(m[1], 64)
}

// pipeTable is a Markdown pipe table: its header cells and data rows.
type pipeTable struct {
	header []string
	rows   [][]string
}

func splitPipeRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	cells := strings.Split(line, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func isPipeLine(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "|") }

// readPipeTable reads the table starting at lines[i], returning it and the
// index just past it.
func readPipeTable(lines []string, i int) (pipeTable, int) {
	var t pipeTable
	t.header = splitPipeRow(lines[i])
	i++
	if i < len(lines) && isPipeLine(lines[i]) && strings.Trim(strings.ReplaceAll(lines[i], " ", ""), "|:-") == "" {
		i++ // the --- separator
	}
	for ; i < len(lines) && isPipeLine(lines[i]); i++ {
		t.rows = append(t.rows, splitPipeRow(lines[i]))
	}
	return t, i
}

// columns finds each wanted column in header. Each want lists the names
// the column may have; the first present wins.
func columns(header []string, want ...[]string) ([]int, error) {
	idx := make([]int, len(want))
	for w, names := range want {
		idx[w] = -1
	find:
		for _, n := range names {
			for i, h := range header {
				if h == n {
					idx[w] = i
					break find
				}
			}
		}
		if idx[w] < 0 {
			return nil, fmt.Errorf("no %q column", names[0])
		}
	}
	return idx, nil
}

// readRows reads label, input, cache and output from each table row using
// column indexes, with re for the price form.
func readRows(out map[string]row, t pipeTable, col []int, re *regexp.Regexp) {
	label, in, cache, outCol := col[0], col[1], col[2], col[3]
	for _, cells := range t.rows {
		if len(cells) != len(t.header) {
			continue
		}
		var r row
		var errs [3]error
		r.rates.Input, errs[0] = price(cells[in], re)
		r.rates.Cache, errs[1] = price(cells[cache], re)
		r.rates.Output, errs[2] = price(cells[outCol], re)
		for _, err := range errs {
			if err != nil {
				r.err = err // the first is enough to say why
				break
			}
		}
		name := cells[label]
		if prev, dup := out[name]; dup && (prev.err != nil || r.err != nil || prev.rates != r.rates) {
			r.err = errors.New("listed more than once with different prices")
		}
		out[name] = r
	}
}

// parseAnthropic reads the first table under "## Model pricing", whose
// rows are labelled by display name, such as "Claude Opus 5.5", and whose
// prices read "$4 / MTok".
func parseAnthropic(md string) (map[string]row, error) {
	lines := strings.Split(md, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "## Model pricing" {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "## ") {
				break
			}
			if !isPipeLine(lines[j]) {
				continue
			}
			t, _ := readPipeTable(lines, j)
			col, err := columns(t.header, []string{"Model"}, []string{"Base input tokens"},
				[]string{"Cache hits and refreshes"}, []string{"Output tokens"})
			if err != nil {
				return nil, fmt.Errorf("model pricing table: %w", err)
			}
			out := map[string]row{}
			readRows(out, t, col, mtokRE)
			return out, nil
		}
		return nil, errors.New("no table under \"## Model pricing\"")
	}
	return nil, errors.New("no \"## Model pricing\" section")
}

// openAIColumns are the only columns a token price table may have. A
// table with any other column, such as "Training" (fine-tuned models) or
// "Modality" (image and audio models), qualifies its prices and is skipped,
// so a fine-tuned rate never becomes a model's base price.
var openAIColumns = map[string]bool{
	"Model": true, "Category": true,
	"Input": true, "Cached input": true, "Cache writes": true, "Output": true,
	"Short context input": true, "Short context cached input": true, "Short context cache writes": true, "Short context output": true,
	"Long context input": true, "Long context cached input": true, "Long context cache writes": true, "Long context output": true,
}

// openAITiers are the processing tiers the OpenAI page lists prices for.
// Only Standard is read.
var openAITiers = map[string]bool{"Standard": true, "Batch": true, "Flex": true, "Fast": true, "Ultrafast": true, "Priority": true}

// parseOpenAI reads every table in the Standard tier. The page repeats
// the same columns for each tier, so it tracks which tier a table is in:
// a line naming a tier ("Standard", or "### Standard pricing data") sets
// it, and "Prices per 1M tokens." opens a new group with no tier yet.
// Tables outside a known tier, and tables with columns other than token
// prices (see openAIColumns), are skipped. Rates are short-context.
func parseOpenAI(md string) (map[string]row, error) {
	lines := strings.Split(md, "\n")
	out := map[string]row{}
	tier, tables := "", 0
	for i := 0; i < len(lines); {
		l := strings.TrimSpace(lines[i])
		switch {
		case l == "Prices per 1M tokens.":
			tier = ""
		case openAITiers[l]:
			tier = l
		case strings.HasPrefix(l, "### ") && strings.HasSuffix(l, " pricing data") && openAITiers[strings.TrimSuffix(strings.TrimPrefix(l, "### "), " pricing data")]:
			tier = strings.TrimSuffix(strings.TrimPrefix(l, "### "), " pricing data")
		case isPipeLine(l):
			t, next := readPipeTable(lines, i)
			i = next
			if tier != "Standard" || slices.ContainsFunc(t.header, func(h string) bool { return !openAIColumns[h] }) {
				continue
			}
			col, err := columns(t.header, []string{"Model"},
				[]string{"Short context input", "Input"},
				[]string{"Short context cached input", "Cached input"},
				[]string{"Short context output", "Output"})
			if err != nil {
				continue // a Standard table for other units, such as per minute
			}
			readRows(out, t, col, usdRE)
			tables++
			continue
		}
		i++
	}
	if tables == 0 {
		return nil, errors.New("no Standard-tier token price tables")
	}
	return out, nil
}

var (
	docTableRE = regexp.MustCompile(`(?s)<DocTable\s+columns=\{\[(.*?)\]\}\s+rows=\{\[(.*?)\]\}\s*/>`)
	titleRE    = regexp.MustCompile(`title:\s*"([^"]*)"`)
	kimiCellRE = regexp.MustCompile(`<>\{"\$"\}([0-9]+(?:\.[0-9]+)?)</>|"((?:[^"\\]|\\.)*)"`)
)

// parseKimi reads every <DocTable> on the page. Prices are written as
// <>{"$"}3.00</>, and every table must give its unit as "1M tokens".
func parseKimi(md string) (map[string]row, error) {
	tables := docTableRE.FindAllStringSubmatch(md, -1)
	if len(tables) == 0 {
		return nil, errors.New("no <DocTable> blocks")
	}
	out := map[string]row{}
	for _, tm := range tables {
		var t pipeTable
		for _, m := range titleRE.FindAllStringSubmatch(tm[1], -1) {
			t.header = append(t.header, m[1])
		}
		for _, line := range strings.Split(tm[2], "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "[") {
				continue
			}
			var cells []string
			for _, m := range kimiCellRE.FindAllStringSubmatch(line, -1) {
				if m[1] != "" {
					cells = append(cells, "$"+m[1])
				} else {
					cells = append(cells, m[2])
				}
			}
			t.rows = append(t.rows, cells)
		}
		col, err := columns(t.header, []string{"Model"},
			[]string{"Input Price (Cache Miss)", "Input Price"},
			[]string{"Input Price (Cache Hit)", "Cached Input Price"},
			[]string{"Output Price"}, []string{"Unit"})
		if err != nil {
			continue // not a token price table
		}
		for _, cells := range t.rows {
			if len(cells) == len(t.header) && cells[col[4]] != "1M tokens" {
				return nil, fmt.Errorf("unit %q, want \"1M tokens\"", cells[col[4]])
			}
		}
		readRows(out, t, col[:4], usdRE)
	}
	if len(out) == 0 {
		return nil, errors.New("no token price tables")
	}
	return out, nil
}

// parseAnthropicIDs reads the models overview's comparison table, whose
// header names each current model ("Claude Opus 5.5") and whose "Claude
// API ID" and "Claude API alias" rows give its IDs in backticks.
func parseAnthropicIDs(md string) (map[string]modelID, error) {
	lines := strings.Split(md, "\n")
	for i := 0; i < len(lines); i++ {
		if !isPipeLine(lines[i]) {
			continue
		}
		t, next := readPipeTable(lines, i)
		i = next
		if len(t.header) < 2 || t.header[0] != "Feature" {
			continue
		}
		out := map[string]modelID{}
		var idRow, aliasRow []string
		for _, r := range t.rows {
			switch {
			case len(r) != len(t.header):
			case r[0] == "Claude API ID":
				idRow = r
			case r[0] == "Claude API alias":
				aliasRow = r
			}
		}
		if idRow == nil {
			return nil, errors.New("comparison table has no \"Claude API ID\" row")
		}
		for c, name := range t.header[1:] {
			id := strings.Trim(idRow[c+1], "` ")
			if id == "" {
				continue
			}
			m := modelID{ID: id}
			if aliasRow != nil {
				if a := strings.Trim(aliasRow[c+1], "` "); a != id {
					m.Alias = a
				}
			}
			out[name] = m
		}
		return out, nil
	}
	return nil, errors.New("no comparison table with a \"Feature\" column")
}

// baseLabel drops a trailing note in parentheses from a row label, such as
// "gpt-5.5 (<272K context length)" or "Claude Opus 4.1 ([retired](...))".
func baseLabel(label string) string {
	if i := strings.Index(label, " ("); i > 0 && strings.HasSuffix(label, ")") {
		return label[:i]
	}
	return label
}
