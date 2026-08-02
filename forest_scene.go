package main

import (
	_ "embed"
	"fmt"
)

const (
	forestWidth         = 320
	forestHeight        = 192
	forestGroundSize    = 16
	forestFireWidth     = 16
	forestFireHeight    = 32
	forestFireFrames    = 8
	forestTilesetWidth  = 480
	forestTilesetHeight = 224
	forestFireY         = 160
)

//go:embed "forest-tileset.png"
var forestTilesetPNG []byte

func validateForestTileset(tileset sprite) error {
	if tileset.width != forestTilesetWidth || tileset.height != forestTilesetHeight {
		return fmt.Errorf("forest tileset must be %dx%d, got %dx%d", forestTilesetWidth, forestTilesetHeight, tileset.width, tileset.height)
	}
	return nil
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
