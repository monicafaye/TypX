package outline

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/micro-editor/tcell/v2"
	lua "github.com/yuin/gopher-lua"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	ulua "github.com/monicafaye/TypX/internal/lua"
	"github.com/monicafaye/TypX/internal/screen"
)

var simScreen tcell.SimulationScreen

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "typx_outline_test_*")
	if err != nil {
		panic(err)
	}

	ulua.L = lua.NewState()
	_ = config.InitConfigDir(tmpDir)
	config.InitRuntimeFiles(false)
	_ = config.InitGlobalSettings()
	config.GlobalSettings["backup"] = false
	config.GlobalSettings["fastdirty"] = true
	config.GlobalSettings["multiopen"] = "tab"
	config.GlobalSettings["typst.outlineWidth"] = float64(30)
	config.GlobalSettings["typst.outlineShowLabels"] = false
	config.GlobalSettings["typst.debounceMs"] = float64(30)

	simScreen, err = screen.InitSimScreen()
	if err != nil {
		panic(err)
	}
	simScreen.SetSize(80, 24)
	simScreen.Show()

	Init()
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()
	buffer.SetMessager(action.InfoBar)
	_ = config.InitColorscheme()

	code := m.Run()
	SetSymbolProvider(nil)
	SetJumpHandler(nil)
	buffer.CloseOpenBuffers()
	buffer.FlushObservers()
	action.DrainUIQueue()
	_ = os.RemoveAll(tmpDir)
	os.Exit(code)
}

func setupTabs(t *testing.T, w, h int, docs ...struct{ name, text string }) []*action.BufPane {
	t.Helper()
	SetSymbolProvider(nil)
	SetJumpHandler(nil)
	for tab := range defaultManager.panes {
		defaultManager.CloseForTab(tab)
	}
	defaultManager.autoHidden = make(map[*action.Tab]bool)

	buffer.CloseOpenBuffers()
	buffer.FlushObservers()
	action.DrainUIQueue()
	if action.InfoBar != nil {
		action.InfoBar.Reset()
	}

	config.GlobalSettings["multiopen"] = "tab"
	config.GlobalSettings["typst.outlineWidth"] = float64(30)
	config.GlobalSettings["typst.outlineShowLabels"] = false
	config.GlobalSettings["typst.debounceMs"] = float64(30)

	simScreen.SetSize(w, h)
	simScreen.Show()
	simScreen.Clear()

	bufs := make([]*buffer.Buffer, len(docs))
	for i, d := range docs {
		bufs[i] = buffer.NewBufferFromString(d.text, d.name, buffer.BTDefault)
	}
	action.InitTabs(bufs)
	action.Tabs.Resize()
	action.Tabs.SetActive(0)

	panes := make([]*action.BufPane, len(action.Tabs.List))
	for i, tab := range action.Tabs.List {
		panes[i] = tab.CurPane()
	}

	t.Cleanup(func() {
		SetSymbolProvider(nil)
		SetJumpHandler(nil)
		for tab := range defaultManager.panes {
			defaultManager.CloseForTab(tab)
		}
		defaultManager.autoHidden = make(map[*action.Tab]bool)
		buffer.CloseOpenBuffers()
		buffer.FlushObservers()
		action.DrainUIQueue()
		if action.InfoBar != nil {
			action.InfoBar.Reset()
		}
	})
	return panes
}

func renderAll() {
	buffer.FlushObservers()
	screen.Screen.Fill(' ', config.DefStyle)
	action.Tabs.Display()
	for _, p := range action.MainTab().Panes {
		p.Display()
	}
	action.MainTab().Display()
	action.InfoBar.Display()
	screen.Screen.Show()
}

func screenRow(y int) string {
	w, h := simScreen.Size()
	if y < 0 || y >= h {
		return ""
	}
	var b strings.Builder
	for x := 0; x < w; {
		r, _, _, width := simScreen.GetContent(x, y)
		if r == 0 {
			r = ' '
		}
		b.WriteRune(r)
		if width < 1 {
			width = 1
		}
		x += width
	}
	return strings.TrimRight(b.String(), " ")
}

