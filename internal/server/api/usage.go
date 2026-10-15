package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// Usage range limits. Dates are UTC.
const (
	DefaultUsageDays = 30
	MaxUsageDays     = 400
)

const dateLayout = "2006-01-02"

// parseBound reads a from/to value: a YYYY-MM-DD date or an RFC3339 time.
// A date as "to" includes that whole day.
func parseBound(v string, isTo bool) (time.Time, bool) {
	if t, err := time.Parse(dateLayout, v); err == nil {
		if isTo {
			t = t.AddDate(0, 0, 1)
		}
		return t, true
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	return t.UTC(), err == nil
}

type usageResponse struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	GroupBy []string  `json:"group_by"`
	Priced  bool      `json:"priced"`
	// PriceSource says whether cost used built-in default prices, the
	// user's custom prices, or both. Set only when Priced.
	PriceSource store.PriceSource `json:"price_source,omitempty"`
	// PricesAsOf is the date of the default prices, set only when
	// PriceSource is default or mixed.
	PricesAsOf string           `json:"prices_as_of,omitempty"`
	Rows       []store.UsageRow `json:"rows"`
	Totals     usageTotals      `json:"totals"`
}

type usageTotals struct {
	Tokens         transcript.Tokens `json:"tokens"`
	Cost           *float64          `json:"cost,omitempty"`
	UnpricedTokens int64             `json:"unpriced_tokens,omitempty"`
}

func usage(s *store.Store, prices map[string]store.Price, asOf string) http.HandlerFunc {
	// The table's own make-up answers price_source when a response priced
	// nothing, so the label never flips just because a range is empty.
	tableSource := store.TableSource(prices)
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		today := time.Now().UTC().Truncate(24 * time.Hour)
		to := today.AddDate(0, 0, 1)
		if v := q.Get("to"); v != "" {
			var ok bool
			if to, ok = parseBound(v, true); !ok {
				writeErr(w, http.StatusBadRequest, "bad_request", "to must be YYYY-MM-DD or RFC3339")
				return
			}
		}
		from := to.AddDate(0, 0, -DefaultUsageDays)
		if v := q.Get("from"); v != "" {
			var ok bool
			if from, ok = parseBound(v, false); !ok {
				writeErr(w, http.StatusBadRequest, "bad_request", "from must be YYYY-MM-DD or RFC3339")
				return
			}
		}
		if !from.Before(to) {
			writeErr(w, http.StatusBadRequest, "bad_request", "from must be before to")
			return
		}
		if to.Sub(from) > MaxUsageDays*24*time.Hour {
			writeErr(w, http.StatusBadRequest, "bad_request", "range is limited to 400 days")
			return
		}
		groupBy := []string{}
		if v := q.Get("group_by"); v != "" {
			groupBy = strings.Split(v, ",")
		}
		for _, k := range groupBy {
			if !store.ValidUsageGroup(k) {
				writeErr(w, http.StatusBadRequest, "bad_request", "group_by must list day, model, provider, machine, or project")
				return
			}
		}
		rows, sum, err := s.Usage(r.Context(), accountID(r), store.UsageFilter{From: from, To: to, GroupBy: groupBy, Project: q.Get("project"), Prices: prices})
		if err != nil {
			storeErr(w, err)
			return
		}
		if rows == nil {
			rows = []store.UsageRow{}
		}
		res := usageResponse{
			From: from, To: to, GroupBy: groupBy, Priced: len(prices) > 0, Rows: rows,
			Totals: usageTotals{Tokens: sum.Tokens, Cost: sum.Cost, UnpricedTokens: sum.UnpricedTokens},
		}
		if res.Priced {
			res.PriceSource = sum.PriceSource
			if res.PriceSource == "" {
				res.PriceSource = tableSource
			}
			if res.PriceSource != store.PriceSourceCustom {
				res.PricesAsOf = asOf
			}
		}
		writeJSON(w, http.StatusOK, res)
	}
}
