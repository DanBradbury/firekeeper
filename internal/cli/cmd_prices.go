package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"

	"github.com/DanBradbury/firekeeper/internal/config"
	"github.com/DanBradbury/firekeeper/internal/server/prices"
	"github.com/DanBradbury/firekeeper/internal/server/store"
)

// pricesUsage lists the prices commands for the usage line.
const pricesUsage = "usage: firekeeper serve prices missing [--db PATH]"

// servePrices builds the dashboard's price lookup from the user's [prices]
// table and, unless c turns them off, the built-in defaults. asOf is the
// defaults' date, or "" when they are off. A nil table means usage shows
// tokens only.
func servePrices(c config.Config) (table map[string]store.Price, asOf string, err error) {
	custom := make(map[string]store.Price, len(c.Prices))
	for model, p := range c.Prices {
		custom[model] = store.Price{Input: p.Input, Output: p.Output, Cache: p.Cache}
	}
	var defaults *prices.Table
	if !c.NoDefaultPrices {
		t, err := prices.Defaults()
		if err != nil {
			return nil, "", fmt.Errorf("built-in price table: %w", err)
		}
		defaults, asOf = &t, t.AsOf
	}
	table = prices.Merge(custom, defaults)
	if len(table) == 0 {
		return nil, "", nil
	}
	return table, asOf, nil
}

// runPrices implements `firekeeper serve prices`.
func runPrices(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, pricesUsage)
		return 2
	}
	if args[0] == "missing" {
		return runPricesMissing(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "firekeeper serve prices: unknown command %q\n%s\n", args[0], pricesUsage)
	return 2
}

// runPricesMissing prints every model in the dashboard database that has
// no price, with its token counts, so the operator knows what to price. It
// prints model names and counts only, never transcript text.
func runPricesMissing(args []string, stdout, stderr io.Writer) int {
	const name = "firekeeper serve prices missing"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", "", "dashboard database path (default ~/.firekeeper/dashboard.db)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: unexpected argument %q\n", name, fs.Arg(0))
		return 2
	}
	c, err := loadConfig(configFlags{})
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 2
	}
	table, _, err := servePrices(c)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}

	ctx := context.Background()
	s, ok := openDashboardStore(ctx, name, *db, stderr)
	if !ok {
		return 1
	}
	defer s.Close()
	models, err := s.TokensByModel(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	var missing []store.ModelTokens
	for _, m := range models {
		if _, ok := table[m.Model]; !ok {
			missing = append(missing, m)
		}
	}
	if len(missing) == 0 {
		fmt.Fprintln(stdout, "every model with stored tokens has a price")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tINPUT\tOUTPUT\tCACHE\tTOTAL")
	for _, m := range missing {
		t := m.Tokens
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\n", modelLabel(m.Model), t.Input, t.Output, t.Cache, t.Input+t.Output+t.Cache)
	}
	tw.Flush()
	return 0
}

// modelLabel makes a stored model name safe to print: "(no model)" when
// empty, quoted when it holds anything but printable characters, since it
// came from a transcript.
func modelLabel(m string) string {
	if m == "" {
		return "(no model)"
	}
	if q := strconv.Quote(m); q[1:len(q)-1] != m {
		return q
	}
	return m
}
