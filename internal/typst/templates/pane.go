package templates

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/mattn/go-runewidth"
	"github.com/micro-editor/tcell/v2"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/display"
	"github.com/monicafaye/TypX/internal/screen"
)

const (
	// MinTerminalWidth is the minimum terminal width required to dock the Template Gallery.
	MinTerminalWidth = 50

	// DefaultTemplatesWidth is the default width in columns for TemplatePane.
	DefaultTemplatesWidth = 36

	// MinTemplatesWidth is the narrowest usable Template Gallery width when clamped.
	MinTemplatesWidth = 22

	// MinEditorWidth guarantees editor panes keep usable space alongside the gallery.
	MinEditorWidth = 20
)

// EffectiveWidth computes a safe docked sidebar width for a terminal of termWidth
// columns given configuredWidth (from typst.templatesWidth).
func EffectiveWidth(termWidth, configuredWidth int) int {
	if termWidth < MinTerminalWidth {
		return 0
	}
	w := configuredWidth
	if w <= 0 {
		w = DefaultTemplatesWidth
	}
	maxW := termWidth - MinEditorWidth
	if half := termWidth / 2; half < maxW {
		maxW = half
	}
	if maxW < MinTemplatesWidth {
		maxW = MinTemplatesWidth
	}
	if w < MinTemplatesWidth {
		w = MinTemplatesWidth
	}
	if w > maxW {
		w = maxW
	}
	return w
}

// IsBufferBlank reports whether b contains no non-whitespace content.
func IsBufferBlank(b *buffer.Buffer) bool {
	if b == nil {
		return true
	}
	n := b.LinesNum()
	for i := 0; i < n; i++ {
		for _, ch := range b.LineBytes(i) {
			if ch != ' ' && ch != '\t' && ch != '\r' && ch != '\n' {
				return false
			}
		}
	}
	return true
}

func isEditableDocBuffer(b *buffer.Buffer) bool {
	return b != nil && b.Type.Kind == buffer.BTDefault.Kind && !b.Type.Readonly && !b.Type.Scratch
}

func isTypstOrUnnamedEditable(b *buffer.Buffer) bool {
	if !isEditableDocBuffer(b) {
		return false
	}
	return b.IsTypst() || b.Path == ""
}

type rowKind int

const (
	rowHeader rowKind = iota
	rowTemplate
	rowUniverseAction
)

// GalleryRow represents a rendered row inside TemplatePane.
type GalleryRow struct {
	Kind        rowKind
	Label       string
	Template    Template
	NameMatches []int
}

// TemplatePane implements action.Pane, action.AuxiliaryPane, and display.Window
// as a docked Template Gallery sidebar on the left edge of a Tab.
type TemplatePane struct {
	view   *display.View
	id     uint64
	tab    *action.Tab
	target *action.BufPane
	mgr    *Manager

	active        bool
	mouseReleased bool

	allTemplates  []Template
	rows          []GalleryRow
	selectedIdx   int
	scrollTop     int
	filtering     bool
	filterQuery   string
	desiredWidth  int
	clampedByTerm bool

	livePreview          bool
	lastPreviewKey       string
	previewMu            sync.Mutex
	previewGen           uint64
	previewTimer         *time.Timer
	previewCancel        context.CancelFunc
	sourcePreviewBuf     *buffer.Buffer
	suppressClearOnFocus bool

	closed bool
}

var (
	_ action.Pane          = (*TemplatePane)(nil)
	_ action.AuxiliaryPane = (*TemplatePane)(nil)
	_ display.Window       = (*TemplatePane)(nil)
)

func newTemplatePane(tab *action.Tab, target *action.BufPane, mgr *Manager, desiredWidth int) *TemplatePane {
	return &TemplatePane{
		view:          &display.View{},
		tab:           tab,
		target:        target,
		mgr:           mgr,
		mouseReleased: true,
		selectedIdx:   -1,
		desiredWidth:  desiredWidth,
		livePreview:   true,
	}
}

func (tp *TemplatePane) ID() uint64            { return tp.id }
func (tp *TemplatePane) SetID(i uint64)        { tp.id = i }
func (tp *TemplatePane) Name() string          { return "Templates" }
func (tp *TemplatePane) SetTab(t *action.Tab)  { tp.tab = t }
func (tp *TemplatePane) Tab() *action.Tab      { return tp.tab }
func (tp *TemplatePane) IsAuxiliary() bool     { return true }
func (tp *TemplatePane) GetView() *display.View { return tp.view }

func (tp *TemplatePane) SetView(v *display.View) {
	if v != nil {
		tp.view = v
	}
}

func (tp *TemplatePane) LocFromVisual(vloc buffer.Loc) buffer.Loc { return vloc }

func (tp *TemplatePane) Relocate() bool {
	tp.ensureSelectionVisible()
	return true
}

func (tp *TemplatePane) Resize(w, h int) {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	tp.view.Width = w
	tp.view.Height = h
	tp.ensureSelectionVisible()
}

func (tp *TemplatePane) SetActive(b bool) {
	if tp.active == b {
		return
	}
	tp.active = b
	if b {
		if tp.selectedIdx < 0 || tp.selectedIdx >= len(tp.rows) || !isRowSelectable(tp.rows[tp.selectedIdx]) {
			tp.selectFirst()
		}
		tp.ensureSelectionVisible()
		if tp.livePreview {
			tp.schedulePreview(true)
		}
	} else {
		tp.filtering = false
		if !tp.suppressClearOnFocus {
			tp.cancelPendingPreview()
			tp.clearLivePreview()
		}
	}
}

func (tp *TemplatePane) IsActive() bool { return tp.active }

// IsLivePreview reports whether live visual or source preview is currently active.
func (tp *TemplatePane) IsLivePreview() bool {
	if tp.mgr != nil {
		if p := tp.mgr.getPreviewProvider(); p != nil && p.SupportsVisual() {
			return tp.livePreview
		}
	}
	return tp.hasOpenSourcePreview()
}

func (tp *TemplatePane) Clear() {
	if screen.Screen == nil || tp.view.Width <= 0 || tp.view.Height <= 0 {
		return
	}
	for y := 0; y < tp.view.Height; y++ {
		for x := 0; x < tp.view.Width; x++ {
			screen.SetContent(tp.view.X+x, tp.view.Y+y, ' ', nil, config.DefStyle)
		}
	}
}

func (tp *TemplatePane) Close() {
	already := tp.closed
	tp.closed = true
	tp.cancelPendingPreview()
	tp.clearLivePreview()
	tp.closeSourcePreview()
	if !already && tp.mgr != nil && tp.tab != nil {
		if cur := tp.mgr.panes[tp.tab]; cur == tp {
			delete(tp.mgr.panes, tp.tab)
		}
	}
}

// Rows returns a copy of currently displayed gallery rows.
func (tp *TemplatePane) Rows() []GalleryRow {
	out := make([]GalleryRow, len(tp.rows))
	copy(out, tp.rows)
	return out
}

// SelectedTemplate returns the currently highlighted template, if any.
func (tp *TemplatePane) SelectedTemplate() (Template, bool) {
	if tp.selectedIdx < 0 || tp.selectedIdx >= len(tp.rows) {
		return Template{}, false
	}
	r := tp.rows[tp.selectedIdx]
	if r.Kind != rowTemplate {
		return Template{}, false
	}
	return r.Template, true
}

// FilterQuery returns the active search/filter text.
func (tp *TemplatePane) FilterQuery() string { return tp.filterQuery }

// IsFiltering reports whether live search input is active.
func (tp *TemplatePane) IsFiltering() bool { return tp.filtering }

// SetSearchQuery updates the gallery search query and optionally focuses the
// inline search input for typing.
func (tp *TemplatePane) SetSearchQuery(query string, typing bool) {
	tp.filterQuery = query
	tp.filtering = typing
	tp.selectedIdx = -1
	tp.rebuildRows()
}

// Target returns the linked BufPane if still open on this tab.
func (tp *TemplatePane) Target() *action.BufPane { return tp.resolveTarget() }

func (tp *TemplatePane) hasSearchBar() bool {
	return tp.view.Height >= 4
}

func (tp *TemplatePane) hasPreviewFooter() bool {
	return tp.view.Height >= 8
}

func (tp *TemplatePane) listStartRow() int {
	if tp.hasSearchBar() {
		return 2
	}
	return 1
}

func (tp *TemplatePane) listHeight() int {
	reserved := tp.listStartRow()
	if tp.hasPreviewFooter() {
		reserved += 3 // divider + title + description
	}
	lh := tp.view.Height - reserved
	if lh < 0 {
		return 0
	}
	return lh
}

func (tp *TemplatePane) resolveTarget() *action.BufPane {
	if tp.tab == nil {
		return nil
	}
	if tp.target != nil {
		for _, p := range tp.tab.Panes {
			if p == tp.target {
				return tp.target
			}
		}
		tp.target = nil
	}
	for _, p := range tp.tab.Panes {
		if bp, ok := p.(*action.BufPane); ok && bp.Buf != nil {
			tp.target = bp
			return bp
		}
	}
	return nil
}

func (tp *TemplatePane) returnFocus() {
	if tp.tab == nil {
		return
	}
	target := tp.resolveTarget()
	if target != nil {
		for i, p := range tp.tab.Panes {
			if p == target {
				tp.tab.SetActive(i)
				return
			}
		}
	}
	for i, p := range tp.tab.Panes {
		if !action.IsAuxiliaryPane(p) {
			tp.tab.SetActive(i)
			return
		}
	}
}

