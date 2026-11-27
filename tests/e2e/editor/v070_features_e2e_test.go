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
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/lsp"
	"github.com/monicafaye/TypX/internal/typst/bib"
	"github.com/monicafaye/TypX/internal/typst/outline"
	"github.com/monicafaye/TypX/internal/typst/preview"
	"github.com/monicafaye/TypX/internal/typst/templates"
	"github.com/monicafaye/TypX/tests/e2e/harness"
)

func cleanupV070PanesAndState(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if action.Tabs != nil {
			for _, tab := range action.Tabs.List {
				if tab == nil {
					continue
				}
				lsp.CloseListPaneForTab(tab)
				outline.CloseForTab(tab)
				preview.CloseForTab(tab)
				templates.CloseForTab(tab)
			}
		}
		lsp.Shutdown()
		bib.ClearCache()
		config.GlobalSettings["typst.autoComplete"] = true
		config.GlobalSettings["typst.documentHighlight"] = true
		config.GlobalSettings["typst.gotoOpenMode"] = "tab"
		config.GlobalSettings["typst.lspEnabled"] = true
	})
}

func ensureOfflineLSP(t *testing.T, ed *harness.SimEditor) {
	t.Helper()
	config.GlobalSettings["typst.lspEnabled"] = false
	lsp.Shutdown()
	ed.RunCommand("tinymist-restart")
	require.True(t, lsp.IsOffline(), "expected LSP to be offline")
}

func TestE2E_CustomSnippetsThreeLayerPrecedenceAndPicker(t *testing.T) {
	harness.RequirePhase23(t)

	cfgDir := t.TempDir()
	projDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projDir, "typst.toml"), []byte("[package]\nname = \"snips\"\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(projDir, ".typx"), 0o755))

	// Layer 2 (user): overrides built-in callout and defines object snippet usernote.
	userJSON := `{
  "callout": "USER_CALLOUT($1)$0",
  "usernote": {
    "body": "#note[$1]$0",
    "description": "User note block"
  }
}`
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "snippets.json"), []byte(userJSON), 0o644))

	// Layer 3 (project): overrides callout again and defines project snippet lemma.
	projJSON := `{
  "callout": "PROJ_CALLOUT($1)$0",
  "lemma": {
    "body": "#lemma[$1][$2]$0",
    "description": "Project lemma box"
  }
}`
	require.NoError(t, os.WriteFile(filepath.Join(projDir, ".typx", "snippets.json"), []byte(projJSON), 0o644))

	mainPath := filepath.Join(projDir, "main.typ")
	require.NoError(t, os.WriteFile(mainPath, []byte(""), 0o644))

	ed := harness.NewSimEditor(t, harness.SimOptions{
		ConfigDir: cfgDir,
		Files:     []string{mainPath},
		Settings: map[string]any{
			"typst.lspEnabled": false,
		},
		Width:  100,
		Height: 24,
	})
	cleanupV070PanesAndState(t)
	ensureOfflineLSP(t, ed)

	// 1. Alt+I opens SNIPPETS ListPane, filter "lemma", Enter expands project snippet with tabstops.
	ed.InjectKey(tcell.KeyRune, 'i', tcell.ModAlt)
	lp := lsp.ListPaneForTab(action.MainTab())
	require.NotNil(t, lp, "expected Alt+I to open SNIPPETS ListPane")
	assert.Equal(t, "SNIPPETS", lp.Badge())

	ed.InjectString("lemma")
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	assert.Nil(t, lsp.ListPaneForTab(action.MainTab()), "expected ListPane to close on Enter")

	typeText(ed, "Main")
	ed.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(ed, "proof")
	ed.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(ed, ".")
	assert.Equal(t, "#lemma[Main][proof].", string(ed.Buf().LineBytes(0)))

	// 2. Project layer overrides user and built-in for "callout"; user layer provides "usernote".
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	ed.RunCommand("insert callout")
	typeText(ed, "hi")
	ed.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	assert.Equal(t, "PROJ_CALLOUT(hi)", string(ed.Buf().LineBytes(1)))

	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	ed.RunCommand("insert usernote")
	typeText(ed, "memo")
	ed.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	assert.Equal(t, "#note[memo]", string(ed.Buf().LineBytes(2)))

	// 3. Malformed user snippets.json emits non-fatal InfoBar error while preserving built-in snippets.
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "snippets.json"), []byte("{bad json"), 0o644))
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	ed.RunCommand("insert")
	lp = lsp.ListPaneForTab(action.MainTab())
	require.NotNil(t, lp)
	msg, _, _, hasErr := ed.InfoBarState()
	assert.True(t, hasErr, "expected non-fatal infobar warning for malformed snippets.json, got msg=%q", msg)
	assert.Contains(t, msg, "snippets.json")

	ed.InjectString("frac")
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	typeText(ed, "1")
	ed.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(ed, "2")
	ed.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	assert.Equal(t, "frac(1, 2)", string(ed.Buf().LineBytes(3)))
}

