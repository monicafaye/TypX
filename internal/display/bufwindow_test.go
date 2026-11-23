package display

import (
	"bytes"
	"strings"
	"testing"

	"github.com/micro-editor/tcell/v2"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	ulua "github.com/monicafaye/TypX/internal/lua"
	"github.com/monicafaye/TypX/internal/screen"
	"github.com/monicafaye/TypX/internal/util"
	lua "github.com/yuin/gopher-lua"
)

func init() {
	ulua.L = lua.NewState()
	config.InitRuntimeFiles(false)
	config.InitGlobalSettings()
	config.GlobalSettings["backup"] = false
	config.GlobalSettings["fastdirty"] = true
}

func newTestWin(t *testing.T, text, filename string, w, h int) (*BufWindow, tcell.SimulationScreen, func()) {
	t.Helper()
	origScreen := screen.Screen
	origFake := util.FakeCursor
	origColors := config.Colorscheme

	sim, err := screen.InitSimScreen()
	if err != nil {
		t.Fatalf("InitSimScreen: %v", err)
	}
	sim.SetSize(w, h)
	util.FakeCursor = false
	config.Colorscheme = make(map[string]tcell.Style)

	buf := buffer.NewBufferFromString(text, filename, buffer.BTDefault)
	if strings.HasSuffix(filename, ".typ") {
		buf.Settings["filetype"] = "typst"
	}
	buf.Settings["ruler"] = false
	buf.Settings["scrollbar"] = false
	buf.Settings["statusline"] = false
	buf.Settings["softwrap"] = false
	buf.Settings["cursorline"] = false
	buf.Settings["typst.conceal"] = true
	buf.Settings["typst.concealMode"] = "line"

	win := NewBufWindow(0, 0, w, h, buf)
	cleanup := func() {
		buf.Close()
		sim.Fini()
		screen.Screen = origScreen
		util.FakeCursor = origFake
		config.Colorscheme = origColors
	}
	return win, sim, cleanup
}

func readSimRow(sim tcell.SimulationScreen, y, width int) string {
	var rs []rune
	for x := 0; x < width; {
		r, _, _, rw := sim.GetContent(x, y)
		if r == 0 {
			r = ' '
		}
		rs = append(rs, r)
		if rw > 1 {
			x += rw
		} else {
			x++
		}
	}
	return strings.TrimRight(string(rs), " ")
}

func TestConcealRenderingActiveLineVsInactiveLine(t *testing.T) {
	win, sim, cleanup := newTestWin(t, "$alpha + beta$\n$gamma -> delta$", "doc.typ", 30, 5)
	defer cleanup()

	win.Display()
	if got := readSimRow(sim, 0, 30); got != "$alpha + beta$" {
		t.Errorf("line 0 (active) = %q, want $alpha + beta$", got)
	}
	if got := readSimRow(sim, 1, 30); got != "$γ → δ$" {
		t.Errorf("line 1 (inactive) = %q, want $γ → δ$", got)
	}

	// Move cursor to line 1 -> line 0 becomes concealed, line 1 becomes raw
	win.Buf.GetActiveCursor().GotoLoc(buffer.Loc{X: 2, Y: 1})
	win.Display()
	if got := readSimRow(sim, 0, 30); got != "$α + β$" {
		t.Errorf("line 0 (after move) = %q, want $α + β$", got)
	}
	if got := readSimRow(sim, 1, 30); got != "$gamma -> delta$" {
		t.Errorf("line 1 (after move) = %q, want $gamma -> delta$", got)
	}
	cx, cy, vis := sim.GetCursor()
	if !vis || cx != 2 || cy != 1 {
		t.Errorf("cursor = (%d,%d,vis=%v), want (2,1,true)", cx, cy, vis)
	}

	// Selection on active line in "line" mode keeps line unconcealed
	c := win.Buf.GetActiveCursor()
	c.SetSelectionStart(buffer.Loc{X: 1, Y: 1})
	c.SetSelectionEnd(buffer.Loc{X: 4, Y: 1})
	win.Display()
	if got := readSimRow(sim, 1, 30); got != "$gamma -> delta$" {
		t.Errorf("line 1 with selection in line mode = %q, want unconcealed", got)
	}
}

