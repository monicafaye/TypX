package outline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/micro-editor/tcell/v2"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/display"
	"github.com/monicafaye/TypX/internal/screen"
	"github.com/monicafaye/TypX/internal/typst"
	"github.com/monicafaye/TypX/internal/util"
)

const (
	// MinTerminalWidth is the minimum terminal width in columns required to display
	// the Document Outline sidebar. Below this threshold opening is refused and an
	// already-open sidebar automatically hides.
	MinTerminalWidth = 50

	// DefaultOutlineWidth is the default configured sidebar width in columns.
	DefaultOutlineWidth = 30

	// MinOutlineWidth is the narrowest usable sidebar width when clamped.
	MinOutlineWidth = 15

	// MinEditorWidth guarantees editor panes keep usable width alongside the sidebar.
	MinEditorWidth = 20

	// largeFileLineThreshold switches parsing to an async snapshot goroutine to
	// guarantee zero UI stutter on very large documents (> 5,000 lines).
	largeFileLineThreshold = 5000
)

// SymbolProvider asynchronously queries an LSP backend (e.g., tinymist) for
// document symbols of b. If unavailable, it returns false immediately. When
// symbols arrive, cb MUST be called on the main UI goroutine (via action.EnqueueUI)
// with the buffer Version the symbols correspond to.
type SymbolProvider func(b *buffer.Buffer, cb func(version uint64, symbols []Symbol)) bool

// JumpHandler is invoked on the main UI goroutine whenever the user chooses
// an entry in the Document Outline (via Enter or mouse click). allItems holds
// the full unfiltered outline in document order.
type JumpHandler func(bp *action.BufPane, item Item, allItems []Item)

// EffectiveWidth computes a safe docked sidebar width for a terminal of termWidth
// columns given configuredWidth (from typst.outlineWidth). Returns 0 when
// termWidth < MinTerminalWidth.
func EffectiveWidth(termWidth, configuredWidth int) int {
	if termWidth < MinTerminalWidth {
		return 0
	}
	w := configuredWidth
	if w <= 0 {
		w = DefaultOutlineWidth
	}
	maxW := termWidth - MinEditorWidth
	if half := termWidth / 2; half < maxW {
		maxW = half
	}
	if maxW < MinOutlineWidth {
		maxW = MinOutlineWidth
	}
	if w < MinOutlineWidth {
		w = MinOutlineWidth
	}
	if w > maxW {
		w = maxW
	}
	return w
}

// OutlinePane implements action.Pane, action.AuxiliaryPane, and display.Window
// as a docked vertical Document Outline sidebar on the left edge of a Tab.
type OutlinePane struct {
	view   *display.View
	id     uint64
	tab    *action.Tab
	target *action.BufPane
	mgr    *Manager

	active        bool
	mouseReleased bool

	parser        *IncrementalParser
	items         []Item
	visible       []Item
	activeIdx     int // Index in visible corresponding to target's cursor line, or -1
	selectedIdx   int // Highlighted selection index in visible
	scrollTop     int
	lastVersion   uint64
	lastShared    *buffer.SharedBuffer
	lastOpts      ParseOptions
	filtering     bool
	filterQuery   string
	desiredWidth  int
	clampedByTerm bool
	projectMode   bool
	isMultiFile   bool
	hasProject    bool
	includedFiles map[string]bool

	mu             sync.Mutex
	closed         bool
	gen            uint64
	refreshGen     uint64
	timer          *time.Timer
	hasDirty       bool
	dirtyStartLine int
}

var (
	_ action.Pane          = (*OutlinePane)(nil)
	_ action.AuxiliaryPane = (*OutlinePane)(nil)
	_ display.Window       = (*OutlinePane)(nil)
)

func newOutlinePane(tab *action.Tab, target *action.BufPane, mgr *Manager, desiredWidth int) *OutlinePane {
	return &OutlinePane{
		view:          &display.View{},
		tab:           tab,
		target:        target,
		mgr:           mgr,
		mouseReleased: true,
		parser:        NewIncrementalParser(),
		activeIdx:     -1,
		desiredWidth:  desiredWidth,
		projectMode:   true,
	}
}

// ID returns this pane's split node ID.
func (op *OutlinePane) ID() uint64 { return op.id }

// SetID sets this pane's split node ID.
func (op *OutlinePane) SetID(i uint64) { op.id = i }

// Name returns the pane display title.
func (op *OutlinePane) Name() string { return "Outline" }

// SetTab sets the owning Tab.
func (op *OutlinePane) SetTab(t *action.Tab) { op.tab = t }

// Tab returns the owning Tab.
func (op *OutlinePane) Tab() *action.Tab { return op.tab }

// IsAuxiliary marks OutlinePane as an auxiliary sidebar pane so closing the
// last primary buffer closes the Outline rather than stranding an orphaned tab.
func (op *OutlinePane) IsAuxiliary() bool { return true }

// GetView returns the pane's display.View.
func (op *OutlinePane) GetView() *display.View { return op.view }

// SetView updates the pane's display.View.
func (op *OutlinePane) SetView(v *display.View) {
	if v != nil {
		op.view = v
	}
}

// LocFromVisual translates visual coordinates to a buffer.Loc.
func (op *OutlinePane) LocFromVisual(vloc buffer.Loc) buffer.Loc { return vloc }

// Relocate ensures the selected item is scrolled into view.
func (op *OutlinePane) Relocate() bool {
	op.ensureSelectionVisible()
	return true
}

// Resize updates the pane dimensions.
func (op *OutlinePane) Resize(w, h int) {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	op.view.Width = w
	op.view.Height = h
	op.ensureSelectionVisible()
}

// SetActive sets focus state on the OutlinePane.
func (op *OutlinePane) SetActive(b bool) {
	if op.active == b {
		return
	}
	op.active = b
	if b {
		// When entering OutlinePane, start selection on the active section if valid.
		if op.activeIdx >= 0 && op.activeIdx < len(op.visible) && op.visible[op.activeIdx].Kind != KindFile {
			op.selectedIdx = op.activeIdx
		} else {
			op.snapSelectionToSelectable(true)
		}
		op.ensureSelectionVisible()
	} else {
		op.filtering = false
	}
}

// IsActive reports whether OutlinePane currently has focus.
func (op *OutlinePane) IsActive() bool { return op.active }

// Clear clears the pane's screen area.
func (op *OutlinePane) Clear() {
	if screen.Screen == nil || op.view.Width <= 0 || op.view.Height <= 0 {
		return
	}
	for y := 0; y < op.view.Height; y++ {
		for x := 0; x < op.view.Width; x++ {
			screen.SetContent(op.view.X+x, op.view.Y+y, ' ', nil, config.DefStyle)
		}
	}
}

// Close cancels pending timers and detaches this pane from its manager.
// Safe and idempotent.
func (op *OutlinePane) Close() {
	op.mu.Lock()
	already := op.closed
	op.closed = true
	op.gen++
	if op.timer != nil {
		op.timer.Stop()
		op.timer = nil
	}
	op.mu.Unlock()

	if !already && op.mgr != nil && op.tab != nil {
		if cur := op.mgr.panes[op.tab]; cur == op {
			delete(op.mgr.panes, op.tab)
		}
	}
}

// Items returns a copy of all parsed outline items.
func (op *OutlinePane) Items() []Item {
	out := make([]Item, len(op.items))
	copy(out, op.items)
	return out
}

// VisibleItems returns a copy of currently visible (filtered) outline items.
func (op *OutlinePane) VisibleItems() []Item {
	out := make([]Item, len(op.visible))
	copy(out, op.visible)
	return out
}

// ActiveItemIndex returns the index in VisibleItems matching the target cursor line, or -1.
func (op *OutlinePane) ActiveItemIndex() int { return op.activeIdx }

