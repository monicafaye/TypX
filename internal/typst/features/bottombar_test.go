package features

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/display"
	"github.com/monicafaye/TypX/internal/lsp"
	"github.com/monicafaye/TypX/internal/screen"
	"github.com/monicafaye/TypX/internal/typst/outline"
	"github.com/monicafaye/TypX/internal/typst/preview"
	"github.com/monicafaye/TypX/internal/typst/templates"
)

func TestBottomBarEmptyBufferTemplatesHintAndMenu(t *testing.T) {
	initTestEnv(t)
	t.Cleanup(func() {
		if tab := action.MainTab(); tab != nil {
			templates.CloseForTab(tab)
		}
		buffer.CloseOpenBuffers()
		action.DrainUIQueue()
	})

	// 1. Empty .typ buffer promotes Alt+⇧N Templates at Priority 0.
	emptyBuf := buffer.NewBufferFromString("", "blank.typ", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{emptyBuf})
	action.Tabs.Resize()

	ctx := buildBottomBarContext()
	foundHint := false
	for _, h := range ctx.Hints {
		if h.Action == "ToggleTemplates" {
			foundHint = true
			assert.Equal(t, 0, h.Priority)
			assert.Equal(t, "Templates", h.Label)
		}
	}
	assert.True(t, foundHint, "expected ToggleTemplates hint on empty .typ buffer")

	// Also verify Alt+G Typst key menu row contains ToggleTemplates.
	foundMenu := false
	for _, row := range ctx.MenuRows {
		if row.Title == "Typst" {
			for _, it := range row.Items {
				if it.Action == "ToggleTemplates" {
					foundMenu = true
				}
			}
		}
	}
	assert.True(t, foundMenu, "expected ToggleTemplates in Typst Alt+G key menu")

	// 2. Typing content hides ToggleTemplates from the main bottom bar strip.
	emptyBuf.Insert(buffer.Loc{X: 0, Y: 0}, "= Heading\n")
	ctxAfter := buildBottomBarContext()
	for _, h := range ctxAfter.Hints {
		assert.NotEqual(t, "ToggleTemplates", h.Action)
	}

	// 3. Focusing TemplatePane switches bottom bar to TEMPLATES context.
	bp := action.MainTab().CurPane()
	tp, ok := templates.Open(bp, true)
	require.True(t, ok)
	require.NotNil(t, tp)

	tpCtx := buildBottomBarContext()
	assert.Equal(t, "TEMPLATES", tpCtx.Badge)
	assert.Contains(t, tpCtx.Hints, display.BottomBarItem{Key: "Enter", Label: "Apply", Action: "pane:Enter", Priority: 0})
	assert.Contains(t, tpCtx.Hints, display.BottomBarItem{Key: "u", Label: "Universe", Action: "pane:u", Priority: 1})
}

func TestBottomBarTemplatesHintWithoutTypFile(t *testing.T) {
	initTestEnv(t)
	t.Cleanup(func() {
		if tab := action.MainTab(); tab != nil {
			templates.CloseForTab(tab)
		}
		buffer.CloseOpenBuffers()
		action.DrainUIQueue()
	})

	// 1. Blank unnamed buffer promotes Alt+⇧N Templates at Priority 0.
	unnamed := buffer.NewBufferFromString("", "", buffer.BTDefault)
	require.False(t, unnamed.IsTypst())
	action.InitTabs([]*buffer.Buffer{unnamed})
	action.Tabs.Resize()

	ctx := buildBottomBarContext()
	foundHint := false
	for _, h := range ctx.Hints {
		if h.Action == "ToggleTemplates" {
			foundHint = true
			assert.Equal(t, 0, h.Priority)
			assert.Equal(t, "Templates", h.Label)
		}
	}
	assert.True(t, foundHint, "expected ToggleTemplates hint on blank unnamed buffer")

	// Typing into unnamed buffer hides Templates from the main strip, but keeps it in Alt+G key menu.
	unnamed.Insert(buffer.Loc{X: 0, Y: 0}, "hello\n")
	ctxTyped := buildBottomBarContext()
	for _, h := range ctxTyped.Hints {
		assert.NotEqual(t, "ToggleTemplates", h.Action)
	}
	foundMenu := false
	for _, row := range ctxTyped.MenuRows {
		for _, it := range row.Items {
			if it.Action == "ToggleTemplates" {
				foundMenu = true
			}
		}
	}
	assert.True(t, foundMenu, "expected ToggleTemplates in generic Alt+G key menu")

	// 2. Non-Typst named file (even if blank) keeps Templates in Alt+G menu without crowding the strip.
	mdBuf := buffer.NewBufferFromString("", "README.md", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{mdBuf})
	action.Tabs.Resize()

	mdCtx := buildBottomBarContext()
	for _, h := range mdCtx.Hints {
		assert.NotEqual(t, "ToggleTemplates", h.Action)
	}
	foundMDMenu := false
	for _, row := range mdCtx.MenuRows {
		for _, it := range row.Items {
			if it.Action == "ToggleTemplates" {
				foundMDMenu = true
			}
		}
	}
	assert.True(t, foundMDMenu, "expected ToggleTemplates in Alt+G key menu for non-Typst files")
}

