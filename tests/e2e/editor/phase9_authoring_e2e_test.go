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
	"github.com/monicafaye/TypX/internal/typst/preview"
	"github.com/monicafaye/TypX/tests/e2e/harness"
)

func typeText(ed *harness.SimEditor, s string) {
	for _, r := range s {
		ed.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
}

func TestE2E_SmartListContinuationAndSnippetTabstops(t *testing.T) {
	harness.RequirePhase23(t)
	docPath := filepath.Join(t.TempDir(), "authoring.typ")
	require.NoError(t, os.WriteFile(docPath, []byte("- first item"), 0644))

	ed := harness.NewSimEditor(t, harness.SimOptions{
		Files:  []string{docPath},
		Width:  100,
		Height: 30,
		Settings: map[string]any{
			"typst.lspEnabled": false,
		},
	})

	// 1. Enter at end of bullet item continues list; second Enter exits list.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 12, Y: 0})
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	assert.Equal(t, "- first item\n- ", string(ed.Buf().Bytes()))

	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	assert.Equal(t, "- first item\n", string(ed.Buf().Bytes()))

	// 2. :insert frac activates multi-stop Tab / Shift+Tab navigation.
	ed.RunCommand("insert frac")
	typeText(ed, "x + 1")
	ed.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(ed, "y")
	ed.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(ed, " = 0")

	assert.Equal(t, "- first item\nfrac(x + 1, y) = 0", string(ed.Buf().Bytes()))
}

func TestE2E_SignatureHelpAndAutoComplete(t *testing.T) {
	harness.RequirePhase23(t)
	mockBin := harness.BuildMockTinymist(t)

	workDir := t.TempDir()
	docPath := filepath.Join(workDir, "sig.typ")
	require.NoError(t, os.WriteFile(docPath, []byte(""), 0644))

	ed, _ := bootSimWithMockTinymist(t, mockBin, []string{docPath}, nil)

	// 1. Typing '#' triggers non-mutating auto-completion suggestions on statusline.
	typeText(ed, "#")
	gotSuggestions := ed.PumpUntil(4*time.Second, func() bool {
		return ed.Buf().HasSuggestions && len(ed.Buf().Suggestions) > 0
	})
	require.True(t, gotSuggestions, "expected auto-completion suggestions after typing '#'")
	assert.Equal(t, "#", string(ed.Buf().LineBytes(0)), "auto-completion must not mutate buffer before Tab")

	// Pressing Tab inserts the first completion suggestion.
	ed.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	assert.Equal(t, "#alpha", string(ed.Buf().LineBytes(0)))

	// 2. Typing '(' triggers signature help automatically, and Alt+H works explicitly.
	typeText(ed, "(")
	gotSig := ed.PumpUntil(4*time.Second, func() bool {
		return action.InfoBar != nil && strings.Contains(action.InfoBar.Msg, "»columns: auto«")
	})
	require.True(t, gotSig, "expected auto signature help on '('; got %q", action.InfoBar.Msg)

	// Explicit Alt+H refreshes signature help after moving to second argument.
	typeText(ed, "2, ")
	ed.InjectKey(tcell.KeyRune, 'h', tcell.ModAlt)
	gotSig2 := ed.PumpUntil(4*time.Second, func() bool {
		return action.InfoBar != nil && strings.Contains(action.InfoBar.Msg, "»body: content«")
	})
	require.True(t, gotSig2, "expected Alt+H signature help for second parameter; got %q", action.InfoBar.Msg)
}

func TestE2E_PreviewSyncToggleAndPageCommand(t *testing.T) {
	harness.RequirePhase23(t)
	docPath := filepath.Join(t.TempDir(), "sync.typ")
	require.NoError(t, os.WriteFile(docPath, []byte("= Intro\n\nBody\n"), 0644))

	prevCaps := preview.Caps()
	preview.SetCapsForTest(preview.TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})
	t.Cleanup(func() {
		preview.CloseForTab(action.MainTab())
		preview.SetCapsForTest(prevCaps)
	})

	ed := harness.NewSimEditor(t, harness.SimOptions{
		Files:  []string{docPath},
		Width:  100,
		Height: 30,
		Settings: map[string]any{
			"typst.lspEnabled":        false,
			"typst.previewSyncCursor": false,
		},
	})

	pp, ok := preview.Open(ed.Pane(), true)
	require.True(t, ok)
	require.NotNil(t, pp)
	assert.False(t, pp.IsSyncCursor())

	// Pressing 'f' in focused PreviewPane toggles cursor-follow sync on and off.
	ed.InjectKey(tcell.KeyRune, 'f', tcell.ModNone)
	assert.True(t, pp.IsSyncCursor())
	ed.InjectKey(tcell.KeyRune, 'f', tcell.ModNone)
	assert.False(t, pp.IsSyncCursor())

	// :preview sync toggles cursor-follow sync from the command bar.
	ed.RunCommand("preview sync")
	assert.True(t, pp.IsSyncCursor())
	assert.Contains(t, action.InfoBar.Msg, "cursor sync enabled")

	// Invalid page number reports an error cleanly.
	ed.RunCommand("preview page abc")
	assert.Contains(t, action.InfoBar.Msg, "Invalid preview page number")
}
