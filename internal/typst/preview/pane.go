package preview

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/micro-editor/tcell/v2"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/display"
	"github.com/monicafaye/TypX/internal/screen"
	"github.com/monicafaye/TypX/internal/views"
)

const (
	// MinTerminalWidth is the minimum terminal width required to dock the inline preview.
	MinTerminalWidth = 60

	// MinPreviewWidth is the narrowest usable preview width in columns.
	MinPreviewWidth = 24

	// MinEditorWidth guarantees the editor keeps usable columns beside the preview.
	MinEditorWidth = 24

	// DefaultPreviewWidthPct is the default right-pane width percentage.
	DefaultPreviewWidthPct = 48

	// scrollStepPx is the default continuous scroll step in pixels per key/wheel notch.
	scrollStepPx = 140

	// panStepPx is the horizontal pan step in pixels when zoomed past 100%.
	panStepPx = 120

	// Zoom percentage bounds and step size.
	minZoomPct     = 40
	defaultZoomPct = 100
	maxZoomPct     = 250
	zoomStepPct    = 15

	// doubleClickMaxWait and doubleClickMaxCellDist bound click-to-source double-clicks.
	doubleClickMaxWait     = 400 * time.Millisecond
	doubleClickMaxCellDist = 2

	// resizeStepCols is the column delta per keyboard preview resize ('<' / '>').
	resizeStepCols = 6

	// minEditDebounceMs prevents rapid typing from spawning overlapping compiles.
	minEditDebounceMs = 300

	// cursorSyncDebounceMs debounces cursor-to-preview scroll sync on line moves.
	cursorSyncDebounceMs = 120
)

var spinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// EffectiveWidth computes the initial docked preview width in columns given
// termWidth and configuredPct (20..80). Returns 0 when termWidth < MinTerminalWidth.
func EffectiveWidth(termWidth, configuredPct int) int {
	if termWidth < MinTerminalWidth {
		return 0
	}
	pct := configuredPct
	if pct < 20 {
		pct = DefaultPreviewWidthPct
	}
	if pct > 80 {
		pct = 80
	}
	w := (termWidth * pct) / 100
	maxW := termWidth - MinEditorWidth
	if w > maxW {
		w = maxW
	}
	if w < MinPreviewWidth {
		w = MinPreviewWidth
	}
	if w >= termWidth {
		return 0
	}
	return w
}

type pendingClickTarget struct {
	virtualY   int
	shared     *buffer.SharedBuffer
	version    uint64
	jumpedLine int
}

// PreviewPane implements action.Pane, action.AuxiliaryPane, and display.Window
// as a docked right-hand continuous inline (Kitty or Sixel) live preview pane.
type PreviewPane struct {
	view   *display.View
	id     uint64
	tab    *action.Tab
	target *action.BufPane
	mgr    *Manager

	active bool

	deck           *DocumentDeck
	viewport       *ViewportRender
	scrollY        int
	scrollX        int
	zoomPct        int
	invert         bool
	trimMargins    bool
	syncCursor     bool
	pendingSection *SectionTarget
	pendingClick   *pendingClickTarget

	// Temporary override deck state (used by Template Gallery live preview).
	overrideActive   bool
	overrideTitle    string
	overrideLoading  bool
	overrideErr      string
	overrideDeck     *DocumentDeck
	openedByOverride bool
	savedScrollY     int
	savedScrollX     int

	compiling            bool
	compilingWithAnchors bool
	compileStart         time.Time
	lastErr              string
	lastVersion          uint64
	lastShared           *buffer.SharedBuffer
	lastPath             string
	lastCompiledMain     string
	pinnedSubfileDirty   bool
	spinTick             atomic.Int32
	spinStop             chan struct{}
	hadOverlayTxt        bool

	// Mouse click / drag tracking for click-to-source reverse sync.
	mouseReleased bool
	lastClickTime time.Time
	lastClickX    int
	lastClickY    int

	// Inline graphics emission tracking (main UI goroutine only).
	frameDirty       bool
	clearPending     bool
	lastEmitX        int
	lastEmitY        int
	lastEmitW        int
	lastEmitH        int
	lastEmitProto    GraphicsProtocol
	protocolOverride GraphicsProtocol
	vpTargetW        int
	vpViewW          int
	vpTargetH        int

	mu              sync.Mutex
	closed          bool
	gen             uint64
	debounceTimer   *time.Timer
	cursorSyncTimer *time.Timer
	compileCancel   context.CancelFunc
}

var (
	_ action.Pane          = (*PreviewPane)(nil)
	_ action.AuxiliaryPane = (*PreviewPane)(nil)
	_ display.Window       = (*PreviewPane)(nil)
)

func newPreviewPane(tab *action.Tab, target *action.BufPane, mgr *Manager) *PreviewPane {
	return &PreviewPane{
		view:          &display.View{},
		tab:           tab,
		target:        target,
		mgr:           mgr,
		zoomPct:       defaultZoomPct,
		invert:        readGlobalBool("typst.previewInvert", false),
		trimMargins:   readGlobalBool("typst.previewTrimMargins", false),
		syncCursor:    readGlobalBool("typst.previewSyncCursor", false),
		mouseReleased: true,
		lastEmitX:     -1,
		lastEmitY:     -1,
	}
}

func (pp *PreviewPane) ID() uint64            { return pp.id }
func (pp *PreviewPane) SetID(i uint64)        { pp.id = i }
func (pp *PreviewPane) Name() string          { return "Preview" }
func (pp *PreviewPane) SetTab(t *action.Tab)  { pp.tab = t }
func (pp *PreviewPane) Tab() *action.Tab      { return pp.tab }
func (pp *PreviewPane) IsAuxiliary() bool     { return true }
func (pp *PreviewPane) GetView() *display.View { return pp.view }
func (pp *PreviewPane) SetView(v *display.View) {
	if v != nil {
		pp.view = v
	}
}
func (pp *PreviewPane) LocFromVisual(vloc buffer.Loc) buffer.Loc { return vloc }
func (pp *PreviewPane) Relocate() bool                           { return true }
func (pp *PreviewPane) IsActive() bool                           { return pp.active }
func (pp *PreviewPane) IsInverted() bool                         { return pp.invert }
func (pp *PreviewPane) IsTrimMargins() bool                      { return pp.trimMargins }
func (pp *PreviewPane) IsSyncCursor() bool                       { return pp.syncCursor }

func (pp *PreviewPane) SetActive(b bool) {
	pp.active = b
	if !b {
		pp.mouseReleased = true
	}
}

func (pp *PreviewPane) isTabResizing() bool {
	return pp.tab != nil && pp.tab.IsResizing()
}

func (pp *PreviewPane) Resize(w, h int) {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	changed := pp.view.Width != w || pp.view.Height != h
	pp.view.Width = w
	pp.view.Height = h
	if changed && pp.activeDeck() != nil && !pp.isTabResizing() {
		pp.preserveRatioRebuild()
	}
}

func (pp *PreviewPane) Clear() {
	if screen.Screen == nil || pp.view.Width <= 0 || pp.view.Height <= 0 {
		return
	}
	for y := 0; y < pp.view.Height; y++ {
		for x := 0; x < pp.view.Width; x++ {
			screen.SetContent(pp.view.X+x, pp.view.Y+y, ' ', nil, config.DefStyle)
		}
	}
}

// Close cancels pending compiles/timers, cleans up temporary PNG decks, and detaches.
func (pp *PreviewPane) Close() {
	pp.mu.Lock()
	already := pp.closed
	pp.closed = true
	pp.gen++
	if pp.debounceTimer != nil {
		pp.debounceTimer.Stop()
		pp.debounceTimer = nil
	}
	if pp.cursorSyncTimer != nil {
		pp.cursorSyncTimer.Stop()
		pp.cursorSyncTimer = nil
	}
	if pp.compileCancel != nil {
		pp.compileCancel()
		pp.compileCancel = nil
	}
	pp.mu.Unlock()

	pp.stopSpinner()
	if pp.deck != nil {
		pp.deck.Close()
		pp.deck = nil
	}
	if pp.overrideDeck != nil {
		pp.overrideDeck.Close()
		pp.overrideDeck = nil
	}
	pp.overrideActive = false
	pp.viewport = nil
	pp.scrollX = 0
	pp.savedScrollX = 0
	pp.pendingSection = nil
	pp.pendingClick = nil
	pp.lastClickTime = time.Time{}
	pp.mouseReleased = true
	pp.frameDirty = false

	if !already && pp.mgr != nil && pp.tab != nil {
		if cur := pp.mgr.panes[pp.tab]; cur == pp {
			delete(pp.mgr.panes, pp.tab)
		}
	}
}

// ScrollY returns the current continuous vertical scroll offset in pixels.
func (pp *PreviewPane) ScrollY() int { return pp.scrollY }

// MaxScrollY returns the current maximum scroll offset in pixels.
func (pp *PreviewPane) MaxScrollY() int {
	if pp.viewport == nil {
		return 0
	}
	return pp.viewport.MaxScrollY
}

// ScrollX returns the current horizontal pan offset in pixels.
func (pp *PreviewPane) ScrollX() int { return pp.scrollX }

// MaxScrollX returns the current maximum horizontal pan offset in pixels.
func (pp *PreviewPane) MaxScrollX() int {
	if pp.viewport == nil {
		return 0
	}
	return pp.viewport.MaxScrollX
}

// ZoomPct returns the current preview zoom percentage (40..250).
func (pp *PreviewPane) ZoomPct() int { return pp.zoomPct }

// PageRange returns the currently visible 1-based page range and total page count.
func (pp *PreviewPane) PageRange() (first, last, total int) {
	if pp.viewport == nil {
		if d := pp.activeDeck(); d != nil {
			return 1, 1, d.PageCount()
		}
		return 0, 0, 0
	}
	return pp.viewport.FirstPage, pp.viewport.LastPage, pp.viewport.PageCount
}

// HasOverride reports whether PreviewPane is displaying a temporary gallery override.
func (pp *PreviewPane) HasOverride() bool { return pp.overrideActive }

// OverrideTitle returns the title of the currently displayed gallery override, if any.
func (pp *PreviewPane) OverrideTitle() string { return pp.overrideTitle }

// ShowOverrideLoading switches the preview pane into loading state for a template.
func (pp *PreviewPane) ShowOverrideLoading(title string) {
	if !pp.overrideActive {
		pp.savedScrollY = pp.scrollY
		pp.savedScrollX = pp.scrollX
	}
	if pp.overrideDeck != nil {
		pp.overrideDeck.Close()
		pp.overrideDeck = nil
	}
	pp.overrideActive = true
	pp.overrideTitle = title
	pp.overrideLoading = true
	pp.overrideErr = ""
	pp.viewport = nil
	pp.scrollY = 0
	pp.scrollX = 0
	pp.lastClickTime = time.Time{}
	pp.pendingClick = nil
	pp.frameDirty = false
	pp.clearPending = true
	if pp.mgr != nil && pp.mgr.kittyActive {
		pp.mgr.kittyDeletePending = true
	}
	pp.compileStart = time.Now()
	pp.startSpinner()
	screen.Redraw()
}