func paneScreenText(x, y, w, h int) string {
	rows := make([]string, h)
	for dy := 0; dy < h; dy++ {
		var b strings.Builder
		for dx := 0; dx < w; {
			r, _, _, width := simScreen.GetContent(x+dx, y+dy)
			if r == 0 {
				r = ' '
			}
			b.WriteRune(r)
			if width < 1 {
				width = 1
			}
			dx += width
		}
		rows[dy] = strings.TrimRight(b.String(), " ")
	}
	return strings.Join(rows, "\n")
}

func resizeScreen(w, h int) {
	simScreen.SetSize(w, h)
	simScreen.Show()
	action.InfoBar.HandleEvent(tcell.NewEventResize(w, h))
	action.Tabs.HandleEvent(tcell.NewEventResize(w, h))
	renderAll()
}

func sampleMultiSectionDoc() string {
	var lines []string
	lines = append(lines, "= Introduction <sec:intro>")
	for i := 0; i < 8; i++ {
		lines = append(lines, "Introductory paragraph line.")
	}
	lines = append(lines, "== Background")
	lines = append(lines, "```typ")
	lines = append(lines, "= Fake Heading Inside Code Block")
	lines = append(lines, "```")
	lines = append(lines, "/* outer /* nested */ = Fake Heading Inside Comment */")
	for i := 0; i < 8; i++ {
		lines = append(lines, "Background body line.")
	}
	lines = append(lines, "#figure(")
	lines = append(lines, "  rect(),")
	lines = append(lines, "  caption: [Pipeline Overview],")
	lines = append(lines, ") <fig:pipe>")
	for i := 0; i < 8; i++ {
		lines = append(lines, "Discussion body line.")
	}
	lines = append(lines, "= Conclusion")
	lines = append(lines, "Final remarks.")
	return strings.Join(lines, "\n")
}

func TestOutlineToggleNavigateJumpAndEscape(t *testing.T) {
	panes := setupTabs(t, 80, 24, struct{ name, text string }{
		name: "paper.typ",
		text: sampleMultiSectionDoc(),
	})
	bp := panes[0]
	tab := action.MainTab()

	// Open via :outline command.
	bp.HandleCommand("outline")
	renderAll()

	op := PaneForTab(tab)
	if op == nil {
		t.Fatal("expected OutlinePane to be open after :outline")
	}
	if len(tab.Panes) != 2 {
		t.Fatalf("expected 2 panes in tab, got %d", len(tab.Panes))
	}
	if tab.ActivePane() != op {
		t.Fatalf("expected OutlinePane to be focused on open, got %T", tab.ActivePane())
	}
	if tab.CurPane() != bp {
		t.Fatalf("expected Tab.CurPane() to fall back to linked BufPane while Outline is focused")
	}
	if op.GetView().X != 0 || op.GetView().Width != 30 {
		t.Fatalf("unexpected OutlinePane geometry: %+v", op.GetView())
	}
	if bp.GetView().X != 31 || bp.GetView().Width != 49 {
		t.Fatalf("unexpected editor BufPane geometry: %+v", bp.GetView())
	}

	items := op.Items()
	if len(items) != 4 {
		t.Fatalf("expected 4 outline items (ignoring raw/comment fakes), got %d: %#v", len(items), items)
	}

	ov := op.GetView()
	rendered := paneScreenText(ov.X, ov.Y, ov.Width, ov.Height)
	for _, want := range []string{"Outline (4)", "Introduction", "Background", "Fig: Pipeline Overview", "Conclusion"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected rendered outline pane to contain %q, got:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "Fake Heading") {
		t.Fatalf("rendered outline pane leaked fake heading from raw/comment block:\n%s", rendered)
	}

	// Move down twice (Introduction -> Background -> Pipeline Overview) and press Enter.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone, ""))
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	renderAll()

	if tab.ActivePane() != bp {
		t.Fatalf("expected Enter to return focus to editor BufPane, got %T", tab.ActivePane())
	}
	figLine := items[2].Line
	if bp.Cursor.Y != figLine {
		t.Fatalf("expected cursor to jump to figure line %d, got %d", figLine, bp.Cursor.Y)
	}
	if op.ActiveItemIndex() != 2 {
		t.Fatalf("expected active section index 2, got %d", op.ActiveItemIndex())
	}

	// Re-focus Outline, move down to Conclusion, then press Esc -> focus returns without jumping.
	bp.HandleCommand("outline focus")
	if tab.ActivePane() != op {
		t.Fatalf("expected :outline focus to focus OutlinePane")
	}
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, ""))
	if tab.ActivePane() != bp {
		t.Fatalf("expected Esc to return focus to editor")
	}
	if bp.Cursor.Y != figLine {
		t.Fatalf("Esc must not jump cursor; got %d, want %d", bp.Cursor.Y, figLine)
	}

	// Toggle closed from editor via Alt+O.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'o', tcell.ModAlt, ""))
	renderAll()
	if PaneForTab(tab) != nil {
		t.Fatal("expected Alt+O to close OutlinePane")
	}
	if len(tab.Panes) != 1 || bp.GetView().X != 0 || bp.GetView().Width != 80 {
		t.Fatalf("expected editor restored to full 80 cols, got panes=%d view=%+v", len(tab.Panes), bp.GetView())
	}

	// Re-open with Alt+O, then press Alt+O while focused inside OutlinePane to close.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'o', tcell.ModAlt, ""))
	if PaneForTab(tab) == nil || tab.ActivePane() != PaneForTab(tab) {
		t.Fatal("expected Alt+O to re-open and focus OutlinePane")
	}
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'o', tcell.ModAlt, ""))
	if PaneForTab(tab) != nil || tab.ActivePane() != bp {
		t.Fatal("expected Alt+O inside OutlinePane to close it and return focus to editor")
	}
}

