package lsp

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
	"github.com/monicafaye/TypX/internal/config"
)

func TestDefinitionSingleJumpAndJumpBack(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)

	mainPath := filepath.Join(dir, "main.typ")
	helperPath := filepath.Join(dir, "helper.typ")
	require.NoError(t, os.WriteFile(helperPath, []byte("// line 0\n// line 1\n#let greet(name) = [Hi #name]\n"), 0644))

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected StateReady")

	bMain := openTestBuffer(t, mainPath, "#import \"helper.typ\": greet\n#greet(\"world\")\n")
	action.InitTabs([]*buffer.Buffer{bMain})
	bpMain := action.MainTab().CurPane()
	bpMain.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 1})

	assert.True(t, LspDefinitionAction()(bpMain))

	waitForCondition(t, 3*time.Second, func() bool {
		return len(action.Tabs.List) == 2 && canonicalDocKey(action.MainTab().CurPane().Buf.AbsPath) == canonicalDocKey(helperPath)
	}, "expected definition jump to open helper.typ in new tab")

	bpDest := action.MainTab().CurPane()
	assert.Equal(t, buffer.Loc{X: 5, Y: 2}, bpDest.Cursor.Loc)

	// Jump back returns to main.typ and restores original cursor position.
	assert.True(t, LspJumpBackAction()(bpDest))
	assert.Equal(t, canonicalDocKey(mainPath), canonicalDocKey(action.MainTab().CurPane().Buf.AbsPath))
	assert.Equal(t, buffer.Loc{X: 2, Y: 1}, action.MainTab().CurPane().Cursor.Loc)

	// Stack is now empty.
	assert.False(t, LspJumpBackAction()(action.MainTab().CurPane()))
	assert.Equal(t, "Jump stack is empty", action.InfoBar.Msg)
}

func TestDefinitionJumpToBibFile(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)

	mainPath := filepath.Join(dir, "paper.typ")
	bibPath := filepath.Join(dir, "refs.bib")
	require.NoError(t, os.WriteFile(bibPath, []byte("@book{knuth84,\n  title={The TeXbook}\n}\n"), 0644))

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected StateReady")

	bMain := openTestBuffer(t, mainPath, "See @citekey for details.\n")
	action.InitTabs([]*buffer.Buffer{bMain})
	bpMain := action.MainTab().CurPane()
	bpMain.Cursor.GotoLoc(buffer.Loc{X: 5, Y: 0})

	DefinitionCommand()(bpMain, nil)

	waitForCondition(t, 3*time.Second, func() bool {
		return canonicalDocKey(action.MainTab().CurPane().Buf.AbsPath) == canonicalDocKey(bibPath)
	}, "expected definition jump to open refs.bib")

	bpBib := action.MainTab().CurPane()
	assert.Equal(t, buffer.Loc{X: 2, Y: 1}, bpBib.Cursor.Loc)

	JumpBackCommand()(bpBib, nil)
	assert.Equal(t, canonicalDocKey(mainPath), canonicalDocKey(action.MainTab().CurPane().Buf.AbsPath))
	assert.Equal(t, buffer.Loc{X: 5, Y: 0}, action.MainTab().CurPane().Cursor.Loc)
}

func TestDefinitionMultipleLocationsOpensListPane(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)
	config.GlobalSettings["typst.gotoOpenMode"] = "vsplit"

	mainPath := filepath.Join(dir, "main.typ")
	helperPath := filepath.Join(dir, "helper.typ")
	bibPath := filepath.Join(dir, "refs.bib")
	require.NoError(t, os.WriteFile(helperPath, []byte("line 0\n#let helper = 1\n"), 0644))
	require.NoError(t, os.WriteFile(bibPath, []byte("@article{multi, title={T}}\n"), 0644))

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected StateReady")

	bMain := openTestBuffer(t, mainPath, "MULTI_DEF\n")
	action.InitTabs([]*buffer.Buffer{bMain})
	bpMain := action.MainTab().CurPane()
	bpMain.Cursor.GotoLoc(buffer.Loc{X: 3, Y: 0})

	assert.True(t, LspDefinitionAction()(bpMain))

	waitForCondition(t, 3*time.Second, func() bool {
		return ListPaneForTab(action.MainTab()) != nil
	}, "expected multiple definition locations to open ListPane picker")

	lp := ListPaneForTab(action.MainTab())
	items := lp.VisibleItems()
	require.Len(t, items, 2)
	assert.Contains(t, items[0].Primary, "#let helper = 1")

	// Select first item with Enter -> closes ListPane and opens helper.typ in vsplit.
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	assert.Nil(t, ListPaneForTab(action.MainTab()))
	bpDest := action.MainTab().CurPane()
	assert.Equal(t, canonicalDocKey(helperPath), canonicalDocKey(bpDest.Buf.AbsPath))
	assert.Equal(t, buffer.Loc{X: 0, Y: 1}, bpDest.Cursor.Loc)

	// Jump back returns focus to main.typ pane at original location.
	assert.True(t, LspJumpBackAction()(bpDest))
	assert.Equal(t, canonicalDocKey(mainPath), canonicalDocKey(action.MainTab().CurPane().Buf.AbsPath))
	assert.Equal(t, buffer.Loc{X: 3, Y: 0}, action.MainTab().CurPane().Cursor.Loc)
}

