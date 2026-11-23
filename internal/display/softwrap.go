package display

import (
	runewidth "github.com/mattn/go-runewidth"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/typst/conceal"
	"github.com/monicafaye/TypX/internal/util"
)

// SLoc represents a vertical scrolling location, i.e. a location of a visual line
// in the buffer. When softwrap is enabled, a buffer line may be displayed as
// multiple visual lines (rows). So SLoc stores a number of a line in the buffer
// and a number of a row within this line.
type SLoc struct {
	Line, Row int
}

// LessThan returns true if s is less b
func (s SLoc) LessThan(b SLoc) bool {
	if s.Line < b.Line {
		return true
	}
	return s.Line == b.Line && s.Row < b.Row
}

// GreaterThan returns true if s is bigger than b
func (s SLoc) GreaterThan(b SLoc) bool {
	if s.Line > b.Line {
		return true
	}
	return s.Line == b.Line && s.Row > b.Row
}

// LessEqual returns true if s is less than or equal to b
func (s SLoc) LessEqual(b SLoc) bool {
	if s.Line < b.Line {
		return true
	}
	if s.Line == b.Line && s.Row < b.Row {
		return true
	}
	return s == b
}

// GreaterEqual returns true if s is bigger than or equal to b
func (s SLoc) GreaterEqual(b SLoc) bool {
	if s.Line > b.Line {
		return true
	}
	if s.Line == b.Line && s.Row > b.Row {
		return true
	}
	return s == b
}

// VLoc represents a location in the buffer as a visual location in the
// linewrapped buffer.
type VLoc struct {
	SLoc
	VisualX int
}

type SoftWrap interface {
	Scroll(s SLoc, n int) SLoc
	Diff(s1, s2 SLoc) int
	SLocFromLoc(loc buffer.Loc) SLoc
	VLocFromLoc(loc buffer.Loc) VLoc
	LocFromVLoc(vloc VLoc) buffer.Loc
}

func (w *BufWindow) getVLocFromLocConcealed(lv *conceal.LineView, loc buffer.Loc) VLoc {
	vloc := VLoc{SLoc: SLoc{loc.Y, 0}, VisualX: 0}
	if loc.X <= 0 || w.bufWidth <= 0 {
		return vloc
	}

	wordwrap := w.Buf.Settings["wordwrap"].(bool)
	tabsize := util.IntOpt(w.Buf.Settings["tabsize"])
	targetVisIdx := lv.BufToVisual(loc.X)

	cur := 0
	totalwidth := 0
	wordwidth := 0
	wordoffset := 0

	for cur < len(lv.DisplayText) {
		r, _, nextIdx := decodeDisplayChar(lv.DisplayText, cur)

		width := 0
		switch r {
		case '\t':
			ts := tabsize - (totalwidth % tabsize)
			width = util.Min(ts, w.bufWidth-vloc.VisualX)
			totalwidth += ts
		default:
			width = runewidth.RuneWidth(r)
			totalwidth += width
		}

		wordwidth += width

		if wordwrap {
			if !util.IsWhitespace(r) && nextIdx < len(lv.DisplayText) && wordwidth < w.bufWidth {
				if cur < targetVisIdx {
					wordoffset += width
				}
				cur = nextIdx
				continue
			}
		}

		if vloc.VisualX+wordwidth > w.bufWidth && vloc.VisualX > 0 {
			vloc.Row++
			vloc.VisualX = 0
		}

		if targetVisIdx < nextIdx {
			vloc.VisualX += wordoffset
			return vloc
		}
		cur = nextIdx

		vloc.VisualX += wordwidth

		wordwidth = 0
		wordoffset = 0

		if vloc.VisualX >= w.bufWidth {
			vloc.Row++
			vloc.VisualX = 0
		}
	}
	return vloc
}

