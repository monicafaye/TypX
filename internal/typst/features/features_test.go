package features

import (
	"context"
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/display"
	"github.com/monicafaye/TypX/internal/lsp"
	ulua "github.com/monicafaye/TypX/internal/lua"
	"github.com/monicafaye/TypX/internal/screen"
	"github.com/monicafaye/TypX/internal/typst/bib"
	"github.com/monicafaye/TypX/internal/typst/outline"
	"github.com/monicafaye/TypX/internal/typst/preview"
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
	assert.Contains(t, action.BufKeyActions, "SymbolPicker")
	assert.Contains(t, action.BufKeyActions, "CitePicker")
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

type stubAnchoredCompiler struct {
	pages   int
	anchors *preview.DocumentAnchors
}

func (s *stubAnchoredCompiler) CompilePages(ctx context.Context, req preview.CompileRequest) (*preview.DocumentDeck, error) {
	imgs := make([]*image.RGBA, s.pages)
	for i := 0; i < s.pages; i++ {
		img := image.NewRGBA(image.Rect(0, 0, 400, 600))
		draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)
		imgs[i] = img
	}
	deck := preview.NewDocumentDeckFromImages(imgs, time.Millisecond)
	deck.SetAnchors(s.anchors)
	return deck, nil
}

func TestOutlineSelectionRepositionsPreviewPane(t *testing.T) {
	initTestEnv(t)
	if sim, ok := screen.Screen.(tcell.SimulationScreen); ok {
		sim.SetSize(140, 36)
	}
	preview.SetCapsForTest(preview.TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})
	preview.SetCompiler(&stubAnchoredCompiler{
		pages: 3,
		anchors: &preview.DocumentAnchors{
			Outlines: []preview.OutlineAnchor{
				{Title: "Introduction", Depth: 1, PageIdx: 0, YFrac: 0.08},
				{Title: "Methodology", Depth: 1, PageIdx: 1, YFrac: 0.45},
				{Title: "Conclusion", Depth: 1, PageIdx: 2, YFrac: 0.15},
			},
		},
	})
	t.Cleanup(func() {
		preview.Shutdown()
		if tab := action.MainTab(); tab != nil {
			outline.CloseForTab(tab)
		}
		buffer.CloseOpenBuffers()
		action.DrainUIQueue()
	})

	doc := "= Introduction\nIntro body.\n\n= Methodology\nMethod details.\n\n= Conclusion\nFinal remarks.\n"
	b := buffer.NewBufferFromString(doc, "paper.typ", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{b})
	action.Tabs.Resize()
	tab := action.MainTab()
	bp := tab.CurPane()

	pp, ok := preview.Open(bp, false)
	require.True(t, ok)
	require.NotNil(t, pp)

	// Wait for mock compile to deliver the 3-page deck.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		action.DrainUIQueue()
		if !pp.IsCompiling() && pp.MaxScrollY() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.False(t, pp.IsCompiling())
	require.Greater(t, pp.MaxScrollY(), 0)

	// Initially preview is at page 1.
	fp, _, total := pp.PageRange()
	require.Equal(t, 3, total)
	require.Equal(t, 1, fp)

	// Open outline, select "Conclusion" (index 2), press Enter -> editor jumps AND preview repositions to page 3!
	op, ok := outline.Open(bp, true)
	require.True(t, ok)
	require.NotNil(t, op)
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))

	assert.Equal(t, 6, bp.Cursor.Y, "editor cursor should jump to Conclusion line")
	fp, _, _ = pp.PageRange()
	assert.Equal(t, 3, fp, "preview pane should reposition to page 3 for Conclusion")

	// Filter "/Method" in outline and press Enter -> preview repositions to page 2!
	outline.Open(bp, true)
	for _, r := range "/Method" {
		action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone, ""))
	}
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))

	assert.Equal(t, 3, bp.Cursor.Y, "editor cursor should jump to Methodology line")
	fp, _, _ = pp.PageRange()
	assert.Equal(t, 2, fp, "preview pane should reposition to page 2 for Methodology")
}

