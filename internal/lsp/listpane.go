package lsp

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mattn/go-runewidth"
	"github.com/micro-editor/tcell/v2"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/display"
	"github.com/monicafaye/TypX/internal/screen"
)

const (
	defaultListPaneHeight = 10
	minListPaneHeight     = 4
	minEditorHeightRows   = 6
)

// ListItem represents one selectable row in the bottom-docked ListPane.
type ListItem struct {
	FilePath  string
	Line      int // 0-based
	Col       int // 0-based
	Severity  int // 0 or SeverityError/SeverityWarning/SeverityInformation/SeverityHint
	Badge     string
	Primary   string
	Secondary string
	OnSelect  func()
}

// ListPane implements action.Pane, action.AuxiliaryPane, and display.Window as a
// reusable bottom-docked interactive list (used for diagnostics, definitions, etc.).
type ListPane struct {
	view   *display.View
	id     uint64
	tab    *action.Tab
	target *action.BufPane

	title         string
	filterable    bool
	errorsOnly    bool
	filtering     bool
	filterQuery   string
	items         []ListItem
	visible       []ListItem
	selectedIdx   int
	scrollTop     int
	active        bool
	mouseReleased bool
	closed        bool
}

var (
	_ action.Pane          = (*ListPane)(nil)
	_ action.AuxiliaryPane = (*ListPane)(nil)
	_ display.Window       = (*ListPane)(nil)

	listPanesMu  sync.Mutex
	tabListPanes = make(map[*action.Tab]*ListPane)
)

// ListPaneForTab returns the currently open ListPane for tab, or nil.
func ListPaneForTab(tab *action.Tab) *ListPane {
	if tab == nil {
		return nil
	}
	listPanesMu.Lock()
	lp := tabListPanes[tab]
	listPanesMu.Unlock()
	if lp == nil || lp.closed {
		return nil
	}
	for _, p := range tab.Panes {
		if p == lp {
			return lp
		}
	}
	listPanesMu.Lock()
	if tabListPanes[tab] == lp {
		delete(tabListPanes, tab)
	}
	listPanesMu.Unlock()
	return nil
}

// OpenListPane opens or updates the single bottom-docked ListPane on bp's Tab
// and focuses it.
func OpenListPane(bp *action.BufPane, title string, filterable bool, items []ListItem) *ListPane {
	if bp == nil || bp.Tab() == nil {
		return nil
	}
	tab := bp.Tab()
	if existing := ListPaneForTab(tab); existing != nil {
		existing.target = bp
		existing.title = title
		existing.filterable = filterable
		existing.errorsOnly = false
		existing.filtering = false
		existing.filterQuery = ""
		existing.setItems(items)
		for i, p := range tab.Panes {
			if p == existing {
				tab.SetActive(i)
				break
			}
		}
		return existing
	}

	termH := tab.Node.H
	if termH <= 0 && screen.Screen != nil {
		_, termH = screen.Screen.Size()
	}
	effH := effectiveListHeight(termH, readGlobalInt("typst.listPaneHeight", defaultListPaneHeight))
	if effH <= 0 {
		notifyInfoError("Terminal height too small for bottom list drawer")
		return nil
	}

	dockID := tab.Node.DockBottom(effH)
	if dockID == 0 {
		notifyInfoError("Unable to dock bottom list drawer in current layout")
		return nil
	}

	lp := &ListPane{
		view:          &display.View{},
		id:            dockID,
		tab:           tab,
		target:        bp,
		title:         title,
		filterable:    filterable,
		mouseReleased: true,
	}
	lp.setItems(items)

	listPanesMu.Lock()
	tabListPanes[tab] = lp
	listPanesMu.Unlock()

	idx := len(tab.Panes)
	tab.AddPane(lp, idx)
	tab.Resize()
	tab.SetActive(idx)
	return lp
}

