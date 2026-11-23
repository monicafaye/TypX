package shortcuts

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/util"
)

// NewMathInlineAction returns the BufKeyAction for toggling/inserting inline
// math ($...$) or stepping out of an active math span.
func NewMathInlineAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		if !canEditTypst(bp) {
			return false
		}
		forEachCursorReverse(bp, func(c *buffer.Cursor) {
			mathInlineAtCursor(bp.Buf, c)
		})
		return true
	}
}

// NewMathInlineCmd returns the command handler for :math-inline.
func NewMathInlineCmd() func(bp *action.BufPane, args []string) {
	return func(bp *action.BufPane, args []string) {
		if !guardTypstCmd(bp) {
			return
		}
		forEachCursorReverse(bp, func(c *buffer.Cursor) {
			mathInlineAtCursor(bp.Buf, c)
		})
	}
}

// NewMathDisplayAction returns the BufKeyAction for inserting or toggling
// display math ($ ... $ or multi-line $\n  ...\n$).
func NewMathDisplayAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		if !canEditTypst(bp) {
			return false
		}
		forEachCursorReverse(bp, func(c *buffer.Cursor) {
			mathDisplayAtCursor(bp.Buf, c)
		})
		return true
	}
}

// NewMathDisplayCmd returns the command handler for :math-display.
func NewMathDisplayCmd() func(bp *action.BufPane, args []string) {
	return func(bp *action.BufPane, args []string) {
		if !guardTypstCmd(bp) {
			return
		}
		forEachCursorReverse(bp, func(c *buffer.Cursor) {
			mathDisplayAtCursor(bp.Buf, c)
		})
	}
}

// NewWrapBoldAction returns the BufKeyAction for wrapping/unwrapping *bold*.
func NewWrapBoldAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		if !canEditTypst(bp) {
			return false
		}
		forEachCursorReverse(bp, func(c *buffer.Cursor) {
			wrapDelimiterAtCursor(bp.Buf, c, '*')
		})
		return true
	}
}

// NewWrapBoldCmd returns the command handler for :wrap-bold.
func NewWrapBoldCmd() func(bp *action.BufPane, args []string) {
	return func(bp *action.BufPane, args []string) {
		if !guardTypstCmd(bp) {
			return
		}
		forEachCursorReverse(bp, func(c *buffer.Cursor) {
			wrapDelimiterAtCursor(bp.Buf, c, '*')
		})
	}
}

// NewWrapItalicAction returns the BufKeyAction for wrapping/unwrapping _italic_.
func NewWrapItalicAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		if !canEditTypst(bp) {
			return false
		}
		forEachCursorReverse(bp, func(c *buffer.Cursor) {
			wrapDelimiterAtCursor(bp.Buf, c, '_')
		})
		return true
	}
}

// NewWrapItalicCmd returns the command handler for :wrap-italic.
func NewWrapItalicCmd() func(bp *action.BufPane, args []string) {
	return func(bp *action.BufPane, args []string) {
		if !guardTypstCmd(bp) {
			return
		}
		forEachCursorReverse(bp, func(c *buffer.Cursor) {
			wrapDelimiterAtCursor(bp.Buf, c, '_')
		})
	}
}

// NewHeadingPromoteAction returns the BufKeyAction for promoting headings
// toward top level (== X -> = X, clamped at 1 =). Returns false if no cursor
// is on a Typst heading line so "|" fallback bindings continue to work.
func NewHeadingPromoteAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		if !canEditTypst(bp) {
			return false
		}
		return adjustHeadings(bp, -1)
	}
}

// NewHeadingPromoteCmd returns the command handler for :heading-promote.
func NewHeadingPromoteCmd() func(bp *action.BufPane, args []string) {
	return func(bp *action.BufPane, args []string) {
		if !guardTypstCmd(bp) {
			return
		}
		adjustHeadings(bp, -1)
	}
}

// NewHeadingDemoteAction returns the BufKeyAction for demoting headings deeper
// (= X -> == X, clamped at 6 =). Returns false if no cursor is on a Typst
// heading line so "|" fallback bindings continue to work.
func NewHeadingDemoteAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		if !canEditTypst(bp) {
			return false
		}
		return adjustHeadings(bp, +1)
	}
}

