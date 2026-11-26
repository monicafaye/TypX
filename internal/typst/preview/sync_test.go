package preview

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"sync"
	"testing"
	"time"

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
