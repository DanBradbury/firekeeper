package main

import (
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	forestWidth         = 320
	forestHeight        = 192
	forestGroundSize    = 16
	forestGroundTiles   = 21
	forestTreeWidth     = 48
	forestTreeHeight    = 64
	forestTreeCount     = 10
	forestPathSize      = 48
	forestPathCount     = 10
	forestFireWidth     = 16
	forestFireHeight    = 32
	forestFireFrames    = 8
	forestObjectWidth   = 16
	forestObjectHeight  = 32
	forestObjectCount   = 13
	forestFrameDuration = 140 * time.Millisecond
	forestInterfaceRows = 3
	forestTilesetWidth  = 480
	forestTilesetHeight = 224
	forestTreesY        = 16
	forestPathsY        = 112
	forestFireY         = 160
	forestObjectsY      = 192
)

//go:embed "forest-tileset.png"
var forestTilesetPNG []byte

type forestLayer int

const (
	forestGroundLayer forestLayer = iota
	forestPathLayer
	forestFullLayer
)

func (l forestLayer) String() string {
	switch l {
	case forestGroundLayer:
		return "ground"
	case forestPathLayer:
		return "ground + paths"
	default:
		return "full scene"
	}
}

type forestModel struct {
	width, height int
	tileset       sprite
	frame         int
	layer         forestLayer
	playing       bool
	renderer      spriteRenderer
}

func newForestModel(tileset sprite, renderer spriteRenderer) forestModel {
	return forestModel{
		width:    80,
		height:   30,
		tileset:  tileset,
		layer:    forestFullLayer,
		playing:  true,
		renderer: renderer,
	}
}

func (m forestModel) Init() tea.Cmd {
	return tick(forestFrameDuration)
}

func (m forestModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.KeyMsg:
		switch message.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case " ":
			m.playing = !m.playing
		case "l", "tab", "right":
			m.layer = (m.layer + 1) % 3
		case "shift+tab", "left":
			m.layer = (m.layer + 2) % 3
		}
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
	case tickMsg:
		if m.playing {
			m.frame = (m.frame + 1) % forestFireFrames
		}
		return m, tick(forestFrameDuration)
	}
	return m, nil
}

func (m forestModel) View() string {
	if m.width < 20 || m.height < 8 {
		return "Terminal too small (need 20x8)\n"
	}

	contentRows := m.height - forestInterfaceRows
	scene := composeForestScene(m.tileset, m.frame, m.layer)
	content := m.viewForest(scene, contentRows)
	state := "playing"
	if !m.playing {
		state = "paused"
	}

	return strings.Join([]string{
		fitLine("  FOREST TILESET  •  layered background demo", m.width),
		content,
		fitLine("  L/Tab layer  •  Space pause fire  •  q quit", m.width),
		fitLine(fmt.Sprintf("  %s  •  fire %d/%d  •  %s", m.layer, m.frame+1, forestFireFrames, state), m.width),
	}, "\n")
}

func (m forestModel) viewForest(scene sprite, rows int) string {
	if m.renderer == kittyRenderer {
		return viewKittyScene(scene, m.width, rows)
	}

	frame := scene.fit(m.width, rows*2)
	left := (m.width - frame.width) / 2
	top := (rows*2 - frame.height) / 2
	canvas := newCanvas(m.width, rows*2, background)
	canvas.drawSprite(left, top, frame)
	return canvas.render()
}

func viewKittyScene(scene sprite, width, rows int) string {
	lines := make([]string, rows)
	for row := range lines {
		lines[row] = strings.Repeat(" ", width)
	}
	if width < 1 || rows < 1 {
		return strings.Join(lines, "\n")
	}

	columns := min(width, rows*2*scene.width/scene.height)
	columns = max(columns, 1)
	imageRows := max(1, columns*scene.height/(scene.width*2))
	imageRows = min(imageRows, rows)
	upload, err := encodeKittySprite(scene, columns, imageRows)
	if err != nil {
		return strings.Join(lines, "\n")
	}

	left := (width - columns) / 2
	top := (rows - imageRows) / 2
	for row := 0; row < imageRows; row++ {
		prefix := strings.Repeat(" ", left)
		if row == 0 {
			prefix += upload
		}
		lines[top+row] = prefix + kittySpritePlaceholderRow(kittyImageID, kittyPlacementID, row, columns)
	}
	return strings.Join(lines, "\n")
}

