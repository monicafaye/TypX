package lsp

import (
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

func setupCompletionUnitEnv(t *testing.T, content, filename string) (*action.BufPane, *buffer.Buffer) {
	t.Helper()
	dir, _ := setupLSPEnv(t, 0)
	RegisterCompletionHooks()
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	b := openTestBuffer(t, filepath.Join(dir, filename), content)
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()
	require.NotNil(t, bp)
	bp.Resize(80, 23)
	return bp, b
}

func TestRegisterCompletionHooksIdempotent(t *testing.T) {
	RegisterCompletionHooks()
	RegisterCompletionHooks()
}

func TestAutoCompletionEnterCommitsAndEscDismisses(t *testing.T) {
	items := []CompletionItem{
		{Label: "alpha", InsertText: "alpha"},
		{Label: "algorithm", InsertText: "algorithm"},
	}

	t.Run("enter_commits_index_0", func(t *testing.T) {
		bp, b := setupCompletionUnitEnv(t, "#al", "auto_commit.typ")
		bp.Cursor.GotoLoc(buffer.Loc{X: 3, Y: 0})

		applyCompletionItems(bp, b, b.LineBytes(0), b.Version(), bp.Cursor.Loc, items, false)
		require.True(t, b.HasSuggestions)
		assert.Equal(t, -1, b.CurSuggestion)
		assert.Equal(t, "#al", string(b.Bytes()))

		bp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
		assert.Equal(t, "#alpha", string(b.Bytes()))
		assert.False(t, b.HasSuggestions)
		assert.Equal(t, buffer.Loc{X: 6, Y: 0}, bp.Cursor.Loc)

		// Next Enter inserts a normal newline
		bp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
		assert.Equal(t, "#alpha\n", string(b.Bytes()))
	})

	t.Run("esc_dismisses_without_mutation", func(t *testing.T) {
		bp2, b2 := setupCompletionUnitEnv(t, "#al", "auto_esc.typ")
		bp2.Cursor.GotoLoc(buffer.Loc{X: 3, Y: 0})
		applyCompletionItems(bp2, b2, b2.LineBytes(0), b2.Version(), bp2.Cursor.Loc, items, false)
		require.True(t, b2.HasSuggestions)

		bp2.HandleEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, ""))
		assert.Equal(t, "#al", string(b2.Bytes()))
		assert.False(t, b2.HasSuggestions)
		assert.Nil(t, getActiveCompletion(b2.SharedBuffer))
	})
}

