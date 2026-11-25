package editor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/tests/e2e/harness"
)

func TestE2E_FileManagerPluginTreeNavigationAndOps(t *testing.T) {
	harness.RequirePhase23(t)

	// Create an isolated project directory and chdir into it for the duration of the test.
	workDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workDir, "chapters"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "chapters", "intro.typ"), []byte("= Intro\nHello from chapter.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "main.typ"), []byte("= Main\nRoot document.\n"), 0o644))

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workDir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	ed := harness.NewSimEditor(t, harness.SimOptions{
		InitialName: "main.typ",
		InitialText: "= Main\nRoot document.\n",
		Settings: map[string]any{
			"colorscheme":      "catppuccin-mocha",
			"typst.lspEnabled": false,
			"typst.keyHints":   true,
			"ruler":            false,
			"softwrap":         false,
		},
		Width:  120,
		Height: 24,
	})

	// Ensure filemanager is closed at test end so shared Lua state stays clean for subsequent tests.
	t.Cleanup(func() {
		tab := action.MainTab()
		if tab == nil {
			return
		}
		for _, p := range tab.Panes {
			if bp, ok := p.(*action.BufPane); ok && bp.Buf != nil && bp.Buf.Path == "filemanager" {
				bp.HandleCommand("tree")
				break
			}
		}
	})

	// 1. Typst bottom bar shows clickable "Alt+T Files" shortcut; clicking it toggles the file tree open.
	initialBar := ed.InfoBarText()
	assert.Contains(t, initialBar, "Alt+T Files")
	filesX := strings.Index(initialBar, "Alt+T")
	require.Greater(t, filesX, 0, "expected Alt+T in Typst bottom bar: %q", initialBar)
	_, screenH := ed.Sim.Size()
	ed.InjectMouse(filesX+2, screenH-1, tcell.Button1, tcell.ModNone)
	ed.InjectMouse(filesX+2, screenH-1, tcell.ButtonNone, tcell.ModNone)

	tab := action.MainTab()
	require.Len(t, tab.Panes, 2, "clicking Alt+T Files on bottom bar should open filemanager sidebar")

	treePane := tab.CurPane()
	require.NotNil(t, treePane)
	assert.Equal(t, "filemanager", treePane.Buf.Path)
	assert.Equal(t, "filemanager", treePane.Buf.Settings["filetype"])

	// Bottom bar should show dedicated FILES navigation hints.
	bar := ed.InfoBarText()
	assert.Contains(t, bar, "FILES")
	assert.Contains(t, bar, "Enter Open")
	assert.Contains(t, bar, "Alt+T Close")

	treeText := string(treePane.Buf.Bytes())
	assert.Contains(t, treeText, "+ chapters/")
	assert.Contains(t, treeText, "  main.typ")

	// 2. Move down from ".." (line 2) to "+ chapters/" (line 3), expand with Right, verify nested file.
	ed.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	assert.Equal(t, 3, treePane.Cursor.Loc.Y)

	ed.InjectKey(tcell.KeyRight, 0, tcell.ModNone)
	treeText = string(treePane.Buf.Bytes())
	assert.Contains(t, treeText, "- chapters/")
	assert.Contains(t, treeText, "intro.typ")

	// Move down to nested intro.typ (line 4) and open it with Enter.
	ed.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	assert.Equal(t, 4, treePane.Cursor.Loc.Y)
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

	require.Len(t, tab.Panes, 3, "Enter on a file should open it in a vertical split")
	openedPane := tab.CurPane()
	assert.Equal(t, "intro.typ", filepath.Base(openedPane.Buf.Path))
	assert.Contains(t, string(openedPane.Buf.Bytes()), "Hello from chapter.")

	// 3. Switch focus back to filemanager pane, collapse chapters/ with Left, then test touch & rename.
	tab.SetActive(0)
	ed.Pump(2)
	require.Equal(t, treePane, tab.CurPane())

	// Currently on line 4 (intro.typ); press Left to jump to parent (chapters/), then Left again to collapse.
	ed.InjectKey(tcell.KeyLeft, 0, tcell.ModNone)
	assert.Equal(t, 3, treePane.Cursor.Loc.Y)
	ed.InjectKey(tcell.KeyLeft, 0, tcell.ModNone)
	assert.Contains(t, string(treePane.Buf.Bytes()), "+ chapters/")
	assert.NotContains(t, string(treePane.Buf.Bytes()), "intro.typ")

	// Move cursor to ".." (line 2) so touch/mkdir create directly in workDir.
	ed.InjectKey(tcell.KeyUp, 0, tcell.ModNone)
	assert.Equal(t, 2, treePane.Cursor.Loc.Y)

	treePane.HandleCommand("touch notes.typ")
	ed.Pump(2)
	_, err = os.Stat(filepath.Join(workDir, "notes.typ"))
	require.NoError(t, err, ":touch should create notes.typ on disk")

	// Cursor is now on the newly created notes.typ; rename it (verifies Windows file handle was closed!).
	treePane.HandleCommand("rename appendix.typ")
	ed.Pump(2)
	_, err = os.Stat(filepath.Join(workDir, "appendix.typ"))
	require.NoError(t, err, ":rename should rename notes.typ to appendix.typ")
	assert.Contains(t, string(treePane.Buf.Bytes()), "appendix.typ")

	// Create a subdirectory with :mkdir from "..".
	ed.InjectKey(tcell.KeyUp, 0, tcell.ModNone)
	ed.InjectKey(tcell.KeyUp, 0, tcell.ModNone)
	ed.InjectKey(tcell.KeyUp, 0, tcell.ModNone)
	assert.Equal(t, 2, treePane.Cursor.Loc.Y)
	treePane.HandleCommand("mkdir figures")
	ed.Pump(2)
	fi, err := os.Stat(filepath.Join(workDir, "figures"))
	require.NoError(t, err)
	assert.True(t, fi.IsDir())

	// 4. Click on main.typ in the tree pane with MouseLeft (verifies LocFromVisual fix,
	// including when the long directory path on line 0 soft-wraps across multiple screen rows).
	_, sh := ed.Sim.Size()
	mainRow := -1
	for row := 0; row < sh-2; row++ {
		if strings.Contains(ed.ScreenLine(row), "main.typ") {
			mainRow = row
			break
		}
	}
	require.Greater(t, mainRow, 0, "expected main.typ rendered on screen")
	ed.InjectMouse(4, mainRow, tcell.Button1, tcell.ModNone)
	ed.InjectMouse(4, mainRow, tcell.ButtonNone, tcell.ModNone)
	assert.Equal(t, "main.typ", filepath.Base(tab.CurPane().Buf.Path))

	// 5. Close filemanager via Alt+T.
	ed.InjectKey(tcell.KeyRune, 't', tcell.ModAlt)
	for _, p := range tab.Panes {
		if bp, ok := p.(*action.BufPane); ok && bp.Buf != nil {
			assert.NotEqual(t, "filemanager", bp.Buf.Path, "Alt+T should close the filemanager pane")
		}
	}
}
