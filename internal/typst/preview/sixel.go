package preview

import (
	"bytes"
	"image"
	"image/draw"
	"strconv"
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
	if src == nil {
		return nil
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
		return src
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
	rect := image.Rect(0, 0, maxX-minX+1, maxY-minY+1)
	out := image.NewRGBA(rect)
	draw.Draw(out, rect, src, image.Pt(minX, minY), draw.Src)
	return out
}

// scaleArea performs box-averaged downscaling (crisp for anti-aliased text/math)
// with optional soft dark-theme tone inversion.
func scaleArea(src *image.RGBA, dstW int, invert bool) *image.RGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	if dstW <= 0 {
		dstW = sw
	}
	dstH := (sh * dstW) / sw
	if dstH < 1 {
		dstH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	scaleX := float64(sw) / float64(dstW)
	scaleY := float64(sh) / float64(dstH)

	for dy := 0; dy < dstH; dy++ {
		sy0 := float64(dy) * scaleY
		sy1 := float64(dy+1) * scaleY
		iy0 := int(sy0)
		iy1 := int(sy1)
		if iy1 <= iy0 {
			iy1 = iy0 + 1
		}
		if iy1 > sh {
			iy1 = sh
		}
		if iy0 >= sh {
			iy0 = sh - 1
		}

		for dx := 0; dx < dstW; dx++ {
			sx0 := float64(dx) * scaleX
			sx1 := float64(dx+1) * scaleX
			ix0 := int(sx0)
			ix1 := int(sx1)
			if ix1 <= ix0 {
				ix1 = ix0 + 1
			}
			if ix1 > sw {
				ix1 = sw
			}
			if ix0 >= sw {
				ix0 = sw - 1
			}

			var rSum, gSum, bSum, count uint32
			for sy := iy0; sy < iy1; sy++ {
				rowOff := sy * src.Stride
				for sx := ix0; sx < ix1; sx++ {
					off := rowOff + sx*4
					rSum += uint32(src.Pix[off])
					gSum += uint32(src.Pix[off+1])
					bSum += uint32(src.Pix[off+2])
					count++
				}
			}
			if count == 0 {
				count = 1
			}
			r := uint8(rSum / count)
			g := uint8(gSum / count)
			b := uint8(bSum / count)

			if invert {
				// Soft dark-theme paper inversion: white -> #1b1e28, black -> #e6edf3
				r = uint8(27 + (int(255-r)*203)/255)
				g = uint8(30 + (int(255-g)*207)/255)
				b = uint8(40 + (int(255-b)*203)/255)
			}

			doff := dy*dst.Stride + dx*4
			dst.Pix[doff] = r
			dst.Pix[doff+1] = g
			dst.Pix[doff+2] = b
			dst.Pix[doff+3] = 255
		}
	}
	return dst
}
