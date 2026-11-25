package action

import (
	luar "layeh.com/gopher-luar"

	"github.com/micro-editor/tcell/v2"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/display"
	ulua "github.com/monicafaye/TypX/internal/lua"
	"github.com/monicafaye/TypX/internal/screen"
	"github.com/monicafaye/TypX/internal/views"
)

// The TabList is a list of tabs and a window to display the tab bar
// at the top of the screen
type TabList struct {
	*display.TabWindow
	List []*Tab
}

// NewTabList creates a TabList from a list of buffers by creating a Tab
// for each buffer
func NewTabList(bufs []*buffer.Buffer) *TabList {
	w, h := screen.Screen.Size()
	iOffset := config.GetInfoBarOffset()
	tl := new(TabList)
	tl.List = make([]*Tab, len(bufs))
	if len(bufs) > 1 {
		for i, b := range bufs {
			tl.List[i] = NewTabFromBuffer(0, 1, w, h-1-iOffset, b)
		}
	} else {
		tl.List[0] = NewTabFromBuffer(0, 0, w, h-iOffset, bufs[0])
	}
	tl.TabWindow = display.NewTabWindow(w, 0)
	tl.Names = make([]string, len(bufs))

	return tl
}

var (
	preResizeHooks  []func(w, h int)
	postResizeHooks []func(w, h int)
	tabSwitchHooks  []func(tab *Tab)
	paneFocusHooks  []func(tab *Tab, pane Pane)
)

// RegisterPreResizeHook registers a callback invoked at the start of TabList.Resize
// before split trees are resized (e.g., to auto-hide sidebars on narrow terminals).
func RegisterPreResizeHook(fn func(w, h int)) {
	if fn == nil {
		panic("action.RegisterPreResizeHook: hook must not be nil")
	}
	preResizeHooks = append(preResizeHooks, fn)
}

// RegisterPostResizeHook registers a callback invoked at the end of TabList.Resize
// after split trees and panes have been resized.
func RegisterPostResizeHook(fn func(w, h int)) {
	if fn == nil {
		panic("action.RegisterPostResizeHook: hook must not be nil")
	}
	postResizeHooks = append(postResizeHooks, fn)
}

// RegisterTabSwitchHook registers a callback invoked when the active Tab changes.
func RegisterTabSwitchHook(fn func(tab *Tab)) {
	if fn == nil {
		panic("action.RegisterTabSwitchHook: hook must not be nil")
	}
	tabSwitchHooks = append(tabSwitchHooks, fn)
}

// RegisterPaneFocusHook registers a callback invoked when the active Pane within a Tab changes.
func RegisterPaneFocusHook(fn func(tab *Tab, pane Pane)) {
	if fn == nil {
		panic("action.RegisterPaneFocusHook: hook must not be nil")
	}
	paneFocusHooks = append(paneFocusHooks, fn)
}

// UpdateNames makes sure that the list of names the tab window has access to is
// correct
func (t *TabList) UpdateNames() {
	t.Names = t.Names[:0]
	for _, p := range t.List {
		if len(p.Panes) == 0 {
			t.Names = append(t.Names, "")
			continue
		}
		activePane := p.Panes[p.active]
		if IsAuxiliaryPane(activePane) {
			if cp := p.CurPane(); cp != nil {
				t.Names = append(t.Names, cp.Name())
				continue
			}
		}
		t.Names = append(t.Names, activePane.Name())
	}
}

// AddTab adds a new tab to this TabList
func (t *TabList) AddTab(p *Tab) {
	t.List = append(t.List, p)
	t.Resize()
	t.UpdateNames()
}

// RemoveTab removes a tab with the given id from the TabList
func (t *TabList) RemoveTab(id uint64) {
	for i, p := range t.List {
		if len(p.Panes) == 0 {
			continue
		}
		matched := false
		for _, pane := range p.Panes {
			if pane != nil && pane.ID() == id {
				matched = true
				break
			}
		}
		if matched {
			copy(t.List[i:], t.List[i+1:])
			t.List[len(t.List)-1] = nil
			t.List = t.List[:len(t.List)-1]
			if len(t.List) > 0 && t.Active() >= len(t.List) {
				t.SetActive(len(t.List) - 1)
			}
			t.Resize()
			t.UpdateNames()
			return
		}
	}
}