// composeForestScene draws back-to-front. Every output pixel first receives a
// ground tile; paths and tall sprites are then alpha-blended over that base.
func composeForestScene(tileset sprite, fireFrame int, layer forestLayer) sprite {
	scene := opaqueSprite(forestWidth, forestHeight)

	for y := 0; y < forestHeight; y += forestGroundSize {
		for x := 0; x < forestWidth; x += forestGroundSize {
			column := x / forestGroundSize
			row := y / forestGroundSize
			variant := (column*7 + row*11 + column*row) % forestGroundTiles
			scene.draw(x, y, tileset.crop(variant*forestGroundSize, 0, forestGroundSize, forestGroundSize))
		}
	}
	if layer == forestGroundLayer {
		return scene
	}

	paths := []placement{
		{0, 0, 80}, {1, 48, 80}, {2, 96, 80}, {3, 144, 80},
		{4, 192, 80}, {5, 240, 80}, {6, 272, 80},
		{7, 136, 32}, {8, 136, 128}, {9, 16, 128},
	}
	for _, item := range paths {
		scene.draw(item.x, item.y, tileset.crop(item.index*forestPathSize, forestPathsY, forestPathSize, forestPathSize))
	}
	if layer == forestPathLayer {
		return scene
	}

	// Tree root position controls draw order. Far trees render before near trees.
	trees := []placement{
		{0, -8, 4}, {1, 32, 0}, {2, 72, 6}, {3, 112, -2}, {4, 160, 4},
		{5, 208, -4}, {6, 256, 2}, {7, 288, 8},
		{8, -12, 94}, {9, 284, 96}, {2, 26, 134}, {6, 250, 136},
	}
	for _, item := range trees {
		scene.draw(item.x, item.y, tileset.crop(item.index*forestTreeWidth, forestTreesY, forestTreeWidth, forestTreeHeight))
	}

	objects := []placement{
		{0, 72, 56}, {1, 104, 143}, {2, 124, 146}, {3, 144, 143},
		{4, 164, 145}, {5, 184, 144}, {6, 204, 142}, {7, 224, 144},
		{8, 56, 137}, {9, 88, 139}, {10, 236, 55}, {11, 252, 54}, {12, 268, 55},
	}
	for _, item := range objects {
		scene.draw(item.x, item.y, tileset.crop(item.index*forestObjectWidth, forestObjectsY, forestObjectWidth, forestObjectHeight))
	}

	frame := ((fireFrame % forestFireFrames) + forestFireFrames) % forestFireFrames
	scene.draw(152, 96, tileset.crop(frame*forestFireWidth, forestFireY, forestFireWidth, forestFireHeight))
	return scene
}

type placement struct {
	index int
	x, y  int
}

func opaqueSprite(width, height int) sprite {
	pixels := make([]rgba, width*height)
	for index := range pixels {
		pixels[index].a = 255
	}
	return sprite{width: width, height: height, pixels: pixels}
}

func (s sprite) crop(x, y, width, height int) sprite {
	result := sprite{width: width, height: height, pixels: make([]rgba, width*height)}
	for row := 0; row < height; row++ {
		for column := 0; column < width; column++ {
			result.pixels[row*width+column] = s.at(x+column, y+row)
		}
	}
	return result
}

func (s *sprite) draw(x, y int, source sprite) {
	for row := 0; row < source.height; row++ {
		for column := 0; column < source.width; column++ {
			destinationX, destinationY := x+column, y+row
			if destinationX < 0 || destinationX >= s.width || destinationY < 0 || destinationY >= s.height {
				continue
			}

			over := source.at(column, row)
			if over.a == 0 {
				continue
			}
			under := s.at(destinationX, destinationY)
			alpha := int(over.a)
			inverse := 255 - alpha
			s.pixels[destinationY*s.width+destinationX] = rgba{
				r: uint8((int(over.r)*alpha + int(under.r)*inverse + 127) / 255),
				g: uint8((int(over.g)*alpha + int(under.g)*inverse + 127) / 255),
				b: uint8((int(over.b)*alpha + int(under.b)*inverse + 127) / 255),
				a: 255,
			}
		}
	}
}

func writeForestPreview(output io.Writer, tileset sprite) error {
	scene := composeForestScene(tileset, 2, forestFullLayer)
	preview := image.NewNRGBA(image.Rect(0, 0, scene.width, scene.height))
	for y := 0; y < scene.height; y++ {
		for x := 0; x < scene.width; x++ {
			pixel := scene.at(x, y)
			preview.SetNRGBA(x, y, color.NRGBA{R: pixel.r, G: pixel.g, B: pixel.b, A: pixel.a})
		}
	}
	if err := png.Encode(output, preview); err != nil {
		return fmt.Errorf("encode forest preview: %w", err)
	}
	return nil
}

func validateForestTileset(tileset sprite) error {
	if tileset.width != forestTilesetWidth || tileset.height != forestTilesetHeight {
		return fmt.Errorf("forest tileset must be %dx%d, got %dx%d", forestTilesetWidth, forestTilesetHeight, tileset.width, tileset.height)
	}
	return nil
}
