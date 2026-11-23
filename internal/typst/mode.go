package typst

import (
	"unicode"
	"unicode/utf8"
)

// Mode represents the Typst lexical mode at a given buffer position.
type Mode int

const (
	ModeMarkup Mode = iota
	ModeMath
	ModeCode
	ModeRaw
)

// String returns the human-readable uppercase name of the mode.
func (m Mode) String() string {
	switch m {
	case ModeMarkup:
		return "MARKUP"
	case ModeMath:
		return "MATH"
	case ModeCode:
		return "CODE"
	case ModeRaw:
		return "RAW"
	default:
		return "UNKNOWN"
	}
}

// LineSource abstracts line-based access to a buffer or slice of lines.
type LineSource interface {
	LinesNum() int
	LineBytes(line int) []byte
}

// SliceSource implements LineSource for a slice of raw UTF-8 lines.
type SliceSource [][]byte

// LinesNum returns the number of lines in the slice.
func (s SliceSource) LinesNum() int {
	return len(s)
}

// LineBytes returns the raw bytes of line, or nil if out of bounds.
func (s SliceSource) LineBytes(line int) []byte {
	if line < 0 || line >= len(s) {
		return nil
	}
	return s[line]
}

// LineState holds the multi-line lexical state carried across line boundaries.
type LineState struct {
	InMath       bool
	InRaw        bool
	RawFenceLen  int
	CommentDepth int
	CodeDepth    int
}

// MathSpan describes a math region ($...$ or $ ... $) on a single line in
// 0-based character column coordinates (matching util.DecodeCharacter).
type MathSpan struct {
	Start, End               int
	ContentStart, ContentEnd int
	Display                  bool
}

type blockKind uint8

const (
	blockCodeBrace blockKind = iota + 1
	blockCodeParen
	blockContentBracket
)

type scanCtx struct {
	inMath       bool
	inRaw        bool
	rawFenceLen  int
	commentDepth int
	codeDepth    int
	inString     bool

	inCodeStmt       bool
	stmtBaseStackLen int
	stmtBaseCode     int
	inHashChain      bool
	hashBaseStackLen int

	mathStartCol        int
	mathContentStartCol int

	stackBuf [16]blockKind
	stack    []blockKind
}

func newScanCtx(prev LineState, trackStack bool) scanCtx {
	ctx := scanCtx{
		inMath:       prev.InMath,
		inRaw:        prev.InRaw,
		rawFenceLen:  prev.RawFenceLen,
		commentDepth: prev.CommentDepth,
		codeDepth:    prev.CodeDepth,
	}
	if trackStack {
		ctx.stack = ctx.stackBuf[:0]
	}
	return ctx
}

func (c *scanCtx) push(k blockKind) {
	c.stack = append(c.stack, k)
}

func (c *scanCtx) pop() (blockKind, bool) {
	if len(c.stack) == 0 {
		return 0, false
	}
	k := c.stack[len(c.stack)-1]
	c.stack = c.stack[:len(c.stack)-1]
	return k, true
}

func (c *scanCtx) top() (blockKind, bool) {
	if len(c.stack) == 0 {
		return 0, false
	}
	return c.stack[len(c.stack)-1], true
}

// currentMode returns the underlying Typst mode at the current scanner
// state (comments do not introduce a separate Mode value).
func (c *scanCtx) currentMode() Mode {
	if c.inRaw {
		return ModeRaw
	}
	if c.inMath {
		return ModeMath
	}
	if c.inCodeStmt && len(c.stack) == c.stmtBaseStackLen {
		return ModeCode
	}
	if c.inHashChain && len(c.stack) == c.hashBaseStackLen {
		return ModeCode
	}
	if k, ok := c.top(); ok {
		if k == blockContentBracket {
			return ModeMarkup
		}
		return ModeCode
	}
	if c.codeDepth > 0 {
		return ModeCode
	}
	return ModeMarkup
}

func (c *scanCtx) isCodeContext() bool {
	if c.inMath || c.inRaw {
		return false
	}
	if c.inCodeStmt && len(c.stack) == c.stmtBaseStackLen {
		return true
	}
	if c.inHashChain && len(c.stack) == c.hashBaseStackLen {
		return true
	}
	if k, ok := c.top(); ok {
		return k == blockCodeBrace || k == blockCodeParen
	}
	return c.codeDepth > 0
}

