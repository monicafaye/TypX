package lsp

import (
	"testing"

	"github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
)

func TestListPaneDockNavigateFilterSelectAndClose(t *testing.T) {
	setupLSPEnv(t, 0)
	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	b := buffer.NewBufferFromString("line 1\nline 2\n", "main.typ", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{b})
	tab := action.MainTab()
	bp := tab.CurPane()
	require.NotNil(t, bp)

	var selected []string
	items := []ListItem{
		{FilePath: "/tmp/a.typ", Line: 1, Col: 0, Severity: SeverityWarning, Primary: "warn item", OnSelect: func() { selected = append(selected, "warn") }},
		{FilePath: "/tmp/a.typ", Line: 4, Col: 2, Severity: SeverityError, Primary: "err item", OnSelect: func() { selected = append(selected, "err") }},
	}

	lp := OpenListPane(bp, "Diagnostics", true, items)
	require.NotNil(t, lp)
	assert.Equal(t, lp, ListPaneForTab(tab))
	assert.True(t, lp.IsActive())
	assert.Len(t, tab.Panes, 2)
	assert.Equal(t, 10, lp.GetView().Height)

	// Navigate down -> Enter selects second item and returns focus to editor.
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
	assert.Equal(t, 1, lp.SelectedIndex())
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	assert.Equal(t, []string{"err"}, selected)
	assert.Equal(t, bp, tab.ActivePane())

	// Reopening updates existing ListPane rather than stacking a second pane.
	lp2 := OpenListPane(bp, "Diagnostics", true, items)
	assert.Equal(t, lp, lp2)
	assert.Len(t, tab.Panes, 2)
	assert.True(t, lp.IsActive())

	// 'f' toggles Errors-only filter.
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'f', tcell.ModNone, ""))
	require.Len(t, lp.VisibleItems(), 1)
	assert.Equal(t, "err item", lp.VisibleItems()[0].Primary)

	// 'f' again restores all items.
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'f', tcell.ModNone, ""))
	assert.Len(t, lp.VisibleItems(), 2)

	// Esc closes drawer and restores focus to editor.
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, ""))
	assert.Nil(t, ListPaneForTab(tab))
	assert.Len(t, tab.Panes, 1)
	assert.Equal(t, bp, tab.ActivePane())
}

func TestListPaneTextFilter(t *testing.T) {
	setupLSPEnv(t, 0)
	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	b := buffer.NewBufferFromString("hello\n", "main.typ", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{b})
	tab := action.MainTab()
	bp := tab.CurPane()

	var picked []string
	items := []ListItem{
		{Badge: "FN", Primary: "computeAlpha", Secondary: "main.typ:1:1", OnSelect: func() { picked = append(picked, "computeAlpha") }},
		{Badge: "VAR", Primary: "betaWeight", Secondary: "sub.typ:4:3", OnSelect: func() { picked = append(picked, "betaWeight") }},
		{Badge: "FN", Primary: "formatSummary", Secondary: "main.typ:10:1", OnSelect: func() { picked = append(picked, "formatSummary") }},
	}

	lp := OpenListPane(bp, "Workspace Symbols", false, items)
	require.NotNil(t, lp)
	assert.False(t, lp.IsFiltering())

	// Press '/' -> enter filter mode, type "beta", backspace -> "bet", verify 1 match.
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '/', tcell.ModNone, ""))
	assert.True(t, lp.IsFiltering())
	for _, ch := range "betz" {
		lp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, ch, tcell.ModNone, ""))
	}
	assert.Len(t, lp.VisibleItems(), 0)
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyBackspace2, 0, tcell.ModNone, ""))
	assert.Equal(t, "bet", lp.FilterQuery())
	require.Len(t, lp.VisibleItems(), 1)
	assert.Equal(t, "betaWeight", lp.VisibleItems()[0].Primary)

	// Enter selects filtered item cleanly.
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	assert.Equal(t, []string{"betaWeight"}, picked)

	// Re-enter filter, Esc clears filter query without closing ListPane, second Esc closes.
	for i, p := range tab.Panes {
		if p == lp {
			tab.SetActive(i)
			break
		}
	}
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '/', tcell.ModNone, ""))
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone, ""))
	assert.Len(t, lp.VisibleItems(), 0)
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, ""))
	assert.False(t, lp.IsFiltering())
	assert.Equal(t, "", lp.FilterQuery())
	assert.Len(t, lp.VisibleItems(), 3)
	require.NotNil(t, ListPaneForTab(tab))

	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, ""))
	assert.Nil(t, ListPaneForTab(tab))
}
