package features

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/display"
	ulua "github.com/monicafaye/TypX/internal/lua"
	"github.com/monicafaye/TypX/internal/screen"
	"github.com/monicafaye/TypX/internal/typst/shortcuts"
	lua "github.com/yuin/gopher-lua"
)

func initTestEnv(t *testing.T) {
	t.Helper()
	if ulua.L == nil {
		ulua.L = lua.NewState()
	}
	config.ConfigDir = t.TempDir()
	config.InitRuntimeFiles(false)
	_ = config.InitGlobalSettings()
	config.GlobalSettings["backup"] = false
	config.GlobalSettings["fastdirty"] = true
	if screen.Screen == nil {
		sim, err := screen.InitSimScreen()
		require.NoError(t, err)
		sim.SetSize(80, 24)
	}
	Init()
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()
	buffer.SetMessager(action.InfoBar)
}

func newTestPane(t *testing.T, content, filename string) *action.BufPane {
	t.Helper()
	initTestEnv(t)
	b := buffer.NewBufferFromString(content, filename, buffer.BTDefault)
	t.Cleanup(func() { b.Close() })
	win := display.NewBufWindow(0, 0, 80, 23, b)
	return action.NewBufPane(b, win, nil)
}

func TestInitIdempotentAndRegistersPhase2(t *testing.T) {
	initTestEnv(t)
	// Second Init call must not panic.
	assert.NotPanics(t, func() { Init() })

	assert.Contains(t, action.BufKeyActions, "ToggleConceal")
	assert.Contains(t, action.BufKeyActions, "MathInline")
	assert.Contains(t, action.BufKeyActions, "MathDisplay")
	assert.Contains(t, action.BufKeyActions, "WrapBold")
	assert.Contains(t, action.BufKeyActions, "WrapItalic")
	assert.Contains(t, action.BufKeyActions, "HeadingPromote")
	assert.Contains(t, action.BufKeyActions, "HeadingDemote")
	assert.Contains(t, action.BufKeyActions, "InsertPalette")
}

func TestConcealActionAndCommandTriggerOptionCallback(t *testing.T) {
	bp := newTestPane(t, "$alpha + beta$\n", "paper.typ")
	require.True(t, bp.Buf.IsTypst())

	callbackCalls := 0
	var lastVal any
	origCB := bp.Buf.OptionCallback
	bp.Buf.OptionCallback = func(option string, nativeValue any) {
		if option == "typst.conceal" {
			callbackCalls++
			lastVal = nativeValue
		}
		if origCB != nil {
			origCB(option, nativeValue)
		}
	}

	// Default is true; ToggleConceal flips to false and fires OptionCallback.
	toggle := NewToggleConcealAction()
	assert.True(t, toggle(bp))
	assert.Equal(t, false, bp.Buf.Settings["typst.conceal"])
	assert.Equal(t, 1, callbackCalls)
	assert.Equal(t, false, lastVal)

	// :conceal on sets true and fires OptionCallback.
	bp.HandleCommand("conceal on")
	assert.Equal(t, true, bp.Buf.Settings["typst.conceal"])
	assert.Equal(t, 2, callbackCalls)
	assert.Equal(t, true, lastVal)

	// :conceal off sets false.
	bp.HandleCommand("conceal off")
	assert.Equal(t, false, bp.Buf.Settings["typst.conceal"])
	assert.Equal(t, 3, callbackCalls)

	// :conceal with invalid arg preserves false and reports error.
	bp.HandleCommand("conceal bogus")
	assert.Equal(t, false, bp.Buf.Settings["typst.conceal"])
	assert.Equal(t, 3, callbackCalls)
	assert.True(t, action.InfoBar.HasError)

	// Non-typst buffer returns false for ToggleConceal (so fallback works).
	md := newTestPane(t, "# hello\n", "readme.md")
	assert.False(t, md.Buf.IsTypst())
	assert.False(t, toggle(md))
}