var minMark = rune(unicode.Mark.R16[0].Lo)

func isMark(r rune) bool {
	if r < minMark {
		return false
	}
	return unicode.In(r, unicode.Mark)
}

// decodeChar decodes one character cluster (base rune + trailing unicode.Mark
// combining runes) matching internal/util.DecodeCharacter without importing internal/util.
func decodeChar(b []byte) (rune, int) {
	if len(b) == 0 {
		return 0, 0
	}
	if b[0] < utf8.RuneSelf {
		r := rune(b[0])
		if len(b) == 1 || b[1] < utf8.RuneSelf {
			return r, 1
		}
		size := 1
		for size < len(b) {
			c, s := utf8.DecodeRune(b[size:])
			if !isMark(c) {
				break
			}
			size += s
		}
		return r, size
	}
	r, size := utf8.DecodeRune(b)
	for size < len(b) {
		c, s := utf8.DecodeRune(b[size:])
		if !isMark(c) {
			break
		}
		size += s
	}
	return r, size
}

func isIdentStart(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

func isIdentCont(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}

func isTypstKeyword(ident []byte) bool {
	switch string(ident) {
	case "let", "set", "show", "import", "include", "if", "else", "for", "while", "return", "context", "break", "continue":
		return true
	default:
		return false
	}
}

func isURLPrefix(prefix []byte) bool {
	if len(prefix) >= 5 && string(prefix[len(prefix)-5:]) == "http:" {
		if len(prefix) == 5 || !isIdentCont(prefix[len(prefix)-6]) {
			return true
		}
	}
	if len(prefix) >= 6 && string(prefix[len(prefix)-6:]) == "https:" {
		if len(prefix) == 6 || !isIdentCont(prefix[len(prefix)-7]) {
			return true
		}
	}
	return false
}

func isSpaceOrTab(b byte) bool {
	return b == ' ' || b == '\t'
}

func isWhitespaceRune(r rune) bool {
	return unicode.IsSpace(r)
}

func checkElseAhead(line []byte, idx int) bool {
	j := idx
	for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
		j++
	}
	if j+4 <= len(line) && string(line[j:j+4]) == "else" {
		if j+4 == len(line) || (!isIdentCont(line[j+4]) && line[j+4] != '-') {
			return true
		}
	}
	return false
}

