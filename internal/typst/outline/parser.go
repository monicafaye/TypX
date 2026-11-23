package outline

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/monicafaye/TypX/internal/typst"
)

// ItemKind identifies the structural element type in a Typst document outline.
type ItemKind int

const (
	KindHeading ItemKind = iota
	KindFigure
	KindLabel
)

// String returns a readable name for the ItemKind.
func (k ItemKind) String() string {
	switch k {
	case KindHeading:
		return "heading"
	case KindFigure:
		return "figure"
	case KindLabel:
		return "label"
	default:
		return "unknown"
	}
}

// Item represents a single navigable entry in the Document Outline.
type Item struct {
	Kind   ItemKind
	Level  int    // 1..6 for headings, 0 for figures and labels
	Title  string // Cleaned display text
	Label  string // Optional attached <label> name (without angle brackets)
	Line   int    // 0-based buffer line number
	Column int    // 0-based rune column on Line
}

// ParseOptions controls which optional elements the outline parser emits.
type ParseOptions struct {
	// IncludeLabels emits standalone <label-name> anchors as KindLabel items.
	IncludeLabels bool
}

// Symbol is a decoupled representation of an LSP DocumentSymbol used to enrich
// local outline items when tinymist is connected.
type Symbol struct {
	Name     string
	Kind     int
	Line     int
	Column   int
	Children []Symbol
}

const (
	figureLookaheadLines  = 15
	headingLookaheadLines = 3

	// LSP SymbolKind constants relevant to Typst headings/sections.
	lspSymbolKindFile      = 1
	lspSymbolKindModule    = 2
	lspSymbolKindNamespace = 3
	lspSymbolKindPackage   = 4
	lspSymbolKindString    = 15
	lspSymbolKindObject    = 19
)

type lexState struct {
	inRaw        bool
	rawFenceLen  int
	commentDepth int
	inMath       bool
}

type labelPos struct {
	line int
	col  int
}

type cachedLine struct {
	endState      lexState
	items         []Item
	attachedLabel []labelPos
	hasFigure     bool
	hasHashHead   bool
	spanEndLine   int
}

// IncrementalParser caches per-line lexical state and extracted items so buffer
// edits only re-scan from the earliest affected line forward.
type IncrementalParser struct {
	lines    []cachedLine
	lastOpts ParseOptions
	valid    bool
}

// NewIncrementalParser returns a fresh IncrementalParser.
func NewIncrementalParser() *IncrementalParser {
	return &IncrementalParser{}
}

// Reset discards cached state so the next Update performs a full scan.
func (p *IncrementalParser) Reset() {
	p.lines = nil
	p.valid = false
}

// Parse performs a full stateless parse of src and returns all outline items in
// document order.
func Parse(src typst.LineSource, opts ParseOptions) []Item {
	var p IncrementalParser
	return p.Update(src, 0, opts)
}

// ParseString is a convenience wrapper around Parse for string inputs.
func ParseString(text string, opts ParseOptions) []Item {
	if text == "" {
		return nil
	}
	rawLines := strings.Split(text, "\n")
	lines := make([][]byte, len(rawLines))
	for i, l := range rawLines {
		l = strings.TrimSuffix(l, "\r")
		lines[i] = []byte(l)
	}
	return Parse(typst.SliceSource(lines), opts)
}