// Refresh reloads offline templates and merges loaded Universe templates.
func (tp *TemplatePane) Refresh() {
	if tp.mgr == nil {
		return
	}
	target := tp.resolveTarget()
	tp.allTemplates = tp.mgr.allTemplates(target)
	tp.rebuildRows()
}

func (tp *TemplatePane) rebuildRows() {
	q := strings.TrimSpace(tp.filterQuery)
	var selectedID string
	var selectedWasAction bool
	if tp.selectedIdx >= 0 && tp.selectedIdx < len(tp.rows) {
		selectedID = tp.rows[tp.selectedIdx].Template.ID
		selectedWasAction = tp.rows[tp.selectedIdx].Kind == rowUniverseAction
	}

	sectionOrder := []string{
		"Project & Personal",
		"Local & Cached",
		"Typst Universe",
	}
	matched := SearchTemplates(tp.allTemplates, q)
	buckets := make(map[string][]SearchResult, len(sectionOrder))
	for _, res := range matched {
		sec := res.Template.Source.SectionTitle()
		buckets[sec] = append(buckets[sec], res)
	}

	tp.rows = tp.rows[:0]
	for _, sec := range sectionOrder {
		items := buckets[sec]
		if sec == "Typst Universe" {
			loaded := tp.mgr != nil && tp.mgr.isUniverseLoaded()
			loading := tp.mgr != nil && tp.mgr.isUniverseLoading()
			if len(items) == 0 && !loaded {
				tp.rows = append(tp.rows, GalleryRow{Kind: rowHeader, Label: sec})
				lbl := "[u] Load Typst Universe catalog"
				if loading {
					lbl = "Loading Typst Universe..."
				} else if q != "" {
					lbl = "[Enter] Load Universe to search"
				}
				tp.rows = append(tp.rows, GalleryRow{Kind: rowUniverseAction, Label: lbl})
				continue
			}
		}
		if len(items) == 0 {
			continue
		}
		tp.rows = append(tp.rows, GalleryRow{Kind: rowHeader, Label: sec})
		for _, res := range items {
			tp.rows = append(tp.rows, GalleryRow{
				Kind:        rowTemplate,
				Template:    res.Template,
				NameMatches: res.NameMatches,
			})
		}
	}

	// Restore selection cleanly.
	tp.selectedIdx = -1
	if selectedWasAction {
		for i, r := range tp.rows {
			if r.Kind == rowUniverseAction {
				tp.selectedIdx = i
				break
			}
		}
	} else if selectedID != "" {
		for i, r := range tp.rows {
			if r.Kind == rowTemplate && r.Template.ID == selectedID {
				tp.selectedIdx = i
				break
			}
		}
	}
	if tp.selectedIdx < 0 {
		tp.selectFirst()
	}
	tp.ensureSelectionVisible()
	if tp.livePreview && tp.active {
		tp.schedulePreview(false)
	}
}

func isRowSelectable(r GalleryRow) bool {
	return r.Kind == rowTemplate || r.Kind == rowUniverseAction
}

func (tp *TemplatePane) selectFirst() {
	for i, r := range tp.rows {
		if isRowSelectable(r) {
			tp.selectedIdx = i
			return
		}
	}
	tp.selectedIdx = -1
}

func (tp *TemplatePane) selectLast() {
	for i := len(tp.rows) - 1; i >= 0; i-- {
		if isRowSelectable(tp.rows[i]) {
			tp.selectedIdx = i
			return
		}
	}
	tp.selectedIdx = -1
}

func (tp *TemplatePane) moveSelection(delta int) {
	if len(tp.rows) == 0 || delta == 0 {
		return
	}
	step := 1
	count := delta
	if delta < 0 {
		step = -1
		count = -delta
	}
	idx := tp.selectedIdx
	if idx < 0 || idx >= len(tp.rows) {
		if step > 0 {
			tp.selectFirst()
		} else {
			tp.selectLast()
		}
		tp.ensureSelectionVisible()
		return
	}
	for moved := 0; moved < count; moved++ {
		next := idx + step
		for next >= 0 && next < len(tp.rows) && !isRowSelectable(tp.rows[next]) {
			next += step
		}
		if next < 0 || next >= len(tp.rows) {
			break
		}
		idx = next
	}
	tp.selectedIdx = idx
	tp.ensureSelectionVisible()
	if tp.livePreview && tp.active {
		tp.schedulePreview(false)
	}
}

func (tp *TemplatePane) ensureSelectionVisible() {
	lh := tp.listHeight()
	if lh <= 0 || len(tp.rows) == 0 {
		tp.scrollTop = 0
		return
	}
	if tp.selectedIdx >= 0 {
		// If selection sits right below a section header, scroll so the header stays visible when possible.
		if tp.selectedIdx < tp.scrollTop {
			tp.scrollTop = tp.selectedIdx
			if tp.scrollTop > 0 && tp.rows[tp.scrollTop-1].Kind == rowHeader {
				tp.scrollTop--
			}
		} else if tp.selectedIdx >= tp.scrollTop+lh {
			tp.scrollTop = tp.selectedIdx - lh + 1
		}
	}
	maxScroll := len(tp.rows) - lh
	if maxScroll < 0 {
		maxScroll = 0
	}
	if tp.scrollTop > maxScroll {
		tp.scrollTop = maxScroll
	}
	if tp.scrollTop < 0 {
		tp.scrollTop = 0
	}
}

func (tp *TemplatePane) scrollBy(delta int) {
	lh := tp.listHeight()
	if lh <= 0 || len(tp.rows) == 0 {
		return
	}
	maxScroll := len(tp.rows) - lh
	if maxScroll < 0 {
		maxScroll = 0
	}
	next := tp.scrollTop + delta
	if next < 0 {
		next = 0
	} else if next > maxScroll {
		next = maxScroll
	}
	tp.scrollTop = next
}

// HandleEvent processes keyboard and mouse input for TemplatePane.
func (tp *TemplatePane) HandleEvent(event tcell.Event) {
	switch e := event.(type) {
	case *tcell.EventKey:
		tp.handleKeyEvent(e)
	case *tcell.EventMouse:
		tp.handleMouseEvent(e)
	}
}

// HandleCommand delegates commands typed in the command bar while focused on TemplatePane.
func (tp *TemplatePane) HandleCommand(text string) {
	if tp.tab != nil {
		if cp := tp.tab.CurPane(); cp != nil {
			cp.HandleCommand(text)
		}
	}
}