// Resize resizes all elements within the tab list
// One thing to note is that when there is only 1 tab
// the tab bar should not be drawn so resizing must take
// that into account
func (t *TabList) Resize() {
	w, h := screen.Screen.Size()
	for _, fn := range preResizeHooks {
		fn(w, h)
	}
	iOffset := config.GetInfoBarOffset()
	InfoBar.Resize(w, h-1)
	if len(t.List) > 1 {
		for _, p := range t.List {
			p.Y = 1
			p.Node.Resize(w, h-1-iOffset)
			p.Resize()
		}
	} else if len(t.List) == 1 {
		t.List[0].Y = 0
		t.List[0].Node.Resize(w, h-iOffset)
		t.List[0].Resize()
	}
	t.TabWindow.Resize(w, h)
	for _, fn := range postResizeHooks {
		fn(w, h)
	}
}

// HandleEvent checks for a resize event or a mouse event on the tab bar
// otherwise it will forward the event to the currently active tab
func (t *TabList) HandleEvent(event tcell.Event) {
	switch e := event.(type) {
	case *tcell.EventResize:
		t.Resize()
	case *tcell.EventMouse:
		mx, my := e.Position()
		switch e.Buttons() {
		case tcell.Button1:
			if my == t.Y && len(t.List) > 1 {
				if mx == 0 {
					t.Scroll(-4)
				} else if mx == t.Width-1 {
					t.Scroll(4)
				} else {
					ind := t.LocFromVisual(buffer.Loc{mx, my})
					if ind != -1 {
						t.SetActive(ind)
					}
				}
				return
			}
		case tcell.ButtonNone:
			if t.List[t.Active()].release {
				// Mouse release received, while already released
				t.ResetMouse()
				return
			}
		case tcell.WheelUp:
			if my == t.Y && len(t.List) > 1 {
				t.Scroll(4)
				return
			}
		case tcell.WheelDown:
			if my == t.Y && len(t.List) > 1 {
				t.Scroll(-4)
				return
			}
		}
	}
	t.List[t.Active()].HandleEvent(event)
}

// Display updates the names and then displays the tab bar
func (t *TabList) Display() {
	t.UpdateNames()
	if len(t.List) > 1 {
		t.TabWindow.Display()
	}
}

func (t *TabList) SetActive(a int) {
	if a < 0 || a >= len(t.List) {
		return
	}
	t.TabWindow.SetActive(a)

	var activated *Tab
	for i, p := range t.List {
		if i == a {
			if !p.isActive {
				p.isActive = true
				activated = p

				err := config.RunPluginFn("onSetActive", luar.New(ulua.L, p.CurPane()))
				if err != nil {
					screen.TermMessage(err)
				}
			}
		} else {
			p.isActive = false
		}
	}
	if activated != nil {
		for _, fn := range tabSwitchHooks {
			fn(activated)
		}
	}
}

// ResetMouse resets the mouse release state after the screen was stopped
// or the pane changed.
// This prevents situations in which mouse releases are received at the wrong place
// and the mouse state is still pressed.
func (t *TabList) ResetMouse() {
	for _, tab := range t.List {
		if !tab.release && tab.resizing != nil {
			tab.resizing = nil
		}

		tab.release = true

		for _, p := range tab.Panes {
			if bp, ok := p.(*BufPane); ok {
				bp.resetMouse()
			}
		}
	}
}

// CloseTerms notifies term panes that a terminal job has finished.
func (t *TabList) CloseTerms() {
	for _, tab := range t.List {
		for _, p := range tab.Panes {
			if tp, ok := p.(*TermPane); ok {
				tp.HandleTermClose()
			}
		}
	}
}

// Tabs is the global tab list
var Tabs *TabList

func InitTabs(bufs []*buffer.Buffer) {
	multiopen := config.GetGlobalOption("multiopen").(string)
	if multiopen == "tab" {
		Tabs = NewTabList(bufs)
	} else {
		Tabs = NewTabList(bufs[:1])
		for _, b := range bufs[1:] {
			if multiopen == "vsplit" {
				MainTab().CurPane().VSplitBuf(b)
			} else { // default hsplit
				MainTab().CurPane().HSplitBuf(b)
			}
		}
	}

	screen.RestartCallback = Tabs.ResetMouse
}

func MainTab() *Tab {
	return Tabs.List[Tabs.Active()]
}

