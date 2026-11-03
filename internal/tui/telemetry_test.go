package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/DanBradbury/firekeeper/internal/otelrecv"
)

func TestMarqueeLeavesFittingTextAlone(t *testing.T) {
	if got := marquee("short", 10, 7); got != "short" {
		t.Fatalf("marquee = %q, want unchanged", got)
	}
}

func TestMarqueeScrollsOverflowingText(t *testing.T) {
	text := "Background calls"
	loopLen := len(text) + len([]rune(marqueeGap))
	tests := []struct {
		frame int
		want  string
	}{
		{0, "Backgr"},
		{1, "ackgro"},
		{len(text) - 2, "ls   ·"},
		{loopLen, "Backgr"},
	}
	for _, tt := range tests {
		got := marquee(text, 6, tt.frame)
		if got != tt.want {
			t.Fatalf("frame %d: marquee = %q, want %q", tt.frame, got, tt.want)
		}
		if n := len([]rune(got)); n != 6 {
			t.Fatalf("frame %d: width %d, want 6", tt.frame, n)
		}
	}
}

func TestMarqueeTickAdvancesFrame(t *testing.T) {
	next, cmd := NewModel().Update(marqueeTickMsg{})
	if got := next.(model).marqueeFrame; got != 1 {
		t.Fatalf("marqueeFrame = %d, want 1", got)
	}
	if cmd == nil {
		t.Fatal("marquee tick did not schedule the next tick")
	}
}

func TestBackgroundCallsLineScrollsInTokensPanel(t *testing.T) {
	m := NewModel()
	m.processGroups = []processGroup{{tool: "Claude", sessions: []sessionInfo{{
		id:            "s1",
		tokens:        tokenBreakdown{Input: 1},
		telemetryLine: "Background calls (not in transcripts): 265.9K (prompt suggestions 124.3K)",
	}}}}
	first := m.dashTokenLines(20)
	m.marqueeFrame = 3
	later := m.dashTokenLines(20)
	if first[len(first)-1] == later[len(later)-1] {
		t.Fatal("background calls line did not move between frames")
	}
}

func TestWrapItemsPacksAndCuts(t *testing.T) {
	got := wrapItems([]string{"aaa", "bb", "cccc", "dddddddddd"}, " · ", 8)
	want := []string{"aaa · bb", "cccc", "ddddddd…"}
	if !slices.Equal(got, want) {
		t.Fatalf("wrapItems = %q, want %q", got, want)
	}
	if got := wrapItems(nil, " · ", 8); got != nil {
		t.Fatalf("wrapItems(nil) = %q, want nil", got)
	}
}

func TestSessionPanelWrapsBackgroundCalls(t *testing.T) {
	sup := otelrecv.Supplement{
		Covered: true, Transcript: 3_400_000, Added: 265_900, Exact: 3_665_900,
		BySource: map[string]int64{
			"prompt_suggestion": 124_300, "generate_session_title": 1_500,
			"away_summary": 126_500, "web_search_tool": 13_500,
		},
	}
	groups := annotateClaudeTelemetry([]processGroup{{
		tool:      "Claude",
		root:      processInfo{pid: 1, tty: "ttys002"},
		processes: []processInfo{{pid: 1, tty: "ttys002", command: "claude"}},
		sessions:  []sessionInfo{{id: "s1", tokens: tokenBreakdown{Input: 68, Output: 35_500, CacheRead: 3_200_000}}},
	}}, map[string]otelrecv.Supplement{"s1": sup})
	m := NewModel()
	m.width, m.height = 60, 50
	m.processGroups = groups

	first := ansi.Strip(m.View())
	m.marqueeFrame = 5
	if later := ansi.Strip(m.View()); !strings.Contains(later, "prompt suggestions 124.3K") {
		t.Fatalf("background calls missing at a later frame:\n%s", later)
	}
	for _, want := range []string{"background calls 265.9K (not in transcripts)", "prompt suggestions 124.3K", "titles 1.5K", "away_summary 126.5K", "web_search_tool 13.5K", "transcripts 3.4M", "PID 1"} {
		if !strings.Contains(first, want) {
			t.Fatalf("session panel missing %q:\n%s", want, first)
		}
	}
	if layout := m.dashboardLayout(); layout.detail <= dashDetailMaxRows {
		t.Fatalf("detail rows = %d, want more than %d to fit the wrapped calls", layout.detail, dashDetailMaxRows)
	}
}