// ShowOverrideImages renders in-memory RGBA page images inside PreviewPane without
// disturbing the underlying document deck.
func (pp *PreviewPane) ShowOverrideImages(title string, imgs []*image.RGBA) {
	if !pp.overrideActive {
		pp.savedScrollY = pp.scrollY
		pp.savedScrollX = pp.scrollX
	}
	if !pp.compiling {
		pp.stopSpinner()
	}
	if pp.overrideDeck != nil {
		pp.overrideDeck.Close()
		pp.overrideDeck = nil
	}
	pp.overrideActive = true
	pp.overrideTitle = title
	pp.overrideLoading = false
	pp.overrideErr = ""
	pp.overrideDeck = NewDocumentDeckFromImages(imgs, 0)
	pp.scrollY = 0
	pp.scrollX = 0
	pp.lastClickTime = time.Time{}
	pp.pendingClick = nil
	pp.clearPending = true
	pp.rebuildViewport(true)
	screen.Redraw()
}

// ShowOverrideError displays an error message while keeping gallery override active.
func (pp *PreviewPane) ShowOverrideError(title, errMsg string) {
	if !pp.overrideActive {
		pp.savedScrollY = pp.scrollY
		pp.savedScrollX = pp.scrollX
	}
	if !pp.compiling {
		pp.stopSpinner()
	}
	if pp.overrideDeck != nil {
		pp.overrideDeck.Close()
		pp.overrideDeck = nil
	}
	pp.overrideActive = true
	pp.overrideTitle = title
	pp.overrideLoading = false
	pp.overrideErr = firstUsefulErrorLine(errMsg)
	pp.viewport = nil
	pp.lastClickTime = time.Time{}
	pp.pendingClick = nil
	pp.frameDirty = false
	pp.clearPending = true
	if pp.mgr != nil && pp.mgr.kittyActive {
		pp.mgr.kittyDeletePending = true
	}
	screen.Redraw()
}

// ClearOverride exits temporary override mode and restores the underlying document deck.
func (pp *PreviewPane) ClearOverride() {
	if !pp.overrideActive {
		return
	}
	if pp.overrideDeck != nil {
		pp.overrideDeck.Close()
		pp.overrideDeck = nil
	}
	pp.overrideActive = false
	pp.overrideTitle = ""
	pp.overrideLoading = false
	pp.overrideErr = ""
	if !pp.compiling {
		pp.stopSpinner()
	}
	pp.scrollY = pp.savedScrollY
	pp.scrollX = pp.savedScrollX
	pp.lastClickTime = time.Time{}
	pp.viewport = nil
	pp.clearPending = true
	if pp.deck != nil {
		if dstW, viewH := pp.targetPixelSize(); dstW > 0 && viewH > 0 {
			pp.deck.ComputeLayout(dstW, pp.trimMargins)
		}
		pp.rebuildViewport(true)
		pp.frameDirty = true
		pp.clearPending = true
	} else {
		if pp.mgr != nil && pp.mgr.kittyActive {
			pp.mgr.kittyDeletePending = true
		}
		pp.ScheduleCompile(true)
	}
	screen.Redraw()
}

func (pp *PreviewPane) activeDeck() *DocumentDeck {
	if pp.overrideActive {
		return pp.overrideDeck
	}
	return pp.deck
}

// IsCompiling reports whether an async compilation is currently in flight.
func (pp *PreviewPane) IsCompiling() bool { return pp.compiling || pp.overrideLoading }

// LastError returns the most recent compile error message, if any.
func (pp *PreviewPane) LastError() string {
	if pp.overrideActive {
		return pp.overrideErr
	}
	return pp.lastErr
}

// Invert reports whether dark-theme tone inversion is enabled.
func (pp *PreviewPane) Invert() bool { return pp.invert }

// TrimMargins reports whether outer page whitespace trimming is enabled.
func (pp *PreviewPane) TrimMargins() bool { return pp.trimMargins }

// ToggleInvert switches dark/light color inversion and re-renders immediately.
func (pp *PreviewPane) ToggleInvert() {
	pp.invert = !pp.invert
	if pp.activeDeck() != nil {
		pp.rebuildViewport(false)
	}
	screen.Redraw()
}

// ToggleTrimMargins switches outer whitespace margin trimming and re-renders immediately.
func (pp *PreviewPane) ToggleTrimMargins() {
	pp.trimMargins = !pp.trimMargins
	if pp.activeDeck() != nil {
		pp.preserveRatioRebuild()
	}
	screen.Redraw()
}

// ToggleSyncCursor toggles cursor-to-preview scroll synchronization on this pane.
// When enabled on a deck compiled without PDF anchors, it schedules an anchor-enabled
// recompile and immediately syncs to the active cursor line.
func (pp *PreviewPane) ToggleSyncCursor() bool {
	pp.syncCursor = !pp.syncCursor
	if pp.syncCursor {
		if pp.deck != nil && pp.deck.anchors == nil && !pp.deck.anchorsChecked && !pp.compiling && !pp.overrideActive {
			pp.ScheduleCompile(true)
		}
		pp.syncToTargetCursorNow()
	} else {
		pp.mu.Lock()
		if pp.cursorSyncTimer != nil {
			pp.cursorSyncTimer.Stop()
			pp.cursorSyncTimer = nil
		}
		pp.mu.Unlock()
	}
	screen.Redraw()
	return pp.syncCursor
}

func (pp *PreviewPane) hasFooter() bool {
	return pp.view.Height >= 3 && readGlobalBool("statusline", true)
}

func (pp *PreviewPane) imageRows() int {
	rows := pp.view.Height
	if pp.hasFooter() {
		rows--
	}
	if rows < 1 {
		return 0
	}
	return rows
}

func (pp *PreviewPane) targetViewportSize() (scaledW, viewW, viewH int) {
	c := Caps()
	cellW, cellH := c.CellW, c.CellH
	if cellW <= 0 {
		cellW = defaultCellW
	}
	if cellH <= 0 {
		cellH = defaultCellH
	}
	rows := pp.imageRows()
	if pp.view.Width <= 0 || rows <= 0 {
		return 0, 0, 0
	}
	availW := pp.view.Width * cellW
	availH := rows*cellH - 2 // stay 2px shy of footer so terminal never scrolls
	if availH < 32 {
		availH = 32
	}
	zoom := pp.zoomPct
	if zoom <= 0 {
		zoom = defaultZoomPct
	}
	scaledW = (availW * zoom) / 100
	if scaledW < 64 {
		scaledW = 64
	}
	return scaledW, availW, availH
}

func (pp *PreviewPane) targetPixelSize() (dstW, viewH int) {
	scaledW, _, availH := pp.targetViewportSize()
	return scaledW, availH
}

func (pp *PreviewPane) preserveRatioRebuild() {
	pp.pendingClick = nil
	deck := pp.activeDeck()
	if deck == nil {
		return
	}
	var ratio float64
	if pp.viewport != nil && pp.viewport.MaxScrollY > 0 {
		ratio = float64(pp.scrollY) / float64(pp.viewport.MaxScrollY)
	}
	dstW, viewH := pp.targetPixelSize()
	if dstW <= 0 || viewH <= 0 {
		return
	}
	deck.ComputeLayout(dstW, pp.trimMargins)
	newMax := deck.TotalHeight() - viewH
	if newMax < 0 {
		newMax = 0
	}
	pp.scrollY = int(ratio * float64(newMax))
	pp.rebuildViewport(true)
}

func (pp *PreviewPane) targetProtocol() GraphicsProtocol {
	if pp.protocolOverride != ProtocolNone {
		return pp.protocolOverride
	}
	return ActiveProtocol()
}

func (pp *PreviewPane) effectiveEmitProtocol() GraphicsProtocol {
	p := pp.targetProtocol()
	if p == ProtocolNone {
		return ProtocolSixel
	}
	return p
}

func (pp *PreviewPane) rebuildViewport(clearRight bool) bool {
	deck := pp.activeDeck()
	if deck == nil {
		return false
	}
	scaledW, viewW, viewH := pp.targetViewportSize()
	if scaledW <= 0 || viewW <= 0 || viewH <= 0 {
		return false
	}
	prev := pp.viewport
	vp, err := deck.RenderViewportCached(scaledW, viewW, viewH, pp.scrollY, pp.scrollX, pp.invert, pp.trimMargins, pp.targetProtocol(), prev)
	if err != nil {
		if pp.overrideActive {
			pp.overrideErr = err.Error()
		} else {
			pp.lastErr = err.Error()
		}
		return false
	}
	pp.scrollY = vp.ScrollY // strictly clamped to [0, MaxScrollY]
	pp.scrollX = vp.ScrollX // strictly clamped to [0, MaxScrollX]
	pp.viewport = vp
	pp.vpTargetW = scaledW
	pp.vpViewW = viewW
	pp.vpTargetH = viewH
	pixelChanged := !vp.Reused || prev == nil || prev.DstW != vp.DstW || prev.DstH != vp.DstH || prev.Protocol != vp.Protocol
	if clearRight || pixelChanged || pp.hadOverlayTxt || pp.lastEmitX < 0 || pp.lastEmitProto != vp.Protocol {
		pp.frameDirty = true
	}
	if clearRight || prev == nil || prev.DstW != vp.DstW || prev.DstH != vp.DstH || pp.hadOverlayTxt {
		pp.clearPending = true
	}
	return true
}

// ScrollBy shifts the continuous scroll offset by delta pixels, strictly clamped
// to [0, MaxScrollY] with zero overscroll debt. Returns true if the viewport moved.
func (pp *PreviewPane) ScrollBy(delta int) bool {
	pp.pendingSection = nil
	if pp.viewport == nil || pp.activeDeck() == nil {
		return false
	}
	nextY := pp.scrollY + delta
	if nextY < 0 {
		nextY = 0
	}
	if nextY > pp.viewport.MaxScrollY {
		nextY = pp.viewport.MaxScrollY
	}
	if nextY == pp.scrollY {
		return false
	}
	pp.scrollY = nextY
	return pp.rebuildViewport(false)
}

// ScrollXBy shifts the horizontal pan offset by delta pixels, strictly clamped
// to [0, MaxScrollX]. Returns true if the viewport panned.
func (pp *PreviewPane) ScrollXBy(delta int) bool {
	if pp.viewport == nil || pp.activeDeck() == nil {
		return false
	}
	nextX := pp.scrollX + delta
	if nextX < 0 {
		nextX = 0
	}
	if nextX > pp.viewport.MaxScrollX {
		nextX = pp.viewport.MaxScrollX
	}
	if nextX == pp.scrollX {
		return false
	}
	pp.scrollX = nextX
	return pp.rebuildViewport(false)
}

