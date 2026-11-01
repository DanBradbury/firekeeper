package tui

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// mouse_select.go is drag selection: dragging selects text on screen,
// double-clicking selects a token and a held second press extends it by whole
// words. Releasing the button copies the text to the clipboard.

// borderRunes are panel frame and gutter glyphs; they are never part of a token
// and are dropped from copied text.
const borderRunes = "│╭╮╰╯─▍"

type copyResultMsg struct {
	chars int
	err   error
}

// copyToClipboard writes text to the system clipboard; tests replace it.
var copyToClipboard = copyText

func copyCmd(text string) tea.Cmd {
	return func() tea.Msg {
		return copyResultMsg{chars: len([]rune(text)), err: copyToClipboard(text)}
	}
}

// copyText uses the platform clipboard tool when there is one and otherwise
// asks the terminal to set the clipboard (OSC 52), which also works over SSH.
func copyText(text string) error {
	var candidates [][]string
	switch runtime.GOOS {
	case "darwin":
		candidates = [][]string{{"pbcopy"}}
	case "linux":
		candidates = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	}
	for _, args := range candidates {
		path, err := exec.LookPath(args[0])
		if err != nil {
			continue
		}
		command := exec.Command(path, args[1:]...)
		command.Stdin = strings.NewReader(text)
		if command.Run() == nil {
			return nil
		}
	}
	_, err := os.Stdout.WriteString(ansi.SetSystemClipboard(text))
	return err
}

func (m *model) clearSelection() {
	m.mouse.dragging, m.mouse.wordMode = false, false
	m.mouse.anchor, m.mouse.cursor = cell{}, cell{}
	m.mouse.anchorWord = [2]int{}
}

// screenRows is the rendered screen split into rows.
func (m model) screenRows() []string { return strings.Split(m.screen(), "\n") }

type wordSpan struct{ start, end int }

// wordAt returns the token under column x on a plain row: a run of cells that
// are neither spaces nor frame glyphs.
func wordAt(row string, x int) (wordSpan, bool) {
	column := 0
	var spans []wordSpan
	current := -1
	for _, r := range row {
		width := ansi.StringWidth(string(r))
		separator := unicode.IsSpace(r) || strings.ContainsRune(borderRunes, r)
		switch {
		case separator && current >= 0:
			spans = append(spans, wordSpan{current, column})
			current = -1
		case !separator && current < 0:
			current = column
		}
		column += width
	}
	if current >= 0 {
		spans = append(spans, wordSpan{current, column})
	}
	for _, span := range spans {
		if x >= span.start && x < span.end {
			return span, true
		}
	}
	return wordSpan{}, false
}

func (m *model) startWordSelection(at cell) {
	rows := m.screenRows()
	if at.y < 0 || at.y >= len(rows) {
		return
	}
	span, ok := wordAt(ansi.Strip(rows[at.y]), at.x)
	if !ok {
		span = wordSpan{at.x, at.x + 1}
	}
	m.mouse.dragging, m.mouse.wordMode = true, true
	m.mouse.anchor, m.mouse.cursor = at, at
	m.mouse.anchorWord = [2]int{span.start, span.end}
}

func (m *model) dragTo(at cell) {
	m.mouse.cursor = cell{x: min(max(at.x, 0), m.width-1), y: min(max(at.y, 0), m.height-1)}
}

func (m model) endDrag() (tea.Model, tea.Cmd) {
	m.mouse.dragging = false
	from, to, ok := m.selectionRange()
	if !ok {
		m.clearSelection()
		return m, nil
	}
	text := selectionText(m.screenRows(), from, to)
	if strings.TrimSpace(text) == "" {
		m.clearSelection()
		return m, nil
	}
	return m, copyCmd(text)
}

func before(a, b cell) bool { return a.y < b.y || (a.y == b.y && a.x < b.x) }