// A Tab represents a single tab
// It consists of a list of edit panes (the open buffers),
// a split tree (stored as just the root node), and a uiwindow
// to display the UI elements like the borders between splits
type Tab struct {
	*views.Node
	*display.UIWindow

	isActive bool

	Panes       []Pane
	active      int
	lastBufPane *BufPane

	resizing *views.Node // node currently being resized
	// captures whether the mouse is released
	release bool
}

// NewTabFromBuffer creates a new tab from the given buffer
func NewTabFromBuffer(x, y, width, height int, b *buffer.Buffer) *Tab {
	t := new(Tab)
	t.Node = views.NewRoot(x, y, width, height)
	t.UIWindow = display.NewUIWindow(t.Node)
	t.release = true

	e := NewBufPaneFromBuf(b, t)
	e.SetID(t.ID())
	t.lastBufPane = e

	t.Panes = append(t.Panes, e)
	return t
}

func NewTabFromPane(x, y, width, height int, pane Pane) *Tab {
	t := new(Tab)
	t.Node = views.NewRoot(x, y, width, height)
	t.UIWindow = display.NewUIWindow(t.Node)
	t.release = true
	pane.SetTab(t)
	pane.SetID(t.ID())
	if bp, ok := pane.(*BufPane); ok {
		t.lastBufPane = bp
	}

	t.Panes = append(t.Panes, pane)
	return t
}

// HandleEvent takes a tcell event and usually dispatches it to the current
// active pane. However if the event is a resize or a mouse event where the user
// is interacting with the UI (resizing splits) then the event is consumed here
// If the event is a mouse press event in a pane, that pane will become active
// and get the event
func (t *Tab) HandleEvent(event tcell.Event) {
	switch e := event.(type) {
	case *tcell.EventMouse:
		mx, my := e.Position()
		btn := e.Buttons()
		switch {
		case btn & ^(tcell.WheelUp|tcell.WheelDown|tcell.WheelLeft|tcell.WheelRight) != tcell.ButtonNone:
			// button press or drag
			wasReleased := t.release
			t.release = false

			if btn == tcell.Button1 {
				if t.resizing != nil {
					var size int
					if t.resizing.Kind == views.STVert {
						size = mx - t.resizing.X
					} else {
						size = my - t.resizing.Y + 1
					}
					t.resizing.ResizeSplit(size)
					t.Resize()
					return
				}
				if wasReleased {
					t.resizing = t.GetMouseSplitNode(buffer.Loc{mx, my})
					if t.resizing != nil {
						return
					}
				}
			}

			if wasReleased {
				for i, p := range t.Panes {
					v := p.GetView()
					inpane := mx >= v.X && mx < v.X+v.Width && my >= v.Y && my < v.Y+v.Height
					if inpane {
						t.SetActive(i)
						break
					}
				}
			}
		case btn == tcell.ButtonNone:
			// button release
			t.release = true
			if t.resizing != nil {
				t.resizing = nil
				return
			}
		default:
			// wheel move
			for _, p := range t.Panes {
				v := p.GetView()
				inpane := mx >= v.X && mx < v.X+v.Width && my >= v.Y && my < v.Y+v.Height
				if inpane {
					p.HandleEvent(event)
					return
				}
			}
		}

	}
	t.Panes[t.active].HandleEvent(event)
}

// SetActive changes the currently active pane to the specified index
func (t *Tab) SetActive(i int) {
	if len(t.Panes) == 0 {
		t.active = 0
		return
	}
	if i < 0 {
		i = 0
	} else if i >= len(t.Panes) {
		i = len(t.Panes) - 1
	}
	target := t.Panes[i]
	t.active = i
	panes := make([]Pane, len(t.Panes))
	copy(panes, t.Panes)
	for _, p := range panes {
		if p == nil || !t.containsPane(p) {
			continue
		}
		if p == target {
			p.SetActive(true)
		} else {
			p.SetActive(false)
		}
	}
	activeIdx := -1
	for j, p := range t.Panes {
		if p == target {
			activeIdx = j
			break
		}
	}
	if activeIdx >= 0 {
		t.active = activeIdx
	} else if len(t.Panes) == 0 {
		t.active = 0
		return
	} else if t.active >= len(t.Panes) {
		t.active = len(t.Panes) - 1
	}
	activePane := t.Panes[t.active]
	if bp, ok := activePane.(*BufPane); ok {
		t.lastBufPane = bp
	}
	for _, fn := range paneFocusHooks {
		fn(t, activePane)
	}
}