// JumpPageDelta jumps directly to the top of the next/previous page.
func (pp *PreviewPane) JumpPageDelta(delta int) bool {
	pp.pendingSection = nil
	pp.pendingClick = nil
	pp.lastClickTime = time.Time{}
	deck := pp.activeDeck()
	if pp.viewport == nil || deck == nil || deck.PageCount() == 0 {
		return false
	}
	curIdx := pp.viewport.FirstPage - 1
	if curIdx < 0 {
		curIdx = 0
	}
	targetIdx := curIdx + delta
	if targetIdx < 0 {
		targetIdx = 0
	}
	if targetIdx >= deck.PageCount() {
		targetIdx = deck.PageCount() - 1
	}
	nextY := deck.PageTop(targetIdx)
	if nextY > pp.viewport.MaxScrollY {
		nextY = pp.viewport.MaxScrollY
	}
	if nextY == pp.scrollY {
		return false
	}
	pp.scrollY = nextY
	return pp.rebuildViewport(false)
}

// JumpToPage jumps directly to the top of the 1-based page number (clamped to
// [1, PageCount]). Returns the resolved 1-based page, total page count, and
// whether a rendered deck was available.
func (pp *PreviewPane) JumpToPage(pageNum int) (int, int, bool) {
	pp.pendingSection = nil
	pp.pendingClick = nil
	pp.lastClickTime = time.Time{}
	deck := pp.activeDeck()
	if deck == nil || deck.PageCount() == 0 {
		return 0, 0, false
	}
	total := deck.PageCount()
	if pageNum < 1 {
		pageNum = 1
	}
	if pageNum > total {
		pageNum = total
	}
	dstW, viewH := pp.targetPixelSize()
	if dstW > 0 {
		deck.ComputeLayout(dstW, pp.trimMargins)
	}
	nextY := deck.PageTop(pageNum - 1)
	maxY := deck.TotalHeight() - viewH
	if maxY < 0 {
		maxY = 0
	}
	if nextY > maxY {
		nextY = maxY
	}
	if nextY < 0 {
		nextY = 0
	}
	if nextY != pp.scrollY || pp.viewport == nil {
		pp.scrollY = nextY
		if pp.rebuildViewport(false) {
			screen.Redraw()
		}
	}
	return pageNum, total, true
}

// ScrollToCursorLine smoothly scrolls the inline preview to keep the given
// 0-based editor line comfortably visible, avoiding redundant graphics redraws
// when the target coordinate already lies inside the visible band.
func (pp *PreviewPane) ScrollToCursorLine(line, totalLines int, sections []SectionEntry) bool {
	if pp.overrideActive || pp.deck == nil || pp.deck.PageCount() == 0 {
		return false
	}
	dstW, viewH := pp.targetPixelSize()
	if dstW <= 0 || viewH <= 0 {
		return false
	}
	target := SectionTarget{
		Kind:        SectionLabel,
		Line:        line,
		TotalLines:  totalLines,
		AllSections: sections,
	}
	for _, s := range sections {
		if s.Line == line {
			target.Kind = s.Kind
			target.Level = s.Level
			target.Title = s.Title
			target.Label = s.Label
			break
		}
	}
	virtualY, maxScrollY, ok := pp.deck.ResolveCursorVirtualY(target, dstW, viewH, pp.invert, pp.trimMargins)
	if !ok {
		return false
	}
	if pp.viewport != nil {
		topMargin := viewH / 8
		botMargin := (viewH * 7) / 8
		if virtualY >= pp.scrollY+topMargin && virtualY <= pp.scrollY+botMargin {
			return false
		}
	}
	nextY := clampScrollY(virtualY-(viewH/3), maxScrollY)
	if pp.viewport != nil && nextY == pp.scrollY {
		return false
	}
	pp.scrollY = nextY
	if pp.rebuildViewport(false) {
		screen.Redraw()
		return true
	}
	return false
}

func (pp *PreviewPane) syncToTargetCursorNow() {
	if pp.overrideActive || !pp.syncCursor {
		return
	}
	target := pp.resolveTarget()
	if target == nil || target.Buf == nil || target.Cursor == nil || !target.Buf.IsTypst() {
		return
	}
	var sections []SectionEntry
	if pp.mgr != nil {
		if fn := pp.mgr.getSectionsProvider(); fn != nil {
			sections = fn(target.Buf)
		}
	}
	pp.ScrollToCursorLine(target.Cursor.Loc.Y, target.Buf.LinesNum(), sections)
}

// Target returns the linked editor BufPane, if still open in this tab.
func (pp *PreviewPane) Target() *action.BufPane {
	return pp.resolveTarget()
}

// ScrollToSection repositions the continuous preview viewport to the rendered
// location of target. When a compile is still in flight (or no deck is rendered
// yet), the target is also remembered and applied as soon as the deck arrives.
// Returns true if the viewport moved immediately.
func (pp *PreviewPane) ScrollToSection(target SectionTarget) bool {
	pp.lastClickTime = time.Time{}
	cp := target
	if len(target.AllSections) > 0 {
		cp.AllSections = append([]SectionEntry(nil), target.AllSections...)
	}

	targetPane := pp.resolveTarget()
	deckMatchesTarget := pp.deck != nil &&
		(targetPane == nil || targetPane.Buf == nil || pp.lastShared == nil || pp.lastShared == targetPane.Buf.SharedBuffer)
	if !deckMatchesTarget && pp.deck != nil && targetPane != nil && targetPane.Buf != nil && pp.lastCompiledMain != "" && pp.mgr != nil {
		effMain := pp.mgr.resolveMainFile(targetPane.Buf.AbsPath)
		if effMain != "" && canonicalPreviewPath(effMain) == canonicalPreviewPath(pp.lastCompiledMain) {
			deckMatchesTarget = true
		}
	}

	if !deckMatchesTarget {
		pp.pendingSection = &cp
		return false
	}
	if pp.compiling {
		pp.pendingSection = &cp
	} else if pp.deck != nil && pp.deck.anchors == nil && !pp.deck.anchorsChecked && !pp.overrideActive {
		// Deck was compiled with SkipAnchors=true; jump proportionally now and
		// trigger a background anchor-enabled compile to snap to the exact PDF anchor.
		pp.pendingSection = &cp
		pp.ScheduleCompile(true)
	} else {
		pp.pendingSection = nil
	}

	dstW, viewH := pp.targetPixelSize()
	if dstW <= 0 || viewH <= 0 {
		pp.pendingSection = &cp
		return false
	}

	nextY := pp.deck.ResolveSectionScrollY(cp, dstW, viewH, pp.invert, pp.trimMargins)
	if pp.viewport != nil && nextY == pp.scrollY {
		return false
	}
	pp.scrollY = nextY
	if pp.rebuildViewport(false) {
		screen.Redraw()
		return true
	}
	return false
}

func (pp *PreviewPane) resolveTarget() *action.BufPane {
	if pp.tab == nil {
		return nil
	}
	if pp.target != nil {
		for _, p := range pp.tab.Panes {
			if p == pp.target {
				return pp.target
			}
		}
		pp.target = nil
	}
	for _, p := range pp.tab.Panes {
		if bp, ok := p.(*action.BufPane); ok && bp.Buf != nil && bp.Buf.IsTypst() {
			pp.target = bp
			return bp
		}
	}
	return nil
}

func (pp *PreviewPane) returnFocus() {
	if pp.tab == nil {
		return
	}
	target := pp.resolveTarget()
	if target != nil {
		for i, p := range pp.tab.Panes {
			if p == target {
				pp.tab.SetActive(i)
				return
			}
		}
	}
	for i, p := range pp.tab.Panes {
		if !action.IsAuxiliaryPane(p) {
			pp.tab.SetActive(i)
			return
		}
	}
}

func (pp *PreviewPane) startSpinner() {
	if pp.spinStop != nil {
		return
	}
	stop := make(chan struct{})
	pp.spinStop = stop
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				pp.spinTick.Add(1)
				screen.Redraw()
			}
		}
	}()
}

func (pp *PreviewPane) stopSpinner() {
	if pp.spinStop != nil {
		close(pp.spinStop)
		pp.spinStop = nil
	}
}

// ScheduleCompile schedules a debounced background compile of the target Typst buffer.
func (pp *PreviewPane) ScheduleCompile(immediate bool) {
	if pp.overrideActive {
		return
	}
	pp.mu.Lock()
	if pp.closed {
		pp.mu.Unlock()
		return
	}
	if pp.debounceTimer != nil {
		pp.debounceTimer.Stop()
		pp.debounceTimer = nil
	}
	if immediate {
		pp.mu.Unlock()
		pp.startCompileNow()
		return
	}
	delayMs := readGlobalInt("typst.debounceMs", 150)
	if delayMs < minEditDebounceMs {
		delayMs = minEditDebounceMs
	}
	gen := pp.gen
	pp.debounceTimer = time.AfterFunc(time.Duration(delayMs)*time.Millisecond, func() {
		action.EnqueueUI(func() {
			pp.mu.Lock()
			stale := pp.closed || pp.gen != gen
			pp.debounceTimer = nil
			pp.mu.Unlock()
			if !stale {
				pp.startCompileNow()
			}
		})
	})
	pp.mu.Unlock()
}