// NewHeadingDemoteCmd returns the command handler for :heading-demote.
func NewHeadingDemoteCmd() func(bp *action.BufPane, args []string) {
	return func(bp *action.BufPane, args []string) {
		if !guardTypstCmd(bp) {
			return
		}
		adjustHeadings(bp, +1)
	}
}

func canEditTypst(bp *action.BufPane) bool {
	return bp != nil && bp.Buf != nil && bp.Buf.IsTypst() && !bp.Buf.Type.Readonly
}

func guardTypstCmd(bp *action.BufPane) bool {
	if bp == nil || bp.Buf == nil {
		return false
	}
	if bp.Buf.Type.Readonly {
		if action.InfoBar != nil {
			action.InfoBar.Error("Buffer is read-only")
		}
		return false
	}
	if !bp.Buf.IsTypst() {
		if action.InfoBar != nil {
			action.InfoBar.Error("Command requires a Typst buffer")
		}
		return false
	}
	return true
}

func activeCursor(bp *action.BufPane) *buffer.Cursor {
	if bp.Cursor != nil {
		return bp.Cursor
	}
	return bp.Buf.GetActiveCursor()
}

func orderedSelection(c *buffer.Cursor) (buffer.Loc, buffer.Loc) {
	a, b := c.CurSelection[0], c.CurSelection[1]
	if a.GreaterThan(b) {
		return b, a
	}
	return a, b
}

func forEachCursorReverse(bp *action.BufPane, fn func(c *buffer.Cursor)) {
	cursors := append([]*buffer.Cursor(nil), bp.Buf.GetCursors()...)
	if len(cursors) == 0 {
		return
	}
	sort.SliceStable(cursors, func(i, j int) bool {
		li := cursorSortLoc(cursors[i])
		lj := cursorSortLoc(cursors[j])
		if li.Y != lj.Y {
			return li.Y > lj.Y
		}
		return li.X > lj.X
	})
	for _, c := range cursors {
		bp.Buf.SetCurCursor(c.Num)
		bp.Cursor = c
		fn(c)
	}
	bp.Buf.SetCurCursor(0)
	bp.Cursor = bp.Buf.GetActiveCursor()
	bp.Buf.MergeCursors()
	if bp.BWindow != nil {
		bp.Relocate()
	}
}

func cursorSortLoc(c *buffer.Cursor) buffer.Loc {
	if c.HasSelection() {
		start, _ := orderedSelection(c)
		return start
	}
	return c.Loc
}

func safeBoolSetting(b *buffer.Buffer, key string, def bool) bool {
	if b == nil || b.Settings == nil {
		return def
	}
	v, ok := b.Settings[key]
	if !ok {
		return def
	}
	if bv, ok := v.(bool); ok {
		return bv
	}
	return def
}

func safeIntSetting(b *buffer.Buffer, key string, def int) int {
	if b == nil || b.Settings == nil {
		return def
	}
	v, ok := b.Settings[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		if n > 0 {
			return int(n)
		}
	case int:
		if n > 0 {
			return n
		}
	}
	return def
}

func safeIndentString(b *buffer.Buffer) string {
	tabsToSpaces := safeBoolSetting(b, "tabstospaces", true)
	tabSize := safeIntSetting(b, "tabsize", 2)
	if tabsToSpaces {
		return strings.Repeat(" ", tabSize)
	}
	return "\t"
}

func leadingWhitespaceBeforeLoc(b *buffer.Buffer, loc buffer.Loc) string {
	lineBytes := b.LineBytes(loc.Y)
	ws := util.GetLeadingWhitespace(lineBytes)
	wsChars := util.CharacterCount(ws)
	if loc.X < wsChars {
		ws = util.SliceStart(ws, loc.X)
	}
	return string(ws)
}

func isEscapedAt(b *buffer.Buffer, pos buffer.Loc) bool {
	if pos.X <= 0 {
		return false
	}
	count := 0
	for x := pos.X - 1; x >= 0; x-- {
		if b.RuneAt(buffer.Loc{X: x, Y: pos.Y}) == '\\' {
			count++
		} else {
			break
		}
	}
	return count%2 == 1
}

