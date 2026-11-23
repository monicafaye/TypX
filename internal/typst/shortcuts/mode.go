package shortcuts

import (
	"sync"
	"unicode/utf8"

	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/util"
)

// Mode classifies the Typst lexical context at a buffer location.
// Values and order match internal/typst.Mode so M4 can wire typst.DetectModeAt
// via SetModeDetector without introducing an import cycle.
type Mode int

const (
	ModeMarkup Mode = iota
	ModeMath
	ModeCode
	ModeRaw
)

// String returns the human-readable uppercase mode name.
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
		return "MARKUP"
	}
}

var (
	modeDetectorMu sync.RWMutex
	modeDetectorFn func(lines [][]byte, line, col int) Mode
)

// SetModeDetector installs an external lexical mode detector (e.g.
// typst.DetectModeAt in M4). Passing nil restores the built-in fallback.
func SetModeDetector(fn func(lines [][]byte, line, col int) Mode) {
	modeDetectorMu.Lock()
	modeDetectorFn = fn
	modeDetectorMu.Unlock()
}

// DetectMode classifies the Typst lexical mode at 0-based (line, col),
// where col is a 0-based rune column in lines[line].
func DetectMode(lines [][]byte, line, col int) Mode {
	modeDetectorMu.RLock()
	fn := modeDetectorFn
	modeDetectorMu.RUnlock()
	if fn != nil {
		return fn(lines, line, col)
	}
	return fallbackDetectMode(lines, line, col)
}

func detectModeAtLoc(b *buffer.Buffer, loc buffer.Loc) Mode {
	if b == nil || b.LinesNum() == 0 {
		return ModeMarkup
	}
	y := util.Clamp(loc.Y, 0, b.LinesNum()-1)
	lines := make([][]byte, b.LinesNum())
	for i := 0; i < b.LinesNum(); i++ {
		lines[i] = b.LineBytes(i)
	}
	prefix := util.SliceStart(lines[y], loc.X)
	runeCol := utf8.RuneCount(prefix)
	return DetectMode(lines, y, runeCol)
}