func (w *BufWindow) getVLocFromLoc(loc buffer.Loc) VLoc {
	vloc := VLoc{SLoc: SLoc{loc.Y, 0}, VisualX: 0}

	if loc.X <= 0 {
		return vloc
	}

	if w.bufWidth <= 0 {
		return vloc
	}

	if lv := w.lineConcealView(loc.Y); lv != nil {
		return w.getVLocFromLocConcealed(lv, loc)
	}

	wordwrap := w.Buf.Settings["wordwrap"].(bool)
	tabsize := util.IntOpt(w.Buf.Settings["tabsize"])

	line := w.Buf.LineBytes(loc.Y)
	x := 0
	totalwidth := 0

	wordwidth := 0
	wordoffset := 0

	for len(line) > 0 {
		r, _, size := util.DecodeCharacter(line)
		line = line[size:]

		width := 0
		switch r {
		case '\t':
			ts := tabsize - (totalwidth % tabsize)
			width = util.Min(ts, w.bufWidth-vloc.VisualX)
			totalwidth += ts
		default:
			width = runewidth.RuneWidth(r)
			totalwidth += width
		}

		wordwidth += width

		// Collect a complete word to know its width.
		// If wordwrap is off, every single character is a complete "word".
		if wordwrap {
			if !util.IsWhitespace(r) && len(line) > 0 && wordwidth < w.bufWidth {
				if x < loc.X {
					wordoffset += width
					x++
				}
				continue
			}
		}

		// If a word (or just a wide rune) does not fit in the window
		if vloc.VisualX+wordwidth > w.bufWidth && vloc.VisualX > 0 {
			vloc.Row++
			vloc.VisualX = 0
		}

		if x == loc.X {
			vloc.VisualX += wordoffset
			return vloc
		}
		x++

		vloc.VisualX += wordwidth

		wordwidth = 0
		wordoffset = 0

		if vloc.VisualX >= w.bufWidth {
			vloc.Row++
			vloc.VisualX = 0
		}
	}
	return vloc
}

func (w *BufWindow) getLocFromVLocConcealed(lv *conceal.LineView, svloc VLoc) buffer.Loc {
	if w.bufWidth <= 0 {
		return buffer.Loc{X: 0, Y: svloc.Line}
	}

	wordwrap := w.Buf.Settings["wordwrap"].(bool)
	tabsize := util.IntOpt(w.Buf.Settings["tabsize"])

	type wrapChar struct {
		width      int
		visIdx     int
		nextVisIdx int
	}

	var chars []wrapChar
	if wordwrap {
		chars = make([]wrapChar, 0, w.bufWidth)
	} else {
		chars = make([]wrapChar, 0, 1)
	}

	vloc := VLoc{SLoc: SLoc{svloc.Line, 0}, VisualX: 0}
	totalwidth := 0
	wordwidth := 0
	prevVisIdx := -1
	committedNextVisIdx := 0

	for cur := 0; cur < len(lv.DisplayText); {
		r, _, nextIdx := decodeDisplayChar(lv.DisplayText, cur)

		width := 0
		switch r {
		case '\t':
			ts := tabsize - (totalwidth % tabsize)
			width = util.Min(ts, w.bufWidth-vloc.VisualX)
			totalwidth += ts
		default:
			width = runewidth.RuneWidth(r)
			totalwidth += width
		}

		chars = append(chars, wrapChar{width: width, visIdx: cur, nextVisIdx: nextIdx})
		wordwidth += width
		cur = nextIdx

		if wordwrap {
			if !util.IsWhitespace(r) && cur < len(lv.DisplayText) && wordwidth < w.bufWidth {
				continue
			}
		}

		if vloc.VisualX+wordwidth > w.bufWidth && vloc.VisualX > 0 {
			if vloc.Row == svloc.Row {
				if wordwrap && prevVisIdx >= 0 {
					return buffer.Loc{X: lv.VisualToBuf(prevVisIdx), Y: svloc.Line}
				}
				return buffer.Loc{X: lv.VisualToBuf(committedNextVisIdx), Y: svloc.Line}
			}
			vloc.Row++
			vloc.VisualX = 0
		}

		for _, ch := range chars {
			vloc.VisualX += ch.width
			if vloc.Row == svloc.Row && vloc.VisualX > svloc.VisualX {
				return buffer.Loc{X: lv.VisualToBuf(ch.visIdx), Y: svloc.Line}
			}
			prevVisIdx = ch.visIdx
			committedNextVisIdx = ch.nextVisIdx
		}

		chars = chars[:0]
		wordwidth = 0

		if vloc.VisualX >= w.bufWidth {
			vloc.Row++
			vloc.VisualX = 0
		}
	}
	return buffer.Loc{X: lv.VisualToBuf(len(lv.DisplayText)), Y: svloc.Line}
}

