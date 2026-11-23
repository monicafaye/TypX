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

	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/lsp"
	"github.com/monicafaye/TypX/tests/e2e/harness"
)

func TestE2E_Phase2_ConcealRenderToggleAndMouseClick(t *testing.T) {
	harness.RequirePhase23(t)

	ed := harness.NewSimEditor(t, harness.SimOptions{
		InitialName: "math.typ",
		InitialText: "= Intro\n$alpha -> beta$\n$sin(x) + index + summary$\n",
		Settings: map[string]any{
			"typst.lspEnabled": false,
			"ruler":            false,
			"softwrap":         false,
		},
		Width:  80,
		Height: 20,
	})

	// Cursor is on line 0; line 1 is inactive and must be concealed,
	// line 2 must preserve sin/index/summary untouched.
	assert.Equal(t, "= Intro", ed.ScreenLine(0))
	assert.Equal(t, "$α → β$", ed.ScreenLine(1))
	assert.Equal(t, "$sin(x) + index + summary$", ed.ScreenLine(2))

	// Statusline must display MARKUP, word count, and LSP: off.
	status := ed.StatusLineText()
	assert.Contains(t, status, "MARKUP |")
	assert.Contains(t, status, "words |")
	assert.Contains(t, status, "LSP: off |")

	// Mouse click on 'β' in concealed line 1 ("$α → β$" -> x=5, y=1)
	// must land on exact buffer column of "beta" (col 10 in "$alpha -> beta$").
	ed.InjectMouse(5, 1, tcell.Button1, tcell.ModNone)
	ed.InjectMouse(5, 1, tcell.ButtonNone, tcell.ModNone)
	assert.Equal(t, buffer.Loc{X: 10, Y: 1}, ed.Cursor())

	// Now line 1 is active -> must unconceal to raw Typst source,
	// and statusline mode must switch to MATH!
	assert.Equal(t, "$alpha -> beta$", ed.ScreenLine(1))
	assert.Contains(t, ed.StatusLineText(), "MATH |")

	// Move back to line 0 -> line 1 conceals again.
	ed.InjectKey(tcell.KeyUp, 0, tcell.ModNone)
	assert.Equal(t, "$α → β$", ed.ScreenLine(1))

	// Alt-c toggles concealment off live.
	ed.InjectKey(tcell.KeyRune, 'c', tcell.ModAlt)
	assert.Equal(t, "$alpha -> beta$", ed.ScreenLine(1))

	// :conceal on turns it back on live.
	ed.RunCommand("conceal on")
	assert.Equal(t, "$α → β$", ed.ScreenLine(1))

	// Underlying buffer bytes are never mutated by concealment.
	assert.Contains(t, string(ed.Buf().Bytes()), "$alpha -> beta$")
}

func TestE2E_Phase2_ShortcutsTemplatesAndSmartPairsWithAutocloseLoaded(t *testing.T) {
	harness.RequirePhase23(t)

	ed := harness.NewSimEditor(t, harness.SimOptions{
		InitialName: "shortcuts.typ",
		InitialText: "= Title\n",
		Settings: map[string]any{
			"typst.lspEnabled": false,
			"typst.smartPairs": true,
		},
		Width:  80,
		Height: 20,
	})

	// Alt-Right demotes heading: "= Title" -> "== Title"
	ed.InjectKey(tcell.KeyRight, 0, tcell.ModAlt)
	assert.Equal(t, "== Title", string(ed.Buf().LineBytes(0)))

	// Alt-Left promotes heading: "== Title" -> "= Title"
	ed.InjectKey(tcell.KeyLeft, 0, tcell.ModAlt)
	assert.Equal(t, "= Title", string(ed.Buf().LineBytes(0)))

	// Move to line 1 and test smart pair '$' + skip-over + Backspace delete pair.
	ed.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	ed.InjectRune('$')
	assert.Equal(t, "$$", string(ed.Buf().LineBytes(1)))
	assert.Equal(t, buffer.Loc{X: 1, Y: 1}, ed.Cursor())

	// Typing '$' inside $|$ skips over closing '$'.
	ed.InjectRune('$')
	assert.Equal(t, "$$", string(ed.Buf().LineBytes(1)))
	assert.Equal(t, buffer.Loc{X: 2, Y: 1}, ed.Cursor())

	// Move left into $|$ and press Backspace -> deletes empty pair.
	ed.InjectKey(tcell.KeyLeft, 0, tcell.ModNone)
	ed.InjectKey(tcell.KeyBackspace2, 0, tcell.ModNone)
	assert.Equal(t, "", string(ed.Buf().LineBytes(1)))

	// Verify autoclose.lua does NOT corrupt raw span when typing '(' inside backticks!
	ed.InjectString("`(")
	assert.Equal(t, "`(", string(ed.Buf().LineBytes(1)))

	// Clear line 1 and test Alt-m (MathInline) and :insert-frac
	ed.Buf().Remove(buffer.Loc{X: 0, Y: 1}, buffer.Loc{X: 2, Y: 1})
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 0, Y: 1})
	ed.InjectKey(tcell.KeyRune, 'm', tcell.ModAlt)
	assert.Equal(t, "$$", string(ed.Buf().LineBytes(1)))
	assert.Equal(t, buffer.Loc{X: 1, Y: 1}, ed.Cursor())

	ed.RunCommand("insert-frac")
	assert.Equal(t, "$frac(, )$", string(ed.Buf().LineBytes(1)))
	assert.Equal(t, buffer.Loc{X: 6, Y: 1}, ed.Cursor())

	// Test Alt-b (WrapBold) and Alt-e (WrapItalic) on fresh line
	ed.Buf().Insert(ed.Buf().End(), "\nhello")
	c := ed.Pane().Cursor
	c.SetSelectionStart(buffer.Loc{X: 0, Y: 2})
	c.SetSelectionEnd(buffer.Loc{X: 5, Y: 2})
	ed.InjectKey(tcell.KeyRune, 'b', tcell.ModAlt)
	assert.Equal(t, "*hello*", string(ed.Buf().LineBytes(2)))
}

