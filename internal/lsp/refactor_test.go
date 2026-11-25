package lsp

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
)

func TestFindReferencesOpensListPaneAndJumpsWithBackStack(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)
	mainPath := filepath.Join(dir, "main.typ")
	subPath := filepath.Join(dir, "sub.typ")
	require.NoError(t, os.WriteFile(subPath, []byte("// sub header\n#helper()\n"), 0644))

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected StateReady")

	bMain := openTestBuffer(t, mainPath, "#let helper() = [ok]\n#helper()\n")
	action.InitTabs([]*buffer.Buffer{bMain})
	bpMain := action.MainTab().CurPane()
	bpMain.Cursor.GotoLoc(buffer.Loc{X: 6, Y: 0})

	assert.True(t, LspReferencesAction()(bpMain))
	waitForCondition(t, 3*time.Second, func() bool {
		return ListPaneForTab(action.MainTab()) != nil
	}, "expected References ListPane")

	lp := ListPaneForTab(action.MainTab())
	items := lp.VisibleItems()
	require.Len(t, items, 3)
	assert.Equal(t, "REF", items[0].Badge)
	assert.Equal(t, canonicalDocKey(mainPath), canonicalDocKey(items[0].FilePath))
	assert.Equal(t, canonicalDocKey(subPath), canonicalDocKey(items[2].FilePath))

	// Jump to reference in sub.typ; ListPane remains open on main tab while jumping.
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))

	bpDest := action.MainTab().CurPane()
	assert.Equal(t, canonicalDocKey(subPath), canonicalDocKey(bpDest.Buf.AbsPath))
	assert.Equal(t, buffer.Loc{X: 1, Y: 1}, bpDest.Cursor.Loc)

	// Jump back returns to main.typ at original cursor.
	assert.True(t, LspJumpBackAction()(bpDest))
	assert.Equal(t, canonicalDocKey(mainPath), canonicalDocKey(action.MainTab().CurPane().Buf.AbsPath))
	assert.Equal(t, buffer.Loc{X: 6, Y: 0}, action.MainTab().CurPane().Cursor.Loc)
}

func TestRenamePrepareRejectionPromptAndMultiFileUndo(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)
	mainPath := filepath.Join(dir, "main.typ")
	subPath := filepath.Join(dir, "sub.typ")
	require.NoError(t, os.WriteFile(subPath, []byte("#let targetSym = 42\n"), 0644))

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected StateReady")

	bMain := openTestBuffer(t, mainPath, "#import \"sub.typ\": targetSym\n#let y = targetSym\n// NO_RENAME here\n")
	action.InitTabs([]*buffer.Buffer{bMain})
	bpMain := action.MainTab().CurPane()

	// 1. PrepareRename rejection notifies cleanly.
	bpMain.Cursor.GotoLoc(buffer.Loc{X: 5, Y: 2})
	assert.True(t, LspRenameAction()(bpMain))
	waitForCondition(t, 3*time.Second, func() bool {
		return action.InfoBar.Msg == "Symbol cannot be renamed"
	}, "expected prepareRename rejection message")

	// 2. Explicit :lsp-rename <newName> renames across open buffer + disk sub.typ.
	bpMain.Cursor.GotoLoc(buffer.Loc{X: 20, Y: 0})
	LspRenameCommand()(bpMain, []string{"renamedSym"})
	waitForCondition(t, 3*time.Second, func() bool {
		return strings.Contains(string(bMain.Bytes()), "renamedSym")
	}, "expected open buffer renamed")
	assert.Equal(t, "#import \"sub.typ\": renamedSym\n#let y = renamedSym\n// NO_RENAME here\n", string(bMain.Bytes()))
	subDisk, err := os.ReadFile(subPath)
	require.NoError(t, err)
	assert.Equal(t, "#let renamedSym = 42\n", string(subDisk))

	// Single undo reverts both edits in bMain.
	assert.True(t, bMain.Undo())
	assert.Equal(t, "#import \"sub.typ\": targetSym\n#let y = targetSym\n// NO_RENAME here\n", string(bMain.Bytes()))

	// 3. Interactive F2 opens InfoBar prompt with placeholder, accepts new name, renames again.
	LspRenameAction()(bpMain)
	waitForCondition(t, 3*time.Second, func() bool {
		return action.InfoBar.HasPrompt && string(action.InfoBar.LineBytes(0)) == "targetSym"
	}, "expected InfoBar rename prompt prefilled with prepareRename placeholder")

	action.InfoBar.Buffer.Remove(action.InfoBar.Buffer.Start(), action.InfoBar.Buffer.End())
	action.InfoBar.Buffer.Insert(action.InfoBar.Buffer.Start(), "promptedSym")
	action.InfoBar.DonePrompt(false)

	waitForCondition(t, 3*time.Second, func() bool {
		return strings.Contains(string(bMain.Bytes()), "promptedSym")
	}, "expected F2 prompt rename to update buffer")
}

