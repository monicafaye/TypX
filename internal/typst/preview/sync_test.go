package preview

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/micro-editor/tcell/v2"
	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
)

func makeGradientTestImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8((x * 3 + y * 5) & 0xff),
				G: uint8((x * 7 + y * 11) & 0xff),
				B: uint8((x * 13 + y * 17) & 0xff),
				A: 255,
			})
		}
	}
	return img
}

type recordingCompiler struct {
	mu      sync.Mutex
	reqs    []CompileRequest
	pages   int
	anchors *DocumentAnchors
}

func (rc *recordingCompiler) CompilePages(ctx context.Context, req CompileRequest) (*DocumentDeck, error) {
	rc.mu.Lock()
	rc.reqs = append(rc.reqs, req)
	pages := rc.pages
	if pages <= 0 {
		pages = 3
	}
	anchors := rc.anchors
	rc.mu.Unlock()

	imgs := make([]*image.RGBA, pages)
	for i := range imgs {
		imgs[i] = makeGradientTestImage(300, 420)
	}
	deck := NewDocumentDeckFromImages(imgs, 5*time.Millisecond)
	if !req.SkipAnchors && anchors != nil {
		deck.anchors = anchors
	}
	return deck, nil
}

func (rc *recordingCompiler) Requests() []CompileRequest {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	out := make([]CompileRequest, len(rc.reqs))
	copy(out, rc.reqs)
	return out
}

func TestConditionalPDFAnchorSkipping(t *testing.T) {
	_, bp := initPreviewTestEnv(t)
	SetCapsForTest(TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})
	t.Cleanup(func() {
		SetCapsForTest(TerminalCaps{})
		SetAnchorNeedChecker(nil)
		SetSectionsProvider(nil)
		config.GlobalSettings["typst.previewSyncCursor"] = false
	})
	config.GlobalSettings["typst.previewSyncCursor"] = false

	outlineOpen := false
	SetAnchorNeedChecker(func(tab *action.Tab) bool { return outlineOpen })

	rc := &recordingCompiler{
		pages: 3,
		anchors: &DocumentAnchors{
			Outlines: []OutlineAnchor{
				{Title: "Intro", Depth: 1, PageIdx: 0, YFrac: 0.1},
				{Title: "Results", Depth: 1, PageIdx: 2, YFrac: 0.2},
			},
		},
	}
	SetCompiler(rc)
	var outBuf bytes.Buffer
	SetOutput(&outBuf)
	Init()

	// 1. Outline closed + syncCursor off -> SkipAnchors=true
	pp, ok := Open(bp, false)
	if !ok || pp == nil {
		t.Fatal("Open failed")
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return !pp.IsCompiling() && pp.viewport != nil
	})
	reqs := rc.Requests()
	if len(reqs) != 1 || !reqs[0].SkipAnchors {
		t.Fatalf("expected initial compile with SkipAnchors=true, got %+v", reqs)
	}
	if pp.deck.anchors != nil {
		t.Fatal("expected nil anchors when SkipAnchors=true")
	}

	// 2. Outline open -> recompile requests PDF anchors (SkipAnchors=false)
	outlineOpen = true
	pp.ScheduleCompile(true)
	waitForCondition(t, 2*time.Second, func() bool {
		return len(rc.Requests()) >= 2 && !pp.IsCompiling()
	})
	reqs = rc.Requests()
	if reqs[1].SkipAnchors {
		t.Fatalf("expected SkipAnchors=false when outline is open, got %+v", reqs[1])
	}
	if pp.deck.anchors == nil {
		t.Fatal("expected PDF anchors populated when SkipAnchors=false")
	}

	// 3. Outline closed again, then ToggleSyncCursor enables anchors on demand
	outlineOpen = false
	pp.deck.anchors = nil
	pp.deck.anchorsChecked = false
	if !pp.ToggleSyncCursor() {
		t.Fatal("expected ToggleSyncCursor to enable sync")
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return len(rc.Requests()) >= 3 && !pp.IsCompiling()
	})
	reqs = rc.Requests()
	if reqs[2].SkipAnchors {
		t.Fatalf("expected SkipAnchors=false when syncCursor is enabled, got %+v", reqs[2])
	}
}