// selectionRange is the selected cells as a start and an exclusive end. A
// plain click without a drag selects nothing.
func (m model) selectionRange() (from, to cell, ok bool) {
	anchor, cursor := m.mouse.anchor, m.mouse.cursor
	if m.mouse.wordMode {
		rows := m.screenRows()
		start, end := cell{m.mouse.anchorWord[0], anchor.y}, cell{m.mouse.anchorWord[1], anchor.y}
		current := cell{cursor.x, cursor.y}
		currentEnd := cell{cursor.x + 1, cursor.y}
		if cursor.y >= 0 && cursor.y < len(rows) {
			if span, found := wordAt(ansi.Strip(rows[cursor.y]), cursor.x); found {
				current, currentEnd = cell{span.start, cursor.y}, cell{span.end, cursor.y}
			}
		}
		if before(current, start) {
			start = current
		}
		if before(end, currentEnd) {
			end = currentEnd
		}
		return start, end, true
	}
	if anchor == cursor {
		return cell{}, cell{}, false
	}
	lo, hi := anchor, cursor
	if before(hi, lo) {
		lo, hi = hi, lo
	}
	return lo, cell{hi.x + 1, hi.y}, true
}

// hasSelection reports whether a selection is on screen.
func (m model) hasSelection() bool {
	_, _, ok := m.selectionRange()
	return ok
}

// highlightSelection draws the selected cells in reverse video.
func (m model) highlightSelection(rows []string) []string {
	from, to, ok := m.selectionRange()
	if !ok {
		return rows
	}
	for y := from.y; y <= to.y && y < len(rows); y++ {
		if y < 0 {
			continue
		}
		left, right := 0, m.width
		if y == from.y {
			left = from.x
		}
		if y == to.y {
			right = to.x
		}
		left, right = max(left, 0), min(right, m.width)
		if left >= right {
			continue
		}
		rows[y] = ansi.Cut(rows[y], 0, left) +
			"\x1b[0;7m" + ansi.Strip(ansi.Cut(rows[y], left, right)) + "\x1b[0m" +
			ansi.Cut(rows[y], right, m.width)
	}
	return rows
}

// selectionText extracts the selected cells from the rendered rows as plain
// text, without panel frames or trailing padding.
func selectionText(rows []string, from, to cell) string {
	var out []string
	for y := from.y; y <= to.y && y < len(rows); y++ {
		if y < 0 {
			continue
		}
		plain := ansi.Strip(rows[y])
		left, right := 0, ansi.StringWidth(plain)
		if y == from.y {
			left = from.x
		}
		if y == to.y {
			right = min(to.x, right)
		}
		line := ""
		if left < right {
			line = ansi.Cut(plain, left, right)
		}
		out = append(out, cleanSelectedLine(line))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// cleanSelectedLine drops a frame or gutter at either edge, and any row made
// only of frame glyphs, so copied text is the content.
func cleanSelectedLine(line string) string {
	line = strings.TrimRight(line, " ")
	if strings.Trim(line, borderRunes+" ") == "" {
		return ""
	}
	trimmed := strings.TrimLeft(line, " ")
	if strings.HasPrefix(trimmed, "╭") {
		// A panel's top edge carries its title between the corners.
		return strings.Trim(strings.TrimLeft(trimmed, "╭─"), "─╮ ")
	}
	for _, glyph := range []string{"│ ", "▍ "} {
		if strings.HasPrefix(trimmed, glyph) {
			line = strings.TrimPrefix(trimmed, glyph)
			break
		}
	}
	line = strings.TrimRight(strings.TrimSuffix(strings.TrimRight(line, " "), "│"), " ")
	return line
}

// View draws the screen, then the selection and any context menu over it.
func (m model) View() string {
	view := m.screen()
	if m.width < 20 || m.height < 6 || (m.menu == nil && !m.hasSelection()) {
		return view
	}
	rows := strings.Split(view, "\n")
	if m.hasSelection() {
		rows = m.highlightSelection(rows)
	}
	if m.menu != nil {
		rows = m.overlayMenu(rows)
	}
	return strings.Join(rows, "\n")
}