func TestE2E_Phase3_MockTinymistDiagnosticsCompletionHoverAndPreview(t *testing.T) {
	harness.RequirePhase23(t)

	mockBin := harness.BuildMockTinymist(t)
	mockEnv, _, traceFile, _ := harness.MockEnv(t, harness.MockOptions{})
	for k, v := range mockEnv {
		t.Setenv(k, v)
	}

	workDir := t.TempDir()
	typFile := filepath.Join(workDir, "live.typ")
	// Surrogate pair '𝕏' (U+1D54F, 2 UTF-16 code units, 1 rune/char) before ERR_TRIGGER:
	// '𝕏' (col 0), ' ' (col 1), '+' (col 2), ' ' (col 3), 'E' (col 4) -> buffer Loc.X == 4.
	require.NoError(t, os.WriteFile(typFile, []byte("𝕏 + ERR_TRIGGER\n#fig\n"), 0644))

	ed := harness.NewSimEditor(t, harness.SimOptions{
		Files: []string{typFile},
		Settings: map[string]any{
			"basename":                 true,
			"typst.lspEnabled":         true,
			"typst.tinymistPath":       mockBin,
			"typst.debounceMs":         30,
			"typst.previewOpenBrowser": false,
		},
		Width:  100,
		Height: 24,
	})
	t.Cleanup(func() { lsp.Shutdown() })

	// Ensure LSP starts with the mock binary for this test (even if another test initialized SimEditor earlier).
	lsp.Init(lsp.Options{})
	ed.RunCommand("tinymist-restart")

	// Wait for diagnostic on line 0 with accurate UTF-16 -> rune column conversion (X == 4).
	msgs := ed.WaitDiagnostics(5*time.Second, 1)
	require.GreaterOrEqual(t, len(msgs), 1)
	assert.Equal(t, 0, msgs[0].Start.Y)
	assert.Equal(t, 4, msgs[0].Start.X, "surrogate pair '𝕏' must map UTF-16 offset 5 to rune col 4")
	assert.Contains(t, msgs[0].Msg, "ERR_TRIGGER")

	// Statusline should show LSP: ready |
	assert.Contains(t, ed.StatusLineText(), "LSP: ready |")

	// Hover via Alt-k on line 0, col 4 ("ERR_TRIGGER") -> InfoBar displays hover text
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 4, Y: 0})
	ed.InjectKey(tcell.KeyRune, 'k', tcell.ModAlt)
	ok := ed.PumpUntil(3*time.Second, func() bool {
		msg, _, _, _ := ed.InfoBarState()
		return strings.Contains(msg, "mock hover docs") && strings.Contains(msg, "ERR_TRIGGER")
	})
	assert.True(t, ok, "expected hover message in InfoBar, got %q", ed.InfoBarText())

	// Completion via Ctrl+Space on line 1 after "#fig" -> autocompletes to "#figure"
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 4, Y: 1})
	ed.InjectKey(tcell.KeyCtrlSpace, 0, tcell.ModCtrl)
	ok = ed.PumpUntil(3*time.Second, func() bool {
		return strings.Contains(string(ed.Buf().LineBytes(1)), "#figure")
	})
	assert.True(t, ok, "expected Ctrl+Space completion to expand #fig to #figure, got %q", string(ed.Buf().LineBytes(1)))

	// Toggle preview via :preview -> statusline displays PREVIEW: ON
	ed.RunCommand("preview")
	ok = ed.PumpUntil(3*time.Second, func() bool {
		return strings.Contains(ed.StatusLineText(), "PREVIEW: ON |")
	})
	assert.True(t, ok, "expected PREVIEW: ON in statusline, got %q", ed.StatusLineText())

	// Edit buffer to remove ERR_TRIGGER -> debounced didChange clears diagnostics!
	ed.Buf().Remove(buffer.Loc{X: 0, Y: 0}, buffer.Loc{X: 15, Y: 0})
	ed.Buf().Insert(buffer.Loc{X: 0, Y: 0}, "clean text")
	ed.WaitNoDiagnostics(5 * time.Second)

	// Toggle preview off via :preview
	ed.RunCommand("preview")
	ok = ed.PumpUntil(3*time.Second, func() bool {
		return !strings.Contains(ed.StatusLineText(), "PREVIEW: ON")
	})
	assert.True(t, ok)

	// Clean shutdown sends shutdown + exit to mock tinymist.
	lsp.Shutdown()
	recs, err := harness.ReadMockTrace(traceFile)
	require.NoError(t, err)
	assert.True(t, harness.HasTraceMethod(recs, "initialize"))
	assert.True(t, harness.HasTraceMethod(recs, "textDocument/hover"))
	assert.True(t, harness.HasTraceMethod(recs, "textDocument/completion"))
	assert.True(t, harness.HasTraceMethod(recs, "workspace/executeCommand"))
	assert.True(t, harness.HasTraceMethod(recs, "shutdown"))
	assert.True(t, harness.HasTraceMethod(recs, "exit"))
}