func TestAutoCompletionSingleSnippetEnterExpands(t *testing.T) {
	bp, b := setupCompletionUnitEnv(t, "#tab", "auto_snip.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 4, Y: 0})

	var gotStart, gotEnd buffer.Loc
	var gotRaw string
	SetSnippetExpander(func(pane *action.BufPane, start, end buffer.Loc, rawSnippet string) bool {
		gotStart = start
		gotEnd = end
		gotRaw = rawSnippet
		pane.Buf.Replace(start, end, "table(2, body)")
		pane.Cursor.GotoLoc(buffer.Loc{X: start.X + 6, Y: start.Y})
		return true
	})
	t.Cleanup(func() { SetSnippetExpander(nil) })

	items := []CompletionItem{
		{
			Label:            "table",
			InsertText:       "table(${1:2}, ${2:body})$0",
			InsertTextFormat: InsertTextFormatSnippet,
		},
	}
	applyCompletionItems(bp, b, b.LineBytes(0), b.Version(), bp.Cursor.Loc, items, false)
	require.True(t, b.HasSuggestions)
	assert.Equal(t, -1, b.CurSuggestion)

	bp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	assert.Equal(t, buffer.Loc{X: 1, Y: 0}, gotStart)
	assert.Equal(t, buffer.Loc{X: 4, Y: 0}, gotEnd)
	assert.Equal(t, "table(${1:2}, ${2:body})$0", gotRaw)
	assert.Equal(t, "#table(2, body)", string(b.Bytes()))
	assert.False(t, b.HasSuggestions)
}

func TestMultiCandidateExplicitCycleEnterAndEsc(t *testing.T) {
	items := []CompletionItem{
		{Label: "tab_plain", InsertText: "tab_plain"},
		{Label: "table", InsertText: "table(${1:2}, ${2:body})$0", InsertTextFormat: InsertTextFormatSnippet},
		{Label: "tab", InsertText: "tab"}, // empty suffix when prefix is "tab"
	}

	t.Run("cycle_to_snippet_and_enter_expands", func(t *testing.T) {
		bp, b := setupCompletionUnitEnv(t, "#tab", "explicit_snip.typ")
		bp.Cursor.GotoLoc(buffer.Loc{X: 4, Y: 0})

		var gotStart, gotEnd buffer.Loc
		var gotRaw string
		SetSnippetExpander(func(pane *action.BufPane, start, end buffer.Loc, rawSnippet string) bool {
			gotStart = start
			gotEnd = end
			gotRaw = rawSnippet
			pane.Buf.Replace(start, end, "table(2, body)")
			return true
		})
		t.Cleanup(func() { SetSnippetExpander(nil) })

		applyCompletionItems(bp, b, b.LineBytes(0), b.Version(), bp.Cursor.Loc, items, true)
		require.True(t, b.HasSuggestions)
		assert.Equal(t, 0, b.CurSuggestion)
		assert.Equal(t, "#tab_plain", string(b.Bytes()))

		bp.HandleEvent(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone, ""))
		assert.Equal(t, 1, b.CurSuggestion)
		assert.Equal(t, "#table(2, body)", string(b.Bytes()))

		bp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
		assert.Equal(t, buffer.Loc{X: 1, Y: 0}, gotStart)
		assert.Equal(t, buffer.Loc{X: 15, Y: 0}, gotEnd)
		assert.Equal(t, "table(${1:2}, ${2:body})$0", gotRaw)
		assert.False(t, b.HasSuggestions)
		assert.NotContains(t, string(b.Bytes()), "\n")
	})

	t.Run("cycle_then_esc_reverts_and_runs_chained_actions", func(t *testing.T) {
		bp2, b2 := setupCompletionUnitEnv(t, "#tab", "explicit_esc.typ")
		bp2.Cursor.GotoLoc(buffer.Loc{X: 4, Y: 0})
		applyCompletionItems(bp2, b2, b2.LineBytes(0), b2.Version(), bp2.Cursor.Loc, items, true)
		assert.Equal(t, "#tab_plain", string(b2.Bytes()))

		bp2.HandleEvent(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone, ""))
		assert.Equal(t, "#table(2, body)", string(b2.Bytes()))
		bp2.HandleEvent(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone, ""))
		assert.Equal(t, "#tab_plain", string(b2.Bytes()))

		action.InfoBar.Message("temp info")
		bp2.HandleEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, ""))
		assert.Equal(t, "#tab", string(b2.Bytes()))
		assert.Equal(t, buffer.Loc{X: 4, Y: 0}, bp2.Cursor.Loc)
		assert.False(t, b2.HasSuggestions)
		assert.Equal(t, "", action.InfoBar.Msg, "chained ClearInfo on Esc must still run")
	})

	t.Run("enter_commits_plain_candidate", func(t *testing.T) {
		bp3, b3 := setupCompletionUnitEnv(t, "#tab", "explicit_plain.typ")
		bp3.Cursor.GotoLoc(buffer.Loc{X: 4, Y: 0})
		applyCompletionItems(bp3, b3, b3.LineBytes(0), b3.Version(), bp3.Cursor.Loc, items, true)
		bp3.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
		assert.Equal(t, "#tab_plain", string(b3.Bytes()))
		assert.False(t, b3.HasSuggestions)
	})

	t.Run("enter_commits_empty_suffix_candidate", func(t *testing.T) {
		bp4, b4 := setupCompletionUnitEnv(t, "#tab", "explicit_empty_suffix.typ")
		bp4.Cursor.GotoLoc(buffer.Loc{X: 4, Y: 0})
		applyCompletionItems(bp4, b4, b4.LineBytes(0), b4.Version(), bp4.Cursor.Loc, items, true)
		bp4.HandleEvent(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone, ""))
		bp4.HandleEvent(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone, ""))
		assert.Equal(t, 2, b4.CurSuggestion)
		bp4.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
		assert.Equal(t, "#tab", string(b4.Bytes()))
		assert.False(t, b4.HasSuggestions)
	})

	t.Run("single_non_snippet_clears_has_suggestions", func(t *testing.T) {
		bp5, b5 := setupCompletionUnitEnv(t, "#tab", "single_plain.typ")
		bp5.Cursor.GotoLoc(buffer.Loc{X: 4, Y: 0})
		applyCompletionItems(bp5, b5, b5.LineBytes(0), b5.Version(), bp5.Cursor.Loc, items[:1], true)
		assert.Equal(t, "#tab_plain", string(b5.Bytes()))
		assert.False(t, b5.HasSuggestions)
		bp5.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
		assert.Equal(t, "#tab_plain\n", string(b5.Bytes()))
	})
}

