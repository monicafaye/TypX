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
	"github.com/monicafaye/TypX/internal/lsp"
	"github.com/monicafaye/TypX/tests/e2e/harness"
)

func TestE2E_ReferencesAndJumpBack(t *testing.T) {
	harness.RequirePhase23(t)
	mockBin := harness.BuildMockTinymist(t)

	workDir := t.TempDir()
	mainPath := filepath.Join(workDir, "main.typ")
	subPath := filepath.Join(workDir, "sub.typ")
	require.NoError(t, os.WriteFile(subPath, []byte("// sibling\n#sharedFn()\n"), 0644))
	require.NoError(t, os.WriteFile(mainPath, []byte("#let sharedFn() = [ok]\n#sharedFn()\n"), 0644))

	ed, _ := bootSimWithMockTinymist(t, mockBin, []string{mainPath}, nil)

	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 6, Y: 0})
	ed.InjectKey(tcell.KeyF12, 0, tcell.ModShift)

	gotRefs := ed.PumpUntil(4*time.Second, func() bool {
		return lsp.ListPaneForTab(action.MainTab()) != nil
	})
	require.True(t, gotRefs, "expected Shift+F12 to open References ListPane")
	assert.Contains(t, strings.Join(ed.AllScreenLines(), "\n"), "References (3)")

	ed.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	ed.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

	assert.True(t, strings.EqualFold(filepath.Clean(ed.Buf().AbsPath), filepath.Clean(subPath)))
	assert.Equal(t, buffer.Loc{X: 1, Y: 1}, ed.Cursor())

	ed.InjectKey(tcell.KeyRune, 'u', tcell.ModAlt)
	assert.True(t, strings.EqualFold(filepath.Clean(ed.Buf().AbsPath), filepath.Clean(mainPath)))
	assert.Equal(t, buffer.Loc{X: 6, Y: 0}, ed.Cursor())
}

func TestE2E_RenameAcrossOpenAndDiskFiles(t *testing.T) {
	harness.RequirePhase23(t)
	mockBin := harness.BuildMockTinymist(t)

	workDir := t.TempDir()
	mainPath := filepath.Join(workDir, "main.typ")
	subPath := filepath.Join(workDir, "sub.typ")
	require.NoError(t, os.WriteFile(subPath, []byte("#let oldSym = 1\n"), 0644))
	require.NoError(t, os.WriteFile(mainPath, []byte("#import \"sub.typ\": oldSym\n#let z = oldSym\n"), 0644))

	ed, _ := bootSimWithMockTinymist(t, mockBin, []string{mainPath}, nil)

	// Press F2 on oldSym -> opens InfoBar rename prompt -> replace with newSym & press Enter.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 20, Y: 0})
	ed.InjectKey(tcell.KeyF2, 0, tcell.ModNone)

	promptReady := ed.PumpUntil(4*time.Second, func() bool {
		return action.InfoBar.HasPrompt && string(action.InfoBar.Buffer.LineBytes(0)) == "oldSym"
	})
	require.True(t, promptReady, "expected F2 rename prompt prefilled with oldSym")

	action.InfoBar.Buffer.Remove(action.InfoBar.Buffer.Start(), action.InfoBar.Buffer.End())
	action.InfoBar.Buffer.Insert(action.InfoBar.Buffer.Start(), "newSym")
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

	renamedOK := ed.PumpUntil(4*time.Second, func() bool {
		return strings.Contains(string(ed.Buf().Bytes()), "newSym")
	})
	require.True(t, renamedOK, "expected buffer updated to newSym, got %q", string(ed.Buf().Bytes()))
	assert.Equal(t, "#import \"sub.typ\": newSym\n#let z = newSym\n", string(ed.Buf().Bytes()))

	subDisk, err := os.ReadFile(subPath)
	require.NoError(t, err)
	assert.Equal(t, "#let newSym = 1\n", string(subDisk))

	// Single Ctrl+Z undo restores both occurrences in main.typ.
	ed.InjectKey(tcell.KeyCtrlZ, 0, tcell.ModCtrl)
	assert.Equal(t, "#import \"sub.typ\": oldSym\n#let z = oldSym\n", string(ed.Buf().Bytes()))
}

func TestE2E_CodeActionEditAndApplyEditCommand(t *testing.T) {
	harness.RequirePhase23(t)
	mockBin := harness.BuildMockTinymist(t)

	workDir := t.TempDir()
	docPath := filepath.Join(workDir, "fix.typ")
	require.NoError(t, os.WriteFile(docPath, []byte("// FIXME\n#let v = CMD_TARGET\n"), 0644))

	ed, _ := bootSimWithMockTinymist(t, mockBin, []string{docPath}, nil)

	// 1. Alt+A -> Enter applies preferred direct WorkspaceEdit quickfix.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 3, Y: 0})
	ed.InjectKey(tcell.KeyRune, 'a', tcell.ModAlt)
	gotActions := ed.PumpUntil(4*time.Second, func() bool {
		return lsp.ListPaneForTab(action.MainTab()) != nil
	})
	require.True(t, gotActions, "expected Alt+A to open Code Actions ListPane")

	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	assert.Equal(t, "// DONE\n#let v = CMD_TARGET\n", string(ed.Buf().Bytes()))

	// 2. :code-action -> select command-based refactor triggering workspace/applyEdit.
	ed.RunCommand("code-action")
	gotActions2 := ed.PumpUntil(4*time.Second, func() bool {
		return lsp.ListPaneForTab(action.MainTab()) != nil
	})
	require.True(t, gotActions2)

	ed.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	cmdApplied := ed.PumpUntil(4*time.Second, func() bool {
		return strings.Contains(string(ed.Buf().Bytes()), "CMD_APPLIED")
	})
	assert.True(t, cmdApplied, "expected server-initiated workspace/applyEdit to update buffer")
	assert.Equal(t, "// DONE\n#let v = CMD_APPLIED\n", string(ed.Buf().Bytes()))
}

func TestE2E_WorkspaceSymbolsAndTextFilter(t *testing.T) {
	harness.RequirePhase23(t)
	mockBin := harness.BuildMockTinymist(t)

	workDir := t.TempDir()
	mainPath := filepath.Join(workDir, "main.typ")
	helperPath := filepath.Join(workDir, "helper.typ")
	require.NoError(t, os.WriteFile(helperPath, []byte("= Appendix\n#let calcNorm() = 1\n"), 0644))
	require.NoError(t, os.WriteFile(mainPath, []byte("= Intro\n#let baseRate = 2\n"), 0644))

	ed, _ := bootSimWithMockTinymist(t, mockBin, []string{mainPath}, nil)

	// Alt+S opens Workspace Symbols drawer.
	ed.InjectKey(tcell.KeyRune, 's', tcell.ModAlt)
	gotSyms := ed.PumpUntil(4*time.Second, func() bool {
		return lsp.ListPaneForTab(action.MainTab()) != nil
	})
	require.True(t, gotSyms, "expected Alt+S to open Workspace Symbols ListPane")

	// '/' enters live text filter, typing "calcNorm" narrows to helper.typ's function, Enter jumps there.
	ed.InjectRune('/')
	ed.InjectString("calcNorm")
	lp := lsp.ListPaneForTab(action.MainTab())
	require.NotNil(t, lp)
	require.Len(t, lp.VisibleItems(), 1)
	assert.Contains(t, lp.VisibleItems()[0].Primary, "calcNorm")

	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	assert.True(t, strings.EqualFold(filepath.Clean(ed.Buf().AbsPath), filepath.Clean(helperPath)))
	assert.Equal(t, 1, ed.Cursor().Y)
}