func TestJumpToPageAndClamping(t *testing.T) {
	_, bp := initPreviewTestEnv(t)
	SetCapsForTest(TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})
	t.Cleanup(func() { SetCapsForTest(TerminalCaps{}) })

	rc := &recordingCompiler{pages: 4}
	SetCompiler(rc)
	var outBuf bytes.Buffer
	SetOutput(&outBuf)
	Init()

	pp, ok := Open(bp, false)
	if !ok || pp == nil {
		t.Fatal("Open failed")
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return !pp.IsCompiling() && pp.viewport != nil
	})

	p, total, jumped := pp.JumpToPage(3)
	if !jumped || p != 3 || total != 4 {
		t.Fatalf("JumpToPage(3) = (%d, %d, %v), want (3, 4, true)", p, total, jumped)
	}
	if pp.scrollY <= 0 {
		t.Fatalf("expected positive scrollY after jumping to page 3, got %d", pp.scrollY)
	}

	// Out-of-range page numbers clamp cleanly to [1, total]
	p, total, jumped = pp.JumpToPage(99)
	if !jumped || p != 4 || total != 4 {
		t.Fatalf("JumpToPage(99) = (%d, %d, %v), want (4, 4, true)", p, total, jumped)
	}
	p, total, jumped = pp.JumpToPage(0)
	if !jumped || p != 1 || total != 4 {
		t.Fatalf("JumpToPage(0) = (%d, %d, %v), want (1, 4, true)", p, total, jumped)
	}
	if pp.scrollY != 0 {
		t.Fatalf("expected scrollY=0 after JumpToPage(0), got %d", pp.scrollY)
	}
}

func TestCursorFollowScrollSyncAndDeadband(t *testing.T) {
	_, bp := initPreviewTestEnv(t)
	SetCapsForTest(TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})
	t.Cleanup(func() {
		SetCapsForTest(TerminalCaps{})
		SetAnchorNeedChecker(nil)
		SetSectionsProvider(nil)
		config.GlobalSettings["typst.previewSyncCursor"] = false
	})
	config.GlobalSettings["typst.previewSyncCursor"] = true

	sections := []SectionEntry{
		{Kind: SectionHeading, Level: 1, Title: "Intro", Line: 0},
		{Kind: SectionHeading, Level: 1, Title: "Middle", Line: 20},
		{Kind: SectionHeading, Level: 1, Title: "End", Line: 40},
	}
	SetSectionsProvider(func(b *buffer.Buffer) []SectionEntry { return sections })

	rc := &recordingCompiler{
		pages: 4,
		anchors: &DocumentAnchors{
			Outlines: []OutlineAnchor{
				{Title: "Intro", Depth: 1, PageIdx: 0, YFrac: 0.05},
				{Title: "Middle", Depth: 1, PageIdx: 1, YFrac: 0.50},
				{Title: "End", Depth: 1, PageIdx: 3, YFrac: 0.60},
			},
		},
	}
	SetCompiler(rc)
	var outBuf bytes.Buffer
	SetOutput(&outBuf)
	Init()

	pp, ok := Open(bp, false)
	if !ok || pp == nil {
		t.Fatal("Open failed")
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return !pp.IsCompiling() && pp.viewport != nil
	})
	if !pp.IsSyncCursor() {
		t.Fatal("expected IsSyncCursor=true from typst.previewSyncCursor setting")
	}

	// Moving to line 40 (page 4) scrolls preview down
	if !pp.ScrollToCursorLine(40, 50, sections) {
		t.Fatal("expected ScrollToCursorLine(40) to scroll viewport")
	}
	scrollAtEnd := pp.scrollY
	if scrollAtEnd <= 0 {
		t.Fatalf("expected scrollY > 0 at End heading, got %d", scrollAtEnd)
	}

	// Repeating at the same line stays inside visible deadband (no redundant redraw)
	if pp.ScrollToCursorLine(40, 50, sections) {
		t.Fatal("expected ScrollToCursorLine inside visible band to skip redundant scroll")
	}

	// Moving back to line 0 scrolls back to top
	if !pp.ScrollToCursorLine(0, 50, sections) {
		t.Fatal("expected ScrollToCursorLine(0) to return toward top")
	}
	if pp.scrollY >= scrollAtEnd {
		t.Fatalf("expected scrollY (%d) < scrollAtEnd (%d)", pp.scrollY, scrollAtEnd)
	}
}