func TestCompletionHooksInvalidationAndIsolation(t *testing.T) {
	items := []CompletionItem{
		{Label: "alpha", InsertText: "alpha"},
		{Label: "algorithm", InsertText: "algorithm"},
	}

	t.Run("cursor_left_invalidates", func(t *testing.T) {
		bp, b := setupCompletionUnitEnv(t, "#al", "move.typ")
		bp.Cursor.GotoLoc(buffer.Loc{X: 3, Y: 0})
		applyCompletionItems(bp, b, b.LineBytes(0), b.Version(), bp.Cursor.Loc, items, true)
		assert.Equal(t, "#alpha", string(b.Bytes()))
		bp.HandleEvent(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone, ""))
		assert.False(t, b.HasSuggestions)
		bp.HandleEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, ""))
		assert.Equal(t, "#alpha", string(b.Bytes()))
		bp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
		assert.Contains(t, string(b.Bytes()), "\n")
	})

	t.Run("direct_goto_loc_invalidates", func(t *testing.T) {
		bp2, b2 := setupCompletionUnitEnv(t, "#al", "direct_move.typ")
		bp2.Cursor.GotoLoc(buffer.Loc{X: 3, Y: 0})
		applyCompletionItems(bp2, b2, b2.LineBytes(0), b2.Version(), bp2.Cursor.Loc, items, false)
		bp2.Cursor.GotoLoc(buffer.Loc{X: 1, Y: 0})
		bp2.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
		assert.Equal(t, "#\nal", string(b2.Bytes()))
	})

	t.Run("non_lsp_buffer_complete_untouched", func(t *testing.T) {
		bp3, b3 := setupCompletionUnitEnv(t, "foobar foo", "nonlsp.typ")
		bp3.Cursor.GotoLoc(buffer.Loc{X: 10, Y: 0})
		setActiveCompletion(b3.SharedBuffer, &activeCompletion{
			candidates: []completionCandidate{{label: "alpha", suffix: "pha"}},
			reqVersion: b3.Version(),
			reqLoc:     buffer.Loc{X: 10, Y: 0},
		})
		require.True(t, b3.ShowSuggestions(buffer.BufferComplete))
		require.True(t, b3.HasSuggestions)
		assert.False(t, handleCompletionInsertNewline(bp3), "LSP hook must ignore non-LSP BufferComplete")
		assert.True(t, b3.HasSuggestions, "LSP hook must not clear non-LSP HasSuggestions")
		assert.False(t, handleCompletionEscape(bp3), "LSP escape hook must ignore non-LSP BufferComplete")
		assert.True(t, b3.HasSuggestions, "LSP escape hook must not clear non-LSP HasSuggestions")
	})

	t.Run("multi_cursor_guard", func(t *testing.T) {
		bp4, b4 := setupCompletionUnitEnv(t, "#al\n#al", "mc.typ")
		bp4.Cursor.GotoLoc(buffer.Loc{X: 3, Y: 0})
		applyCompletionItems(bp4, b4, b4.LineBytes(0), b4.Version(), bp4.Cursor.Loc, items, false)
		c1 := buffer.NewCursor(b4, buffer.Loc{X: 3, Y: 1})
		c1.Num = 1
		b4.AddCursor(c1)
		assert.False(t, handleCompletionInsertNewline(bp4))
		assert.False(t, b4.HasSuggestions)
	})

	t.Run("split_buffer_isolation_and_close_cleanup", func(t *testing.T) {
		bp5, b5 := setupCompletionUnitEnv(t, "#al\nother", "split.typ")
		bp5.Cursor.GotoLoc(buffer.Loc{X: 3, Y: 0})
		applyCompletionItems(bp5, b5, b5.LineBytes(0), b5.Version(), bp5.Cursor.Loc, items, true)
		require.NotNil(t, getActiveCompletion(b5.SharedBuffer))
		assert.Equal(t, "#alpha\nother", string(b5.Bytes()))

		bSplit, err := buffer.NewBufferFromFile(b5.Path, buffer.BTDefault)
		require.NoError(t, err)
		bpSplit := action.NewBufPaneFromBuf(bSplit, action.MainTab())
		bpSplit.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 1})
		assert.False(t, handleCompletionInsertNewline(bpSplit), "split pane at different cursor must not commit bp5 completion")
		assert.Equal(t, "#alpha\nother", string(b5.Bytes()), "split pane must not mutate bp5 completion site")

		// Re-arm and verify OnBufferClose preserves entry while shared, evicts when last closes
		setActiveCompletion(b5.SharedBuffer, &activeCompletion{reqVersion: b5.Version()})
		require.True(t, bSplit.Shared())
		completionCleanupObserver{}.OnBufferClose(bSplit)
		assert.NotNil(t, getActiveCompletion(b5.SharedBuffer))
		bSplit.Close()

		require.False(t, b5.Shared())
		completionCleanupObserver{}.OnBufferClose(b5)
		assert.Nil(t, getActiveCompletion(b5.SharedBuffer))
	})
}

