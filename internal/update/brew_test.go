package update

import (
	"regexp"
	"strings"
	"testing"
)

func TestParseBrewInfo(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want brewFormulaState
	}{
		{
			name: "upgraded",
			in:   `{"formulae":[{"name":"firekeeper","versions":{"stable":"0.10.3"},"linked_keg":"0.10.3"}],"casks":[]}`,
			want: brewFormulaState{Formula: "0.10.3", Linked: "0.10.3"},
		},
		{
			name: "stale tap",
			in:   `{"formulae":[{"versions":{"stable":"0.10.2"},"linked_keg":"0.10.2"}]}`,
			want: brewFormulaState{Formula: "0.10.2", Linked: "0.10.2"},
		},
		{
			name: "revision suffix on the keg",
			in:   `{"formulae":[{"versions":{"stable":"0.10.3"},"linked_keg":"0.10.3_1"}]}`,
			want: brewFormulaState{Formula: "0.10.3", Linked: "0.10.3"},
		},
		{
			name: "not linked",
			in:   `{"formulae":[{"versions":{"stable":"0.10.3"},"linked_keg":null}]}`,
			want: brewFormulaState{Formula: "0.10.3"},
		},
		{
			name: "fields missing",
			in:   `{"formulae":[{}]}`,
			want: brewFormulaState{},
		},
		{
			name: "unexpected version text is dropped",
			in:   `{"formulae":[{"versions":{"stable":"HEAD-abc\u001b[2J"},"linked_keg":"0.10.3; rm"}]}`,
			want: brewFormulaState{},
		},
		{
			name: "numbers too large to compare are dropped",
			in:   `{"formulae":[{"versions":{"stable":"99999999999999999999.0.0"},"linked_keg":"0.1.0"}]}`,
			want: brewFormulaState{Linked: "0.1.0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseBrewInfo([]byte(tt.in))
			if err != nil {
				t.Fatalf("parseBrewInfo: %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseBrewInfo = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseBrewInfoRejects(t *testing.T) {
	for name, in := range map[string]string{
		"empty":          ``,
		"not json":       `Error: No available formula with the name "firekeeper".`,
		"no formulae":    `{"formulae":[],"casks":[]}`,
		"two formulae":   `{"formulae":[{"versions":{"stable":"0.10.3"}},{"versions":{"stable":"0.10.3"}}]}`,
		"wrong type":     `{"formulae":[{"versions":{"stable":10}}]}`,
		"truncated":      `{"formulae":[{"versions":{"stable":"0.10.3"`,
		"top-level list": `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := parseBrewInfo([]byte(in)); err == nil {
				t.Fatalf("parseBrewInfo accepted %q as %+v", in, got)
			}
		})
	}
}

func TestBrewFormulaStateCheck(t *testing.T) {
	tests := []struct {
		name    string
		state   brewFormulaState
		wantErr []string // substrings of the error; nil means success
	}{
		{name: "linked the new version", state: brewFormulaState{Formula: "0.10.3", Linked: "0.10.3"}},
		{name: "linked a newer version", state: brewFormulaState{Formula: "0.10.4", Linked: "0.10.4"}},
		{
			name:    "stale tap, nothing to upgrade",
			state:   brewFormulaState{Formula: "0.10.2", Linked: "0.10.2"},
			wantErr: []string{"still at 0.10.2", "nothing to upgrade to 0.10.3", "brew update", "brew upgrade DanBradbury/tap/firekeeper"},
		},
		{
			name:    "formula version unknown",
			state:   brewFormulaState{Linked: "0.10.2"},
			wantErr: []string{"still at unknown", "brew update"},
		},
		{
			name:    "formula current but old version linked",
			state:   brewFormulaState{Formula: "0.10.3", Linked: "0.10.2"},
			wantErr: []string{"0.10.3 available", "linked firekeeper is 0.10.2"},
		},
		{
			name:    "formula current but nothing linked",
			state:   brewFormulaState{Formula: "0.10.3"},
			wantErr: []string{"linked firekeeper is none"},
		},
		{name: "nothing known", state: brewFormulaState{}, wantErr: []string{"still at unknown"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.state.check("0.10.3")
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("check = %v, want success", err)
				}
				return
			}
			if err == nil {
				t.Fatal("check succeeded, want an error")
			}
			for _, s := range tt.wantErr {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not mention %q", err, s)
				}
			}
		})
	}
}

var plainVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func FuzzParseBrewInfo(f *testing.F) {
	for _, s := range []string{
		`{"formulae":[{"versions":{"stable":"0.10.3"},"linked_keg":"0.10.3"}]}`,
		`{"formulae":[{"versions":{"stable":"v1.2.3"},"linked_keg":"1.2.3_4"}]}`,
		`{"formulae":[{"linked_keg":null}]}`,
		`{"formulae":[]}`,
		`not json`,
		``,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		st, err := parseBrewInfo(b)
		if err != nil {
			if st != (brewFormulaState{}) {
				t.Fatalf("error %v came with state %+v", err, st)
			}
			return
		}
		for _, v := range []string{st.Formula, st.Linked} {
			if v != "" && !plainVersionPattern.MatchString(v) {
				t.Fatalf("parseBrewInfo produced version %q", v)
			}
		}
		// check must not panic on anything the parser lets through.
		_ = st.check("0.10.3")
	})
}