func (w *BufWindow) getLocFromVLoc(svloc VLoc) buffer.Loc {
	loc := buffer.Loc{X: 0, Y: svloc.Line}

	if w.bufWidth <= 0 {
		return loc
	}

	if lv := w.lineConcealView(svloc.Line); lv != nil {
		return w.getLocFromVLocConcealed(lv, svloc)
	}

	wordwrap := w.Buf.Settings["wordwrap"].(bool)
	tabsize := util.IntOpt(w.Buf.Settings["tabsize"])

	line := w.Buf.LineBytes(svloc.Line)
	vloc := VLoc{SLoc: SLoc{svloc.Line, 0}, VisualX: 0}

	totalwidth := 0

	var widths []int
	if wordwrap {
		widths = make([]int, 0, w.bufWidth)
	} else {
		widths = make([]int, 0, 1)
	}
	wordwidth := 0

	for len(line) > 0 {
		r, _, size := util.DecodeCharacter(line)
		line = line[size:]

		width := 0
		switch r {
		case '\t':
			ts := tabsize - (totalwidth % tabsize)
			width = util.Min(ts, w.bufWidth-vloc.VisualX)
			totalwidth += ts
		default:
			width = runewidth.RuneWidth(r)
			totalwidth += width
		}

		widths = append(widths, width)
		wordwidth += width

		// Collect a complete word to know its width.
		// If wordwrap is off, every single character is a complete "word".
		if wordwrap {
			if !util.IsWhitespace(r) && len(line) > 0 && wordwidth < w.bufWidth {
				continue
			}
		}

		// If a word (or just a wide rune) does not fit in the window
		if vloc.VisualX+wordwidth > w.bufWidth && vloc.VisualX > 0 {
			if vloc.Row == svloc.Row {
				if wordwrap {
					// it's a word, not a wide rune
					loc.X--
				}
				return loc
			}
			vloc.Row++
			vloc.VisualX = 0
		}

		for i := range widths {
			vloc.VisualX += widths[i]
			if vloc.Row == svloc.Row && vloc.VisualX > svloc.VisualX {
				return loc
			}
			loc.X++
		}

		widths = widths[:0]
		wordwidth = 0

		if vloc.VisualX >= w.bufWidth {
			vloc.Row++
			vloc.VisualX = 0
		}
	}
	return loc
}

func (w *BufWindow) getRowCount(line int) int {
	eol := buffer.Loc{X: util.CharacterCount(w.Buf.LineBytes(line)), Y: line}
	return w.getVLocFromLoc(eol).Row + 1
}

func (w *BufWindow) scrollUp(s SLoc, n int) SLoc {
	for n > 0 {
		if n <= s.Row {
			s.Row -= n
			n = 0
		} else if s.Line > 0 {
			s.Line--
			n -= s.Row + 1
			s.Row = w.getRowCount(s.Line) - 1
		} else {
			s.Row = 0
			break
		}
	}
	return s
}

func (w *BufWindow) scrollDown(s SLoc, n int) SLoc {
	for n > 0 {
		rc := w.getRowCount(s.Line)
		if n < rc-s.Row {
			s.Row += n
			n = 0
		} else if s.Line < w.Buf.LinesNum()-1 {
			s.Line++
			n -= rc - s.Row
			s.Row = 0
		} else {
			s.Row = rc - 1
			break
		}
	}
	return s
}

func (w *BufWindow) scroll(s SLoc, n int) SLoc {
	if n < 0 {
		return w.scrollUp(s, -n)
	}
	return w.scrollDown(s, n)
}

