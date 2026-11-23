package conceal

import (
	"reflect"
	"sync"
	"unicode"

	runewidth "github.com/mattn/go-runewidth"
	"github.com/monicafaye/TypX/internal/typst"
	"github.com/monicafaye/TypX/internal/util"
)

// Span represents a single active concealed region on a line.
// BufStart and BufEnd are 0-based character column indices (matching
// util.DecodeCharacter and buffer.Loc.X), forming the half-open range [BufStart, BufEnd).
type Span struct {
	BufStart int
	BufEnd   int
	Glyph    rune
	Width    int
}

// LineView holds the concealed display text and bidirectional coordinate maps
// for one line.
//
// VisToBuf (len == len(DisplayText)+1) and BufToVis (len == OriginalLen+1)
// index DisplayText rune offsets (0..len(DisplayText) inclusive of the EOL
// sentinel mapping to OriginalLen), with any combining marks on a base rune
// mapping to that base character's buffer column.
type LineView struct {
	OriginalLen int
	DisplayText []rune
	Spans       []Span
	VisToBuf    []int
	BufToVis    []int
}

// VisualToBuf maps a DisplayText rune offset (clamped to [0, len(DisplayText)])
// to the corresponding 0-based buffer character column.
func (lv *LineView) VisualToBuf(visIdx int) int {
	if lv == nil || len(lv.VisToBuf) == 0 {
		return 0
	}
	if visIdx < 0 {
		visIdx = 0
	} else if visIdx >= len(lv.VisToBuf) {
		visIdx = len(lv.VisToBuf) - 1
	}
	return lv.VisToBuf[visIdx]
}

// BufToVisual maps a 0-based buffer character column (clamped to [0, OriginalLen])
// to the starting DisplayText rune offset for that character or concealed span.
func (lv *LineView) BufToVisual(bufIdx int) int {
	if lv == nil || len(lv.BufToVis) == 0 {
		return 0
	}
	if bufIdx < 0 {
		bufIdx = 0
	} else if bufIdx >= len(lv.BufToVis) {
		bufIdx = len(lv.BufToVis) - 1
	}
	return lv.BufToVis[bufIdx]
}

// SpanAtBufCol returns the active concealed Span covering bufCol
// (s.BufStart <= bufCol && bufCol < s.BufEnd), if any.
func (lv *LineView) SpanAtBufCol(bufCol int) (Span, bool) {
	if lv == nil || len(lv.Spans) == 0 {
		return Span{}, false
	}
	lo, hi := 0, len(lv.Spans)-1
	for lo <= hi {
		mid := lo + (hi-lo)/2
		s := lv.Spans[mid]
		if bufCol < s.BufStart {
			hi = mid - 1
		} else if bufCol >= s.BufEnd {
			lo = mid + 1
		} else {
			return s, true
		}
	}
	return Span{}, false
}

type charCell struct {
	base rune
	comb []rune
}

func decodeCells(line []byte) []charCell {
	if len(line) == 0 {
		return nil
	}
	cells := make([]charCell, 0, len(line))
	for len(line) > 0 {
		r, combc, size := util.DecodeCharacter(line)
		cells = append(cells, charCell{base: r, comb: combc})
		line = line[size:]
	}
	return cells
}

func isCellIdentStart(c charCell) bool {
	if len(c.comb) != 0 {
		return false
	}
	r := c.base
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
}