// Update incrementally updates the outline from startLine onward and returns
// the full sorted slice of outline items. When startLine <= 0 or options change,
// it performs a full scan.
func (p *IncrementalParser) Update(src typst.LineSource, startLine int, opts ParseOptions) []Item {
	if src == nil {
		p.Reset()
		return nil
	}
	n := src.LinesNum()
	if n <= 0 {
		p.Reset()
		return nil
	}

	fullRescan := !p.valid || p.lastOpts != opts || startLine <= 0 || len(p.lines) == 0
	sameLineCount := !fullRescan && len(p.lines) == n

	reparseFrom := 0
	if !fullRescan {
		reparseFrom = startLine
		if reparseFrom > len(p.lines) {
			reparseFrom = len(p.lines)
		}
		if reparseFrom > n {
			reparseFrom = n
		}
		// Back up if a prior multi-line #figure or #heading could span across startLine.
		lookback := reparseFrom - figureLookaheadLines
		if lookback < 0 {
			lookback = 0
		}
		for i := reparseFrom - 1; i >= lookback; i-- {
			cl := p.lines[i]
			if cl.hasFigure && (cl.spanEndLine >= startLine || i+figureLookaheadLines >= startLine) {
				reparseFrom = i
			} else if cl.hasHashHead && i+headingLookaheadLines >= startLine {
				reparseFrom = i
			}
		}
	}

	if fullRescan || len(p.lines) != n {
		next := make([]cachedLine, n)
		if !fullRescan && reparseFrom > 0 {
			copy(next[:reparseFrom], p.lines[:reparseFrom])
		}
		p.lines = next
	}

	var st lexState
	if reparseFrom > 0 {
		st = p.lines[reparseFrom-1].endState
	}

	for lineIdx := reparseFrom; lineIdx < n; lineIdx++ {
		prevEnd := p.lines[lineIdx].endState
		cl := scanOutlineLine(src, lineIdx, st, opts)
		p.lines[lineIdx] = cl
		st = cl.endState

		// When the total line count is unchanged and we are past startLine,
		// once lexical state converges and we're not inside a multi-line scan
		// crossing lineIdx, remaining cached lines are guaranteed valid.
		if sameLineCount && lineIdx >= startLine && cl.endState == prevEnd && cl.spanEndLine <= lineIdx {
			break
		}
	}

	p.lastOpts = opts
	p.valid = true

	return p.assemble(opts)
}

func (p *IncrementalParser) assemble(opts ParseOptions) []Item {
	var attached map[labelPos]bool
	if opts.IncludeLabels {
		for _, cl := range p.lines {
			for _, pos := range cl.attachedLabel {
				if attached == nil {
					attached = make(map[labelPos]bool)
				}
				attached[pos] = true
			}
		}
	}

	var out []Item
	for _, cl := range p.lines {
		for _, it := range cl.items {
			if it.Kind == KindLabel && attached != nil && attached[labelPos{line: it.Line, col: it.Column}] {
				continue
			}
			out = append(out, it)
		}
	}
	return out
}