// CloseListPaneForTab closes and unsplits the bottom ListPane on tab if open.
func CloseListPaneForTab(tab *action.Tab) bool {
	lp := ListPaneForTab(tab)
	if lp == nil {
		return false
	}
	wasFocused := tab.ActivePane() == lp
	target := lp.resolveTarget()
	id := lp.ID()

	lp.Close()
	if n := tab.GetNode(id); n != nil {
		n.Unsplit()
	}
	for i, p := range tab.Panes {
		if p == lp {
			tab.RemovePane(i)
			break
		}
	}
	tab.Resize()

	if len(tab.Panes) > 0 {
		if wasFocused && target != nil {
			for i, p := range tab.Panes {
				if p == target {
					tab.SetActive(i)
					return true
				}
			}
		}
		tab.SetActive(tab.ActiveIndex())
	}
	return true
}

func effectiveListHeight(termH, configured int) int {
	if termH < minListPaneHeight+minEditorHeightRows {
		return 0
	}
	h := configured
	if h <= 0 {
		h = defaultListPaneHeight
	}
	maxH := termH - minEditorHeightRows
	if h > maxH {
		h = maxH
	}
	if h < minListPaneHeight {
		h = minListPaneHeight
	}
	return h
}

func (lp *ListPane) ID() uint64                 { return lp.id }
func (lp *ListPane) SetID(i uint64)             { lp.id = i }
func (lp *ListPane) Name() string               { return lp.title }
func (lp *ListPane) SetTab(t *action.Tab)       { lp.tab = t }
func (lp *ListPane) Tab() *action.Tab           { return lp.tab }
func (lp *ListPane) IsAuxiliary() bool          { return true }
func (lp *ListPane) GetView() *display.View     { return lp.view }
func (lp *ListPane) IsActive() bool             { return lp.active }
func (lp *ListPane) IsFilterable() bool         { return lp.filterable }
func (lp *ListPane) IsErrorsOnly() bool         { return lp.errorsOnly }
func (lp *ListPane) IsFiltering() bool          { return lp.filtering }
func (lp *ListPane) FilterQuery() string        { return lp.filterQuery }
func (lp *ListPane) VisibleItems() []ListItem   { return append([]ListItem(nil), lp.visible...) }
func (lp *ListPane) SelectedIndex() int         { return lp.selectedIdx }
func (lp *ListPane) LocFromVisual(v buffer.Loc) buffer.Loc { return v }

func (lp *ListPane) SetView(v *display.View) {
	if v != nil {
		lp.view = v
	}
}

func (lp *ListPane) Relocate() bool {
	lp.ensureSelectionVisible()
	return true
}

func (lp *ListPane) Resize(w, h int) {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	lp.view.Width = w
	lp.view.Height = h
	lp.ensureSelectionVisible()
}

func (lp *ListPane) SetActive(b bool) {
	lp.active = b
	if b {
		lp.ensureSelectionVisible()
	}
}

func (lp *ListPane) Clear() {
	if screen.Screen == nil || lp.view.Width <= 0 || lp.view.Height <= 0 {
		return
	}
	for y := 0; y < lp.view.Height; y++ {
		for x := 0; x < lp.view.Width; x++ {
			screen.SetContent(lp.view.X+x, lp.view.Y+y, ' ', nil, config.DefStyle)
		}
	}
}

func (lp *ListPane) Close() {
	if lp.closed {
		return
	}
	lp.closed = true
	listPanesMu.Lock()
	if lp.tab != nil && tabListPanes[lp.tab] == lp {
		delete(tabListPanes, lp.tab)
	}
	listPanesMu.Unlock()
}

func (lp *ListPane) setItems(items []ListItem) {
	lp.items = append([]ListItem(nil), items...)
	lp.rebuildVisible()
}