func TestConcealTokenModeAndRelocate(t *testing.T) {
	win, sim, cleanup := newTestWin(t, "$alpha + beta + gamma + delta$", "doc.typ", 16, 4)
	defer cleanup()

	win.Buf.Settings["typst.concealMode"] = "token"
	c := win.Buf.GetActiveCursor()
	c.GotoLoc(buffer.Loc{X: 3, Y: 0}) // inside alpha
	win.Display()
	if got := readSimRow(sim, 0, 16); got != "$alpha + β + γ +" {
		t.Errorf("token mode cursor on alpha: got %q, want %q", got, "$alpha + β + γ +")
	}
	cx, cy, vis := sim.GetCursor()
	if !vis || cx != 3 || cy != 0 {
		t.Errorf("cursor on alpha = (%d,%d,vis=%v), want (3,0,true)", cx, cy, vis)
	}

	// Move cursor to closing '$' of delta$ at end of line and verify Relocate scrolls using concealed visual X
	c.GotoLoc(buffer.Loc{X: 29, Y: 0})
	win.Relocate()
	win.Display()
	if got := readSimRow(sim, 0, 16); !strings.Contains(got, "delta$") {
		t.Errorf("expected delta$ visible after Relocate, got %q (StartCol=%d)", got, win.StartCol)
	}
}

func TestConcealInactiveWindow(t *testing.T) {
	win, sim, cleanup := newTestWin(t, "$alpha + beta$", "doc.typ", 24, 4)
	defer cleanup()

	win.Buf.GetActiveCursor().GotoLoc(buffer.Loc{X: 2, Y: 0})
	win.SetActive(false)
	win.Display()
	if got := readSimRow(sim, 0, 24); got != "$α + β$" {
		t.Errorf("inactive window line = %q, want $α + β$", got)
	}
}

func TestConcealToggleOffAndNonTypst(t *testing.T) {
	win, sim, cleanup := newTestWin(t, "line0\n$alpha -> beta$", "doc.typ", 24, 4)
	defer cleanup()

	win.Buf.Settings["typst.conceal"] = false
	win.Display()
	if got := readSimRow(sim, 1, 24); got != "$alpha -> beta$" {
		t.Errorf("conceal=false line 1 = %q, want raw", got)
	}

	winMD, simMD, cleanupMD := newTestWin(t, "line0\n$alpha -> beta$", "doc.md", 24, 4)
	defer cleanupMD()
	winMD.Display()
	if got := readSimRow(simMD, 1, 24); got != "$alpha -> beta$" {
		t.Errorf("non-typst doc.md line 1 = %q, want raw", got)
	}
}

func TestConcealStylingAndFallback(t *testing.T) {
	syntaxStyle := tcell.StyleDefault.Foreground(tcell.ColorGreen)
	symbolStyle := tcell.StyleDefault.Foreground(tcell.ColorBlue)
	symbolMathStyle := tcell.StyleDefault.Foreground(tcell.ColorYellow)
	concealStyle := tcell.StyleDefault.Foreground(tcell.ColorRed)

	origColors := config.Colorscheme
	defer func() { config.Colorscheme = origColors }()

	config.Colorscheme = map[string]tcell.Style{}
	if got := concealGlyphStyle(syntaxStyle); got != syntaxStyle {
		t.Errorf("fallback should preserve syntaxStyle when no conceal/symbol color exists")
	}

	config.Colorscheme["symbol"] = symbolStyle
	if got := concealGlyphStyle(syntaxStyle); got != symbolStyle {
		t.Errorf("expected symbol style fallback")
	}

	config.Colorscheme["symbol.math"] = symbolMathStyle
	if got := concealGlyphStyle(syntaxStyle); got != symbolMathStyle {
		t.Errorf("expected symbol.math to beat symbol")
	}

	config.Colorscheme["conceal"] = concealStyle
	if got := concealGlyphStyle(syntaxStyle); got != concealStyle {
		t.Errorf("expected conceal to beat symbol.math")
	}
}

