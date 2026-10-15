package prices

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/store"
)

// TestDefaultsValid fails when the embedded table breaks a rule: an entry
// without a source or as_of, a negative rate, or a malformed alias.
func TestDefaultsValid(t *testing.T) {
	d, err := Defaults()
	if err != nil {
		t.Fatalf("embedded table: %v", err)
	}
	if _, err := time.Parse(dateLayout, d.AsOf); err != nil {
		t.Fatalf("as_of %q", d.AsOf)
	}
	for _, e := range d.Models {
		if e.Source == "" || e.AsOf == "" {
			t.Errorf("%s: missing source or as_of", e.Name)
		}
		if e.Input < 0 || e.Output < 0 || e.Cache < 0 {
			t.Errorf("%s: negative rate", e.Name)
		}
		for _, a := range e.Aliases {
			if checkName(a) != nil {
				t.Errorf("%s: malformed alias %q", e.Name, a)
			}
		}
	}
}

// TestDefaultsCoverFixtureModels pins the models this repository's
// fixtures and README use, so dropping one is a deliberate change.
func TestDefaultsCoverFixtureModels(t *testing.T) {
	d, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	table := Merge(nil, &d)
	for _, m := range []string{"gpt-5", "o3", "claude-sonnet-5-5", "claude-sonnet-4.6", "kimi-k3"} {
		if _, ok := table[m]; !ok {
			t.Errorf("%s has no default price", m)
		}
	}
}

func validTable() Table {
	return Table{AsOf: "2026-10-01", Models: []Entry{
		{Name: "model-a", Aliases: []string{"model.a"}, Input: 1, Output: 2, Cache: 0.1, Source: "https://example.com/pricing", AsOf: "2026-10-01"},
		{Name: "model-b", Input: 3, Output: 4, Cache: 0.3, Source: "https://example.com/pricing", AsOf: "2026-10-02"},
	}}
}