func TestPreviewPaneClickToSourceDoubleClickCtrlClickDragAndAnchors(t *testing.T) {
	_, _ = initPreviewTestEnv(t)
	SetCapsForTest(TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})
	t.Cleanup(func() {
		SetCapsForTest(TerminalCaps{})
		SetAnchorNeedChecker(nil)
		SetSectionsProvider(nil)
		config.GlobalSettings["typst.previewSyncCursor"] = false
	})
	config.GlobalSettings["typst.previewSyncCursor"] = false

	// Build a 101-line Typst buffer so line numbers and cursor movement are clear.
	var doc strings.Builder
	for i := 0; i < 101; i++ {
		switch i {
		case 0:
			doc.WriteString("= Intro\n")
		case 20:
			doc.WriteString("= Middle\n")
		case 80:
			doc.WriteString("= Conclusion\n")
		default:
			fmt.Fprintf(&doc, "Paragraph line %d.\n", i)
		}
	}
	b := buffer.NewBufferFromString(strings.TrimSuffix(doc.String(), "\n"), "click_sync.typ", buffer.BTDefault)
	b.Settings["filetype"] = "typst"
	action.InitTabs([]*buffer.Buffer{b})
	action.Tabs.Resize()
	bp := action.MainTab().CurPane()
	tab := action.MainTab()

	sections := []SectionEntry{
		{Kind: SectionHeading, Level: 1, Title: "Intro", Line: 0},
		{Kind: SectionHeading, Level: 1, Title: "Middle", Line: 20},
		{Kind: SectionHeading, Level: 1, Title: "Conclusion", Line: 80},
	}
	SetSectionsProvider(func(*buffer.Buffer) []SectionEntry { return sections })
	SetAnchorNeedChecker(func(*action.Tab) bool { return false })

	// Put Middle anchor near top of page 1 (so anchor line 20 differs clearly from proportional).
	rc := &recordingCompiler{
		pages: 4,
		anchors: &DocumentAnchors{
			Outlines: []OutlineAnchor{
				{Title: "Intro", Depth: 1, PageIdx: 0, YFrac: 0.05},
				{Title: "Middle", Depth: 1, PageIdx: 0, YFrac: 0.15},
				{Title: "Conclusion", Depth: 1, PageIdx: 3, YFrac: 0.50},
			},
		},
	}
	SetCompiler(rc)
	var outBuf bytes.Buffer
	SetOutput(&outBuf)
	Init()

	pp, ok := Open(bp, false)
	if !ok || pp == nil {
		t.Fatal("Open failed")
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return !pp.IsCompiling() && pp.viewport != nil
	})
	if pp.deck.anchors != nil {
		t.Fatal("expected initial compile to skip anchors (SkipAnchors=true)")
	}

	v := pp.GetView()
	imgX := v.X + 2
	imgY := v.Y + 4 // localClickYpx = 4*20 + 10 = 90

	// 1. Single left click focuses PreviewPane without moving editor cursor.
	tab.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.Button1, 0, ""))
	tab.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.ButtonNone, 0, ""))
	if tab.ActivePane() != pp {
		t.Fatal("expected single left-click to focus PreviewPane")
	}
	if bp.Cursor.Loc.Y != 0 {
		t.Fatalf("expected single click NOT to move editor cursor, got line %d", bp.Cursor.Loc.Y)
	}

	// 2. Drag (repeated Button1 without ButtonNone) must NOT trigger double-click.
	pp.lastClickTime = time.Time{}
	pp.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.Button1, 0, ""))
	pp.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.Button1, 0, ""))
	if bp.Cursor.Loc.Y != 0 || tab.ActivePane() != pp {
		t.Fatalf("drag Button1 should not trigger jump (cursor=%d, active=%v)", bp.Cursor.Loc.Y, tab.ActivePane() == pp)
	}
	pp.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.ButtonNone, 0, ""))

	// 3. Footer click must be ignored and clear lastClickTime.
	footerY := v.Y + pp.imageRows()
	pp.HandleEvent(tcell.NewEventMouse(imgX, footerY, tcell.Button1, tcell.ModCtrl, ""))
	pp.HandleEvent(tcell.NewEventMouse(imgX, footerY, tcell.ButtonNone, 0, ""))
	if bp.Cursor.Loc.Y != 0 {
		t.Fatalf("footer click should not jump, got cursor line %d", bp.Cursor.Loc.Y)
	}

	// 4. Ctrl+Click while anchors == nil jumps immediately AND triggers on-demand anchor compile + refines cursor.
	tab.SetActive(0) // unfocus preview to also verify unfocused Ctrl+Click works via Tab.HandleEvent
	prevReqs := len(rc.Requests())
	tab.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.Button1, tcell.ModCtrl, ""))
	tab.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.ButtonNone, 0, ""))

	if tab.ActivePane() != bp {
		t.Fatal("expected Ctrl+Click to return focus to editor BufPane")
	}
	if !pp.mouseReleased {
		t.Fatal("expected mouseReleased=true after returnFocus")
	}
	initialLine := bp.Cursor.Loc.Y
	if initialLine <= 0 {
		t.Fatalf("expected Ctrl+Click to move editor cursor down from 0, got %d", initialLine)
	}

	// Wait for on-demand anchor compile (SkipAnchors == false) to land and refine cursor.
	waitForCondition(t, 2*time.Second, func() bool {
		return len(rc.Requests()) > prevReqs && !pp.IsCompiling()
	})
	reqs := rc.Requests()
	if reqs[len(reqs)-1].SkipAnchors {
		t.Fatal("expected on-demand anchor compile with SkipAnchors=false")
	}
	if pp.deck.anchors == nil {
		t.Fatal("expected deck.anchors populated after on-demand compile")
	}
	refinedLine := bp.Cursor.Loc.Y
	if refinedLine == initialLine {
		t.Fatalf("expected anchor refinement to adjust cursor from proportional line %d", initialLine)
	}

	// 5. Clicking > 2 cells away does NOT trigger double-click.
	bp.Cursor.GotoLoc(buffer.Loc{X: 0, Y: 0})
	pp.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.Button1, 0, ""))
	pp.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.ButtonNone, 0, ""))
	pp.HandleEvent(tcell.NewEventMouse(imgX+5, imgY, tcell.Button1, 0, ""))
	pp.HandleEvent(tcell.NewEventMouse(imgX+5, imgY, tcell.ButtonNone, 0, ""))
	if bp.Cursor.Loc.Y != 0 {
		t.Fatalf("clicks >2 cells apart should not trigger double-click jump, got line %d", bp.Cursor.Loc.Y)
	}

	// 6. Double-click within <= 400ms and <= 2 cells jumps and centers cursor + returns focus.
	reqCountBeforeDouble := len(rc.Requests())
	pp.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.Button1, 0, ""))
	pp.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.ButtonNone, 0, ""))
	pp.HandleEvent(tcell.NewEventMouse(imgX+1, imgY+1, tcell.Button1, 0, ""))
	pp.HandleEvent(tcell.NewEventMouse(imgX+1, imgY+1, tcell.ButtonNone, 0, ""))

	if tab.ActivePane() != bp {
		t.Fatal("expected double-click to return focus to editor BufPane")
	}
	if bp.Cursor.Loc.Y == 0 {
		t.Fatal("expected double-click to move editor cursor")
	}
	if len(rc.Requests()) != reqCountBeforeDouble {
		t.Fatal("double-click with existing anchors should not trigger extra compile")
	}
}

