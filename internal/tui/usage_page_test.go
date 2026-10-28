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

func TestUsageFooterMatchesProcessesFooterStyle(t *testing.T) {
	m := model{width: 100, height: 30, activeTab: usageTab}
	usageHelp, usageStatus := m.usageFooter()
	processHelp, processStatus := m.processFooter()
	for name, line := range map[string]string{"usage help": usageHelp, "usage status": usageStatus} {
		if got := ansi.StringWidth(line); got != m.width {
			t.Errorf("%s width = %d, want %d", name, got, m.width)
		}
	}
	if !strings.Contains(usageHelp, dashPaint("tab ", dashBlue, true)) {
		t.Errorf("usage help keys are not painted like the Processes footer: %q", usageHelp)
	}
	if len(usageHelp) == 0 || len(processHelp) == 0 || len(usageStatus) == 0 || len(processStatus) == 0 {
		t.Fatal("empty footer")
	}
}

func TestUsageFitStatPaintsGapWithPanelBackground(t *testing.T) {
	got := usageFitStat(usageStat("Output", "374.9K", ""), 30)
	gap := dashPaint(strings.Repeat(" ", 30-len("Output")-len("374.9K")), usageTextColor, false)
	if !strings.Contains(got, gap) {
		t.Fatalf("gap between name and value is not painted: %q", got)
	}
}
