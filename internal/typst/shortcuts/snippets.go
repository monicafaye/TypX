package shortcuts

import (
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/util"
)

// SnippetStop describes a single tabstop inside a parsed snippet.
// StartChar and EndChar are 0-based character offsets into ParsedSnippet.Text.
type SnippetStop struct {
	Index     int
	StartChar int
	EndChar   int
}

// ParsedSnippet holds plain rendered text and ordered tabstops ($1, $2, ..., $0).
type ParsedSnippet struct {
	Text  string
	Stops []SnippetStop
}

// ParseSnippet parses TextMate / LSP snippet syntax ($0, $1, ${1}, ${1:text},
// ${1|a,b,c|}, and escaped \$, \}, \\) into plain text and ordered tabstops.
func ParseSnippet(raw string) ParsedSnippet {
	var out strings.Builder
	var rawStops []SnippetStop
	charPos := 0

	parseSnippetRange(raw, 0, len(raw), false, &out, &charPos, &rawStops)

	text := out.String()
	if len(rawStops) == 0 {
		return ParsedSnippet{Text: text}
	}

	// Keep the first occurrence of each tabstop index, ordered 1..N then 0.
	byIndex := make(map[int]SnippetStop, len(rawStops))
	for _, st := range rawStops {
		if _, exists := byIndex[st.Index]; !exists {
			byIndex[st.Index] = st
		}
	}

	stops := make([]SnippetStop, 0, len(byIndex)+1)
	hasZero := false
	hasPositive := false
	for idx, st := range byIndex {
		if idx == 0 {
			hasZero = true
		} else if idx > 0 {
			hasPositive = true
		}
		stops = append(stops, st)
	}
	if hasPositive && !hasZero {
		endChar := util.CharacterCountInString(text)
		stops = append(stops, SnippetStop{Index: 0, StartChar: endChar, EndChar: endChar})
	}

	sort.Slice(stops, func(i, j int) bool {
		ai, aj := stops[i].Index, stops[j].Index
		if ai == 0 && aj != 0 {
			return false
		}
		if aj == 0 && ai != 0 {
			return true
		}
		return ai < aj
	})

	return ParsedSnippet{Text: text, Stops: stops}
}

// HasSnippetTabstops reports whether raw contains at least one LSP/TextMate
// tabstop ($0, $1, ${1:...}, ${1|...|}).
func HasSnippetTabstops(raw string) bool {
	return len(ParseSnippet(raw).Stops) > 0
}

func parseSnippetRange(s string, start, end int, insideBrace bool, out *strings.Builder, charPos *int, stops *[]SnippetStop) int {
	i := start
	for i < end {
		ch := s[i]
		if ch == '\\' && i+1 < end {
			next := s[i+1]
			if next == '$' || next == '\\' || (insideBrace && next == '}') {
				out.WriteByte(next)
				*charPos++
				i += 2
				continue
			}
		}
		if insideBrace && ch == '}' {
			return i + 1
		}
		if ch == '$' && i+1 < end {
			// ${...}
			if s[i+1] == '{' {
				if nextIdx, ok := parseBraceSnippetToken(s, i, end, out, charPos, stops); ok {
					i = nextIdx
					continue
				}
			}
			// $N
			if s[i+1] >= '0' && s[i+1] <= '9' {
				j := i + 1
				for j < end && s[j] >= '0' && s[j] <= '9' {
					j++
				}
				if idx, err := strconv.Atoi(s[i+1 : j]); err == nil {
					*stops = append(*stops, SnippetStop{
						Index:     idx,
						StartChar: *charPos,
						EndChar:   *charPos,
					})
					i = j
					continue
				}
			}
		}
		r, _, sz := util.DecodeCharacter([]byte(s[i:end]))
		if sz <= 0 {
			sz = 1
		}
		_ = r
		out.WriteString(s[i : i+sz])
		*charPos++
		i += sz
	}
	return i
}