func isCellIdentCont(c charCell) bool {
	if len(c.comb) != 0 {
		return false
	}
	r := c.base
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

func cellsMatchKeyword(cells []charCell, start, end int) bool {
	n := end - start
	if n < 2 || n > 8 {
		return false
	}
	var buf [8]byte
	for i := 0; i < n; i++ {
		r := cells[start+i].base
		if r > 0x7f {
			return false
		}
		buf[i] = byte(r)
	}
	switch string(buf[:n]) {
	case "let", "set", "show", "import", "include", "if", "else", "for", "while", "return", "context", "break", "continue":
		return true
	default:
		return false
	}
}

func isCellURLCommentPrefix(cells []charCell, idx int) bool {
	if idx >= 5 &&
		cellsMatchASCII(cells[idx-5:idx], "http:") &&
		(idx == 5 || !isCellIdentCont(cells[idx-6])) {
		return true
	}
	if idx >= 6 &&
		cellsMatchASCII(cells[idx-6:idx], "https:") &&
		(idx == 6 || !isCellIdentCont(cells[idx-7])) {
		return true
	}
	return false
}

func urlPrefixLenAt(cells []charCell, idx int) int {
	if idx > 0 && isCellIdentCont(cells[idx-1]) {
		return 0
	}
	if idx+8 <= len(cells) && cellsMatchASCII(cells[idx:idx+8], "https://") {
		return 8
	}
	if idx+7 <= len(cells) && cellsMatchASCII(cells[idx:idx+7], "http://") {
		return 7
	}
	if idx+6 <= len(cells) && cellsMatchASCII(cells[idx:idx+6], "ftp://") {
		return 6
	}
	if idx+7 <= len(cells) && cellsMatchASCII(cells[idx:idx+7], "mailto:") {
		return 7
	}
	return 0
}

func cellsMatchASCII(cells []charCell, s string) bool {
	if len(cells) != len(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if len(cells[i].comb) != 0 || cells[i].base != rune(s[i]) {
			return false
		}
	}
	return true
}

func isPlainRune(c charCell, r rune) bool {
	return len(c.comb) == 0 && c.base == r
}

func isURLTerminator(r rune) bool {
	return unicode.IsSpace(r) || r == '<' || r == '>' || r == '"' || r == '\'' || r == ')' || r == ']' || r == ','
}

type blockKind uint8

const (
	blockBrace blockKind = iota + 1
	blockParen
	blockBracket
)

func classifyLineCells(cells []charCell, prev typst.LineState, mathSpans []typst.MathSpan) (inMathContent, inMarkup []bool) {
	n := len(cells)
	inMathContent = make([]bool, n)
	inMarkup = make([]bool, n)
	if n == 0 {
		return inMathContent, inMarkup
	}

	isMathCell := make([]bool, n)
	isMathDelim := make([]bool, n)
	for _, ms := range mathSpans {
		cs := ms.ContentStart
		if cs < 0 {
			cs = 0
		}
		ce := ms.ContentEnd
		if ce > n {
			ce = n
		}
		for k := cs; k < ce; k++ {
			isMathCell[k] = true
		}
		if ms.Start >= 0 && ms.Start < n && ms.Start < ms.ContentStart {
			isMathDelim[ms.Start] = true
		}
		if ms.End > ms.ContentEnd && ms.End-1 >= 0 && ms.End-1 < n {
			isMathDelim[ms.End-1] = true
		}
	}

	inRaw := prev.InRaw
	rawFenceLen := prev.RawFenceLen
	commentDepth := prev.CommentDepth
	codeDepth := prev.CodeDepth
	inString := false
	inCodeStmt := false
	stmtBaseStackLen := 0
	stmtBaseCode := 0
	inHashChain := false
	hashBaseStackLen := 0
	var stack []blockKind

	isCodeCtx := func() bool {
		if inCodeStmt && len(stack) == stmtBaseStackLen {
			return true
		}
		if inHashChain && len(stack) == hashBaseStackLen {
			return true
		}
		if len(stack) > 0 {
			top := stack[len(stack)-1]
			return top == blockBrace || top == blockParen
		}
		return codeDepth > 0
	}

	hasChainCont := func(idx int) bool {
		if idx >= n {
			return false
		}
		if isPlainRune(cells[idx], '(') || isPlainRune(cells[idx], '[') {
			return true
		}
		if isPlainRune(cells[idx], '.') && idx+1 < n && isCellIdentStart(cells[idx+1]) {
			return true
		}
		return false
	}

	checkElse := func(idx int) bool {
		j := idx
		for j < n && (isPlainRune(cells[j], ' ') || isPlainRune(cells[j], '\t')) {
			j++
		}
		if j+4 <= n && cellsMatchASCII(cells[j:j+4], "else") {
			if j+4 == n || (!isCellIdentCont(cells[j+4]) && !isPlainRune(cells[j+4], '-')) {
				return true
			}
		}
		return false
	}

	for i := 0; i < n; {
		if isMathDelim[i] {
			inString = false
			i++
			continue
		}

		if isMathCell[i] {
			if commentDepth > 0 {
				if isPlainRune(cells[i], '/') && i+1 < n && isMathCell[i+1] && isPlainRune(cells[i+1], '*') {
					commentDepth++
					i += 2
					continue
				}
				if isPlainRune(cells[i], '*') && i+1 < n && isMathCell[i+1] && isPlainRune(cells[i+1], '/') {
					commentDepth--
					i += 2
					continue
				}
				i++
				continue
			}
			if inString {
				if isPlainRune(cells[i], '\\') && i+1 < n && isMathCell[i+1] {
					i += 2
					continue
				}
				if isPlainRune(cells[i], '"') {
					inString = false
				}
				i++
				continue
			}
			if isPlainRune(cells[i], '/') && i+1 < n && isMathCell[i+1] {
				if isPlainRune(cells[i+1], '*') {
					commentDepth++
					i += 2
					continue
				}
				if isPlainRune(cells[i+1], '/') {
					for i < n && isMathCell[i] {
						i++
					}
					continue
				}
			}
			if isPlainRune(cells[i], '\\') && i+1 < n && isMathCell[i+1] {
				i += 2
				continue
			}
			if isPlainRune(cells[i], '"') {
				inString = true
				i++
				continue
			}
			inMathContent[i] = true
			i++
			continue
		}

		// Non-math regions
		if inRaw {
			if isPlainRune(cells[i], '`') {
				runLen := 0
				for i+runLen < n && !isMathCell[i+runLen] && !isMathDelim[i+runLen] && isPlainRune(cells[i+runLen], '`') {
					runLen++
				}
				if runLen >= rawFenceLen {
					inRaw = false
					rawFenceLen = 0
				}
				i += runLen
				continue
			}
			i++
			continue
		}

		if commentDepth > 0 {
			if isPlainRune(cells[i], '/') && i+1 < n && isPlainRune(cells[i+1], '*') {
				commentDepth++
				i += 2
				continue
			}
			if isPlainRune(cells[i], '*') && i+1 < n && isPlainRune(cells[i+1], '/') {
				commentDepth--
				i += 2
				continue
			}
			i++
			continue
		}

		if inString {
			if isPlainRune(cells[i], '\\') && i+1 < n {
				i += 2
				continue
			}
			if isPlainRune(cells[i], '"') {
				inString = false
			}
			i++
			continue
		}

		if isPlainRune(cells[i], '/') && i+1 < n && isPlainRune(cells[i+1], '/') {
			if isCodeCtx() || !isCellURLCommentPrefix(cells, i) {
				break
			}
		}

		if isPlainRune(cells[i], '/') && i+1 < n && isPlainRune(cells[i+1], '*') {
			commentDepth++
			i += 2
			continue
		}

		if !isCodeCtx() && isPlainRune(cells[i], '\\') && i+1 < n {
			i += 2
			continue
		}

		if isCodeCtx() && isPlainRune(cells[i], '"') {
			inString = true
			i++
			continue
		}

		if isPlainRune(cells[i], '`') {
			runLen := 0
			for i+runLen < n && !isMathCell[i+runLen] && !isMathDelim[i+runLen] && isPlainRune(cells[i+runLen], '`') {
				runLen++
			}
			if runLen == 2 {
				i += 2
				continue
			}
			inRaw = true
			rawFenceLen = runLen
			i += runLen
			continue
		}

		if isPlainRune(cells[i], '#') && i+1 < n {
			if isPlainRune(cells[i+1], '{') {
				inHashChain = false
				stack = append(stack, blockBrace)
				codeDepth++
				i += 2
				continue
			}
			if isPlainRune(cells[i+1], '(') {
				inHashChain = false
				stack = append(stack, blockParen)
				codeDepth++
				i += 2
				continue
			}
			if isPlainRune(cells[i+1], '[') {
				inHashChain = false
				stack = append(stack, blockBracket)
				i += 2
				continue
			}
			if isCellIdentStart(cells[i+1]) {
				identStart := i + 1
				j := identStart
				for j < n {
					if isCellIdentCont(cells[j]) {
						j++
					} else if isPlainRune(cells[j], '-') && j+1 < n && isCellIdentCont(cells[j+1]) {
						j++
					} else {
						break
					}
				}
				if cellsMatchKeyword(cells, identStart, j) {
					inCodeStmt = true
					stmtBaseStackLen = len(stack)
					stmtBaseCode = codeDepth
					inHashChain = false
				} else {
					hashBaseStackLen = len(stack)
					inHashChain = hasChainCont(j)
				}
				i = j
				continue
			}
		}

		if isCodeCtx() {
			if isPlainRune(cells[i], '{') {
				stack = append(stack, blockBrace)
				codeDepth++
				i++
				continue
			}
			if isPlainRune(cells[i], '(') {
				stack = append(stack, blockParen)
				codeDepth++
				i++
				continue
			}
			if isPlainRune(cells[i], '[') {
				stack = append(stack, blockBracket)
				i++
				continue
			}
			if isPlainRune(cells[i], '}') || isPlainRune(cells[i], ')') {
				want := blockBrace
				if isPlainRune(cells[i], ')') {
					want = blockParen
				}
				if len(stack) > 0 && stack[len(stack)-1] == want {
					stack = stack[:len(stack)-1]
					if codeDepth > 0 {
						codeDepth--
					}
					if inCodeStmt && len(stack) <= stmtBaseStackLen && codeDepth <= stmtBaseCode {
						if want == blockBrace && !checkElse(i+1) {
							inCodeStmt = false
						}
					}
					if inHashChain && len(stack) == hashBaseStackLen {
						inHashChain = hasChainCont(i + 1)
					}
				} else if codeDepth > 0 {
					codeDepth--
					if inCodeStmt && codeDepth <= stmtBaseCode {
						if want == blockBrace && !checkElse(i+1) {
							inCodeStmt = false
						}
					}
				}
				i++
				continue
			}
			if isPlainRune(cells[i], ';') {
				if inCodeStmt && len(stack) == stmtBaseStackLen && codeDepth == stmtBaseCode {
					inCodeStmt = false
				}
				inHashChain = false
				i++
				continue
			}
			if inHashChain && len(stack) == hashBaseStackLen {
				if isPlainRune(cells[i], '.') && i+1 < n && isCellIdentStart(cells[i+1]) {
					j := i + 1
					for j < n {
						if isCellIdentCont(cells[j]) || (isPlainRune(cells[j], '-') && j+1 < n && isCellIdentCont(cells[j+1])) {
							j++
						} else {
							break
						}
					}
					inHashChain = hasChainCont(j)
					i = j
					continue
				}
				inHashChain = false
				continue
			}
			if isPlainRune(cells[i], ']') && len(stack) > 0 && stack[len(stack)-1] == blockBracket {
				stack = stack[:len(stack)-1]
				inCodeStmt = false
				inHashChain = false
				i++
				continue
			}
			i++
			continue
		}

		// Markup / content block context
		if len(stack) > 0 && stack[len(stack)-1] == blockBracket {
			if isPlainRune(cells[i], '[') {
				stack = append(stack, blockBracket)
				inMarkup[i] = true
				i++
				continue
			}
			if isPlainRune(cells[i], ']') {
				stack = stack[:len(stack)-1]
				if inCodeStmt && len(stack) == stmtBaseStackLen && codeDepth == stmtBaseCode {
					if !checkElse(i + 1) {
						inCodeStmt = false
					}
				}
				if inHashChain && len(stack) == hashBaseStackLen {
					inHashChain = hasChainCont(i + 1)
				}
				i++
				continue
			}
		}

		if urlPrefixLenAt(cells, i) > 0 {
			for i < n && !isMathCell[i] && !isMathDelim[i] && !isURLTerminator(cells[i].base) {
				i++
			}
			continue
		}

		inMarkup[i] = true
		i++
	}

	return inMathContent, inMarkup
}

func matchMathToken(cells []charCell, i int, inMathContent []bool) (Span, bool) {
	if len(cells[i].comb) != 0 {
		return Span{}, false
	}
	n := len(cells)

	// 1. Greedy longest-prefix lookup in curated mathBuckets.
	if entries, ok := mathBuckets[cells[i].base]; ok {
		for _, entry := range entries {
			patLen := len(entry.runes)
			if i+patLen > n {
				continue
			}
			matched := true
			for k := 0; k < patLen; k++ {
				if !inMathContent[i+k] || len(cells[i+k].comb) != 0 || cells[i+k].base != entry.runes[k] {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			if entry.wordBoundary {
				if i > 0 {
					if util.IsWordChar(cells[i-1].base) {
						continue
					}
					if isPlainRune(cells[i-1], '.') && i > 1 && util.IsWordChar(cells[i-2].base) {
						continue
					}
				}
				j := i + patLen
				if j < n {
					if util.IsWordChar(cells[j].base) {
						continue
					}
					if isPlainRune(cells[j], '.') && j+1 < n && util.IsWordChar(cells[j+1].base) {
						continue
					}
				}
			}
			w := runewidth.RuneWidth(entry.glyph)
			if w <= 0 {
				w = 1
			}
			return Span{
				BufStart: i,
				BufEnd:   i + patLen,
				Glyph:    entry.glyph,
				Width:    w,
			}, true
		}
	}

	// 2. Single-character superscript (^c) or subscript (_c).
	if (cells[i].base == '^' || cells[i].base == '_') && i+1 < n && inMathContent[i+1] && len(cells[i+1].comb) == 0 {
		var glyph rune
		var ok bool
		if cells[i].base == '^' {
			glyph, ok = superMap[cells[i+1].base]
		} else {
			glyph, ok = subMap[cells[i+1].base]
		}
		if ok {
			leftOK := i == 0 || (!isPlainRune(cells[i-1], '^') && !isPlainRune(cells[i-1], '_') && !isPlainRune(cells[i-1], '\\'))
			j := i + 2
			rightOK := j >= n || (!util.IsWordChar(cells[j].base) && !isPlainRune(cells[j], cells[i].base))
			if leftOK && rightOK {
				w := runewidth.RuneWidth(glyph)
				if w <= 0 {
					w = 1
				}
				return Span{
					BufStart: i,
					BufEnd:   j,
					Glyph:    glyph,
					Width:    w,
				}, true
			}
		}
	}

	// 3. Ellipsis (...) in math.
	if cells[i].base == '.' && i+3 <= n &&
		inMathContent[i+1] && inMathContent[i+2] &&
		isPlainRune(cells[i+1], '.') && isPlainRune(cells[i+2], '.') {
		leftOK := i == 0 || !isPlainRune(cells[i-1], '.')
		rightOK := i+3 >= n || !isPlainRune(cells[i+3], '.')
		if leftOK && rightOK {
			w := runewidth.RuneWidth('…')
			if w <= 0 {
				w = 1
			}
			return Span{
				BufStart: i,
				BufEnd:   i + 3,
				Glyph:    '…',
				Width:    w,
			}, true
		}
	}

	return Span{}, false
}

func matchMarkupToken(cells []charCell, i int, inMarkup []bool) (Span, bool) {
	if len(cells[i].comb) != 0 {
		return Span{}, false
	}
	n := len(cells)

	if cells[i].base == '-' {
		isHTMLCommentOpen := i >= 2 && isPlainRune(cells[i-2], '<') && isPlainRune(cells[i-1], '!')
		// Check --- before --.
		if i+3 <= n && inMarkup[i+1] && inMarkup[i+2] &&
			isPlainRune(cells[i+1], '-') && isPlainRune(cells[i+2], '-') {
			leftOK := !isHTMLCommentOpen && (i == 0 || (!isPlainRune(cells[i-1], '-') && !isPlainRune(cells[i-1], '<') && !isPlainRune(cells[i-1], '\\')))
			rightOK := i+3 >= n || (!isPlainRune(cells[i+3], '-') && !isPlainRune(cells[i+3], '>'))
			if leftOK && rightOK {
				w := runewidth.RuneWidth('—')
				if w <= 0 {
					w = 1
				}
				return Span{BufStart: i, BufEnd: i + 3, Glyph: '—', Width: w}, true
			}
		}
		if i+2 <= n && inMarkup[i+1] && isPlainRune(cells[i+1], '-') {
			leftOK := !isHTMLCommentOpen && (i == 0 || (!isPlainRune(cells[i-1], '-') && !isPlainRune(cells[i-1], '<') && !isPlainRune(cells[i-1], '\\')))
			rightOK := i+2 >= n || (!isPlainRune(cells[i+2], '-') && !isPlainRune(cells[i+2], '>'))
			if leftOK && rightOK {
				w := runewidth.RuneWidth('–')
				if w <= 0 {
					w = 1
				}
				return Span{BufStart: i, BufEnd: i + 2, Glyph: '–', Width: w}, true
			}
		}
	}

	if cells[i].base == '.' && i+3 <= n &&
		inMarkup[i+1] && inMarkup[i+2] &&
		isPlainRune(cells[i+1], '.') && isPlainRune(cells[i+2], '.') {
		leftOK := i == 0 || (!isPlainRune(cells[i-1], '.') && !isPlainRune(cells[i-1], '\\'))
		rightOK := i+3 >= n || !isPlainRune(cells[i+3], '.')
		if leftOK && rightOK {
			w := runewidth.RuneWidth('…')
			if w <= 0 {
				w = 1
			}
			return Span{BufStart: i, BufEnd: i + 3, Glyph: '…', Width: w}, true
		}
	}

	return Span{}, false
}

// ScanLine computes concealed spans and bidirectional rune-offset coordinate
// maps for a single line given its entering LineState, line MathSpans, and
// optional active cursor columns (for "token" mode unconcealing).
func ScanLine(line []byte, lineState typst.LineState, mathSpans []typst.MathSpan, activeCursorCols []int) *LineView {
	cells := decodeCells(line)
	n := len(cells)
	if n == 0 {
		return &LineView{
			OriginalLen: 0,
			VisToBuf:    []int{0},
			BufToVis:    []int{0},
		}
	}

	inMathContent, inMarkup := classifyLineCells(cells, lineState, mathSpans)

	var candidates []Span
	for i := 0; i < n; {
		if inMathContent[i] {
			if sp, ok := matchMathToken(cells, i, inMathContent); ok {
				candidates = append(candidates, sp)
				i = sp.BufEnd
				continue
			}
		} else if inMarkup[i] {
			if sp, ok := matchMarkupToken(cells, i, inMarkup); ok {
				candidates = append(candidates, sp)
				i = sp.BufEnd
				continue
			}
		}
		i++
	}

	var spans []Span
	if len(activeCursorCols) == 0 {
		spans = candidates
	} else {
		for _, sp := range candidates {
			touched := false
			for _, col := range activeCursorCols {
				if sp.BufStart <= col && col <= sp.BufEnd {
					touched = true
					break
				}
			}
			if !touched {
				spans = append(spans, sp)
			}
		}
	}

	displayText := make([]rune, 0, n)
	visToBuf := make([]int, 0, n+1)
	bufToVis := make([]int, n+1)

	spanIdx := 0
	for i := 0; i < n; {
		if spanIdx < len(spans) && spans[spanIdx].BufStart == i {
			sp := spans[spanIdx]
			visStart := len(displayText)
			displayText = append(displayText, sp.Glyph)
			visToBuf = append(visToBuf, sp.BufStart)
			for k := sp.BufStart; k < sp.BufEnd; k++ {
				bufToVis[k] = visStart
			}
			i = sp.BufEnd
			spanIdx++
			continue
		}
		visStart := len(displayText)
		bufToVis[i] = visStart
		displayText = append(displayText, cells[i].base)
		visToBuf = append(visToBuf, i)
		for _, m := range cells[i].comb {
			displayText = append(displayText, m)
			visToBuf = append(visToBuf, i)
		}
		i++
	}

	bufToVis[n] = len(displayText)
	visToBuf = append(visToBuf, n)

	return &LineView{
		OriginalLen: n,
		DisplayText: displayText,
		Spans:       spans,
		VisToBuf:    visToBuf,
		BufToVis:    bufToVis,
	}
}

const (
	defaultMaxDocs  = 16
	defaultMaxViews = 512
)

type docCache struct {
	version uint64
	states  []typst.LineState
	views   map[int]*LineView
	order   []int
}

// BufferCache caches per-document LineState prefixes and LineViews keyed by
// (docKey, version), with bounded LRU/FIFO eviction and thread-safe access.
type BufferCache struct {
	mu       sync.Mutex
	maxDocs  int
	maxViews int
	docs     map[any]*docCache
	docOrder []any
}

// NewBufferCache constructs a BufferCache with bounded document and view caches.
func NewBufferCache() *BufferCache {
	return &BufferCache{
		maxDocs:  defaultMaxDocs,
		maxViews: defaultMaxViews,
		docs:     make(map[any]*docCache),
	}
}

func isComparableKey(k any) bool {
	if k == nil {
		return false
	}
	return reflect.TypeOf(k).Comparable()
}

// Invalidate removes all cached states and views for docKey.
func (c *BufferCache) Invalidate(docKey any) {
	if c == nil || !isComparableKey(docKey) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.docs, docKey)
	for i, k := range c.docOrder {
		if k == docKey {
			c.docOrder = append(c.docOrder[:i], c.docOrder[i+1:]...)
			break
		}
	}
}

func (c *BufferCache) touchDocLocked(docKey any) *docCache {
	if dc, ok := c.docs[docKey]; ok {
		for i, k := range c.docOrder {
			if k == docKey {
				if i != len(c.docOrder)-1 {
					copy(c.docOrder[i:], c.docOrder[i+1:])
					c.docOrder[len(c.docOrder)-1] = docKey
				}
				break
			}
		}
		return dc
	}
	if len(c.docs) >= c.maxDocs && len(c.docOrder) > 0 {
		evict := c.docOrder[0]
		c.docOrder = c.docOrder[1:]
		delete(c.docs, evict)
	}
	dc := &docCache{
		views: make(map[int]*LineView),
	}
	c.docs[docKey] = dc
	c.docOrder = append(c.docOrder, docKey)
	return dc
}

// LineView returns the concealed LineView for lineN at version.
// When len(activeCursorCols) > 0, view caching is bypassed so cursor movement
// at the same buffer version never returns stale unconcealment.
func (c *BufferCache) LineView(docKey any, version uint64, src typst.LineSource, lineN int, activeCursorCols []int) *LineView {
	if src == nil || lineN < 0 || lineN >= src.LinesNum() {
		return &LineView{
			OriginalLen: 0,
			VisToBuf:    []int{0},
			BufToVis:    []int{0},
		}
	}

	if c == nil || !isComparableKey(docKey) {
		var st typst.LineState
		for i := 0; i < lineN; i++ {
			st, _ = typst.ScanLineState(st, src.LineBytes(i))
		}
		lineBytes := src.LineBytes(lineN)
		_, mathSpans := typst.ScanLineState(st, lineBytes)
		return ScanLine(lineBytes, st, mathSpans, activeCursorCols)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.docs == nil {
		c.docs = make(map[any]*docCache)
	}
	if c.maxDocs <= 0 {
		c.maxDocs = defaultMaxDocs
	}
	if c.maxViews <= 0 {
		c.maxViews = defaultMaxViews
	}

	dc := c.touchDocLocked(docKey)
	if dc.version != version || len(dc.states) == 0 {
		dc.version = version
		dc.states = append(dc.states[:0], typst.LineState{})
		dc.views = make(map[int]*LineView)
		dc.order = dc.order[:0]
	}

	for len(dc.states) <= lineN {
		idx := len(dc.states) - 1
		next, _ := typst.ScanLineState(dc.states[idx], src.LineBytes(idx))
		dc.states = append(dc.states, next)
	}

	prevState := dc.states[lineN]
	if len(activeCursorCols) > 0 {
		lineBytes := src.LineBytes(lineN)
		_, mathSpans := typst.ScanLineState(prevState, lineBytes)
		return ScanLine(lineBytes, prevState, mathSpans, activeCursorCols)
	}

	if lv, ok := dc.views[lineN]; ok {
		return lv
	}

	lineBytes := src.LineBytes(lineN)
	_, mathSpans := typst.ScanLineState(prevState, lineBytes)
	lv := ScanLine(lineBytes, prevState, mathSpans, nil)

	if len(dc.views) >= c.maxViews && len(dc.order) > 0 {
		evictLine := dc.order[0]
		dc.order = dc.order[1:]
		delete(dc.views, evictLine)
	}
	dc.views[lineN] = lv
	dc.order = append(dc.order, lineN)
	return lv
}