func isSingleWrappedSpan(sel string, delim byte) bool {
	if len(sel) < 2 || sel[0] != delim || sel[len(sel)-1] != delim {
		return false
	}
	// Ensure closing delimiter is not escaped.
	bs := 0
	for i := len(sel) - 2; i >= 1 && sel[i] == '\\'; i-- {
		bs++
	}
	if bs%2 == 1 {
		return false
	}
	// Ensure there isn't an unescaped delimiter in the middle (e.g. "*a* *b*").
	escaped := false
	for i := 1; i < len(sel)-1; i++ {
		if escaped {
			escaped = false
			continue
		}
		if sel[i] == '\\' {
			escaped = true
			continue
		}
		if sel[i] == delim {
			return false
		}
	}
	return true
}

func mathInlineAtCursor(b *buffer.Buffer, c *buffer.Cursor) {
	if c.HasSelection() {
		start, end := orderedSelection(c)
		sel := string(c.GetSelection())
		if isSingleWrappedSpan(sel, '$') {
			inner := sel[1 : len(sel)-1]
			b.Replace(start, end, inner)
			c.ResetSelection()
			return
		}
		if start.X > 0 && !isEscapedAt(b, buffer.Loc{X: start.X - 1, Y: start.Y}) &&
			b.RuneAt(buffer.Loc{X: start.X - 1, Y: start.Y}) == '$' &&
			b.RuneAt(end) == '$' && !isEscapedAt(b, end) {
			b.Remove(end, buffer.Loc{X: end.X + 1, Y: end.Y})
			b.Remove(buffer.Loc{X: start.X - 1, Y: start.Y}, start)
			c.ResetSelection()
			target := end
			if start.Y == end.Y {
				target.X--
			}
			c.GotoLoc(target)
			return
		}
		b.Insert(end, "$")
		b.Insert(start, "$")
		c.ResetSelection()
		target := buffer.Loc{X: end.X + 1, Y: end.Y}
		if start.Y == end.Y {
			target.X = end.X + 2
		}
		c.GotoLoc(target)
		return
	}

	pos := c.Loc
	lineLen := util.CharacterCount(b.LineBytes(pos.Y))
	if pos.X > 0 && pos.X < lineLen &&
		b.RuneAt(buffer.Loc{X: pos.X - 1, Y: pos.Y}) == '$' &&
		b.RuneAt(pos) == '$' &&
		!isEscapedAt(b, buffer.Loc{X: pos.X - 1, Y: pos.Y}) {
		target := buffer.Loc{X: pos.X - 1, Y: pos.Y}
		b.Remove(target, buffer.Loc{X: pos.X + 1, Y: pos.Y})
		c.GotoLoc(target)
		return
	}

	if detectModeAtLoc(b, pos) == ModeMath {
		if closeLoc, ok := findClosingDollarForward(b, pos); ok {
			c.GotoLoc(buffer.Loc{X: closeLoc.X + 1, Y: closeLoc.Y})
			return
		}
		b.Insert(pos, "$")
		return
	}

	b.Insert(pos, "$$")
	c.GotoLoc(buffer.Loc{X: pos.X + 1, Y: pos.Y})
}

