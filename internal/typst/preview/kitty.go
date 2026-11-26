package preview

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
)

const (
	// Single fixed image and placement ID for TypX's active preview frame.
	// Deleting with d=I frees terminal memory and prevents leaked GPU textures.
	kittyImageID     = 1
	kittyPlacementID = 1

	// Kitty graphics protocol requires base64 chunk sizes to be <= 4096 bytes
	// and a multiple of 4 for all non-final chunks.
	kittyChunkSize = 4096

	// kittyDeleteSeq deletes the active preview placement and frees its stored
	// image data while suppressing all terminal responses (q=2).
	kittyDeleteSeq = "\x1b_Ga=d,d=I,i=1,q=2\x1b\\"
)

var kittyPNGEncoder = png.Encoder{CompressionLevel: png.BestSpeed}

// EncodeKitty encodes img into a chunked Kitty Graphics Protocol PNG sequence
// using direct placement (a=T, f=100), fixed ID (i=1, p=1), suppressed replies
// (q=2), and no cursor advance (C=1). Returns nil for nil or empty images.
func EncodeKitty(img *image.RGBA) []byte {
	if img == nil {
		return nil
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil
	}

	var pngBuf bytes.Buffer
	if err := kittyPNGEncoder.Encode(&pngBuf, img); err != nil {
		return nil
	}

	raw := pngBuf.Bytes()
	b64Len := base64.StdEncoding.EncodedLen(len(raw))
	b64 := make([]byte, b64Len)
	base64.StdEncoding.Encode(b64, raw)

	numChunks := (b64Len + kittyChunkSize - 1) / kittyChunkSize
	if numChunks < 1 {
		numChunks = 1
	}

	var out bytes.Buffer
	out.Grow(b64Len + numChunks*48)

	for off := 0; off < b64Len; off += kittyChunkSize {
		end := off + kittyChunkSize
		more := 1
		if end >= b64Len {
			end = b64Len
			more = 0
		}

		if off == 0 {
			if more == 1 {
				out.WriteString("\x1b_Ga=T,f=100,i=1,p=1,q=2,C=1,m=1;")
			} else {
				out.WriteString("\x1b_Ga=T,f=100,i=1,p=1,q=2,C=1,m=0;")
			}
		} else {
			if more == 1 {
				out.WriteString("\x1b_Gm=1;")
			} else {
				out.WriteString("\x1b_Gm=0;")
			}
		}
		out.Write(b64[off:end])
		out.WriteString("\x1b\\")
	}

	return out.Bytes()
}
