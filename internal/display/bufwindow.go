package display

import (
	"strconv"
	"strings"
	"unicode"

	runewidth "github.com/mattn/go-runewidth"
	"github.com/micro-editor/tcell/v2"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/screen"
	"github.com/monicafaye/TypX/internal/typst/conceal"
	"github.com/monicafaye/TypX/internal/util"
)

// The BufWindow provides a way of displaying a certain section of a buffer.
type BufWindow struct {
	*View

	// Buffer being shown in this window
	Buf *buffer.Buffer

	active bool

	sline *StatusLine

	bufWidth         int
	bufHeight        int
	gutterOffset     int
	hasMessage       bool
	maxLineNumLength int
	drawDivider      bool

	concealCache *conceal.BufferCache
}

// NewBufWindow creates a new window at a location in the screen with a width and height
func NewBufWindow(x, y, width, height int, buf *buffer.Buffer) *BufWindow {
	w := new(BufWindow)
	w.View = new(View)
	w.X, w.Y, w.Width, w.Height = x, y, width, height
	w.concealCache = conceal.NewBufferCache()
	w.SetBuffer(buf)
	w.active = true

	w.sline = NewStatusLine(w)

	return w
}

// SetBuffer sets this window's buffer.
func (w *BufWindow) SetBuffer(b *buffer.Buffer) {
	if w.concealCache == nil {
		w.concealCache = conceal.NewBufferCache()
	}
	if w.Buf != nil && w.Buf != b && w.Buf.SharedBuffer != nil {
		w.concealCache.Invalidate(w.Buf.SharedBuffer)
	}
	w.Buf = b
	b.OptionCallback = func(option string, nativeValue any) {
		if option == "softwrap" {
			if nativeValue.(bool) {
				w.StartCol = 0
			} else {
				w.StartLine.Row = 0
			}
		}

		if option == "softwrap" || option == "wordwrap" || option == "typst.conceal" || option == "typst.concealMode" {
			w.Relocate()
			for _, c := range w.Buf.GetCursors() {
				c.LastWrappedVisualX = c.GetVisualX(true)
			}
		}

		if option == "diffgutter" || option == "ruler" || option == "scrollbar" ||
			option == "statusline" {
			w.updateDisplayInfo()
			w.Relocate()
		}
	}
	b.GetVisualX = func(loc buffer.Loc) int {
		return w.VLocFromLoc(loc).VisualX
	}
}

// GetView gets the view.
func (w *BufWindow) GetView() *View {
	return w.View
}

// GetView sets the view.
func (w *BufWindow) SetView(view *View) {
	w.View = view
}

// Resize resizes this window.
func (w *BufWindow) Resize(width, height int) {
	w.Width, w.Height = width, height
	w.updateDisplayInfo()

	w.Relocate()
}

// SetActive marks the window as active.
func (w *BufWindow) SetActive(b bool) {
	w.active = b
}

// IsActive returns true if this window is active.
func (w *BufWindow) IsActive() bool {
	return w.active
}

// BufView returns the width, height and x,y location of the actual buffer.
// It is not exactly the same as the whole window which also contains gutter,
// ruler, scrollbar and statusline.
func (w *BufWindow) BufView() View {
	return View{
		X:         w.X + w.gutterOffset,
		Y:         w.Y,
		Width:     w.bufWidth,
		Height:    w.bufHeight,
		StartLine: w.StartLine,
		StartCol:  w.StartCol,
	}
}

func (w *BufWindow) updateDisplayInfo() {
	b := w.Buf

	w.drawDivider = false
	if !b.Settings["statusline"].(bool) {
		_, h := screen.Screen.Size()
		infoY := h
		if config.GetGlobalOption("infobar").(bool) {
			infoY--
		}
		if w.Y+w.Height != infoY {
			w.drawDivider = true
		}
	}

	w.bufHeight = w.Height
	if b.Settings["statusline"].(bool) || w.drawDivider {
		w.bufHeight--
	}

	scrollbarWidth := 0
	if w.Buf.Settings["scrollbar"].(bool) && w.Buf.LinesNum() > w.Height && w.Width > 0 {
		scrollbarWidth = 1
	}

	w.hasMessage = len(b.Messages) > 0

	// We need to know the string length of the largest line number
	// so we can pad appropriately when displaying line numbers
	w.maxLineNumLength = len(strconv.Itoa(b.LinesNum()))

	w.gutterOffset = 0
	if w.hasMessage {
		w.gutterOffset += 2
	}
	if b.Settings["diffgutter"].(bool) {
		w.gutterOffset++
	}
	if b.Settings["ruler"].(bool) {
		w.gutterOffset += w.maxLineNumLength + 1
	}

	if w.gutterOffset > w.Width-scrollbarWidth {
		w.gutterOffset = w.Width - scrollbarWidth
	}

	prevBufWidth := w.bufWidth
	w.bufWidth = w.Width - w.gutterOffset - scrollbarWidth

	if w.bufWidth != prevBufWidth && w.Buf.Settings["softwrap"].(bool) {
		for _, c := range w.Buf.GetCursors() {
			c.LastWrappedVisualX = c.GetVisualX(true)
		}
	}
}