// scanOutlineLine scans a single line starting in lexical state st, returning
// the updated end-of-line lexical state and any outline items rooted on lineIdx.
func scanOutlineLine(src typst.LineSource, lineIdx int, st lexState, opts ParseOptions) cachedLine {
	line := trimCR(src.LineBytes(lineIdx))
	cl := cachedLine{spanEndLine: lineIdx}

	startedClean := !st.inRaw && st.commentDepth == 0 && !st.inMath

	// Check for markup heading `^\s*(=+)\s+(.+)$` when starting outside raw/comment/math.
	headingMatched := false
	if startedClean {
		if eqStart, eqCol, level, afterSpaceIdx, ok := matchMarkupHeadingPrefix(line); ok {
			headingMatched = true
			title, endSt, attachedLbl, attachedCol := scanHeadingRest(line, afterSpaceIdx, st)
			cl.endState = endSt
			if title != "" {
				cl.items = append(cl.items, Item{
					Kind:   KindHeading,
					Level:  level,
					Title:  title,
					Label:  attachedLbl,
					Line:   lineIdx,
					Column: eqCol,
				})
				if attachedLbl != "" && attachedCol >= 0 {
					cl.attachedLabel = append(cl.attachedLabel, labelPos{line: lineIdx, col: attachedCol})
				}
			}
			_ = eqStart
			return cl
		}
	}
	_ = headingMatched

	// General single-line lexical pass extracting #figure(...), #heading(...),
	// and standalone <label> anchors while updating st accurately.
	inString := false
	i := 0
	col := 0

	for i < len(line) {
		b := line[i]

		// 1. Inside raw span/block (`...` or ```...```).
		if st.inRaw {
			if b == '`' {
				runLen := countBackticks(line, i)
				i += runLen
				col += runLen
				if runLen >= st.rawFenceLen {
					st.inRaw = false
					st.rawFenceLen = 0
				}
				continue
			}
			_, sz := utf8.DecodeRune(line[i:])
			i += sz
			col++
			continue
		}

		// 2. Inside block comment /* ... */ (supports arbitrary nesting).
		if st.commentDepth > 0 {
			if b == '/' && i+1 < len(line) && line[i+1] == '*' {
				st.commentDepth++
				i += 2
				col += 2
				continue
			}
			if b == '*' && i+1 < len(line) && line[i+1] == '/' {
				st.commentDepth--
				i += 2
				col += 2
				continue
			}
			_, sz := utf8.DecodeRune(line[i:])
			i += sz
			col++
			continue
		}

		// 3. Inside string literal "..." (in math or code arguments).
		if inString {
			if b == '\\' && i+1 < len(line) {
				i++
				col++
				_, sz := utf8.DecodeRune(line[i:])
				i += sz
				col++
				continue
			}
			if b == '"' {
				inString = false
				i++
				col++
				continue
			}
			_, sz := utf8.DecodeRune(line[i:])
			i += sz
			col++
			continue
		}

		// 4. Line comment // (preserving http:// and https:// URLs).
		if b == '/' && i+1 < len(line) && line[i+1] == '/' {
			if st.inMath || !isURLPrefix(line[:i]) {
				break
			}
		}

		// 5. Block comment opener /*.
		if b == '/' && i+1 < len(line) && line[i+1] == '*' {
			st.commentDepth++
			i += 2
			col += 2
			continue
		}

		// 6. Inside math mode ($...$).
		if st.inMath {
			if b == '\\' && i+1 < len(line) {
				i++
				col++
				_, sz := utf8.DecodeRune(line[i:])
				i += sz
				col++
				continue
			}
			if b == '"' {
				inString = true
				i++
				col++
				continue
			}
			if b == '$' {
				st.inMath = false
				i++
				col++
				continue
			}
			_, sz := utf8.DecodeRune(line[i:])
			i += sz
			col++
			continue
		}

		// 7. Markup backslash escape (\$, \`, \<, \#, etc.).
		if b == '\\' && i+1 < len(line) {
			i++
			col++
			_, sz := utf8.DecodeRune(line[i:])
			i += sz
			col++
			continue
		}

		// 8. Raw backtick opener (` or ```...).
		if b == '`' {
			runLen := countBackticks(line, i)
			i += runLen
			col += runLen
			if runLen != 2 { // `` is an empty inline raw literal in Typst.
				st.inRaw = true
				st.rawFenceLen = runLen
			}
			continue
		}

		// 9. Math opener ($).
		if b == '$' {
			st.inMath = true
			i++
			col++
			continue
		}

		// 10. #figure(...) and #heading(...) constructs.
		if b == '#' && i+1 < len(line) {
			if matchKeywordAt(line, i+1, "figure") {
				j := skipSpaces(line, i+1+len("figure"))
				if j < len(line) && line[j] == '(' {
					cl.hasFigure = true
					figItem, endLine, attachedPos := parseFigureBlock(src, lineIdx, j, col)
					if endLine > cl.spanEndLine {
						cl.spanEndLine = endLine
					}
					cl.items = append(cl.items, figItem)
					if attachedPos != nil {
						cl.attachedLabel = append(cl.attachedLabel, *attachedPos)
					}
					i += 1 + len("figure")
					col += 1 + len("figure")
					continue
				}
			} else if matchKeywordAt(line, i+1, "heading") {
				j := skipSpaces(line, i+1+len("heading"))
				if j < len(line) && (line[j] == '(' || line[j] == '[') {
					cl.hasHashHead = true
					if headItem, endLine, attachedPos, ok := parseHashHeading(src, lineIdx, j, col); ok {
						if endLine > cl.spanEndLine {
							cl.spanEndLine = endLine
						}
						cl.items = append(cl.items, headItem)
						if attachedPos != nil {
							cl.attachedLabel = append(cl.attachedLabel, *attachedPos)
						}
					}
					i += 1 + len("heading")
					col += 1 + len("heading")
					continue
				}
			}
		}

		// 11. Standalone <label-name> anchors (when enabled).
		if opts.IncludeLabels && b == '<' {
			if name, endIdx, ok := matchLabelAt(line, i); ok {
				cl.items = append(cl.items, Item{
					Kind:   KindLabel,
					Level:  0,
					Title:  name,
					Label:  name,
					Line:   lineIdx,
					Column: col,
				})
				advance := endIdx - i
				i = endIdx
				col += advance
				continue
			}
		}

		_, sz := utf8.DecodeRune(line[i:])
		i += sz
		col++
	}

	cl.endState = st
	return cl
}

func trimCR(b []byte) []byte {
	return bytes.TrimSuffix(b, []byte{'\r'})
}

func countBackticks(line []byte, idx int) int {
	n := 0
	for idx+n < len(line) && line[idx+n] == '`' {
		n++
	}
	return n
}

