package main

import (
	"bytes"
	"image/png"
	"testing"
)

func testForestTileset(t *testing.T) sprite {
	t.Helper()
	tileset, err := decodeSprite(forestTilesetPNG)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateForestTileset(tileset); err != nil {
		t.Fatal(err)
	}
	return tileset
}

func TestForestTilesetAtlasDimensions(t *testing.T) {
	tileset := testForestTileset(t)
	if tileset.width != 480 || tileset.height != 224 {
		t.Fatalf("tileset = %dx%d, want 480x224", tileset.width, tileset.height)
	}
	if got := tileset.crop(0, forestTreesY, forestTreeWidth, forestTreeHeight); got.width != 48 || got.height != 64 {
		t.Fatalf("tree crop = %dx%d, want 48x64", got.width, got.height)
	}
}

func TestForestSceneStartsWithOpaqueGroundBase(t *testing.T) {
	scene := composeForestScene(testForestTileset(t), 0, forestGroundLayer)
	if scene.width != forestWidth || scene.height != forestHeight {
		t.Fatalf("scene = %dx%d, want %dx%d", scene.width, scene.height, forestWidth, forestHeight)
	}
	for index, pixel := range scene.pixels {
		if pixel.a != 255 {
			t.Fatalf("ground pixel %d alpha = %d, want 255", index, pixel.a)
		}
	}
}

func TestForestLayersAddPixelsOverGround(t *testing.T) {
	tileset := testForestTileset(t)
	ground := composeForestScene(tileset, 0, forestGroundLayer)
	paths := composeForestScene(tileset, 0, forestPathLayer)
	full := composeForestScene(tileset, 0, forestFullLayer)
	if equalSprites(ground, paths) {
		t.Fatal("path layer did not change ground scene")
	}
	if equalSprites(paths, full) {
		t.Fatal("trees, objects, and fire did not change path scene")
	}
}

func TestForestFireAnimationChangesFullSceneOnly(t *testing.T) {
	tileset := testForestTileset(t)
	first := composeForestScene(tileset, 0, forestFullLayer)
	second := composeForestScene(tileset, 1, forestFullLayer)
	if equalSprites(first, second) {
		t.Fatal("successive fire frames produced identical scenes")
	}
	groundFirst := composeForestScene(tileset, 0, forestGroundLayer)
	groundSecond := composeForestScene(tileset, 1, forestGroundLayer)
	if !equalSprites(groundFirst, groundSecond) {
		t.Fatal("fire frame changed ground-only scene")
	}
}

func TestForestPreviewIsDecodablePNG(t *testing.T) {
	var output bytes.Buffer
	if err := writeForestPreview(&output, testForestTileset(t)); err != nil {
		t.Fatal(err)
	}
	preview, err := png.Decode(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Bounds().Dx() != forestWidth || preview.Bounds().Dy() != forestHeight {
		t.Fatalf("preview = %v, want %dx%d", preview.Bounds(), forestWidth, forestHeight)
	}
}

func equalSprites(left, right sprite) bool {
	if left.width != right.width || left.height != right.height || len(left.pixels) != len(right.pixels) {
		return false
	}
	for index := range left.pixels {
		if left.pixels[index] != right.pixels[index] {
			return false
		}
	}
	return true
}
