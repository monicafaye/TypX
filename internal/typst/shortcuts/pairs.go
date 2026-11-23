package shortcuts

import (
	"sort"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/util"
)

var pairClose = map[rune]rune{
	'$': '$',
	'[': ']',
	'(': ')',
	'{': '}',
	'"': '"',
}

func isPairRelevantRune(r rune) bool {
	switch r {
	case '$', '[', ']', '(', ')', '{', '}', '"':
		return true
	default:
		return false
	}
}

func smartPairsEnabled(b *buffer.Buffer) bool {
	return safeBoolSetting(b, "typst.smartPairs", true)
}

// HandleTypstRune applies Typst-aware smart pairing, skip-over, and selection
// wrapping for $, [, (, {, " before normal character insertion.
// Returns true if the rune was handled and normal insertion should be skipped.
func HandleTypstRune(bp *action.BufPane, r rune) bool {
	if !isPairRelevantRune(r) || !canEditTypst(bp) || !smartPairsEnabled(bp.Buf) {
		return false
	}

	cursors := append([]*buffer.Cursor(nil), bp.Buf.GetCursors()...)
	if len(cursors) <= 1 {
		c := activeCursor(bp)
		if c == nil {
			return false
		}
		handled := handleTypstRuneAtCursor(bp.Buf, c, r)
		if handled && bp.BWindow != nil {
			bp.Relocate()
		}
		return handled
	}

	// Multi-cursor: check whether at least one cursor triggers smart-pair logic.
	// If none trigger, return false so DoRuneInsert handles all cursors normally.
	anyHandled := false
	for _, c := range cursors {
		if wouldHandleTypstRune(bp.Buf, c, r) {
			anyHandled = true
			break
		}
	}
	if !anyHandled {
		return false
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
		if !handleTypstRuneAtCursor(bp.Buf, c, r) {
			if c.HasSelection() {
				c.DeleteSelection()
				c.ResetSelection()
			}
			bp.Buf.Insert(c.Loc, string(r))
		}
	}
	bp.Buf.SetCurCursor(0)
	bp.Cursor = bp.Buf.GetActiveCursor()
	bp.Buf.MergeCursors()
	if bp.BWindow != nil {
		bp.Relocate()
	}
	return true
}

func wouldHandleTypstRune(b *buffer.Buffer, c *buffer.Cursor, r rune) bool {
	if c == nil || (b.OverwriteMode && !c.HasSelection()) {
		return false
	}
	if c.HasSelection() {
		if _, isOpen := pairClose[r]; !isOpen {
			return false
		}
		start, _ := orderedSelection(c)
		return detectModeAtLoc(b, start) != ModeRaw
	}
	pos := c.Loc
	if isEscapedAt(b, pos) || detectModeAtLoc(b, pos) == ModeRaw {
		return false
	}
	lineBytes := b.LineBytes(pos.Y)
	lineLen := util.CharacterCount(lineBytes)
	mode := detectModeAtLoc(b, pos)
	var nextRune, prevRune rune
	if pos.X < lineLen {
		nextRune = b.RuneAt(pos)
	}
	if pos.X > 0 {
		prevRune = b.RuneAt(buffer.Loc{X: pos.X - 1, Y: pos.Y})
	}
	switch r {
	case '$':
		if pos.X < lineLen && nextRune == '$' && mode == ModeMath {
			return true
		}
		return mode != ModeMath && !(pos.X < lineLen && util.IsWordChar(nextRune))
	case '"':
		if pos.X < lineLen && nextRune == '"' {
			return true
		}
		return !((pos.X > 0 && util.IsWordChar(prevRune)) ||
			(pos.X < lineLen && util.IsWordChar(nextRune)) ||
			hasOddUnescapedQuotesBefore(lineBytes, pos.X))
	case ']', ')', '}':
		return pos.X < lineLen && nextRune == r
	case '[', '(', '{':
		if (mode == ModeCode || mode == ModeMath) && hasOddUnescapedQuotesBefore(lineBytes, pos.X) {
			return false
		}
		return !(pos.X < lineLen && util.IsWordChar(nextRune))
	default:
		return false
	}
}

func handleTypstRuneAtCursor(b *buffer.Buffer, c *buffer.Cursor, r rune) bool {
	if b.OverwriteMode && !c.HasSelection() {
		return false
	}

	if c.HasSelection() {
		closeR, isOpen := pairClose[r]
		if !isOpen {
			return false
		}
		start, end := orderedSelection(c)
		if detectModeAtLoc(b, start) == ModeRaw {
			return false
		}
		sel := string(c.GetSelection())
		b.Replace(start, end, string(r)+sel+string(closeR))
		c.ResetSelection()
		return true
	}

	pos := c.Loc
	if isEscapedAt(b, pos) {
		return false
	}
	mode := detectModeAtLoc(b, pos)
	if mode == ModeRaw {
		return false
	}

	lineBytes := b.LineBytes(pos.Y)
	lineLen := util.CharacterCount(lineBytes)
	var nextRune, prevRune rune
	if pos.X < lineLen {
		nextRune = b.RuneAt(pos)
	}
	if pos.X > 0 {
		prevRune = b.RuneAt(buffer.Loc{X: pos.X - 1, Y: pos.Y})
	}

	// Skip-over checks
	switch r {
	case '$':
		if pos.X < lineLen && nextRune == '$' && mode == ModeMath {
			c.Right()
			return true
		}
	case '"':
		if pos.X < lineLen && nextRune == '"' {
			c.Right()
			return true
		}
	case ']', ')', '}':
		if pos.X < lineLen && nextRune == r {
			c.Right()
			return true
		}
		return false
	}

	closeR, isOpen := pairClose[r]
	if !isOpen {
		return false
	}

	// Auto-close suppression rules
	switch r {
	case '$':
		if mode == ModeMath {
			return false
		}
		if pos.X < lineLen && util.IsWordChar(nextRune) {
			return false
		}
	case '"':
		if (pos.X > 0 && util.IsWordChar(prevRune)) ||
			(pos.X < lineLen && util.IsWordChar(nextRune)) ||
			hasOddUnescapedQuotesBefore(lineBytes, pos.X) {
			return false
		}
	case '[', '(', '{':
		if (mode == ModeCode || mode == ModeMath) && hasOddUnescapedQuotesBefore(lineBytes, pos.X) {
			return false
		}
		if pos.X < lineLen && util.IsWordChar(nextRune) {
			return false
		}
	}

	b.Insert(pos, string(r)+string(closeR))
	c.GotoLoc(buffer.Loc{X: pos.X + 1, Y: pos.Y})
	return true
}