func (lp *ListPane) rebuildVisible() {
	q := strings.ToLower(strings.TrimSpace(lp.filterQuery))
	if !lp.errorsOnly && q == "" {
		lp.visible = lp.items
	} else {
		filtered := make([]ListItem, 0, len(lp.items))
		for _, it := range lp.items {
			if lp.errorsOnly && it.Severity != SeverityError && it.Severity != 0 {
				continue
			}
			if q != "" && !matchesListItemQuery(it, q) {
				continue
			}
			filtered = append(filtered, it)
		}
		lp.visible = filtered
	}
	if len(lp.visible) == 0 {
		lp.selectedIdx = 0
		lp.scrollTop = 0
		return
	}
	if lp.selectedIdx < 0 {
		lp.selectedIdx = 0
	} else if lp.selectedIdx >= len(lp.visible) {
		lp.selectedIdx = len(lp.visible) - 1
	}
	lp.ensureSelectionVisible()
}

func matchesListItemQuery(it ListItem, q string) bool {
	return strings.Contains(strings.ToLower(it.Primary), q) ||
		strings.Contains(strings.ToLower(it.Secondary), q) ||
		strings.Contains(strings.ToLower(it.Badge), q) ||
		strings.Contains(strings.ToLower(it.FilePath), q)
}

func (lp *ListPane) hasFooter() bool {
	return lp.view.Height >= 4
}

func (lp *ListPane) listHeight() int {
	reserved := 1 // header
	if lp.hasFooter() {
		reserved++
	}
	lh := lp.view.Height - reserved
	if lh < 0 {
		return 0
	}
	return lh
}

func (lp *ListPane) ensureSelectionVisible() {
	lh := lp.listHeight()
	if lh <= 0 || len(lp.visible) == 0 {
		lp.scrollTop = 0
		return
	}
	if lp.selectedIdx < lp.scrollTop {
		lp.scrollTop = lp.selectedIdx
	} else if lp.selectedIdx >= lp.scrollTop+lh {
		lp.scrollTop = lp.selectedIdx - lh + 1
	}
	maxScroll := len(lp.visible) - lh
	if maxScroll < 0 {
		maxScroll = 0
	}
	if lp.scrollTop > maxScroll {
		lp.scrollTop = maxScroll
	}
	if lp.scrollTop < 0 {
		lp.scrollTop = 0
	}
}

func (lp *ListPane) moveSelection(delta int) {
	if len(lp.visible) == 0 {
		return
	}
	next := lp.selectedIdx + delta
	if next < 0 {
		next = 0
	} else if next >= len(lp.visible) {
		next = len(lp.visible) - 1
	}
	lp.selectedIdx = next
	lp.ensureSelectionVisible()
}

func (lp *ListPane) scrollBy(delta int) {
	lh := lp.listHeight()
	if lh <= 0 || len(lp.visible) == 0 {
		return
	}
	maxScroll := len(lp.visible) - lh
	if maxScroll < 0 {
		maxScroll = 0
	}
	next := lp.scrollTop + delta
	if next < 0 {
		next = 0
	} else if next > maxScroll {
		next = maxScroll
	}
	lp.scrollTop = next
}

func (lp *ListPane) resolveTarget() *action.BufPane {
	if lp.tab == nil {
		return nil
	}
	for _, p := range lp.tab.Panes {
		if bp, ok := p.(*action.BufPane); ok && bp == lp.target {
			return bp
		}
	}
	return lp.tab.CurPane()
}

func (lp *ListPane) returnFocus() {
	if lp.tab == nil {
		return
	}
	target := lp.resolveTarget()
	if target != nil {
		for i, p := range lp.tab.Panes {
			if p == target {
				lp.tab.SetActive(i)
				return
			}
		}
	}
	for i, p := range lp.tab.Panes {
		if p != lp && !action.IsAuxiliaryPane(p) {
			lp.tab.SetActive(i)
			return
		}
	}
}