func fallbackDetectMode(lines [][]byte, line, col int) Mode {
	if len(lines) == 0 {
		return ModeMarkup
	}
	targetLine := util.Clamp(line, 0, len(lines)-1)
	if col < 0 {
		col = 0
	}

	inRawBlock := false
	rawFenceLen := 0
	inMath := false
	commentDepth := 0
	var codeStack []byte // tracks '{', '(', '[' opened from '#' code expressions

	for y := 0; y <= targetLine; y++ {
		runes := []rune(string(lines[y]))
		limit := len(runes)
		if y == targetLine && col < limit {
			limit = col
		}

		if inRawBlock {
			closeTicks, isClose := matchRawFenceClose(runes, rawFenceLen)
			if y < targetLine {
				if isClose {
					inRawBlock = false
					rawFenceLen = 0
				}
				continue
			}
			// On targetLine inside a fenced raw block:
			if isClose && col >= closeTicks {
				inRawBlock = false
				rawFenceLen = 0
				return ModeMarkup
			}
			return ModeRaw
		}

		// Line-level fenced raw block start check (when not inside math or block comment)
		if !inMath && commentDepth == 0 && len(codeStack) == 0 {
			if fenceStart, fenceLen, ok := matchRawFenceOpen(runes); ok {
				// Check if a closing fence of >= fenceLen exists later on the same line.
				closeEnd := findClosingBackticksSameLine(runes, fenceStart+fenceLen, fenceLen)
				if closeEnd < 0 {
					if y < targetLine {
						inRawBlock = true
						rawFenceLen = fenceLen
						continue
					}
					if col >= fenceStart {
						return ModeRaw
					}
					return ModeMarkup
				}
			}
		}

		inLineComment := false
		inInlineRaw := false
		inlineRawLen := 0
		inString := false
		inLineCode := false
		i := 0

		for i < limit {
			r := runes[i]

			if inLineComment {
				i++
				continue
			}

			if commentDepth > 0 {
				if r == '/' && i+1 < len(runes) && runes[i+1] == '*' {
					commentDepth++
					i += 2
					continue
				}
				if r == '*' && i+1 < len(runes) && runes[i+1] == '/' {
					commentDepth--
					i += 2
					continue
				}
				i++
				continue
			}

			if inInlineRaw {
				if r == '`' {
					n := countBackticks(runes, i)
					if n == inlineRawLen {
						if y == targetLine && col < i+n {
							return ModeRaw
						}
						inInlineRaw = false
						inlineRawLen = 0
						i += n
						continue
					}
					i += n
					continue
				}
				i++
				continue
			}

			if inString {
				if r == '\\' {
					if i+1 < limit {
						i += 2
					} else {
						i++
					}
					continue
				}
				if r == '"' {
					inString = false
				}
				i++
				continue
			}

			// Outside comments, raw, and strings:
			if r == '\\' {
				if i+1 < limit {
					i += 2
				} else {
					i++
				}
				continue
			}

			if r == '/' && i+1 < len(runes) && runes[i+1] == '/' {
				inLineComment = true
				i += 2
				continue
			}
			if r == '/' && i+1 < len(runes) && runes[i+1] == '*' {
				commentDepth++
				i += 2
				continue
			}

			if r == '`' && !inMath {
				n := countBackticks(runes, i)
				inInlineRaw = true
				inlineRawLen = n
				i += n
				continue
			}

			if r == '$' {
				inMath = !inMath
				i++
				continue
			}

			if inMath {
				if r == '"' {
					inString = true
				}
				i++
				continue
			}

			// Code mode tracking outside math
			inCodeContext := (len(codeStack) > 0 && codeStack[len(codeStack)-1] != '[') || (len(codeStack) == 0 && inLineCode)
			if inCodeContext && r == '"' {
				inString = true
				i++
				continue
			}

			if !inCodeContext && r == '#' && i+1 < len(runes) {
				next := runes[i+1]
				if next == '{' || next == '[' {
					codeStack = append(codeStack, byte(next))
					i += 2
					continue
				}
				if isIdentStart(next) {
					inLineCode = true
					i++
					continue
				}
			}

			if inLineCode && len(codeStack) == 0 {
				if r == '{' || r == '(' || r == '[' {
					codeStack = append(codeStack, byte(r))
					i++
					continue
				}
				if r == ';' {
					inLineCode = false
					i++
					continue
				}
			}

			if len(codeStack) > 0 {
				top := codeStack[len(codeStack)-1]
				if r == '{' || r == '(' || r == '[' {
					// In markup content block [...], bare { or ( is literal unless preceded by #
					if top != '[' {
						codeStack = append(codeStack, byte(r))
					}
					i++
					continue
				}
				if (top == '{' && r == '}') || (top == '(' && r == ')') || (top == '[' && r == ']') {
					codeStack = codeStack[:len(codeStack)-1]
					if len(codeStack) == 0 && (top == '{' || top == '[') {
						inLineCode = false
					}
					i++
					continue
				}
			}

			i++
		}

		if y == targetLine {
			if inRawBlock || inInlineRaw {
				return ModeRaw
			}
			if inMath {
				return ModeMath
			}
			if len(codeStack) > 0 {
				if codeStack[len(codeStack)-1] == '[' {
					return ModeMarkup
				}
				return ModeCode
			}
			if inLineCode {
				return ModeCode
			}
			return ModeMarkup
		}
	}

	return ModeMarkup
}

func countBackticks(runes []rune, start int) int {
	n := 0
	for start+n < len(runes) && runes[start+n] == '`' {
		n++
	}
	return n
}

func matchRawFenceOpen(runes []rune) (start int, length int, ok bool) {
	i := 0
	for i < len(runes) && (runes[i] == ' ' || runes[i] == '\t') {
		i++
	}
	if i+2 >= len(runes) {
		return 0, 0, false
	}
	n := countBackticks(runes, i)
	if n < 3 {
		return 0, 0, false
	}
	return i, n, true
}

func matchRawFenceClose(runes []rune, minLen int) (endCol int, ok bool) {
	i := 0
	for i < len(runes) && (runes[i] == ' ' || runes[i] == '\t') {
		i++
	}
	n := countBackticks(runes, i)
	if n < minLen {
		return 0, false
	}
	j := i + n
	for j < len(runes) {
		if runes[j] != ' ' && runes[j] != '\t' {
			return 0, false
		}
		j++
	}
	return i + n, true
}

func findClosingBackticksSameLine(runes []rune, from, minLen int) int {
	i := from
	for i < len(runes) {
		if runes[i] == '`' {
			n := countBackticks(runes, i)
			if n >= minLen {
				return i + n
			}
			i += n
			continue
		}
		i++
	}
	return -1
}

func isIdentStart(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
}