func TestWiredPickersInsertSymbolAndCiteOpenListPane(t *testing.T) {
	initTestEnv(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "typst.toml"), []byte("[package]\nname = \"demo\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "refs.bib"), []byte(`@article{vaswani2017,
  title = {Attention Is All You Need},
  author = {Ashish Vaswani},
  year = {2017},
}
`), 0o644))
	mainPath := filepath.Join(dir, "main.typ")
	require.NoError(t, os.WriteFile(mainPath, []byte("#bibliography(\"refs.bib\")\n"), 0o644))

	t.Cleanup(func() {
		if tab := action.MainTab(); tab != nil {
			lsp.CloseListPaneForTab(tab)
		}
		bib.ClearCache()
		buffer.CloseOpenBuffers()
		action.DrainUIQueue()
	})

	b, err := buffer.NewBufferFromFile(mainPath, buffer.BTDefault)
	require.NoError(t, err)
	action.InitTabs([]*buffer.Buffer{b})
	action.Tabs.Resize()
	tab := action.MainTab()
	bp := tab.CurPane()
	bp.Cursor.GotoLoc(buffer.Loc{X: 0, Y: 1})

	// 1. :symbol opens ListPane with SYMBOLS badge and inserts $subset.eq$ in markup mode.
	bp.HandleCommand("symbol subset.eq")
	lp := lsp.ListPaneForTab(tab)
	require.NotNil(t, lp)
	assert.Equal(t, "SYMBOLS", lp.Badge())
	assert.Equal(t, "Insert", lp.EnterLabel())
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	assert.Nil(t, lsp.ListPaneForTab(tab))
	assert.Contains(t, string(b.LineBytes(1)), "$subset.eq$")

	// 2. :cite opens ListPane with CITE badge and inserts @vaswani2017.
	b.Insert(buffer.Loc{X: len("$subset.eq$"), Y: 1}, " ")
	bp.Cursor.GotoLoc(buffer.Loc{X: len("$subset.eq$ "), Y: 1})
	bp.HandleCommand("cite vaswani")
	lp = lsp.ListPaneForTab(tab)
	require.NotNil(t, lp)
	assert.Equal(t, "CITE", lp.Badge())
	assert.Equal(t, "Insert", lp.EnterLabel())
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	assert.Nil(t, lsp.ListPaneForTab(tab))
	assert.Contains(t, string(b.LineBytes(1)), "@vaswani2017")

	// 3. :insert opens ListPane with SNIPPETS badge and expands a snippet.
	bp.HandleCommand("insert")
	lp = lsp.ListPaneForTab(tab)
	require.NotNil(t, lp)
	assert.Equal(t, "SNIPPETS", lp.Badge())
	assert.Equal(t, "Insert", lp.EnterLabel())
	for _, r := range "callout" {
		lp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone, ""))
	}
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	assert.Nil(t, lsp.ListPaneForTab(tab))
	assert.Contains(t, string(b.Bytes()), "#block(")
}

func TestOfflineCiteObserverBehavior(t *testing.T) {
	initTestEnv(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "refs.bib"), []byte(`@article{vaswani2017,
  title = {Attention Is All You Need},
  author = {Ashish Vaswani},
  year = {2017},
}
@inproceedings{vas2020intro,
  title = {Intro Paper},
  author = {Vas},
  year = {2020},
}
`), 0o644))
	mainPath := filepath.Join(dir, "main.typ")
	require.NoError(t, os.WriteFile(mainPath, []byte("#bibliography(\"refs.bib\")\n"), 0o644))

	t.Cleanup(func() {
		config.GlobalSettings["typst.autoComplete"] = true
		bib.ClearCache()
		buffer.CloseOpenBuffers()
		action.DrainUIQueue()
	})

	b, err := buffer.NewBufferFromFile(mainPath, buffer.BTDefault)
	require.NoError(t, err)
	win := display.NewBufWindow(0, 0, 80, 23, b)
	bp := action.NewBufPane(b, win, nil)
	require.True(t, lsp.IsOffline())

	// 1. Typing @vas in Markup mode populates HasSuggestions with two matches.
	bp.Cursor.GotoLoc(buffer.Loc{X: 0, Y: 1})
	for _, r := range "@vas" {
		bp.DoRuneInsert(r)
		buffer.FlushObservers()
	}
	require.True(t, b.HasSuggestions)
	assert.Equal(t, []string{"vas2020intro", "vaswani2017"}, b.Suggestions)
	assert.Equal(t, -1, b.CurSuggestion)

	// 2. Tab cycles via Autocomplete without OnBufferChange clobbering cycling.
	require.True(t, bp.Autocomplete())
	buffer.FlushObservers()
	assert.True(t, b.HasSuggestions)
	assert.Equal(t, 0, b.CurSuggestion)
	assert.Contains(t, string(b.LineBytes(1)), "@vas2020intro")

	require.True(t, bp.Autocomplete())
	buffer.FlushObservers()
	assert.True(t, b.HasSuggestions)
	assert.Equal(t, 1, b.CurSuggestion)
	assert.Contains(t, string(b.LineBytes(1)), "@vaswani2017")

	// 3. Single-match completion does not re-open HasSuggestions after Tab.
	b.Insert(buffer.Loc{X: len("@vaswani2017"), Y: 1}, " @vasw")
	bp.Cursor.GotoLoc(buffer.Loc{X: len("@vaswani2017 @vasw"), Y: 1})
	buffer.FlushObservers()
	require.True(t, b.HasSuggestions)
	assert.Equal(t, []string{"vaswani2017"}, b.Suggestions)
	require.True(t, bp.Autocomplete())
	buffer.FlushObservers()
	assert.False(t, b.HasSuggestions)

	// 4. Mid-identifier email and Math mode do not trigger offline citation suggestions.
	b.Insert(buffer.Loc{X: len(b.LineBytes(1)), Y: 1}, " user@vas")
	bp.Cursor.GotoLoc(buffer.Loc{X: len(b.LineBytes(1)), Y: 1})
	buffer.FlushObservers()
	assert.False(t, b.HasSuggestions)

	b.Insert(buffer.Loc{X: len(b.LineBytes(1)), Y: 1}, " $@vas$")
	bp.Cursor.GotoLoc(buffer.Loc{X: len(b.LineBytes(1)) - 1, Y: 1})
	buffer.FlushObservers()
	assert.False(t, b.HasSuggestions)

	// 5. typst.autoComplete=false disables offline @ suggestions.
	config.GlobalSettings["typst.autoComplete"] = false
	b.Insert(buffer.Loc{X: len(b.LineBytes(1)), Y: 1}, " @vas")
	bp.Cursor.GotoLoc(buffer.Loc{X: len(b.LineBytes(1)), Y: 1})
	buffer.FlushObservers()
	assert.False(t, b.HasSuggestions)
}

func TestSyncPreviewToOutlineItemSkipsKindFile(t *testing.T) {
	initTestEnv(t)
	if sim, ok := screen.Screen.(tcell.SimulationScreen); ok {
		sim.SetSize(140, 36)
	}
	preview.SetCapsForTest(preview.TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})
	preview.SetCompiler(&stubAnchoredCompiler{
		pages: 2,
		anchors: &preview.DocumentAnchors{
			Outlines: []preview.OutlineAnchor{
				{Title: "Intro", Depth: 1, PageIdx: 0, YFrac: 0.1},
				{Title: "End", Depth: 1, PageIdx: 1, YFrac: 0.5},
			},
		},
	})
	t.Cleanup(func() {
		preview.Shutdown()
		if tab := action.MainTab(); tab != nil {
			outline.CloseForTab(tab)
		}
		buffer.CloseOpenBuffers()
		action.DrainUIQueue()
	})

	b := buffer.NewBufferFromString("= Intro\nbody\n= End\n", "paper.typ", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{b})
	action.Tabs.Resize()
	tab := action.MainTab()
	bp := tab.CurPane()
	pp, ok := preview.Open(bp, false)
	require.True(t, ok)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		action.DrainUIQueue()
		if !pp.IsCompiling() && pp.MaxScrollY() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.False(t, pp.IsCompiling())

	// Selecting a KindFile header must not scroll preview.
	allItems := []outline.Item{
		{Kind: outline.KindFile, Title: "main.typ"},
		{Kind: outline.KindHeading, Level: 1, Title: "Intro", Line: 0},
		{Kind: outline.KindFile, Title: "chapters/end.typ"},
		{Kind: outline.KindHeading, Level: 1, Title: "End", Line: 2},
	}
	syncPreviewToOutlineItem(bp, allItems[2], allItems)
	assert.Equal(t, 0, pp.ScrollY())

	// Selecting a heading filters KindFile out of AllSections and scrolls to page 2.
	syncPreviewToOutlineItem(bp, allItems[3], allItems)
	fp, _, _ := pp.PageRange()
	assert.Equal(t, 2, fp)
}
