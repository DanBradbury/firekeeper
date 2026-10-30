package config

import "testing"

// SEC-26-3 on Windows itself: drive and component case, and either
// separator, all reach an excluded directory.
func TestExcludedWindowsSpellings(t *testing.T) {
	c := Config{Exclude: []string{`C:\Users\Dan\clients\acme`, `D:\Work\*\secret`}}
	tests := []struct {
		dir  string
		want string
	}{
		{`C:\Users\Dan\clients\acme`, `C:\Users\Dan\clients\acme`},
		{`c:\Users\Dan\clients\acme\api`, `C:\Users\Dan\clients\acme`},
		{`C:\users\dan\CLIENTS\Acme\api`, `C:\Users\Dan\clients\acme`},
		{`c:/Users/Dan/clients/acme/api`, `C:\Users\Dan\clients\acme`},
		{`C:\Users/Dan\clients/acme`, `C:\Users\Dan\clients\acme`},
		{`d:\work\Proj\SECRET\x`, `D:\Work\*\secret`},
		{`C:\Users\Dan\clients\acme-two`, ""},
		{`C:\Users\Dan\clients`, ""},
		{`E:\Users\Dan\clients\acme`, ""},
	}
	for _, tt := range tests {
		got, ok := c.Excluded([]string{tt.dir}, nil)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("Excluded(%q) = (%q, %v), want %q", tt.dir, got, ok, tt.want)
		}
	}
}