// SelectedIndex returns the currently selected index in VisibleItems.
func (op *OutlinePane) SelectedIndex() int { return op.selectedIdx }

// Target returns the linked BufPane, if still open in this tab.
func (op *OutlinePane) Target() *action.BufPane { return op.resolveTarget() }

// FilterQuery returns the active inline filter text.
func (op *OutlinePane) FilterQuery() string { return op.filterQuery }

// IsFiltering reports whether inline '/' filter input is active.
func (op *OutlinePane) IsFiltering() bool { return op.filtering }

// IsProjectMode reports whether multi-file project mode is enabled.
func (op *OutlinePane) IsProjectMode() bool { return op.projectMode }

// IsMultiFile reports whether the currently displayed outline spans multiple files.
func (op *OutlinePane) IsMultiFile() bool { return op.isMultiFile }

// ToggleProjectMode toggles between multi-file project view and current-file-only view.
func (op *OutlinePane) ToggleProjectMode() {
	op.projectMode = !op.projectMode
	op.RefreshNow(0, !op.projectMode)
}

// listHeight returns the number of screen rows available for outline entries.
func (op *OutlinePane) listHeight() int {
	if op.view.Height <= 1 {
		return 0
	}
	// Reserve row 0 for the header; reserve the bottom row for a footer bar when
	// height >= 4 (aligning visually with BufWindow's bottom statusline).
	if op.hasFooter() {
		return op.view.Height - 2
	}
	return op.view.Height - 1
}

func (op *OutlinePane) hasFooter() bool {
	return op.view.Height >= 4 && readGlobalBool("statusline", true)
}

func (op *OutlinePane) resolveTarget() *action.BufPane {
	if op.tab == nil {
		return nil
	}
	if op.target != nil {
		for _, p := range op.tab.Panes {
			if p == op.target {
				return op.target
			}
		}
		op.target = nil
	}
	// Fallback to any open Typst BufPane in the same tab.
	for _, p := range op.tab.Panes {
		if bp, ok := p.(*action.BufPane); ok && bp.Buf != nil && bp.Buf.IsTypst() {
			op.target = bp
			return bp
		}
	}
	return nil
}

func (op *OutlinePane) returnFocus() {
	if op.tab == nil {
		return
	}
	target := op.resolveTarget()
	if target != nil {
		for i, p := range op.tab.Panes {
			if p == target {
				op.tab.SetActive(i)
				return
			}
		}
	}
	for i, p := range op.tab.Panes {
		if !action.IsAuxiliaryPane(p) {
			op.tab.SetActive(i)
			return
		}
	}
}

// RefreshNow parses the target buffer (and #include project tree when project
// mode is enabled) and optionally requests LSP documentSymbol enrichment.
func (op *OutlinePane) RefreshNow(startLine int, requestLSP bool) {
	op.mu.Lock()
	if op.closed {
		op.mu.Unlock()
		return
	}
	op.refreshGen++
	gen := op.gen
	rgen := op.refreshGen
	op.mu.Unlock()

	target := op.resolveTarget()
	if target == nil || target.Buf == nil || !target.Buf.IsTypst() {
		op.isMultiFile = false
		op.hasProject = false
		op.includedFiles = nil
		op.setItems(nil)
		return
	}

	b := target.Buf
	opts := currentParseOptions()
	if op.lastShared != b.SharedBuffer || op.lastOpts != opts {
		op.parser.Reset()
		startLine = 0
	}
	op.lastShared = b.SharedBuffer
	op.lastOpts = opts
	ver := b.Version()
	op.lastVersion = ver

	activePath := bufferAbsPath(b)
	activeCanon := canonicalOutlinePath(activePath)
	var pinnedMain string
	if op.mgr != nil {
		if mfn := op.mgr.getMainFileResolver(); mfn != nil {
			pinnedMain = mfn(activePath)
		}
	}
	pinnedCanon := canonicalOutlinePath(pinnedMain)
	hasReadablePin := pinnedMain != "" && fileReadable(pinnedMain, b)

	if !op.projectMode {
		op.isMultiFile = false
		op.includedFiles = nil
		if b.LinesNum() > largeFileLineThreshold {
			op.parser.Reset()
			op.hasProject = hasReadablePin && pinnedCanon != activeCanon
			snap := snapshotLines(b)
			targetBuf := b
			go func() {
				var p IncrementalParser
				parsed := p.Update(snap, 0, opts)
				hasIncludes := len(p.Includes()) > 0
				stampItemPaths(parsed, activePath, activeCanon)
				action.EnqueueUI(func() {
					op.mu.Lock()
					stale := op.closed || op.gen != gen || op.refreshGen != rgen
					op.mu.Unlock()
					if stale || op.projectMode {
						return
					}
					curTarget := op.resolveTarget()
					if curTarget == nil || curTarget.Buf != targetBuf || targetBuf.Version() != ver {
						return
					}
					op.isMultiFile = false
					op.includedFiles = nil
					op.hasProject = (hasReadablePin && pinnedCanon != activeCanon) || hasIncludes
					op.setItems(parsed)
					if requestLSP {
						op.requestLSPEnrichment(targetBuf, gen, rgen)
					}
				})
			}()
		} else {
			parsed := op.parser.Update(b, startLine, opts)
			stampItemPaths(parsed, activePath, activeCanon)
			op.hasProject = (hasReadablePin && pinnedCanon != activeCanon) || len(op.parser.Includes()) > 0
			op.setItems(parsed)
			if requestLSP {
				op.requestLSPEnrichment(b, gen, rgen)
			}
		}
		return
	}

	entryPath := activePath
	if hasReadablePin {
		entryPath = pinnedMain
	}

	if !hasReadablePin && b.LinesNum() > largeFileLineThreshold {
		op.parser.Reset()
		// Copy immutable snapshots on main goroutine, parse and walk off-thread.
		snap := snapshotLines(b)
		openSnaps := snapshotOpenBuffers(b, snap)
		rootDir := ""
		if op.mgr != nil {
			if rfn := op.mgr.getRootResolver(); rfn != nil && entryPath != "" {
				rootDir = rfn(entryPath)
			}
		}
		if rootDir == "" && entryPath != "" {
			rootDir = filepath.Dir(entryPath)
		}
		targetBuf := b
		go func() {
			var p IncrementalParser
			parsed := p.Update(snap, 0, opts)
			items := parsed
			multiFile := false
			var walked map[string]bool
			if entryPath != "" && len(p.Includes()) > 0 {
				items, multiFile, walked = walkProjectOutlineFromSnapshots(entryPath, rootDir, activeCanon, snap, &p, openSnaps, opts)
			} else {
				stampItemPaths(items, activePath, activeCanon)
			}
			action.EnqueueUI(func() {
				op.mu.Lock()
				stale := op.closed || op.gen != gen || op.refreshGen != rgen
				op.mu.Unlock()
				if stale || !op.projectMode {
					return
				}
				curTarget := op.resolveTarget()
				if curTarget == nil || curTarget.Buf != targetBuf || targetBuf.Version() != ver {
					return
				}
				op.isMultiFile = multiFile
				op.includedFiles = walked
				op.hasProject = multiFile
				op.setItems(items)
				if requestLSP && !op.isMultiFile {
					op.requestLSPEnrichment(targetBuf, gen, rgen)
				}
			})
		}()
		return
	}

	var activeParser *IncrementalParser
	var localParsed []Item
	if b.LinesNum() <= largeFileLineThreshold {
		localParsed = op.parser.Update(b, startLine, opts)
		activeParser = op.parser
	} else {
		op.parser.Reset()
	}

	if entryPath == "" || (!hasReadablePin && activeParser != nil && len(activeParser.Includes()) == 0) {
		stampItemPaths(localParsed, activePath, activeCanon)
		op.isMultiFile = false
		op.hasProject = false
		op.includedFiles = nil
		op.setItems(localParsed)
		if requestLSP {
			op.requestLSPEnrichment(b, gen, rgen)
		}
		return
	}

	var rootResolver func(string) string
	if op.mgr != nil {
		rootResolver = op.mgr.getRootResolver()
	}
	items, multiFile, walked := walkProjectOutline(entryPath, b, activeParser, opts, rootResolver)
	op.isMultiFile = multiFile
	op.includedFiles = walked
	op.hasProject = multiFile || (hasReadablePin && pinnedCanon != activeCanon)
	op.setItems(items)

	if requestLSP && !op.isMultiFile {
		op.requestLSPEnrichment(b, gen, rgen)
	}
}