func (w *BufWindow) lineConcealView(lineN int) *conceal.LineView {
	if w.Buf == nil || !w.Buf.IsTypst() {
		return nil
	}
	if on, _ := w.Buf.Settings["typst.conceal"].(bool); !on {
		return nil
	}
	if lineN < 0 || lineN >= w.Buf.LinesNum() {
		return nil
	}
	mode, _ := w.Buf.Settings["typst.concealMode"].(string)
	var activeCols []int
	if w.active {
		for _, c := range w.Buf.GetCursors() {
			if c.Y == lineN {
				if mode != "token" {
					return nil
				}
				activeCols = append(activeCols, c.X)
			}
		}
	}
	if w.concealCache == nil {
		w.concealCache = conceal.NewBufferCache()
	}
	return w.concealCache.LineView(w.Buf.SharedBuffer, w.Buf.Version(), w.Buf, lineN, activeCols)
}

func decodeDisplayChar(runes []rune, idx int) (r rune, combc []rune, nextIdx int) {
	r = runes[idx]
	nextIdx = idx + 1
	for nextIdx < len(runes) && unicode.Is(unicode.Mark, runes[nextIdx]) {
		combc = append(combc, runes[nextIdx])
		nextIdx++
	}
	return r, combc, nextIdx
}

func concealGlyphStyle(syntaxStyle tcell.Style) tcell.Style {
	if s, ok := config.Colorscheme["conceal"]; ok {
		return s
	}
	if _, ok := config.Colorscheme["symbol.math"]; ok {
		return config.GetColor("symbol.math")
	}
	if _, ok := config.Colorscheme["symbol"]; ok {
		return config.GetColor("symbol")
	}
	return syntaxStyle
}

func (w *BufWindow) getStartInfoConcealed(lv *conceal.LineView, n, lineN int) (int, int, int, *tcell.Style) {
	tabsize := util.IntOpt(w.Buf.Settings["tabsize"])
	width := 0
	visIdx := 0
	curStyle := config.DefStyle
	var s *tcell.Style
	prevStyleCol := 0
	for visIdx < len(lv.DisplayText) {
		r, _, nextIdx := decodeDisplayChar(lv.DisplayText, visIdx)
		bufCol := lv.VisualToBuf(visIdx)
		for col := prevStyleCol; col <= bufCol; col++ {
			var found bool
			curStyle, found = w.getStyle(curStyle, buffer.Loc{X: col, Y: lineN})
			if found {
				st := curStyle
				s = &st
			}
		}
		prevStyleCol = bufCol + 1

		rw := 0
		switch r {
		case '\t':
			rw = tabsize - (width % tabsize)
		default:
			rw = runewidth.RuneWidth(r)
		}
		if width+rw > n {
			return visIdx, n - width, bufCol, s
		}
		width += rw
		visIdx = nextIdx
	}
	for col := prevStyleCol; col < lv.OriginalLen; col++ {
		var found bool
		curStyle, found = w.getStyle(curStyle, buffer.Loc{X: col, Y: lineN})
		if found {
			st := curStyle
			s = &st
		}
	}
	return len(lv.DisplayText), n - width, lv.OriginalLen, s
}

func (w *BufWindow) getStartInfo(n, lineN int) ([]byte, int, int, *tcell.Style) {
	tabsize := util.IntOpt(w.Buf.Settings["tabsize"])
	width := 0
	bloc := buffer.Loc{0, lineN}
	b := w.Buf.LineBytes(lineN)
	curStyle := config.DefStyle
	var s *tcell.Style
	for len(b) > 0 {
		r, _, size := util.DecodeCharacter(b)

		curStyle, found := w.getStyle(curStyle, bloc)
		if found {
			s = &curStyle
		}

		w := 0
		switch r {
		case '\t':
			ts := tabsize - (width % tabsize)
			w = ts
		default:
			w = runewidth.RuneWidth(r)
		}
		if width+w > n {
			return b, n - width, bloc.X, s
		}
		width += w
		b = b[size:]
		bloc.X++
	}
	return b, n - width, bloc.X, s
}

