// Package prices holds the built-in default model price table that the
// dashboard uses to estimate cost, and merges it with the user's own
// prices.
//
// The table is embedded at build time from defaults.toml. The server never
// fetches prices from the network. Every rate is a published list price,
// so any cost computed from it is an estimate, not what a subscription or
// invoice charged.
//
// A model is matched by its exact name, then by an entry's explicit
// aliases, then not at all. There is no prefix or fuzzy matching: a wrong
// price is worse than no price.
package prices

import (
	_ "embed"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"

	"github.com/DanBradbury/firekeeper/internal/server/store"
)

//go:embed defaults.toml
var defaultsTOML []byte

// dateLayout is the format of every as_of date.
const dateLayout = "2006-01-02"

// ErrInvalid means a price table breaks one of the rules Validate checks.
var ErrInvalid = errors.New("invalid price table")

// Table is a default price table.
type Table struct {
	// AsOf is the date of the oldest entry, YYYY-MM-DD. It is the date the
	// dashboard shows.
	AsOf   string  `toml:"as_of"`
	Models []Entry `toml:"model"`
}

// Entry is one model's list price in USD per million tokens.
type Entry struct {
	Name string `toml:"name"`
	// Aliases are other exact names a harness reports for this model.
	Aliases []string `toml:"aliases"`
	Input   float64  `toml:"input"`
	Output  float64  `toml:"output"`
	Cache   float64  `toml:"cache"`
	// Source is the provider's public pricing page the rates came from.
	Source string `toml:"source"`
	// AsOf is the date the rates were read from Source, YYYY-MM-DD.
	AsOf string `toml:"as_of"`
}

// Parse decodes and validates a table. Unknown keys are errors, so a typo
// cannot silently drop a rate.
func Parse(data []byte) (Table, error) {
	var t Table
	md, err := toml.Decode(string(data), &t)
	if err != nil {
		return Table{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return Table{}, fmt.Errorf("%w: unknown key %s", ErrInvalid, undecoded[0])
	}
	if err := t.Validate(); err != nil {
		return Table{}, err
	}
	return t, nil
}

// Defaults returns the embedded default table.
var Defaults = sync.OnceValues(func() (Table, error) { return Parse(defaultsTOML) })

// Validate checks that every entry has a model name, an https source and
// an as_of date; that no rate is negative or not finite; that names and
// aliases are well formed and unique across the table; and that the
// table's as_of is its oldest entry's date.
func (t Table) Validate() error {
	if len(t.Models) == 0 {
		return fmt.Errorf("%w: no models", ErrInvalid)
	}
	asOf, err := time.Parse(dateLayout, t.AsOf)
	if err != nil {
		return fmt.Errorf("%w: as_of %q is not a YYYY-MM-DD date", ErrInvalid, t.AsOf)
	}
	var oldest time.Time
	seen := map[string]bool{}
	for i, e := range t.Models {
		if err := checkName(e.Name); err != nil {
			return fmt.Errorf("%w: model %d: name %w", ErrInvalid, i+1, err)
		}
		if seen[e.Name] {
			return fmt.Errorf("%w: model %q is listed twice", ErrInvalid, e.Name)
		}
		seen[e.Name] = true
		for _, r := range []float64{e.Input, e.Output, e.Cache} {
			if r < 0 || math.IsNaN(r) || math.IsInf(r, 0) {
				return fmt.Errorf("%w: model %q has a negative or non-finite rate", ErrInvalid, e.Name)
			}
		}
		u, err := url.Parse(e.Source)
		if e.Source == "" || err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("%w: model %q needs a source that is an https URL", ErrInvalid, e.Name)
		}
		d, err := time.Parse(dateLayout, e.AsOf)
		if err != nil {
			return fmt.Errorf("%w: model %q needs an as_of YYYY-MM-DD date", ErrInvalid, e.Name)
		}
		if oldest.IsZero() || d.Before(oldest) {
			oldest = d
		}
	}
	// Aliases are checked after every name is known, so an alias cannot
	// shadow a model listed later.
	for _, e := range t.Models {
		for _, a := range e.Aliases {
			if err := checkName(a); err != nil {
				return fmt.Errorf("%w: model %q: alias %w", ErrInvalid, e.Name, err)
			}
			if seen[a] {
				return fmt.Errorf("%w: model %q: alias %q is already a model name or alias", ErrInvalid, e.Name, a)
			}
			seen[a] = true
		}
	}
	if !asOf.Equal(oldest) {
		return fmt.Errorf("%w: as_of %s must be the oldest entry's as_of, %s", ErrInvalid, t.AsOf, oldest.Format(dateLayout))
	}
	return nil
}

// reservedName is the [prices] config key that turns defaults off, so no
// model may use it.
const reservedName = "defaults"

// checkName reports why a model name or alias is malformed.
func checkName(n string) error {
	switch {
	case n == "":
		return errors.New("is empty")
	case len(n) > 200:
		return errors.New("is longer than 200 bytes")
	case n == reservedName:
		return fmt.Errorf("%q is reserved", n)
	case strings.IndexFunc(n, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }) >= 0:
		return fmt.Errorf("%q contains a space or control character", n)
	}
	return nil
}

// Merge returns the price lookup the usage query uses. custom is the
// user's table and always wins for the models it names. defaults, when not
// nil, fills in every other model and alias. An alias of a model the user
// priced takes the user's price, unless the user priced the alias itself.
// Custom prices are marked store.PriceSourceCustom, defaults
// store.PriceSourceDefault.
func Merge(custom map[string]store.Price, defaults *Table) map[string]store.Price {
	out := make(map[string]store.Price, len(custom))
	mark := func(p store.Price) store.Price {
		p.Source = store.PriceSourceCustom
		return p
	}
	if defaults != nil {
		for _, e := range defaults.Models {
			p := store.Price{Input: e.Input, Output: e.Output, Cache: e.Cache, Source: store.PriceSourceDefault}
			if c, ok := custom[e.Name]; ok {
				p = mark(c)
			}
			out[e.Name] = p
			for _, a := range e.Aliases {
				out[a] = p
			}
		}
	}
	for m, p := range custom {
		out[m] = mark(p)
	}
	return out
}