func snapshotLines(b *buffer.Buffer) typst.SliceSource {
	n := b.LinesNum()
	lines := make([][]byte, n)
	for i := 0; i < n; i++ {
		raw := b.LineBytes(i)
		cp := make([]byte, len(raw))
		copy(cp, raw)
		lines[i] = cp
	}
	return typst.SliceSource(lines)
}

func (op *OutlinePane) requestLSPEnrichment(b *buffer.Buffer, gen, rgen uint64) {
	if op.mgr == nil || b == nil {
		return
	}
	provider := op.mgr.getSymbolProvider()
	if provider == nil {
		return
	}
	targetBuf := b
	opts := currentParseOptions()
	provider(targetBuf, func(version uint64, symbols []Symbol) {
		op.mu.Lock()
		stale := op.closed || op.gen != gen || op.refreshGen != rgen
		op.mu.Unlock()
		if stale || len(symbols) == 0 {
			return
		}
		curTarget := op.resolveTarget()
		if curTarget == nil || curTarget.Buf != targetBuf || targetBuf.Version() != version {
			return
		}
		enriched := EnrichWithSymbols(op.items, symbols, opts)
		op.setItems(enriched)
	})
}

func (op *OutlinePane) scheduleRefresh(startLine int) {
	delay := time.Duration(readGlobalInt("typst.debounceMs", 150)) * time.Millisecond
	if delay < 0 {
		delay = 0
	}

	op.mu.Lock()
	if op.closed {
		op.mu.Unlock()
		return
	}
	if !op.hasDirty || startLine < op.dirtyStartLine {
		op.dirtyStartLine = startLine
		op.hasDirty = true
	}
	if op.timer != nil {
		op.timer.Stop()
	}
	gen := op.gen
	op.timer = time.AfterFunc(delay, func() {
		op.mu.Lock()
		if op.closed || op.gen != gen {
			op.mu.Unlock()
			return
		}
		op.mu.Unlock()

		action.EnqueueUI(func() {
			op.runScheduledRefresh(gen)
		})
	})
	op.mu.Unlock()
}

func (op *OutlinePane) runScheduledRefresh(gen uint64) {
	op.mu.Lock()
	if op.closed || op.gen != gen {
		op.mu.Unlock()
		return
	}
	startLine := 0
	if op.hasDirty {
		startLine = op.dirtyStartLine
		op.hasDirty = false
		op.dirtyStartLine = 0
	}
	op.mu.Unlock()

	op.RefreshNow(startLine, true)
}

func (op *OutlinePane) setItems(items []Item) {
	op.items = items
	op.rebuildVisible()
	op.syncActiveSection()
}

func (op *OutlinePane) rebuildVisible() {
	if op.filterQuery == "" {
		op.visible = op.items
	} else {
		q := strings.ToLower(op.filterQuery)
		filtered := make([]Item, 0, len(op.items))
		var curHeader *Item
		var headerMatches bool
		var segMatches []Item

		flushSeg := func() {
			if curHeader != nil {
				if headerMatches {
					// Header matched: header + all items in segment already accumulated or emit all?
					// If header matched, segMatches holds all items in this segment.
					filtered = append(filtered, *curHeader)
					filtered = append(filtered, segMatches...)
				} else if len(segMatches) > 0 {
					filtered = append(filtered, *curHeader)
					filtered = append(filtered, segMatches...)
				}
			} else if len(segMatches) > 0 {
				filtered = append(filtered, segMatches...)
			}
			curHeader = nil
			headerMatches = false
			segMatches = nil
		}

		for i := range op.items {
			it := op.items[i]
			if it.Kind == KindFile {
				flushSeg()
				hdr := it
				curHeader = &hdr
				headerMatches = strings.Contains(strings.ToLower(it.Title), q)
				continue
			}
			if headerMatches ||
				strings.Contains(strings.ToLower(it.Title), q) ||
				(it.Label != "" && strings.Contains(strings.ToLower(it.Label), q)) {
				segMatches = append(segMatches, it)
			}
		}
		flushSeg()
		op.visible = filtered
	}
	if len(op.visible) == 0 {
		op.selectedIdx = 0
		op.scrollTop = 0
	} else {
		op.snapSelectionToSelectable(true)
	}
	op.ensureSelectionVisible()
}

func (op *OutlinePane) snapSelectionToSelectable(preferForward bool) {
	if len(op.visible) == 0 {
		op.selectedIdx = 0
		return
	}
	if op.selectedIdx < 0 {
		op.selectedIdx = 0
	} else if op.selectedIdx >= len(op.visible) {
		op.selectedIdx = len(op.visible) - 1
	}
	if op.visible[op.selectedIdx].Kind != KindFile {
		return
	}
	if preferForward {
		for i := op.selectedIdx + 1; i < len(op.visible); i++ {
			if op.visible[i].Kind != KindFile {
				op.selectedIdx = i
				return
			}
		}
		for i := op.selectedIdx - 1; i >= 0; i-- {
			if op.visible[i].Kind != KindFile {
				op.selectedIdx = i
				return
			}
		}
		return
	}
	for i := op.selectedIdx - 1; i >= 0; i-- {
		if op.visible[i].Kind != KindFile {
			op.selectedIdx = i
			return
		}
	}
	for i := op.selectedIdx + 1; i < len(op.visible); i++ {
		if op.visible[i].Kind != KindFile {
			op.selectedIdx = i
			return
		}
	}
}

func (op *OutlinePane) navigableCount() int {
	n := 0
	for _, it := range op.visible {
		if it.Kind != KindFile {
			n++
		}
	}
	return n
}

// syncActiveSection finds the last visible item in the target file whose Line <= target cursor Y.
func (op *OutlinePane) syncActiveSection() {
	target := op.resolveTarget()
	if target == nil || target.Buf == nil || len(op.visible) == 0 {
		op.activeIdx = -1
		return
	}
	c := target.Buf.GetActiveCursor()
	if c == nil {
		op.activeIdx = -1
		return
	}
	curY := c.Y
	targetKey := canonicalOutlinePath(bufferAbsPath(target.Buf))

	found := -1
	for i, it := range op.visible {
		if it.Kind == KindFile {
			continue
		}
		if op.isMultiFile || (it.canonPath != "" && targetKey != "") {
			if it.canonPath != targetKey {
				continue
			}
		}
		if it.Line <= curY {
			found = i
		}
	}
	op.activeIdx = found

	// When unfocused, keep selection & scroll aligned with the active section.
	if !op.active && found >= 0 {
		op.selectedIdx = found
		op.ensureSelectionVisible()
	}
}