// Clear resets all cells in this window to the default style
func (w *BufWindow) Clear() {
	for y := 0; y < w.Height; y++ {
		for x := 0; x < w.Width; x++ {
			screen.SetContent(w.X+x, w.Y+y, ' ', nil, config.DefStyle)
		}
	}
}

// Relocate moves the view window so that the cursor is in view
// This is useful if the user has scrolled far away, and then starts typing
// Returns true if the window location is moved
func (w *BufWindow) Relocate() bool {
	b := w.Buf
	height := w.bufHeight
	ret := false
	activeC := w.Buf.GetActiveCursor()
	scrollmargin := int(b.Settings["scrollmargin"].(float64))

	c := w.SLocFromLoc(activeC.Loc)
	bStart := SLoc{0, 0}
	bEnd := w.SLocFromLoc(b.End())

	if c.LessThan(w.Scroll(w.StartLine, scrollmargin)) && c.GreaterThan(w.Scroll(bStart, scrollmargin-1)) {
		w.StartLine = w.Scroll(c, -scrollmargin)
		ret = true
	} else if c.LessThan(w.StartLine) {
		w.StartLine = c
		ret = true
	}
	if c.GreaterThan(w.Scroll(w.StartLine, height-1-scrollmargin)) && c.LessEqual(w.Scroll(bEnd, -scrollmargin)) {
		w.StartLine = w.Scroll(c, -height+1+scrollmargin)
		ret = true
	} else if c.GreaterThan(w.Scroll(bEnd, -scrollmargin)) && c.GreaterThan(w.Scroll(w.StartLine, height-1)) {
		w.StartLine = w.Scroll(bEnd, -height+1)
		ret = true
	}

	// horizontal relocation (scrolling)
	if !b.Settings["softwrap"].(bool) {
		cx := w.VLocFromLoc(activeC.Loc).VisualX
		rw := runewidth.RuneWidth(activeC.RuneUnder(activeC.X))
		if rw == 0 {
			rw = 1 // tab or newline
		}

		if cx < w.StartCol {
			w.StartCol = cx
			ret = true
		}
		if cx+rw > w.StartCol+w.bufWidth {
			w.StartCol = cx - w.bufWidth + rw
			ret = true
		}
	}
	return ret
}

// LocFromVisual takes a visual location (x and y position) and returns the
// position in the buffer corresponding to the visual location
// If the requested position does not correspond to a buffer location it returns
// the nearest position
func (w *BufWindow) LocFromVisual(svloc buffer.Loc) buffer.Loc {
	vx := svloc.X - w.X - w.gutterOffset
	if vx < 0 {
		vx = 0
	}
	vloc := VLoc{
		SLoc:    w.Scroll(w.StartLine, svloc.Y-w.Y),
		VisualX: vx + w.StartCol,
	}
	return w.LocFromVLoc(vloc)
}

func (w *BufWindow) drawGutter(vloc *buffer.Loc, bloc *buffer.Loc) {
	char := ' '
	s := config.DefStyle
	for _, m := range w.Buf.Messages {
		if m.Start.Y == bloc.Y || m.End.Y == bloc.Y {
			s = m.Style()
			char = '>'
			break
		}
	}
	for i := 0; i < 2 && vloc.X < w.gutterOffset; i++ {
		screen.SetContent(w.X+vloc.X, w.Y+vloc.Y, char, nil, s)
		vloc.X++
	}
}

func (w *BufWindow) drawDiffGutter(backgroundStyle tcell.Style, softwrapped bool, vloc *buffer.Loc, bloc *buffer.Loc) {
	if vloc.X >= w.gutterOffset {
		return
	}

	symbol := ' '
	styleName := ""

	switch w.Buf.DiffStatus(bloc.Y) {
	case buffer.DSAdded:
		symbol = '\u258C' // Left half block
		styleName = "diff-added"
	case buffer.DSModified:
		symbol = '\u258C' // Left half block
		styleName = "diff-modified"
	case buffer.DSDeletedAbove:
		if !softwrapped {
			symbol = '\u2594' // Upper one eighth block
			styleName = "diff-deleted"
		}
	}

	style := backgroundStyle
	if s, ok := config.Colorscheme[styleName]; ok {
		foreground, _, _ := s.Decompose()
		style = style.Foreground(foreground)
	}

	screen.SetContent(w.X+vloc.X, w.Y+vloc.Y, symbol, nil, style)
	vloc.X++
}