// scanLine processes one line, mutating ctx in place.
// If collectSpans is true, it records MathSpans on this line.
// If queryCol >= 0, it returns the Mode at queryCol (clamped to line length).
func scanLine(ctx *scanCtx, line []byte, collectSpans bool, queryCol int) ([]MathSpan, Mode) {
	var spans []MathSpan
	queryMode := ModeMarkup
	queryResolved := false

	if ctx.inMath {
		ctx.mathStartCol = 0
		ctx.mathContentStartCol = 0
	}

	var firstContentRune rune
	hasFirstContent := false
	var lastContentRune rune
	if ctx.inMath {
		hasFirstContent = true
	}

	i := 0
	col := 0

	resolveQuery := func(m Mode) {
		if queryCol >= 0 && !queryResolved && col >= queryCol {
			queryMode = m
			queryResolved = true
		}
	}

	for i < len(line) {
		resolveQuery(ctx.currentMode())
		if queryCol >= 0 && queryResolved && !collectSpans {
			return nil, queryMode
		}

		b := line[i]

		// 1. Inside raw block/inline (`...` or ```...```)
		if ctx.inRaw {
			if b == '`' {
				runLen := 0
				for i+runLen < len(line) && line[i+runLen] == '`' {
					runLen++
				}
				if runLen >= ctx.rawFenceLen {
					for k := 0; k < runLen; k++ {
						if k > 0 {
							resolveQuery(ModeRaw)
						}
						i++
						col++
					}
					ctx.inRaw = false
					ctx.rawFenceLen = 0
					continue
				}
				for k := 0; k < runLen; k++ {
					if k > 0 {
						resolveQuery(ModeRaw)
					}
					i++
					col++
				}
				continue
			}
			_, sz := decodeChar(line[i:])
			i += sz
			col++
			continue
		}

		// 2. Inside block comment /* ... */
		if ctx.commentDepth > 0 {
			if b == '/' && i+1 < len(line) && line[i+1] == '*' {
				ctx.commentDepth++
				i++
				col++
				resolveQuery(ctx.currentMode())
				i++
				col++
				continue
			}
			if b == '*' && i+1 < len(line) && line[i+1] == '/' {
				ctx.commentDepth--
				i++
				col++
				resolveQuery(ctx.currentMode())
				i++
				col++
				continue
			}
			_, sz := decodeChar(line[i:])
			i += sz
			col++
			continue
		}

		// 3. Inside string literal "..." (only active in ModeCode or ModeMath)
		if ctx.inString {
			if b == '\\' && i+1 < len(line) {
				i++
				col++
				resolveQuery(ctx.currentMode())
				_, sz := decodeChar(line[i:])
				i += sz
				col++
				continue
			}
			if b == '"' {
				if ctx.inMath {
					lastContentRune = '"'
					if !hasFirstContent {
						firstContentRune = '"'
						hasFirstContent = true
					}
				}
				ctx.inString = false
				i++
				col++
				continue
			}
			r, sz := decodeChar(line[i:])
			if ctx.inMath {
				lastContentRune = r
				if !hasFirstContent {
					firstContentRune = r
					hasFirstContent = true
				}
			}
			i += sz
			col++
			continue
		}

		// 4. Line comment // (outside raw, comments, strings; except http:// and https:// in markup)
		if b == '/' && i+1 < len(line) && line[i+1] == '/' {
			if ctx.isCodeContext() || ctx.inMath || !isURLPrefix(line[:i]) {
				m := ctx.currentMode()
				// Advance through EOL so column count for MathSpan/EOL clamp is exact.
				for i < len(line) {
					resolveQuery(m)
					if queryCol >= 0 && queryResolved && !collectSpans {
						return nil, queryMode
					}
					_, sz := decodeChar(line[i:])
					i += sz
					col++
				}
				break
			}
		}

		// 5. Block comment start /*
		if b == '/' && i+1 < len(line) && line[i+1] == '*' {
			ctx.commentDepth++
			i++
			col++
			resolveQuery(ctx.currentMode())
			i++
			col++
			continue
		}

		// 6. Inside math mode ($...$)
		if ctx.inMath {
			if b == '\\' && i+1 < len(line) {
				i++
				col++
				resolveQuery(ModeMath)
				r, sz := decodeChar(line[i:])
				lastContentRune = r
				if !hasFirstContent {
					firstContentRune = r
					hasFirstContent = true
				}
				i += sz
				col++
				continue
			}
			if b == '"' {
				ctx.inString = true
				lastContentRune = '"'
				if !hasFirstContent {
					firstContentRune = '"'
					hasFirstContent = true
				}
				i++
				col++
				continue
			}
			if b == '$' {
				if collectSpans {
					display := false
					if ctx.mathStartCol == 0 && ctx.mathContentStartCol == 0 {
						// Continued from previous line.
						display = true
					} else if col > ctx.mathContentStartCol && isWhitespaceRune(firstContentRune) && isWhitespaceRune(lastContentRune) {
						display = true
					}
					spans = append(spans, MathSpan{
						Start:        ctx.mathStartCol,
						End:          col + 1,
						ContentStart: ctx.mathContentStartCol,
						ContentEnd:   col,
						Display:      display,
					})
				}
				ctx.inMath = false
				hasFirstContent = false
				firstContentRune = 0
				lastContentRune = 0
				i++
				col++
				continue
			}
			r, sz := decodeChar(line[i:])
			lastContentRune = r
			if !hasFirstContent {
				firstContentRune = r
				hasFirstContent = true
			}
			i += sz
			col++
			continue
		}

		// 7. Escapes in Markup (note: in Code mode outside strings, '\' is not an escape)
		if !ctx.isCodeContext() && b == '\\' && i+1 < len(line) {
			i++
			col++
			resolveQuery(ctx.currentMode())
			_, sz := decodeChar(line[i:])
			i += sz
			col++
			continue
		}

		// 8. Strings in Code mode
		if ctx.isCodeContext() && b == '"' {
			ctx.inString = true
			i++
			col++
			continue
		}

		// 9. Raw backtick spans (`...` or ```...```) in Markup or Code
		if b == '`' {
			runLen := 0
			for i+runLen < len(line) && line[i+runLen] == '`' {
				runLen++
			}
			if runLen == 2 {
				// In Typst, `` is an empty 1-backtick raw literal (open + close).
				i++
				col++
				resolveQuery(ModeRaw)
				i++
				col++
				continue
			}
			ctx.inRaw = true
			ctx.rawFenceLen = runLen
			for k := 0; k < runLen; k++ {
				if k > 0 {
					resolveQuery(ModeRaw)
				}
				i++
				col++
			}
			continue
		}

		// 10. Math opener ($)
		if b == '$' {
			ctx.inMath = true
			ctx.mathStartCol = col
			ctx.mathContentStartCol = col + 1
			hasFirstContent = false
			firstContentRune = 0
			lastContentRune = 0
			i++
			col++
			continue
		}

		// 11. Hash expression (#{, #(, #[, #ident) outside pure code or inside content/markup
		if b == '#' && i+1 < len(line) {
			nextB := line[i+1]
			if nextB == '{' {
				ctx.inHashChain = false
				i++
				col++
				resolveQuery(ModeCode)
				ctx.push(blockCodeBrace)
				ctx.codeDepth++
				i++
				col++
				continue
			}
			if nextB == '(' {
				ctx.inHashChain = false
				i++
				col++
				resolveQuery(ModeCode)
				ctx.push(blockCodeParen)
				ctx.codeDepth++
				i++
				col++
				continue
			}
			if nextB == '[' {
				ctx.inHashChain = false
				i++
				col++
				resolveQuery(ModeCode)
				ctx.push(blockContentBracket)
				i++
				col++
				continue
			}
			if isIdentStart(nextB) {
				i++
				col++
				identStart := i
				for i < len(line) {
					c := line[i]
					if isIdentCont(c) {
						resolveQuery(ModeCode)
						i++
						col++
					} else if c == '-' && i+1 < len(line) && isIdentCont(line[i+1]) {
						resolveQuery(ModeCode)
						i++
						col++
					} else {
						break
					}
				}
				ident := line[identStart:i]
				if isTypstKeyword(ident) {
					ctx.inCodeStmt = true
					ctx.stmtBaseStackLen = len(ctx.stack)
					ctx.stmtBaseCode = ctx.codeDepth
					ctx.inHashChain = false
				} else {
					ctx.hashBaseStackLen = len(ctx.stack)
					ctx.inHashChain = hasChainContinuation(line, i)
				}
				continue
			}
		}

		// 12. Delimiters in Code or Content blocks
		if ctx.isCodeContext() {
			if b == '{' {
				ctx.push(blockCodeBrace)
				ctx.codeDepth++
				i++
				col++
				continue
			}
			if b == '(' {
				ctx.push(blockCodeParen)
				ctx.codeDepth++
				i++
				col++
				continue
			}
			if b == '[' {
				ctx.push(blockContentBracket)
				i++
				col++
				continue
			}
			if b == '}' || b == ')' {
				if k, ok := ctx.top(); ok {
					if (b == '}' && k == blockCodeBrace) || (b == ')' && k == blockCodeParen) {
						ctx.pop()
						if ctx.codeDepth > 0 {
							ctx.codeDepth--
						}
						if ctx.inCodeStmt && len(ctx.stack) <= ctx.stmtBaseStackLen && ctx.codeDepth <= ctx.stmtBaseCode {
							if b == '}' && !checkElseAhead(line, i+1) {
								ctx.inCodeStmt = false
							}
						}
						if ctx.inHashChain && len(ctx.stack) == ctx.hashBaseStackLen {
							ctx.inHashChain = hasChainContinuation(line, i+1)
						}
					}
				} else if ctx.codeDepth > 0 {
					ctx.codeDepth--
					if ctx.inCodeStmt && ctx.codeDepth <= ctx.stmtBaseCode {
						if b == '}' && !checkElseAhead(line, i+1) {
							ctx.inCodeStmt = false
						}
					}
				}
				i++
				col++
				continue
			}
			if b == ';' {
				if ctx.inCodeStmt && len(ctx.stack) == ctx.stmtBaseStackLen && ctx.codeDepth == ctx.stmtBaseCode {
					ctx.inCodeStmt = false
				}
				ctx.inHashChain = false
				i++
				col++
				continue
			}
			if ctx.inHashChain && len(ctx.stack) == ctx.hashBaseStackLen {
				// Inside a postfix chain like #sym.arrow.r or #emph[a][b]
				if b == '.' && i+1 < len(line) && isIdentStart(line[i+1]) {
					i++
					col++
					for i < len(line) {
						c := line[i]
						if isIdentCont(c) || (c == '-' && i+1 < len(line) && isIdentCont(line[i+1])) {
							resolveQuery(ModeCode)
							i++
							col++
						} else {
							break
						}
					}
					ctx.inHashChain = hasChainContinuation(line, i)
					continue
				}
				ctx.inHashChain = false
				continue
			}
			if b == ']' {
				// Closing an outer content bracket if #stmt was opened inside it.
				if k, ok := ctx.top(); ok && k == blockContentBracket {
					ctx.pop()
					ctx.inCodeStmt = false
					ctx.inHashChain = false
					i++
					col++
					continue
				}
			}
		} else {
			// Markup / content block context
			if k, ok := ctx.top(); ok && k == blockContentBracket {
				if b == '[' {
					ctx.push(blockContentBracket)
					i++
					col++
					continue
				}
				if b == ']' {
					ctx.pop()
					if ctx.inCodeStmt && len(ctx.stack) == ctx.stmtBaseStackLen && ctx.codeDepth == ctx.stmtBaseCode {
						if !checkElseAhead(line, i+1) {
							ctx.inCodeStmt = false
						}
					}
					if ctx.inHashChain && len(ctx.stack) == ctx.hashBaseStackLen {
						if !hasChainContinuation(line, i+1) {
							ctx.inHashChain = false
						}
					}
					i++
					col++
					continue
				}
			}
		}

		// Default: consume one character cluster.
		_, sz := decodeChar(line[i:])
		i += sz
		col++
	}

	if ctx.inMath && collectSpans {
		spans = append(spans, MathSpan{
			Start:        ctx.mathStartCol,
			End:          col,
			ContentStart: ctx.mathContentStartCol,
			ContentEnd:   col,
			Display:      true,
		})
	}

	if !ctx.inString && ctx.commentDepth == 0 {
		ctx.inCodeStmt = false
	}
	ctx.inHashChain = false

	resolveQuery(ctx.currentMode())
	if queryCol >= 0 && !queryResolved {
		queryMode = ctx.currentMode()
	}

	return spans, queryMode
}