func TestOutlineFilterAndMouseClick(t *testing.T) {
	panes := setupTabs(t, 80, 24, struct{ name, text string }{
		name: "paper.typ",
		text: sampleMultiSectionDoc(),
	})
	bp := panes[0]
	tab := action.MainTab()

	bp.HandleCommand("outline open")
	op := PaneForTab(tab)
	if op == nil {
		t.Fatal("expected OutlinePane open")
	}

	// Filter with '/' -> "concl" -> Enter jumps to Conclusion.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '/', tcell.ModNone, ""))
	for _, r := range "concl" {
		action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone, ""))
	}
	if len(op.VisibleItems()) != 1 || op.VisibleItems()[0].Title != "Conclusion" {
		t.Fatalf("expected 1 filtered item 'Conclusion', got %#v", op.VisibleItems())
	}
	conclLine := op.VisibleItems()[0].Line
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	if bp.Cursor.Y != conclLine || tab.ActivePane() != bp {
		t.Fatalf("filter Enter failed: cursor=%d want=%d active=%T", bp.Cursor.Y, conclLine, tab.ActivePane())
	}
	if op.FilterQuery() != "" || op.IsFiltering() {
		t.Fatalf("expected filter cleared after jump, got query=%q filtering=%v", op.FilterQuery(), op.IsFiltering())
	}

	// Mouse click on row 2 (item index 1 = Background) jumps and returns focus to editor.
	renderAll()
	bgLine := op.Items()[1].Line
	clickY := op.GetView().Y + 2 // row 0 = header, row 1 = idx 0, row 2 = idx 1
	action.Tabs.HandleEvent(tcell.NewEventMouse(4, clickY, tcell.Button1, tcell.ModNone, ""))
	action.Tabs.HandleEvent(tcell.NewEventMouse(4, clickY, tcell.ButtonNone, tcell.ModNone, ""))
	if bp.Cursor.Y != bgLine || tab.ActivePane() != bp {
		t.Fatalf("mouse click jump failed: cursor=%d want=%d active=%T", bp.Cursor.Y, bgLine, tab.ActivePane())
	}

	// Mouse wheel events should never panic.
	action.Tabs.HandleEvent(tcell.NewEventMouse(4, clickY, tcell.WheelDown, tcell.ModNone, ""))
	action.Tabs.HandleEvent(tcell.NewEventMouse(4, clickY, tcell.WheelUp, tcell.ModNone, ""))
}