func TestLiveMockTinymistCompletionCycleEnterAndEsc(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)
	config.GlobalSettings["typst.autoComplete"] = false
	config.GlobalSettings["typst.signatureHelp"] = false
	RegisterCompletionHooks()

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected StateReady")

	// 1. LspCompleteAction -> Tab -> Esc reverts previewed suffix
	docPath := filepath.Join(dir, "live_esc.typ")
	b := openTestBuffer(t, docPath, "#al")
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()
	bp.Cursor.GotoLoc(buffer.Loc{X: 3, Y: 0})

	require.True(t, LspCompleteAction()(bp))
	waitForCondition(t, 2*time.Second, func() bool {
		return b.HasSuggestions && string(b.LineBytes(0)) == "#alpha"
	}, "expected explicit completion to insert alpha")

	bp.HandleEvent(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone, ""))
	assert.Equal(t, "#algorithm", string(b.LineBytes(0)))

	bp.HandleEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, ""))
	assert.Equal(t, "#al", string(b.LineBytes(0)))
	assert.Equal(t, buffer.Loc{X: 3, Y: 0}, bp.Cursor.Loc)
	assert.False(t, b.HasSuggestions)

	// 2. LspCompleteAction -> Tab -> Enter commits without inserting '\n'
	require.True(t, LspCompleteAction()(bp))
	waitForCondition(t, 2*time.Second, func() bool {
		return b.HasSuggestions && string(b.LineBytes(0)) == "#alpha"
	}, "expected explicit completion to insert alpha")
	bp.HandleEvent(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone, ""))
	assert.Equal(t, "#algorithm", string(b.LineBytes(0)))

	bp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	assert.Equal(t, "#algorithm", string(b.Bytes()))
	assert.False(t, b.HasSuggestions)
}

func TestAutoCompletionSingleSnippetTabExpandsAndPlainTextPreservesDollar(t *testing.T) {
	t.Run("tab_expands_single_auto_snippet", func(t *testing.T) {
		bp, b := setupCompletionUnitEnv(t, "#fig", "auto_snip_tab.typ")
		bp.Cursor.GotoLoc(buffer.Loc{X: 4, Y: 0})

		var gotRaw string
		SetSnippetExpander(func(pane *action.BufPane, start, end buffer.Loc, rawSnippet string) bool {
			gotRaw = rawSnippet
			pane.Buf.Replace(start, end, "figure(body)")
			return true
		})
		t.Cleanup(func() { SetSnippetExpander(nil) })

		items := []CompletionItem{
			{
				Label:            "figure",
				InsertText:       "figure(${1:body})$0",
				InsertTextFormat: InsertTextFormatSnippet,
			},
		}
		applyCompletionItems(bp, b, b.LineBytes(0), b.Version(), bp.Cursor.Loc, items, false)
		require.True(t, b.HasSuggestions)

		bp.HandleEvent(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone, ""))
		assert.Equal(t, "figure(${1:body})$0", gotRaw)
		assert.Equal(t, "#figure(body)", string(b.Bytes()))
		assert.False(t, b.HasSuggestions)
	})

	t.Run("plain_text_preserves_dollar", func(t *testing.T) {
		// Explicit InsertTextFormatPlainText with literal "$0$" must not be treated as a snippet.
		bp2, b2 := setupCompletionUnitEnv(t, "$", "plain_dollar.typ")
		bp2.Cursor.GotoLoc(buffer.Loc{X: 1, Y: 0})
		cands := buildCompletionCandidates(b2, b2.LineBytes(0), bp2.Cursor.Loc, []CompletionItem{
			{
				Label:            "$0$",
				InsertText:       "$0$",
				InsertTextFormat: InsertTextFormatPlainText,
			},
		})
		require.Len(t, cands, 1)
		assert.False(t, cands[0].isSnippet)
		assert.Equal(t, "$0$", cands[0].rawInsert)
	})
}