func mathDisplayAtCursor(b *buffer.Buffer, c *buffer.Cursor) {
	indentUnit := safeIndentString(b)

	if c.HasSelection() {
		start, end := orderedSelection(c)
		sel := string(c.GetSelection())
		ws := leadingWhitespaceBeforeLoc(b, start)
		innerWS := ws + indentUnit

		if isSingleWrappedSpan(sel, '$') {
			inner := sel[1 : len(sel)-1]
			inner = unwrapDisplayInner(inner, innerWS)
			b.Replace(start, end, inner)
			c.ResetSelection()
			return
		}

		wrapped := "$\n" + innerWS + sel + "\n" + ws + "$"
		b.Replace(start, end, wrapped)
		c.ResetSelection()
		// Place cursor at end of sel inside display math.
		selLines := strings.Split(sel, "\n")
		targetY := start.Y + len(selLines)
		var targetX int
		if len(selLines) == 1 {
			targetX = util.CharacterCountInString(innerWS + selLines[0])
		} else {
			targetX = util.CharacterCountInString(selLines[len(selLines)-1])
		}
		c.GotoLoc(buffer.Loc{X: targetX, Y: targetY})
		return
	}

	pos := c.Loc
	lineLen := util.CharacterCount(b.LineBytes(pos.Y))

	// Empty $|$ -> expand to multi-line display math
	if pos.X > 0 && pos.X < lineLen &&
		b.RuneAt(buffer.Loc{X: pos.X - 1, Y: pos.Y}) == '$' &&
		b.RuneAt(pos) == '$' &&
		!isEscapedAt(b, buffer.Loc{X: pos.X - 1, Y: pos.Y}) {
		ws := leadingWhitespaceBeforeLoc(b, buffer.Loc{X: pos.X - 1, Y: pos.Y})
		innerWS := ws + indentUnit
		openLoc := buffer.Loc{X: pos.X - 1, Y: pos.Y}
		closeLoc := buffer.Loc{X: pos.X + 1, Y: pos.Y}
		b.Replace(openLoc, closeLoc, "$\n"+innerWS+"\n"+ws+"$")
		c.GotoLoc(buffer.Loc{X: util.CharacterCountInString(innerWS), Y: pos.Y + 1})
		return
	}

	if detectModeAtLoc(b, pos) == ModeMath {
		// Check if inside a single-line $...$ span on pos.Y
		if openX, closeX, ok := findEnclosingSingleLineMath(b, pos); ok {
			if closeX-openX >= 4 &&
				b.RuneAt(buffer.Loc{X: openX + 1, Y: pos.Y}) == ' ' &&
				b.RuneAt(buffer.Loc{X: closeX - 1, Y: pos.Y}) == ' ' {
				// Toggle $ x $ -> $x$
				b.Remove(buffer.Loc{X: closeX - 1, Y: pos.Y}, buffer.Loc{X: closeX, Y: pos.Y})
				b.Remove(buffer.Loc{X: openX + 1, Y: pos.Y}, buffer.Loc{X: openX + 2, Y: pos.Y})
				newX := util.Clamp(pos.X-1, openX+1, closeX-2)
				c.GotoLoc(buffer.Loc{X: newX, Y: pos.Y})
				return
			}
			// Toggle $x$ -> $ x $
			b.Insert(buffer.Loc{X: closeX, Y: pos.Y}, " ")
			b.Insert(buffer.Loc{X: openX + 1, Y: pos.Y}, " ")
			c.GotoLoc(buffer.Loc{X: pos.X + 1, Y: pos.Y})
			return
		}
		// Multi-line display math: step out past closing "$"
		if closeLoc, ok := findClosingDollarForward(b, pos); ok {
			c.GotoLoc(buffer.Loc{X: closeLoc.X + 1, Y: closeLoc.Y})
			return
		}
	}

	ws := leadingWhitespaceBeforeLoc(b, pos)
	innerWS := ws + indentUnit
	b.Insert(pos, "$\n"+innerWS+"\n"+ws+"$")
	c.GotoLoc(buffer.Loc{X: util.CharacterCountInString(innerWS), Y: pos.Y + 1})
}

func unwrapDisplayInner(inner, innerWS string) string {
	if strings.Contains(inner, "\n") {
		inner = strings.TrimPrefix(inner, "\n")
		lines := strings.Split(inner, "\n")
		if len(lines) > 1 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		for i := range lines {
			if strings.HasPrefix(lines[i], innerWS) {
				lines[i] = strings.TrimPrefix(lines[i], innerWS)
			}
		}
		return strings.Join(lines, "\n")
	}
	if len(inner) >= 2 && strings.HasPrefix(inner, " ") && strings.HasSuffix(inner, " ") {
		return inner[1 : len(inner)-1]
	}
	return inner
}