func (w *BufWindow) diff(s1, s2 SLoc) int {
	n := 0
	for s1.LessThan(s2) {
		if s1.Line < s2.Line {
			n += w.getRowCount(s1.Line) - s1.Row
			s1.Line++
			s1.Row = 0
		} else {
			n += s2.Row - s1.Row
			s1.Row = s2.Row
		}
	}
	return n
}

// Scroll returns the location which is n visual lines below the location s
// i.e. the result of scrolling n lines down. n can be negative,
// which means scrolling up. The returned location is guaranteed to be
// within the buffer boundaries.
func (w *BufWindow) Scroll(s SLoc, n int) SLoc {
	if !w.Buf.Settings["softwrap"].(bool) {
		s.Line = util.Clamp(s.Line+n, 0, w.Buf.LinesNum()-1)
		return s
	}
	return w.scroll(s, n)
}

// Diff returns the difference (the vertical distance) between two SLocs.
func (w *BufWindow) Diff(s1, s2 SLoc) int {
	if !w.Buf.Settings["softwrap"].(bool) {
		return s2.Line - s1.Line
	}
	if s1.GreaterThan(s2) {
		return -w.diff(s2, s1)
	}
	return w.diff(s1, s2)
}

// SLocFromLoc takes a position in the buffer and returns the location
// of the visual line containing this position.
func (w *BufWindow) SLocFromLoc(loc buffer.Loc) SLoc {
	if !w.Buf.Settings["softwrap"].(bool) {
		return SLoc{loc.Y, 0}
	}
	return w.getVLocFromLoc(loc).SLoc
}

// VLocFromLoc takes a position in the buffer and returns the corresponding
// visual location in the linewrapped buffer.
func (w *BufWindow) VLocFromLoc(loc buffer.Loc) VLoc {
	if !w.Buf.Settings["softwrap"].(bool) {
		tabsize := util.IntOpt(w.Buf.Settings["tabsize"])
		if lv := w.lineConcealView(loc.Y); lv != nil {
			targetVisIdx := lv.BufToVisual(loc.X)
			visualx := 0
			for i := 0; i < targetVisIdx && i < len(lv.DisplayText); {
				r, _, nextIdx := decodeDisplayChar(lv.DisplayText, i)
				switch r {
				case '\t':
					visualx += tabsize - (visualx % tabsize)
				default:
					visualx += runewidth.RuneWidth(r)
				}
				i = nextIdx
			}
			return VLoc{SLoc{loc.Y, 0}, visualx}
		}

		visualx := util.StringWidth(w.Buf.LineBytes(loc.Y), loc.X, tabsize)
		return VLoc{SLoc{loc.Y, 0}, visualx}
	}
	return w.getVLocFromLoc(loc)
}

// LocFromVLoc takes a visual location in the linewrapped buffer and returns
// the position in the buffer corresponding to this visual location.
func (w *BufWindow) LocFromVLoc(vloc VLoc) buffer.Loc {
	if !w.Buf.Settings["softwrap"].(bool) {
		tabsize := util.IntOpt(w.Buf.Settings["tabsize"])
		if lv := w.lineConcealView(vloc.Line); lv != nil {
			if vloc.VisualX <= 0 {
				return buffer.Loc{X: 0, Y: vloc.Line}
			}
			visIdx := 0
			width := 0
			for visIdx < len(lv.DisplayText) {
				r, _, nextIdx := decodeDisplayChar(lv.DisplayText, visIdx)
				switch r {
				case '\t':
					width += tabsize - (width % tabsize)
				default:
					width += runewidth.RuneWidth(r)
				}
				if width >= vloc.VisualX {
					if width == vloc.VisualX {
						visIdx = nextIdx
					}
					break
				}
				visIdx = nextIdx
			}
			return buffer.Loc{X: lv.VisualToBuf(visIdx), Y: vloc.Line}
		}

		x := util.GetCharPosInLine(w.Buf.LineBytes(vloc.Line), vloc.VisualX, tabsize)
		return buffer.Loc{x, vloc.Line}
	}
	return w.getLocFromVLoc(vloc)
}