func TestBottomBarListPaneContext(t *testing.T) {
	initTestEnv(t)
	t.Cleanup(func() {
		if tab := action.MainTab(); tab != nil {
			lsp.CloseListPaneForTab(tab)
		}
		buffer.CloseOpenBuffers()
		action.DrainUIQueue()
	})

	b := buffer.NewBufferFromString("= Title\n", "doc.typ", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{b})
	action.Tabs.Resize()
	tab := action.MainTab()
	bp := tab.CurPane()
	require.NotNil(t, bp)

	// 1. Diagnostics mode (OpenListPane(..., true, ...))
	diagLP := lsp.OpenListPane(bp, "Diagnostics", true, []lsp.ListItem{
		{Severity: lsp.SeverityError, Primary: "bad expr"},
	})
	require.NotNil(t, diagLP)
	diagCtx := buildBottomBarContext()
	assert.Equal(t, "DIAGNOSTICS", diagCtx.Badge)
	assert.Contains(t, diagCtx.Hints, display.BottomBarItem{Key: "Enter", Label: "Jump", Action: "pane:Enter", Priority: 0})
	assert.Contains(t, diagCtx.Hints, display.BottomBarItem{Key: "f", Label: "Errors", Action: "pane:f", Priority: 1, Active: false})

	diagLP.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'f', tcell.ModNone, ""))
	diagErrCtx := buildBottomBarContext()
	assert.Contains(t, diagErrCtx.Hints, display.BottomBarItem{Key: "f", Label: "Errors", Action: "pane:f", Priority: 1, Active: true})

	// 2. Custom picker (custom Badge, EnterLabel, StartFiltering, DiagnosticsMode=false)
	lsp.OpenListPaneWithOptions(bp, lsp.ListPaneOptions{
		Title:           "Math Symbols",
		StartFiltering:  true,
		EnterLabel:      "Insert",
		Badge:           "SYMBOLS",
		DiagnosticsMode: false,
		CloseOnSelect:   true,
	}, []lsp.ListItem{
		{Badge: "greek", Primary: "alpha", Secondary: "α"},
	})
	filterCtx := buildBottomBarContext()
	assert.Equal(t, "SYMBOLS", filterCtx.Badge)
	assert.Contains(t, filterCtx.Hints, display.BottomBarItem{Key: "Enter", Label: "Insert", Action: "pane:Enter", Priority: 0})
	assert.Contains(t, filterCtx.Hints, display.BottomBarItem{Key: "Esc", Label: "Clear Filter", Action: "pane:Esc", Priority: 0})
	for _, h := range filterCtx.Hints {
		assert.NotEqual(t, "f", h.Key)
	}

	// Exit filtering -> non-filtering custom picker keeps custom Badge & EnterLabel and omits 'f'.
	diagLP.HandleEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, ""))
	nonFilterCtx := buildBottomBarContext()
	assert.Equal(t, "SYMBOLS", nonFilterCtx.Badge)
	assert.Contains(t, nonFilterCtx.Hints, display.BottomBarItem{Key: "Enter", Label: "Insert", Action: "pane:Enter", Priority: 0})
	for _, h := range nonFilterCtx.Hints {
		assert.NotEqual(t, "f", h.Key)
	}

	// 3. Generic non-diagnostics list (OpenListPane(..., false, ...))
	lsp.OpenListPane(bp, "References", false, []lsp.ListItem{{Primary: "ref 1"}})
	refCtx := buildBottomBarContext()
	assert.Equal(t, "LIST", refCtx.Badge)
	assert.Contains(t, refCtx.Hints, display.BottomBarItem{Key: "Enter", Label: "Jump", Action: "pane:Enter", Priority: 0})
	for _, h := range refCtx.Hints {
		assert.NotEqual(t, "f", h.Key)
	}
}