func TestOutlineCursorTrackingAndDebouncedRefresh(t *testing.T) {
	panes := setupTabs(t, 80, 24, struct{ name, text string }{
		name: "notes.typ",
		text: "Preamble line\n= Alpha\nBody A\n== Beta\nBody B\n",
	})
	bp := panes[0]
	tab := action.MainTab()

	op, ok := Open(bp, false)
	if !ok || op == nil {
		t.Fatal("failed to open outline")
	}

	// Cursor on preamble line 0 (above first heading on line 1) -> activeIdx == -1.
	bp.Cursor.GotoLoc(buffer.Loc{X: 0, Y: 0})
	buffer.FlushObservers()
	if op.ActiveItemIndex() != -1 {
		t.Fatalf("expected activeIdx=-1 before first heading, got %d", op.ActiveItemIndex())
	}

	// Move cursor to line 4 (under == Beta on line 3) -> activeIdx == 1.
	bp.Cursor.GotoLoc(buffer.Loc{X: 0, Y: 4})
	buffer.FlushObservers()
	if op.ActiveItemIndex() != 1 {
		t.Fatalf("expected activeIdx=1 under Beta, got %d", op.ActiveItemIndex())
	}

	// Mutate buffer to add a new heading and wait for debounced refresh via UIQueue.
	end := bp.Buf.End()
	bp.Buf.Insert(end, "\n= Gamma\n")
	buffer.FlushObservers()

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		action.DrainUIQueue()
		if len(op.Items()) == 3 && op.Items()[2].Title == "Gamma" {
			break
		}
		select {
		case <-action.UIQueueReady():
		case <-time.After(10 * time.Millisecond):
		}
	}
	if len(op.Items()) != 3 || op.Items()[2].Title != "Gamma" {
		t.Fatalf("debounced refresh did not pick up '= Gamma': %#v", op.Items())
	}
	_ = tab
}

func TestEdgeCaseClosingMainBufferWhileOutlineOpen(t *testing.T) {
	t.Run("multi-tab close removes tab cleanly with no orphan", func(t *testing.T) {
		panes := setupTabs(t, 80, 24,
			struct{ name, text string }{"paper.typ", "= Paper\nBody\n"},
			struct{ name, text string }{"other.typ", "= Other\nBody\n"},
		)
		tab0 := action.Tabs.List[0]
		tab1 := action.Tabs.List[1]
		bp0 := panes[0]

		if _, ok := Open(bp0, true); !ok {
			t.Fatal("failed to open outline on tab 0")
		}
		if len(tab0.Panes) != 2 {
			t.Fatalf("expected 2 panes in tab 0, got %d", len(tab0.Panes))
		}

		// Close main buffer on tab 0 while Outline is open.
		bp0.ForceQuit()
		buffer.FlushObservers()
		renderAll()

		if len(action.Tabs.List) != 1 {
			t.Fatalf("expected tab 0 removed leaving 1 tab, got %d", len(action.Tabs.List))
		}
		if action.MainTab() != tab1 {
			t.Fatal("expected tab 1 to become active after closing tab 0")
		}
		if PaneForTab(tab0) != nil {
			t.Fatal("expected tab 0 OutlinePane closed and unregistered")
		}
		if tab1.CurPane() == nil || tab1.CurPane().Buf.GetName() != "other.typ" {
			t.Fatalf("unexpected active pane after tab close: %v", tab1.CurPane())
		}
	})

	t.Run("multi-split close retargets remaining typst split", func(t *testing.T) {
		panes := setupTabs(t, 100, 24,
			struct{ name, text string }{"a.typ", "= Doc A\n"},
		)
		tab := action.MainTab()
		bpA := panes[0]
		bufB := buffer.NewBufferFromString("= Doc B\n== Sub B\n", "b.typ", buffer.BTDefault)
		bpB := bpA.VSplitIndex(bufB, true)
		tab.SetActive(tab.GetPane(bpA.ID()))

		op, ok := Open(bpA, false)
		if !ok || op == nil {
			t.Fatal("failed to open outline on split tab")
		}
		if len(op.Items()) != 1 || op.Items()[0].Title != "Doc A" {
			t.Fatalf("expected outline of Doc A, got %#v", op.Items())
		}

		// Close bpA; Outline should stay open and automatically retarget bpB.
		bpA.ForceQuit()
		buffer.FlushObservers()
		renderAll()

		if PaneForTab(tab) == nil {
			t.Fatal("expected OutlinePane to stay open and retarget remaining .typ split")
		}
		if op.Target() != bpB {
			t.Fatalf("expected Outline target to switch to bpB")
		}
		if len(op.Items()) != 2 || op.Items()[0].Title != "Doc B" {
			t.Fatalf("expected outline refreshed to Doc B, got %#v", op.Items())
		}
		if tab.ActivePane() != bpB {
			t.Fatalf("expected focus on bpB after closing bpA, got %T", tab.ActivePane())
		}
	})

	t.Run("multi-split close closes outline if only non-typst split remains", func(t *testing.T) {
		panes := setupTabs(t, 100, 24,
			struct{ name, text string }{"a.typ", "= Doc A\n"},
		)
		tab := action.MainTab()
		bpA := panes[0]
		bufTxt := buffer.NewBufferFromString("plain notes\n", "notes.txt", buffer.BTDefault)
		bpTxt := bpA.VSplitIndex(bufTxt, true)
		tab.SetActive(tab.GetPane(bpA.ID()))

		if _, ok := Open(bpA, false); !ok {
			t.Fatal("failed to open outline")
		}

		bpA.ForceQuit()
		buffer.FlushObservers()
		renderAll()

		if PaneForTab(tab) != nil {
			t.Fatal("expected OutlinePane to close when no .typ pane remains in tab")
		}
		if len(tab.Panes) != 1 || tab.ActivePane() != bpTxt {
			t.Fatalf("expected only notes.txt pane remaining and active, got panes=%d active=%T", len(tab.Panes), tab.ActivePane())
		}
	})

	t.Run("single-tab last primary pane count and CloseAuxiliaryPanes", func(t *testing.T) {
		panes := setupTabs(t, 80, 24,
			struct{ name, text string }{"solo.typ", "= Solo\n"},
		)
		tab := action.MainTab()
		bp := panes[0]

		if _, ok := Open(bp, true); !ok {
			t.Fatal("failed to open outline")
		}
		if tab.PrimaryPaneCount() != 1 {
			t.Fatalf("expected PrimaryPaneCount()==1, got %d", tab.PrimaryPaneCount())
		}
		tab.CloseAuxiliaryPanes()
		if len(tab.Panes) != 1 || PaneForTab(tab) != nil || tab.ActivePane() != bp {
			t.Fatalf("CloseAuxiliaryPanes failed: panes=%d active=%T", len(tab.Panes), tab.ActivePane())
		}
	})
}

