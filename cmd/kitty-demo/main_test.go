package main

import (
	"image"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi/kitty"
)

func TestKittyTransmissionCarriesDisplayMetadata(t *testing.T) {
	frame := image.NewNRGBA(image.Rect(0, 0, frameWidth, frameHeight))
	transmission, err := encodeKittyFrame(frame)
	if err != nil {
		t.Fatalf("encode Kitty frame: %v", err)
	}

	for _, option := range []string{"f=100", "q=2", "i=42", "p=1", "U=1", "C=1", "c=16", "r=8", "a=T"} {
		if !strings.Contains(transmission, option) {
			t.Errorf("transmission missing %q", option)
		}
	}
}

func TestKittyPlaceholderRowUsesDisplayWidth(t *testing.T) {
	row := kittyPlaceholderRow(imageID, placementID, 0, displayColumns)
	count := 0
	remaining := row
	for len(remaining) > 0 {
		r, size := utf8.DecodeRuneInString(remaining)
		if r == kitty.Placeholder {
			count++
		}
		remaining = remaining[size:]
	}
	if count != displayColumns {
		t.Fatalf("placeholder count = %d, want %d", count, displayColumns)
	}
	for column := 0; column < displayColumns; column++ {
		want := string([]rune{kitty.Placeholder, kitty.Diacritic(0), kitty.Diacritic(column)})
		if !strings.Contains(row, want) {
			t.Errorf("placeholder missing coordinates (0, %d)", column)
		}
	}
}

func TestDisplayIsHalfPortableRendererFootprint(t *testing.T) {
	if displayColumns != frameWidth/2 {
		t.Fatalf("display width = %d, want %d", displayColumns, frameWidth/2)
	}
	if displayRows != frameHeight/4 {
		t.Fatalf("display height = %d, want %d", displayRows, frameHeight/4)
	}
}

func TestViewRejectsHeightTooSmallForSprite(t *testing.T) {
	m := newModel([][]string{{""}})
	m.width = 80
	m.height = displayRows + 3

	if got := m.View(); !strings.Contains(got, "Terminal too small") {
		t.Fatalf("small terminal view = %q", got)
	}
}