// HandleTypstBackspace deletes an empty smart pair ($$, [], (), {}, "") around
// the cursor when applicable. Returns true if handled.
func HandleTypstBackspace(bp *action.BufPane) bool {
	if !canEditTypst(bp) || !smartPairsEnabled(bp.Buf) {
		return false
	}
	c := activeCursor(bp)
	if c == nil || c.HasSelection() {
		return false
	}

	pos := c.Loc
	lineLen := util.CharacterCount(bp.Buf.LineBytes(pos.Y))
	if pos.X <= 0 || pos.X >= lineLen {
		return false
	}

	openPos := buffer.Loc{X: pos.X - 1, Y: pos.Y}
	prev := bp.Buf.RuneAt(openPos)
	next := bp.Buf.RuneAt(pos)
	if closeR, ok := pairClose[prev]; !ok || closeR != next {
		return false
	}
	if isEscapedAt(bp.Buf, openPos) {
		return false
	}
	if detectModeAtLoc(bp.Buf, pos) == ModeRaw {
		return false
	}

	end := buffer.Loc{X: pos.X + 1, Y: pos.Y}
	bp.Buf.Remove(openPos, end)
	c.GotoLoc(openPos)
	if bp.BWindow != nil {
		bp.Relocate()
	}
	return true
}

// HandleTypstNewline expands an empty pair ({}, [], (), $$) across lines with
// autoindent-aware indentation. Does not expand "". Returns true if handled.
func HandleTypstNewline(bp *action.BufPane) bool {
	if !canEditTypst(bp) || !smartPairsEnabled(bp.Buf) {
		return false
	}
	c := activeCursor(bp)
	if c == nil || c.HasSelection() {
		return false
	}

	pos := c.Loc
	lineBytes := bp.Buf.LineBytes(pos.Y)
	lineLen := util.CharacterCount(lineBytes)
	if pos.X <= 0 || pos.X >= lineLen {
		return false
	}

	openPos := buffer.Loc{X: pos.X - 1, Y: pos.Y}
	prev := bp.Buf.RuneAt(openPos)
	next := bp.Buf.RuneAt(pos)
	if prev == '"' {
		return false
	}
	if closeR, ok := pairClose[prev]; !ok || closeR != next {
		return false
	}
	if isEscapedAt(bp.Buf, openPos) {
		return false
	}
	if detectModeAtLoc(bp.Buf, pos) == ModeRaw {
		return false
	}

	baseIndent := string(util.GetLeadingWhitespace(lineBytes))
	autoindent := safeBoolSetting(bp.Buf, "autoindent", true)
	indentUnit := safeIndentString(bp.Buf)

	outerIndent := ""
	innerIndent := ""
	if autoindent {
		outerIndent = baseIndent
		innerIndent = baseIndent + indentUnit
	}

	bp.Buf.Insert(pos, "\n"+innerIndent+"\n"+outerIndent)
	c.GotoLoc(buffer.Loc{X: util.CharacterCountInString(innerIndent), Y: pos.Y + 1})
	if bp.BWindow != nil {
		bp.Relocate()
	}
	return true
}

func hasOddUnescapedQuotesBefore(lineBytes []byte, charCol int) bool {
	quotes := 0
	escaped := false
	inRaw := false
	rawLen := 0
	i := 0
	b := lineBytes

	for len(b) > 0 && i < charCol {
		r, _, size := util.DecodeCharacter(b)
		b = b[size:]
		i++

		if inRaw {
			if r == '`' {
				// Check consecutive backticks in raw context
				n := 1
				for len(b) > 0 && i < charCol {
					nr, _, nsize := util.DecodeCharacter(b)
					if nr != '`' {
						break
					}
					n++
					b = b[nsize:]
					i++
				}
				if n == rawLen {
					inRaw = false
					rawLen = 0
				}
			}
			continue
		}

		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '`' {
			n := 1
			for len(b) > 0 && i < charCol {
				nr, _, nsize := util.DecodeCharacter(b)
				if nr != '`' {
					break
				}
				n++
				b = b[nsize:]
				i++
			}
			inRaw = true
			rawLen = n
			continue
		}
		if r == '"' {
			quotes++
		}
	}

	return quotes%2 == 1
}
