package preview

import (
	"bytes"
	"image"
	"image/draw"
	"strconv"

	xdraw "golang.org/x/image/draw"
)

// Palette layout (248 colors):
//   0..215: 6x6x6 RGB color cube
//   216..247: 32-step neutral grayscale ramp for crisp anti-aliased text & math.
const (
	paletteSize = 248
	grayBase    = 216
	graySteps   = 32
)

var paletteRGB [paletteSize][3]uint8

func init() {
	idx := 0
	for r := 0; r < 6; r++ {
		for g := 0; g < 6; g++ {
			for b := 0; b < 6; b++ {
				paletteRGB[idx] = [3]uint8{
					uint8((r * 255) / 5),
					uint8((g * 255) / 5),
					uint8((b * 255) / 5),
				}
				idx++
			}
		}
	}
	for i := 0; i < graySteps; i++ {
		v := uint8((i * 255) / (graySteps - 1))
		paletteRGB[grayBase+i] = [3]uint8{v, v, v}
	}
}

func quantizePixel(r, g, b uint8) uint8 {
	minC, maxC := r, r
	if g < minC {
		minC = g
	}
	if b < minC {
		minC = b
	}
	if g > maxC {
		maxC = g
	}
	if b > maxC {
		maxC = b
	}
	if maxC-minC <= 14 {
		luma := (77*int(r) + 150*int(g) + 29*int(b)) >> 8
		gi := (luma*(graySteps-1) + 127) / 255
		return uint8(grayBase + gi)
	}
	ri := (int(r)*5 + 127) / 255
	gi := (int(g)*5 + 127) / 255
	bi := (int(b)*5 + 127) / 255
	return uint8(ri*36 + gi*6 + bi)
}