func (lp *ListPane) activateSelected() {
	if len(lp.visible) == 0 || lp.selectedIdx < 0 || lp.selectedIdx >= len(lp.visible) {
		CloseListPaneForTab(lp.tab)
		return
	}
	item := lp.visible[lp.selectedIdx]
	if item.OnSelect != nil {
		item.OnSelect()
	}
	if !lp.closed && lp.tab != nil && action.MainTab() == lp.tab && lp.tab.ActivePane() == lp {
		lp.returnFocus()
	}
}

func (lp *ListPane) HandleCommand(text string) {
	if lp.tab != nil {
		if cp := lp.tab.CurPane(); cp != nil {
			cp.HandleCommand(text)
		}
	}
}

func (lp *ListPane) HandleEvent(event tcell.Event) {
	switch e := event.(type) {
	case *tcell.EventKey:
		lp.handleKeyEvent(e)
	case *tcell.EventMouse:
		lp.handleMouseEvent(e)
	}
}

func (lp *ListPane) handleKeyEvent(e *tcell.EventKey) {
	mod := e.Modifiers()
	key := e.Key()
	r := e.Rune()

	if key == tcell.KeyCtrlE {
		if action.InfoBar != nil && lp.tab != nil {
			tab := lp.tab
			action.InfoBar.Prompt("> ", "", "Command", nil, func(resp string, canceled bool) {
				if !canceled {
					if cp := tab.CurPane(); cp != nil {
						cp.HandleCommand(resp)
					}
				}
			})
		}
		return
	}
	if key == tcell.KeyCtrlW {
		if lp.tab != nil && len(lp.tab.Panes) > 1 {
			next := (lp.tab.ActiveIndex() + 1) % len(lp.tab.Panes)
			lp.tab.SetActive(next)
		}
		return
	}
	if key == tcell.KeyCtrlQ {
		CloseListPaneForTab(lp.tab)
		return
	}

	if lp.filtering {
		switch key {
		case tcell.KeyEscape:
			lp.filtering = false
			lp.filterQuery = ""
			lp.rebuildVisible()
			return
		case tcell.KeyEnter:
			lp.activateSelected()
			return
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			runes := []rune(lp.filterQuery)
			if len(runes) > 0 {
				lp.filterQuery = string(runes[:len(runes)-1])
				lp.rebuildVisible()
			}
			return
		case tcell.KeyUp:
			lp.moveSelection(-1)
			return
		case tcell.KeyDown:
			lp.moveSelection(1)
			return
		case tcell.KeyHome:
			if len(lp.visible) > 0 {
				lp.selectedIdx = 0
				lp.ensureSelectionVisible()
			}
			return
		case tcell.KeyEnd:
			if len(lp.visible) > 0 {
				lp.selectedIdx = len(lp.visible) - 1
				lp.ensureSelectionVisible()
			}
			return
		case tcell.KeyPgUp:
			step := lp.listHeight()
			if step < 1 {
				step = 1
			}
			lp.moveSelection(-step)
			return
		case tcell.KeyPgDn:
			step := lp.listHeight()
			if step < 1 {
				step = 1
			}
			lp.moveSelection(step)
			return
		case tcell.KeyRune:
			if mod&(tcell.ModAlt|tcell.ModCtrl) == 0 && r != 0 {
				lp.filterQuery += string(r)
				lp.rebuildVisible()
			}
			return
		}
		return
	}

	switch key {
	case tcell.KeyEscape:
		CloseListPaneForTab(lp.tab)
	case tcell.KeyTab:
		lp.returnFocus()
	case tcell.KeyEnter:
		lp.activateSelected()
	case tcell.KeyUp:
		lp.moveSelection(-1)
	case tcell.KeyDown:
		lp.moveSelection(1)
	case tcell.KeyHome:
		if len(lp.visible) > 0 {
			lp.selectedIdx = 0
			lp.ensureSelectionVisible()
		}
	case tcell.KeyEnd:
		if len(lp.visible) > 0 {
			lp.selectedIdx = len(lp.visible) - 1
			lp.ensureSelectionVisible()
		}
	case tcell.KeyPgUp:
		step := lp.listHeight()
		if step < 1 {
			step = 1
		}
		lp.moveSelection(-step)
	case tcell.KeyPgDn:
		step := lp.listHeight()
		if step < 1 {
			step = 1
		}
		lp.moveSelection(step)
	case tcell.KeyRune:
		if mod&(tcell.ModAlt|tcell.ModCtrl) != 0 {
			return
		}
		switch r {
		case 'k':
			lp.moveSelection(-1)
		case 'j':
			lp.moveSelection(1)
		case 'g':
			if len(lp.visible) > 0 {
				lp.selectedIdx = 0
				lp.ensureSelectionVisible()
			}
		case 'G':
			if len(lp.visible) > 0 {
				lp.selectedIdx = len(lp.visible) - 1
				lp.ensureSelectionVisible()
			}
		case 'f':
			if lp.filterable {
				lp.errorsOnly = !lp.errorsOnly
				lp.rebuildVisible()
			}
		case '/':
			lp.filtering = true
		case 'q':
			CloseListPaneForTab(lp.tab)
		}
	}
}

