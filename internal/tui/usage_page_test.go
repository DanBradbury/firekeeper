package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestUsageFitStatKeepsNameAndFloatsValueRight(t *testing.T) {
	line := usageStat("Cache write", "2.2M", "")
	for _, inner := range []int{30, 18, 12} {
		got := ansi.Strip(usageFitStat(line, inner))
		if !strings.HasPrefix(got, "Cache write") {
			t.Fatalf("inner %d: name truncated: %q", inner, got)
		}
		if ansi.StringWidth(got) != inner {
			t.Fatalf("inner %d: width = %d, want %d: %q", inner, ansi.StringWidth(got), inner, got)
		}
		if inner >= 16 && !strings.HasSuffix(got, "2.2M") {
			t.Fatalf("inner %d: value not flush right: %q", inner, got)
		}
	}
}