func skipSpaces(line []byte, idx int) int {
	for idx < len(line) && (line[idx] == ' ' || line[idx] == '\t') {
		idx++
	}
	return idx
}

func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}

func matchKeywordAt(line []byte, idx int, kw string) bool {
	if idx+len(kw) > len(line) {
		return false
	}
	for k := 0; k < len(kw); k++ {
		if line[idx+k] != kw[k] {
			return false
		}
	}
	end := idx + len(kw)
	if end < len(line) && isIdentChar(line[end]) {
		return false
	}
	return true
}

func isURLPrefix(prefix []byte) bool {
	if len(prefix) >= 5 && bytes.Equal(prefix[len(prefix)-5:], []byte("http:")) {
		if len(prefix) == 5 || !isIdentChar(prefix[len(prefix)-6]) {
			return true
		}
	}
	if len(prefix) >= 6 && bytes.Equal(prefix[len(prefix)-6:], []byte("https:")) {
		if len(prefix) == 6 || !isIdentChar(prefix[len(prefix)-7]) {
			return true
		}
	}
	return false
}

// matchMarkupHeadingPrefix checks whether line matches `^\s*(=+)\s+\S` with 1..6 '='.
func matchMarkupHeadingPrefix(line []byte) (eqByteIdx, eqCol, level, afterSpaceIdx int, ok bool) {
	i := 0
	col := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
		col++
	}
	if i >= len(line) || line[i] != '=' {
		return 0, 0, 0, 0, false
	}
	eqByteIdx = i
	eqCol = col
	for i < len(line) && line[i] == '=' {
		i++
		level++
	}
	if level < 1 || level > 6 {
		return 0, 0, 0, 0, false
	}
	// Typst requires whitespace after the '=' sequence for a heading.
	if i >= len(line) || (line[i] != ' ' && line[i] != '\t') {
		return 0, 0, 0, 0, false
	}
	afterSpaceIdx = skipSpaces(line, i)
	if afterSpaceIdx >= len(line) {
		return 0, 0, 0, 0, false
	}
	return eqByteIdx, eqCol, level, afterSpaceIdx, true
}