func TestEdgeCaseNarrowTerminalUnder50Cols(t *testing.T) {
	t.Run("refuse open when terminal width < 50", func(t *testing.T) {
		panes := setupTabs(t, 49, 24, struct{ name, text string }{"paper.typ", "= Intro\n"})
		bp := panes[0]
		tab := action.MainTab()

		bp.HandleCommand("outline")
		if PaneForTab(tab) != nil || len(tab.Panes) != 1 {
			t.Fatal("expected outline to be refused when terminal width is 49")
		}
		if !action.InfoBar.HasError || !strings.Contains(action.InfoBar.Msg, "50") {
			t.Fatalf("expected InfoBar error mentioning 50 cols, got hasErr=%v msg=%q", action.InfoBar.HasError, action.InfoBar.Msg)
		}

		// Alt+O also refuses cleanly.
		action.InfoBar.Reset()
		action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'o', tcell.ModAlt, ""))
		if PaneForTab(tab) != nil || !action.InfoBar.HasError {
			t.Fatal("expected Alt+O to be refused on 49-col terminal")
		}
	})

	t.Run("auto-hide on shrink below 50 and auto-restore on widen", func(t *testing.T) {
		panes := setupTabs(t, 80, 24, struct{ name, text string }{"paper.typ", "= Intro\n== Sub\n"})
		bp := panes[0]
		tab := action.MainTab()

		op, ok := Open(bp, true)
		if !ok || op == nil {
			t.Fatal("expected outline open at 80 cols")
		}

		// Shrink terminal to 40 cols -> Outline auto-hides, focus returns to editor, editor gets full 40 cols.
		resizeScreen(40, 24)
		if PaneForTab(tab) != nil {
			t.Fatal("expected OutlinePane to auto-hide when resized to 40 cols")
		}
		if len(tab.Panes) != 1 || tab.ActivePane() != bp {
			t.Fatalf("expected focus restored to editor at 40 cols, got panes=%d active=%T", len(tab.Panes), tab.ActivePane())
		}
		if bp.GetView().X != 0 || bp.GetView().Width != 40 {
			t.Fatalf("expected editor full width 40, got %+v", bp.GetView())
		}

		// Widen to 52 cols -> Outline auto-restores clamped to 26 cols, keeping editor focused.
		resizeScreen(52, 24)
		restored := PaneForTab(tab)
		if restored == nil {
			t.Fatal("expected OutlinePane to auto-restore when widened to 52 cols")
		}
		if tab.ActivePane() != bp {
			t.Fatalf("auto-restore should not steal focus from editor, active=%T", tab.ActivePane())
		}
		if restored.GetView().Width != 26 {
			t.Fatalf("expected clamped width 26 at 52 cols, got %d", restored.GetView().Width)
		}
		if bp.GetView().Width < 20 {
			t.Fatalf("editor width too narrow at 52 cols: %d", bp.GetView().Width)
		}

		// Widen to 80 cols -> Outline restores to configured 30 cols.
		resizeScreen(80, 24)
		if restored.GetView().Width != 30 {
			t.Fatalf("expected restored width 30 at 80 cols, got %d", restored.GetView().Width)
		}
	})
}

