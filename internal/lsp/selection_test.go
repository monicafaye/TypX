package lsp

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
)

func TestSelectionRangeExpandShrinkAndBindings(t *testing.T) {
	var posMu sync.Mutex
	var queriedPositions []Position

	dir, _, latestSrv := setupM4bPipeEnv(t, 0, m4bFakeServerConfig{
		SelectionRangeProvider: true,
		OnSelectionRange: func(params SelectionRangeParams) (any, *RPCError) {
			posMu.Lock()
			queriedPositions = append(queriedPositions, params.Positions...)
			posMu.Unlock()
			// Line 0: "a😀b foo(bar)\n"
			// Level 1: "foo" -> UTF-16 [5, 8) -> Loc [4, 7)
			// Level 2: "foo(bar)" -> UTF-16 [5, 13) -> Loc [4, 12)
			// Level 3: full line -> [0, 13) -> Loc [0, 12)
			return []SelectionRange{{
				Range: Range{Start: Position{Line: 0, Character: 5}, End: Position{Line: 0, Character: 8}},
				Parent: &SelectionRange{
					Range: Range{Start: Position{Line: 0, Character: 5}, End: Position{Line: 0, Character: 13}},
					Parent: &SelectionRange{
						Range: Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 1, Character: 0}},
					},
				},
			}}, nil
		},
	})

	mainPath := filepath.Join(dir, "main.typ")
	Init(Options{InitialFiles: []string{mainPath}})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForM4b(t, 3*time.Second, func() bool {
		return defaultManager.State() == StateReady && latestSrv() != nil
	}, "StateReady")

	b := openM4bBuffer(t, mainPath, "a😀b foo(bar)\nnext\n")
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()
	origLoc := buffer.Loc{X: 5, Y: 0} // inside "foo", UTF-16 col 6
	bp.Cursor.GotoLoc(origLoc)

	// Verify Alt-w and Alt-W / Alt+Shift+W default bindings dispatch SelectExpand / SelectShrink.
	bp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'w', tcell.ModAlt, ""))
	waitForM4b(t, 2*time.Second, func() bool {
		return bp.Cursor.HasSelection()
	}, "level 1 selection")
	assert.Equal(t, [2]buffer.Loc{{X: 4, Y: 0}, {X: 7, Y: 0}}, bp.Cursor.CurSelection)
	assert.Equal(t, buffer.Loc{X: 7, Y: 0}, bp.Cursor.Loc)

	// Level 2 via :select-expand command.
	SelectExpandCommand()(bp, nil)
	waitForM4b(t, 2*time.Second, func() bool {
		return bp.Cursor.CurSelection[1] == (buffer.Loc{X: 12, Y: 0})
	}, "level 2 selection")
	assert.Equal(t, [2]buffer.Loc{{X: 4, Y: 0}, {X: 12, Y: 0}}, bp.Cursor.CurSelection)

	// Level 3 via Alt-w.
	assert.True(t, SelectExpandAction()(bp))
	waitForM4b(t, 2*time.Second, func() bool {
		return bp.Cursor.CurSelection[0] == (buffer.Loc{X: 0, Y: 0}) && bp.Cursor.CurSelection[1] == (buffer.Loc{X: 0, Y: 1})
	}, "level 3 selection")

	// All 3 requests must have queried the original anchor originLoc (UTF-16 col 6), not the moving CurSelection[1].
	posMu.Lock()
	require.Len(t, queriedPositions, 3)
	for _, qp := range queriedPositions {
		assert.Equal(t, Position{Line: 0, Character: 6}, qp)
	}
	posMu.Unlock()

	// Extra expand reports "No larger selection range" and preserves selection.
	assert.True(t, SelectExpandAction()(bp))
	waitForM4b(t, 2*time.Second, func() bool {
		return action.InfoBar.Msg == "No larger selection range"
	}, "no larger selection range msg")
	assert.Equal(t, [2]buffer.Loc{{X: 0, Y: 0}, {X: 0, Y: 1}}, bp.Cursor.CurSelection)

	// Shrink step 1 via Alt+Shift+W fallback key event -> back to level 2.
	bp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'W', tcell.ModAlt|tcell.ModShift, ""))
	assert.Equal(t, [2]buffer.Loc{{X: 4, Y: 0}, {X: 12, Y: 0}}, bp.Cursor.CurSelection)
	assert.Equal(t, buffer.Loc{X: 12, Y: 0}, bp.Cursor.Loc)

	// Shrink step 2 via :select-shrink -> back to level 1.
	SelectShrinkCommand()(bp, nil)
	assert.Equal(t, [2]buffer.Loc{{X: 4, Y: 0}, {X: 7, Y: 0}}, bp.Cursor.CurSelection)
	assert.Equal(t, buffer.Loc{X: 7, Y: 0}, bp.Cursor.Loc)

	// Shrink step 3 via Alt-W -> back to original cursor with no selection.
	bp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'W', tcell.ModAlt, ""))
	assert.False(t, bp.Cursor.HasSelection())
	assert.Equal(t, origLoc, bp.Cursor.Loc)

	// Extra shrink notifies and returns false.
	assert.False(t, SelectShrinkAction()(bp))
	assert.Equal(t, "No selection history", action.InfoBar.Msg)
}