// EncodeSixel encodes an RGBA image into a compact, RLE-compressed Sixel DCS sequence.
func EncodeSixel(img *image.RGBA) []byte {
	if img == nil {
		return nil
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if w <= 0 || h <= 0 {
		return nil
	}

	indices := make([]uint8, w*h)
	var used [paletteSize]bool

	for y := 0; y < h; y++ {
		rowOff := y * img.Stride
		dstRow := y * w
		for x := 0; x < w; x++ {
			off := rowOff + x*4
			idx := quantizePixel(img.Pix[off], img.Pix[off+1], img.Pix[off+2])
			indices[dstRow+x] = idx
			used[idx] = true
		}
	}

	var buf bytes.Buffer
	buf.Grow(w * h / 2)

	// DCS P1=0; P2=1 (transparent bg); P3=0 q  + raster attributes "1;1;W;H
	buf.WriteString("\x1bP0;1;0q\"1;1;")
	buf.WriteString(strconv.Itoa(w))
	buf.WriteByte(';')
	buf.WriteString(strconv.Itoa(h))

	for i := 0; i < paletteSize; i++ {
		if !used[i] {
			continue
		}
		rgb := paletteRGB[i]
		buf.WriteByte('#')
		buf.WriteString(strconv.Itoa(i))
		buf.WriteString(";2;")
		buf.WriteString(strconv.Itoa((int(rgb[0])*100 + 127) / 255))
		buf.WriteByte(';')
		buf.WriteString(strconv.Itoa((int(rgb[1])*100 + 127) / 255))
		buf.WriteByte(';')
		buf.WriteString(strconv.Itoa((int(rgb[2])*100 + 127) / 255))
	}

	rowBits := make([]byte, w)
	var bandColors []uint8
	var inBand [paletteSize]bool

	for y := 0; y < h; y += 6 {
		bandH := 6
		if y+bandH > h {
			bandH = h - y
		}

		bandColors = bandColors[:0]
		for dy := 0; dy < bandH; dy++ {
			row := indices[(y+dy)*w : (y+dy+1)*w]
			for x := 0; x < w; x++ {
				c := row[x]
				if !inBand[c] {
					inBand[c] = true
					bandColors = append(bandColors, c)
				}
			}
		}

		for ci, c := range bandColors {
			if ci > 0 {
				buf.WriteByte('$')
			}
			buf.WriteByte('#')
			buf.WriteString(strconv.Itoa(int(c)))

			lastNonZero := -1
			for x := 0; x < w; x++ {
				var mask byte
				for dy := 0; dy < bandH; dy++ {
					if indices[(y+dy)*w+x] == c {
						mask |= 1 << uint(dy)
					}
				}
				rowBits[x] = mask
				if mask != 0 {
					lastNonZero = x
				}
			}
			if lastNonZero < 0 {
				continue
			}

			for x := 0; x <= lastNonZero; {
				ch := byte(63 + rowBits[x])
				run := 1
				for x+run <= lastNonZero && byte(63+rowBits[x+run]) == ch {
					run++
				}
				if run >= 4 {
					buf.WriteByte('!')
					buf.WriteString(strconv.Itoa(run))
					buf.WriteByte(ch)
				} else {
					for k := 0; k < run; k++ {
						buf.WriteByte(ch)
					}
				}
				x += run
			}
		}

		for _, c := range bandColors {
			inBand[c] = false
		}
		if y+6 < h {
			buf.WriteByte('-')
		}
	}

	buf.WriteString("\x1b\\")
	return buf.Bytes()
}

// cropWhitespace trims near-white outer margins around a rendered page, keeping pad pixels.
func cropWhitespace(src *image.RGBA, pad int) *image.RGBA {
	out, _, _ := cropWhitespaceWithBounds(src, pad)
	return out
}

// cropWhitespaceWithBounds trims near-white outer margins around src and returns
// the cropped image along with its top pixel offset and height in src coordinates.
func cropWhitespaceWithBounds(src *image.RGBA, pad int) (*image.RGBA, int, int) {
	if src == nil {
		return nil, 0, 0
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	minX, minY := w, h
	maxX, maxY := -1, -1
	for y := 0; y < h; y++ {
		rowOff := y * src.Stride
		for x := 0; x < w; x++ {
			off := rowOff + x*4
			if src.Pix[off] < 248 || src.Pix[off+1] < 248 || src.Pix[off+2] < 248 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < minX || maxY < minY {
		return src, 0, h
	}
	minX -= pad
	minY -= pad
	maxX += pad
	maxY += pad
	if minX < 0 {
		minX = 0
	}
	if minY < 0 {
		minY = 0
	}
	if maxX >= w {
		maxX = w - 1
	}
	if maxY >= h {
		maxY = h - 1
	}
	cropH := maxY - minY + 1
	rect := image.Rect(0, 0, maxX-minX+1, cropH)
	out := image.NewRGBA(rect)
	draw.Draw(out, rect, src, image.Pt(minX, minY), draw.Src)
	return out, minY, cropH
}

// scaleArea performs high-quality Catmull-Rom resampling (crisp for both downscaled
// text/math and upscaled thumbnails) with optional soft dark-theme tone inversion.
func scaleArea(src *image.RGBA, dstW int, invert bool) *image.RGBA {
	if src == nil {
		return nil
	}
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	if sw <= 0 || sh <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	if dstW <= 0 {
		dstW = sw
	}
	dstH := (sh * dstW) / sw
	if dstH < 1 {
		dstH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	if dstW == sw && dstH == sh {
		draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	} else {
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	}

	for dy := 0; dy < dstH; dy++ {
		rowOff := dy * dst.Stride
		for dx := 0; dx < dstW; dx++ {
			off := rowOff + dx*4
			if invert {
				// Soft dark-theme paper inversion: white -> #1b1e28, black -> #e6edf3
				dst.Pix[off] = uint8(27 + (int(255-dst.Pix[off])*203)/255)
				dst.Pix[off+1] = uint8(30 + (int(255-dst.Pix[off+1])*207)/255)
				dst.Pix[off+2] = uint8(40 + (int(255-dst.Pix[off+2])*203)/255)
			}
			dst.Pix[off+3] = 255
		}
	}
	return dst
}