func TestEdgeCaseMultiTabSwitching(t *testing.T) {
	panes := setupTabs(t, 90, 24,
		struct{ name, text string }{"paper.typ", "= Paper Title\nBody\n== Paper Sec\n"},
		struct{ name, text string }{"appendix.typ", "= Appendix A\nBody\n== Appendix B\n"},
		struct{ name, text string }{"notes.md", "# Markdown\n"},
	)
	tab0, tab1, tab2 := action.Tabs.List[0], action.Tabs.List[1], action.Tabs.List[2]
	bp0, bp1, bp2 := panes[0], panes[1], panes[2]

	// Open Outline on Tab 0 (focused on Outline).
	op0, ok := Open(bp0, true)
	if !ok || op0 == nil {
		t.Fatal("failed to open outline on tab 0")
	}
	action.Tabs.UpdateNames()
	if action.Tabs.Names[0] != "paper.typ" {
		t.Fatalf("tab bar name should stay 'paper.typ' when Outline is focused, got %q", action.Tabs.Names[0])
	}

	// Switch to Tab 1 via Alt+. directly from focused OutlinePane.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '.', tcell.ModAlt, ""))
	renderAll()
	if action.MainTab() != tab1 {
		t.Fatalf("expected Alt+. from OutlinePane to switch to Tab 1")
	}
	if PaneForTab(tab1) != nil {
		t.Fatal("Tab 1 should not have an outline open yet")
	}
	if bp1.GetView().Width != 90 {
		t.Fatalf("Tab 1 editor should span full 90 cols, got %d", bp1.GetView().Width)
	}

	// Open Outline on Tab 1 too and verify complete isolation.
	op1, ok := Open(bp1, true)
	if !ok || op1 == nil {
		t.Fatal("failed to open outline on tab 1")
	}
	if op1.Items()[0].Title != "Appendix A" || op0.Items()[0].Title != "Paper Title" {
		t.Fatalf("outlines mixed across tabs: tab0=%#v tab1=%#v", op0.Items(), op1.Items())
	}

	// Jump to Appendix B on Tab 1; Tab 0 cursor must remain untouched.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	if bp1.Cursor.Y != 2 || bp0.Cursor.Y != 0 {
		t.Fatalf("cross-tab jump leaked: bp1.Y=%d bp0.Y=%d", bp1.Cursor.Y, bp0.Cursor.Y)
	}

	// Switch to Tab 2 (non-Typst notes.md) -> :outline refused cleanly.
	action.Tabs.SetActive(2)
	action.InfoBar.Reset()
	bp2.HandleCommand("outline")
	if PaneForTab(tab2) != nil || !action.InfoBar.HasError {
		t.Fatal("expected :outline refused on non-Typst tab")
	}

	// Switch back to Tab 0 and run :tabmove 2 -> Tab 0's outline must stay intact.
	action.Tabs.SetActive(0)
	bp0.HandleCommand("tabmove 2")
	if PaneForTab(tab0) != op0 {
		t.Fatal("expected :tabmove to preserve tab 0's OutlinePane")
	}
}