func (t *Tab) containsPane(target Pane) bool {
	for _, p := range t.Panes {
		if p == target {
			return true
		}
	}
	return false
}

// ActivePane returns the literally focused pane in this tab (including auxiliary panes).
func (t *Tab) ActivePane() Pane {
	if len(t.Panes) == 0 || t.active < 0 || t.active >= len(t.Panes) {
		return nil
	}
	return t.Panes[t.active]
}

// ActiveIndex returns the index of the currently focused pane in this tab.
func (t *Tab) ActiveIndex() int {
	return t.active
}

// IsResizing reports whether the user is currently dragging a split border.
func (t *Tab) IsResizing() bool {
	return t.resizing != nil
}

// PrimaryPaneCount returns the number of non-auxiliary panes open in this tab.
func (t *Tab) PrimaryPaneCount() int {
	count := 0
	for _, p := range t.Panes {
		if !IsAuxiliaryPane(p) {
			count++
		}
	}
	return count
}

// CloseAuxiliaryPanes closes and unsplits every auxiliary pane in this tab.
func (t *Tab) CloseAuxiliaryPanes() {
	changed := false
	for i := len(t.Panes) - 1; i >= 0; i-- {
		p := t.Panes[i]
		if !IsAuxiliaryPane(p) {
			continue
		}
		id := p.ID()
		p.Close()
		if n := t.GetNode(id); n != nil {
			n.Unsplit()
		}
		t.RemovePane(i)
		changed = true
	}
	if changed {
		t.Resize()
		if len(t.Panes) > 0 {
			if t.active < 0 || t.active >= len(t.Panes) {
				t.active = len(t.Panes) - 1
			}
			t.SetActive(t.active)
		}
	}
}

// AddPane adds a pane at a given index
func (t *Tab) AddPane(pane Pane, i int) {
	if len(t.Panes) == i {
		t.Panes = append(t.Panes, pane)
		return
	}
	t.Panes = append(t.Panes[:i+1], t.Panes[i:]...)
	t.Panes[i] = pane
	if i <= t.active {
		t.active++
	}
}

// GetPane returns the pane with the given split index
func (t *Tab) GetPane(splitid uint64) int {
	for i, p := range t.Panes {
		if p.ID() == splitid {
			return i
		}
	}
	return 0
}

// Remove pane removes the pane with the given index
func (t *Tab) RemovePane(i int) {
	if i < 0 || i >= len(t.Panes) {
		return
	}
	if bp, ok := t.Panes[i].(*BufPane); ok && bp == t.lastBufPane {
		t.lastBufPane = nil
	}
	copy(t.Panes[i:], t.Panes[i+1:])
	t.Panes[len(t.Panes)-1] = nil
	t.Panes = t.Panes[:len(t.Panes)-1]
	if i < t.active {
		t.active--
	} else if t.active >= len(t.Panes) && len(t.Panes) > 0 {
		t.active = len(t.Panes) - 1
	}
}

// Resize resizes all panes according to their corresponding split nodes
func (t *Tab) Resize() {
	for _, p := range t.Panes {
		n := t.GetNode(p.ID())
		if n == nil {
			continue
		}
		pv := p.GetView()
		offset := 0
		if n.X != 0 {
			offset = 1
		}
		pv.X, pv.Y = n.X+offset, n.Y
		p.SetView(pv)
		w := n.W - offset
		if w < 0 {
			w = 0
		}
		p.Resize(w, n.H)
	}
}

// CurPane returns the currently active BufPane. When an auxiliary sidebar pane
// (such as the Document Outline) is focused, CurPane falls back to the tab's
// last-focused or first BufPane so commands and plugins keep targeting the document.
func (t *Tab) CurPane() *BufPane {
	if len(t.Panes) == 0 || t.active < 0 || t.active >= len(t.Panes) {
		return nil
	}
	if p, ok := t.Panes[t.active].(*BufPane); ok {
		return p
	}
	if !IsAuxiliaryPane(t.Panes[t.active]) {
		return nil
	}
	if t.lastBufPane != nil {
		for _, p := range t.Panes {
			if p == t.lastBufPane {
				return t.lastBufPane
			}
		}
		t.lastBufPane = nil
	}
	for _, p := range t.Panes {
		if bp, ok := p.(*BufPane); ok {
			return bp
		}
	}
	return nil
}