func findClosingDollarForward(b *buffer.Buffer, from buffer.Loc) (buffer.Loc, bool) {
	linesNum := b.LinesNum()
	inString := false
	for y := from.Y; y < linesNum; y++ {
		lineBytes := b.LineBytes(y)
		runes := lineToRunes(lineBytes)
		startX := 0
		if y == from.Y {
			startX = from.X
		}
		inLineComment := false
		for x := startX; x < len(runes); x++ {
			if inLineComment {
				break
			}
			r := runes[x]
			if inString {
				if r == '\\' {
					x++
					continue
				}
				if r == '"' {
					inString = false
				}
				continue
			}
			if r == '\\' {
				x++
				continue
			}
			if r == '/' && x+1 < len(runes) && runes[x+1] == '/' {
				inLineComment = true
				continue
			}
			if r == '"' {
				inString = true
				continue
			}
			if r == '$' {
				return buffer.Loc{X: x, Y: y}, true
			}
		}
	}
	return buffer.Loc{}, false
}

func findEnclosingSingleLineMath(b *buffer.Buffer, pos buffer.Loc) (openX int, closeX int, ok bool) {
	lineBytes := b.LineBytes(pos.Y)
	runes := lineToRunes(lineBytes)
	openX = -1
	inString := false
	for x := 0; x < len(runes); x++ {
		r := runes[x]
		if inString {
			if r == '\\' {
				x++
				continue
			}
			if r == '"' {
				inString = false
			}
			continue
		}
		if r == '\\' {
			x++
			continue
		}
		if r == '"' {
			inString = true
			continue
		}
		if r == '$' {
			if x < pos.X {
				if openX < 0 {
					openX = x
				} else {
					openX = -1
				}
			} else if openX >= 0 {
				return openX, x, true
			}
		}
	}
	return 0, 0, false
}

func wrapDelimiterAtCursor(b *buffer.Buffer, c *buffer.Cursor, delim rune) {
	dStr := string(delim)

	if c.HasSelection() {
		start, end := orderedSelection(c)
		sel := string(c.GetSelection())
		if isSingleWrappedSpan(sel, byte(delim)) {
			inner := sel[1 : len(sel)-1]
			b.Replace(start, end, inner)
			c.ResetSelection()
			return
		}
		if start.X > 0 && !isEscapedAt(b, buffer.Loc{X: start.X - 1, Y: start.Y}) &&
			b.RuneAt(buffer.Loc{X: start.X - 1, Y: start.Y}) == delim &&
			b.RuneAt(end) == delim && !isEscapedAt(b, end) {
			b.Remove(end, buffer.Loc{X: end.X + 1, Y: end.Y})
			b.Remove(buffer.Loc{X: start.X - 1, Y: start.Y}, start)
			c.ResetSelection()
			target := end
			if start.Y == end.Y {
				target.X--
			}
			c.GotoLoc(target)
			return
		}
		b.Insert(end, dStr)
		b.Insert(start, dStr)
		c.ResetSelection()
		target := buffer.Loc{X: end.X + 1, Y: end.Y}
		if start.Y == end.Y {
			target.X = end.X + 2
		}
		c.GotoLoc(target)
		return
	}

	pos := c.Loc
	lineBytes := b.LineBytes(pos.Y)
	lineLen := util.CharacterCount(lineBytes)

	if pos.X > 0 && pos.X < lineLen &&
		b.RuneAt(buffer.Loc{X: pos.X - 1, Y: pos.Y}) == delim &&
		b.RuneAt(pos) == delim &&
		!isEscapedAt(b, buffer.Loc{X: pos.X - 1, Y: pos.Y}) {
		target := buffer.Loc{X: pos.X - 1, Y: pos.Y}
		b.Remove(target, buffer.Loc{X: pos.X + 1, Y: pos.Y})
		c.GotoLoc(target)
		return
	}

	if detectModeAtLoc(b, pos) == ModeMarkup {
		if openX, closeX, ok := findEnclosingSameLineDelim(lineBytes, pos.X, delim); ok {
			b.Remove(buffer.Loc{X: closeX, Y: pos.Y}, buffer.Loc{X: closeX + 1, Y: pos.Y})
			b.Remove(buffer.Loc{X: openX, Y: pos.Y}, buffer.Loc{X: openX + 1, Y: pos.Y})
			c.GotoLoc(buffer.Loc{X: util.Max(0, pos.X-1), Y: pos.Y})
			return
		}
	}

	b.Insert(pos, dStr+dStr)
	c.GotoLoc(buffer.Loc{X: pos.X + 1, Y: pos.Y})
}