type customPagesCompiler struct {
	mu      sync.Mutex
	reqs    []CompileRequest
	imgs    []*image.RGBA
	anchors *DocumentAnchors
}

func (c *customPagesCompiler) CompilePages(ctx context.Context, req CompileRequest) (*DocumentDeck, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	imgs := c.imgs
	anchors := c.anchors
	c.mu.Unlock()

	copies := make([]*image.RGBA, len(imgs))
	for i, im := range imgs {
		b := im.Bounds()
		cp := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				cp.SetRGBA(x, y, im.RGBAAt(x, y))
			}
		}
		copies[i] = cp
	}
	deck := NewDocumentDeckFromImages(copies, 5*time.Millisecond)
	if !req.SkipAnchors && anchors != nil {
		deck.anchors = anchors
	}
	return deck, nil
}

func (c *customPagesCompiler) Requests() []CompileRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]CompileRequest, len(c.reqs))
	copy(out, c.reqs)
	return out
}

func TestHeadinglessDocumentClickToSourceNoRedundantCompile(t *testing.T) {
	_, _ = initPreviewTestEnv(t)
	SetCapsForTest(TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})
	t.Cleanup(func() {
		SetCapsForTest(TerminalCaps{})
		SetAnchorNeedChecker(nil)
		SetSectionsProvider(nil)
		config.GlobalSettings["typst.previewSyncCursor"] = false
	})
	config.GlobalSettings["typst.previewSyncCursor"] = false

	var doc strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&doc, "Paragraph line %d.\n", i)
	}
	b := buffer.NewBufferFromString(strings.TrimSuffix(doc.String(), "\n"), "headingless.typ", buffer.BTDefault)
	b.Settings["filetype"] = "typst"
	action.InitTabs([]*buffer.Buffer{b})
	action.Tabs.Resize()
	bp := action.MainTab().CurPane()
	tab := action.MainTab()

	SetSectionsProvider(func(*buffer.Buffer) []SectionEntry { return nil })
	SetAnchorNeedChecker(func(*action.Tab) bool { return false })

	// Compiler returns nil anchors even when SkipAnchors=false (headingless document).
	rc := &recordingCompiler{pages: 2, anchors: nil}
	SetCompiler(rc)
	var outBuf bytes.Buffer
	SetOutput(&outBuf)
	Init()

	pp, ok := Open(bp, false)
	if !ok || pp == nil {
		t.Fatal("Open failed")
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return !pp.IsCompiling() && pp.viewport != nil
	})
	if len(rc.Requests()) != 1 || !rc.Requests()[0].SkipAnchors {
		t.Fatalf("expected initial compile with SkipAnchors=true, got %+v", rc.Requests())
	}
	if pp.deck.anchorsChecked {
		t.Fatal("expected anchorsChecked=false after SkipAnchors=true compile")
	}

	v := pp.GetView()
	imgX := v.X + 2
	imgY := v.Y + 4

	// First Ctrl+Click triggers exactly 1 on-demand SkipAnchors=false compile.
	tab.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.Button1, tcell.ModCtrl, ""))
	tab.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.ButtonNone, 0, ""))
	waitForCondition(t, 2*time.Second, func() bool {
		return len(rc.Requests()) >= 2 && !pp.IsCompiling()
	})
	if len(rc.Requests()) != 2 || rc.Requests()[1].SkipAnchors {
		t.Fatalf("expected 1 on-demand SkipAnchors=false compile, got %+v", rc.Requests())
	}
	if !pp.deck.anchorsChecked {
		t.Fatal("expected anchorsChecked=true after SkipAnchors=false compile")
	}

	// Second Ctrl+Click and subsequent double-click must NOT trigger redundant compiles.
	tab.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.Button1, tcell.ModCtrl, ""))
	tab.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.ButtonNone, 0, ""))
	pp.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.Button1, 0, ""))
	pp.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.ButtonNone, 0, ""))
	pp.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.Button1, 0, ""))
	pp.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.ButtonNone, 0, ""))

	if got := len(rc.Requests()); got != 2 {
		t.Fatalf("expected no redundant compiles on headingless document, got %d requests", got)
	}
}