func (pp *PreviewPane) startCompileNow() {
	target := pp.resolveTarget()
	if target == nil || target.Buf == nil || !target.Buf.IsTypst() {
		return
	}

	b := target.Buf
	bufAbsPath := ""
	if b.Path != "" && !b.Type.Scratch {
		bufAbsPath = b.AbsPath
		if bufAbsPath == "" {
			if a, err := filepath.Abs(b.Path); err == nil {
				bufAbsPath = a
			}
		}
	}

	absPath := bufAbsPath
	mainPath := ""
	if pp.mgr != nil && bufAbsPath != "" {
		mainPath = pp.mgr.resolveMainFile(bufAbsPath)
	}

	pinnedOtherFile := mainPath != "" && canonicalPreviewPath(mainPath) != canonicalPreviewPath(bufAbsPath)
	var modified bool
	var content []byte
	if pinnedOtherFile {
		absPath = mainPath
		modified = false
		pp.pinnedSubfileDirty = b.Modified()
	} else {
		if mainPath != "" {
			absPath = mainPath
		}
		modified = b.Modified() || absPath == ""
		if modified {
			raw := b.Bytes()
			content = make([]byte, len(raw))
			copy(content, raw)
		}
		pp.pinnedSubfileDirty = false
	}

	rootDir := ""
	if pp.mgr != nil && absPath != "" {
		if fn := pp.mgr.getRootResolver(); fn != nil {
			rootDir = fn(absPath)
		}
	}
	if rootDir == "" && absPath != "" {
		rootDir = filepath.Dir(absPath)
	}

	ppi := readGlobalInt("typst.previewPPI", 144)
	if ppi < 72 {
		ppi = 144
	}
	if ppi > 300 {
		ppi = 300
	}

	needAnchors := pp.pendingSection != nil || pp.pendingClick != nil || pp.syncCursor
	if !needAnchors && pp.mgr != nil {
		if fn := pp.mgr.getAnchorNeedChecker(); fn != nil && fn(pp.tab) {
			needAnchors = true
		}
	}

	req := CompileRequest{
		FilePath:    absPath,
		RootDir:     rootDir,
		Content:     content,
		Modified:    modified,
		PPI:         ppi,
		SkipAnchors: !needAnchors,
	}

	compiler := pp.mgr.getCompiler()
	if compiler == nil {
		pp.lastErr = "preview compiler unavailable"
		screen.Redraw()
		return
	}

	pp.mu.Lock()
	if pp.closed {
		pp.mu.Unlock()
		return
	}
	if pp.compileCancel != nil {
		pp.compileCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	pp.compileCancel = cancel
	pp.gen++
	gen := pp.gen
	pp.mu.Unlock()

	pp.lastVersion = b.Version()
	pp.lastShared = b.SharedBuffer
	pp.lastPath = bufAbsPath
	pp.lastCompiledMain = absPath
	pp.compiling = true
	pp.compilingWithAnchors = needAnchors
	pp.compileStart = time.Now()
	pp.startSpinner()
	screen.Redraw()

	go func() {
		deck, err := compiler.CompilePages(ctx, req)
		canceled := ctx.Err() == context.Canceled
		cancel()

		action.EnqueueUI(func() {
			pp.mu.Lock()
			stale := pp.closed || pp.gen != gen
			pp.mu.Unlock()
			if stale {
				if deck != nil {
					deck.Close()
				}
				return
			}

			pp.compiling = false
			pp.compilingWithAnchors = false
			pp.stopSpinner()

			if err != nil {
				if deck != nil {
					deck.Close()
				}
				if canceled {
					return
				}
				pp.pendingClick = nil
				pp.lastErr = firstUsefulErrorLine(err.Error())
				screen.Redraw()
				return
			}

			if pp.overrideActive {
				if needAnchors {
					deck.anchorsChecked = true
				}
				oldDeck := pp.deck
				pp.deck = deck
				pp.lastErr = ""
				if oldDeck != nil {
					deck.CarryForwardFrom(oldDeck)
					oldDeck.Close()
				}
				return
			}
			if needAnchors {
				deck.anchorsChecked = true
			}
			oldDeck := pp.deck
			pp.deck = deck
			pp.lastErr = ""
			if oldDeck != nil {
				deck.CarryForwardFrom(oldDeck)
				oldDeck.Close()
			}
			if pp.pendingSection != nil {
				sec := *pp.pendingSection
				pp.pendingSection = nil
				if dstW, viewH := pp.targetPixelSize(); dstW > 0 && viewH > 0 {
					pp.scrollY = pp.deck.ResolveSectionScrollY(sec, dstW, viewH, pp.invert, pp.trimMargins)
				}
			}
			// Re-render viewport at current scrollY/scrollX (clamped safely to new doc size).
			pp.rebuildViewport(false)
			if pp.pendingClick != nil {
				pc := *pp.pendingClick
				pp.pendingClick = nil
				pp.applyPendingClickJump(pc)
			}
			screen.Redraw()
		})
	}()
}

func (pp *PreviewPane) resolveClickWithLayoutSync(virtY int, sections []SectionEntry, totalLines int) int {
	if pp.deck == nil {
		return 0
	}
	dstW := pp.deck.layoutW
	if dstW <= 0 && len(pp.deck.pages) > 0 && pp.deck.pages[0] != nil && pp.deck.pages[0].srcW > 0 {
		dstW = pp.deck.pages[0].srcW
	}
	if dstW < 16 {
		dstW = 16
	}
	preTops, preHeights, preTotalH := snapshotDeckLayout(pp.deck, dstW, pp.deck.layoutTrim)
	line := pp.deck.ResolveClickToSourceLine(virtY, sections, totalLines)
	if layoutShifted(preTops, preHeights, preTotalH, pp.deck.pageTops, pp.deck.pageHeights, pp.deck.totalHeight) {
		pp.scrollY = remapVirtualYAcrossLayout(pp.scrollY, preTops, preHeights, preTotalH, pp.deck.pageTops, pp.deck.pageHeights, pp.deck.totalHeight)
		pp.rebuildViewport(false)
	}
	return line
}

func (pp *PreviewPane) jumpToSourceAtVirtualY(virtY int) {
	target := pp.resolveTarget()
	if target == nil || target.Buf == nil || pp.deck == nil {
		return
	}
	var sections []SectionEntry
	if pp.mgr != nil {
		if fn := pp.mgr.getSectionsProvider(); fn != nil {
			sections = fn(target.Buf)
		}
	}
	line := pp.resolveClickWithLayoutSync(virtY, sections, target.Buf.LinesNum())
	moveTargetCursorToLine(target, line)
	pp.returnFocus()
	screen.Redraw()

	if pp.deck.anchors == nil && !pp.deck.anchorsChecked && !pp.overrideActive {
		pp.pendingClick = &pendingClickTarget{
			virtualY:   virtY,
			shared:     target.Buf.SharedBuffer,
			version:    target.Buf.Version(),
			jumpedLine: line,
		}
		if !pp.compiling || !pp.compilingWithAnchors {
			pp.ScheduleCompile(true)
		}
	} else {
		pp.pendingClick = nil
	}
}

func (pp *PreviewPane) applyPendingClickJump(pc pendingClickTarget) {
	if pp.overrideActive || pp.deck == nil || pp.deck.anchors == nil {
		return
	}
	target := pp.resolveTarget()
	if target == nil || target.Buf == nil || target.Buf.SharedBuffer != pc.shared {
		return
	}
	if target.Buf.Version() != pc.version {
		return
	}
	if target.Cursor == nil || target.Cursor.Loc.Y != pc.jumpedLine {
		return
	}
	var sections []SectionEntry
	if pp.mgr != nil {
		if fn := pp.mgr.getSectionsProvider(); fn != nil {
			sections = fn(target.Buf)
		}
	}
	refinedLine := pp.resolveClickWithLayoutSync(pc.virtualY, sections, target.Buf.LinesNum())
	if refinedLine != pc.jumpedLine {
		moveTargetCursorToLine(target, refinedLine)
	}
}

func moveTargetCursorToLine(target *action.BufPane, line int) {
	if target == nil || target.Buf == nil {
		return
	}
	n := target.Buf.LinesNum()
	if n <= 0 {
		return
	}
	if line < 0 {
		line = 0
	} else if line >= n {
		line = n - 1
	}
	target.Buf.ClearCursors()
	c := target.Buf.GetActiveCursor()
	if c == nil {
		return
	}
	c.ResetSelection()
	c.GotoLoc(buffer.Loc{X: 0, Y: line})
	target.Cursor = c
	target.Relocate()
	target.Center()
}

func firstUsefulErrorLine(raw string) string {
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return "compile failed"
}

// Display draws the preview pane's statusline (and loading/error text when no
// Sixel image is displayed yet). When a Sixel image is active, the image area
// cells are deliberately left untouched so tcell does not overwrite the bitmap.
func (pp *PreviewPane) Display() {
	if screen.Screen == nil || pp.view.Width <= 0 || pp.view.Height <= 0 {
		return
	}

	imgRows := pp.imageRows()

	// While the user drags a split border, clear any visible bitmap once and show
	// a lightweight placeholder so resizing is lag-free and never ghosts images.
	if pp.isTabResizing() {
		if pp.lastEmitX >= 0 {
			if pp.mgr != nil {
				if pp.lastEmitProto == ProtocolKitty || pp.mgr.kittyActive {
					pp.mgr.kittyDeletePending = true
				} else {
					pp.mgr.pendingSync = true
				}
			}
			pp.lastEmitX = -1
			pp.lastEmitY = -1
			pp.lastEmitProto = ProtocolNone
		}
		pp.frameDirty = false
		if imgRows > 0 {
			pp.hadOverlayTxt = true
			pp.drawResizePlaceholder(imgRows)
		}
		if pp.hasFooter() {
			pp.drawFooter(pp.view.Y + pp.view.Height - 1)
		}
		return
	}

	// Detect layout shift (resize, tab bar appear/disappear, divider drag end, sidebar dock).
	if pp.lastEmitX != pp.view.X || pp.lastEmitY != pp.view.Y ||
		pp.lastEmitW != pp.view.Width || pp.lastEmitH != pp.view.Height {
		if pp.lastEmitX >= 0 && pp.mgr != nil {
			if pp.lastEmitProto == ProtocolKitty || pp.mgr.kittyActive {
				// Kitty graphics deletes the previous image ID atomically inside
				// the next synchronized update without needing a full-screen ED 2 wipe.
				pp.mgr.kittyDeletePending = true
			} else {
				// Force a synchronized full-screen erase + tcell sync so ConPTY /
				// terminals wipe stale Sixel pixels in any newly exposed area before
				// re-emitting the relocated preview.
				pp.mgr.pendingSync = true
			}
		}
		if pp.activeDeck() != nil {
			scaledW, viewW, viewH := pp.targetViewportSize()
			if pp.viewport == nil || pp.vpTargetW != scaledW || pp.vpViewW != viewW || pp.vpTargetH != viewH {
				pp.preserveRatioRebuild()
			}
		}
		pp.frameDirty = true
		pp.clearPending = true
	}

	// Detect live protocol switch (for example via `> set typst.previewProtocol`).
	if pp.viewport != nil && pp.viewport.Protocol != pp.effectiveEmitProtocol() {
		if pp.mgr != nil {
			if pp.lastEmitProto == ProtocolKitty || pp.mgr.kittyActive {
				pp.mgr.kittyDeletePending = true
			} else if pp.lastEmitX >= 0 {
				pp.mgr.pendingSync = true
			}
		}
		pp.rebuildViewport(true)
	}

	// Only write cell text into the image area when we have NO rendered frame yet.
	if pp.viewport == nil && imgRows > 0 {
		if pp.mgr != nil && pp.mgr.kittyActive {
			pp.mgr.kittyDeletePending = true
		}
		pp.hadOverlayTxt = true
		pp.drawPlaceholder(imgRows)
	} else if pp.hadOverlayTxt && pp.viewport != nil {
		// Previous frame had placeholder text; screen.Fill already cleared it to spaces.
		// Mark clearPending so postShow cleanly emits the image over the blank cells.
		pp.hadOverlayTxt = false
		pp.frameDirty = true
		pp.clearPending = true
	}

	if pp.hasFooter() {
		pp.drawFooter(pp.view.Y + pp.view.Height - 1)
	}
}

func (pp *PreviewPane) drawPlaceholder(imgRows int) {
	w := pp.view.Width
	centerY := pp.view.Y + imgRows/2

	var msg string
	var sub string
	if pp.overrideActive {
		if pp.overrideLoading {
			spin := spinnerFrames[int(pp.spinTick.Load())%len(spinnerFrames)]
			msg = fmt.Sprintf("%s Loading preview for %s...", spin, pp.overrideTitle)
		} else if pp.overrideErr != "" {
			msg = "Template preview unavailable:"
			sub = pp.overrideErr
		} else {
			msg = fmt.Sprintf("Template: %s", pp.overrideTitle)
		}
	} else if pp.compiling {
		spin := spinnerFrames[int(pp.spinTick.Load())%len(spinnerFrames)]
		elapsed := ""
		if d := time.Since(pp.compileStart); d >= 200*time.Millisecond {
			elapsed = fmt.Sprintf(" (%.1fs)", d.Seconds())
		}
		msg = fmt.Sprintf("%s Rendering preview%s...", spin, elapsed)
	} else if pp.lastErr != "" {
		msg = "Preview compile error:"
		sub = pp.lastErr
	} else {
		msg = "Waiting for Typst document..."
	}

	drawCentered := func(y int, text string, st tcell.Style) {
		if y < pp.view.Y || y >= pp.view.Y+imgRows {
			return
		}
		rw := runewidth.StringWidth(text)
		startX := pp.view.X
		if rw < w {
			startX += (w - rw) / 2
		}
		col := startX
		maxX := pp.view.X + w
		for _, r := range text {
			cw := runewidth.RuneWidth(r)
			if cw <= 0 {
				cw = 1
			}
			if col+cw > maxX {
				break
			}
			screen.SetContent(col, y, r, nil, st)
			col += cw
		}
	}

	stMain := config.GetColor("statusline")
	if stMain == config.DefStyle {
		stMain = config.DefStyle.Bold(true)
	} else {
		stMain = config.DefStyle.Bold(true)
	}
	drawCentered(centerY, msg, stMain)
	if sub != "" {
		errStyle := config.GetColor("gutter-error")
		if errStyle == config.DefStyle {
			errStyle = config.DefStyle.Foreground(tcell.ColorRed)
		}
		drawCentered(centerY+2, sub, errStyle)
	}
}

func (pp *PreviewPane) drawResizePlaceholder(imgRows int) {
	w := pp.view.Width
	centerY := pp.view.Y + imgRows/2

	drawCentered := func(y int, text string, st tcell.Style) {
		if y < pp.view.Y || y >= pp.view.Y+imgRows {
			return
		}
		rw := runewidth.StringWidth(text)
		startX := pp.view.X
		if rw < w {
			startX += (w - rw) / 2
		}
		col := startX
		maxX := pp.view.X + w
		for _, r := range text {
			cw := runewidth.RuneWidth(r)
			if cw <= 0 {
				cw = 1
			}
			if col+cw > maxX {
				break
			}
			screen.SetContent(col, y, r, nil, st)
			col += cw
		}
	}

	drawCentered(centerY, "Resizing preview...", config.DefStyle.Bold(true))
	dimStyle := config.GetColor("comment")
	if dimStyle == config.DefStyle {
		dimStyle = config.DefStyle
	}
	drawCentered(centerY+1, fmt.Sprintf("%d cols", w), dimStyle)
}

func (pp *PreviewPane) drawFooter(y int) {
	style := config.GetColor("statusline")
	if !pp.active {
		if inactive := config.GetColor("statusline.inactive"); inactive != config.DefStyle {
			style = inactive
		}
	} else {
		style = style.Bold(true)
	}

	var left string
	prefix := "Preview"
	if pp.active {
		prefix = "*Preview"
	}

	switch {
	case pp.overrideActive:
		switch {
		case pp.overrideLoading:
			spin := spinnerFrames[int(pp.spinTick.Load())%len(spinnerFrames)]
			left = fmt.Sprintf(" Template: %s | %s loading...", pp.overrideTitle, spin)
		case pp.overrideErr != "":
			left = fmt.Sprintf(" Template: %s | ERR: %s", pp.overrideTitle, pp.overrideErr)
		case pp.viewport != nil:
			left = fmt.Sprintf(" Template: %s | %s", pp.overrideTitle, pp.pageLabel())
		default:
			left = fmt.Sprintf(" Template: %s", pp.overrideTitle)
		}
	case pp.compiling:
		spin := spinnerFrames[int(pp.spinTick.Load())%len(spinnerFrames)]
		if pp.viewport != nil {
			left = fmt.Sprintf(" %s | %s compiling... | %s", prefix, spin, pp.pageLabel())
		} else {
			left = fmt.Sprintf(" %s | %s compiling...", prefix, spin)
		}
	case pp.lastErr != "":
		if pp.viewport != nil {
			left = fmt.Sprintf(" %s | ERR (stale) | %s", prefix, pp.pageLabel())
		} else {
			left = fmt.Sprintf(" %s | ERR: %s", prefix, pp.lastErr)
		}
	case pp.viewport != nil:
		pct := 0
		if pp.viewport.MaxScrollY > 0 {
			pct = (pp.viewport.ScrollY * 100) / pp.viewport.MaxScrollY
		}
		flags := ""
		if pp.zoomPct > 0 && pp.zoomPct != defaultZoomPct {
			flags += fmt.Sprintf(" z:%d%%", pp.zoomPct)
		}
		if pp.invert {
			flags += " dark"
		}
		if pp.trimMargins {
			flags += " trim"
		}
		if pp.syncCursor {
			flags += " sync"
		}
		if pp.pinnedSubfileDirty {
			flags += " (save to update)"
		}
		base := fmt.Sprintf(" %s | %s (%d%%)%s", prefix, pp.pageLabel(), pct, flags)
		left = fitPreviewFooter(base, pp.view.Width)
	default:
		left = fmt.Sprintf(" %s", prefix)
	}

	w := pp.view.Width
	col := pp.view.X
	maxX := pp.view.X + w
	truncated := runewidth.StringWidth(left) > w && w >= 2
	limitX := maxX
	if truncated {
		limitX = maxX - 1
	}
	for _, r := range left {
		cw := runewidth.RuneWidth(r)
		if cw <= 0 {
			cw = 1
		}
		if col+cw > limitX {
			break
		}
		screen.SetContent(col, y, r, nil, style)
		col += cw
	}
	if truncated && col < maxX {
		screen.SetContent(col, y, '…', nil, style)
		col++
	}
	for col < maxX {
		screen.SetContent(col, y, ' ', nil, style)
		col++
	}
}

func fitPreviewFooter(base string, width int) string {
	candidates := []string{
		base + " | [Wheel/w/s] scroll [t] dark [m] trim [q] close",
		base + " | [t] dark [m] trim [q] close",
		base + " | [t/m/q]",
		base,
	}
	for _, c := range candidates {
		if runewidth.StringWidth(c) <= width {
			return c
		}
	}
	return base
}

func (pp *PreviewPane) pageLabel() string {
	if pp.viewport == nil {
		return ""
	}
	if pp.viewport.LastPage != pp.viewport.FirstPage {
		return fmt.Sprintf("p.%d-%d/%d", pp.viewport.FirstPage, pp.viewport.LastPage, pp.viewport.PageCount)
	}
	return fmt.Sprintf("p.%d/%d", pp.viewport.FirstPage, pp.viewport.PageCount)
}

// emitFrameIfDirty writes the encoded Kitty or Sixel sequence after screen.Show().
// When wrapSync is true, the sequence is wrapped in DEC Mode 2026 synchronized
// output markers; callers already inside a synchronized frame pass false.
func (pp *PreviewPane) emitFrameIfDirty(out io.Writer, wrapSync bool) {
	if !pp.frameDirty || pp.viewport == nil || len(pp.viewport.Payload) == 0 {
		return
	}
	if pp.view.Width <= 0 || pp.imageRows() <= 0 {
		return
	}

	pp.frameDirty = false
	clearRight := pp.clearPending
	pp.clearPending = false
	pp.lastEmitX = pp.view.X
	pp.lastEmitY = pp.view.Y
	pp.lastEmitW = pp.view.Width
	pp.lastEmitH = pp.view.Height
	pp.lastEmitProto = pp.viewport.Protocol

	if out == nil {
		return
	}

	topRow := pp.view.Y + 1  // 1-based ANSI row
	leftCol := pp.view.X + 1 // 1-based ANSI column
	imgRows := pp.imageRows()

	var buf bytes.Buffer
	buf.Grow(len(pp.viewport.Payload) + 256)
	if wrapSync {
		buf.WriteString("\x1b[?2026h") // begin synchronized update
	}
	buf.WriteString("\x1b7")     // DECSC: save editor cursor & SGR state
	buf.WriteString("\x1b[?25l") // hide cursor during graphics draw
	if pp.viewport.Protocol == ProtocolKitty {
		if pp.mgr != nil && (pp.mgr.kittyActive || pp.mgr.kittyDeletePending) {
			buf.WriteString(kittyDeleteSequence(UseTmuxPassthrough()))
		}
		fmt.Fprintf(&buf, "\x1b[%d;%dH", topRow, leftCol)
		buf.Write(pp.viewport.Payload)
		if pp.mgr != nil {
			pp.mgr.kittyActive = true
			pp.mgr.kittyDeletePending = false
		}
	} else {
		if pp.mgr != nil && (pp.mgr.kittyActive || pp.mgr.kittyDeletePending) {
			buf.WriteString(kittyDeleteSequence(UseTmuxPassthrough()))
			pp.mgr.kittyActive = false
			pp.mgr.kittyDeletePending = false
		}
		buf.WriteString("\x1b[0m") // reset SGR so clearing spaces use default bg
		if clearRight {
			blank := strings.Repeat(" ", pp.view.Width)
			for r := 0; r < imgRows; r++ {
				fmt.Fprintf(&buf, "\x1b[%d;%dH%s", topRow+r, leftCol, blank)
			}
		}
		fmt.Fprintf(&buf, "\x1b[%d;%dH", topRow, leftCol)
		buf.Write(pp.viewport.Payload)
	}
	buf.WriteString("\x1b8") // DECRC: restore cursor to editor position
	if !pp.active && !screen.UseFake() {
		buf.WriteString("\x1b[?25h")
	}
	if wrapSync {
		buf.WriteString("\x1b[?2026l") // end synchronized update
	}

	EnableVTOutput()
	_, _ = out.Write(buf.Bytes())
}

// HandleCommand executes Ex-style commands while PreviewPane is focused.
func (pp *PreviewPane) HandleCommand(text string) {
	if target := pp.resolveTarget(); target != nil {
		target.HandleCommand(text)
	}
}

// HandleEvent processes mouse wheel scrolling, click-to-source, and keyboard navigation on PreviewPane.
func (pp *PreviewPane) HandleEvent(event tcell.Event) {
	switch e := event.(type) {
	case *tcell.EventMouse:
		btns := e.Buttons()
		if btns&tcell.WheelDown != 0 {
			if pp.ScrollBy(scrollStepPx) {
				screen.Redraw()
			}
			return
		}
		if btns&tcell.WheelUp != 0 {
			if pp.ScrollBy(-scrollStepPx) {
				screen.Redraw()
			}
			return
		}
		if btns&tcell.WheelRight != 0 {
			if pp.ScrollXBy(panStepPx) {
				screen.Redraw()
			}
			return
		}
		if btns&tcell.WheelLeft != 0 {
			if pp.ScrollXBy(-panStepPx) {
				screen.Redraw()
			}
			return
		}
		if btns == tcell.ButtonNone {
			pp.mouseReleased = true
			return
		}
		if btns&tcell.Button1 != 0 {
			if !pp.mouseReleased {
				return
			}
			pp.mouseReleased = false

			mx, my := e.Position()
			imgRows := pp.imageRows()
			if pp.overrideActive || pp.viewport == nil || pp.deck == nil || imgRows <= 0 ||
				mx < pp.view.X || mx >= pp.view.X+pp.view.Width ||
				my < pp.view.Y || my >= pp.view.Y+imgRows {
				pp.lastClickTime = time.Time{}
				return
			}

			now := time.Now()
			isCtrl := e.Modifiers()&tcell.ModCtrl != 0
			dx := mx - pp.lastClickX
			if dx < 0 {
				dx = -dx
			}
			dy := my - pp.lastClickY
			if dy < 0 {
				dy = -dy
			}
			isDouble := !pp.lastClickTime.IsZero() &&
				now.Sub(pp.lastClickTime) <= doubleClickMaxWait &&
				dx <= doubleClickMaxCellDist && dy <= doubleClickMaxCellDist

			if !isCtrl && !isDouble {
				pp.lastClickTime = now
				pp.lastClickX = mx
				pp.lastClickY = my
				return
			}

			pp.lastClickTime = time.Time{}
			cellH := Caps().CellH
			if cellH <= 0 {
				cellH = defaultCellH
			}
			localClickYpx := (my-pp.view.Y)*cellH + cellH/2
			virtY := pp.scrollY + localClickYpx
			pp.jumpToSourceAtVirtualY(virtY)
			return
		}
		pp.lastClickTime = time.Time{}

	case *tcell.EventKey:
		// Alt+P closes the preview even while focused.
		if e.Key() == tcell.KeyRune && (e.Rune() == 'p' || e.Rune() == 'P') && e.Modifiers()&tcell.ModAlt != 0 {
			if pp.mgr != nil && pp.tab != nil {
				pp.mgr.CloseForTab(pp.tab)
			}
			return
		}

		switch e.Key() {
		case tcell.KeyEscape, tcell.KeyEnter:
			pp.returnFocus()
			return
		case tcell.KeyCtrlW, tcell.KeyTab:
			pp.returnFocus()
			return
		case tcell.KeyCtrlE:
			if action.InfoBar != nil {
				action.InfoBar.Prompt("> ", "", "Command", nil, func(resp string, canceled bool) {
					if !canceled {
						pp.HandleCommand(resp)
					}
				})
			}
			return
		case tcell.KeyDown:
			pp.ScrollBy(scrollStepPx)
			return
		case tcell.KeyUp:
			pp.ScrollBy(-scrollStepPx)
			return
		case tcell.KeyPgDn, tcell.KeyCtrlD:
			_, viewH := pp.targetPixelSize()
			step := (viewH * 3) / 4
			if step < scrollStepPx {
				step = scrollStepPx
			}
			pp.ScrollBy(step)
			return
		case tcell.KeyPgUp, tcell.KeyCtrlU:
			_, viewH := pp.targetPixelSize()
			step := (viewH * 3) / 4
			if step < scrollStepPx {
				step = scrollStepPx
			}
			pp.ScrollBy(-step)
			return
		case tcell.KeyHome:
			pp.pendingSection = nil
			if pp.scrollY != 0 {
				pp.scrollY = 0
				pp.rebuildViewport(false)
			}
			return
		case tcell.KeyEnd:
			pp.pendingSection = nil
			if pp.viewport != nil && pp.scrollY != pp.viewport.MaxScrollY {
				pp.scrollY = pp.viewport.MaxScrollY
				pp.rebuildViewport(false)
			}
			return
		case tcell.KeyRight:
			if pp.zoomPct > defaultZoomPct && pp.MaxScrollX() > 0 {
				pp.ScrollXBy(panStepPx)
			} else {
				pp.JumpPageDelta(1)
			}
			return
		case tcell.KeyLeft:
			if pp.zoomPct > defaultZoomPct && pp.MaxScrollX() > 0 {
				pp.ScrollXBy(-panStepPx)
			} else {
				pp.JumpPageDelta(-1)
			}
			return
		case tcell.KeyRune:
			switch e.Rune() {
			case 'q', 'Q':
				if pp.mgr != nil && pp.tab != nil {
					pp.mgr.CloseForTab(pp.tab)
				}
			case 'j', 's', 'S':
				pp.ScrollBy(scrollStepPx)
			case 'k', 'w', 'W':
				pp.ScrollBy(-scrollStepPx)
			case 'h':
				if pp.MaxScrollX() > 0 {
					pp.ScrollXBy(-panStepPx)
				}
			case 'l':
				if pp.MaxScrollX() > 0 {
					pp.ScrollXBy(panStepPx)
				}
			case ' ':
				_, viewH := pp.targetPixelSize()
				step := (viewH * 3) / 4
				if step < scrollStepPx {
					step = scrollStepPx
				}
				pp.ScrollBy(step)
			case 'g':
				pp.pendingSection = nil
				if pp.scrollY != 0 {
					pp.scrollY = 0
					pp.rebuildViewport(false)
				}
			case 'G':
				pp.pendingSection = nil
				if pp.viewport != nil && pp.scrollY != pp.viewport.MaxScrollY {
					pp.scrollY = pp.viewport.MaxScrollY
					pp.rebuildViewport(false)
				}
			case ']':
				pp.JumpPageDelta(1)
			case '[':
				pp.JumpPageDelta(-1)
			case '+', '=':
				if pp.zoomPct < maxZoomPct {
					pp.zoomPct += zoomStepPct
					if pp.zoomPct > maxZoomPct {
						pp.zoomPct = maxZoomPct
					}
					pp.preserveRatioRebuild()
				}
			case '-', '_':
				if pp.zoomPct > minZoomPct {
					pp.zoomPct -= zoomStepPct
					if pp.zoomPct < minZoomPct {
						pp.zoomPct = minZoomPct
					}
					pp.preserveRatioRebuild()
				}
			case '0':
				if pp.zoomPct != defaultZoomPct || pp.scrollX != 0 {
					pp.zoomPct = defaultZoomPct
					pp.scrollX = 0
					pp.preserveRatioRebuild()
				}
			case 't', 'T':
				pp.ToggleInvert()
			case 'm', 'M':
				pp.ToggleTrimMargins()
			case 'f', 'F':
				pp.ToggleSyncCursor()
			case 'r', 'R':
				pp.frameDirty = true
				pp.clearPending = true
				if pp.overrideActive {
					pp.rebuildViewport(true)
					screen.Redraw()
				} else {
					pp.ScheduleCompile(true)
				}
			case '<':
				pp.ResizePaneBy(resizeStepCols)
			case '>':
				pp.ResizePaneBy(-resizeStepCols)
			}
		}
	}
}

// ResizePaneBy adjusts the docked preview pane's width by delta columns
// (positive widens preview, negative narrows preview), clamping so both the
// preview and its adjacent editor split retain usable widths.
func (pp *PreviewPane) ResizePaneBy(delta int) bool {
	if delta == 0 || pp.tab == nil || pp.tab.Node == nil {
		return false
	}
	parent, idx := findNodeParent(pp.tab.Node, pp.ID())
	if parent == nil || parent.Kind != views.STHoriz || idx <= 0 {
		return false
	}
	children := parent.Children()
	left := children[idx-1]
	cur := children[idx]
	totW := left.W + cur.W

	minPrev := MinPreviewWidth + 1 // +1 column reserved for the vertical split divider
	minLeft := MinEditorWidth
	if left.X > 0 {
		minLeft++
	}
	if totW < minPrev+minLeft {
		return false
	}

	nextPrevW := cur.W + delta
	if nextPrevW < minPrev {
		nextPrevW = minPrev
	}
	if nextPrevW > totW-minLeft {
		nextPrevW = totW - minLeft
	}
	if nextPrevW == cur.W {
		return false
	}
	if !left.ResizeSplit(totW - nextPrevW) {
		return false
	}
	pp.tab.Resize()
	return true
}

func findNodeParent(n *views.Node, id uint64) (*views.Node, int) {
	if n == nil || id == 0 {
		return nil, -1
	}
	for i, c := range n.Children() {
		if c.IsLeaf() && c.ID() == id {
			return n, i
		}
		if p, idx := findNodeParent(c, id); p != nil {
			return p, idx
		}
	}
	return nil, -1
}

// MainFileResolverFunc resolves an optional pinned main .typ path for the
// given buffer path. Returning "" uses the buffer's own path.
type MainFileResolverFunc func(bufPath string) string

// AnchorNeedCheckerFunc reports whether tab currently needs PDF anchors
// (for example, when the Document Outline sidebar is open in that tab).
type AnchorNeedCheckerFunc func(tab *action.Tab) bool

// SectionsProviderFunc extracts ordered document sections for cursor-to-preview
// scroll synchronization without coupling preview to the outline package.
type SectionsProviderFunc func(b *buffer.Buffer) []SectionEntry

// Manager coordinates per-tab docked PreviewPanes, compiler wiring, buffer
// observers, resize handling, and post-show inline graphics emission.
type Manager struct {
	buffer.NopObserver

	panes              map[*action.Tab]*PreviewPane
	autoHidden         map[*action.Tab]bool
	pendingSync        bool
	kittyActive        bool
	kittyDeletePending bool

	cfgMu             sync.RWMutex
	compiler          Compiler
	rootResolver      RootResolverFunc
	mainFileResolver  MainFileResolverFunc
	anchorNeedChecker AnchorNeedCheckerFunc
	sectionsProvider  SectionsProviderFunc
	output            io.Writer // nil = auto (os.Stdout when not SimulationScreen)
}

var (
	defaultManager = newManager()
	initOnce       sync.Once
)

func newManager() *Manager {
	return &Manager{
		panes:      make(map[*action.Tab]*PreviewPane),
		autoHidden: make(map[*action.Tab]bool),
		compiler:   &TinymistCompiler{},
	}
}

// Init registers buffer observers, tab/resize hooks, and inline graphics lifecycle hooks.
// Safe to call multiple times (idempotent).
func Init() {
	initOnce.Do(func() {
		buffer.RegisterObserver(defaultManager)
		action.RegisterPreResizeHook(defaultManager.onPreResize)
		action.RegisterPostResizeHook(defaultManager.onPostResize)
		action.RegisterTabSwitchHook(defaultManager.onTabSwitch)
		action.RegisterPaneFocusHook(defaultManager.onPaneFocus)
		screen.RegisterPostShowHook(defaultManager.postShow)
		screen.RegisterPreFiniHook(defaultManager.onPreFini)
		screen.RegisterPostRestartHook(defaultManager.onPostRestart)
	})
}

// Shutdown closes all open inline preview panes and cleans up temporary files.
func Shutdown() {
	defaultManager.Shutdown()
}

// SetCompiler overrides the compiler used to produce page PNGs.
func SetCompiler(c Compiler) {
	defaultManager.setCompiler(c)
}

// SetRootResolver configures how project roots are resolved for .typ files.
func SetRootResolver(fn RootResolverFunc) {
	defaultManager.setRootResolver(fn)
}

// SetMainFileResolver configures optional resolution of a pinned main .typ file.
func SetMainFileResolver(fn MainFileResolverFunc) {
	defaultManager.setMainFileResolver(fn)
}

// SetAnchorNeedChecker configures an optional predicate reporting whether a tab
// requires PDF anchor extraction during background preview compiles.
func SetAnchorNeedChecker(fn AnchorNeedCheckerFunc) {
	defaultManager.setAnchorNeedChecker(fn)
}

// SetSectionsProvider configures an optional outline section extractor used for
// inline cursor-to-preview scroll synchronization.
func SetSectionsProvider(fn SectionsProviderFunc) {
	defaultManager.setSectionsProvider(fn)
}

// NotifyMainFileChanged triggers a recompile on all open document preview panes.
func NotifyMainFileChanged() {
	defaultManager.notifyMainFileChanged()
}

// SetOutput overrides the raw Sixel output writer (intended for tests; nil restores default).
func SetOutput(w io.Writer) {
	defaultManager.setOutput(w)
}

// PaneForTab returns the currently open PreviewPane on tab, or nil.
func PaneForTab(tab *action.Tab) *PreviewPane {
	return defaultManager.paneForTab(tab)
}

// Open opens (or focuses/refreshes) the inline preview pane on bp's tab.
func Open(bp *action.BufPane, focus bool) (*PreviewPane, bool) {
	return defaultManager.Open(bp, focus)
}

// Toggle toggles the inline preview pane on bp's tab.
func Toggle(bp *action.BufPane) bool {
	return defaultManager.Toggle(bp)
}

// ToggleWithProtocol opens the inline preview pane using proto, switches an open
// pane to proto if it currently uses another protocol, or closes the pane if it
// is already displaying proto.
func ToggleWithProtocol(bp *action.BufPane, proto GraphicsProtocol) bool {
	return defaultManager.ToggleWithProtocol(bp, proto)
}

// OpenForOverride opens (or reuses) the right-docked Sixel PreviewPane in
// temporary gallery override mode. Returns (nil, false) silently when inline
// Sixel preview is unsupported or the terminal is too narrow.
func OpenForOverride(bp *action.BufPane, title string) (*PreviewPane, bool) {
	return defaultManager.OpenForOverride(bp, title)
}

// ClearOverrideForTab exits gallery override mode on tab: if PreviewPane was
// opened solely for the gallery, closes it cleanly; otherwise restores the
// pre-existing document preview.
func ClearOverrideForTab(tab *action.Tab) {
	defaultManager.ClearOverrideForTab(tab)
}

func (m *Manager) setCompiler(c Compiler) {
	m.cfgMu.Lock()
	m.compiler = c
	m.cfgMu.Unlock()
}

func (m *Manager) getCompiler() Compiler {
	m.cfgMu.RLock()
	c := m.compiler
	m.cfgMu.RUnlock()
	return c
}

func (m *Manager) setRootResolver(fn RootResolverFunc) {
	m.cfgMu.Lock()
	m.rootResolver = fn
	m.cfgMu.Unlock()
}

func (m *Manager) getRootResolver() RootResolverFunc {
	m.cfgMu.RLock()
	fn := m.rootResolver
	m.cfgMu.RUnlock()
	return fn
}

func (m *Manager) setMainFileResolver(fn MainFileResolverFunc) {
	m.cfgMu.Lock()
	m.mainFileResolver = fn
	m.cfgMu.Unlock()
}

func (m *Manager) setAnchorNeedChecker(fn AnchorNeedCheckerFunc) {
	m.cfgMu.Lock()
	m.anchorNeedChecker = fn
	m.cfgMu.Unlock()
}

func (m *Manager) getAnchorNeedChecker() AnchorNeedCheckerFunc {
	m.cfgMu.RLock()
	fn := m.anchorNeedChecker
	m.cfgMu.RUnlock()
	return fn
}

func (m *Manager) setSectionsProvider(fn SectionsProviderFunc) {
	m.cfgMu.Lock()
	m.sectionsProvider = fn
	m.cfgMu.Unlock()
}

func (m *Manager) getSectionsProvider() SectionsProviderFunc {
	m.cfgMu.RLock()
	fn := m.sectionsProvider
	m.cfgMu.RUnlock()
	return fn
}

func (m *Manager) resolveMainFile(bufPath string) string {
	m.cfgMu.RLock()
	fn := m.mainFileResolver
	m.cfgMu.RUnlock()
	if fn == nil {
		return ""
	}
	return fn(bufPath)
}

func (m *Manager) notifyMainFileChanged() {
	for _, pp := range m.panes {
		if pp != nil && !pp.overrideActive {
			pp.ScheduleCompile(true)
		}
	}
}

func canonicalPreviewPath(p string) string {
	if p == "" {
		return ""
	}
	cleaned := filepath.Clean(p)
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}

func (m *Manager) setOutput(w io.Writer) {
	m.cfgMu.Lock()
	m.output = w
	m.cfgMu.Unlock()
}

func (m *Manager) resolveOutput() io.Writer {
	m.cfgMu.RLock()
	w := m.output
	m.cfgMu.RUnlock()
	if w != nil {
		return w
	}
	if screen.Screen == nil {
		return nil
	}
	if _, isSim := screen.Screen.(tcell.SimulationScreen); isSim {
		return nil
	}
	return os.Stdout
}

func (m *Manager) deleteKittyNow(out io.Writer) {
	hadKitty := m.kittyActive || m.kittyDeletePending
	m.kittyActive = false
	m.kittyDeletePending = false
	if !hadKitty || out == nil {
		return
	}
	EnableVTOutput()
	_, _ = io.WriteString(out, kittyDeleteSequence(UseTmuxPassthrough()))
}

func (m *Manager) onPreFini() {
	m.deleteKittyNow(m.resolveOutput())
}

func (m *Manager) onPostRestart() {
	for t, pp := range m.panes {
		if m.paneForTab(t) == nil {
			continue
		}
		pp.frameDirty = true
		pp.clearPending = true
	}
}

// Shutdown closes every tracked preview pane, deletes any active Kitty graphics
// placement, and removes temp PNG directories.
func (m *Manager) Shutdown() {
	m.deleteKittyNow(m.resolveOutput())
	m.pendingSync = false
	for tab, pp := range m.panes {
		pp.Close()
		delete(m.panes, tab)
	}
	for tab := range m.autoHidden {
		delete(m.autoHidden, tab)
	}
}

func (m *Manager) paneForTab(tab *action.Tab) *PreviewPane {
	if tab == nil {
		return nil
	}
	pp := m.panes[tab]
	if pp == nil {
		return nil
	}
	for _, p := range tab.Panes {
		if p == pp {
			return pp
		}
	}
	pp.Close()
	delete(m.panes, tab)
	return nil
}

// Toggle opens or closes the inline preview pane for bp's tab.
func (m *Manager) Toggle(bp *action.BufPane) bool {
	if bp == nil || bp.Tab() == nil {
		return false
	}
	tab := bp.Tab()
	if m.paneForTab(tab) != nil {
		return m.CloseForTab(tab)
	}
	_, ok := m.Open(bp, false)
	return ok
}

// ToggleWithProtocol opens, switches, or closes the inline preview pane with proto.
func (m *Manager) ToggleWithProtocol(bp *action.BufPane, proto GraphicsProtocol) bool {
	if bp == nil || bp.Tab() == nil {
		return false
	}
	if proto == ProtocolNone {
		return m.Toggle(bp)
	}
	tab := bp.Tab()
	if existing := m.paneForTab(tab); existing != nil {
		cur := existing.lastEmitProto
		if cur == ProtocolNone && existing.viewport != nil {
			cur = existing.viewport.Protocol
		}
		if cur == ProtocolNone {
			cur = existing.effectiveEmitProtocol()
		}
		if cur == proto {
			return m.CloseForTab(tab)
		}
		existing.protocolOverride = proto
		if cur == ProtocolKitty || m.kittyActive {
			m.kittyDeletePending = true
		} else if existing.lastEmitX >= 0 {
			m.pendingSync = true
		}
		if existing.activeDeck() != nil {
			existing.rebuildViewport(true)
		}
		existing.frameDirty = true
		existing.clearPending = true
		screen.Redraw()
		notifyMessage(fmt.Sprintf("Inline preview switched to %s", proto.String()))
		return true
	}
	pp, ok := m.openWithProtocol(bp, proto, false)
	return ok && pp != nil
}

// Open docks the continuous inline PreviewPane on the right edge of bp's tab.
func (m *Manager) Open(bp *action.BufPane, focus bool) (*PreviewPane, bool) {
	return m.openWithProtocol(bp, ProtocolNone, focus)
}

func (m *Manager) openWithProtocol(bp *action.BufPane, proto GraphicsProtocol, focus bool) (*PreviewPane, bool) {
	if bp == nil || bp.Buf == nil || bp.Tab() == nil {
		return nil, false
	}
	m.pruneStaleTabs()
	tab := bp.Tab()

	if existing := m.paneForTab(tab); existing != nil {
		existing.target = bp
		if proto != ProtocolNone {
			existing.protocolOverride = proto
		}
		existing.frameDirty = true
		existing.clearPending = true
		existing.ScheduleCompile(true)
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
		notifyError("Preview is only available for .typ buffers")
		return nil, false
	}

	termW := tabTerminalWidth(tab)
	if termW < MinTerminalWidth {
		delete(m.autoHidden, tab)
		notifyError(fmt.Sprintf("Inline preview requires terminal width >= %d columns (current: %d)", MinTerminalWidth, termW))
		return nil, false
	}

	cfgPct := readGlobalInt("typst.previewWidth", DefaultPreviewWidthPct)
	effW := EffectiveWidth(termW, cfgPct)
	if effW <= 0 {
		notifyError(fmt.Sprintf("Inline preview requires terminal width >= %d columns", MinTerminalWidth))
		return nil, false
	}

	dockID := tab.Node.DockRight(effW)
	if dockID == 0 {
		notifyError("Unable to dock inline preview in current split layout")
		return nil, false
	}

	delete(m.autoHidden, tab)
	pp := newPreviewPane(tab, bp, m)
	pp.protocolOverride = proto
	pp.SetID(dockID)
	m.panes[tab] = pp

	idx := len(tab.Panes)
	tab.AddPane(pp, idx)
	tab.Resize()
	pp.ScheduleCompile(true)

	if focus {
		tab.SetActive(idx)
	}
	return pp, true
}

// CloseForTab closes the inline preview pane on tab if open.
func CloseForTab(tab *action.Tab) bool {
	return defaultManager.CloseForTab(tab)
}

func (m *Manager) OpenForOverride(bp *action.BufPane, title string) (*PreviewPane, bool) {
	if !SupportsInline() || bp == nil || bp.Tab() == nil {
		return nil, false
	}
	m.pruneStaleTabs()
	tab := bp.Tab()

	if existing := m.paneForTab(tab); existing != nil {
		existing.ShowOverrideLoading(title)
		return existing, true
	}

	termW := tabTerminalWidth(tab)
	if termW < MinTerminalWidth {
		return nil, false
	}
	cfgPct := readGlobalInt("typst.previewWidth", DefaultPreviewWidthPct)
	effW := EffectiveWidth(termW, cfgPct)
	if effW <= 0 {
		return nil, false
	}
	dockID := tab.Node.DockRight(effW)
	if dockID == 0 {
		return nil, false
	}

	delete(m.autoHidden, tab)
	pp := newPreviewPane(tab, bp, m)
	pp.SetID(dockID)
	pp.openedByOverride = true
	m.panes[tab] = pp

	idx := len(tab.Panes)
	tab.AddPane(pp, idx)
	tab.Resize()
	pp.ShowOverrideLoading(title)
	return pp, true
}

func (m *Manager) ClearOverrideForTab(tab *action.Tab) {
	pp := m.paneForTab(tab)
	if pp == nil || !pp.HasOverride() {
		return
	}
	if pp.openedByOverride {
		m.CloseForTab(tab)
		return
	}
	pp.ClearOverride()
}

// CloseForTab closes and unsplits the inline preview pane on tab if open.
func (m *Manager) CloseForTab(tab *action.Tab) bool {
	if tab == nil {
		return false
	}
	delete(m.autoHidden, tab)
	return m.detachFromTab(tab)
}

func (m *Manager) detachFromTab(tab *action.Tab) bool {
	pp := m.paneForTab(tab)
	if pp == nil {
		return false
	}
	wasFocused := tab.ActivePane() == pp
	wasKitty := pp.lastEmitProto == ProtocolKitty || m.kittyActive
	target := pp.resolveTarget()
	id := pp.ID()

	pp.Close()
	if n := tab.GetNode(id); n != nil {
		n.Unsplit()
	}
	for i, p := range tab.Panes {
		if p == pp {
			tab.RemovePane(i)
			break
		}
	}
	tab.Resize()

	if len(tab.Panes) > 0 {
		if wasFocused && target != nil {
			focused := false
			for i, p := range tab.Panes {
				if p == target {
					tab.SetActive(i)
					focused = true
					break
				}
			}
			if !focused {
				tab.SetActive(tab.ActiveIndex())
			}
		} else {
			tab.SetActive(tab.ActiveIndex())
		}
	}

	if wasKitty {
		// Explicitly delete the Kitty placement on the next frame (overwriting
		// cells with spaces does not remove Kitty graphics placements).
		m.kittyDeletePending = true
	} else {
		// Schedule a full screen sync after the next frame draws so no stale Sixel
		// pixels can linger over unchanged blank cells in the expanded editor.
		m.pendingSync = true
	}
	screen.Redraw()
	return true
}

func (m *Manager) postShow() {
	out := m.resolveOutput()
	var pp *PreviewPane
	if action.Tabs != nil && len(action.Tabs.List) > 0 {
		if tab := action.MainTab(); tab != nil {
			pp = m.paneForTab(tab)
		}
	}

	if m.pendingSync {
		m.pendingSync = false
		// Wrap ED 2 + tcell Sync + inline graphics re-emit in one synchronized update so
		// Windows Terminal / ConPTY (where tcell's cScreen.Sync does not issue
		// ED 2 and ConPTY skips unchanged blank cells) reliably wipes every
		// stale Sixel bitmap with zero flicker.
		if out != nil {
			EnableVTOutput()
			_, _ = io.WriteString(out, "\x1b[?2026h\x1b[?25l\x1b[0m\x1b[2J")
			if m.kittyActive || m.kittyDeletePending {
				_, _ = io.WriteString(out, kittyDeleteSequence(UseTmuxPassthrough()))
				m.kittyActive = false
				m.kittyDeletePending = false
			}
		} else {
			m.kittyActive = false
			m.kittyDeletePending = false
		}
		if screen.Screen != nil {
			screen.Screen.Sync()
		}
		if pp != nil && !pp.isTabResizing() {
			pp.frameDirty = true
			pp.clearPending = false // ED 2 just cleared the entire viewport
			pp.emitFrameIfDirty(out, false)
		}
		if out != nil {
			_, _ = io.WriteString(out, "\x1b[?2026l")
		}
		return
	}

	if pp != nil && !pp.isTabResizing() && pp.frameDirty && pp.viewport != nil && len(pp.viewport.Payload) > 0 {
		pp.emitFrameIfDirty(out, true)
		return
	}

	if m.kittyDeletePending {
		m.kittyDeletePending = false
		if m.kittyActive {
			m.kittyActive = false
			if out != nil {
				EnableVTOutput()
				_, _ = io.WriteString(out, "\x1b[?2026h"+kittyDeleteSequence(UseTmuxPassthrough())+"\x1b[?2026l")
			}
		}
	}
}

func (m *Manager) pruneStaleTabs() {
	if action.Tabs == nil {
		return
	}
	live := make(map[*action.Tab]bool, len(action.Tabs.List))
	for _, t := range action.Tabs.List {
		live[t] = true
	}
	for t, pp := range m.panes {
		if !live[t] {
			if m.kittyActive {
				m.kittyDeletePending = true
			}
			pp.Close()
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
			if bp := findTypstPaneInTab(t); bp != nil {
				m.Open(bp, false)
			}
		}
	}
	for t, pp := range m.panes {
		if m.paneForTab(t) == nil {
			continue
		}
		pp.frameDirty = true
		pp.clearPending = true
	}
}

func (m *Manager) onTabSwitch(tab *action.Tab) {
	m.pruneStaleTabs()
	// Switching tabs invalidates any inline bitmap from the previous tab.
	if len(m.panes) > 0 {
		if m.kittyActive {
			m.kittyDeletePending = true
		} else {
			m.pendingSync = true
		}
	}
	pp := m.paneForTab(tab)
	if pp == nil {
		return
	}
	if pp.overrideActive {
		pp.frameDirty = true
		pp.clearPending = true
		return
	}
	target := pp.resolveTarget()
	if target == nil || target.Buf == nil || !target.Buf.IsTypst() {
		m.CloseForTab(tab)
		return
	}
	pp.frameDirty = true
	pp.clearPending = true
	if pp.lastShared != target.Buf.SharedBuffer || pp.lastVersion != target.Buf.Version() {
		pp.ScheduleCompile(true)
	}
}

func (m *Manager) onPaneFocus(tab *action.Tab, pane action.Pane) {
	pp := m.paneForTab(tab)
	if pp == nil || pp.overrideActive || pane == nil || pane == pp {
		return
	}
	bp, ok := pane.(*action.BufPane)
	if !ok || bp.Buf == nil || !bp.Buf.IsTypst() {
		return
	}
	if pp.target != bp {
		effMain := m.resolveMainFile(bp.Buf.AbsPath)
		if effMain == "" {
			effMain = bp.Buf.AbsPath
		}
		samePinnedMain := pp.lastCompiledMain != "" &&
			effMain != "" &&
			canonicalPreviewPath(effMain) == canonicalPreviewPath(pp.lastCompiledMain)
		pp.target = bp
		if samePinnedMain {
			return
		}
		pp.scrollY = 0
		pp.scrollX = 0
		pp.pendingSection = nil
		pp.pendingClick = nil
		pp.lastClickTime = time.Time{}
		pp.ScheduleCompile(true)
	}
}

// OnCursorMove schedules a debounced cursor-to-preview scroll sync when
// inline cursor sync is enabled on an open preview pane.
func (m *Manager) OnCursorMove(b *buffer.Buffer, ev buffer.CursorEvent) {
	if b == nil || !b.IsTypst() || ev.New.Y == ev.Old.Y {
		return
	}
	for tab, pp := range m.panes {
		if m.paneForTab(tab) == nil || pp.overrideActive || !pp.syncCursor {
			continue
		}
		target := pp.resolveTarget()
		if target == nil || target.Buf == nil || target.Buf.SharedBuffer != b.SharedBuffer {
			continue
		}
		pane := pp
		pane.mu.Lock()
		if pane.closed {
			pane.mu.Unlock()
			continue
		}
		if pane.cursorSyncTimer != nil {
			pane.cursorSyncTimer.Stop()
		}
		gen := pane.gen
		pane.cursorSyncTimer = time.AfterFunc(cursorSyncDebounceMs*time.Millisecond, func() {
			action.EnqueueUI(func() {
				pane.mu.Lock()
				stale := pane.closed || pane.gen != gen
				pane.cursorSyncTimer = nil
				pane.mu.Unlock()
				if !stale {
					pane.syncToTargetCursorNow()
				}
			})
		})
		pane.mu.Unlock()
	}
}

// OnBufferChange schedules a debounced live recompile for open preview panes.
func (m *Manager) OnBufferChange(b *buffer.Buffer, ev buffer.ChangeEvent) {
	if b == nil || !b.IsTypst() {
		return
	}
	for tab, pp := range m.panes {
		if m.paneForTab(tab) == nil {
			continue
		}
		target := pp.resolveTarget()
		mainPath := ""
		if b.AbsPath != "" {
			mainPath = m.resolveMainFile(b.AbsPath)
		}
		isPinnedOther := mainPath != "" && b.AbsPath != "" &&
			canonicalPreviewPath(mainPath) != canonicalPreviewPath(b.AbsPath)
		if target != nil && target.Buf != nil && target.Buf.SharedBuffer == b.SharedBuffer {
			if isPinnedOther {
				pp.pinnedSubfileDirty = b.Modified()
				screen.Redraw()
			} else {
				pp.ScheduleCompile(false)
			}
			continue
		}
		if mainPath != "" && b.AbsPath != "" && canonicalPreviewPath(mainPath) == canonicalPreviewPath(b.AbsPath) {
			pp.ScheduleCompile(false)
		}
	}
}

// OnBufferSave triggers an immediate recompile for open preview panes.
func (m *Manager) OnBufferSave(b *buffer.Buffer, ev buffer.SaveEvent) {
	if b == nil || !b.IsTypst() {
		return
	}
	for tab, pp := range m.panes {
		if m.paneForTab(tab) == nil {
			continue
		}
		target := pp.resolveTarget()
		if target != nil && target.Buf != nil && target.Buf.SharedBuffer == b.SharedBuffer {
			pp.pinnedSubfileDirty = false
			pp.ScheduleCompile(true)
			continue
		}
		if pp.lastCompiledMain != "" && b.AbsPath != "" {
			rootDir := ""
			if fn := m.getRootResolver(); fn != nil {
				rootDir = fn(pp.lastCompiledMain)
			}
			if rootDir == "" {
				rootDir = filepath.Dir(pp.lastCompiledMain)
			}
			if canonicalPreviewPath(b.AbsPath) == canonicalPreviewPath(pp.lastCompiledMain) ||
				isSubPath(rootDir, b.AbsPath) {
				pp.pinnedSubfileDirty = false
				pp.ScheduleCompile(true)
			}
		}
	}
}

// OnBufferClose retargets or cleanly closes any open PreviewPane whose buffer closed.
func (m *Manager) OnBufferClose(b *buffer.Buffer) {
	if b == nil {
		return
	}
	var affected []*action.Tab
	for tab, pp := range m.panes {
		if pp.target != nil && pp.target.Buf == b {
			affected = append(affected, tab)
		}
	}
	for _, tab := range affected {
		pp := m.paneForTab(tab)
		if pp == nil {
			continue
		}
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
			pp.target = replacement
			pp.scrollY = 0
			pp.scrollX = 0
			pp.pendingSection = nil
			pp.pendingClick = nil
			pp.lastClickTime = time.Time{}
			pp.ScheduleCompile(true)
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