func TestConcealSoftwrapWordwrapAndClicks(t *testing.T) {
	// Line 0 is active so line 1 ("\t中$alpha + beta$ z") is concealed:
	// DisplayText: ['\t','中','$','α',' ','+',' ','β','$',' ','z']
	text := "top\n\t中$alpha + beta$ z"
	win, sim, cleanup := newTestWin(t, text, "doc.typ", 10, 6)
	defer cleanup()

	win.Buf.Settings["softwrap"] = true
	win.Buf.Settings["wordwrap"] = true
	win.Buf.Settings["tabsize"] = float64(4)
	win.Display()

	if rc := win.getRowCount(1); rc != 2 {
		t.Fatalf("getRowCount(1) = %d, want 2", rc)
	}

	// Row 0: "\t" (4) + "中" (2) + "$α " (3) -> padded at X=9; Row 1: "+ β$ z"
	if got := readSimRow(sim, 1, 10); got != "\t中$α" && got != "    中$α" {
		// In SimulationScreen, '\t' renders as ' '
		if got != "    中$α" {
			t.Errorf("wrapped row 0 = %q", got)
		}
	}
	if got := readSimRow(sim, 2, 10); got != "+ β$ z" {
		t.Errorf("wrapped row 1 = %q, want %q", got, "+ β$ z")
	}

	// VLocFromLoc for 'beta' (bufCol 11) -> row 1, visualX 2
	vloc := win.VLocFromLoc(buffer.Loc{X: 11, Y: 1})
	if vloc.SLoc.Line != 1 || vloc.SLoc.Row != 1 || vloc.VisualX != 2 {
		t.Errorf("VLocFromLoc(11,1) = %+v, want {SLoc:{1 1} VisualX:2}", vloc)
	}

	// LocFromVisual clicks on wide rune (2nd cell of 中 at visual x=5, screen y=1) -> bufCol 1
	if loc := win.LocFromVisual(buffer.Loc{X: 5, Y: 1}); loc != (buffer.Loc{X: 1, Y: 1}) {
		t.Errorf("click on 2nd cell of 中 = %+v, want {1, 1}", loc)
	}
	// Click on α (screen x=7, y=1) -> bufCol 3
	if loc := win.LocFromVisual(buffer.Loc{X: 7, Y: 1}); loc != (buffer.Loc{X: 3, Y: 1}) {
		t.Errorf("click on α = %+v, want {3, 1}", loc)
	}
	// Click on β (screen x=2, y=2) -> bufCol 11
	if loc := win.LocFromVisual(buffer.Loc{X: 2, Y: 2}); loc != (buffer.Loc{X: 11, Y: 1}) {
		t.Errorf("click on β = %+v, want {11, 1}", loc)
	}
	// Click past EOL on row 1 (screen x=9, y=2) -> OriginalLen (18)
	if loc := win.LocFromVisual(buffer.Loc{X: 9, Y: 2}); loc != (buffer.Loc{X: 18, Y: 1}) {
		t.Errorf("click past EOL = %+v, want {18, 1}", loc)
	}

	// Test softwrap=true, wordwrap=false
	win.Buf.Settings["wordwrap"] = false
	win.Display()
	if rc := win.getRowCount(1); rc != 2 {
		t.Errorf("wordwrap=false getRowCount(1) = %d, want 2", rc)
	}
	if loc := win.LocFromVisual(buffer.Loc{X: 1, Y: 2}); loc != (buffer.Loc{X: 11, Y: 1}) {
		t.Errorf("wordwrap=false click on β (row 1, x=1) = %+v, want {11, 1}", loc)
	}

	// Test softwrap=false with StartCol=5 (straddling 2nd cell of 中)
	win.Buf.Settings["softwrap"] = false
	win.StartCol = 5
	win.Display()
	if loc := win.LocFromVisual(buffer.Loc{X: 2, Y: 1}); loc != (buffer.Loc{X: 3, Y: 1}) {
		t.Errorf("horizontal scroll click on α (StartCol=5, screen x=2) = %+v, want {3, 1}", loc)
	}
}