func TestOutlineWithExistingHSplit(t *testing.T) {
	panes := setupTabs(t, 100, 24, struct{ name, text string }{"paper.typ", "= Top\n== Mid\n"})
	bpTop := panes[0]
	tab := action.MainTab()
	bufBot := buffer.NewBufferFromString("= Bottom\n", "bot.typ", buffer.BTDefault)
	bpBot := bpTop.HSplitIndex(bufBot, true)
	tab.SetActive(tab.GetPane(bpTop.ID()))

	op, ok := Open(bpTop, false)
	if !ok || op == nil {
		t.Fatal("failed to open outline alongside HSplit")
	}
	renderAll()

	if op.GetView().X != 0 || op.GetView().Width != 30 || op.GetView().Height != tab.H {
		t.Fatalf("outline should dock full left height %d, got %+v", tab.H, op.GetView())
	}
	if bpTop.GetView().X != 31 || bpBot.GetView().X != 31 {
		t.Fatalf("horizontal splits should sit to the right of outline: top=%+v bot=%+v", bpTop.GetView(), bpBot.GetView())
	}

	// Switching focus to bottom .typ split should retarget the outline to bot.typ!
	tab.SetActive(tab.GetPane(bpBot.ID()))
	if op.Target() != bpBot || len(op.Items()) != 1 || op.Items()[0].Title != "Bottom" {
		t.Fatalf("expected outline to retarget bpBot on pane focus, got target=%v items=%#v", op.Target(), op.Items())
	}

	CloseForTab(tab)
	if bpTop.GetView().X != 0 || bpTop.GetView().Width != 100 || bpBot.GetView().X != 0 || bpBot.GetView().Width != 100 {
		t.Fatalf("after closing outline, HSplit panes should reclaim full 100 cols: top=%+v bot=%+v", bpTop.GetView(), bpBot.GetView())
	}
}

func TestOutlineLSPEnrichmentAndStaleVersionSafety(t *testing.T) {
	panes := setupTabs(t, 80, 24, struct{ name, text string }{
		name: "lsp.typ",
		text: "= Intro\n#my-section[Dynamic]\n#figure(rect(), caption: [Chart])\n",
	})
	bp := panes[0]

	var wg sync.WaitGroup
	staleRelease := make(chan struct{})
	firstCall := true

	SetSymbolProvider(func(b *buffer.Buffer, cb func(version uint64, symbols []Symbol)) bool {
		ver := b.Version()
		isFirst := firstCall
		firstCall = false
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !isFirst {
				<-staleRelease
			}
			action.EnqueueUI(func() {
				cb(ver, []Symbol{
					{
						Name: "Intro",
						Kind: lspSymbolKindNamespace,
						Line: 0,
						Children: []Symbol{
							{Name: "Dynamic Section", Kind: lspSymbolKindNamespace, Line: 1},
						},
					},
				})
			})
		}()
		return true
	})

	op, ok := Open(bp, false)
	if !ok || op == nil {
		t.Fatal("failed to open outline")
	}

	// Wait for first async enrichment callback and drain UI queue.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		action.DrainUIQueue()
		if len(op.Items()) == 3 {
			break
		}
		select {
		case <-action.UIQueueReady():
		case <-time.After(10 * time.Millisecond):
		}
	}

	items := op.Items()
	if len(items) != 3 || items[1].Title != "Dynamic Section" || items[2].Kind != KindFigure {
		t.Fatalf("expected LSP enrichment merged with local figure, got %#v", items)
	}

	// Trigger a second request, then immediately mutate buffer before it resolves.
	op.RefreshNow(0, true)
	bp.Buf.Insert(bp.Buf.End(), "\n= Added After\n")
	buffer.FlushObservers()
	close(staleRelease)
	wg.Wait()
	action.DrainUIQueue()

	// Stale callback must not overwrite newer version; refresh now shows '= Added After'.
	op.RefreshNow(0, false)
	found := false
	for _, it := range op.Items() {
		if it.Title == "Added After" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected '= Added After' present after stale LSP callback ignored: %#v", op.Items())
	}
}

