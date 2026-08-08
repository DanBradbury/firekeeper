package main

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