func (tp *TemplatePane) handleKeyEvent(e *tcell.EventKey) {
	mod := e.Modifiers()
	key := e.Key()
	r := e.Rune()

	// Alt+Shift+N toggles the gallery closed even when focused.
	if key == tcell.KeyRune && (r == 'N' || (r == 'n' && mod&tcell.ModShift != 0)) && mod&tcell.ModAlt != 0 {
		if tp.mgr != nil {
			tp.mgr.CloseForTab(tp.tab)
		}
		return
	}
	if key == tcell.KeyRune && r == ',' && mod&tcell.ModAlt != 0 {
		if action.Tabs != nil && action.Tabs.Active() > 0 {
			action.Tabs.SetActive(action.Tabs.Active() - 1)
		}
		return
	}
	if key == tcell.KeyRune && r == '.' && mod&tcell.ModAlt != 0 {
		if action.Tabs != nil && action.Tabs.Active() < len(action.Tabs.List)-1 {
			action.Tabs.SetActive(action.Tabs.Active() + 1)
		}
		return
	}
	if key == tcell.KeyCtrlE {
		if action.InfoBar != nil && tp.tab != nil {
			tab := tp.tab
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
		if tp.tab != nil && len(tp.tab.Panes) > 1 {
			next := (tp.tab.ActiveIndex() + 1) % len(tp.tab.Panes)
			tp.tab.SetActive(next)
		}
		return
	}
	if key == tcell.KeyCtrlQ {
		if tp.mgr != nil {
			tp.mgr.CloseForTab(tp.tab)
		}
		return
	}
	if key == tcell.KeyCtrlF {
		tp.filtering = true
		return
	}

	if tp.filtering {
		tp.handleFilterKey(e)
		return
	}

	switch key {
	case tcell.KeyEscape, tcell.KeyTab:
		if tp.filterQuery != "" {
			tp.filterQuery = ""
			tp.rebuildRows()
			return
		}
		tp.returnFocus()
	case tcell.KeyEnter:
		tp.activateSelected()
	case tcell.KeyUp:
		tp.moveSelection(-1)
	case tcell.KeyDown:
		tp.moveSelection(1)
	case tcell.KeyHome:
		tp.selectFirst()
		tp.ensureSelectionVisible()
		if tp.livePreview && tp.active {
			tp.schedulePreview(false)
		}
	case tcell.KeyEnd:
		tp.selectLast()
		tp.ensureSelectionVisible()
		if tp.livePreview && tp.active {
			tp.schedulePreview(false)
		}
	case tcell.KeyPgUp:
		step := tp.listHeight()
		if step < 1 {
			step = 1
		}
		tp.moveSelection(-step)
	case tcell.KeyPgDn:
		step := tp.listHeight()
		if step < 1 {
			step = 1
		}
		tp.moveSelection(step)
	case tcell.KeyRune:
		if mod&(tcell.ModAlt|tcell.ModCtrl) != 0 {
			return
		}
		switch r {
		case 'k':
			tp.moveSelection(-1)
		case 'j':
			tp.moveSelection(1)
		case 'g':
			tp.selectFirst()
			tp.ensureSelectionVisible()
			if tp.livePreview && tp.active {
				tp.schedulePreview(false)
			}
		case 'G':
			tp.selectLast()
			tp.ensureSelectionVisible()
			if tp.livePreview && tp.active {
				tp.schedulePreview(false)
			}
		case '/':
			tp.filtering = true
		case 'u':
			if tp.mgr != nil {
				tp.mgr.LoadUniverseAsync(tp.mgr.isUniverseLoaded())
			}
		case 'v':
			tp.toggleLivePreview()
		case 'p':
			if tmpl, ok := tp.SelectedTemplate(); ok && tp.mgr != nil {
				tp.mgr.InsertPreamble(tp.resolveTarget(), tp.tab, tmpl)
			}
		case 'n':
			if tmpl, ok := tp.SelectedTemplate(); ok && tp.mgr != nil {
				tp.mgr.PromptScaffoldDir(tp.resolveTarget(), tp.tab, tmpl)
			}
		case 'q':
			if tp.mgr != nil {
				tp.mgr.CloseForTab(tp.tab)
			}
		}
	}
}

func (tp *TemplatePane) handleFilterKey(e *tcell.EventKey) {
	switch e.Key() {
	case tcell.KeyEscape:
		tp.filtering = false
		tp.filterQuery = ""
		tp.rebuildRows()
	case tcell.KeyTab:
		// Exit search typing while keeping the filtered results active for browsing/actions.
		tp.filtering = false
	case tcell.KeyEnter:
		tp.filtering = false
		if tp.selectedIdx >= 0 && tp.selectedIdx < len(tp.rows) {
			if tp.rows[tp.selectedIdx].Kind == rowUniverseAction {
				// Keep the active search query so Universe results filter as soon as they load.
				tp.activateSelected()
				return
			}
			tp.activateSelected()
			tp.filterQuery = ""
			tp.rebuildRows()
		}
	case tcell.KeyCtrlU:
		tp.filterQuery = ""
		tp.selectedIdx = -1
		tp.rebuildRows()
	case tcell.KeyUp:
		tp.moveSelection(-1)
	case tcell.KeyDown:
		tp.moveSelection(1)
	case tcell.KeyHome:
		tp.selectFirst()
		tp.ensureSelectionVisible()
		if tp.livePreview && tp.active {
			tp.schedulePreview(false)
		}
	case tcell.KeyEnd:
		tp.selectLast()
		tp.ensureSelectionVisible()
		if tp.livePreview && tp.active {
			tp.schedulePreview(false)
		}
	case tcell.KeyPgUp:
		step := tp.listHeight()
		if step < 1 {
			step = 1
		}
		tp.moveSelection(-step)
	case tcell.KeyPgDn:
		step := tp.listHeight()
		if step < 1 {
			step = 1
		}
		tp.moveSelection(step)
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if tp.filterQuery == "" {
			tp.filtering = false
			return
		}
		if e.Modifiers()&tcell.ModAlt != 0 {
			tp.filterQuery = deletePrevWord(tp.filterQuery)
		} else {
			runes := []rune(tp.filterQuery)
			tp.filterQuery = string(runes[:len(runes)-1])
		}
		tp.selectedIdx = -1
		tp.rebuildRows()
	case tcell.KeyRune:
		if e.Modifiers()&(tcell.ModAlt|tcell.ModCtrl) != 0 {
			return
		}
		tp.filterQuery += string(e.Rune())
		tp.selectedIdx = -1
		tp.rebuildRows()
	}
}

func deletePrevWord(s string) string {
	runes := []rune(s)
	i := len(runes)
	for i > 0 && unicode.IsSpace(runes[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(runes[i-1]) {
		i--
	}
	return string(runes[:i])
}

func (tp *TemplatePane) handleMouseEvent(e *tcell.EventMouse) {
	mx, my := e.Position()
	btn := e.Buttons()

	switch btn {
	case tcell.Button1:
		wasReleased := tp.mouseReleased
		tp.mouseReleased = false
		if !wasReleased {
			return
		}
		if mx < tp.view.X || mx >= tp.view.X+tp.view.Width {
			return
		}
		localY := my - tp.view.Y
		if tp.hasSearchBar() && localY == 1 {
			tp.filtering = true
			return
		}
		startRow := tp.listStartRow()
		lh := tp.listHeight()
		if localY >= startRow && localY < startRow+lh {
			idx := tp.scrollTop + (localY - startRow)
			if idx >= 0 && idx < len(tp.rows) && isRowSelectable(tp.rows[idx]) {
				tp.filtering = false
				already := tp.selectedIdx == idx
				tp.selectedIdx = idx
				if already || tp.rows[idx].Kind == rowUniverseAction {
					tp.activateSelected()
				} else if tp.livePreview && tp.active {
					tp.schedulePreview(false)
				}
			}
		}
	case tcell.ButtonNone:
		tp.mouseReleased = true
	case tcell.WheelUp:
		tp.scrollBy(-2)
	case tcell.WheelDown:
		tp.scrollBy(2)
	}
}

func (tp *TemplatePane) activateSelected() {
	if tp.selectedIdx < 0 || tp.selectedIdx >= len(tp.rows) {
		return
	}
	row := tp.rows[tp.selectedIdx]
	switch row.Kind {
	case rowUniverseAction:
		if tp.mgr != nil {
			tp.mgr.LoadUniverseAsync(false)
		}
	case rowTemplate:
		if tp.mgr != nil {
			tp.mgr.ApplyTemplate(tp.resolveTarget(), tp.tab, row.Template)
		}
	}
}

// Display renders the Template Gallery onto the terminal screen.
func (tp *TemplatePane) Display() {
	if screen.Screen == nil || tp.view.Width <= 0 || tp.view.Height <= 0 {
		return
	}

	headerStyle := tp.headerStyle()
	count := tp.templateCount()
	total := len(tp.allTemplates)
	hasQuery := strings.TrimSpace(tp.filterQuery) != ""
	headerText := " Templates"
	switch {
	case !tp.hasSearchBar() && tp.filtering:
		headerText = " /" + tp.filterQuery + "_"
	case !tp.hasSearchBar() && hasQuery:
		headerText = fmt.Sprintf(" /%s (%d)", tp.filterQuery, count)
	case tp.mgr != nil && tp.mgr.isUniverseLoading():
		headerText = " Templates (loading…)"
	case hasQuery:
		headerText = fmt.Sprintf(" Templates (%d/%d)", count, total)
	case count > 0:
		headerText = fmt.Sprintf(" Templates (%d)", count)
	}
	drawClippedRow(tp.view.X, tp.view.Y, tp.view.Width, headerText, headerStyle)

	if tp.hasSearchBar() {
		tp.drawSearchBar(tp.view.X, tp.view.Y+1, tp.view.Width)
	}

	startRow := tp.listStartRow()
	lh := tp.listHeight()
	if lh > 0 {
		if len(tp.rows) == 0 {
			emptyMsg := " (no templates)"
			if hasQuery {
				emptyMsg = " (no matches)"
			}
			dimStyle := lookupStyle("comment", config.DefStyle)
			drawClippedRow(tp.view.X, tp.view.Y+startRow, tp.view.Width, emptyMsg, dimStyle)
			for row := 1; row < lh; row++ {
				drawClippedRow(tp.view.X, tp.view.Y+startRow+row, tp.view.Width, "", config.DefStyle)
			}
		} else {
			for row := 0; row < lh; row++ {
				idx := tp.scrollTop + row
				screenY := tp.view.Y + startRow + row
				if idx >= len(tp.rows) {
					drawClippedRow(tp.view.X, screenY, tp.view.Width, "", config.DefStyle)
					continue
				}
				gr := tp.rows[idx]
				isSelected := idx == tp.selectedIdx
				label := formatGalleryRow(gr, isSelected)
				st := tp.rowStyle(gr, isSelected)
				if gr.Kind == rowTemplate && len(gr.NameMatches) > 0 {
					mask := buildNameMatchMask(label, 2, gr.NameMatches)
					matchSt := tp.matchHighlightStyle(st, isSelected)
					drawHighlightedRow(tp.view.X, screenY, tp.view.Width, label, st, matchSt, mask)
				} else {
					drawClippedRow(tp.view.X, screenY, tp.view.Width, label, st)
				}
			}
		}
	}

	if tp.hasPreviewFooter() {
		divY := tp.view.Y + tp.view.Height - 3
		titleY := divY + 1
		descY := divY + 2

		divStyle := lookupStyle("comment", config.DefStyle)
		drawClippedRow(tp.view.X, divY, tp.view.Width, strings.Repeat("─", tp.view.Width), divStyle)

		line1, line2 := tp.previewFooterLines()
		drawClippedRow(tp.view.X, titleY, tp.view.Width, " "+line1, lookupStyle("identifier.class", config.DefStyle.Bold(true)))
		drawClippedRow(tp.view.X, descY, tp.view.Width, " "+line2, lookupStyle("comment", config.DefStyle))
	}
}

func (tp *TemplatePane) drawSearchBar(x, y, width int) {
	if width <= 0 || screen.Screen == nil {
		return
	}
	if tp.filtering {
		prefix := " / "
		cursor := "_"
		avail := width - runewidth.StringWidth(prefix) - runewidth.StringWidth(cursor)
		qDisplay := clipLeftToWidth(tp.filterQuery, avail)
		st := applyCursorLineHighlight(config.DefStyle)
		drawClippedRow(x, y, width, prefix+qDisplay+cursor, st)
		return
	}
	if tp.filterQuery != "" {
		st := lookupStyle("identifier.class", config.DefStyle)
		drawClippedRow(x, y, width, " / "+tp.filterQuery, st)
		return
	}
	placeholder := " / Search templates..."
	if width < 24 {
		placeholder = " / Search..."
	}
	st := lookupStyle("comment", config.DefStyle)
	drawClippedRow(x, y, width, placeholder, st)
}

func clipLeftToWidth(text string, maxW int) string {
	if maxW <= 0 || text == "" {
		return ""
	}
	if runewidth.StringWidth(text) <= maxW {
		return text
	}
	if maxW == 1 {
		return "…"
	}
	runes := []rune(text)
	w := 1 // reserve 1 col for leading ellipsis
	start := len(runes)
	for start > 0 {
		rw := runewidth.RuneWidth(runes[start-1])
		if rw < 1 {
			rw = 1
		}
		if w+rw > maxW {
			break
		}
		w += rw
		start--
	}
	return "…" + string(runes[start:])
}

func buildNameMatchMask(label string, offset int, nameMatches []int) []bool {
	n := len([]rune(label))
	if n == 0 || len(nameMatches) == 0 {
		return nil
	}
	mask := make([]bool, n)
	for _, idx := range nameMatches {
		pos := offset + idx
		if pos >= 0 && pos < n {
			mask[pos] = true
		}
	}
	return mask
}

func (tp *TemplatePane) matchHighlightStyle(rowStyle tcell.Style, isSelected bool) tcell.Style {
	_, bg, _ := rowStyle.Decompose()
	accentFg, _, _ := lookupStyle("constant.specialChar", lookupStyle("constant", rowStyle)).Decompose()
	st := rowStyle.Foreground(accentFg).Bold(true)
	if isSelected {
		st = st.Background(bg).Underline(true)
	}
	return st
}

func (tp *TemplatePane) templateCount() int {
	n := 0
	for _, r := range tp.rows {
		if r.Kind == rowTemplate {
			n++
		}
	}
	return n
}

func (tp *TemplatePane) previewFooterLines() (string, string) {
	if tp.selectedIdx < 0 || tp.selectedIdx >= len(tp.rows) {
		if strings.TrimSpace(tp.filterQuery) != "" {
			return "No matching templates", "Press 'Esc' to clear search or 'u' for Universe"
		}
		return "No template selected", "Press '/' or Ctrl-F to search, 'u' for Universe"
	}
	row := tp.rows[tp.selectedIdx]
	if row.Kind == rowUniverseAction {
		if tp.mgr != nil && tp.mgr.isUniverseLoading() {
			return "Typst Universe (@preview)", "Fetching online template catalog..."
		}
		if strings.TrimSpace(tp.filterQuery) != "" {
			return "Typst Universe (@preview)", "Press Enter or 'u' to load and search online templates"
		}
		return "Typst Universe (@preview)", "Press 'u' or Enter to load community templates"
	}
	t := row.Template
	badge := t.Source.String()
	if t.Version != "" {
		badge += " v" + t.Version
	}
	line1 := fmt.Sprintf("%s [%s]", t.DisplayTitle(), badge)
	desc := strings.TrimSpace(t.Description)
	if desc == "" {
		if t.IsMultiFile {
			desc = "Multi-file template (" + t.Entrypoint + ")"
		} else {
			desc = "Single-file Typst template"
		}
	}
	return line1, desc
}

func formatGalleryRow(gr GalleryRow, isSelected bool) string {
	switch gr.Kind {
	case rowHeader:
		return " " + gr.Label
	case rowUniverseAction:
		prefix := "  "
		if isSelected {
			prefix = "▸ "
		}
		return prefix + gr.Label
	default:
		prefix := "  "
		if isSelected {
			prefix = "▸ "
		}
		t := gr.Template
		tag := ""
		if t.Category != "" && !t.IsPackage() {
			tag = " · " + t.Category
		} else if t.Version != "" {
			tag = " " + t.Version
		}
		if t.IsMultiFile && t.Source == SourcePersonal {
			tag += " [dir]"
		}
		return prefix + t.Name + tag
	}
}

func (tp *TemplatePane) headerStyle() tcell.Style {
	if tp.active {
		return lookupStyle("statusline", config.DefStyle.Reverse(true))
	}
	return lookupStyle("statusline.inactive", lookupStyle("statusline", config.DefStyle.Reverse(true)))
}

func (tp *TemplatePane) rowStyle(gr GalleryRow, isSelected bool) tcell.Style {
	switch gr.Kind {
	case rowHeader:
		return lookupStyle("comment", config.DefStyle).Bold(true)
	case rowUniverseAction:
		base := lookupStyle("type", config.DefStyle)
		if isSelected {
			return applyCursorLineHighlight(base)
		}
		return base
	default:
		var base tcell.Style
		switch gr.Template.Source {
		case SourceProject, SourcePersonal:
			base = lookupStyle("identifier.class", config.DefStyle)
		default:
			base = lookupStyle("type", config.DefStyle)
		}
		if isSelected {
			return applyCursorLineHighlight(base)
		}
		return base
	}
}

func applyCursorLineHighlight(base tcell.Style) tcell.Style {
	if config.Colorscheme != nil {
		if st, ok := config.Colorscheme["cursor-line"]; ok {
			fg, _, _ := st.Decompose()
			return base.Background(fg).Bold(true)
		}
	}
	return lookupStyle("selection", base.Reverse(true))
}

func lookupStyle(name string, fallback tcell.Style) tcell.Style {
	if config.Colorscheme != nil {
		if st, ok := config.Colorscheme[name]; ok {
			return st
		}
	}
	return fallback
}

func drawClippedRow(x, y, width int, text string, style tcell.Style) {
	drawHighlightedRow(x, y, width, text, style, style, nil)
}

func drawHighlightedRow(x, y, width int, text string, baseStyle, matchStyle tcell.Style, matchMask []bool) {
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

	for i, r := range runes {
		rw := runewidth.RuneWidth(r)
		if rw < 1 {
			rw = 1
		}
		if col+rw > limit {
			break
		}
		st := baseStyle
		if i < len(matchMask) && matchMask[i] {
			st = matchStyle
		}
		screen.SetContent(x+col, y, r, nil, st)
		col += rw
	}
	if truncated && col < width {
		screen.SetContent(x+col, y, '…', nil, baseStyle)
		col++
	}
	for col < width {
		screen.SetContent(x+col, y, ' ', nil, baseStyle)
		col++
	}
}

// Manager coordinates per-tab TemplatePane instances, optional LSP template
// backend, Typst Universe loading, and resize/focus hooks.
type Manager struct {
	buffer.NopObserver

	panes      map[*action.Tab]*TemplatePane
	autoHidden map[*action.Tab]bool

	mu              sync.RWMutex
	backend         ExternalBackend
	rootResolver    func(string) string
	previewProvider PreviewProvider
	previewCacheDir string
	previewWG       sync.WaitGroup
	universeClient  *UniverseClient
	universeItems   []Template
	universeLoaded  bool
	universeLoading bool
	universeGen     uint64
	universeCancel  context.CancelFunc
}

func (m *Manager) waitPendingPreviews() {
	m.previewWG.Wait()
}

func newManager() *Manager {
	return &Manager{
		panes:      make(map[*action.Tab]*TemplatePane),
		autoHidden: make(map[*action.Tab]bool),
	}
}

var (
	defaultManager = newManager()
	initOnce       sync.Once
)

// SetExternalBackend wires tinymist template RPCs into templates without importing internal/lsp.
func SetExternalBackend(b ExternalBackend) {
	defaultManager.setExternalBackend(b)
}

// SetRootResolver wires Typst project-root discovery into templates without importing internal/lsp.
func SetRootResolver(fn func(string) string) {
	defaultManager.setRootResolver(fn)
}

// SetUniverseClient overrides the Universe HTTP/cache client (intended for tests).
func SetUniverseClient(c *UniverseClient) {
	defaultManager.setUniverseClient(c)
}

// SetPreviewProvider wires visual preview rendering into templates without importing preview.
func SetPreviewProvider(p PreviewProvider) {
	defaultManager.setPreviewProvider(p)
}

// SetPreviewCacheDir overrides the template preview cache directory (intended for tests).
func SetPreviewCacheDir(dir string) {
	defaultManager.setPreviewCacheDir(dir)
}

func (m *Manager) setPreviewProvider(p PreviewProvider) {
	m.mu.Lock()
	m.previewProvider = p
	m.mu.Unlock()
}

func (m *Manager) getPreviewProvider() PreviewProvider {
	m.mu.RLock()
	p := m.previewProvider
	m.mu.RUnlock()
	return p
}

func (m *Manager) setPreviewCacheDir(dir string) {
	m.mu.Lock()
	m.previewCacheDir = dir
	m.mu.Unlock()
}

func (m *Manager) getPreviewCacheDir() string {
	m.mu.RLock()
	dir := m.previewCacheDir
	m.mu.RUnlock()
	if dir != "" {
		return dir
	}
	return DefaultPreviewCacheDir()
}

func (m *Manager) setExternalBackend(b ExternalBackend) {
	m.mu.Lock()
	m.backend = b
	m.mu.Unlock()
}

func (m *Manager) getExternalBackend() ExternalBackend {
	m.mu.RLock()
	b := m.backend
	m.mu.RUnlock()
	return b
}

func (m *Manager) setRootResolver(fn func(string) string) {
	m.mu.Lock()
	m.rootResolver = fn
	m.mu.Unlock()
}

func (m *Manager) getRootResolver() func(string) string {
	m.mu.RLock()
	fn := m.rootResolver
	m.mu.RUnlock()
	return fn
}

func (m *Manager) setUniverseClient(c *UniverseClient) {
	m.mu.Lock()
	m.universeClient = c
	m.universeItems = nil
	m.universeLoaded = false
	m.mu.Unlock()
}

func (m *Manager) getUniverseClient() *UniverseClient {
	m.mu.RLock()
	c := m.universeClient
	m.mu.RUnlock()
	if c != nil {
		return c
	}
	return NewUniverseClient()
}

func (m *Manager) isUniverseLoaded() bool {
	m.mu.RLock()
	loaded := m.universeLoaded
	m.mu.RUnlock()
	return loaded
}

func (m *Manager) isUniverseLoading() bool {
	m.mu.RLock()
	loading := m.universeLoading
	m.mu.RUnlock()
	return loading
}

func (m *Manager) preloadCachedUniverse() {
	m.mu.RLock()
	loaded := m.universeLoaded
	m.mu.RUnlock()
	if loaded {
		return
	}
	items, _, ok := m.getUniverseClient().LoadCachedUniverse()
	if !ok || len(items) == 0 {
		return
	}
	m.mu.Lock()
	if !m.universeLoaded {
		m.universeItems = items
		m.universeLoaded = true
	}
	m.mu.Unlock()
}

func (m *Manager) allTemplates(bp *action.BufPane) []Template {
	projectRoot := m.resolveProjectRoot(bp)
	offline := DiscoverOffline(DiscoverOptions{
		ProjectRoot: projectRoot,
		ConfigDir:   config.ConfigDir,
	})

	m.mu.RLock()
	uItems := make([]Template, len(m.universeItems))
	copy(uItems, m.universeItems)
	m.mu.RUnlock()

	if len(uItems) == 0 {
		return offline
	}
	seenCached := make(map[string]bool)
	for _, t := range offline {
		if t.Source == SourceCachedPkg {
			seenCached[strings.ToLower(t.Name)] = true
		}
	}
	out := make([]Template, 0, len(offline)+len(uItems))
	out = append(out, offline...)
	for _, ut := range uItems {
		if seenCached[strings.ToLower(ut.Name)] {
			continue
		}
		out = append(out, ut)
	}
	return out
}

func (m *Manager) resolveProjectRoot(bp *action.BufPane) string {
	if bp != nil && bp.Buf != nil && bp.Buf.Path != "" && !bp.Buf.Type.Scratch && bp.Buf.AbsPath != "" {
		if rr := m.getRootResolver(); rr != nil {
			if r := rr(bp.Buf.AbsPath); r != "" {
				return r
			}
		}
		return filepath.Dir(bp.Buf.AbsPath)
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// LoadUniverseAsync fetches the Typst Universe catalog off the main loop and
// refreshes open TemplatePane instances via action.EnqueueUI.
func (m *Manager) LoadUniverseAsync(forceRefresh bool) {
	m.mu.Lock()
	if m.universeCancel != nil {
		m.universeCancel()
		m.universeCancel = nil
	}
	m.universeGen++
	gen := m.universeGen
	m.universeLoading = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	m.universeCancel = cancel
	client := m.universeClient
	m.mu.Unlock()
	if client == nil {
		client = NewUniverseClient()
	}

	m.refreshAllPanes()

	go func() {
		items, err := client.FetchUniverse(ctx, forceRefresh)
		canceled := ctx.Err() == context.Canceled
		cancel()

		if canceled {
			return
		}
		action.EnqueueUI(func() {
			m.mu.Lock()
			if m.universeGen != gen {
				m.mu.Unlock()
				return
			}
			m.universeLoading = false
			if len(items) > 0 {
				m.universeItems = items
				m.universeLoaded = true
			}
			m.mu.Unlock()

			m.refreshAllPanes()
			if err != nil {
				if len(items) > 0 {
					notifyError(fmt.Sprintf("Universe offline (showing %d cached): %v", len(items), err))
				} else {
					notifyError("Failed to load Typst Universe: " + err.Error())
				}
				return
			}
			notifyMessage(fmt.Sprintf("Loaded %d Typst Universe templates", len(items)))
		})
	}()
}

func (m *Manager) refreshAllPanes() {
	m.pruneStaleTabs()
	for tab, tp := range m.panes {
		if m.paneForTab(tab) != nil {
			tp.Refresh()
		}
	}
}

// PaneForTab returns the open TemplatePane on tab, or nil.
func PaneForTab(tab *action.Tab) *TemplatePane {
	return defaultManager.paneForTab(tab)
}

func (m *Manager) paneForTab(tab *action.Tab) *TemplatePane {
	if tab == nil {
		return nil
	}
	tp := m.panes[tab]
	if tp == nil {
		return nil
	}
	for _, p := range tab.Panes {
		if p == tp {
			return tp
		}
	}
	tp.Close()
	delete(m.panes, tab)
	return nil
}

// Toggle opens or closes the Template Gallery on bp's tab.
func Toggle(bp *action.BufPane) bool {
	return defaultManager.Toggle(bp)
}

// Open ensures the Template Gallery sidebar is open on bp's tab.
func Open(bp *action.BufPane, focus bool) (*TemplatePane, bool) {
	return defaultManager.Open(bp, focus)
}

// CloseForTab closes the Template Gallery sidebar on tab if open.
func CloseForTab(tab *action.Tab) bool {
	return defaultManager.CloseForTab(tab)
}

func (m *Manager) Toggle(bp *action.BufPane) bool {
	if bp == nil || bp.Tab() == nil {
		return false
	}
	tab := bp.Tab()
	if m.paneForTab(tab) != nil {
		return m.CloseForTab(tab)
	}
	_, ok := m.Open(bp, true)
	return ok
}

func (m *Manager) Open(bp *action.BufPane, focus bool) (*TemplatePane, bool) {
	if bp == nil || bp.Buf == nil || bp.Tab() == nil {
		return nil, false
	}
	m.pruneStaleTabs()
	m.preloadCachedUniverse()
	tab := bp.Tab()

	if existing := m.paneForTab(tab); existing != nil {
		existing.target = bp
		existing.Refresh()
		if focus {
			wasActive := existing.active
			for i, p := range tab.Panes {
				if p == existing {
					tab.SetActive(i)
					break
				}
			}
			if wasActive && existing.livePreview {
				existing.schedulePreview(true)
			}
		}
		return existing, true
	}

	termW := tabTerminalWidth(tab)
	if termW < MinTerminalWidth {
		delete(m.autoHidden, tab)
		notifyError(fmt.Sprintf("Template Gallery requires terminal width >= %d columns (current: %d)", MinTerminalWidth, termW))
		return nil, false
	}

	cfgW := readGlobalInt("typst.templatesWidth", DefaultTemplatesWidth)
	effW := EffectiveWidth(termW, cfgW)
	if effW <= 0 {
		notifyError(fmt.Sprintf("Template Gallery requires terminal width >= %d columns", MinTerminalWidth))
		return nil, false
	}

	dockID := tab.Node.DockLeft(effW)
	if dockID == 0 {
		notifyError("Unable to dock Template Gallery in current split layout")
		return nil, false
	}

	delete(m.autoHidden, tab)
	tp := newTemplatePane(tab, bp, m, cfgW)
	tp.SetID(dockID)
	tp.clampedByTerm = effW < cfgW
	m.panes[tab] = tp

	tab.AddPane(tp, 0)
	tab.Resize()
	tp.Refresh()

	if focus {
		tab.SetActive(0)
	}
	return tp, true
}

func (m *Manager) CloseForTab(tab *action.Tab) bool {
	if tab == nil {
		return false
	}
	delete(m.autoHidden, tab)
	return m.detachFromTab(tab)
}

func (m *Manager) detachFromTab(tab *action.Tab) bool {
	tp := m.paneForTab(tab)
	if tp == nil {
		return false
	}
	wasFocused := tab.ActivePane() == tp
	target := tp.resolveTarget()
	id := tp.ID()

	tp.Close()
	if n := tab.GetNode(id); n != nil {
		n.Unsplit()
	}
	for i, p := range tab.Panes {
		if p == tp {
			tab.RemovePane(i)
			break
		}
	}
	tab.Resize()

	if len(tab.Panes) > 0 {
		if wasFocused {
			focused := false
			if target != nil {
				for i, p := range tab.Panes {
					if p == target {
						tab.SetActive(i)
						focused = true
						break
					}
				}
			}
			if !focused {
				tab.SetActive(tab.ActiveIndex())
			}
		} else {
			tab.SetActive(tab.ActiveIndex())
		}
	}
	return true
}

func (m *Manager) pruneStaleTabs() {
	if action.Tabs == nil {
		return
	}
	live := make(map[*action.Tab]bool, len(action.Tabs.List))
	for _, t := range action.Tabs.List {
		live[t] = true
	}
	for t, tp := range m.panes {
		if !live[t] {
			tp.Close()
			delete(m.panes, t)
		}
	}
	for t := range m.autoHidden {
		if !live[t] {
			delete(m.autoHidden, t)
		}
	}
}

func (m *Manager) onPreResize(w, h int) {
	m.pruneStaleTabs()
	if w >= MinTerminalWidth {
		return
	}
	var toHide []*action.Tab
	for t := range m.panes {
		toHide = append(toHide, t)
	}
	for _, t := range toHide {
		if m.paneForTab(t) != nil {
			m.autoHidden[t] = true
			m.detachFromTab(t)
		}
	}
}

func (m *Manager) onPostResize(w, h int) {
	m.pruneStaleTabs()
	if w < MinTerminalWidth {
		return
	}
	if len(m.autoHidden) > 0 {
		var toRestore []*action.Tab
		for t := range m.autoHidden {
			toRestore = append(toRestore, t)
		}
		for _, t := range toRestore {
			delete(m.autoHidden, t)
			if m.paneForTab(t) != nil {
				continue
			}
			if bp := findEditorPaneInTab(t); bp != nil {
				m.Open(bp, false)
			}
		}
	}

	cfgW := readGlobalInt("typst.templatesWidth", DefaultTemplatesWidth)
	effW := EffectiveWidth(w, cfgW)
	if effW <= 0 {
		return
	}
	for t, tp := range m.panes {
		if m.paneForTab(t) == nil {
			continue
		}
		n := t.GetNode(tp.ID())
		if n == nil {
			continue
		}
		if n.W > effW {
			tp.clampedByTerm = true
			n.ResizeSplit(effW)
			t.Resize()
		} else if tp.clampedByTerm && n.W < effW {
			targetW := effW
			if tp.desiredWidth > 0 && tp.desiredWidth < targetW {
				targetW = tp.desiredWidth
			}
			if targetW > n.W {
				n.ResizeSplit(targetW)
				t.Resize()
			}
			tp.clampedByTerm = targetW < tp.desiredWidth
		}
	}
}

func (m *Manager) onTabSwitch(tab *action.Tab) {
	m.pruneStaleTabs()
	if tp := m.paneForTab(tab); tp != nil {
		tp.Refresh()
	}
}

func (m *Manager) onPaneFocus(tab *action.Tab, pane action.Pane) {
	tp := m.paneForTab(tab)
	if tp == nil || pane == nil || pane == tp {
		return
	}
	bp, ok := pane.(*action.BufPane)
	if !ok || bp.Buf == nil {
		return
	}
	tp.target = bp
}

func (m *Manager) OnBufferClose(b *buffer.Buffer) {
	if b == nil {
		return
	}
	var affected []*action.Tab
	for tab, tp := range m.panes {
		if tp.target != nil && tp.target.Buf == b {
			affected = append(affected, tab)
		}
	}
	for _, tab := range affected {
		tp := m.paneForTab(tab)
		if tp == nil {
			continue
		}
		var replacement *action.BufPane
		for _, p := range tab.Panes {
			bp, ok := p.(*action.BufPane)
			if !ok || bp.Buf == nil || bp.Buf == b {
				continue
			}
			replacement = bp
			break
		}
		if replacement != nil {
			tp.target = replacement
		} else {
			m.CloseForTab(tab)
		}
	}
}

// ApplyTemplate inserts a single-file template into an empty Typst or unnamed
// buffer (or prompts insert-at-top vs. new-tab when non-empty), opens a new
// .typ tab when the active buffer is a non-Typst or read-only file, or
// scaffolds multi-file/package templates.
func (m *Manager) ApplyTemplate(bp *action.BufPane, tab *action.Tab, t Template) {
	if t.IsMultiFile || t.IsPackage() {
		m.PromptScaffoldDir(bp, tab, t)
		return
	}
	if bp == nil || !isTypstOrUnnamedEditable(bp.Buf) {
		m.openTemplateInNewTab(tab, bp, t, false)
		return
	}
	if IsBufferBlank(bp.Buf) {
		m.insertTemplateIntoBuffer(bp, tab, t, false)
		return
	}
	if action.InfoBar == nil {
		return
	}
	action.InfoBar.Prompt("Buffer non-empty ([i] insert at top, [t] new tab): ", "", "TemplateAction", nil, func(resp string, canceled bool) {
		if canceled {
			return
		}
		choice := strings.ToLower(strings.TrimSpace(resp))
		curBP := resolveActivePane(tab, bp)
		switch choice {
		case "i", "insert", "top":
			m.insertTemplateIntoBuffer(curBP, tab, t, false)
		case "t", "tab", "new":
			m.openTemplateInNewTab(tab, curBP, t, false)
		case "p", "preamble":
			m.insertTemplateIntoBuffer(curBP, tab, t, true)
		}
	})
}

func (m *Manager) insertTemplateIntoBuffer(bp *action.BufPane, tab *action.Tab, t Template, preambleOnly bool) {
	if bp == nil || !isTypstOrUnnamedEditable(bp.Buf) {
		m.openTemplateInNewTab(tab, bp, t, preambleOnly)
		return
	}
	var vars TemplateVars
	if bp.Buf.Path != "" {
		vars.Title = DeriveTitle(bp.Buf.Path)
	}
	rendered, err := RenderEntry(t, vars)
	if err != nil {
		notifyError("Failed to read template: " + err.Error())
		return
	}
	if preambleOnly {
		if pre := ExtractPreamble(rendered); pre != "" {
			rendered = pre
		}
	}
	if !strings.HasSuffix(rendered, "\n") {
		rendered += "\n"
	}

	if IsBufferBlank(bp.Buf) {
		bp.Buf.Replace(bp.Buf.Start(), bp.Buf.End(), rendered)
	} else {
		bp.Buf.Insert(buffer.Loc{X: 0, Y: 0}, rendered+"\n")
	}
	if !bp.Buf.IsTypst() {
		bp.Buf.DoSetOptionNative("filetype", "typst")
	}
	if c := bp.Buf.GetActiveCursor(); c != nil {
		c.ResetSelection()
		c.GotoLoc(buffer.Loc{X: 0, Y: 0})
	}
	bp.Relocate()
	focusTargetPane(tab, bp)
	notifyMessage("Applied template: " + t.DisplayTitle())
}

func (m *Manager) openTemplateInNewTab(tab *action.Tab, bp *action.BufPane, t Template, preambleOnly bool) {
	var vars TemplateVars
	vars.Title = DeriveTitle(t.Name + ".typ")
	rendered, err := RenderEntry(t, vars)
	if err != nil {
		notifyError("Failed to read template: " + err.Error())
		return
	}
	if preambleOnly {
		if pre := ExtractPreamble(rendered); pre != "" {
			rendered = pre
		}
	}
	if !strings.HasSuffix(rendered, "\n") {
		rendered += "\n"
	}
	focusTargetPane(tab, bp)
	bufName := uniqueUntitledTypstName(t.Name)
	b := buffer.NewBufferFromString(rendered, bufName, buffer.BTDefault)
	openBufferInNewTab(b)
	if preambleOnly {
		notifyMessage("Opened preamble in new tab: " + t.DisplayTitle())
		return
	}
	notifyMessage("Opened template in new tab: " + t.DisplayTitle())
}

// InsertPreamble inserts t's entrypoint/preamble at line 0 of bp's buffer, or
// opens it in a new .typ tab when no editable Typst or unnamed buffer is active.
func (m *Manager) InsertPreamble(bp *action.BufPane, tab *action.Tab, t Template) {
	if !t.IsPackage() {
		m.insertTemplateIntoBuffer(bp, tab, t, true)
		return
	}

	target := resolveActivePane(tab, bp)
	var targetBuf *buffer.Buffer
	if target != nil && isTypstOrUnnamedEditable(target.Buf) {
		targetBuf = target.Buf
	}
	spec := packageSpecForTemplate(t)
	backend := m.getExternalBackend()
	client := m.getUniverseClient()

	notifyMessage("Loading preamble for " + spec + "...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	go func() {
		content, err := m.fetchPackageEntryContent(ctx, backend, client, t, spec)
		canceled := ctx.Err() == context.Canceled
		cancel()
		if canceled {
			return
		}
		action.EnqueueUI(func() {
			if err != nil {
				notifyError("Failed to load preamble for " + spec + ": " + err.Error())
				return
			}
			curTab := tab
			if curTab == nil && action.Tabs != nil {
				curTab = action.MainTab()
			}
			if !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			if targetBuf == nil {
				focusTargetPane(curTab, resolveActivePane(curTab, bp))
				bufName := uniqueUntitledTypstName(t.Name)
				b := buffer.NewBufferFromString(content, bufName, buffer.BTDefault)
				openBufferInNewTab(b)
				notifyMessage("Opened preamble in new tab: " + spec)
				return
			}
			curBP := resolvePaneForBuffer(curTab, targetBuf)
			if curBP == nil {
				notifyError("Target buffer was closed")
				return
			}
			if IsBufferBlank(curBP.Buf) {
				curBP.Buf.Replace(curBP.Buf.Start(), curBP.Buf.End(), content)
			} else {
				curBP.Buf.Insert(buffer.Loc{X: 0, Y: 0}, content+"\n")
			}
			if !curBP.Buf.IsTypst() {
				curBP.Buf.DoSetOptionNative("filetype", "typst")
			}
			if c := curBP.Buf.GetActiveCursor(); c != nil {
				c.ResetSelection()
				c.GotoLoc(buffer.Loc{X: 0, Y: 0})
			}
			curBP.Relocate()
			focusTargetPane(curTab, curBP)
			notifyMessage("Inserted preamble: " + spec)
		})
	}()
}

// PromptScaffoldDir asks the user for a target directory and scaffolds t into it.
func (m *Manager) PromptScaffoldDir(bp *action.BufPane, tab *action.Tab, t Template) {
	if action.InfoBar == nil {
		return
	}
	defDir := defaultScaffoldDir(bp, t)
	action.InfoBar.Prompt("Scaffold directory: ", defDir, "TemplateDir", nil, func(resp string, canceled bool) {
		if canceled {
			return
		}
		dir := strings.TrimSpace(resp)
		if dir == "" {
			return
		}
		m.ScaffoldProjectAsync(tab, bp, t, dir)
	})
}

// ScaffoldProjectAsync scaffolds t into targetDir (using tinymist.doInitTemplate
// for @preview/@local when available, with pure-Go fallback) and opens the entrypoint.
func (m *Manager) ScaffoldProjectAsync(tab *action.Tab, bp *action.BufPane, t Template, targetDir string) {
	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		notifyError("Invalid directory: " + err.Error())
		return
	}
	if info, statErr := os.Stat(absTarget); statErr == nil {
		if !info.IsDir() {
			notifyError("Target path exists and is not a directory: " + targetDir)
			return
		}
		if entries, readErr := os.ReadDir(absTarget); readErr == nil && len(entries) > 0 {
			notifyError("Target directory is not empty: " + targetDir)
			return
		}
	}

	if !t.IsPackage() {
		entryAbs, err := ScaffoldToDir(t, absTarget, TemplateVars{}, false)
		if err != nil {
			notifyError("Scaffold failed: " + err.Error())
			return
		}
		openScaffoldedEntry(tab, bp, entryAbs)
		notifyMessage("Scaffolded " + t.DisplayTitle() + " -> " + targetDir)
		return
	}

	spec := packageSpecForTemplate(t)
	backend := m.getExternalBackend()
	client := m.getUniverseClient()
	notifyMessage("Scaffolding " + spec + "...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go func() {
		entryAbs, err := m.initPackageProject(ctx, backend, client, t, spec, absTarget)
		canceled := ctx.Err() == context.Canceled
		cancel()
		if canceled {
			return
		}
		action.EnqueueUI(func() {
			if err != nil {
				notifyError("Scaffold failed: " + err.Error())
				return
			}
			curTab := tab
			if curTab == nil && action.Tabs != nil {
				curTab = action.MainTab()
			}
			openScaffoldedEntry(curTab, resolveActivePane(curTab, bp), entryAbs)
			notifyMessage("Scaffolded " + spec + " -> " + targetDir)
		})
	}()
}

func (m *Manager) fetchPackageEntryContent(ctx context.Context, backend ExternalBackend, client *UniverseClient, t Template, spec string) (string, error) {
	if backend != nil {
		content, err := backend.GetTemplateEntry(ctx, spec)
		if err == nil && strings.TrimSpace(content) != "" {
			return content, nil
		}
	}
	if t.FilePath != "" {
		return ReadEntryContent(t)
	}
	ns, pkgName, pkgVer, ok := ParsePackageSpec(spec)
	if !ok || ns != "preview" {
		return "", fmt.Errorf("cannot read template %q offline without tinymist", spec)
	}
	if pkgVer == "" {
		var err error
		pkgName, pkgVer, err = ResolveUniverseVersion(ctx, client, spec)
		if err != nil {
			return "", err
		}
	}
	_, cacheDir := PackageDirs()
	pkgDir, err := client.DownloadAndExtractPreviewPackage(ctx, pkgName, pkgVer, cacheDir)
	if err != nil {
		return "", err
	}
	man, err := readPackageManifest(pkgDir)
	if err != nil || !man.HasTemplate() {
		return "", fmt.Errorf("downloaded package %s lacks [template] metadata", spec)
	}
	entryAbs := filepath.Join(pkgDir, filepath.FromSlash(man.Template.Path), filepath.FromSlash(man.Template.Entrypoint))
	raw, err := os.ReadFile(entryAbs)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (m *Manager) initPackageProject(ctx context.Context, backend ExternalBackend, client *UniverseClient, t Template, spec, absTarget string) (string, error) {
	if backend != nil {
		relEntry, err := backend.InitTemplate(ctx, spec, absTarget)
		if err == nil {
			if relEntry == "" {
				relEntry = "main.typ"
			}
			entryAbs := filepath.Clean(filepath.Join(absTarget, filepath.FromSlash(relEntry)))
			if isSubpath(absTarget, entryAbs) {
				return entryAbs, nil
			}
			return filepath.Join(absTarget, "main.typ"), nil
		}
	}

	// Pure-Go fallback: copy from already-cached/@local directory or fetch tarball.
	if t.DirPath != "" && t.IsMultiFile {
		return ScaffoldToDir(t, absTarget, TemplateVars{}, false)
	}
	ns, pkgName, pkgVer, ok := ParsePackageSpec(spec)
	if !ok || ns != "preview" {
		return "", fmt.Errorf("cannot initialize %q offline without tinymist", spec)
	}
	if pkgVer == "" {
		var err error
		pkgName, pkgVer, err = ResolveUniverseVersion(ctx, client, spec)
		if err != nil {
			return "", err
		}
	}
	_, cacheDir := PackageDirs()
	pkgDir, err := client.DownloadAndExtractPreviewPackage(ctx, pkgName, pkgVer, cacheDir)
	if err != nil {
		return "", err
	}
	man, err := readPackageManifest(pkgDir)
	if err != nil || !man.HasTemplate() {
		return "", fmt.Errorf("package %s has no [template] section", spec)
	}
	tmplRoot := filepath.Clean(filepath.Join(pkgDir, filepath.FromSlash(man.Template.Path)))
	entryAbs := filepath.Clean(filepath.Join(tmplRoot, filepath.FromSlash(man.Template.Entrypoint)))
	if !isSubpath(pkgDir, tmplRoot) || !isSubpath(tmplRoot, entryAbs) {
		return "", errors.New("unsafe template path in typst.toml")
	}
	resolved := Template{
		Name:        pkgName,
		Version:     pkgVer,
		Source:      SourceCachedPkg,
		IsMultiFile: true,
		DirPath:     tmplRoot,
		FilePath:    entryAbs,
		Entrypoint:  filepath.ToSlash(man.Template.Entrypoint),
		PackageSpec: "@" + ns + "/" + pkgName + ":" + pkgVer,
	}
	return ScaffoldToDir(resolved, absTarget, TemplateVars{}, false)
}

func readPackageManifest(pkgDir string) (TypstManifest, error) {
	raw, err := os.ReadFile(filepath.Join(pkgDir, "typst.toml"))
	if err != nil {
		return TypstManifest{}, err
	}
	return ParseTypstTOML(raw)
}

func packageSpecForTemplate(t Template) string {
	if t.PackageSpec != "" {
		return t.PackageSpec
	}
	if t.Source == SourceLocalPkg {
		if t.Version != "" {
			return "@local/" + t.Name + ":" + t.Version
		}
		return "@local/" + t.Name
	}
	if t.Version != "" {
		return "@preview/" + t.Name + ":" + t.Version
	}
	return "@preview/" + t.Name
}

func defaultScaffoldDir(bp *action.BufPane, t Template) string {
	baseDir := ""
	if bp != nil && bp.Buf != nil && bp.Buf.Path != "" && !bp.Buf.Type.Scratch && bp.Buf.AbsPath != "" {
		baseDir = filepath.Dir(bp.Buf.AbsPath)
	} else if wd, err := os.Getwd(); err == nil {
		baseDir = wd
	}
	slug := sanitizeDirSlug(t.Name)
	if slug == "" {
		slug = "typst-project"
	}
	if baseDir == "" {
		return slug
	}
	return filepath.Join(baseDir, slug)
}

func sanitizeDirSlug(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func uniqueUntitledTypstName(stem string) string {
	slug := sanitizeDirSlug(stem)
	if slug == "" {
		slug = "untitled"
	}
	for i := 0; i < 1000; i++ {
		cand := fmt.Sprintf("%s.typ", slug)
		if i > 0 {
			cand = fmt.Sprintf("%s-%d.typ", slug, i)
		}
		if _, err := os.Stat(cand); err == nil {
			continue
		}
		abs, _ := filepath.Abs(cand)
		inUse := false
		for _, buf := range buffer.OpenBuffers {
			if buf != nil && (buf.Path == cand || (abs != "" && buf.AbsPath == abs)) {
				inUse = true
				break
			}
		}
		if !inUse {
			return cand
		}
	}
	return slug + "-new.typ"
}

func openScaffoldedEntry(tab *action.Tab, bp *action.BufPane, entryAbs string) {
	b, err := buffer.NewBufferFromFile(entryAbs, buffer.BTDefault)
	if err != nil {
		notifyError("Failed to open scaffolded entry: " + err.Error())
		return
	}
	if bp != nil && isEditableDocBuffer(bp.Buf) && !bp.Buf.Modified() && bp.Buf.Path == "" && IsBufferBlank(bp.Buf) {
		bp.OpenBuffer(b)
		focusTargetPane(tab, bp)
		return
	}
	focusTargetPane(tab, bp)
	openBufferInNewTab(b)
}

func openBufferInNewTab(b *buffer.Buffer) {
	if b == nil {
		return
	}
	if action.Tabs == nil {
		return
	}
	w, h := 80, 24
	if screen.Screen != nil {
		w, h = screen.Screen.Size()
	}
	iOffset := config.GetInfoBarOffset()
	tp := action.NewTabFromBuffer(0, 0, w, h-1-iOffset, b)
	action.Tabs.AddTab(tp)
	action.Tabs.SetActive(len(action.Tabs.List) - 1)
	action.Tabs.Resize()
}

func resolveActivePane(tab *action.Tab, fallback *action.BufPane) *action.BufPane {
	if tab != nil {
		if cp := tab.CurPane(); cp != nil {
			return cp
		}
	}
	if fallback != nil {
		return fallback
	}
	if action.Tabs != nil {
		if mt := action.MainTab(); mt != nil {
			return mt.CurPane()
		}
	}
	return nil
}

func resolvePaneForBuffer(tab *action.Tab, targetBuf *buffer.Buffer) *action.BufPane {
	if tab != nil {
		for _, p := range tab.Panes {
			if bp, ok := p.(*action.BufPane); ok && bp.Buf == targetBuf {
				return bp
			}
		}
	}
	if action.Tabs != nil {
		for _, t := range action.Tabs.List {
			for _, p := range t.Panes {
				if bp, ok := p.(*action.BufPane); ok && bp.Buf == targetBuf {
					return bp
				}
			}
		}
	}
	return nil
}

func focusTargetPane(tab *action.Tab, bp *action.BufPane) {
	if tab == nil || bp == nil {
		return
	}
	for i, p := range tab.Panes {
		if p == bp {
			tab.SetActive(i)
			return
		}
	}
}

func findEditorPaneInTab(tab *action.Tab) *action.BufPane {
	if tab == nil {
		return nil
	}
	if cp := tab.CurPane(); cp != nil {
		return cp
	}
	for _, p := range tab.Panes {
		if bp, ok := p.(*action.BufPane); ok {
			return bp
		}
	}
	return nil
}

func tabTerminalWidth(tab *action.Tab) int {
	if screen.Screen != nil {
		w, _ := screen.Screen.Size()
		if w > 0 {
			return w
		}
	}
	if tab != nil && tab.Node != nil {
		return tab.Node.W
	}
	return 0
}

func readGlobalInt(key string, fallback int) int {
	v := config.GetGlobalOption(key)
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return fallback
	}
}

func notifyMessage(msg string) {
	if action.InfoBar != nil {
		action.InfoBar.Message(msg)
	}
}

func notifyError(msg string) {
	if action.InfoBar != nil {
		action.InfoBar.Error(msg)
	}
}

const previewDebounceDelay = 120 * time.Millisecond

func (tp *TemplatePane) cancelPendingPreview() {
	tp.previewMu.Lock()
	if tp.previewTimer != nil {
		tp.previewTimer.Stop()
		tp.previewTimer = nil
	}
	if tp.previewCancel != nil {
		tp.previewCancel()
		tp.previewCancel = nil
	}
	tp.previewGen++
	tp.lastPreviewKey = ""
	tp.previewMu.Unlock()
}

func (tp *TemplatePane) clearLivePreview() {
	if tp.mgr == nil || tp.tab == nil {
		return
	}
	if provider := tp.mgr.getPreviewProvider(); provider != nil {
		provider.Clear(tp.tab)
	}
}

func (tp *TemplatePane) schedulePreview(immediate bool) {
	if tp.closed || tp.mgr == nil {
		return
	}
	provider := tp.mgr.getPreviewProvider()
	if provider == nil || !provider.SupportsVisual() {
		if tp.hasOpenSourcePreview() {
			tp.updateSourcePreview()
		}
		return
	}
	if immediate {
		tp.previewMu.Lock()
		if tp.previewTimer != nil {
			tp.previewTimer.Stop()
			tp.previewTimer = nil
		}
		tp.lastPreviewKey = ""
		tp.previewMu.Unlock()
		tp.runPreviewNow(true)
		return
	}

	tp.previewMu.Lock()
	if tp.previewTimer != nil {
		tp.previewTimer.Stop()
	}
	tp.previewGen++
	gen := tp.previewGen
	tp.previewTimer = time.AfterFunc(previewDebounceDelay, func() {
		action.EnqueueUI(func() {
			tp.previewMu.Lock()
			stale := tp.closed || !tp.active || !tp.livePreview || tp.previewGen != gen
			tp.previewTimer = nil
			tp.previewMu.Unlock()
			if !stale {
				tp.runPreviewNow(false)
			}
		})
	})
	tp.previewMu.Unlock()
}

func (tp *TemplatePane) runPreviewNow(force bool) {
	if tp.closed || !tp.active || !tp.livePreview || tp.mgr == nil || tp.tab == nil {
		return
	}
	provider := tp.mgr.getPreviewProvider()
	if provider == nil || !provider.SupportsVisual() {
		return
	}

	tmpl, ok := tp.SelectedTemplate()
	if !ok {
		tp.cancelPendingPreview()
		provider.Clear(tp.tab)
		return
	}

	key := tmpl.ID + "\x00" + tmpl.Version + "\x00" + tmpl.Title
	tp.previewMu.Lock()
	if !force && tp.lastPreviewKey == key {
		tp.previewMu.Unlock()
		return
	}
	tp.lastPreviewKey = key
	if tp.previewCancel != nil {
		tp.previewCancel()
		tp.previewCancel = nil
	}
	tp.previewGen++
	gen := tp.previewGen
	tp.previewMu.Unlock()

	if !provider.OpenLoading(tp.tab, tp.resolveTarget(), tmpl.DisplayTitle()) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	tp.previewMu.Lock()
	if tp.closed || tp.previewGen != gen {
		tp.previewMu.Unlock()
		cancel()
		return
	}
	tp.previewCancel = cancel
	tp.previewMu.Unlock()

	mgr := tp.mgr
	client := mgr.getUniverseClient()
	cacheDir := mgr.getPreviewCacheDir()
	tab := tp.tab

	mgr.previewWG.Add(1)
	go func() {
		defer mgr.previewWG.Done()
		imgs, err := LoadTemplatePreviewImages(ctx, tmpl, client, cacheDir, provider.CompileTypst)
		canceled := ctx.Err() == context.Canceled
		cancel()
		if canceled {
			return
		}
		action.EnqueueUI(func() {
			tp.previewMu.Lock()
			stale := tp.closed || !tp.active || !tp.livePreview || tp.previewGen != gen
			tp.previewMu.Unlock()
			if stale {
				return
			}
			if err != nil {
				provider.ShowError(tab, tmpl.DisplayTitle(), err.Error())
				return
			}
			provider.ShowImages(tab, tmpl.DisplayTitle(), imgs)
		})
	}()
}

func (tp *TemplatePane) toggleLivePreview() {
	var provider PreviewProvider
	if tp.mgr != nil {
		provider = tp.mgr.getPreviewProvider()
	}
	if provider != nil && provider.SupportsVisual() {
		tp.livePreview = !tp.livePreview
		if tp.livePreview {
			tp.schedulePreview(true)
			notifyMessage("Template live preview enabled")
		} else {
			tp.cancelPendingPreview()
			tp.clearLivePreview()
			notifyMessage("Template live preview disabled")
		}
		return
	}
	tp.toggleSourcePreview()
}

func (tp *TemplatePane) hasOpenSourcePreview() bool {
	if tp.sourcePreviewBuf == nil || tp.tab == nil {
		return false
	}
	for _, p := range tp.tab.Panes {
		if bp, ok := p.(*action.BufPane); ok && bp.Buf == tp.sourcePreviewBuf {
			return true
		}
	}
	return false
}

func (tp *TemplatePane) toggleSourcePreview() {
	if tp.hasOpenSourcePreview() {
		tp.closeSourcePreview()
		return
	}
	tp.updateSourcePreview()
}

func (tp *TemplatePane) updateSourcePreview() {
	tmpl, ok := tp.SelectedTemplate()
	if !ok || tp.tab == nil {
		return
	}
	text := renderSourcePreviewText(tmpl)

	for _, p := range tp.tab.Panes {
		if bp, isBP := p.(*action.BufPane); isBP && tp.sourcePreviewBuf != nil && bp.Buf == tp.sourcePreviewBuf {
			bp.Buf.Type.Readonly = false
			bp.Buf.Settings["readonly"] = false
			bp.Buf.Replace(bp.Buf.Start(), bp.Buf.End(), text)
			bp.Buf.Type.Readonly = true
			bp.Buf.Settings["readonly"] = true
			if c := bp.Buf.GetActiveCursor(); c != nil {
				c.GotoLoc(buffer.Loc{X: 0, Y: 0})
			}
			bp.Relocate()
			return
		}
	}

	target := tp.resolveTarget()
	if target == nil {
		return
	}
	buf := buffer.NewBufferFromString(text, "template-preview.typ", buffer.BTScratch)
	buf.Type.Readonly = true
	buf.Settings["readonly"] = true
	buf.Settings["filetype"] = "typst"
	tp.sourcePreviewBuf = buf

	tp.suppressClearOnFocus = true
	target.VSplitIndex(buf, true)
	for i, p := range tp.tab.Panes {
		if p == tp {
			tp.tab.SetActive(i)
			break
		}
	}
	tp.suppressClearOnFocus = false
}

func (tp *TemplatePane) closeSourcePreview() {
	if tp.sourcePreviewBuf == nil || tp.tab == nil {
		tp.sourcePreviewBuf = nil
		return
	}
	targetBuf := tp.sourcePreviewBuf
	tp.sourcePreviewBuf = nil
	for i, p := range tp.tab.Panes {
		bp, ok := p.(*action.BufPane)
		if !ok || bp.Buf != targetBuf {
			continue
		}
		id := bp.ID()
		bp.Buf.Close()
		if n := tp.tab.GetNode(id); n != nil {
			n.Unsplit()
		}
		tp.tab.RemovePane(i)
		tp.tab.Resize()
		for j, rem := range tp.tab.Panes {
			if rem == tp {
				tp.tab.SetActive(j)
				break
			}
		}
		break
	}
}

func renderSourcePreviewText(t Template) string {
	if t.FilePath != "" {
		if rendered, err := RenderEntry(t, TemplateVars{Title: t.DisplayTitle()}); err == nil {
			return rendered
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// %s (%s)\n", t.DisplayTitle(), t.Source.String())
	if t.Description != "" {
		fmt.Fprintf(&b, "// %s\n", t.Description)
	}
	if t.PackageSpec != "" {
		fmt.Fprintf(&b, "\n#import %q: *\n", t.PackageSpec)
	}
	return b.String()
}