func TestDefinitionPackageCacheMarkedReadonly(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)
	pkgDir := filepath.Join(dir, "cache", "typst", "packages", "preview", "cetz", "0.2.0")
	require.NoError(t, os.MkdirAll(pkgDir, 0755))
	pkgFile := filepath.Join(pkgDir, "lib.typ")
	require.NoError(t, os.WriteFile(pkgFile, []byte("#let canvas() = []\n"), 0644))
	t.Setenv("TYPX_MOCK_DEF_PKG_PATH", pkgFile)

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected StateReady")

	mainPath := filepath.Join(dir, "main.typ")
	bMain := openTestBuffer(t, mainPath, "#canvas()\n")
	action.InitTabs([]*buffer.Buffer{bMain})
	bpMain := action.MainTab().CurPane()

	assert.True(t, LspDefinitionAction()(bpMain))

	waitForCondition(t, 3*time.Second, func() bool {
		return canonicalDocKey(action.MainTab().CurPane().Buf.AbsPath) == canonicalDocKey(pkgFile)
	}, "expected jump to package cache file")

	destBuf := action.MainTab().CurPane().Buf
	assert.True(t, destBuf.Type.Readonly, "package cache target must open readonly")
}

func TestDiagnosticsNextPrevWrapAndListPaneJump(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)
	// Inject diagnostics directly without a background mock LSP server overwriting them on didOpen.
	config.GlobalSettings["typst.lspEnabled"] = false

	mainPath := filepath.Join(dir, "main.typ")
	subPath := filepath.Join(dir, "sub.typ")
	require.NoError(t, os.WriteFile(subPath, []byte("sub line 0\nsub error here\n"), 0644))

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	bMain := openTestBuffer(t, mainPath, "alpha ERR_ONE\nbeta\ngamma ERR_TWO\n")
	action.InitTabs([]*buffer.Buffer{bMain})
	bpMain := action.MainTab().CurPane()

	// Populate multi-file diagnostics directly through applyDiagnostics.
	defaultManager.mu.Lock()
	gen := defaultManager.gen
	defaultManager.mu.Unlock()

	defaultManager.applyDiagnostics(gen, PublishDiagnosticsParams{
		URI: PathToURI(mainPath),
		Diagnostics: []Diagnostic{
			{Range: Range{Start: Position{0, 6}, End: Position{0, 13}}, Severity: SeverityError, Message: "first main error"},
			{Range: Range{Start: Position{2, 6}, End: Position{2, 13}}, Severity: SeverityWarning, Message: "second main warn"},
		},
	})
	defaultManager.applyDiagnostics(gen, PublishDiagnosticsParams{
		URI: PathToURI(subPath),
		Diagnostics: []Diagnostic{
			{Range: Range{Start: Position{1, 4}, End: Position{1, 9}}, Severity: SeverityError, Message: "sub file error"},
		},
	})
	waitForCondition(t, 2*time.Second, func() bool {
		return len(bMain.Messages) == 2
	}, "expected buffer diagnostic messages")

	// NextDiagnostic (F8) moves forward then wraps around.
	bpMain.Cursor.GotoLoc(buffer.Loc{X: 0, Y: 0})
	assert.True(t, NextDiagnosticAction()(bpMain))
	assert.Equal(t, buffer.Loc{X: 6, Y: 0}, bpMain.Cursor.Loc)
	assert.Contains(t, action.InfoBar.Msg, "[1/2] first main error")

	assert.True(t, NextDiagnosticAction()(bpMain))
	assert.Equal(t, buffer.Loc{X: 6, Y: 2}, bpMain.Cursor.Loc)
	assert.Contains(t, action.InfoBar.Msg, "[2/2] second main warn")

	assert.True(t, NextDiagnosticAction()(bpMain))
	assert.Equal(t, buffer.Loc{X: 6, Y: 0}, bpMain.Cursor.Loc)
	assert.Contains(t, action.InfoBar.Msg, "[1/2]")

	// PrevDiagnostic (Shift-F8) wraps backward from first to last.
	assert.True(t, PrevDiagnosticAction()(bpMain))
	assert.Equal(t, buffer.Loc{X: 6, Y: 2}, bpMain.Cursor.Loc)
	assert.Contains(t, action.InfoBar.Msg, "[2/2]")

	// :diagnostics opens ListPane with active file items first, then sub.typ.
	DiagnosticsCommand()(bpMain, nil)
	lp := ListPaneForTab(bpMain.Tab())
	require.NotNil(t, lp)
	items := lp.VisibleItems()
	require.Len(t, items, 3)
	assert.Equal(t, "first main error", items[0].Primary)
	assert.Equal(t, "second main warn", items[1].Primary)
	assert.Equal(t, "sub file error", items[2].Primary)

	// 'f' filters down to errors only (2 errors: main line 0 + sub line 1).
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'f', tcell.ModNone, ""))
	filtered := lp.VisibleItems()
	require.Len(t, filtered, 2)
	assert.Equal(t, "first main error", filtered[0].Primary)
	assert.Equal(t, "sub file error", filtered[1].Primary)

	// Navigate to sub file error and press Enter -> jumps to sub.typ in new tab.
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))

	assert.Equal(t, canonicalDocKey(subPath), canonicalDocKey(action.MainTab().CurPane().Buf.AbsPath))
	assert.Equal(t, buffer.Loc{X: 4, Y: 1}, action.MainTab().CurPane().Cursor.Loc)
}
