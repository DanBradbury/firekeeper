package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// Usage group keys accepted by Usage.
const (
	GroupDay      = "day"
	GroupModel    = "model"
	GroupProvider = "provider"
	GroupMachine  = "machine"
	GroupProject  = "project"
)

// usageColumns maps each group key to its SQL expression. An event without
// a model falls back to its session's model; day is the UTC date of ts.
var usageColumns = map[string]string{
	GroupDay:      `substr(e.ts, 1, 10)`,
	GroupModel:    `COALESCE(NULLIF(e.model, ''), s.model)`,
	GroupProvider: `e.provider`,
	GroupMachine:  `e.machine_id`,
	GroupProject:  `s.project`,
}

// ValidUsageGroup reports whether k is a usage group key.
func ValidUsageGroup(k string) bool { _, ok := usageColumns[k]; return ok }

// Price is a model's cost per million tokens of each kind. Each counter is
// priced as stored; Firekeeper does not know whether a provider's input
// count already includes cached tokens.
type Price struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	Cache  float64 `json:"cache"`
}

func (p Price) cost(t transcript.Tokens) float64 {
	return (float64(t.Input)*p.Input + float64(t.Output)*p.Output + float64(t.Cache)*p.Cache) / 1e6
}

// UsageFilter selects events with From <= ts < To and groups their token
// sums by GroupBy keys, in order. Events without a timestamp are excluded.
type UsageFilter struct {
	From, To time.Time
	GroupBy  []string
	Project  string // Empty selects all projects; otherwise an exact match.
	// Prices, when non-empty, adds cost to every row. Models are matched
	// exactly; tokens from models without a price count as unpriced.
	Prices map[string]Price
}

// UsageRow is the token sum for one combination of group values.
type UsageRow struct {
	Group  map[string]string `json:"group"`
	Tokens transcript.Tokens `json:"tokens"`
	// Cost is set only when prices are configured. It covers priced models;
	// UnpricedTokens counts the tokens it leaves out.
	Cost           *float64 `json:"cost,omitempty"`
	UnpricedTokens int64    `json:"unpriced_tokens,omitempty"`
}

func total(t transcript.Tokens) int64 { return t.Input + t.Output + t.Cache }

func (r *UsageRow) add(model string, t transcript.Tokens, prices map[string]Price) {
	r.Tokens.Input += t.Input
	r.Tokens.Output += t.Output
	r.Tokens.Cache += t.Cache
	if len(prices) == 0 {
		return
	}
	if r.Cost == nil {
		r.Cost = new(float64)
	}
	if p, ok := prices[model]; ok {
		*r.Cost += p.cost(t)
	} else {
		r.UnpricedTokens += total(t)
	}
}

// Usage returns token sums grouped by f.GroupBy, plus the total over all
// rows. Rows are ordered by day ascending when grouped by day, then by
// total tokens descending, then by group values.
func (s *Store) Usage(ctx context.Context, accountID string, f UsageFilter) (rows []UsageRow, sum UsageRow, err error) {
	if err := requireAccount(accountID); err != nil {
		return nil, sum, err
	}
	if !f.From.Before(f.To) {
		return nil, sum, fmt.Errorf("%w: from must be before to", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, k := range f.GroupBy {
		if !ValidUsageGroup(k) {
			return nil, sum, fmt.Errorf("%w: unknown group %q", ErrInvalid, k)
		}
		if seen[k] {
			return nil, sum, fmt.Errorf("%w: duplicate group %q", ErrInvalid, k)
		}
		seen[k] = true
	}
	// Always group by model too so cost can be priced per model, then fold
	// the extra dimension away below.
	keys := slices.Clone(f.GroupBy)
	if !seen[GroupModel] {
		keys = append(keys, GroupModel)
	}
	cols := make([]string, len(keys))
	for i, k := range keys {
		cols[i] = usageColumns[k]
	}
	query := `SELECT ` + strings.Join(cols, ", ") + `,
    SUM(e.input_tokens), SUM(e.output_tokens), SUM(e.cache_tokens)
FROM events e JOIN sessions s ON s.account_id = e.account_id AND s.machine_id = e.machine_id AND s.session_id = e.session_id
WHERE e.account_id = ? AND e.ts >= ? AND e.ts < ? AND (e.input_tokens > 0 OR e.output_tokens > 0 OR e.cache_tokens > 0)`
	args := []any{accountID, fmtTime(&f.From), fmtTime(&f.To)}
	if f.Project != "" {
		query += ` AND s.project = ?`
		args = append(args, f.Project)
	}
	query += ` GROUP BY ` + strings.Join(cols, ", ")
	res, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sum, err
	}
	defer res.Close()

	sum.Group = map[string]string{}
	index := map[string]int{}
	vals := make([]string, len(keys))
	dest := make([]any, len(keys)+3)
	for i := range vals {
		dest[i] = &vals[i]
	}
	var t transcript.Tokens
	dest[len(keys)], dest[len(keys)+1], dest[len(keys)+2] = &t.Input, &t.Output, &t.Cache
	for res.Next() {
		if err := res.Scan(dest...); err != nil {
			return nil, sum, err
		}
		model := vals[slices.Index(keys, GroupModel)]
		id := strings.Join(vals[:len(f.GroupBy)], "\x00")
		i, ok := index[id]
		if !ok {
			g := make(map[string]string, len(f.GroupBy))
			for j, k := range f.GroupBy {
				g[k] = vals[j]
			}
			i = len(rows)
			index[id] = i
			rows = append(rows, UsageRow{Group: g})
		}
		rows[i].add(model, t, f.Prices)
		sum.add(model, t, f.Prices)
	}
	if err := res.Err(); err != nil {
		return nil, sum, err
	}
	if len(f.Prices) > 0 && sum.Cost == nil {
		sum.Cost = new(float64)
	}
	slices.SortFunc(rows, func(a, b UsageRow) int {
		if c := strings.Compare(a.Group[GroupDay], b.Group[GroupDay]); c != 0 {
			return c
		}
		if ta, tb := total(a.Tokens), total(b.Tokens); ta != tb {
			if ta > tb {
				return -1
			}
			return 1
		}
		for _, k := range f.GroupBy {
			if c := strings.Compare(a.Group[k], b.Group[k]); c != 0 {
				return c
			}
		}
		return 0
	})
	return rows, sum, nil
}
