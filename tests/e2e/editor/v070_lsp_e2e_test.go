package editor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/tests/e2e/harness"
)

func TestE2E_LSPMultiCandidateCompletionSnippetAndEsc(t *testing.T) {
	harness.RequirePhase23(t)
	mockBin := harness.BuildMockTinymist(t)

	workDir := t.TempDir()
	docPath := filepath.Join(workDir, "comp.typ")
	require.NoError(t, os.WriteFile(docPath, []byte(""), 0o644))

	ed, _ := bootSimWithMockTinymist(t, mockBin, []string{docPath}, nil)
	cleanupV070PanesAndState(t)

	// 1. Auto-suggest on '#': Enter commits index 0 ("#alpha") without inserting a newline.
	typeText(ed, "#")
	gotAuto := ed.PumpUntil(4*time.Second, func() bool {
		return ed.Buf().HasSuggestions && len(ed.Buf().Suggestions) > 0
	})
	require.True(t, gotAuto, "expected auto-suggest suggestions on '#'")
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	assert.Equal(t, "#alpha", string(ed.Buf().LineBytes(0)))
	assert.Equal(t, 1, ed.Buf().LinesNum(), "Enter commit must not insert newline")
	assert.False(t, ed.Buf().HasSuggestions)

	// 2. Explicit Ctrl+Space after "#mk": immediately selects mkplain (index 0); Tab cycles to mksnip, Enter expands snippet tabstops.
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	typeText(ed, "#mk")
	ed.InjectKey(tcell.KeyCtrlSpace, 0, tcell.ModCtrl)
	gotMk := ed.PumpUntil(4*time.Second, func() bool {
		return ed.Buf().HasSuggestions && strings.Contains(string(ed.Buf().LineBytes(1)), "#mkplain")
	})
	require.True(t, gotMk, "expected Ctrl+Space to preview mkplain, got %q", string(ed.Buf().LineBytes(1)))

	ed.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	assert.Contains(t, string(ed.Buf().LineBytes(1)), "mksnip")

	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	typeText(ed, "a")
	ed.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(ed, "b")
	// Shift+Tab steps back to $1, then Tab advances to $2 and $0.
	ed.InjectKey(tcell.KeyBacktab, 0, tcell.ModNone)
	typeText(ed, "1")
	ed.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	ed.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(ed, "!")
	assert.Equal(t, "#mksnip(1, b)!", string(ed.Buf().LineBytes(1)))

	// 3. Esc reverts previewed suffix from Ctrl+Space / Tab cycling and clears HasSuggestions.
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	typeText(ed, "#mk")
	ed.InjectKey(tcell.KeyCtrlSpace, 0, tcell.ModCtrl)
	gotMk2 := ed.PumpUntil(4*time.Second, func() bool {
		return ed.Buf().HasSuggestions && strings.Contains(string(ed.Buf().LineBytes(2)), "#mkplain")
	})
	require.True(t, gotMk2)

	ed.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	assert.Equal(t, "#mk", string(ed.Buf().LineBytes(2)))
	assert.False(t, ed.Buf().HasSuggestions)
}