func TestSelectionRangeManualSelectionAndInvalidation(t *testing.T) {
	dir, _, latestSrv := setupM4bPipeEnv(t, 0, m4bFakeServerConfig{
		SelectionRangeProvider: true,
		OnSelectionRange: func(params SelectionRangeParams) (any, *RPCError) {
			return []SelectionRange{{
				Range: Range{Start: Position{Line: 0, Character: 1}, End: Position{Line: 0, Character: 4}},
				Parent: &SelectionRange{
					Range: Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 10}},
				},
			}}, nil
		},
	})

	mainPath := filepath.Join(dir, "main.typ")
	txtPath := filepath.Join(dir, "notes.txt")
	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()
	waitForM4b(t, 3*time.Second, func() bool {
		return defaultManager.State() == StateReady && latestSrv() != nil
	}, "StateReady")

	b := openM4bBuffer(t, mainPath, "0123456789\n")
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()

	// 1. Expand from manual selection [1, 4) picks strictly enclosing [0, 10) and shrinks back to [1, 4).
	bp.Cursor.SetSelectionStart(buffer.Loc{X: 1, Y: 0})
	bp.Cursor.SetSelectionEnd(buffer.Loc{X: 4, Y: 0})
	bp.Cursor.GotoLoc(buffer.Loc{X: 4, Y: 0})
	assert.True(t, SelectExpandAction()(bp))
	waitForM4b(t, 2*time.Second, func() bool {
		return bp.Cursor.CurSelection == ([2]buffer.Loc{{X: 0, Y: 0}, {X: 10, Y: 0}})
	}, "expanded from manual selection")

	assert.True(t, SelectShrinkAction()(bp))
	assert.Equal(t, [2]buffer.Loc{{X: 1, Y: 0}, {X: 4, Y: 0}}, bp.Cursor.CurSelection)
	assert.Equal(t, buffer.Loc{X: 4, Y: 0}, bp.Cursor.Loc)

	// 2. Buffer edit invalidates stack.
	bp.Cursor.ResetSelection()
	bp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})
	assert.True(t, SelectExpandAction()(bp))
	waitForM4b(t, 2*time.Second, func() bool {
		return bp.Cursor.HasSelection()
	}, "expand before edit")
	b.Insert(buffer.Loc{X: 0, Y: 0}, "x")
	buffer.FlushObservers()
	assert.False(t, SelectShrinkAction()(bp))
	assert.Equal(t, "No selection history", action.InfoBar.Msg)

	// 3. Cursor move invalidates stack.
	bp.Cursor.ResetSelection()
	bp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})
	assert.True(t, SelectExpandAction()(bp))
	waitForM4b(t, 2*time.Second, func() bool {
		return bp.Cursor.HasSelection()
	}, "expand before cursor move")
	bp.Cursor.GotoLoc(buffer.Loc{X: 1, Y: 0})
	assert.False(t, SelectShrinkAction()(bp))

	// 4. Manual selection change invalidates stack.
	bp.Cursor.ResetSelection()
	bp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})
	assert.True(t, SelectExpandAction()(bp))
	waitForM4b(t, 2*time.Second, func() bool {
		return bp.Cursor.HasSelection()
	}, "expand before manual selection divergence")
	bp.Cursor.SetSelectionEnd(buffer.Loc{X: 6, Y: 0})
	assert.False(t, SelectShrinkAction()(bp))

	// 5. Non-Typst buffer returns false on actions and informs on commands.
	bTxt := openM4bBuffer(t, txtPath, "hello\n")
	bp.OpenBuffer(bTxt)
	assert.False(t, SelectExpandAction()(bp))
	assert.False(t, SelectShrinkAction()(bp))
	SelectExpandCommand()(bp, nil)
	assert.Equal(t, "Selection expand is only available for .typ buffers", action.InfoBar.Msg)
	SelectShrinkCommand()(bp, nil)
	assert.Equal(t, "Selection shrink is only available for .typ buffers", action.InfoBar.Msg)
}

func TestSelectionRangeErrorsAnd32601(t *testing.T) {
	dir, _, latestSrv := setupM4bPipeEnv(t, 0, m4bFakeServerConfig{
		SelectionRangeProvider: false,
		OnSelectionRange: func(params SelectionRangeParams) (any, *RPCError) {
			return nil, &RPCError{Code: -32601, Message: "unsupported"}
		},
	})

	mainPath := filepath.Join(dir, "main.typ")
	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()
	waitForM4b(t, 3*time.Second, func() bool {
		return defaultManager.State() == StateReady && latestSrv() != nil
	}, "StateReady")

	// Unsaved .typ buffer.
	bUnsaved := buffer.NewBufferFromString("= Hi\n", "", buffer.BTDefault)
	bUnsaved.SetOptionNative("filetype", "typst")
	action.InitTabs([]*buffer.Buffer{bUnsaved})
	bp := action.MainTab().CurPane()
	assert.True(t, SelectExpandAction()(bp))
	assert.Equal(t, "Save buffer before expanding selection", action.InfoBar.Msg)

	// Saved .typ buffer receiving -32601.
	b := openM4bBuffer(t, mainPath, "= Hi\n")
	bp.OpenBuffer(b)
	bp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})
	assert.True(t, SelectExpandAction()(bp))
	waitForM4b(t, 2*time.Second, func() bool {
		return action.InfoBar.Msg == "Selection range not supported by LSP server"
	}, "unsupported message")
	assert.False(t, bp.Cursor.HasSelection())
}