func hasChainContinuation(line []byte, idx int) bool {
	if idx >= len(line) {
		return false
	}
	b := line[idx]
	if b == '(' || b == '[' {
		return true
	}
	if b == '.' && idx+1 < len(line) && isIdentStart(line[idx+1]) {
		return true
	}
	return false
}

// ScanLineState advances LineState across a single line and returns all MathSpans
// found on that line (with 0-based character columns matching util.DecodeCharacter).
func ScanLineState(prev LineState, line []byte) (next LineState, mathSpans []MathSpan) {
	ctx := newScanCtx(prev, true)
	spans, _ := scanLine(&ctx, line, true, -1)
	next = LineState{
		InMath:       ctx.inMath,
		InRaw:        ctx.inRaw,
		RawFenceLen:  ctx.rawFenceLen,
		CommentDepth: ctx.commentDepth,
		CodeDepth:    ctx.codeDepth,
	}
	return next, spans
}

// DetectModeAtLine returns the Typst Mode at col on a single line given its
// entering LineState (avoiding O(line) rescans when callers cache LineState
// prefixes).
func DetectModeAtLine(prev LineState, line []byte, col int) Mode {
	if col < 0 {
		col = 0
	}
	ctx := newScanCtx(prev, true)
	_, m := scanLine(&ctx, line, false, col)
	return m
}

// DetectModeAt returns the Typst Mode at (line, col) in src, where col is a
// 0-based character index matching buffer.Loc.X / util.DecodeCharacter.
func DetectModeAt(src LineSource, line, col int) Mode {
	if src == nil {
		return ModeMarkup
	}
	n := src.LinesNum()
	if n <= 0 || line < 0 || line >= n {
		return ModeMarkup
	}
	if col < 0 {
		col = 0
	}

	ctx := newScanCtx(LineState{}, true)
	for l := 0; l < line; l++ {
		scanLine(&ctx, src.LineBytes(l), false, -1)
	}
	_, m := scanLine(&ctx, src.LineBytes(line), false, col)
	return m
}