func TestOutlineCurrentItemUsesCursorLineAsBackground(t *testing.T) {
	origScheme := config.Colorscheme
	origDef := config.DefStyle
	t.Cleanup(func() {
		config.Colorscheme = origScheme
		config.DefStyle = origDef
	})

	for _, schemeName := range []string{"typx-dark", "typx-light"} {
		t.Run(schemeName, func(t *testing.T) {
			var parsed []string
			cs, err := config.LoadColorscheme(schemeName, &parsed)
			if err != nil {
				t.Fatalf("LoadColorscheme(%q): %v", schemeName, err)
			}
			config.Colorscheme = cs

			clStyle, ok := config.Colorscheme["cursor-line"]
			if !ok {
				t.Fatalf("%s missing cursor-line link", schemeName)
			}
			wantBG, _, _ := clStyle.Decompose()
			wantH1FG, _, _ := config.Colorscheme["identifier.class"].Decompose()

			op := &OutlinePane{}
			got := op.itemStyle(Item{Kind: KindHeading, Level: 1, Title: "Foundations"}, false, true)
			gotFG, gotBG, gotAttr := got.Decompose()

			if gotBG != wantBG {
				t.Fatalf("%s: expected cursor-line color %v as background, got %v", schemeName, wantBG, gotBG)
			}
			if gotFG == wantBG {
				t.Fatalf("%s: foreground must not equal cursor-line background (%v)", schemeName, wantBG)
			}
			if gotFG != wantH1FG {
				t.Fatalf("%s: expected level-1 heading fg %v preserved on active item, got %v", schemeName, wantH1FG, gotFG)
			}
			if gotAttr&tcell.AttrBold == 0 {
				t.Fatalf("%s: expected active item to be bold", schemeName)
			}
		})
	}
}

func TestOutlineJumpHandlerInvokedOnEnterClickAndFilter(t *testing.T) {
	panes := setupTabs(t, 80, 24, struct{ name, text string }{
		name: "paper.typ",
		text: sampleMultiSectionDoc(),
	})
	bp := panes[0]
	tab := action.MainTab()

	type jumpRecord struct {
		pane     *action.BufPane
		item     Item
		allCount int
	}
	var jumps []jumpRecord
	SetJumpHandler(func(target *action.BufPane, item Item, allItems []Item) {
		jumps = append(jumps, jumpRecord{
			pane:     target,
			item:     item,
			allCount: len(allItems),
		})
	})

	bp.HandleCommand("outline")
	renderAll()
	op := PaneForTab(tab)
	if op == nil {
		t.Fatal("expected OutlinePane open")
	}

	// 1. Enter on second item ("Background") fires JumpHandler with full item list.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	if len(jumps) != 1 {
		t.Fatalf("expected 1 jump after Enter, got %d", len(jumps))
	}
	if jumps[0].pane != bp || jumps[0].item.Title != "Background" || jumps[0].allCount != 4 {
		t.Fatalf("unexpected Enter jump record: %+v", jumps[0])
	}

	// 2. Escape returns focus without invoking JumpHandler.
	bp.HandleCommand("outline focus")
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, ""))
	if len(jumps) != 1 {
		t.Fatalf("expected Esc not to invoke JumpHandler, got %d jumps", len(jumps))
	}

	// 3. Mouse click on 4th entry ("Conclusion", row 1+3=4) fires JumpHandler.
	ov := op.GetView()
	action.Tabs.HandleEvent(tcell.NewEventMouse(ov.X+2, ov.Y+4, tcell.Button1, tcell.ModNone, ""))
	action.Tabs.HandleEvent(tcell.NewEventMouse(ov.X+2, ov.Y+4, tcell.ButtonNone, tcell.ModNone, ""))
	if len(jumps) != 2 || jumps[1].item.Title != "Conclusion" || jumps[1].allCount != 4 {
		t.Fatalf("unexpected click jump record: %#v", jumps)
	}

	// 4. Filter "/Pipe" + Enter still delivers full unfiltered allItems (4 items).
	bp.HandleCommand("outline focus")
	for _, r := range "/Pipe" {
		action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone, ""))
	}
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	if len(jumps) != 3 || jumps[2].item.Kind != KindFigure || jumps[2].allCount != 4 {
		t.Fatalf("unexpected filter jump record: %#v", jumps)
	}
}
