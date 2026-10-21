package contract

import (
	"fmt"
	"sort"
)

// MatchShape compares the structure of two JSON bodies and ignores their
// values. golden is usually a Fixture and got a live response. They match
// when
//
//   - objects have exactly the same keys, recursively;
//   - values have the same JSON type, except that null on either side matches
//     anything, since optional fields are null until set;
//   - every element of a got array matches the first element of the golden
//     array (any elements, including none, when the golden array is empty).
//
// The error lists every difference by path. It never includes values.
func MatchShape(golden, got []byte) error {
	g, err := decodeJSON(golden)
	if err != nil {
		return fmt.Errorf("golden: %w", err)
	}
	v, err := decodeJSON(got)
	if err != nil {
		return fmt.Errorf("got: %w", err)
	}
	return joinProblems(diffShape("$", g, v))
}

func diffShape(path string, golden, got any) []string {
	if golden == nil || got == nil {
		return nil
	}
	if jsonKind(golden) != jsonKind(got) {
		return []string{fmt.Sprintf("%s: want %s, got %s", path, jsonKind(golden), jsonKind(got))}
	}
	var out []string
	switch g := golden.(type) {
	case map[string]any:
		v := got.(map[string]any)
		keys := map[string]bool{}
		for k := range g {
			keys[k] = true
		}
		for k := range v {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			gv, inGolden := g[k]
			vv, inGot := v[k]
			switch {
			case !inGot:
				out = append(out, fmt.Sprintf("%s.%s: missing", path, k))
			case !inGolden:
				out = append(out, fmt.Sprintf("%s.%s: unexpected", path, k))
			default:
				out = append(out, diffShape(path+"."+k, gv, vv)...)
			}
		}
	case []any:
		v := got.([]any)
		if len(g) == 0 {
			return nil
		}
		for i, item := range v {
			out = append(out, diffShape(fmt.Sprintf("%s[%d]", path, i), g[0], item)...)
		}
	}
	return out
}