func findEnclosingSameLineDelim(lineBytes []byte, curX int, delim rune) (openX, closeX int, ok bool) {
	runes := lineToRunes(lineBytes)
	if curX <= 0 || curX > len(runes) {
		return 0, 0, false
	}

	openX = -1
	for x := curX - 1; x >= 0; x-- {
		if runes[x] == delim && !isRuneSliceEscaped(runes, x) {
			if delim == '_' && x > 0 && util.IsWordChar(runes[x-1]) {
				return 0, 0, false
			}
			openX = x
			break
		}
	}
	if openX < 0 {
		return 0, 0, false
	}

	closeX = -1
	for x := curX; x < len(runes); x++ {
		if runes[x] == delim && !isRuneSliceEscaped(runes, x) {
			if delim == '_' && x+1 < len(runes) && util.IsWordChar(runes[x+1]) {
				return 0, 0, false
			}
			closeX = x
			break
		}
	}
	if closeX < 0 || closeX <= openX+1 {
		return 0, 0, false
	}
	return openX, closeX, true
}

func isRuneSliceEscaped(runes []rune, idx int) bool {
	bs := 0
	for i := idx - 1; i >= 0 && runes[i] == '\\'; i-- {
		bs++
	}
	return bs%2 == 1
}

// lineToRunes decodes a line byte slice into one base rune per micro character
// position (matching buffer.Loc.X / util.CharacterCount).
func lineToRunes(lineBytes []byte) []rune {
	out := make([]rune, 0, utf8.RuneCount(lineBytes))
	for len(lineBytes) > 0 {
		r, _, size := util.DecodeCharacter(lineBytes)
		out = append(out, r)
		lineBytes = lineBytes[size:]
	}
	return out
}

func adjustHeadings(bp *action.BufPane, delta int) bool {
	cursors := bp.Buf.GetCursors()
	if len(cursors) == 0 {
		return false
	}

	seenLines := make(map[int]struct{}, len(cursors))
	var headingLines []int
	for _, c := range cursors {
		if _, seen := seenLines[c.Y]; seen {
			continue
		}
		seenLines[c.Y] = struct{}{}
		if _, _, ok := parseHeadingLine(bp.Buf, c.Y); ok {
			headingLines = append(headingLines, c.Y)
		}
	}
	if len(headingLines) == 0 {
		return false
	}

	// Process bottom-up so edits on lower lines don't affect upper line numbers.
	sort.Sort(sort.Reverse(sort.IntSlice(headingLines)))

	for _, y := range headingLines {
		wsChars, eqCount, ok := parseHeadingLine(bp.Buf, y)
		if !ok {
			continue
		}
		if delta < 0 {
			if eqCount > 1 {
				bp.Buf.Remove(buffer.Loc{X: wsChars, Y: y}, buffer.Loc{X: wsChars + 1, Y: y})
			}
		} else if delta > 0 {
			if eqCount < 6 {
				bp.Buf.Insert(buffer.Loc{X: wsChars, Y: y}, "=")
			}
		}
	}

	bp.Buf.MergeCursors()
	if bp.BWindow != nil {
		bp.Relocate()
	}
	return true
}

func parseHeadingLine(b *buffer.Buffer, y int) (wsChars int, eqCount int, ok bool) {
	if y < 0 || y >= b.LinesNum() {
		return 0, 0, false
	}
	runes := lineToRunes(b.LineBytes(y))
	i := 0
	for i < len(runes) && (runes[i] == ' ' || runes[i] == '\t') {
		i++
	}
	wsChars = i
	for i < len(runes) && runes[i] == '=' {
		i++
	}
	eqCount = i - wsChars
	if eqCount == 0 {
		return 0, 0, false
	}
	if i < len(runes) && runes[i] != ' ' && runes[i] != '\t' {
		return 0, 0, false
	}
	mode := detectModeAtLoc(b, buffer.Loc{X: wsChars, Y: y})
	if mode == ModeRaw || mode == ModeMath {
		return 0, 0, false
	}
	return wsChars, eqCount, true
}