func (op *OutlinePane) ensureSelectionVisible() {
	lh := op.listHeight()
	if lh <= 0 || len(op.visible) == 0 {
		op.scrollTop = 0
		return
	}
	if op.selectedIdx < op.scrollTop {
		op.scrollTop = op.selectedIdx
	} else if op.selectedIdx >= op.scrollTop+lh {
		op.scrollTop = op.selectedIdx - lh + 1
	}
	if op.selectedIdx > 0 && op.scrollTop == op.selectedIdx && op.visible[op.scrollTop-1].Kind == KindFile && lh > 1 {
		op.scrollTop--
	}
	maxScroll := len(op.visible) - lh
	if maxScroll < 0 {
		maxScroll = 0
	}
	if op.scrollTop > maxScroll {
		op.scrollTop = maxScroll
	}
	if op.scrollTop < 0 {
		op.scrollTop = 0
	}
}

func (op *OutlinePane) moveSelection(delta int) {
	if len(op.visible) == 0 {
		return
	}
	next := op.selectedIdx + delta
	if next < 0 {
		next = 0
	} else if next >= len(op.visible) {
		next = len(op.visible) - 1
	}
	if op.visible[next].Kind == KindFile {
		if delta > 0 {
			for next < len(op.visible) && op.visible[next].Kind == KindFile {
				next++
			}
			if next >= len(op.visible) {
				if op.selectedIdx >= 0 && op.selectedIdx < len(op.visible) && op.visible[op.selectedIdx].Kind != KindFile {
					return
				}
				next = len(op.visible) - 1
			}
		} else if delta < 0 {
			for next >= 0 && op.visible[next].Kind == KindFile {
				next--
			}
			if next < 0 {
				if op.selectedIdx >= 0 && op.selectedIdx < len(op.visible) && op.visible[op.selectedIdx].Kind != KindFile {
					if op.scrollTop > 0 && op.visible[op.scrollTop-1].Kind == KindFile {
						op.scrollTop--
					}
					return
				}
				next = 0
			}
		}
	}
	op.selectedIdx = next
	op.ensureSelectionVisible()
}

func (op *OutlinePane) jumpToSelected() {
	if len(op.visible) == 0 || op.selectedIdx < 0 || op.selectedIdx >= len(op.visible) {
		op.returnFocus()
		return
	}
	target := op.resolveTarget()
	if target == nil || target.Buf == nil {
		op.returnFocus()
		return
	}

	item := op.visible[op.selectedIdx]
	if item.Kind == KindFile {
		return
	}

	targetKey := canonicalOutlinePath(bufferAbsPath(target.Buf))
	itemKey := item.canonPath
	if itemKey == "" && item.FilePath != "" {
		itemKey = canonicalOutlinePath(item.FilePath)
	}
	isCrossFile := item.FilePath != "" && itemKey != "" && itemKey != targetKey

	if isCrossFile {
		var destBP *action.BufPane
		if op.mgr != nil {
			if opener := op.mgr.getFileOpener(); opener != nil {
				destBP = opener(target, item.FilePath, item.Line, item.Column)
			}
		}
		if destBP == nil && (op.mgr == nil || op.mgr.getFileOpener() == nil) {
			destBP = openFileFallback(target, item.FilePath, item.Line, item.Column)
		}
		if destBP == nil || destBP.Buf == nil {
			op.returnFocus()
			return
		}
		if destBP.Tab() == op.tab {
			op.target = destBP
			for i, p := range op.tab.Panes {
				if p == destBP {
					op.tab.SetActive(i)
					break
				}
			}
			if op.lastShared != destBP.Buf.SharedBuffer {
				op.RefreshNow(0, false)
			} else {
				op.syncActiveSection()
			}
		} else {
			op.returnFocus()
		}
		if op.mgr != nil {
			if handler := op.mgr.getJumpHandler(); handler != nil {
				handler(destBP, item, op.Items())
			}
		}
		return
	}

	line := item.Line
	maxLine := target.Buf.LinesNum() - 1
	if maxLine < 0 {
		maxLine = 0
	}
	if line < 0 {
		line = 0
	} else if line > maxLine {
		line = maxLine
	}

	target.Buf.ClearCursors()
	c := target.Buf.GetActiveCursor()
	if c != nil {
		c.ResetSelection()
		c.GotoLoc(buffer.Loc{X: 0, Y: line})
	}
	target.Center()
	target.Relocate()
	op.syncActiveSection()
	op.returnFocus()

	if op.mgr != nil {
		if handler := op.mgr.getJumpHandler(); handler != nil {
			handler(target, item, op.Items())
		}
	}
}

// HandleEvent processes keyboard and mouse events for the OutlinePane.
func (op *OutlinePane) HandleEvent(event tcell.Event) {
	switch e := event.(type) {
	case *tcell.EventKey:
		op.handleKeyEvent(e)
	case *tcell.EventMouse:
		op.handleMouseEvent(e)
	}
}

// HandleCommand delegates command-bar commands to the linked editor BufPane.
func (op *OutlinePane) HandleCommand(text string) {
	if op.tab != nil {
		if cp := op.tab.CurPane(); cp != nil {
			cp.HandleCommand(text)
		}
	}
}