func TestStatusLineProvidersModeAndWordCount(t *testing.T) {
	bp := newTestPane(t, "Hello world\n$alpha + beta$\n#let x = 1\n", "notes.typ")

	// Cursor at (0,0) -> MARKUP
	assert.Equal(t, "MARKUP | ", modeStatusInfo(bp.Buf))
	// Words: Hello, world, $alpha, +, beta$, #let, x, =, 1 => 9 words
	assert.Equal(t, "9 words | ", wordCountStatusInfo(bp.Buf))

	// Move cursor inside $alpha + beta$ -> MATH
	bp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 1})
	assert.Equal(t, "MATH | ", modeStatusInfo(bp.Buf))

	// Move cursor inside #let x = 1 -> CODE
	bp.Cursor.GotoLoc(buffer.Loc{X: 5, Y: 2})
	assert.Equal(t, "CODE | ", modeStatusInfo(bp.Buf))

	// Single word buffer -> singular "1 word | "
	oneWord := newTestPane(t, "Typst", "one.typ")
	assert.Equal(t, "1 word | ", wordCountStatusInfo(oneWord.Buf))

	// Non-typst buffer -> empty strings
	txt := newTestPane(t, "hello world", "notes.txt")
	assert.Equal(t, "", modeStatusInfo(txt.Buf))
	assert.Equal(t, "", wordCountStatusInfo(txt.Buf))
}

func TestModeDetectorWiringWithCombiningMarksAndSurrogates(t *testing.T) {
	initTestEnv(t)
	// Combining mark "e\u0301" (2 runes, 1 char cluster) + non-BMP "𝕏😀"
	line := []byte("𝕏😀 e\u0301 $x + α$")
	// Rune offsets: 0:'𝕏', 1:'😀', 2:' ', 3:'e', 4:'\u0301', 5:' ', 6:'$', 7:'x'
	assert.Equal(t, 4, runeColToCharCol(line, 5))
	assert.Equal(t, 6, runeColToCharCol(line, 7))

	assert.Equal(t, shortcuts.ModeMarkup, shortcuts.DetectMode([][]byte{line}, 0, 5))
	assert.Equal(t, shortcuts.ModeMath, shortcuts.DetectMode([][]byte{line}, 0, 7))
	assert.Equal(t, shortcuts.ModeMarkup, shortcuts.DetectMode([][]byte{line}, 0, 13))
}

func TestSmartPairHooksWiredThroughActionLayer(t *testing.T) {
	bp := newTestPane(t, "", "pairs.typ")

	// Typing '$' inserts "$$" with cursor between them
	bp.DoRuneInsert('$')
	assert.Equal(t, "$$", string(bp.Buf.Bytes()))
	assert.Equal(t, buffer.Loc{X: 1, Y: 0}, bp.Cursor.Loc)

	// Typing '$' again skips over closing '$'
	bp.DoRuneInsert('$')
	assert.Equal(t, "$$", string(bp.Buf.Bytes()))
	assert.Equal(t, buffer.Loc{X: 2, Y: 0}, bp.Cursor.Loc)

	// Move back between "$$" and press Backspace -> deletes both '$'
	bp.Cursor.GotoLoc(buffer.Loc{X: 1, Y: 0})
	bp.Backspace()
	assert.Equal(t, "", string(bp.Buf.Bytes()))
	assert.Equal(t, buffer.Loc{X: 0, Y: 0}, bp.Cursor.Loc)

	// Typing '{' then Enter expands with indent
	bp.DoRuneInsert('{')
	assert.Equal(t, "{}", string(bp.Buf.Bytes()))
	bp.InsertNewline()
	assert.Equal(t, 3, bp.Buf.LinesNum())
	assert.Equal(t, "{", string(bp.Buf.LineBytes(0)))
	assert.Equal(t, "  ", string(bp.Buf.LineBytes(1)))
	assert.Equal(t, "}", string(bp.Buf.LineBytes(2)))
	assert.Equal(t, buffer.Loc{X: 2, Y: 1}, bp.Cursor.Loc)
}
