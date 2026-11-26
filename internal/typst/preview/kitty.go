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

// WrapTmuxPassthrough wraps a terminal escape sequence in a tmux DCS
// passthrough envelope (\x1bPtmux;...\x1b\\), doubling every ESC (\x1b) inside
// seq as required by tmux (allow-passthrough on).
func WrapTmuxPassthrough(seq []byte) []byte {
	if len(seq) == 0 {
		return nil
	}
	escCount := bytes.Count(seq, []byte{0x1b})
	out := make([]byte, 0, len(seq)+escCount+9)
	out = append(out, "\x1bPtmux;"...)
	for _, b := range seq {
		if b == 0x1b {
			out = append(out, 0x1b, 0x1b)
		} else {
			out = append(out, b)
		}
	}
	out = append(out, "\x1b\\"...)
	return out
}

func kittyDeleteSequence(tmuxPassthrough bool) string {
	if tmuxPassthrough {
		return string(WrapTmuxPassthrough([]byte(kittyDeleteSeq)))
	}
	return kittyDeleteSeq
}

// EncodeKitty encodes img into a chunked Kitty Graphics Protocol PNG sequence
// using direct placement (a=T, f=100), fixed ID (i=1, p=1), suppressed replies
// (q=2), and no cursor advance (C=1). Returns nil for nil or empty images.
func EncodeKitty(img *image.RGBA) []byte {
	return EncodeKittyWithPassthrough(img, false)
}

// EncodeKittyWithPassthrough encodes img as a chunked Kitty Graphics Protocol
// PNG sequence, optionally wrapping each APC chunk in a tmux DCS passthrough
// envelope so multi-chunk frames stay well below tmux's per-DCS buffer limit.
func EncodeKittyWithPassthrough(img *image.RGBA, tmuxPassthrough bool) []byte {
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
	extraPerChunk := 48
	if tmuxPassthrough {
		extraPerChunk = 64
	}
	out.Grow(b64Len + numChunks*extraPerChunk)

	for off := 0; off < b64Len; off += kittyChunkSize {
		end := off + kittyChunkSize
		more := 1
		if end >= b64Len {
			end = b64Len
			more = 0
		}

		var hdr string
		if off == 0 {
			if more == 1 {
				hdr = "\x1b_Ga=T,f=100,i=1,p=1,q=2,C=1,m=1;"
			} else {
				hdr = "\x1b_Ga=T,f=100,i=1,p=1,q=2,C=1,m=0;"
			}
		} else {
			if more == 1 {
				hdr = "\x1b_Gm=1;"
			} else {
				hdr = "\x1b_Gm=0;"
			}
		}

		if tmuxPassthrough {
			// Escape sequence bytes are only at start (\x1b) and end (\x1b\\);
			// base64 payload never contains ESC.
			out.WriteString("\x1bPtmux;\x1b")
			out.WriteString(hdr)
			out.Write(b64[off:end])
			out.WriteString("\x1b\x1b\\\x1b\\")
		} else {
			out.WriteString(hdr)
			out.Write(b64[off:end])
			out.WriteString("\x1b\\")
		}
	}

	return out.Bytes()
}