func (w *BufWindow) drawLineNum(lineNumStyle tcell.Style, softwrapped bool, vloc *buffer.Loc, bloc *buffer.Loc) {
	cursorLine := w.Buf.GetActiveCursor().Loc.Y
	var lineInt int
	if w.Buf.Settings["relativeruler"] == false || cursorLine == bloc.Y {
		lineInt = bloc.Y + 1
	} else {
		lineInt = bloc.Y - cursorLine
	}
	lineNum := []rune(strconv.Itoa(util.Abs(lineInt)))

	// Write the spaces before the line number if necessary
	for i := 0; i < w.maxLineNumLength-len(lineNum) && vloc.X < w.gutterOffset; i++ {
		screen.SetContent(w.X+vloc.X, w.Y+vloc.Y, ' ', nil, lineNumStyle)
		vloc.X++
	}
	// Write the actual line number
	for i := 0; i < len(lineNum) && vloc.X < w.gutterOffset; i++ {
		if softwrapped || (w.bufWidth == 0 && w.Buf.Settings["softwrap"] == true) {
			screen.SetContent(w.X+vloc.X, w.Y+vloc.Y, ' ', nil, lineNumStyle)
		} else {
			screen.SetContent(w.X+vloc.X, w.Y+vloc.Y, lineNum[i], nil, lineNumStyle)
		}
		vloc.X++
	}

	// Write the extra space
	if vloc.X < w.gutterOffset {
		screen.SetContent(w.X+vloc.X, w.Y+vloc.Y, ' ', nil, lineNumStyle)
		vloc.X++
	}
}

// getStyle returns the highlight style for the given character position
// If there is no change to the current highlight style it just returns that
func (w *BufWindow) getStyle(style tcell.Style, bloc buffer.Loc) (tcell.Style, bool) {
	if group, ok := w.Buf.Match(bloc.Y)[bloc.X]; ok {
		s := config.GetColor(group.String())
		return s, true
	}
	return style, false
}

func (w *BufWindow) showCursor(x, y int, main bool) {
	if w.active {
		if main {
			screen.ShowCursor(x, y)
		} else {
			screen.ShowFakeCursorMulti(x, y)
		}
	}
}