func TestConcealEOLCursorSelectionAndDiagnostic(t *testing.T) {
	win, sim, cleanup := newTestWin(t, "top\nhello ---", "doc.typ", 24, 5)
	defer cleanup()

	// Diagnostic overlapping "h" .. middle of "---" (cols 6..8)
	win.Buf.AddMessage(buffer.NewMessage("tinymist", "err", buffer.Loc{X: 6, Y: 1}, buffer.Loc{X: 8, Y: 1}, buffer.MTError))
	// Selection overlapping middle of "---" (cols 7..9)
	c := win.Buf.GetActiveCursor()
	c.SetSelectionStart(buffer.Loc{X: 7, Y: 1})
	c.SetSelectionEnd(buffer.Loc{X: 9, Y: 1})

	selStyle := tcell.StyleDefault.Background(tcell.ColorBlue)
	config.Colorscheme["selection"] = selStyle

	win.Display()

	// Concealed line 1: "hello —" -> '—' at screen x = gutterOffset + 6, y = 1
	r, _, st, _ := sim.GetContent(win.gutterOffset+6, 1)
	if r != '—' {
		t.Fatalf("screen(%d,1) rune = %q, want '—'", win.gutterOffset+6, r)
	}
	_, bg, attrs := st.Decompose()
	if bg != tcell.ColorBlue {
		t.Errorf("expected selection bg on concealed glyph overlapping selection, got %v", bg)
	}
	if attrs&tcell.AttrUnderline == 0 {
		t.Errorf("expected diagnostic underline on concealed glyph overlapping message")
	}

	// EOL cursor check: move cursor to EOL of line 1 in token mode (or line ending with '—')
	c.ResetSelection()
	win.Buf.Settings["typst.concealMode"] = "token"
	// On line 1: "hello ---" has length 9. With cursor at col 9, "---" is unconcealed at cols 6..8 -> EOL cursor at x=9.
	// Put cursor at EOL on a line where an earlier token is concealed: "$alpha$ ok"
	win2, sim2, cleanup2 := newTestWin(t, "$alpha$ ok", "doc_eol.typ", 24, 4)
	defer cleanup2()
	win2.Buf.Settings["typst.concealMode"] = "token"
	win2.Buf.GetActiveCursor().GotoLoc(buffer.Loc{X: 10, Y: 0}) // EOL of "$alpha$ ok" (len 10)
	win2.Display()
	// "$α$ ok" has visual len 6, so EOL cursor should render at x=6, y=0!
	cx, cy, vis := sim2.GetCursor()
	if !vis || cx != 6 || cy != 0 {
		t.Errorf("EOL cursor with earlier concealed token = (%d,%d,vis=%v), want (6,0,true)", cx, cy, vis)
	}
}

func TestConcealBufferImmutability(t *testing.T) {
	orig := "top\n$alpha + beta$ --- end"
	win, _, cleanup := newTestWin(t, orig, "doc.typ", 30, 5)
	defer cleanup()

	verBefore := win.Buf.Version()
	bytesBefore := append([]byte(nil), win.Buf.Bytes()...)

	win.Display()
	_ = win.VLocFromLoc(buffer.Loc{X: 10, Y: 1})
	_ = win.LocFromVisual(buffer.Loc{X: 4, Y: 1})

	if win.Buf.Version() != verBefore {
		t.Fatalf("buffer version mutated: %d -> %d", verBefore, win.Buf.Version())
	}
	if !bytes.Equal(win.Buf.Bytes(), bytesBefore) {
		t.Fatalf("buffer bytes mutated: %q -> %q", string(bytesBefore), string(win.Buf.Bytes()))
	}
}