// scanHeadingRest scans the title portion of a markup heading line, stripping
// inline `/* ... */` comments, trailing `//` comments, and any trailing `<label>`,
// while accurately updating lexState if the line opens an unclosed raw/comment/math block.
func scanHeadingRest(line []byte, startIdx int, st lexState) (title string, endSt lexState, label string, labelCol int) {
	labelCol = -1
	var buf strings.Builder
	// Keep track of rune positions for potential trailing <label>.
	type labelMatch struct {
		name      string
		bufStart  int
		bufEnd    int
		runeCol   int
		afterOnly bool
	}
	var lastLabel *labelMatch

	inString := false
	i := startIdx
	col := utf8.RuneCount(line[:startIdx])

	for i < len(line) {
		b := line[i]

		if st.inRaw {
			if b == '`' {
				runLen := countBackticks(line, i)
				buf.Write(line[i : i+runLen])
				i += runLen
				col += runLen
				if runLen >= st.rawFenceLen {
					st.inRaw = false
					st.rawFenceLen = 0
				}
				continue
			}
			r, sz := utf8.DecodeRune(line[i:])
			buf.WriteRune(r)
			i += sz
			col++
			continue
		}

		if st.commentDepth > 0 {
			if b == '/' && i+1 < len(line) && line[i+1] == '*' {
				st.commentDepth++
				i += 2
				col += 2
				continue
			}
			if b == '*' && i+1 < len(line) && line[i+1] == '/' {
				st.commentDepth--
				i += 2
				col += 2
				continue
			}
			_, sz := utf8.DecodeRune(line[i:])
			i += sz
			col++
			continue
		}

		if inString {
			if b == '\\' && i+1 < len(line) {
				buf.WriteByte(b)
				i++
				col++
				r, sz := utf8.DecodeRune(line[i:])
				buf.WriteRune(r)
				i += sz
				col++
				continue
			}
			if b == '"' {
				inString = false
				buf.WriteByte('"')
				i++
				col++
				continue
			}
			r, sz := utf8.DecodeRune(line[i:])
			buf.WriteRune(r)
			i += sz
			col++
			continue
		}

		if b == '/' && i+1 < len(line) && line[i+1] == '/' {
			if st.inMath || !isURLPrefix(line[:i]) {
				break
			}
		}

		if b == '/' && i+1 < len(line) && line[i+1] == '*' {
			st.commentDepth++
			i += 2
			col += 2
			continue
		}

		if st.inMath {
			if b == '\\' && i+1 < len(line) {
				buf.WriteByte(b)
				i++
				col++
				r, sz := utf8.DecodeRune(line[i:])
				buf.WriteRune(r)
				i += sz
				col++
				continue
			}
			if b == '"' {
				inString = true
				buf.WriteByte('"')
				i++
				col++
				continue
			}
			if b == '$' {
				st.inMath = false
				buf.WriteByte('$')
				i++
				col++
				continue
			}
			r, sz := utf8.DecodeRune(line[i:])
			buf.WriteRune(r)
			i += sz
			col++
			continue
		}

		if b == '\\' && i+1 < len(line) {
			buf.WriteByte(b)
			i++
			col++
			r, sz := utf8.DecodeRune(line[i:])
			buf.WriteRune(r)
			i += sz
			col++
			continue
		}

		if b == '`' {
			runLen := countBackticks(line, i)
			buf.Write(line[i : i+runLen])
			i += runLen
			col += runLen
			if runLen != 2 {
				st.inRaw = true
				st.rawFenceLen = runLen
			}
			continue
		}

		if b == '$' {
			st.inMath = true
			buf.WriteByte('$')
			i++
			col++
			continue
		}

		if b == '<' {
			if name, endIdx, ok := matchLabelAt(line, i); ok {
				bStart := buf.Len()
				buf.Write(line[i:endIdx])
				lastLabel = &labelMatch{
					name:     name,
					bufStart: bStart,
					bufEnd:   buf.Len(),
					runeCol:  col,
				}
				advance := endIdx - i
				i = endIdx
				col += advance
				continue
			}
		}

		r, sz := utf8.DecodeRune(line[i:])
		buf.WriteRune(r)
		i += sz
		col++
	}

	rawTitle := buf.String()
	// If the cleaned title ends with a <label> (ignoring trailing spaces), strip
	// that trailing label into metadata.
	if lastLabel != nil && strings.TrimSpace(rawTitle[lastLabel.bufEnd:]) == "" {
		before := strings.TrimSpace(rawTitle[:lastLabel.bufStart])
		if before != "" {
			rawTitle = before
			label = lastLabel.name
			labelCol = lastLabel.runeCol
		}
	}

	return collapseSpaces(rawTitle), st, label, labelCol
}

func isLabelChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') || b == '_' || b == '-' || b == ':' || b == '.'
}

// matchLabelAt checks whether line[idx:] starts with a valid Typst `<label>`.
// Rejects empty `<>`, whitespace inside `<...>`, and URLs like `<https://...>`.
func matchLabelAt(line []byte, idx int) (name string, endIdx int, ok bool) {
	if idx >= len(line) || line[idx] != '<' {
		return "", 0, false
	}
	if idx > 0 && line[idx-1] == '\\' {
		return "", 0, false
	}
	j := idx + 1
	if j >= len(line) || !isLabelChar(line[j]) || line[j] == '.' || line[j] == ':' {
		return "", 0, false
	}
	for j < len(line) && isLabelChar(line[j]) {
		j++
	}
	if j >= len(line) || line[j] != '>' {
		return "", 0, false
	}
	cand := string(line[idx+1 : j])
	if cand == "" || strings.Contains(cand, "://") {
		return "", 0, false
	}
	return cand, j + 1, true
}

func collapseSpaces(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	inSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
		} else {
			b.WriteRune(r)
			inSpace = false
		}
	}
	return b.String()
}