func TestCodeActionsDirectEditAndApplyEditCommand(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)
	docPath := filepath.Join(dir, "actions.typ")

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected StateReady")

	b := openTestBuffer(t, docPath, "// FIXME\n#let x = CMD_TARGET\n")
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()
	bp.Cursor.GotoLoc(buffer.Loc{X: 3, Y: 0})

	// 1. Direct WorkspaceEdit quickfix.
	assert.True(t, LspCodeActionAction()(bp))
	waitForCondition(t, 3*time.Second, func() bool {
		return ListPaneForTab(action.MainTab()) != nil
	}, "expected Code Actions ListPane")

	lp := ListPaneForTab(action.MainTab())
	items := lp.VisibleItems()
	require.Len(t, items, 2)
	assert.Equal(t, "FIX*", items[0].Badge)
	assert.Equal(t, "REF", items[1].Badge)

	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	assert.Nil(t, ListPaneForTab(action.MainTab()))
	assert.Equal(t, "// DONE\n#let x = CMD_TARGET\n", string(b.Bytes()))
	assert.Contains(t, action.InfoBar.Msg, "Applied:")

	// 2. Command-based CodeAction triggers server-initiated workspace/applyEdit.
	CodeActionCommand()(bp, nil)
	waitForCondition(t, 3*time.Second, func() bool {
		return ListPaneForTab(action.MainTab()) != nil
	}, "expected Code Actions ListPane on second invoke")

	lp2 := ListPaneForTab(action.MainTab())
	lp2.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
	lp2.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))

	waitForCondition(t, 4*time.Second, func() bool {
		return strings.Contains(string(b.Bytes()), "CMD_APPLIED")
	}, "expected server-initiated workspace/applyEdit to update buffer")
	assert.Equal(t, "// DONE\n#let x = CMD_APPLIED\n", string(b.Bytes()))
}

func TestWorkspaceSymbolsSearchFilterAndJump(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)
	mainPath := filepath.Join(dir, "main.typ")
	helperPath := filepath.Join(dir, "helper.typ")
	require.NoError(t, os.WriteFile(helperPath, []byte("= Methods\n#let computeScore() = 1\n"), 0644))

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected StateReady")

	bMain := openTestBuffer(t, mainPath, "= Introduction\n#let localVal = 2\n")
	action.InitTabs([]*buffer.Buffer{bMain})
	bpMain := action.MainTab().CurPane()

	assert.True(t, LspWorkspaceSymbolsAction()(bpMain))
	waitForCondition(t, 3*time.Second, func() bool {
		return ListPaneForTab(action.MainTab()) != nil
	}, "expected Workspace Symbols ListPane")

	lp := ListPaneForTab(action.MainTab())
	require.GreaterOrEqual(t, len(lp.VisibleItems()), 3)

	// Filter with '/' for "compute" and press Enter to jump to helper.typ.
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '/', tcell.ModNone, ""))
	for _, ch := range "compute" {
		lp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, ch, tcell.ModNone, ""))
	}
	filtered := lp.VisibleItems()
	require.Len(t, filtered, 1)
	assert.Contains(t, filtered[0].Primary, "computeScore")
	assert.Equal(t, "FN", filtered[0].Badge)

	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	bpDest := action.MainTab().CurPane()
	assert.Equal(t, canonicalDocKey(helperPath), canonicalDocKey(bpDest.Buf.AbsPath))
	assert.Equal(t, 1, bpDest.Cursor.Loc.Y)

	assert.True(t, LspJumpBackAction()(bpDest))
	assert.Equal(t, canonicalDocKey(mainPath), canonicalDocKey(action.MainTab().CurPane().Buf.AbsPath))
}
