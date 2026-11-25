package shortcuts

import (
	"sync"
	"unicode/utf8"

	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/typst"
	"github.com/monicafaye/TypX/internal/util"
)

// Mode classifies the Typst lexical context at a buffer location.
// Values and order match internal/typst.Mode.
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

// SetModeDetector installs an optional external lexical mode detector (primarily
// for tests). Passing nil uses the canonical internal/typst.DetectModeAt detector.
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
	charCol := col
	if line >= 0 && line < len(lines) {
		charCol = runeColToCharCol(lines[line], col)
	}
	return Mode(typst.DetectModeAt(typst.SliceSource(lines), line, charCol))
}

func detectModeAtLoc(b *buffer.Buffer, loc buffer.Loc) Mode {
	if b == nil || b.LinesNum() == 0 {
		return ModeMarkup
	}
	modeDetectorMu.RLock()
	fn := modeDetectorFn
	modeDetectorMu.RUnlock()
	if fn == nil {
		y := util.Clamp(loc.Y, 0, b.LinesNum()-1)
		x := loc.X
		if x < 0 {
			x = 0
		}
		return Mode(typst.DetectModeAt(b, y, x))
	}

	y := util.Clamp(loc.Y, 0, b.LinesNum()-1)
	lines := make([][]byte, b.LinesNum())
	for i := 0; i < b.LinesNum(); i++ {
		lines[i] = b.LineBytes(i)
	}
	prefix := util.SliceStart(lines[y], loc.X)
	runeCol := utf8.RuneCount(prefix)
	return fn(lines, y, runeCol)
}

func runeColToCharCol(line []byte, runeCol int) int {
	if runeCol <= 0 {
		return 0
	}
	charCol := 0
	runes := 0
	off := 0
	for off < len(line) && runes < runeCol {
		_, combc, size := util.DecodeCharacter(line[off:])
		if size <= 0 {
			break
		}
		runes += 1 + len(combc)
		off += size
		if runes <= runeCol {
			charCol++
		}
	}
	return charCol
}