func (lp *ListPane) handleMouseEvent(e *tcell.EventMouse) {
	mx, my := e.Position()
	btn := e.Buttons()

	switch btn {
	case tcell.Button1:
		wasReleased := lp.mouseReleased
		lp.mouseReleased = false
		if !wasReleased {
			return
		}
		if mx < lp.view.X || mx >= lp.view.X+lp.view.Width {
			return
		}
		localY := my - lp.view.Y
		lh := lp.listHeight()
		if localY >= 1 && localY <= lh {
			idx := lp.scrollTop + (localY - 1)
			if idx >= 0 && idx < len(lp.visible) {
				lp.selectedIdx = idx
				lp.activateSelected()
			}
		}
	case tcell.ButtonNone:
		lp.mouseReleased = true
	case tcell.WheelUp:
		lp.scrollBy(-2)
	case tcell.WheelDown:
		lp.scrollBy(2)
	}
}

func (lp *ListPane) Display() {
	if screen.Screen == nil || lp.view.Width <= 0 || lp.view.Height <= 0 {
		return
	}

	headerStyle := lp.headerStyle()
	headerText := " " + lp.title
	if lp.filterable && lp.errorsOnly {
		headerText += fmt.Sprintf(" [Errors: %d]", len(lp.visible))
	} else {
		headerText += fmt.Sprintf(" (%d)", len(lp.visible))
	}
	if lp.filtering || lp.filterQuery != "" {
		headerText += fmt.Sprintf(" [/%s]", lp.filterQuery)
	}
	drawListClippedRow(lp.view.X, lp.view.Y, lp.view.Width, headerText, headerStyle)

	lh := lp.listHeight()
	if lh > 0 {
		if len(lp.visible) == 0 {
			emptyMsg := " (empty)"
			if lp.filterQuery != "" {
				emptyMsg = " (no matches)"
			} else if lp.errorsOnly {
				emptyMsg = " (no errors — press 'f' to show all)"
			}
			dimStyle := lookupListStyle("comment", config.DefStyle)
			drawListClippedRow(lp.view.X, lp.view.Y+1, lp.view.Width, emptyMsg, dimStyle)
			for row := 1; row < lh; row++ {
				drawListClippedRow(lp.view.X, lp.view.Y+1+row, lp.view.Width, "", config.DefStyle)
			}
		} else {
			for row := 0; row < lh; row++ {
				idx := lp.scrollTop + row
				screenY := lp.view.Y + 1 + row
				if idx >= len(lp.visible) {
					drawListClippedRow(lp.view.X, screenY, lp.view.Width, "", config.DefStyle)
					continue
				}
				it := lp.visible[idx]
				isSelected := idx == lp.selectedIdx
				text := lp.formatItemRow(it)
				st := lp.itemStyle(it, isSelected)
				drawListClippedRow(lp.view.X, screenY, lp.view.Width, text, st)
			}
		}
	}

	if lp.hasFooter() {
		footerY := lp.view.Y + lp.view.Height - 1
		footerText := " Enter:jump  /:filter  Esc/q:close"
		if lp.filtering {
			footerText = " Type to filter  ↑/↓:select  Enter:jump  Esc:clear"
		} else if lp.filterable {
			if lp.errorsOnly {
				footerText = " Enter:jump  f:show all  /:filter  Esc/q:close"
			} else {
				footerText = " Enter:jump  f:errors only  /:filter  Esc/q:close"
			}
		}
		drawListClippedRow(lp.view.X, footerY, lp.view.Width, footerText, headerStyle)
	}
}