func TestE2E_MathSymbolPickerModeAwareInsertion(t *testing.T) {
	harness.RequirePhase23(t)

	docPath := filepath.Join(t.TempDir(), "sym.typ")
	require.NoError(t, os.WriteFile(docPath, []byte("text \n$ $\n"), 0o644))

	ed := harness.NewSimEditor(t, harness.SimOptions{
		Files: []string{docPath},
		Settings: map[string]any{
			"typst.lspEnabled": false,
		},
		Width:  100,
		Height: 24,
	})
	cleanupV070PanesAndState(t)
	ensureOfflineLSP(t, ed)

	// 1. Markup mode: Alt+Y -> filter "subset.eq" -> Enter -> inserts "$subset.eq$" with cursor after '$'.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 5, Y: 0})
	ed.InjectKey(tcell.KeyRune, 'y', tcell.ModAlt)
	lp := lsp.ListPaneForTab(action.MainTab())
	require.NotNil(t, lp)
	assert.Equal(t, "SYMBOLS", lp.Badge())

	ed.InjectString("subset.eq")
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	assert.Nil(t, lsp.ListPaneForTab(action.MainTab()))
	assert.Equal(t, "text $subset.eq$", string(ed.Buf().LineBytes(0)))
	assert.Equal(t, buffer.Loc{X: len("text $subset.eq$"), Y: 0}, ed.Cursor())

	// 2. Math mode: :symbol alpha -> Enter -> inserts raw "alpha" without extra '$'.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 1, Y: 1})
	ed.RunCommand("symbol alpha")
	lp = lsp.ListPaneForTab(action.MainTab())
	require.NotNil(t, lp)
	assert.Equal(t, "SYMBOLS", lp.Badge())
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	assert.Nil(t, lsp.ListPaneForTab(action.MainTab()))
	assert.Equal(t, "$alpha $", string(ed.Buf().LineBytes(1)))
}

func TestE2E_MultiFileOutlineProjectToggleAndCrossFileJump(t *testing.T) {
	harness.RequirePhase23(t)

	projDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projDir, "typst.toml"), []byte("[package]\nname = \"book\"\n"), 0o644))
	chapDir := filepath.Join(projDir, "chapters")
	require.NoError(t, os.MkdirAll(chapDir, 0o755))

	mainPath := filepath.Join(projDir, "main.typ")
	introPath := filepath.Join(chapDir, "intro.typ")
	require.NoError(t, os.WriteFile(introPath, []byte("= Chapter One\nIntro text.\n\n== Sub Section\nDetails.\n"), 0o644))
	require.NoError(t, os.WriteFile(mainPath, []byte("= Root Title\n#include \"chapters/intro.typ\"\n"), 0o644))

	ed := harness.NewSimEditor(t, harness.SimOptions{
		Files: []string{mainPath},
		Settings: map[string]any{
			"typst.lspEnabled":   false,
			"typst.gotoOpenMode": "tab",
		},
		Width:  110,
		Height: 26,
	})
	cleanupV070PanesAndState(t)
	ensureOfflineLSP(t, ed)

	// 1. Alt+O opens Outline in [Project] mode showing chapters/intro.typ header and headings.
	ed.InjectKey(tcell.KeyRune, 'o', tcell.ModAlt)
	tab := action.MainTab()
	op := outline.PaneForTab(tab)
	require.NotNil(t, op)
	assert.True(t, op.IsProjectMode())
	assert.Contains(t, ed.ScreenLine(0), "[Project]")
	screenDump := strings.Join(ed.AllScreenLines(), "\n")
	assert.Contains(t, screenDump, "chapters/intro.typ")
	assert.Contains(t, screenDump, "Chapter One")

	// 2. Pressing 'p' toggles to [File] mode (only main.typ items shown); pressing 'p' again restores [Project].
	ed.InjectRune('p')
	assert.False(t, op.IsProjectMode())
	assert.Contains(t, ed.ScreenLine(0), "[File]")
	assert.NotContains(t, strings.Join(ed.AllScreenLines(), "\n"), "Chapter One")

	ed.InjectRune('p')
	assert.True(t, op.IsProjectMode())
	assert.Contains(t, ed.ScreenLine(0), "[Project]")

	// 3. Filter "/Sub Section" and press Enter -> opens chapters/intro.typ at Sub Section line (line 3) in a new tab.
	ed.InjectString("/Sub Section")
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

	require.Len(t, action.Tabs.List, 2, "expected typst.gotoOpenMode=tab to open chapters/intro.typ in new tab")
	assert.Equal(t, filepath.Clean(introPath), filepath.Clean(ed.Buf().AbsPath))
	assert.Equal(t, 3, ed.Cursor().Y)
}

