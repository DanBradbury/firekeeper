package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi/kitty"
)

const (
	sheetColumns         = 10
	sheetRows            = 10
	frameWidth           = 32
	frameHeight          = 32
	displayColumns       = 16
	displayRows          = 8
	imageID              = 42
	placementID          = 1
	defaultFrameDuration = 120 * time.Millisecond
	rateStep             = 20 * time.Millisecond
	minimumFrameDuration = 40 * time.Millisecond
	maximumFrameDuration = time.Second
)

type tickMsg time.Time

type model struct {
	width, height int
	frames        [][]string
	animation     int
	frame         int
	frameDuration time.Duration
	playing       bool
}

func newModel(frames [][]string) model {
	return model{
		width:         80,
		height:        24,
		frames:        frames,
		frameDuration: defaultFrameDuration,
		playing:       true,
	}
}

func (m model) Init() tea.Cmd {
	return tick(m.frameDuration)
}

func tick(duration time.Duration) tea.Cmd {
	return tea.Tick(duration, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "left", "h":
			m.animation = (m.animation - 1 + len(m.frames)) % len(m.frames)
			m.frame = 0
		case "right", "l":
			m.animation = (m.animation + 1) % len(m.frames)
			m.frame = 0
		case "up", "k":
			m.frameDuration = max(m.frameDuration-rateStep, minimumFrameDuration)
		case "down", "j":
			m.frameDuration = min(m.frameDuration+rateStep, maximumFrameDuration)
		case " ":
			m.playing = !m.playing
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tickMsg:
		if m.playing {
			m.frame = (m.frame + 1) % len(m.frames[m.animation])
		}
		return m, tick(m.frameDuration)
	}
	return m, nil
}

func (m model) View() string {
	if m.width < 28 || m.height < displayRows+4 {
		return fmt.Sprintf("Terminal too small (need 28x%d)\n", displayRows+4)
	}

	lines := make([]string, m.height)
	for row := range lines {
		lines[row] = strings.Repeat(" ", m.width)
	}
	lines[0] = fitLine("  KITTY PIXEL SPRITE  •  32×32 PNG → 16×8 terminal cells", m.width)

	top := max((m.height-displayRows)/2, 2)
	left := max((m.width-displayColumns)/2, 0)
	upload := m.frames[m.animation][m.frame]
	for row := 0; row < displayRows; row++ {
		prefix := strings.Repeat(" ", left)
		if row == 0 {
			prefix += upload
		}
		placeholder := kittyPlaceholderRow(imageID, placementID, row, displayColumns)
		lines[top+row] = prefix + placeholder + strings.Repeat(" ", max(m.width-left-displayColumns, 0))
	}

	state := "playing"
	if !m.playing {
		state = "paused"
	}
	lines[m.height-2] = fitLine("  ←/→ animation  •  ↑ faster  •  ↓ slower  •  space pause  •  q quit", m.width)
	lines[m.height-1] = fitLine(fmt.Sprintf(
		"  animation %02d/%02d  •  frame %02d/%02d  •  %d ms  •  %s",
		m.animation+1,
		len(m.frames),
		m.frame+1,
		len(m.frames[m.animation]),
		m.frameDuration.Milliseconds(),
		state,
	), m.width)
	return strings.Join(lines, "\n")
}

func loadFrames(path string) ([][]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open spritesheet: %w", err)
	}
	defer file.Close()

	sheet, err := png.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("decode spritesheet: %w", err)
	}
	if sheet.Bounds().Dx() != sheetColumns*frameWidth || sheet.Bounds().Dy() != sheetRows*frameHeight {
		return nil, fmt.Errorf(
			"spritesheet is %dx%d, want %dx%d",
			sheet.Bounds().Dx(),
			sheet.Bounds().Dy(),
			sheetColumns*frameWidth,
			sheetRows*frameHeight,
		)
	}

	frames := make([][]string, sheetRows)
	for animation := 0; animation < sheetRows; animation++ {
		frames[animation] = make([]string, sheetColumns)
		for frame := 0; frame < sheetColumns; frame++ {
			bounds := image.Rect(0, 0, frameWidth, frameHeight)
			cropped := image.NewNRGBA(bounds)
			sourcePoint := image.Pt(
				sheet.Bounds().Min.X+frame*frameWidth,
				sheet.Bounds().Min.Y+animation*frameHeight,
			)
			draw.Draw(cropped, bounds, sheet, sourcePoint, draw.Src)

			encoded, err := encodeKittyFrame(cropped)
			if err != nil {
				return nil, fmt.Errorf("encode animation %d frame %d: %w", animation, frame, err)
			}
			frames[animation][frame] = encoded
		}
	}
	return frames, nil
}

func encodeKittyFrame(frame image.Image) (string, error) {
	var output bytes.Buffer
	options := &kitty.Options{
		Action:           kitty.TransmitAndPut,
		Quite:            2,
		ID:               imageID,
		PlacementID:      placementID,
		Format:           kitty.PNG,
		Transmission:     kitty.Direct,
		Chunk:            true,
		Columns:          displayColumns,
		Rows:             displayRows,
		VirtualPlacement: true,
		DoNotMoveCursor:  true,
	}
	if err := kitty.EncodeGraphics(&output, frame, options); err != nil {
		return "", err
	}
	return output.String(), nil
}

func kittyPlaceholderRow(id, placement, row, columns int) string {
	red := (id >> 16) & 0xff
	green := (id >> 8) & 0xff
	blue := id & 0xff
	placementRed := (placement >> 16) & 0xff
	placementGreen := (placement >> 8) & 0xff
	placementBlue := placement & 0xff
	var output strings.Builder
	fmt.Fprintf(
		&output,
		"\x1b[38;2;%d;%d;%d;58;2;%d;%d;%dm",
		red,
		green,
		blue,
		placementRed,
		placementGreen,
		placementBlue,
	)
	for column := 0; column < columns; column++ {
		output.WriteRune(kitty.Placeholder)
		output.WriteRune(kitty.Diacritic(row))
		output.WriteRune(kitty.Diacritic(column))
	}
	output.WriteString("\x1b[39;59m")
	return output.String()
}

func fitLine(value string, width int) string {
	runes := []rune(value)
	if len(runes) > width {
		return string(runes[:width])
	}
	return value + strings.Repeat(" ", width-len(runes))
}

func main() {
	sheetPath := flag.String("sheet", "ranger spritesheet calciumtrice.png", "path to 10x10 PNG spritesheet")
	flag.Parse()

	frames, err := loadFrames(*sheetPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kitty demo failed: %v\n", err)
		os.Exit(1)
	}

	program := tea.NewProgram(newModel(frames), tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "kitty demo failed: %v\n", err)
		os.Exit(1)
	}
}