// parseFigureBlock scans up to figureLookaheadLines starting from the opening
// '(' of `#figure(` to extract a caption (from `caption: [...]` or `caption: "..."`)
// and any trailing `<label>` immediately following the closing ')'.
func parseFigureBlock(src typst.LineSource, startLine, openParenIdx, figCol int) (Item, int, *labelPos) {
	maxLine := startLine + figureLookaheadLines
	if total := src.LinesNum(); maxLine >= total {
		maxLine = total - 1
	}

	parenDepth := 0
	bracketDepth := 0
	inRaw := false
	rawFenceLen := 0
	commentDepth := 0
	inString := false

	capturingCaption := false
	expectCaptionVal := false
	captionMode := byte(0) // '[' or '"'
	captionBracketDepth := 0
	var captionBuf strings.Builder
	captionDone := false

	endLine := startLine
	trailingLabel := ""
	var attachedPos *labelPos

outer:
	for l := startLine; l <= maxLine; l++ {
		endLine = l
		line := trimCR(src.LineBytes(l))
		i := 0
		if l == startLine {
			i = openParenIdx
		}

		for i < len(line) {
			b := line[i]

			if inRaw {
				if b == '`' {
					runLen := countBackticks(line, i)
					if capturingCaption && captionMode == '[' {
						captionBuf.Write(line[i : i+runLen])
					}
					i += runLen
					if runLen >= rawFenceLen {
						inRaw = false
						rawFenceLen = 0
					}
					continue
				}
				r, sz := utf8.DecodeRune(line[i:])
				if capturingCaption && captionMode == '[' {
					captionBuf.WriteRune(r)
				}
				i += sz
				continue
			}

			if commentDepth > 0 {
				if b == '/' && i+1 < len(line) && line[i+1] == '*' {
					commentDepth++
					i += 2
					continue
				}
				if b == '*' && i+1 < len(line) && line[i+1] == '/' {
					commentDepth--
					i += 2
					continue
				}
				_, sz := utf8.DecodeRune(line[i:])
				i += sz
				continue
			}

			if inString {
				if b == '\\' && i+1 < len(line) {
					nextB := line[i+1]
					if capturingCaption {
						switch nextB {
						case 'n', 'r', 't':
							captionBuf.WriteByte(' ')
						default:
							captionBuf.WriteByte(nextB)
						}
					}
					i += 2
					continue
				}
				if b == '"' {
					inString = false
					if capturingCaption && captionMode == '"' {
						capturingCaption = false
						captionDone = true
					}
					i++
					continue
				}
				r, sz := utf8.DecodeRune(line[i:])
				if capturingCaption {
					captionBuf.WriteRune(r)
				}
				i += sz
				continue
			}

			if b == '/' && i+1 < len(line) && line[i+1] == '/' {
				if !isURLPrefix(line[:i]) {
					break
				}
			}

			if b == '/' && i+1 < len(line) && line[i+1] == '*' {
				commentDepth++
				i += 2
				continue
			}

			if b == '`' {
				runLen := countBackticks(line, i)
				if capturingCaption && captionMode == '[' {
					captionBuf.Write(line[i : i+runLen])
				}
				i += runLen
				if runLen != 2 {
					inRaw = true
					rawFenceLen = runLen
				}
				continue
			}

			if capturingCaption && captionMode == '[' {
				if b == '\\' && i+1 < len(line) {
					r, sz := utf8.DecodeRune(line[i+1:])
					captionBuf.WriteRune(r)
					i += 1 + sz
					continue
				}
				if b == '[' {
					captionBracketDepth++
					bracketDepth++
					captionBuf.WriteByte('[')
					i++
					continue
				}
				if b == ']' {
					captionBracketDepth--
					bracketDepth--
					if captionBracketDepth == 0 {
						capturingCaption = false
						captionDone = true
					} else {
						captionBuf.WriteByte(']')
					}
					i++
					continue
				}
				r, sz := utf8.DecodeRune(line[i:])
				captionBuf.WriteRune(r)
				i += sz
				continue
			}

			if expectCaptionVal {
				if b == ' ' || b == '\t' {
					i++
					continue
				}
				if b == '[' {
					expectCaptionVal = false
					capturingCaption = true
					captionMode = '['
					captionBracketDepth = 1
					bracketDepth++
					i++
					continue
				}
				if b == '"' {
					expectCaptionVal = false
					capturingCaption = true
					captionMode = '"'
					inString = true
					i++
					continue
				}
				// Support `figure.caption[...]` or `figure.caption(...)`.
				if matchKeywordAt(line, i, "figure") {
					rest := line[i:]
					if bytes.HasPrefix(rest, []byte("figure.caption")) {
						i += len("figure.caption")
						continue
					}
				}
				if b == '(' {
					parenDepth++
					i++
					continue
				}
				// Unknown expression (e.g. variable reference) — stop waiting for literal caption.
				expectCaptionVal = false
			}

			if b == '"' {
				inString = true
				i++
				continue
			}

			if b == '(' {
				parenDepth++
				i++
				continue
			}

			if b == ')' {
				parenDepth--
				i++
				if parenDepth == 0 {
					// Check for trailing `<label>` on the closing line.
					j := skipSpaces(line, i)
					if lbl, _, ok := matchLabelAt(line, j); ok {
						trailingLabel = lbl
						attachedPos = &labelPos{
							line: l,
							col:  utf8.RuneCount(line[:j]),
						}
					}
					break outer
				}
				continue
			}

			if b == '[' {
				bracketDepth++
				i++
				continue
			}

			if b == ']' {
				if bracketDepth > 0 {
					bracketDepth--
				}
				i++
				continue
			}

			// Detect `caption:` argument at top level of #figure(...).
			if !captionDone && parenDepth == 1 && bracketDepth == 0 && b == 'c' {
				if (i == 0 || !isIdentChar(line[i-1])) && matchKeywordAt(line, i, "caption") {
					j := skipSpaces(line, i+len("caption"))
					if j < len(line) && line[j] == ':' {
						expectCaptionVal = true
						i = j + 1
						continue
					}
				}
			}

			_, sz := utf8.DecodeRune(line[i:])
			i += sz
		}

		// Preserve word boundary across multi-line caption content.
		if capturingCaption && captionMode == '[' {
			captionBuf.WriteByte(' ')
		}
	}

	title := collapseSpaces(captionBuf.String())
	if title == "" {
		title = fmt.Sprintf("Figure (line %d)", startLine+1)
	}

	return Item{
		Kind:   KindFigure,
		Level:  0,
		Title:  title,
		Label:  trailingLabel,
		Line:   startLine,
		Column: figCol,
	}, endLine, attachedPos
}