func TestE2E_DocumentHighlightDebounceRenderAndInvalidation(t *testing.T) {
	harness.RequirePhase23(t)
	mockBin := harness.BuildMockTinymist(t)

	workDir := t.TempDir()
	docPath := filepath.Join(workDir, "hl.typ")
	require.NoError(t, os.WriteFile(docPath, []byte("#let sharedVal = 1\n#let other = sharedVal + 2\n   \n"), 0o644))

	ed, traceFile := bootSimWithMockTinymist(t, mockBin, []string{docPath}, map[string]any{
		"colorscheme":             "catppuccin-mocha",
		"typst.documentHighlight": true,
	})
	cleanupV070PanesAndState(t)

	// 1. Place cursor on "sharedVal" on line 0 -> debounced documentHighlight populates 2 ranges.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 6, Y: 0})
	gotHL := ed.PumpUntil(4*time.Second, func() bool {
		return len(ed.Buf().SymbolHighlights) == 2
	})
	require.True(t, gotHL, "expected 2 documentHighlight ranges for sharedVal")

	recs, err := harness.ReadMockTrace(traceFile)
	require.NoError(t, err)
	assert.True(t, harness.HasTraceMethod(recs, "textDocument/documentHighlight"))

	// Highlighted occurrence on line 1 ("sharedVal") renders with distinct background from unhighlighted "other".
	line1 := ed.ScreenLine(1)
	hlCol := strings.Index(line1, "sharedVal")
	plainCol := strings.Index(line1, "other")
	require.GreaterOrEqual(t, hlCol, 0, "expected sharedVal on rendered line 1, got %q", line1)
	require.GreaterOrEqual(t, plainCol, 0, "expected other on rendered line 1, got %q", line1)
	hlRune, _, hlStyle, _ := ed.ScreenCell(hlCol, 1)
	plainRune, _, plainStyle, _ := ed.ScreenCell(plainCol, 1)
	assert.Equal(t, 's', hlRune)
	assert.Equal(t, 'o', plainRune)
	_, hlBg, _ := hlStyle.Decompose()
	_, plainBg, _ := plainStyle.Decompose()
	assert.NotEqual(t, plainBg, hlBg, "expected documentHighlight cell background to differ from plain identifier background")

	// 2. Moving cursor off symbol to whitespace on line 2 clears SymbolHighlights.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 1, Y: 2})
	cleared := ed.PumpUntil(4*time.Second, func() bool {
		return len(ed.Buf().SymbolHighlights) == 0
	})
	assert.True(t, cleared, "expected SymbolHighlights to clear when cursor leaves symbol")

	// 3. Re-highlight, then editing immediately clears SymbolHighlights.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 6, Y: 0})
	require.True(t, ed.PumpUntil(4*time.Second, func() bool {
		return len(ed.Buf().SymbolHighlights) == 2
	}))
	ed.Buf().Insert(buffer.Loc{X: 0, Y: 2}, "x")
	assert.Empty(t, ed.Buf().SymbolHighlights, "editing buffer must immediately clear SymbolHighlights")

	// 4. Move cursor within sharedVal to re-highlight, then disabling typst.documentHighlight clears on cursor move and skips RPC.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 7, Y: 0})
	require.True(t, ed.PumpUntil(4*time.Second, func() bool {
		return len(ed.Buf().SymbolHighlights) == 2
	}))
	ed.RunCommand("set typst.documentHighlight false")
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 8, Y: 0})
	ed.Pump(4)
	assert.Empty(t, ed.Buf().SymbolHighlights, "disabling typst.documentHighlight must clear SymbolHighlights on cursor move")
}

func TestE2E_SelectionRangeExpandAndShrink(t *testing.T) {
	harness.RequirePhase23(t)
	mockBin := harness.BuildMockTinymist(t)

	workDir := t.TempDir()
	docPath := filepath.Join(workDir, "sel.typ")
	require.NoError(t, os.WriteFile(docPath, []byte("#let alphaVar = 42\n#let betaVar = 99\n"), 0o644))

	ed, traceFile := bootSimWithMockTinymist(t, mockBin, []string{docPath}, nil)
	cleanupV070PanesAndState(t)

	origLoc := buffer.Loc{X: 7, Y: 0} // inside "alphaVar"
	ed.Pane().Cursor.GotoLoc(origLoc)

	// 1. Expand step 1 (Alt-w) -> selects token "alphaVar" (5..13).
	ed.InjectKey(tcell.KeyRune, 'w', tcell.ModAlt)
	ok1 := ed.PumpUntil(3*time.Second, func() bool {
		return ed.Pane().Cursor.HasSelection() &&
			ed.Pane().Cursor.CurSelection[0] == (buffer.Loc{X: 5, Y: 0}) &&
			ed.Pane().Cursor.CurSelection[1] == (buffer.Loc{X: 13, Y: 0})
	})
	require.True(t, ok1, "expected step 1 expand to select alphaVar, got %v", ed.Pane().Cursor.CurSelection)

	// 2. Expand step 2 (:select-expand) -> selects full line 0 (0..18).
	ed.RunCommand("select-expand")
	ok2 := ed.PumpUntil(3*time.Second, func() bool {
		return ed.Pane().Cursor.CurSelection[0] == (buffer.Loc{X: 0, Y: 0}) &&
			ed.Pane().Cursor.CurSelection[1] == (buffer.Loc{X: 18, Y: 0})
	})
	require.True(t, ok2, "expected step 2 expand to select line 0, got %v", ed.Pane().Cursor.CurSelection)

	// 3. Expand step 3 (Alt-w) -> selects full document (0,0)..(0,2).
	ed.InjectKey(tcell.KeyRune, 'w', tcell.ModAlt)
	ok3 := ed.PumpUntil(3*time.Second, func() bool {
		return ed.Pane().Cursor.CurSelection[0] == (buffer.Loc{X: 0, Y: 0}) &&
			ed.Pane().Cursor.CurSelection[1] == (buffer.Loc{X: 0, Y: 2})
	})
	require.True(t, ok3, "expected step 3 expand to select full document, got %v", ed.Pane().Cursor.CurSelection)

	recs, err := harness.ReadMockTrace(traceFile)
	require.NoError(t, err)
	assert.True(t, harness.HasTraceMethod(recs, "textDocument/selectionRange"))

	// 4. Shrink step 1 (Alt-W / Alt+Shift+W) -> back to line 0.
	ed.InjectKey(tcell.KeyRune, 'W', tcell.ModAlt|tcell.ModShift)
	assert.Equal(t, [2]buffer.Loc{{X: 0, Y: 0}, {X: 18, Y: 0}}, ed.Pane().Cursor.CurSelection)

	// 5. Shrink step 2 (:select-shrink) -> back to token "alphaVar".
	ed.RunCommand("select-shrink")
	assert.Equal(t, [2]buffer.Loc{{X: 5, Y: 0}, {X: 13, Y: 0}}, ed.Pane().Cursor.CurSelection)

	// 6. Shrink step 3 (Alt-W) -> back to origLoc with no selection.
	ed.InjectKey(tcell.KeyRune, 'W', tcell.ModAlt)
	assert.False(t, ed.Pane().Cursor.HasSelection())
	assert.Equal(t, origLoc, ed.Cursor())
}