// displayBuffer draws the buffer being shown in this window on the screen.Screen
func (w *BufWindow) displayBuffer() {
	b := w.Buf

	if w.Height <= 0 || w.Width <= 0 {
		return
	}

	maxWidth := w.gutterOffset + w.bufWidth

	if b.ModifiedThisFrame {
		if b.Settings["diffgutter"].(bool) {
			b.UpdateDiff()
		}
		b.ModifiedThisFrame = false
	}

	var matchingBraces []buffer.Loc
	// bracePairs is defined in buffer.go
	if b.Settings["matchbrace"].(bool) {
		for _, c := range b.GetCursors() {
			if c.HasSelection() {
				continue
			}

			mb, left, found := b.FindMatchingBrace(c.Loc)
			if found {
				matchingBraces = append(matchingBraces, mb)
				if !left {
					if b.Settings["matchbracestyle"].(string) != "highlight" {
						matchingBraces = append(matchingBraces, c.Loc)
					}
				} else {
					matchingBraces = append(matchingBraces, c.Loc.Move(-1, b))
				}
			}
		}
	}

	lineNumStyle := config.DefStyle
	if style, ok := config.Colorscheme["line-number"]; ok {
		lineNumStyle = style
	}
	curNumStyle := config.DefStyle
	if style, ok := config.Colorscheme["current-line-number"]; ok {
		if !b.Settings["cursorline"].(bool) {
			curNumStyle = lineNumStyle
		} else {
			curNumStyle = style
		}
	}

	softwrap := b.Settings["softwrap"].(bool)
	wordwrap := softwrap && b.Settings["wordwrap"].(bool)

	tabsize := util.IntOpt(b.Settings["tabsize"])
	colorcolumn := util.IntOpt(b.Settings["colorcolumn"])

	// this represents the current draw position
	// within the current window
	vloc := buffer.Loc{X: 0, Y: 0}
	if softwrap {
		// the start line may be partially out of the current window
		vloc.Y = -w.StartLine.Row
	}

	// this represents the current draw position in the buffer (char positions)
	bloc := buffer.Loc{X: -1, Y: w.StartLine.Line}

	cursors := b.GetCursors()

	curStyle := config.DefStyle

	// Parse showchars which is in the format of key1=val1,key2=val2,...
	spacechars := " "
	tabchars := b.Settings["indentchar"].(string)
	var indentspacechars string
	var indenttabchars string
	for _, entry := range strings.Split(b.Settings["showchars"].(string), ",") {
		split := strings.SplitN(entry, "=", 2)
		if len(split) < 2 {
			continue
		}
		key, val := split[0], split[1]
		switch key {
		case "space":
			spacechars = val
		case "tab":
			tabchars = val
		case "ispace":
			indentspacechars = val
		case "itab":
			indenttabchars = val
		}
	}

	for ; vloc.Y < w.bufHeight; vloc.Y++ {
		vloc.X = 0

		currentLine := false
		for _, c := range cursors {
			if !c.HasSelection() && bloc.Y == c.Y && w.active {
				currentLine = true
				break
			}
		}

		s := lineNumStyle
		if currentLine {
			s = curNumStyle
		}

		if vloc.Y >= 0 {
			if w.hasMessage {
				w.drawGutter(&vloc, &bloc)
			}

			if b.Settings["diffgutter"].(bool) {
				w.drawDiffGutter(s, false, &vloc, &bloc)
			}

			if b.Settings["ruler"].(bool) {
				w.drawLineNum(s, false, &vloc, &bloc)
			}
		} else {
			vloc.X = w.gutterOffset
		}

		bline := b.LineBytes(bloc.Y)
		blineLen := util.CharacterCount(bline)

		leadingwsEnd := len(util.GetLeadingWhitespace(bline))
		trailingwsStart := blineLen - util.CharacterCount(util.GetTrailingWhitespace(bline))

		lv := w.lineConcealView(bloc.Y)
		var line []byte
		var nColsBeforeStart, bslice int
		var startStyle *tcell.Style
		visIdx := 0
		if lv != nil {
			visIdx, nColsBeforeStart, bslice, startStyle = w.getStartInfoConcealed(lv, w.StartCol, bloc.Y)
		} else {
			line, nColsBeforeStart, bslice, startStyle = w.getStartInfo(w.StartCol, bloc.Y)
		}
		if startStyle != nil {
			curStyle = *startStyle
		}
		bloc.X = bslice
		prevStyleCol := bslice
		var activeSpan *conceal.Span

		// returns the rune to be drawn, style of it and if the bg should be preserved
		getRuneStyle := func(r rune, style tcell.Style, showoffset int, linex int, isplaceholder bool) (rune, tcell.Style, bool) {
			if nColsBeforeStart > 0 || vloc.Y < 0 || isplaceholder {
				return r, style, false
			}

			for _, mb := range matchingBraces {
				if mb.X == bloc.X && mb.Y == bloc.Y {
					if b.Settings["matchbracestyle"].(string) == "highlight" {
						if s, ok := config.Colorscheme["match-brace"]; ok {
							return r, s, false
						} else {
							return r, style.Reverse(true), false
						}
					} else {
						return r, style.Underline(true), false
					}
				}
			}

			if r != '\t' && r != ' ' {
				return r, style, false
			}

			var indentrunes []rune
			switch r {
			case '\t':
				if bloc.X < leadingwsEnd && indenttabchars != "" {
					indentrunes = []rune(indenttabchars)
				} else {
					indentrunes = []rune(tabchars)
				}
			case ' ':
				if linex%tabsize == 0 && bloc.X < leadingwsEnd && indentspacechars != "" {
					indentrunes = []rune(indentspacechars)
				} else {
					indentrunes = []rune(spacechars)
				}
			}

			var drawrune rune
			if showoffset < len(indentrunes) {
				drawrune = indentrunes[showoffset]
			} else {
				// use space if no showchars or after we showed showchars
				drawrune = ' '
			}

			if s, ok := config.Colorscheme["indent-char"]; ok {
				fg, _, _ := s.Decompose()
				style = style.Foreground(fg)
			}

			preservebg := false
			if b.Settings["hltaberrors"].(bool) && bloc.X < leadingwsEnd {
				if s, ok := config.Colorscheme["tab-error"]; ok {
					if b.Settings["tabstospaces"].(bool) && r == '\t' {
						fg, _, _ := s.Decompose()
						style = style.Background(fg)
						preservebg = true
					} else if !b.Settings["tabstospaces"].(bool) && r == ' ' {
						fg, _, _ := s.Decompose()
						style = style.Background(fg)
						preservebg = true
					}
				}
			}

			if b.Settings["hltrailingws"].(bool) {
				if s, ok := config.Colorscheme["trailingws"]; ok {
					if bloc.X >= trailingwsStart && bloc.X < blineLen {
						hl := true
						for _, c := range cursors {
							if c.NewTrailingWsY == bloc.Y {
								hl = false
								break
							}
						}
						if hl {
							fg, _, _ := s.Decompose()
							style = style.Background(fg)
							preservebg = true
						}
					}
				}
			}

			return drawrune, style, preservebg
		}

		draw := func(r rune, combc []rune, style tcell.Style, highlight bool, showcursor bool, preservebg bool) {
			defer func() {
				if nColsBeforeStart <= 0 {
					vloc.X++
				}
				nColsBeforeStart--
			}()

			if nColsBeforeStart > 0 || vloc.Y < 0 {
				return
			}

			if highlight {
				matchedSearch := false
				if w.Buf.HighlightSearch {
					if activeSpan != nil {
						for col := activeSpan.BufStart; col < activeSpan.BufEnd; col++ {
							if w.Buf.SearchMatch(buffer.Loc{X: col, Y: bloc.Y}) {
								matchedSearch = true
								break
							}
						}
					} else {
						matchedSearch = w.Buf.SearchMatch(bloc)
					}
				}
				if matchedSearch {
					style = config.DefStyle.Reverse(true)
					if s, ok := config.Colorscheme["hlsearch"]; ok {
						style = s
					}
				} else if matchSymbolHighlight(w.Buf.SymbolHighlights, bloc, activeSpan) {
					style = config.DefStyle.Reverse(true)
					if s, ok := config.Colorscheme["symbol-highlight"]; ok {
						style = s
					} else if s, ok := config.Colorscheme["hlsearch"]; ok {
						style = s
					}
				}

				_, origBg, _ := style.Decompose()
				_, defBg, _ := config.DefStyle.Decompose()

				// syntax or hlsearch highlighting with non-default background takes precedence
				// over cursor-line and color-column
				if !preservebg && origBg != defBg {
					preservebg = true
				}

				for _, c := range cursors {
					selected := false
					if c.HasSelection() {
						if activeSpan != nil {
							selStart, selEnd := c.CurSelection[0], c.CurSelection[1]
							if selEnd.LessThan(selStart) {
								selStart, selEnd = selEnd, selStart
							}
							spanStart := buffer.Loc{X: activeSpan.BufStart, Y: bloc.Y}
							spanEnd := buffer.Loc{X: activeSpan.BufEnd, Y: bloc.Y}
							selected = selStart.LessThan(spanEnd) && spanStart.LessThan(selEnd)
						} else {
							selected = bloc.GreaterEqual(c.CurSelection[0]) && bloc.LessThan(c.CurSelection[1]) ||
								bloc.LessThan(c.CurSelection[0]) && bloc.GreaterEqual(c.CurSelection[1])
						}
					}
					if selected {
						// The current character is selected
						style = config.DefStyle.Reverse(true)

						if s, ok := config.Colorscheme["selection"]; ok {
							style = s
						}
					}

					if b.Settings["cursorline"].(bool) && w.active && !preservebg &&
						!c.HasSelection() && c.Y == bloc.Y {
						if s, ok := config.Colorscheme["cursor-line"]; ok {
							fg, _, _ := s.Decompose()
							style = style.Background(fg)
						}
					}
				}

				for _, m := range b.Messages {
					if activeSpan != nil {
						msgStart, msgEnd := m.Start, m.End
						if msgEnd.LessThan(msgStart) {
							msgStart, msgEnd = msgEnd, msgStart
						}
						spanStart := buffer.Loc{X: activeSpan.BufStart, Y: bloc.Y}
						spanEnd := buffer.Loc{X: activeSpan.BufEnd, Y: bloc.Y}
						if msgStart.LessThan(spanEnd) && spanStart.LessThan(msgEnd) {
							style = style.Underline(true)
							break
						}
					} else if bloc.GreaterEqual(m.Start) && bloc.LessThan(m.End) ||
						bloc.LessThan(m.End) && bloc.GreaterEqual(m.Start) {
						style = style.Underline(true)
						break
					}
				}

				if s, ok := config.Colorscheme["color-column"]; ok {
					if colorcolumn != 0 && vloc.X-w.gutterOffset+w.StartCol == colorcolumn && !preservebg {
						fg, _, _ := s.Decompose()
						style = style.Background(fg)
					}
				}
			}

			screen.SetContent(w.X+vloc.X, w.Y+vloc.Y, r, combc, style)

			if showcursor {
				for _, c := range cursors {
					if !c.HasSelection() && c.Y == bloc.Y {
						onCursor := c.X == bloc.X
						if activeSpan != nil {
							onCursor = c.X >= activeSpan.BufStart && c.X < activeSpan.BufEnd
						}
						if onCursor {
							w.showCursor(w.X+vloc.X, w.Y+vloc.Y, c.Num == 0)
						}
					}
				}
			}
		}

		wrap := func() {
			vloc.X = 0

			if vloc.Y >= 0 {
				if w.hasMessage {
					w.drawGutter(&vloc, &bloc)
				}
				if b.Settings["diffgutter"].(bool) {
					w.drawDiffGutter(lineNumStyle, true, &vloc, &bloc)
				}

				// This will draw an empty line number because the current line is wrapped
				if b.Settings["ruler"].(bool) {
					w.drawLineNum(lineNumStyle, true, &vloc, &bloc)
				}
			} else {
				vloc.X = w.gutterOffset
			}
		}

		type glyph struct {
			r         rune
			combc     []rune
			style     tcell.Style
			width     int
			linex     int
			bufCol    int
			bufEnd    int
			concealed bool
			span      conceal.Span
		}

		var word []glyph
		if wordwrap {
			word = make([]glyph, 0, w.bufWidth)
		} else {
			word = make([]glyph, 0, 1)
		}
		wordwidth := 0

		hasMore := func() bool {
			if lv != nil {
				return visIdx < len(lv.DisplayText)
			}
			return len(line) > 0
		}

		totalwidth := w.StartCol - nColsBeforeStart
		for hasMore() && vloc.X < maxWidth {
			var r rune
			var combc []rune
			var gStyle tcell.Style
			var gBufCol, gBufEnd int
			var gConcealed bool
			var gSpan conceal.Span

			if lv != nil {
				curVisIdx := visIdx
				r, combc, visIdx = decodeDisplayChar(lv.DisplayText, curVisIdx)
				gBufCol = lv.VisualToBuf(curVisIdx)
				for col := prevStyleCol; col <= gBufCol; col++ {
					curStyle, _ = w.getStyle(curStyle, buffer.Loc{X: col, Y: bloc.Y})
				}
				prevStyleCol = gBufCol + 1

				gStyle = curStyle
				if sp, ok := lv.SpanAtBufCol(gBufCol); ok {
					gConcealed = true
					gSpan = sp
					gStyle = concealGlyphStyle(curStyle)
					gBufEnd = sp.BufEnd
				} else {
					gBufEnd = gBufCol + 1
				}
			} else {
				var size int
				r, combc, size = util.DecodeCharacter(line)
				line = line[size:]

				gBufCol = bloc.X + len(word)
				gBufEnd = gBufCol + 1
				curStyle, _ = w.getStyle(curStyle, buffer.Loc{X: gBufCol, Y: bloc.Y})
				gStyle = curStyle
			}

			width := 0

			linex := totalwidth
			switch r {
			case '\t':
				ts := tabsize - (totalwidth % tabsize)
				width = util.Min(ts, maxWidth-vloc.X)
				totalwidth += ts
			default:
				width = runewidth.RuneWidth(r)
				totalwidth += width
			}

			word = append(word, glyph{r, combc, gStyle, width, linex, gBufCol, gBufEnd, gConcealed, gSpan})
			wordwidth += width

			// Collect a complete word to know its width.
			// If wordwrap is off, every single character is a complete "word".
			if wordwrap {
				if !util.IsWhitespace(r) && hasMore() && wordwidth < w.bufWidth {
					continue
				}
			}

			// If a word (or just a wide rune) does not fit in the window
			if vloc.X+wordwidth > maxWidth && vloc.X > w.gutterOffset {
				activeSpan = nil
				for vloc.X < maxWidth {
					draw(' ', nil, config.DefStyle, false, false, true)
				}

				// We either stop or we wrap to draw the word in the next line
				if !softwrap {
					break
				} else {
					vloc.Y++
					if vloc.Y >= w.bufHeight {
						break
					}
					wrap()
				}
			}

			for _, g := range word {
				bloc.X = g.bufCol
				if g.concealed {
					sp := g.span
					activeSpan = &sp
				} else {
					activeSpan = nil
				}
				drawrune, drawstyle, preservebg := getRuneStyle(g.r, g.style, 0, g.linex, false)
				draw(drawrune, g.combc, drawstyle, true, true, preservebg)

				// Draw extra characters for tabs or wide runes
				for i := 1; i < g.width; i++ {
					if g.r == '\t' {
						drawrune, drawstyle, preservebg = getRuneStyle('\t', g.style, i, g.linex+i, false)
					} else {
						drawrune, drawstyle, preservebg = getRuneStyle(' ', g.style, i, g.linex+i, true)
					}
					draw(drawrune, nil, drawstyle, true, false, preservebg)
				}
				bloc.X = g.bufEnd
			}

			word = word[:0]
			wordwidth = 0

			// If we reach the end of the window then we either stop or we wrap for softwrap
			if vloc.X >= maxWidth {
				if !softwrap {
					break
				} else {
					vloc.Y++
					if vloc.Y >= w.bufHeight {
						break
					}
					wrap()
				}
			}
		}

		style := config.DefStyle
		for _, c := range cursors {
			if b.Settings["cursorline"].(bool) && w.active &&
				!c.HasSelection() && c.Y == bloc.Y {
				if s, ok := config.Colorscheme["cursor-line"]; ok {
					fg, _, _ := s.Decompose()
					style = style.Background(fg)
				}
			}
		}
		for i := vloc.X; i < maxWidth; i++ {
			curStyle := style
			if s, ok := config.Colorscheme["color-column"]; ok {
				if colorcolumn != 0 && i-w.gutterOffset+w.StartCol == colorcolumn {
					fg, _, _ := s.Decompose()
					curStyle = style.Background(fg)
				}
			}
			screen.SetContent(i+w.X, vloc.Y+w.Y, ' ', nil, curStyle)
		}

		if vloc.X != maxWidth {
			if lv != nil {
				bloc.X = lv.VisualToBuf(len(lv.DisplayText))
			}
			activeSpan = nil
			// Display newline within a selection
			drawrune, drawstyle, preservebg := getRuneStyle(' ', config.DefStyle, 0, totalwidth, true)
			draw(drawrune, nil, drawstyle, true, true, preservebg)
		}

		bloc.X = w.StartCol
		bloc.Y++
		if bloc.Y >= b.LinesNum() {
			break
		}
	}
}

