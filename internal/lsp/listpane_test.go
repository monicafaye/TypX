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