func TestValidate(t *testing.T) {
	if err := validTable().Validate(); err != nil {
		t.Fatalf("valid table: %v", err)
	}
	tests := []struct {
		name   string
		change func(*Table)
		want   string
	}{
		{"no models", func(t *Table) { t.Models = nil }, "no models"},
		{"no table as_of", func(t *Table) { t.AsOf = "" }, "as_of"},
		{"table as_of not oldest", func(t *Table) { t.AsOf = "2026-10-02" }, "oldest"},
		{"bad table as_of", func(t *Table) { t.AsOf = "October 1" }, "as_of"},
		{"no source", func(t *Table) { t.Models[0].Source = "" }, "source"},
		{"http source", func(t *Table) { t.Models[0].Source = "http://example.com/pricing" }, "source"},
		{"relative source", func(t *Table) { t.Models[0].Source = "pricing.html" }, "source"},
		{"no entry as_of", func(t *Table) { t.Models[1].AsOf = "" }, "as_of"},
		{"bad entry as_of", func(t *Table) { t.Models[1].AsOf = "2026-13-01" }, "as_of"},
		{"negative input", func(t *Table) { t.Models[0].Input = -1 }, "negative"},
		{"negative output", func(t *Table) { t.Models[0].Output = -0.5 }, "negative"},
		{"negative cache", func(t *Table) { t.Models[1].Cache = -0.01 }, "negative"},
		{"NaN rate", func(t *Table) { t.Models[0].Cache = math.NaN() }, "non-finite"},
		{"infinite rate", func(t *Table) { t.Models[0].Output = math.Inf(1) }, "non-finite"},
		{"empty name", func(t *Table) { t.Models[0].Name = "" }, "empty"},
		{"reserved name", func(t *Table) { t.Models[0].Name = "defaults" }, "reserved"},
		{"duplicate name", func(t *Table) { t.Models[1].Name = "model-a" }, "twice"},
		{"empty alias", func(t *Table) { t.Models[0].Aliases = []string{""} }, "empty"},
		{"alias with space", func(t *Table) { t.Models[0].Aliases = []string{"model a"} }, "space"},
		{"alias with control", func(t *Table) { t.Models[0].Aliases = []string{"model\x00a"} }, "control"},
		{"alias with trailing space", func(t *Table) { t.Models[0].Aliases = []string{"model-a2 "} }, "space"},
		{"alias is own name", func(t *Table) { t.Models[0].Aliases = []string{"model-a"} }, "already"},
		{"alias is a later model", func(t *Table) { t.Models[0].Aliases = []string{"model-b"} }, "already"},
		{"alias repeated", func(t *Table) { t.Models[1].Aliases = []string{"model.a"} }, "already"},
		{"alias reserved", func(t *Table) { t.Models[1].Aliases = []string{"defaults"} }, "reserved"},
		{"exclude malformed", func(t *Table) { t.Exclude = []string{"bad name"} }, "exclude"},
		{"exclude a listed model", func(t *Table) { t.Exclude = []string{"model.a"} }, "also a model"},
		{"label with control", func(t *Table) { t.Models[0].Label = "Model\tA" }, "label"},
		{"label with space around", func(t *Table) { t.Models[0].Label = " Model A" }, "label"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := validTable()
			tt.change(&tbl)
			err := tbl.Validate()
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err %v, want ErrInvalid mentioning %q", err, tt.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	const good = `as_of = "2026-10-01"
[[model]]
name = "m"
aliases = ["m.1"]
input = 1
output = 2
cache = 0.5
source = "https://example.com/p"
as_of = "2026-10-01"
`
	tbl, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Models) != 1 || tbl.Models[0].Cache != 0.5 || tbl.Models[0].Aliases[0] != "m.1" {
		t.Fatalf("parsed %+v", tbl)
	}
	for name, doc := range map[string]string{
		"typo in a rate key": strings.Replace(good, "output", "ouput", 1),
		"rate as text":       strings.Replace(good, "input = 1", `input = "1"`, 1),
		"invalid TOML":       good + "[[model",
		"no source":          strings.Replace(good, `source = "https://example.com/p"`, "", 1),
	} {
		if _, err := Parse([]byte(doc)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err %v, want ErrInvalid", name, err)
		}
	}
}

// TestMatching pins the matching rule: exact name, then explicit alias,
// then nothing. Near misses never get a price.
func TestMatching(t *testing.T) {
	tbl := validTable()
	table := Merge(nil, &tbl)
	a := store.Price{Input: 1, Output: 2, Cache: 0.1, Source: store.PriceSourceDefault}
	for model, want := range map[string]*store.Price{
		"model-a": &a,
		"model.a": &a, // alias
		"model-b": {Input: 3, Output: 4, Cache: 0.3, Source: store.PriceSourceDefault},
		// No prefix, suffix, case or date matching.
		"model-a-20260101": nil,
		"model":            nil,
		"Model-A":          nil,
		"model-a ":         nil,
		"model.b":          nil,
		"":                 nil,
	} {
		got, ok := table[model]
		switch {
		case want == nil && ok:
			t.Errorf("%q got price %+v, want none", model, got)
		case want != nil && (!ok || got != *want):
			t.Errorf("%q got %+v (found %v), want %+v", model, got, ok, *want)
		}
	}
}

func TestMerge(t *testing.T) {
	tbl := validTable()
	custom := map[string]store.Price{
		"model-b":      {Input: 9, Output: 9, Cache: 9},
		"local-model":  {Input: 0.5},
		"model.a-only": {Input: 7},
	}
	table := Merge(custom, &tbl)
	want := map[string]store.Price{
		"model-a":      {Input: 1, Output: 2, Cache: 0.1, Source: store.PriceSourceDefault},
		"model.a":      {Input: 1, Output: 2, Cache: 0.1, Source: store.PriceSourceDefault},
		"model-b":      {Input: 9, Output: 9, Cache: 9, Source: store.PriceSourceCustom},
		"local-model":  {Input: 0.5, Source: store.PriceSourceCustom},
		"model.a-only": {Input: 7, Source: store.PriceSourceCustom},
	}
	if len(table) != len(want) {
		t.Fatalf("table %+v", table)
	}
	for m, p := range want {
		if table[m] != p {
			t.Errorf("%s: %+v, want %+v", m, table[m], p)
		}
	}

	// A custom price for a model carries to its aliases, unless the alias
	// is priced itself.
	table = Merge(map[string]store.Price{"model-a": {Input: 5}}, &tbl)
	if table["model.a"] != (store.Price{Input: 5, Source: store.PriceSourceCustom}) {
		t.Errorf("alias of a custom-priced model: %+v", table["model.a"])
	}
	table = Merge(map[string]store.Price{"model-a": {Input: 5}, "model.a": {Input: 6}}, &tbl)
	if table["model.a"].Input != 6 || table["model-a"].Input != 5 {
		t.Errorf("custom alias price: %+v", table)
	}

	// No defaults: only the user's prices, exactly as before.
	table = Merge(custom, nil)
	if len(table) != len(custom) || table["model-a"] != (store.Price{}) {
		t.Fatalf("without defaults: %+v", table)
	}
	if len(Merge(nil, nil)) != 0 {
		t.Fatal("no prices at all should be empty")
	}
}