func TestE2E_BibliographyCitePickerAndOfflineAtSuggestions(t *testing.T) {
	harness.RequirePhase23(t)

	projDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projDir, "typst.toml"), []byte("[package]\nname = \"paper\"\n"), 0o644))
	bibContent := `@article{vaswani2017,
  author = {Ashish Vaswani and Noam Shazeer},
  title = {Attention Is All You Need},
  year = {2017},
}
`
	hayagrivaContent := `vas-2020.intro:
  type: article
  title: Transformer Intro
  author: Vas Author
  date: 2020
`
	require.NoError(t, os.WriteFile(filepath.Join(projDir, "refs.bib"), []byte(bibContent), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(projDir, "extra.yml"), []byte(hayagrivaContent), 0o644))
	mainPath := filepath.Join(projDir, "main.typ")
	require.NoError(t, os.WriteFile(mainPath, []byte("#bibliography((\"refs.bib\", \"extra.yml\"))\n\n"), 0o644))

	ed := harness.NewSimEditor(t, harness.SimOptions{
		Files: []string{mainPath},
		Settings: map[string]any{
			"typst.lspEnabled":   false,
			"typst.autoComplete": true,
		},
		Width:  110,
		Height: 24,
	})
	cleanupV070PanesAndState(t)
	ensureOfflineLSP(t, ed)

	// 1. Alt+R opens CITE ListPane, filter by title "Attention", Enter inserts @vaswani2017.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 0, Y: 1})
	ed.InjectKey(tcell.KeyRune, 'r', tcell.ModAlt)
	lp := lsp.ListPaneForTab(action.MainTab())
	require.NotNil(t, lp)
	assert.Equal(t, "CITE", lp.Badge())
	ed.InjectString("Attention")
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	assert.Nil(t, lsp.ListPaneForTab(action.MainTab()))
	assert.Equal(t, "@vaswani2017", string(ed.Buf().LineBytes(1)))

	// 2. When '@' already precedes cursor, :cite inserts only the key (no double '@@').
	typeText(ed, " @")
	ed.RunCommand("cite Transformer")
	require.NotNil(t, lsp.ListPaneForTab(action.MainTab()))
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	assert.Equal(t, "@vaswani2017 @vas-2020.intro", string(ed.Buf().LineBytes(1)))

	// 3. Offline '@' typing triggers local bibliography suggestions on statusline, Tab cycles, Esc dismisses.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 0, Y: 2})
	typeText(ed, "@vas")
	gotOffline := ed.PumpUntil(2*time.Second, func() bool {
		return ed.Buf().HasSuggestions && len(ed.Buf().Suggestions) == 2
	})
	require.True(t, gotOffline, "expected offline '@' bibliography suggestions, got %v", ed.Buf().Suggestions)
	assert.Equal(t, []string{"vas-2020.intro", "vaswani2017"}, ed.Buf().Suggestions)

	ed.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	assert.Equal(t, "@vas-2020.intro", string(ed.Buf().LineBytes(2)))
	assert.True(t, ed.Buf().HasSuggestions)

	ed.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	assert.Equal(t, "@vaswani2017", string(ed.Buf().LineBytes(2)))

	// Esc reverts previewed Tab suffix back to "@vas" and clears suggestions.
	ed.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	assert.False(t, ed.Buf().HasSuggestions)
	assert.Equal(t, "@vas", string(ed.Buf().LineBytes(2)))

	// Typing another character or re-typing '@vas' and pressing Enter commits first match without inserting newline.
	ed.Buf().Remove(buffer.Loc{X: 0, Y: 2}, buffer.Loc{X: 4, Y: 2})
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 0, Y: 2})
	typeText(ed, "@vas")
	gotOffline2 := ed.PumpUntil(2*time.Second, func() bool {
		return ed.Buf().HasSuggestions && len(ed.Buf().Suggestions) == 2
	})
	require.True(t, gotOffline2)
	linesBefore := ed.Buf().LinesNum()
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	assert.False(t, ed.Buf().HasSuggestions)
	assert.Equal(t, "@vas-2020.intro", string(ed.Buf().LineBytes(2)))
	assert.Equal(t, linesBefore, ed.Buf().LinesNum(), "Enter commit on offline @ suggestion must not insert newline")
}