func TestE2E_BrowserPreviewReverseSyncNotifications(t *testing.T) {
	harness.RequirePhase23(t)
	mockBin := harness.BuildMockTinymist(t)

	workDir := t.TempDir()
	mainPath := filepath.Join(workDir, "main.typ")
	subPath := filepath.Join(workDir, "sub.typ")
	require.NoError(t, os.WriteFile(mainPath, []byte("= Main\n"), 0o644))
	// Line 1: '😀' is 2 UTF-16 code units (1 rune/cluster), so UTF-16 col 5 == Loc.X 4 ('t' in "target").
	require.NoError(t, os.WriteFile(subPath, []byte("line zero\n😀 + target\n"), 0o644))

	ed, traceFile := bootSimWithMockTinymist(t, mockBin, []string{mainPath}, map[string]any{
		"typst.gotoOpenMode": "tab",
	})
	cleanupV070PanesAndState(t)

	// 1. Trigger tinymist/preview/scrollSource for sub.typ:1:5 -> opens sub.typ tab at Loc{X: 4, Y: 1}.
	ed.Buf().Insert(ed.Buf().End(), "// MOCK_SCROLL_SOURCE:sub.typ:1:5\n")
	gotScrollSource := ed.PumpUntil(4*time.Second, func() bool {
		return filepath.Clean(ed.Buf().AbsPath) == filepath.Clean(subPath) &&
			ed.Cursor() == (buffer.Loc{X: 4, Y: 1})
	})
	require.True(t, gotScrollSource, "expected tinymist/preview/scrollSource to jump to sub.typ (4,1), got path=%q loc=%v", ed.Buf().AbsPath, ed.Cursor())
	assert.Len(t, action.Tabs.List, 2)

	// 2. Trigger editorScrollTo on sub.typ (SELF:0:4) -> jumps cursor in current file to (4, 0).
	ed.Buf().Insert(ed.Buf().End(), "// MOCK_EDITOR_SCROLL_TO:SELF:0:4\n")
	gotEditorScrollTo := ed.PumpUntil(4*time.Second, func() bool {
		return ed.Cursor() == (buffer.Loc{X: 4, Y: 0})
	})
	require.True(t, gotEditorScrollTo, "expected editorScrollTo to jump to (4,0), got %v", ed.Cursor())

	recs, err := harness.ReadMockTrace(traceFile)
	require.NoError(t, err)
	hasScrollSource := false
	hasEditorScroll := false
	for _, r := range recs {
		if r.Event == "emit_scroll_source" {
			hasScrollSource = true
		}
		if r.Event == "emit_editor_scroll_to" {
			hasEditorScroll = true
		}
	}
	assert.True(t, hasScrollSource)
	assert.True(t, hasEditorScroll)
}