func (op *OutlinePane) handleKeyEvent(e *tcell.EventKey) {
	mod := e.Modifiers()
	key := e.Key()
	r := e.Rune()

	// Global sidebar shortcuts available in both normal and filter modes.
	if key == tcell.KeyRune && (r == 'o' || r == 'O') && mod&tcell.ModAlt != 0 {
		if op.mgr != nil {
			op.mgr.CloseForTab(op.tab)
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
		if action.InfoBar != nil && op.tab != nil {
			tab := op.tab
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
		if op.tab != nil && len(op.tab.Panes) > 1 {
			next := (op.tab.ActiveIndex() + 1) % len(op.tab.Panes)
			op.tab.SetActive(next)
		}
		return
	}
	if key == tcell.KeyCtrlQ {
		if op.mgr != nil {
			op.mgr.CloseForTab(op.tab)
		}
		return
	}

	if op.filtering {
		op.handleFilterKey(e)
		return
	}

	switch key {
	case tcell.KeyEscape, tcell.KeyTab:
		if op.filterQuery != "" {
			op.filterQuery = ""
			op.rebuildVisible()
			op.syncActiveSection()
			return
		}
		op.returnFocus()
	case tcell.KeyEnter:
		op.jumpToSelected()
	case tcell.KeyUp:
		op.moveSelection(-1)
	case tcell.KeyDown:
		op.moveSelection(1)
	case tcell.KeyHome:
		if len(op.visible) > 0 {
			op.selectedIdx = 0
			op.snapSelectionToSelectable(true)
			op.ensureSelectionVisible()
		}
	case tcell.KeyEnd:
		if len(op.visible) > 0 {
			op.selectedIdx = len(op.visible) - 1
			op.snapSelectionToSelectable(false)
			op.ensureSelectionVisible()
		}
	case tcell.KeyPgUp:
		step := op.listHeight()
		if step < 1 {
			step = 1
		}
		op.moveSelection(-step)
	case tcell.KeyPgDn:
		step := op.listHeight()
		if step < 1 {
			step = 1
		}
		op.moveSelection(step)
	case tcell.KeyRune:
		if mod&(tcell.ModAlt|tcell.ModCtrl) != 0 {
			return
		}
		switch r {
		case 'k':
			op.moveSelection(-1)
		case 'j':
			op.moveSelection(1)
		case 'g':
			if len(op.visible) > 0 {
				op.selectedIdx = 0
				op.snapSelectionToSelectable(true)
				op.ensureSelectionVisible()
			}
		case 'G':
			if len(op.visible) > 0 {
				op.selectedIdx = len(op.visible) - 1
				op.snapSelectionToSelectable(false)
				op.ensureSelectionVisible()
			}
		case 'p':
			op.ToggleProjectMode()
		case '/':
			op.filtering = true
			op.filterQuery = ""
			op.rebuildVisible()
		case 'q':
			if op.mgr != nil {
				op.mgr.CloseForTab(op.tab)
			}
		}
	}
}

func (op *OutlinePane) handleFilterKey(e *tcell.EventKey) {
	switch e.Key() {
	case tcell.KeyEscape:
		op.filtering = false
		op.filterQuery = ""
		op.rebuildVisible()
		op.syncActiveSection()
	case tcell.KeyEnter:
		op.filtering = false
		op.jumpToSelected()
		op.filterQuery = ""
		op.rebuildVisible()
		op.syncActiveSection()
	case tcell.KeyUp:
		op.moveSelection(-1)
	case tcell.KeyDown:
		op.moveSelection(1)
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if op.filterQuery == "" {
			op.filtering = false
			return
		}
		runes := []rune(op.filterQuery)
		op.filterQuery = string(runes[:len(runes)-1])
		op.rebuildVisible()
		op.syncActiveSection()
	case tcell.KeyRune:
		if e.Modifiers()&(tcell.ModAlt|tcell.ModCtrl) != 0 {
			return
		}
		op.filterQuery += string(e.Rune())
		op.selectedIdx = 0
		op.rebuildVisible()
		op.syncActiveSection()
	}
}

func (op *OutlinePane) handleMouseEvent(e *tcell.EventMouse) {
	mx, my := e.Position()
	btn := e.Buttons()

	switch btn {
	case tcell.Button1:
		wasReleased := op.mouseReleased
		op.mouseReleased = false
		if !wasReleased {
			return
		}
		if mx < op.view.X || mx >= op.view.X+op.view.Width {
			return
		}
		localY := my - op.view.Y
		lh := op.listHeight()
		if localY >= 1 && localY <= lh {
			idx := op.scrollTop + (localY - 1)
			if idx >= 0 && idx < len(op.visible) && op.visible[idx].Kind != KindFile {
				op.selectedIdx = idx
				op.jumpToSelected()
			}
		}
	case tcell.ButtonNone:
		op.mouseReleased = true
	case tcell.WheelUp:
		op.scrollBy(-2)
	case tcell.WheelDown:
		op.scrollBy(2)
	}
}

func (op *OutlinePane) scrollBy(delta int) {
	lh := op.listHeight()
	if lh <= 0 || len(op.visible) == 0 {
		return
	}
	maxScroll := len(op.visible) - lh
	if maxScroll < 0 {
		maxScroll = 0
	}
	next := op.scrollTop + delta
	if next < 0 {
		next = 0
	} else if next > maxScroll {
		next = maxScroll
	}
	op.scrollTop = next
}

// Display renders the Document Outline sidebar onto the terminal screen.
func (op *OutlinePane) Display() {
	if screen.Screen == nil || op.view.Width <= 0 || op.view.Height <= 0 {
		return
	}

	// Row 0: Header bar.
	headerStyle := op.headerStyle()
	headerText := " Outline"
	if op.filtering {
		headerText = " /" + op.filterQuery + "_"
	} else if op.filterQuery != "" {
		headerText = " /" + op.filterQuery
	} else if op.hasProject || !op.projectMode {
		tag := "[Project]"
		if !op.projectMode {
			tag = "[File]"
		}
		if op.view.Width < 24 {
			if op.projectMode {
				tag = "[P]"
			} else {
				tag = "[F]"
			}
		}
		if n := op.navigableCount(); n > 0 {
			headerText = fmt.Sprintf(" Outline %s (%d)", tag, n)
		} else {
			headerText = " Outline " + tag
		}
	} else if len(op.visible) > 0 {
		headerText = fmt.Sprintf(" Outline (%d)", len(op.visible))
	}
	drawClippedRow(op.view.X, op.view.Y, op.view.Width, headerText, headerStyle)

	lh := op.listHeight()
	if lh > 0 {
		if len(op.visible) == 0 {
			emptyMsg := " (no headings)"
			if op.filterQuery != "" {
				emptyMsg = " (no matches)"
			}
			dimStyle := lookupStyle("comment", config.DefStyle)
			drawClippedRow(op.view.X, op.view.Y+1, op.view.Width, emptyMsg, dimStyle)
			for row := 1; row < lh; row++ {
				drawClippedRow(op.view.X, op.view.Y+1+row, op.view.Width, "", config.DefStyle)
			}
		} else {
			lastHeadingLevel := 0
			for i := 0; i < op.scrollTop && i < len(op.visible); i++ {
				if op.visible[i].Kind == KindFile {
					lastHeadingLevel = 0
				} else if op.visible[i].Kind == KindHeading {
					lastHeadingLevel = op.visible[i].Level
				}
			}
			for row := 0; row < lh; row++ {
				idx := op.scrollTop + row
				screenY := op.view.Y + 1 + row
				if idx >= len(op.visible) {
					drawClippedRow(op.view.X, screenY, op.view.Width, "", config.DefStyle)
					continue
				}
				it := op.visible[idx]
				if it.Kind == KindFile {
					lastHeadingLevel = 0
				} else if it.Kind == KindHeading {
					lastHeadingLevel = it.Level
				}
				hasChild := idx+1 < len(op.visible) &&
					it.Kind == KindHeading &&
					op.visible[idx+1].Kind == KindHeading &&
					op.visible[idx+1].Level > it.Level

				isSelected := op.active && it.Kind != KindFile && idx == op.selectedIdx
				isCurrent := it.Kind != KindFile && idx == op.activeIdx

				prefix := " "
				if isCurrent {
					prefix = "▎"
				}
				label := prefix + formatOutlineEntry(it, lastHeadingLevel, hasChild)
				st := op.itemStyle(it, isSelected, isCurrent)
				drawClippedRow(op.view.X, screenY, op.view.Width, label, st)
			}
		}
	}

	// Bottom row: Footer bar aligned with BufWindow statusline.
	if op.hasFooter() {
		footerY := op.view.Y + op.view.Height - 1
		footerText := " Alt+O:close"
		if op.active {
			if op.filtering {
				footerText = " Enter:jump Esc:clear"
			} else if op.hasProject || !op.projectMode {
				if op.projectMode {
					footerText = " p:file /:find Enter:jump"
				} else {
					footerText = " p:proj /:find Enter:jump"
				}
			} else {
				footerText = " Enter:jump /:find Esc:back"
			}
		}
		drawClippedRow(op.view.X, footerY, op.view.Width, footerText, op.headerStyle())
	}
}

func formatOutlineEntry(it Item, lastHeadingLevel int, hasChild bool) string {
	switch it.Kind {
	case KindFile:
		return "▸ " + it.Title
	case KindHeading:
		depth := it.Level - 1
		if depth < 0 {
			depth = 0
		}
		if depth > 4 {
			depth = 4
		}
		indent := strings.Repeat("  ", depth)
		icon := "• "
		if it.Level == 1 || hasChild {
			icon = "▾ "
		}
		return indent + icon + it.Title
	case KindFigure:
		depth := lastHeadingLevel
		if depth > 3 {
			depth = 3
		}
		indent := strings.Repeat("  ", depth)
		if strings.HasPrefix(it.Title, "Figure (") {
			return indent + "▣ " + it.Title
		}
		return indent + "▣ Fig: " + it.Title
	case KindLabel:
		depth := lastHeadingLevel
		if depth > 3 {
			depth = 3
		}
		indent := strings.Repeat("  ", depth)
		return indent + "◈ <" + it.Title + ">"
	default:
		return it.Title
	}
}

func (op *OutlinePane) headerStyle() tcell.Style {
	if op.active {
		return lookupStyle("statusline", config.DefStyle.Reverse(true))
	}
	return lookupStyle("statusline.inactive", lookupStyle("statusline", config.DefStyle.Reverse(true)))
}

func (op *OutlinePane) itemStyle(it Item, isSelected, isCurrent bool) tcell.Style {
	if isSelected {
		return lookupStyle("selection", config.DefStyle.Reverse(true))
	}

	var base tcell.Style
	switch it.Kind {
	case KindHeading:
		if it.Level == 1 {
			base = lookupStyle("identifier.class", config.DefStyle.Bold(true))
		} else if it.Level == 2 {
			base = lookupStyle("identifier", config.DefStyle)
		} else {
			base = config.DefStyle
		}
	case KindFigure:
		base = lookupStyle("type", config.DefStyle)
	case KindLabel:
		base = lookupStyle("symbol.tag", lookupStyle("comment", config.DefStyle))
	case KindFile:
		base = lookupStyle("comment", config.DefStyle).Bold(true)
	default:
		base = config.DefStyle
	}

	if isCurrent {
		// In micro/TypX colorschemes, cursor-line is a single-color link storing
		// the highlight background in its foreground slot (matching BufWindow).
		if st, ok := config.Colorscheme["cursor-line"]; ok {
			fg, _, _ := st.Decompose()
			return base.Background(fg).Bold(true)
		}
		return base.Bold(true)
	}
	return base
}

func lookupStyle(name string, fallback tcell.Style) tcell.Style {
	if config.Colorscheme != nil {
		if st, ok := config.Colorscheme[name]; ok {
			return st
		}
	}
	return fallback
}

// drawClippedRow renders text across exactly width terminal cells starting at (x, y),
// truncating with '…' when needed and padding the remainder with spaces.
func drawClippedRow(x, y, width int, text string, style tcell.Style) {
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

// Manager coordinates per-tab OutlinePane instances, buffer observation,
// narrow-terminal auto-hide/restore, multi-tab switching, and optional LSP symbols.
type Manager struct {
	buffer.NopObserver

	panes      map[*action.Tab]*OutlinePane
	autoHidden map[*action.Tab]bool

	providerMu       sync.RWMutex
	symbolProvider   SymbolProvider
	jumpHandler      JumpHandler
	mainFileResolver func(string) string
	rootResolver     func(string) string
	fileOpener       func(bp *action.BufPane, filePath string, line, col int) *action.BufPane
}

func newManager() *Manager {
	return &Manager{
		panes:      make(map[*action.Tab]*OutlinePane),
		autoHidden: make(map[*action.Tab]bool),
	}
}

var (
	defaultManager = newManager()
	initOnce       sync.Once
)

// Init registers the Document Outline action, command, keybinding, buffer
// observer, and tab/resize hooks. Safe to call multiple times (runs once).
func Init() {
	initOnce.Do(func() {
		action.RegisterAction("ToggleOutline", NewToggleOutlineAction())
		action.RegisterCommand("outline", NewOutlineCmd(), outlineCompleter)
		action.RegisterDefaultBindingWithFallback("buffer", "Alt-o", "ToggleOutline")

		buffer.RegisterObserver(defaultManager)
		action.RegisterPreResizeHook(defaultManager.onPreResize)
		action.RegisterPostResizeHook(defaultManager.onPostResize)
		action.RegisterTabSwitchHook(defaultManager.onTabSwitch)
		action.RegisterPaneFocusHook(defaultManager.onPaneFocus)
	})
}

// SetSymbolProvider sets the optional LSP documentSymbol provider used to
// enrich locally parsed outlines when tinymist is connected. Pass nil to disable.
func SetSymbolProvider(p SymbolProvider) {
	defaultManager.setSymbolProvider(p)
}

func (m *Manager) setSymbolProvider(p SymbolProvider) {
	m.providerMu.Lock()
	m.symbolProvider = p
	m.providerMu.Unlock()
}

func (m *Manager) getSymbolProvider() SymbolProvider {
	m.providerMu.RLock()
	p := m.symbolProvider
	m.providerMu.RUnlock()
	return p
}

// SetJumpHandler registers an optional callback invoked when the user selects
// a section in the Document Outline (e.g., to reposition an open preview).
// Pass nil to clear.
func SetJumpHandler(h JumpHandler) {
	defaultManager.setJumpHandler(h)
}

func (m *Manager) setJumpHandler(h JumpHandler) {
	m.providerMu.Lock()
	m.jumpHandler = h
	m.providerMu.Unlock()
}

func (m *Manager) getJumpHandler() JumpHandler {
	m.providerMu.RLock()
	h := m.jumpHandler
	m.providerMu.RUnlock()
	return h
}

// SetMainFileResolver sets the optional resolver returning the pinned main file
// path for an active file path. Pass nil to clear.
func SetMainFileResolver(fn func(string) string) {
	defaultManager.setMainFileResolver(fn)
}

func (m *Manager) setMainFileResolver(fn func(string) string) {
	m.providerMu.Lock()
	m.mainFileResolver = fn
	m.providerMu.Unlock()
}

func (m *Manager) getMainFileResolver() func(string) string {
	m.providerMu.RLock()
	fn := m.mainFileResolver
	m.providerMu.RUnlock()
	return fn
}

// SetRootResolver sets the optional resolver returning the Typst project root
// directory for an entrypoint file path. Pass nil to clear.
func SetRootResolver(fn func(string) string) {
	defaultManager.setRootResolver(fn)
}

func (m *Manager) setRootResolver(fn func(string) string) {
	m.providerMu.Lock()
	m.rootResolver = fn
	m.providerMu.Unlock()
}

func (m *Manager) getRootResolver() func(string) string {
	m.providerMu.RLock()
	fn := m.rootResolver
	m.providerMu.RUnlock()
	return fn
}

// SetFileOpener sets the optional cross-file jump opener (honoring typst.gotoOpenMode).
// Pass nil to use the built-in fallback opener.
func SetFileOpener(fn func(bp *action.BufPane, filePath string, line, col int) *action.BufPane) {
	defaultManager.setFileOpener(fn)
}

func (m *Manager) setFileOpener(fn func(bp *action.BufPane, filePath string, line, col int) *action.BufPane) {
	m.providerMu.Lock()
	m.fileOpener = fn
	m.providerMu.Unlock()
}

func (m *Manager) getFileOpener() func(bp *action.BufPane, filePath string, line, col int) *action.BufPane {
	m.providerMu.RLock()
	fn := m.fileOpener
	m.providerMu.RUnlock()
	return fn
}

// PaneForTab returns the currently open OutlinePane on tab, or nil.
func PaneForTab(tab *action.Tab) *OutlinePane {
	return defaultManager.paneForTab(tab)
}

func (m *Manager) paneForTab(tab *action.Tab) *OutlinePane {
	if tab == nil {
		return nil
	}
	op := m.panes[tab]
	if op == nil {
		return nil
	}
	// Verify the pane is still attached to tab.
	for _, p := range tab.Panes {
		if p == op {
			return op
		}
	}
	op.Close()
	delete(m.panes, tab)
	return nil
}

// Toggle toggles the Document Outline sidebar on bp's tab.
func Toggle(bp *action.BufPane) bool {
	return defaultManager.Toggle(bp)
}

// Open ensures the Document Outline sidebar is open on bp's tab.
func Open(bp *action.BufPane, focus bool) (*OutlinePane, bool) {
	return defaultManager.Open(bp, focus)
}

// CloseForTab closes the Document Outline sidebar on tab if open.
func CloseForTab(tab *action.Tab) bool {
	return defaultManager.CloseForTab(tab)
}

// NotifyMainFileChanged refreshes all open Document Outline panes when the
// pinned main project file changes via :pin or :unpin.
func NotifyMainFileChanged() {
	defaultManager.NotifyMainFileChanged()
}

// NotifyMainFileChanged refreshes all open outline panes on this manager.
func (m *Manager) NotifyMainFileChanged() {
	m.pruneStaleTabs()
	for tab := range m.panes {
		if op := m.paneForTab(tab); op != nil {
			op.RefreshNow(0, true)
		}
	}
}

// NewToggleOutlineAction returns the BufKeyAction bound to Alt+O.
// Returns false on non-Typst buffers so fallback bindings remain intact.
func NewToggleOutlineAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		if bp == nil || bp.Buf == nil {
			return false
		}
		tab := bp.Tab()
		// Allow Alt+O to close an already-open outline even if focus moved to a non-.typ split.
		if defaultManager.paneForTab(tab) != nil {
			defaultManager.CloseForTab(tab)
			return true
		}
		if !bp.Buf.IsTypst() {
			return false
		}
		defaultManager.Open(bp, true)
		return true
	}
}

// NewOutlineCmd returns the `:outline [toggle|open|close|focus]` command handler.
func NewOutlineCmd() func(bp *action.BufPane, args []string) {
	return func(bp *action.BufPane, args []string) {
		if bp == nil || bp.Buf == nil {
			return
		}
		tab := bp.Tab()
		sub := "toggle"
		if len(args) > 0 {
			sub = strings.ToLower(strings.TrimSpace(args[0]))
		}
		switch sub {
		case "toggle", "":
			if defaultManager.paneForTab(tab) != nil {
				defaultManager.CloseForTab(tab)
				return
			}
			if !bp.Buf.IsTypst() {
				notifyError("Outline is only available for Typst buffers")
				return
			}
			defaultManager.Open(bp, true)
		case "open", "on":
			if !bp.Buf.IsTypst() {
				notifyError("Outline is only available for Typst buffers")
				return
			}
			defaultManager.Open(bp, true)
		case "close", "off":
			delete(defaultManager.autoHidden, tab)
			defaultManager.CloseForTab(tab)
		case "focus":
			if op := defaultManager.paneForTab(tab); op != nil {
				for i, p := range tab.Panes {
					if p == op {
						tab.SetActive(i)
						return
					}
				}
			}
			if !bp.Buf.IsTypst() {
				notifyError("Outline is only available for Typst buffers")
				return
			}
			defaultManager.Open(bp, true)
		default:
			notifyError(fmt.Sprintf("Invalid outline argument %q (use open, close, focus, or toggle)", args[0]))
		}
	}
}

var outlineArgChoices = []string{"toggle", "open", "close", "focus"}

func outlineCompleter(b *buffer.Buffer) ([]string, []string) {
	if b == nil {
		return nil, nil
	}
	c := b.GetActiveCursor()
	if c == nil {
		return nil, nil
	}
	input, argstart := b.GetArg()
	lower := strings.ToLower(input)

	var suggestions []string
	for _, choice := range outlineArgChoices {
		if strings.HasPrefix(choice, lower) {
			suggestions = append(suggestions, choice)
		}
	}
	if len(suggestions) == 0 {
		return nil, nil
	}
	prefixChars := util.Max(0, c.X-argstart)
	completions := make([]string, len(suggestions))
	for i, s := range suggestions {
		completions[i] = util.SliceEndStr(s, prefixChars)
	}
	return completions, suggestions
}

// Toggle opens or closes the Document Outline for bp's tab.
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

// Open docks the Document Outline sidebar on the left edge of bp's tab.
func (m *Manager) Open(bp *action.BufPane, focus bool) (*OutlinePane, bool) {
	if bp == nil || bp.Buf == nil || bp.Tab() == nil {
		return nil, false
	}
	m.pruneStaleTabs()
	tab := bp.Tab()

	if existing := m.paneForTab(tab); existing != nil {
		existing.target = bp
		existing.RefreshNow(0, true)
		if focus {
			for i, p := range tab.Panes {
				if p == existing {
					tab.SetActive(i)
					break
				}
			}
		}
		return existing, true
	}

	if !bp.Buf.IsTypst() {
		notifyError("Outline is only available for Typst buffers")
		return nil, false
	}

	termW := tabTerminalWidth(tab)
	if termW < MinTerminalWidth {
		delete(m.autoHidden, tab)
		notifyError(fmt.Sprintf("Outline requires terminal width >= %d columns (current: %d)", MinTerminalWidth, termW))
		return nil, false
	}

	cfgW := readGlobalInt("typst.outlineWidth", DefaultOutlineWidth)
	effW := EffectiveWidth(termW, cfgW)
	if effW <= 0 {
		notifyError(fmt.Sprintf("Outline requires terminal width >= %d columns", MinTerminalWidth))
		return nil, false
	}

	dockID := tab.Node.DockLeft(effW)
	if dockID == 0 {
		notifyError("Unable to dock Outline sidebar in current split layout")
		return nil, false
	}

	delete(m.autoHidden, tab)
	op := newOutlinePane(tab, bp, m, cfgW)
	op.SetID(dockID)
	op.clampedByTerm = effW < cfgW
	m.panes[tab] = op

	tab.AddPane(op, 0)
	tab.Resize()
	op.RefreshNow(0, true)

	if focus {
		tab.SetActive(0)
	}
	return op, true
}

// CloseForTab closes and unsplits the Document Outline sidebar on tab if open.
func (m *Manager) CloseForTab(tab *action.Tab) bool {
	if tab == nil {
		return false
	}
	delete(m.autoHidden, tab)
	return m.detachFromTab(tab)
}

func (m *Manager) detachFromTab(tab *action.Tab) bool {
	op := m.paneForTab(tab)
	if op == nil {
		return false
	}
	wasFocused := tab.ActivePane() == op
	target := op.resolveTarget()
	id := op.ID()

	op.Close()
	if n := tab.GetNode(id); n != nil {
		n.Unsplit()
	}
	for i, p := range tab.Panes {
		if p == op {
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
	for t, op := range m.panes {
		if !live[t] {
			op.Close()
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
	// Collect tabs first because detachFromTab mutates m.panes.
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

	// Restore any tabs whose outline was automatically hidden due to narrow width.
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
			bp := findTypstPaneInTab(t)
			if bp != nil {
				m.Open(bp, false)
			}
		}
	}

	// Clamp or restore docked sidebar widths across remaining open outlines.
	cfgW := readGlobalInt("typst.outlineWidth", DefaultOutlineWidth)
	effW := EffectiveWidth(w, cfgW)
	if effW <= 0 {
		return
	}
	for t, op := range m.panes {
		if m.paneForTab(t) == nil {
			continue
		}
		n := t.GetNode(op.ID())
		if n == nil {
			continue
		}
		if n.W > effW {
			op.clampedByTerm = true
			n.ResizeSplit(effW)
			t.Resize()
		} else if op.clampedByTerm && n.W < effW {
			targetW := effW
			if op.desiredWidth > 0 && op.desiredWidth < targetW {
				targetW = op.desiredWidth
			}
			if targetW > n.W {
				n.ResizeSplit(targetW)
				t.Resize()
			}
			op.clampedByTerm = targetW < op.desiredWidth
		}
	}
}

func (m *Manager) onTabSwitch(tab *action.Tab) {
	m.pruneStaleTabs()
	op := m.paneForTab(tab)
	if op == nil {
		return
	}
	target := op.resolveTarget()
	if target == nil || target.Buf == nil || !target.Buf.IsTypst() {
		m.CloseForTab(tab)
		return
	}
	opts := currentParseOptions()
	if op.lastShared != target.Buf.SharedBuffer || op.lastVersion != target.Buf.Version() || op.lastOpts != opts {
		op.RefreshNow(0, true)
	} else {
		op.syncActiveSection()
	}
}

func (m *Manager) onPaneFocus(tab *action.Tab, pane action.Pane) {
	op := m.paneForTab(tab)
	if op == nil || pane == nil || pane == op {
		return
	}
	bp, ok := pane.(*action.BufPane)
	if !ok || bp.Buf == nil || !bp.Buf.IsTypst() {
		return
	}
	if op.target != bp {
		op.target = bp
		op.RefreshNow(0, true)
	} else {
		op.syncActiveSection()
	}
}

func (op *OutlinePane) referencesFile(b *buffer.Buffer) bool {
	if b == nil {
		return false
	}
	target := op.resolveTarget()
	if target != nil && target.Buf != nil && target.Buf.SharedBuffer == b.SharedBuffer {
		return true
	}
	if !op.projectMode || b.Path == "" {
		return false
	}
	canon := canonicalOutlinePath(bufferAbsPath(b))
	if canon == "" {
		return false
	}
	if op.includedFiles != nil && op.includedFiles[canon] {
		return true
	}
	for _, it := range op.items {
		if it.canonPath == canon {
			return true
		}
	}
	if op.mgr != nil {
		if mfn := op.mgr.getMainFileResolver(); mfn != nil {
			targetPath := ""
			if target != nil && target.Buf != nil {
				targetPath = bufferAbsPath(target.Buf)
			}
			if pinned := mfn(targetPath); pinned != "" && canonicalOutlinePath(pinned) == canon {
				return true
			}
		}
	}
	return false
}

// OnBufferChange schedules a debounced outline refresh for any open OutlinePane
// whose target buffer or referenced project file matches b.
func (m *Manager) OnBufferChange(b *buffer.Buffer, ev buffer.ChangeEvent) {
	if b == nil || !b.IsTypst() {
		return
	}
	for tab, op := range m.panes {
		if m.paneForTab(tab) == nil {
			continue
		}
		if op.referencesFile(b) {
			startLine := ev.StartLine
			target := op.resolveTarget()
			if target == nil || target.Buf == nil || target.Buf.SharedBuffer != b.SharedBuffer {
				startLine = 0
			}
			op.scheduleRefresh(startLine)
		}
	}
}

// OnBufferSave flushes any pending outline refresh immediately on save.
func (m *Manager) OnBufferSave(b *buffer.Buffer, ev buffer.SaveEvent) {
	if b == nil || !b.IsTypst() {
		return
	}
	for tab, op := range m.panes {
		if m.paneForTab(tab) == nil {
			continue
		}
		if op.referencesFile(b) {
			op.mu.Lock()
			if op.timer != nil {
				op.timer.Stop()
				op.timer = nil
			}
			op.hasDirty = false
			op.dirtyStartLine = 0
			op.mu.Unlock()
			op.RefreshNow(0, true)
		}
	}
}

// OnCursorMove updates the highlighted active section in real time.
func (m *Manager) OnCursorMove(b *buffer.Buffer, ev buffer.CursorEvent) {
	if b == nil || !b.IsTypst() {
		return
	}
	for tab, op := range m.panes {
		if m.paneForTab(tab) == nil {
			continue
		}
		target := op.resolveTarget()
		if target != nil && target.Buf == b {
			op.syncActiveSection()
		}
	}
}

// OnBufferClose re-targets or cleanly closes any open OutlinePane whose target
// buffer just closed (e.g., when closing one split in a multi-split tab).
func (m *Manager) OnBufferClose(b *buffer.Buffer) {
	if b == nil {
		return
	}
	var affected []*action.Tab
	for tab, op := range m.panes {
		if op.target != nil && op.target.Buf == b {
			affected = append(affected, tab)
		}
	}
	for _, tab := range affected {
		op := m.paneForTab(tab)
		if op == nil {
			continue
		}
		// Find another still-open Typst BufPane in this tab (excluding the closing buffer).
		var replacement *action.BufPane
		for _, p := range tab.Panes {
			bp, ok := p.(*action.BufPane)
			if !ok || bp.Buf == nil || bp.Buf == b {
				continue
			}
			if bp.Buf.IsTypst() {
				replacement = bp
				break
			}
		}
		if replacement != nil {
			op.target = replacement
			op.RefreshNow(0, true)
		} else {
			m.CloseForTab(tab)
		}
	}
}

func findTypstPaneInTab(tab *action.Tab) *action.BufPane {
	if tab == nil {
		return nil
	}
	if cp := tab.CurPane(); cp != nil && cp.Buf != nil && cp.Buf.IsTypst() {
		return cp
	}
	for _, p := range tab.Panes {
		if bp, ok := p.(*action.BufPane); ok && bp.Buf != nil && bp.Buf.IsTypst() {
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

func currentParseOptions() ParseOptions {
	return ParseOptions{
		IncludeLabels: readGlobalBool("typst.outlineShowLabels", false),
	}
}

func readGlobalBool(key string, fallback bool) bool {
	v := config.GetGlobalOption(key)
	if b, ok := v.(bool); ok {
		return b
	}
	return fallback
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

func readGlobalString(key, fallback string) string {
	v := config.GetGlobalOption(key)
	if s, ok := v.(string); ok {
		return s
	}
	return fallback
}

func openFileFallback(bp *action.BufPane, filePath string, line, col int) *action.BufPane {
	targetKey := canonicalOutlinePath(filePath)
	if targetKey == "" {
		return nil
	}

	moveAndCenter := func(pane *action.BufPane) {
		if pane == nil || pane.Buf == nil {
			return
		}
		maxLine := pane.Buf.LinesNum() - 1
		if maxLine < 0 {
			maxLine = 0
		}
		y := line
		if y < 0 {
			y = 0
		} else if y > maxLine {
			y = maxLine
		}
		x := col
		maxX := util.CharacterCount(pane.Buf.LineBytes(y))
		if x < 0 {
			x = 0
		} else if x > maxX {
			x = maxX
		}
		pane.Buf.ClearCursors()
		if c := pane.Buf.GetActiveCursor(); c != nil {
			c.ResetSelection()
			c.GotoLoc(buffer.Loc{X: x, Y: y})
		}
		pane.Center()
		pane.Relocate()
	}

	if action.Tabs != nil {
		for ti, t := range action.Tabs.List {
			if t == nil {
				continue
			}
			for pi, p := range t.Panes {
				pane, ok := p.(*action.BufPane)
				if !ok || pane == nil || pane.Buf == nil || pane.Buf.Type.Scratch {
					continue
				}
				if canonicalOutlinePath(bufferAbsPath(pane.Buf)) == targetKey {
					action.Tabs.SetActive(ti)
					t.SetActive(pi)
					moveAndCenter(pane)
					return pane
				}
			}
		}
	}

	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		notifyError(fmt.Sprintf("Target file not found: %s", filepath.Base(filePath)))
		return nil
	}

	b, err := buffer.NewBufferFromFile(filePath, buffer.BTDefault)
	if err != nil {
		notifyError(fmt.Sprintf("Failed to open %s: %v", filepath.Base(filePath), err))
		return nil
	}

	mode := strings.ToLower(strings.TrimSpace(readGlobalString("typst.gotoOpenMode", "tab")))
	if mode == "current" && bp != nil && bp.Buf != nil && bp.Buf.Modified() && !bp.Buf.Shared() {
		mode = "tab"
	}

	switch mode {
	case "vsplit":
		if bp != nil && bp.Tab() != nil {
			if newBP := bp.VSplitIndex(b, true); newBP != nil {
				moveAndCenter(newBP)
				return newBP
			}
		}
	case "current":
		if bp != nil {
			bp.OpenBuffer(b)
			if t := bp.Tab(); t != nil {
				for i, p := range t.Panes {
					if p == bp {
						t.SetActive(i)
						break
					}
				}
			}
			moveAndCenter(bp)
			return bp
		}
	}

	if action.Tabs == nil {
		return nil
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
	newBP := tp.CurPane()
	moveAndCenter(newBP)
	return newBP
}

func notifyError(msg string) {
	if action.InfoBar != nil {
		action.InfoBar.Error(msg)
	}
}