// parseHashHeading parses `#heading[Title]` or `#heading(level: 2)[Title]` / `#heading(level: 2, [Title])`.
// Skips headings with `outlined: false`.
func parseHashHeading(src typst.LineSource, startLine, argStartIdx, headCol int) (Item, int, *labelPos, bool) {
	maxLine := startLine + headingLookaheadLines
	if total := src.LinesNum(); maxLine >= total {
		maxLine = total - 1
	}

	var combined strings.Builder
	type lineOffset struct {
		line  int
		start int
		raw   []byte
	}
	var offsets []lineOffset
	for l := startLine; l <= maxLine; l++ {
		raw := trimCR(src.LineBytes(l))
		slice := raw
		if l == startLine {
			slice = raw[argStartIdx:]
		}
		offsets = append(offsets, lineOffset{line: l, start: combined.Len(), raw: raw})
		combined.Write(slice)
		combined.WriteByte('\n')
	}

	s := combined.String()
	level := 1
	outlined := true
	pos := 0
	title := ""

	// Optional (...) argument list.
	if pos < len(s) && s[pos] == '(' {
		endParen := findMatchingDelim(s, pos, '(', ')')
		if endParen < 0 {
			return Item{}, startLine, nil, false
		}
		args := s[pos+1 : endParen]
		if hasOutlinedFalse(args) {
			outlined = false
		}
		if lv, ok := extractNamedIntArg(args, "level"); ok {
			if lv < 1 {
				lv = 1
			} else if lv > 6 {
				lv = 6
			}
			level = lv
		}
		// Check if body content [...] or "..." is passed directly inside (...).
		if body, ok := extractBracketContent(args); ok {
			title = collapseSpaces(body)
		}
		pos = endParen + 1
	}

	if !outlined {
		return Item{}, startLine, nil, false
	}

	// Optional trailing `[...]` content block (e.g. `#heading(level: 2)[Preface]`).
	for pos < len(s) && (s[pos] == ' ' || s[pos] == '\t') {
		pos++
	}
	if pos < len(s) && s[pos] == '[' {
		endBr := findMatchingDelim(s, pos, '[', ']')
		if endBr > pos {
			title = collapseSpaces(s[pos+1 : endBr])
			pos = endBr + 1
		}
	}

	if title == "" {
		return Item{}, startLine, nil, false
	}

	// Locate end line and optional trailing <label>.
	endLine := startLine
	var attachedPos *labelPos
	trailingLabel := ""
	for pos < len(s) && (s[pos] == ' ' || s[pos] == '\t') {
		pos++
	}
	for idx := len(offsets) - 1; idx >= 0; idx-- {
		if pos >= offsets[idx].start {
			endLine = offsets[idx].line
			break
		}
	}
	if pos < len(s) && s[pos] == '<' {
		if lbl, _, ok := matchLabelAt([]byte(s), pos); ok {
			trailingLabel = lbl
			// Compute column on endLine.
			for _, off := range offsets {
				if off.line == endLine {
					rel := pos - off.start
					if endLine == startLine {
						rel += argStartIdx
					}
					if rel >= 0 && rel <= len(off.raw) {
						attachedPos = &labelPos{
							line: endLine,
							col:  utf8.RuneCount(off.raw[:rel]),
						}
					}
					break
				}
			}
		}
	}

	return Item{
		Kind:   KindHeading,
		Level:  level,
		Title:  title,
		Label:  trailingLabel,
		Line:   startLine,
		Column: headCol,
	}, endLine, attachedPos, true
}