func (lp *ListPane) formatItemRow(it ListItem) string {
	badge := it.Badge
	if badge == "" {
		switch it.Severity {
		case SeverityError:
			badge = "✖"
		case SeverityWarning:
			badge = "▲"
		default:
			badge = "•"
		}
	}
	loc := it.Secondary
	if loc == "" && it.FilePath != "" {
		loc = fmt.Sprintf("%s:%d:%d", filepath.Base(it.FilePath), it.Line+1, it.Col+1)
	}
	var sb strings.Builder
	sb.WriteByte(' ')
	sb.WriteString(badge)
	sb.WriteByte(' ')
	if loc != "" {
		sb.WriteString(loc)
		if it.Primary != "" {
			sb.WriteString("  ")
		}
	}
	sb.WriteString(it.Primary)
	return sb.String()
}

func (lp *ListPane) headerStyle() tcell.Style {
	if lp.active {
		return lookupListStyle("statusline", config.DefStyle.Reverse(true))
	}
	return lookupListStyle("statusline.inactive", lookupListStyle("statusline", config.DefStyle.Reverse(true)))
}

func (lp *ListPane) itemStyle(it ListItem, isSelected bool) tcell.Style {
	if isSelected && lp.active {
		return lookupListStyle("selection", config.DefStyle.Reverse(true))
	}

	base := config.DefStyle
	switch it.Severity {
	case SeverityError:
		base = lookupListStyle("gutter-error", lookupListStyle("error", config.DefStyle))
	case SeverityWarning:
		base = lookupListStyle("gutter-warning", lookupListStyle("todo", config.DefStyle))
	}

	if isSelected {
		if st, ok := config.Colorscheme["cursor-line"]; ok {
			fg, _, _ := st.Decompose()
			return base.Background(fg).Bold(true)
		}
		return base.Bold(true)
	}
	return base
}

func lookupListStyle(name string, fallback tcell.Style) tcell.Style {
	if config.Colorscheme != nil {
		if st, ok := config.Colorscheme[name]; ok {
			return st
		}
	}
	return fallback
}

func drawListClippedRow(x, y, width int, text string, style tcell.Style) {
	if width <= 0 || screen.Screen == nil {
		return
	}
	runes := []rune(text)
	totalW := 0
	for _, r := range runes {
		rw := runewidth.RuneWidth(r)
		if rw < 1 {
			rw = 1
		}
		totalW += rw
	}

	col := 0
	truncated := totalW > width
	limit := width
	if truncated && width >= 1 {
		limit = width - 1
	}

	for _, r := range runes {
		rw := runewidth.RuneWidth(r)
		if rw < 1 {
			rw = 1
		}
		if col+rw > limit {
			break
		}
		screen.SetContent(x+col, y, r, nil, style)
		col += rw
	}
	if truncated && col < width {
		screen.SetContent(x+col, y, '…', nil, style)
		col++
	}
	for col < width {
		screen.SetContent(x+col, y, ' ', nil, style)
		col++
	}
}