func parseBraceSnippetToken(s string, dollarPos, end int, out *strings.Builder, charPos *int, stops *[]SnippetStop) (int, bool) {
	j := dollarPos + 2 // after "${"
	numStart := j
	for j < end && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j == numStart || j >= end {
		return dollarPos, false
	}
	idx, err := strconv.Atoi(s[numStart:j])
	if err != nil {
		return dollarPos, false
	}

	switch s[j] {
	case '}':
		*stops = append(*stops, SnippetStop{
			Index:     idx,
			StartChar: *charPos,
			EndChar:   *charPos,
		})
		return j + 1, true

	case ':':
		stopStart := *charPos
		nextPos := parseSnippetRange(s, j+1, end, true, out, charPos, stops)
		stopEnd := *charPos
		*stops = append(*stops, SnippetStop{
			Index:     idx,
			StartChar: stopStart,
			EndChar:   stopEnd,
		})
		return nextPos, true

	case '|':
		// ${N|choice1,choice2|} -> default text is the first choice.
		closeIdx := strings.Index(s[j+1:end], "|}")
		if closeIdx < 0 {
			return dollarPos, false
		}
		choices := s[j+1 : j+1+closeIdx]
		first := choices
		if comma := strings.IndexByte(choices, ','); comma >= 0 {
			first = choices[:comma]
		}
		first = strings.ReplaceAll(first, `\,`, ",")
		first = strings.ReplaceAll(first, `\|`, "|")
		first = strings.ReplaceAll(first, `\\`, `\`)

		stopStart := *charPos
		out.WriteString(first)
		*charPos += util.CharacterCountInString(first)
		stopEnd := *charPos
		*stops = append(*stops, SnippetStop{
			Index:     idx,
			StartChar: stopStart,
			EndChar:   stopEnd,
		})
		return j + 1 + closeIdx + 2, true
	}

	return dollarPos, false
}

type absStop struct {
	index    int
	startOff int
	endOff   int
}

type snippetSession struct {
	stops         []absStop
	curIdx        int
	curTailChars  int
	curTailSample string
}

var (
	snippetMu       sync.Mutex
	snippetSessions = make(map[*buffer.SharedBuffer]*snippetSession)
)

type snippetEvictObserver struct {
	buffer.NopObserver
}

func (snippetEvictObserver) OnBufferClose(b *buffer.Buffer) {
	if b == nil || b.Shared() || b.SharedBuffer == nil {
		return
	}
	clearSnippetSession(b)
}

// SnippetEvictObserver returns a buffer.Observer that clears snippet sessions
// when a buffer closes.
func SnippetEvictObserver() buffer.Observer {
	return snippetEvictObserver{}
}

func clearSnippetSession(b *buffer.Buffer) {
	if b == nil || b.SharedBuffer == nil {
		return
	}
	snippetMu.Lock()
	delete(snippetSessions, b.SharedBuffer)
	snippetMu.Unlock()
}

// HasActiveSnippetSession reports whether b currently has an active multi-stop
// snippet navigation session.
func HasActiveSnippetSession(b *buffer.Buffer) bool {
	if b == nil || b.SharedBuffer == nil {
		return false
	}
	snippetMu.Lock()
	_, ok := snippetSessions[b.SharedBuffer]
	snippetMu.Unlock()
	return ok
}

// ExpandSnippetAt expands raw at the active cursor of bp (replacing any active
// selection), re-indenting continuation lines, and activating Tab / Shift+Tab
// navigation when multiple tabstops are present.
func ExpandSnippetAt(bp *action.BufPane, raw string) bool {
	if !canEditTypst(bp) {
		return false
	}
	c := activeCursor(bp)
	if c == nil {
		return false
	}
	start := c.Loc
	end := c.Loc
	if c.HasSelection() {
		start, end = orderedSelection(c)
	}
	return ExpandSnippetReplacing(bp, start, end, raw)
}

// ExpandSnippetReplacing replaces [start, end) in bp.Buf with raw parsed as an
// LSP/TextMate snippet, re-indenting continuation lines to match start.Y, and
// activates multi-stop Tab / Shift+Tab navigation when appropriate.
func ExpandSnippetReplacing(bp *action.BufPane, start, end buffer.Loc, raw string) bool {
	if bp == nil || bp.Buf == nil || bp.Buf.Type.Readonly {
		return false
	}
	c := activeCursor(bp)
	if c == nil {
		return false
	}
	clearSnippetSession(bp.Buf)

	if c.HasSelection() {
		c.ResetSelection()
	}

	reindented := reindentSnippet(bp.Buf, start, raw)
	parsed := ParseSnippet(reindented)

	startOff := locToCharOffset(bp.Buf, start)
	if start != end {
		bp.Buf.Replace(start, end, parsed.Text)
	} else {
		bp.Buf.Insert(start, parsed.Text)
	}

	if len(parsed.Stops) == 0 {
		endLoc := charOffsetToLoc(bp.Buf, startOff+util.CharacterCountInString(parsed.Text))
		c.GotoLoc(endLoc)
		c.ResetSelection()
		if bp.BWindow != nil {
			bp.Relocate()
		}
		return true
	}

	stops := make([]absStop, len(parsed.Stops))
	for i, st := range parsed.Stops {
		stops[i] = absStop{
			index:    st.Index,
			startOff: startOff + st.StartChar,
			endOff:   startOff + st.EndChar,
		}
	}

	sess := &snippetSession{
		stops:  stops,
		curIdx: 0,
	}
	enterSnippetStop(bp, c, sess, 0)
	if bp.BWindow != nil {
		bp.Relocate()
	}
	return true
}

func enterSnippetStop(bp *action.BufPane, c *buffer.Cursor, sess *snippetSession, idx int) {
	st := sess.stops[idx]
	startLoc := charOffsetToLoc(bp.Buf, st.startOff)
	endLoc := charOffsetToLoc(bp.Buf, st.endOff)

	if st.startOff < st.endOff {
		c.SetSelectionStart(startLoc)
		c.SetSelectionEnd(endLoc)
		c.GotoLoc(endLoc)
	} else {
		c.ResetSelection()
		c.GotoLoc(startLoc)
	}

	// If this is the final empty stop ($0), finish the session immediately so
	// subsequent Tab presses perform normal indentation or completion.
	if idx == len(sess.stops)-1 && st.index == 0 && st.startOff == st.endOff {
		clearSnippetSession(bp.Buf)
		return
	}

	total := totalCharCount(bp.Buf)
	sess.curIdx = idx
	sess.curTailChars = total - st.endOff
	sess.curTailSample = sampleAfterCharOffset(bp.Buf, st.endOff, 32)

	snippetMu.Lock()
	snippetSessions[bp.Buf.SharedBuffer] = sess
	snippetMu.Unlock()
}

// NewSnippetNextAction returns the BufKeyAction for advancing to the next
// active snippet tabstop on Tab. Returns false when no active session exists
// or while autocomplete suggestions are open, allowing Tab fallbacks to run.
func NewSnippetNextAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		return jumpSnippetStop(bp, +1)
	}
}

// NewSnippetPrevAction returns the BufKeyAction for moving to the previous
// active snippet tabstop on Shift+Tab (Backtab).
func NewSnippetPrevAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		return jumpSnippetStop(bp, -1)
	}
}

// HandleSnippetEscape clears any active snippet session on Esc without
// blocking micro's remaining Escape/Deselect chain.
func HandleSnippetEscape(bp *action.BufPane) bool {
	if bp == nil || bp.Buf == nil {
		return false
	}
	clearSnippetSession(bp.Buf)
	return false
}

func jumpSnippetStop(bp *action.BufPane, dir int) bool {
	if bp == nil || bp.Buf == nil || bp.Buf.SharedBuffer == nil {
		return false
	}
	if bp.Buf.HasSuggestions {
		return false
	}
	c := activeCursor(bp)
	if c == nil {
		return false
	}

	snippetMu.Lock()
	sess := snippetSessions[bp.Buf.SharedBuffer]
	snippetMu.Unlock()
	if sess == nil || sess.curIdx < 0 || sess.curIdx >= len(sess.stops) {
		return false
	}

	cur := sess.stops[sess.curIdx]
	totalNow := totalCharCount(bp.Buf)
	newEndOff := totalNow - sess.curTailChars
	if newEndOff < cur.startOff || newEndOff > totalNow {
		clearSnippetSession(bp.Buf)
		return false
	}
	if sampleAfterCharOffset(bp.Buf, newEndOff, util.CharacterCountInString(sess.curTailSample)) != sess.curTailSample {
		clearSnippetSession(bp.Buf)
		return false
	}

	curOff := locToCharOffset(bp.Buf, c.Loc)
	if curOff < cur.startOff || curOff > newEndOff {
		clearSnippetSession(bp.Buf)
		return false
	}

	delta := newEndOff - cur.endOff
	sess.stops[sess.curIdx].endOff = newEndOff
	if delta != 0 {
		for j := sess.curIdx + 1; j < len(sess.stops); j++ {
			sess.stops[j].startOff += delta
			sess.stops[j].endOff += delta
		}
	}

	nextIdx := sess.curIdx + dir
	if nextIdx < 0 {
		return false
	}
	if nextIdx >= len(sess.stops) {
		clearSnippetSession(bp.Buf)
		return false
	}

	enterSnippetStop(bp, c, sess, nextIdx)
	if bp.BWindow != nil {
		bp.Relocate()
	}
	return true
}

func reindentSnippet(b *buffer.Buffer, start buffer.Loc, raw string) string {
	ws := leadingWhitespaceBeforeLoc(b, start)
	indentUnit := safeIndentString(b)

	rawLines := strings.Split(raw, "\n")
	if len(rawLines) == 1 {
		return raw
	}
	outLines := make([]string, len(rawLines))
	for i, line := range rawLines {
		sp := 0
		for sp < len(line) && line[sp] == ' ' {
			sp++
		}
		levels := sp / 2
		rem := sp % 2
		reindented := strings.Repeat(indentUnit, levels) + strings.Repeat(" ", rem) + line[sp:]
		if i > 0 {
			reindented = ws + reindented
		}
		outLines[i] = reindented
	}
	return strings.Join(outLines, "\n")
}

func totalCharCount(b *buffer.Buffer) int {
	n := b.LinesNum()
	if n <= 0 {
		return 0
	}
	total := 0
	for y := 0; y < n; y++ {
		total += util.CharacterCount(b.LineBytes(y))
		if y+1 < n {
			total++ // newline
		}
	}
	return total
}

func locToCharOffset(b *buffer.Buffer, loc buffer.Loc) int {
	n := b.LinesNum()
	if n <= 0 {
		return 0
	}
	y := loc.Y
	if y < 0 {
		y = 0
	}
	if y >= n {
		y = n - 1
	}
	off := 0
	for i := 0; i < y; i++ {
		off += util.CharacterCount(b.LineBytes(i)) + 1
	}
	lineLen := util.CharacterCount(b.LineBytes(y))
	x := loc.X
	if x < 0 {
		x = 0
	}
	if x > lineLen {
		x = lineLen
	}
	return off + x
}

func charOffsetToLoc(b *buffer.Buffer, off int) buffer.Loc {
	n := b.LinesNum()
	if n <= 0 || off <= 0 {
		return buffer.Loc{X: 0, Y: 0}
	}
	rem := off
	for y := 0; y < n; y++ {
		lineLen := util.CharacterCount(b.LineBytes(y))
		if rem <= lineLen {
			return buffer.Loc{X: rem, Y: y}
		}
		rem -= lineLen + 1
	}
	lastY := n - 1
	return buffer.Loc{X: util.CharacterCount(b.LineBytes(lastY)), Y: lastY}
}

func sampleAfterCharOffset(b *buffer.Buffer, off, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	total := totalCharCount(b)
	if off >= total {
		return ""
	}
	endOff := off + maxChars
	if endOff > total {
		endOff = total
	}
	startLoc := charOffsetToLoc(b, off)
	endLoc := charOffsetToLoc(b, endOff)
	return string(b.Substr(startLoc, endLoc))
}