func TestPreviewPaneClickToSourceTrimLazyViewportSync(t *testing.T) {
	_, _ = initPreviewTestEnv(t)
	SetCapsForTest(TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})
	t.Cleanup(func() {
		SetCapsForTest(TerminalCaps{})
		SetAnchorNeedChecker(nil)
		SetSectionsProvider(nil)
		config.GlobalSettings["typst.previewSyncCursor"] = false
		config.GlobalSettings["typst.previewTrimMargins"] = false
	})
	config.GlobalSettings["typst.previewSyncCursor"] = false
	config.GlobalSettings["typst.previewTrimMargins"] = true

	var doc strings.Builder
	for i := 0; i < 100; i++ {
		switch i {
		case 0:
			doc.WriteString("= Page1\n")
		case 50:
			doc.WriteString("= Page2\n")
		default:
			fmt.Fprintf(&doc, "Line %d.\n", i)
		}
	}
	b := buffer.NewBufferFromString(strings.TrimSuffix(doc.String(), "\n"), "trim_lazy.typ", buffer.BTDefault)
	b.Settings["filetype"] = "typst"
	action.InitTabs([]*buffer.Buffer{b})
	action.Tabs.Resize()
	bp := action.MainTab().CurPane()
	tab := action.MainTab()

	sections := []SectionEntry{
		{Kind: SectionHeading, Level: 1, Title: "Page1", Line: 0},
		{Kind: SectionHeading, Level: 1, Title: "Page2", Line: 50},
	}
	SetSectionsProvider(func(*buffer.Buffer) []SectionEntry { return sections })
	SetAnchorNeedChecker(func(*action.Tab) bool { return true })

	white := color.RGBA{255, 255, 255, 255}
	black := color.RGBA{0, 0, 0, 255}
	cc := &customPagesCompiler{
		imgs: []*image.RGBA{
			makePageWithMargins(200, 400, 20, 150, white, black),
			makePageWithMargins(200, 400, 20, 40, white, black),
		},
		anchors: &DocumentAnchors{
			Outlines: []OutlineAnchor{
				{Title: "Page1", Depth: 1, PageIdx: 0, YFrac: 0.45},
				{Title: "Page2", Depth: 1, PageIdx: 1, YFrac: 0.50},
			},
		},
	}
	SetCompiler(cc)
	var outBuf bytes.Buffer
	SetOutput(&outBuf)
	Init()

	pp, ok := Open(bp, false)
	if !ok || pp == nil {
		t.Fatal("Open failed")
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return !pp.IsCompiling() && pp.viewport != nil
	})

	// Jump directly to Page 2, leaving Page 0 lazily unscaled under trimMargins=true.
	// Wait—initial render scaled Page 0, so simulate a fresh recompile deck at scrollY=Page 2.
	pp.JumpToPage(2)
	pp.ScheduleCompile(true)
	waitForCondition(t, 2*time.Second, func() bool {
		return len(cc.Requests()) >= 2 && !pp.IsCompiling()
	})

	// Reset Page 0's scaled cache while staying scrolled to Page 2 so Page 0 stays unscaled.
	pp.deck.pages[0].scaled = nil
	scaledW, viewW, viewH := pp.targetViewportSize()
	pp.deck.ComputeLayout(scaledW, true)
	pp.scrollY = pp.deck.PageTop(1)
	vp, err := pp.deck.RenderViewportCached(scaledW, viewW, viewH, pp.scrollY, pp.scrollX, pp.invert, true, pp.targetProtocol(), nil)
	if err != nil {
		t.Fatalf("RenderViewportCached failed: %v", err)
	}
	pp.viewport = vp
	if pp.deck.pages[0].scaled != nil {
		t.Fatal("expected Page 0 to remain unscaled when rendering Page 2")
	}

	// Ctrl+Click at the middle of Page 2 on screen.
	v := pp.GetView()
	p2H := pp.deck.pageHeights[1]
	clickRow := (p2H / 2) / 20
	imgX := v.X + 2
	imgY := v.Y + clickRow

	tab.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.Button1, tcell.ModCtrl, ""))
	tab.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.ButtonNone, 0, ""))

	firstJump := bp.Cursor.Loc.Y
	if firstJump < 47 || firstJump > 53 {
		t.Fatalf("first Ctrl+Click with unscaled Page 0 jumped to line %d, want ~50", firstJump)
	}
	if pp.ScrollY() != pp.deck.PageTop(1) {
		t.Fatalf("expected pp.ScrollY (%d) synced to post-trim PageTop(1) (%d)", pp.ScrollY(), pp.deck.PageTop(1))
	}

	// Second Ctrl+Click at the same screen row must still resolve to ~50.
	bp.Cursor.GotoLoc(buffer.Loc{X: 0, Y: 0})
	tab.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.Button1, tcell.ModCtrl, ""))
	tab.HandleEvent(tcell.NewEventMouse(imgX, imgY, tcell.ButtonNone, 0, ""))
	secondJump := bp.Cursor.Loc.Y
	if secondJump != firstJump {
		t.Fatalf("second Ctrl+Click drifted from %d to %d", firstJump, secondJump)
	}
}