func TestBottomBarNewTypstMenuMathOutlineAndPreviewHints(t *testing.T) {
	initTestEnv(t)
	if sim, ok := screen.Screen.(tcell.SimulationScreen); ok {
		sim.SetSize(120, 32)
	}
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.typ")
	subPath := filepath.Join(dir, "sub.typ")
	require.NoError(t, os.WriteFile(subPath, []byte("= Sub Heading\n"), 0o644))
	require.NoError(t, os.WriteFile(mainPath, []byte("= Main\n$alpha$\n#include \"sub.typ\"\n"), 0o644))

	preview.SetCapsForTest(preview.TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})
	preview.SetCompiler(&stubAnchoredCompiler{pages: 2})
	t.Cleanup(func() {
		preview.Shutdown()
		if tab := action.MainTab(); tab != nil {
			outline.CloseForTab(tab)
		}
		buffer.CloseOpenBuffers()
		action.DrainUIQueue()
	})

	b, err := buffer.NewBufferFromFile(mainPath, buffer.BTDefault)
	require.NoError(t, err)
	action.InitTabs([]*buffer.Buffer{b})
	action.Tabs.Resize()
	tab := action.MainTab()
	bp := tab.CurPane()

	// 1. Typst key menu includes SymbolPicker, CitePicker, SelectExpand, SelectShrink.
	markupCtx := buildBottomBarContext()
	var typstActions []string
	for _, row := range markupCtx.MenuRows {
		if row.Title == "Typst" {
			for _, it := range row.Items {
				typstActions = append(typstActions, it.Action)
			}
		}
	}
	assert.Contains(t, typstActions, "SymbolPicker")
	assert.Contains(t, typstActions, "CitePicker")
	assert.Contains(t, typstActions, "SelectExpand")
	assert.Contains(t, typstActions, "SelectShrink")

	// 2. ModeMath includes SymbolPicker (Alt+Y Symbol).
	bp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 1})
	mathCtx := buildBottomBarContext()
	assert.Equal(t, "MATH", mathCtx.Badge)
	assert.Contains(t, mathCtx.Hints, display.BottomBarItem{Key: "Alt+Y", Label: "Symbol", Action: "SymbolPicker", Priority: 0})

	// 3. Outline context includes Project toggle (`p`) with Active state.
	op, ok := outline.Open(bp, true)
	require.True(t, ok)
	require.NotNil(t, op)
	outCtx := buildBottomBarContext()
	assert.Equal(t, "OUTLINE", outCtx.Badge)
	assert.Contains(t, outCtx.Hints, display.BottomBarItem{Key: "p", Label: "Project", Action: "pane:p", Priority: 1, Active: true})
	op.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'p', tcell.ModNone, ""))
	outFileCtx := buildBottomBarContext()
	assert.Contains(t, outFileCtx.Hints, display.BottomBarItem{Key: "p", Label: "Project", Action: "pane:p", Priority: 1, Active: false})
	outline.CloseForTab(tab)

	// 4. Preview context includes h/l Pan, DblClick Source, and 0 Reset when zoomed.
	pp, ok := preview.Open(bp, true)
	require.True(t, ok)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		action.DrainUIQueue()
		_, _, total := pp.PageRange()
		if !pp.IsCompiling() && total == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	prevCtx := buildBottomBarContext()
	assert.Equal(t, "PREVIEW", prevCtx.Badge)
	assert.Contains(t, prevCtx.Hints, display.BottomBarItem{Key: "DblClick", Label: "Source", Priority: 4})
	for _, h := range prevCtx.Hints {
		assert.NotEqual(t, "0", h.Key)
	}
	for i := 0; i < 6; i++ {
		pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '+', tcell.ModNone, ""))
	}
	require.Greater(t, pp.ZoomPct(), 100)
	zoomCtx := buildBottomBarContext()
	assert.Contains(t, zoomCtx.Hints, display.BottomBarItem{Key: "0", Label: "Reset", Action: "pane:0", Priority: 2})
	if pp.MaxScrollX() > 0 {
		assert.Contains(t, zoomCtx.Hints, display.BottomBarItem{Key: "h/l", Label: "Pan", Priority: 2})
	}
}