func (w *BufWindow) displayStatusLine() {
	if w.Buf.Settings["statusline"].(bool) {
		w.sline.Display()
	} else if w.drawDivider {
		divchars := config.GetGlobalOption("divchars").(string)
		if util.CharacterCountInString(divchars) != 2 {
			divchars = "|-"
		}

		_, _, size := util.DecodeCharacterInString(divchars)
		divchar, combc, _ := util.DecodeCharacterInString(divchars[size:])

		dividerStyle := config.DefStyle
		if style, ok := config.Colorscheme["divider"]; ok {
			dividerStyle = style
		}

		divreverse := config.GetGlobalOption("divreverse").(bool)
		if divreverse {
			dividerStyle = dividerStyle.Reverse(true)
		}

		for x := w.X; x < w.X+w.Width; x++ {
			screen.SetContent(x, w.Y+w.Height-1, divchar, combc, dividerStyle)
		}
	}
}

func (w *BufWindow) displayScrollBar() {
	if w.Buf.Settings["scrollbar"].(bool) && w.Buf.LinesNum() > w.Height {
		scrollX := w.X + w.Width - 1
		barsize := int(float64(w.Height) / float64(w.Buf.LinesNum()) * float64(w.Height))
		if barsize < 1 {
			barsize = 1
		}
		barstart := w.Y + int(float64(w.StartLine.Line)/float64(w.Buf.LinesNum())*float64(w.Height))

		scrollBarStyle := config.DefStyle.Reverse(true)
		if style, ok := config.Colorscheme["scrollbar"]; ok {
			scrollBarStyle = style
		}

		scrollBarChar := config.GetGlobalOption("scrollbarchar").(string)
		if util.CharacterCountInString(scrollBarChar) != 1 {
			scrollBarChar = "|"
		}
		scrollBarRune := []rune(scrollBarChar)

		for y := barstart; y < util.Min(barstart+barsize, w.Y+w.bufHeight); y++ {
			screen.SetContent(scrollX, y, scrollBarRune[0], nil, scrollBarStyle)
		}
	}
}

// Display displays the buffer and the statusline
func (w *BufWindow) Display() {
	w.updateDisplayInfo()

	w.displayStatusLine()
	w.displayScrollBar()
	w.displayBuffer()
}

func matchSymbolHighlight(ranges []buffer.HighlightRange, bloc buffer.Loc, activeSpan *conceal.Span) bool {
	for _, hr := range ranges {
		start, end := hr.Start, hr.End
		if end.LessThan(start) {
			start, end = end, start
		}
		if !start.LessThan(end) {
			continue
		}
		if activeSpan != nil {
			spanStart := buffer.Loc{X: activeSpan.BufStart, Y: bloc.Y}
			spanEnd := buffer.Loc{X: activeSpan.BufEnd, Y: bloc.Y}
			if start.LessThan(spanEnd) && spanStart.LessThan(end) {
				return true
			}
		} else if bloc.GreaterEqual(start) && bloc.LessThan(end) {
			return true
		}
	}
	return false
}
