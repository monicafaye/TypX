package lsp

import (
	"strings"
	"testing"

	"github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/screen"
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

func readScreenRowText(sim tcell.SimulationScreen, x, y, width int) string {
	var rs []rune
	for col := 0; col < width; {
		r, _, _, rw := sim.GetContent(x+col, y)
		if r == 0 {
			r = ' '
		}
		rs = append(rs, r)
		if rw > 1 {
			col += rw
		} else {
			col++
		}
	}
	return strings.TrimRight(string(rs), " ")
}

func TestOpenListPaneWithOptionsPickerReuseAndCloseOnSelect(t *testing.T) {
	setupLSPEnv(t, 0)
	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	b := buffer.NewBufferFromString("start ", "main.typ", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{b})
	tab := action.MainTab()
	bp := tab.CurPane()
	require.NotNil(t, bp)

	// 1. Open Diagnostics first, move down, and turn on errors-only mode.
	diagItems := []ListItem{
		{Severity: SeverityWarning, Primary: "warn 1"},
		{Severity: SeverityError, Primary: "err 1"},
	}
	diagLP := OpenListPane(bp, "Diagnostics", true, diagItems)
	require.NotNil(t, diagLP)
	assert.Equal(t, "", diagLP.Badge())
	assert.Equal(t, "", diagLP.EnterLabel())
	assert.True(t, diagLP.IsDiagnosticsMode())
	assert.True(t, diagLP.IsFilterable())

	diagLP.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
	diagLP.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'f', tcell.ModNone, ""))
	assert.True(t, diagLP.IsErrorsOnly())

	// 2. Reuse the open ListPane for a custom CloseOnSelect picker with StartFiltering & InitialQuery.
	var callbackRan bool
	var paneDuringSelect *ListPane
	var activeDuringSelect action.Pane
	var bpActiveDuringSelect bool
	pickerItems := []ListItem{
		{Badge: "built-in", Primary: "frac", Secondary: "$1/$2"},
		{
			Badge:     "user",
			Primary:   "eq",
			Secondary: "$ a = b $",
			OnSelect: func() {
				callbackRan = true
				paneDuringSelect = ListPaneForTab(tab)
				activeDuringSelect = tab.ActivePane()
				bpActiveDuringSelect = bp.IsActive()
				b.Insert(buffer.Loc{X: 6, Y: 0}, "$ a = b $")
			},
		},
	}

	lp := OpenListPaneWithOptions(bp, ListPaneOptions{
		Title:           "Snippets",
		StartFiltering:  true,
		InitialQuery:    "eq",
		EnterLabel:      "Insert",
		Badge:           "SNIPPETS",
		DiagnosticsMode: false,
		CloseOnSelect:   true,
	}, pickerItems)
	require.Same(t, diagLP, lp)
	assert.Len(t, tab.Panes, 2)
	assert.Equal(t, "Snippets", lp.Name())
	assert.Equal(t, "SNIPPETS", lp.Badge())
	assert.Equal(t, "Insert", lp.EnterLabel())
	assert.False(t, lp.IsDiagnosticsMode())
	assert.False(t, lp.IsFilterable())
	assert.False(t, lp.IsErrorsOnly())
	assert.True(t, lp.IsFiltering())
	assert.Equal(t, "eq", lp.FilterQuery())
	assert.Equal(t, 0, lp.SelectedIndex())
	require.Len(t, lp.VisibleItems(), 1)
	assert.Equal(t, "eq", lp.VisibleItems()[0].Primary)

	// Exit filter mode and verify 'f' does NOT toggle errorsOnly in non-diagnostics mode.
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, ""))
	assert.False(t, lp.IsFiltering())
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'f', tcell.ModNone, ""))
	assert.False(t, lp.IsErrorsOnly())

	// Re-enter filter, type "eq", press Enter -> closes ListPane & restores bp focus BEFORE OnSelect.
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '/', tcell.ModNone, ""))
	for _, ch := range "eq" {
		lp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, ch, tcell.ModNone, ""))
	}
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))

	assert.True(t, callbackRan)
	assert.Nil(t, paneDuringSelect, "ListPane must be closed before OnSelect runs")
	assert.Same(t, bp, activeDuringSelect, "target BufPane must be active before OnSelect runs")
	assert.True(t, bpActiveDuringSelect, "target BufPane must be focused before OnSelect runs")
	assert.Equal(t, "start $ a = b $", string(b.Bytes()))
	assert.Nil(t, ListPaneForTab(tab))
	assert.Len(t, tab.Panes, 1)
}

func TestListPaneDisplayCustomFooterAndEscWithInitialQuery(t *testing.T) {
	setupLSPEnv(t, 0)
	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	b := buffer.NewBufferFromString("hello\n", "main.typ", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{b})
	tab := action.MainTab()
	bp := tab.CurPane()

	lp := OpenListPaneWithOptions(bp, ListPaneOptions{
		Title:           "Symbols",
		StartFiltering:  false,
		InitialQuery:    "foo",
		EnterLabel:      "Insert",
		Badge:           "SYMBOLS",
		DiagnosticsMode: false,
		CloseOnSelect:   true,
	}, []ListItem{{Badge: "math", Primary: "foo", Secondary: "φ"}})
	require.NotNil(t, lp)

	sim, ok := screen.Screen.(tcell.SimulationScreen)
	require.True(t, ok)

	lp.Display()
	v := lp.GetView()
	footer := readScreenRowText(sim, v.X, v.Y+v.Height-1, v.Width)
	assert.Contains(t, footer, "Enter:insert")
	assert.NotContains(t, footer, "f:errors only")
	assert.NotContains(t, footer, "Enter:jump")

	// When StartFiltering is false (even with InitialQuery), single Esc closes the pane.
	lp.HandleEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, ""))
	assert.Nil(t, ListPaneForTab(tab))
}
