package editor_test

import (
	"strings"
	"testing"

	"github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/tests/e2e/harness"
)

func TestE2E_BottomBarContextHintsClicksAndKeyMenu(t *testing.T) {
	harness.RequirePhase23(t)

	ed := harness.NewSimEditor(t, harness.SimOptions{
		InitialName: "paper.typ",
		InitialText: "= Intro\nSome prose.\n$alpha + beta$\n#let x = 1\n",
		Settings: map[string]any{
			"colorscheme":      "typx-dark",
			"typst.lspEnabled": false,
			"typst.keyHints":   true,
			"ruler":            false,
			"softwrap":         false,
		},
		Width:  120,
		Height: 24,
	})

	// 1. Idle Markup mode shows helpful TypX shortcuts and active Conceal indicator.
	bar := ed.InfoBarText()
	assert.Contains(t, bar, "^S Save")
	assert.Contains(t, bar, "Alt+P Preview")
	assert.Contains(t, bar, "Alt+O Outline")
	assert.Contains(t, bar, "Alt+C Conceal●")
	assert.Contains(t, bar, "Alt+G Keys")
	assert.Contains(t, bar, "^Q Quit")

	// Statusline right side shows percentage and encoding cleanly on wide screen.
	status := ed.StatusLineText()
	assert.Contains(t, status, "MARKUP |")
	assert.Contains(t, status, "% | utf-8")

	// 2. Moving cursor into math block switches bottom bar to MATH context hints.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 2, Y: 2})
	ed.Pump(4)
	mathBar := ed.InfoBarText()
	assert.Contains(t, mathBar, "MATH")
	assert.Contains(t, mathBar, "Alt+I Snippets")
	assert.Contains(t, mathBar, "^Space Complete")
	assert.Contains(t, mathBar, "Alt+M Exit Math")

	// 3. Moving cursor into code expression switches bottom bar to CODE context hints.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 3, Y: 3})
	ed.Pump(4)
	codeBar := ed.InfoBarText()
	assert.Contains(t, codeBar, "CODE")
	assert.Contains(t, codeBar, "^Space Complete")
	assert.Contains(t, codeBar, "Alt+K Hover")

	// 4. Return to line 0 (Markup) and click "Alt+O Outline" on the bottom bar -> opens Outline!
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 0, Y: 0})
	ed.Pump(4)
	bar = ed.InfoBarText()
	outlineX := strings.Index(bar, "Alt+O")
	require.Greater(t, outlineX, 0, "expected Alt+O in bottom bar: %q", bar)

	_, h := ed.Sim.Size()
	ed.InjectMouse(outlineX+2, h-1, tcell.Button1, tcell.ModNone)
	ed.InjectMouse(outlineX+2, h-1, tcell.ButtonNone, tcell.ModNone)

	tab := action.MainTab()
	require.Len(t, tab.Panes, 2, "clicking Alt+O on bottom bar should open Document Outline")
	assert.Equal(t, "Outline", tab.ActivePane().Name())

	// Bottom bar should now show Outline navigation hints.
	outlineBar := ed.InfoBarText()
	assert.Contains(t, outlineBar, "OUTLINE")
	assert.Contains(t, outlineBar, "Enter Jump")
	assert.Contains(t, outlineBar, "/ Filter")
	assert.Contains(t, outlineBar, "Esc Editor")

	// Click "Esc Editor" on bottom bar -> returns focus to editor, and Outline shows active dot!
	escX := strings.Index(outlineBar, "Esc Editor")
	require.Greater(t, escX, 0)
	ed.InjectMouse(escX+1, h-1, tcell.Button1, tcell.ModNone)
	ed.InjectMouse(escX+1, h-1, tcell.ButtonNone, tcell.ModNone)
	assert.False(t, action.IsAuxiliaryPane(tab.ActivePane()))
	assert.Contains(t, ed.InfoBarText(), "Alt+O Outline●")

	// 5. Clicking "Alt+C Conceal●" toggles concealment off and removes the active dot.
	concealBar := ed.InfoBarText()
	concealX := strings.Index(concealBar, "Alt+C")
	require.Greater(t, concealX, 0)
	ed.InjectMouse(concealX+1, h-1, tcell.Button1, tcell.ModNone)
	ed.InjectMouse(concealX+1, h-1, tcell.ButtonNone, tcell.ModNone)
	assert.Equal(t, false, ed.Buf().Settings["typst.conceal"])
	// Message "Concealment disabled" appears; clicking bottom bar dismisses it back to idle hints.
	assert.Contains(t, ed.InfoBarText(), "Concealment disabled")
	ed.InjectMouse(5, h-1, tcell.Button1, tcell.ModNone)
	ed.InjectMouse(5, h-1, tcell.ButtonNone, tcell.ModNone)
	assert.Contains(t, ed.InfoBarText(), "Alt+C Conceal")
	assert.NotContains(t, ed.InfoBarText(), "Alt+C Conceal●")

	// 6. Clicking "Alt+G Keys" opens the 2-row TypX expanded key menu.
	keysBar := ed.InfoBarText()
	keysX := strings.Index(keysBar, "Alt+G")
	require.Greater(t, keysX, 0)
	ed.InjectMouse(keysX+1, h-1, tcell.Button1, tcell.ModNone)
	ed.InjectMouse(keysX+1, h-1, tcell.ButtonNone, tcell.ModNone)

	rowEditor := ed.ScreenLine(h - 3)
	rowTypst := ed.ScreenLine(h - 2)
	rowBottom := ed.ScreenLine(h - 1)
	assert.Contains(t, rowEditor, "Editor")
	assert.Contains(t, rowEditor, "^S Save")
	assert.Contains(t, rowTypst, "Typst")
	assert.Contains(t, rowTypst, "Alt+P Preview")
	assert.Contains(t, rowBottom, "KEYS")
	assert.Contains(t, rowBottom, "Alt+G Hide")

	// Clicking "Alt+G Hide" closes the key menu cleanly.
	hideX := strings.Index(rowBottom, "Alt+G Hide")
	require.Greater(t, hideX, 0)
	ed.InjectMouse(hideX+1, h-1, tcell.Button1, tcell.ModNone)
	ed.InjectMouse(hideX+1, h-1, tcell.ButtonNone, tcell.ModNone)
	assert.NotContains(t, ed.InfoBarText(), "Alt+G Hide")
	assert.Contains(t, ed.InfoBarText(), "Alt+G Keys")

	// 7. Diagnostic errors surface a clickable error badge on the bottom bar.
	ed.Buf().AddMessage(buffer.NewMessage("tinymist", "unknown variable", buffer.Loc{X: 0, Y: 3}, buffer.Loc{X: 4, Y: 3}, buffer.MTError))
	ed.Render()
	diagBar := ed.InfoBarText()
	assert.Contains(t, diagBar, "✖ 1")
	assert.Contains(t, diagBar, "Error")

	// 8. Disabling typst.keyHints restores blank idle bottom bar.
	ed.RunCommand("set typst.keyHints false")
	ed.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	assert.Equal(t, "", ed.InfoBarText())
}