func findMatchingDelim(s string, openIdx int, openCh, closeCh byte) int {
	depth := 0
	inStr := false
	for i := openIdx; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			continue
		}
		if c == openCh {
			depth++
		} else if c == closeCh {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func hasOutlinedFalse(args string) bool {
	idx := strings.Index(args, "outlined")
	if idx < 0 {
		return false
	}
	rest := strings.TrimSpace(args[idx+len("outlined"):])
	if !strings.HasPrefix(rest, ":") {
		return false
	}
	val := strings.TrimSpace(rest[1:])
	return strings.HasPrefix(val, "false")
}

func extractNamedIntArg(args, name string) (int, bool) {
	idx := strings.Index(args, name)
	if idx < 0 {
		return 0, false
	}
	if idx > 0 && isIdentChar(args[idx-1]) {
		return 0, false
	}
	rest := strings.TrimSpace(args[idx+len(name):])
	if !strings.HasPrefix(rest, ":") {
		return 0, false
	}
	val := strings.TrimSpace(rest[1:])
	end := 0
	for end < len(val) && val[end] >= '0' && val[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(val[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

func extractBracketContent(s string) (string, bool) {
	idx := strings.IndexByte(s, '[')
	if idx < 0 {
		return "", false
	}
	end := findMatchingDelim(s, idx, '[', ']')
	if end <= idx {
		return "", false
	}
	return s[idx+1 : end], true
}

// EnrichWithSymbols merges optional LSP DocumentSymbol entries (e.g., from
// tinymist) into locally parsed outline items. If symbols is empty, local is
// returned unchanged. Local figures and labels are always preserved, matching
// heading lines keep their local label metadata, and any additional LSP headings
// (such as evaluated or macro-generated headings) are merged in document order.
func EnrichWithSymbols(local []Item, symbols []Symbol, opts ParseOptions) []Item {
	if len(symbols) == 0 {
		return local
	}

	out := make([]Item, len(local))
	copy(out, local)

	headingByLine := make(map[int]int)
	for i, it := range out {
		if it.Kind == KindHeading {
			headingByLine[it.Line] = i
		}
	}

	var walk func(syms []Symbol, depth int)
	walk = func(syms []Symbol, depth int) {
		level := depth
		if level < 1 {
			level = 1
		} else if level > 6 {
			level = 6
		}
		for _, sym := range syms {
			if isHeadingSymbolKind(sym.Kind) {
				title := collapseSpaces(sym.Name)
				if title != "" && sym.Line >= 0 {
					if idx, exists := headingByLine[sym.Line]; exists {
						// Enrich title if local was empty or keep cleaner LSP title
						// while preserving local level and label metadata.
						if out[idx].Title == "" {
							out[idx].Title = title
						}
					} else {
						headingByLine[sym.Line] = len(out)
						out = append(out, Item{
							Kind:   KindHeading,
							Level:  level,
							Title:  title,
							Line:   sym.Line,
							Column: sym.Column,
						})
					}
				}
			}
			if len(sym.Children) > 0 {
				walk(sym.Children, depth+1)
			}
		}
	}

	walk(symbols, 1)

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		if out[i].Column != out[j].Column {
			return out[i].Column < out[j].Column
		}
		return out[i].Kind < out[j].Kind
	})

	_ = opts
	return out
}

func isHeadingSymbolKind(kind int) bool {
	switch kind {
	case lspSymbolKindFile, lspSymbolKindModule, lspSymbolKindNamespace, lspSymbolKindPackage, lspSymbolKindString, lspSymbolKindObject:
		return true
	default:
		return false
	}
}
