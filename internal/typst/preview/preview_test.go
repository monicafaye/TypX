package preview

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
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

func makeTestPage(w, h int, bg, ink color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)
	// Draw a centered dark rectangle (simulating text/math with outer margins).
	rect := image.Rect(w/5, h/5, (4*w)/5, (4*h)/5)
	draw.Draw(img, rect, &image.Uniform{C: ink}, image.Point{}, draw.Src)
	return img
}

func TestParseProbeResponse(t *testing.T) {
	// Windows Terminal style response: CSI 16t (21x10 cell) + DA1 with attr 4 (Sixel).
	resp := []byte("\x1b[6;21;10t\x1b[4;966;1920t\x1b[?61;6;7;14;21;22;23;24;28;32;42;4c")
	sixel, cw, ch, src := parseProbeResponse(resp, 192, 46)
	if !sixel {
		t.Fatal("expected sixel=true for DA1 containing 4")
	}
	if cw != 10 || ch != 21 || src != "CSI16t" {
		t.Fatalf("got cell=%dx%d (%s), want 10x21 (CSI16t)", cw, ch, src)
	}

	// Fallback to CSI 14t when CSI 16t is absent.
	resp14 := []byte("\x1b[4;800;1600t\x1b[?62;4;22c")
	sixel, cw, ch, src = parseProbeResponse(resp14, 160, 40)
	if !sixel || cw != 10 || ch != 20 || src != "CSI14t" {
		t.Fatalf("got sixel=%v cell=%dx%d (%s), want true 10x20 (CSI14t)", sixel, cw, ch, src)
	}

	// Terminal without Sixel (no 4 in DA1).
	respNo := []byte("\x1b[?62;22c")
	sixel, _, _, _ = parseProbeResponse(respNo, 80, 24)
	if sixel {
		t.Fatal("expected sixel=false when DA1 omits 4")
	}
}

func TestEncodeSixelAndCrop(t *testing.T) {
	page := makeTestPage(200, 300, color.RGBA{255, 255, 255, 255}, color.RGBA{20, 40, 120, 255})

	cropped := cropWhitespace(page, 10)
	if cropped.Bounds().Dx() >= page.Bounds().Dx() || cropped.Bounds().Dy() >= page.Bounds().Dy() {
		t.Fatalf("expected cropWhitespace to trim margins, got %v from %v", cropped.Bounds(), page.Bounds())
	}

	scaled := scaleArea(page, 100, true)
	if scaled.Bounds().Dx() != 100 || scaled.Bounds().Dy() != 150 {
		t.Fatalf("expected 100x150 scaled image, got %v", scaled.Bounds())
	}
	// Top-left white margin inverted to soft dark slate (~#1b1e28).
	if scaled.RGBAAt(0, 0).R > 50 {
		t.Fatalf("expected dark inverted background pixel, got %+v", scaled.RGBAAt(0, 0))
	}

	sixel := EncodeSixel(scaled)
	if !bytes.HasPrefix(sixel, []byte("\x1bP0;1;0q\"1;1;100;150")) {
		t.Fatalf("unexpected sixel header: %q", string(sixel[:minInt(len(sixel), 40)]))
	}
	if !bytes.HasSuffix(sixel, []byte("\x1b\\")) {
		t.Fatal("expected sixel to terminate with ST (ESC \\)")
	}
	if !bytes.Contains(sixel, []byte("!")) {
		t.Fatal("expected RLE compression tokens in Sixel output")
	}
}

func TestContinuousDeckScrollingAndZeroOverscrollDebt(t *testing.T) {
	// 3 pages of 200x300 (each scales to 200x300 at dstW=200).
	// Total height = 300 + 14 + 300 + 14 + 300 = 928 px.
	imgs := []*image.RGBA{
		makeTestPage(200, 300, color.RGBA{255, 255, 255, 255}, color.RGBA{0, 0, 0, 255}),
		makeTestPage(200, 300, color.RGBA{250, 250, 255, 255}, color.RGBA{0, 0, 180, 255}),
		makeTestPage(200, 300, color.RGBA{255, 250, 250, 255}, color.RGBA{180, 0, 0, 255}),
	}
	deck := NewDocumentDeckFromImages(imgs, 5*time.Millisecond)
	defer deck.Close()

	viewH := 250
	vp1, err := deck.RenderViewport(200, viewH, 0, false, false)
	if err != nil {
		t.Fatalf("RenderViewport top failed: %v", err)
	}
	if vp1.FirstPage != 1 || vp1.LastPage != 1 || vp1.PageCount != 3 {
		t.Fatalf("at y=0 want p.1-1/3, got p.%d-%d/%d", vp1.FirstPage, vp1.LastPage, vp1.PageCount)
	}
	wantMax := 928 - viewH
	if vp1.MaxScrollY != wantMax {
		t.Fatalf("want MaxScrollY=%d, got %d", wantMax, vp1.MaxScrollY)
	}

	// Scroll across page 1 -> page 2 boundary (y=200 shows [200..450], overlapping p1 & p2).
	vpCross, err := deck.RenderViewport(200, viewH, 200, false, false)
	if err != nil {
		t.Fatalf("RenderViewport boundary failed: %v", err)
	}
	if vpCross.FirstPage != 1 || vpCross.LastPage != 2 {
		t.Fatalf("at y=200 want p.1-2, got p.%d-%d", vpCross.FirstPage, vpCross.LastPage)
	}

	// Attempt massive overscroll past the bottom -> must clamp strictly to MaxScrollY.
	vpEnd, err := deck.RenderViewport(200, viewH, 99999, false, false)
	if err != nil {
		t.Fatalf("RenderViewport end failed: %v", err)
	}
	if vpEnd.ScrollY != wantMax {
		t.Fatalf("expected strict clamp to %d, got %d", wantMax, vpEnd.ScrollY)
	}
	if vpEnd.FirstPage != 3 || vpEnd.LastPage != 3 {
		t.Fatalf("at bottom want p.3-3, got p.%d-%d", vpEnd.FirstPage, vpEnd.LastPage)
	}
}

type fakeCompiler struct {
	mu    sync.Mutex
	calls int
	pages int
}

func (f *fakeCompiler) CompilePages(ctx context.Context, req CompileRequest) (*DocumentDeck, error) {
	f.mu.Lock()
	f.calls++
	n := f.pages
	f.mu.Unlock()

	imgs := make([]*image.RGBA, n)
	for i := 0; i < n; i++ {
		imgs[i] = makeTestPage(400, 600, color.RGBA{255, 255, 255, 255}, color.RGBA{10, 10, 10, 255})
	}
	return NewDocumentDeckFromImages(imgs, 2*time.Millisecond), nil
}

func (f *fakeCompiler) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func initPreviewTestEnv(t *testing.T) (tcell.SimulationScreen, *action.BufPane) {
	t.Helper()
	config.InitRuntimeFiles(true)
	config.GlobalSettings = config.DefaultAllSettings()
	config.GlobalSettings["typst.debounceMs"] = float64(20)
	config.GlobalSettings["typst.previewWidth"] = float64(50)
	if ulua.L == nil {
		ulua.L = lua.NewState()
	}

	sim, err := screen.InitSimScreen()
	if err != nil {
		t.Fatalf("InitSimScreen: %v", err)
	}
	sim.SetSize(120, 36)

	action.InitGlobals()
	buf := buffer.NewBufferFromString("= Hello TypX\n\n$a in A$\n", "demo.typ", buffer.BTDefault)
	buf.Settings["filetype"] = "typst"
	action.InitTabs([]*buffer.Buffer{buf})
	action.Tabs.Resize()

	t.Cleanup(func() {
		Shutdown()
		SetOutput(nil)
		SetCompiler(&TinymistCompiler{})
		if screen.Screen != nil {
			screen.Screen.Fini()
			screen.Screen = nil
		}
	})
	return sim, action.MainTab().CurPane()
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		action.DrainUIQueue()
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for condition after %v", timeout)
}

func TestPreviewPaneLifecycleScrollAndEmission(t *testing.T) {
	sim, bp := initPreviewTestEnv(t)
	SetCapsForTest(TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})

	fc := &fakeCompiler{pages: 3}
	SetCompiler(fc)
	var outBuf bytes.Buffer
	SetOutput(&outBuf)
	Init()

	pp, ok := Open(bp, false)
	if !ok || pp == nil {
		t.Fatal("preview.Open failed")
	}
	if len(action.MainTab().Panes) != 2 {
		t.Fatalf("expected 2 panes in tab, got %d", len(action.MainTab().Panes))
	}

	// Wait for initial async compile to land on UI queue.
	waitForCondition(t, 2*time.Second, func() bool {
		return !pp.IsCompiling() && pp.viewport != nil
	})
	if fc.CallCount() != 1 {
		t.Fatalf("expected 1 compile call, got %d", fc.CallCount())
	}

	// Simulate one editor Display + Show + PostShow frame -> should emit synchronized Sixel once.
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()

	emitted1 := outBuf.String()
	if !strings.Contains(emitted1, "\x1b[?2026h") ||
		!strings.Contains(emitted1, "\x1b7") ||
		!strings.Contains(emitted1, "\x1bP0;1;0q") ||
		!strings.Contains(emitted1, "\x1b8") ||
		!strings.Contains(emitted1, "\x1b[?2026l") {
		t.Fatalf("expected synchronized DECSC/Sixel/DECRC frame, got len=%d", len(emitted1))
	}

	// Second frame with no preview changes -> MUST emit zero bytes (no flicker).
	outBuf.Reset()
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	if outBuf.Len() != 0 {
		t.Fatalf("expected zero Sixel bytes on idle editor redraw, got %d bytes", outBuf.Len())
	}

	// Scroll to the very bottom with repeated excess ScrollBy calls -> zero overscroll debt!
	maxY := pp.MaxScrollY()
	if maxY <= 0 {
		t.Fatalf("expected multi-page preview to have MaxScrollY > 0, got %d", maxY)
	}
	for i := 0; i < 40; i++ {
		pp.ScrollBy(200)
	}
	if pp.ScrollY() != maxY {
		t.Fatalf("expected ScrollY clamped to %d, got %d", maxY, pp.ScrollY())
	}
	// Very first upward scroll MUST immediately move up (no overscroll debt).
	if !pp.ScrollBy(-scrollStepPx) {
		t.Fatal("expected first scroll up from bottom to move immediately")
	}
	if pp.ScrollY() != maxY-scrollStepPx {
		t.Fatalf("expected ScrollY=%d after 1 scroll up, got %d", maxY-scrollStepPx, pp.ScrollY())
	}

	// Wheel event on PreviewPane updates scroll and marks sixel dirty.
	outBuf.Reset()
	evWheel := tcell.NewEventMouse(pp.GetView().X+2, pp.GetView().Y+2, tcell.WheelUp, 0, "")
	action.MainTab().HandleEvent(evWheel)
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	if outBuf.Len() == 0 {
		t.Fatal("expected wheel scroll to emit updated Sixel frame")
	}

	// Toggle invert and trim via keyboard when focused.
	pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 't', 0, ""))
	if !pp.Invert() {
		t.Fatal("expected 't' to toggle dark inversion on")
	}
	pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'm', 0, ""))
	if !pp.TrimMargins() {
		t.Fatal("expected 'm' to toggle margin trim on")
	}

	// Pressing 'q' closes the preview pane and restores single-pane tab.
	pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'q', 0, ""))
	if PaneForTab(action.MainTab()) != nil {
		t.Fatal("expected preview pane to close on 'q'")
	}
	if len(action.MainTab().Panes) != 1 {
		t.Fatalf("expected 1 pane after closing preview, got %d", len(action.MainTab().Panes))
	}
}

type failingCompiler struct {
	err error
}

func (f *failingCompiler) CompilePages(ctx context.Context, req CompileRequest) (*DocumentDeck, error) {
	return nil, f.err
}

func TestPreviewPaneSurfacesCompileError(t *testing.T) {
	sim, bp := initPreviewTestEnv(t)
	SetCapsForTest(TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})

	SetCompiler(&failingCompiler{err: errors.New("tinymist binary not found on PATH")})
	Init()

	pp, ok := Open(bp, false)
	if !ok || pp == nil {
		t.Fatal("preview.Open failed")
	}

	waitForCondition(t, 2*time.Second, func() bool {
		return !pp.IsCompiling()
	})

	if got := pp.LastError(); got != "tinymist binary not found on PATH" {
		t.Fatalf("expected compile error to be surfaced, got %q", got)
	}

	sim.Clear()
	pp.Display()
	sim.Show()

	var rendered strings.Builder
	v := pp.GetView()
	for y := v.Y; y < v.Y+v.Height; y++ {
		for x := v.X; x < v.X+v.Width; x++ {
			r, _, _, _ := sim.GetContent(x, y)
			if r == 0 {
				r = ' '
			}
			rendered.WriteRune(r)
		}
		rendered.WriteByte('\n')
	}
	text := rendered.String()
	if !strings.Contains(text, "Preview compile error:") {
		t.Fatalf("expected error header in preview pane, got:\n%s", text)
	}
	if !strings.Contains(text, "tinymist binary not found") {
		t.Fatalf("expected error detail in preview pane, got:\n%s", text)
	}
	if strings.Contains(text, "Waiting for Typst document") {
		t.Fatalf("preview pane should not fall back to Waiting placeholder on compile error:\n%s", text)
	}
}

func TestPreviewGeometryShiftTriggersFullScreenSync(t *testing.T) {
	sim, bp := initPreviewTestEnv(t)
	SetCapsForTest(TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})

	SetCompiler(&fakeCompiler{pages: 2})
	var outBuf bytes.Buffer
	SetOutput(&outBuf)
	Init()

	pp, ok := Open(bp, false)
	if !ok || pp == nil {
		t.Fatal("preview.Open failed")
	}

	waitForCondition(t, 2*time.Second, func() bool {
		return !pp.IsCompiling() && pp.viewport != nil
	})

	// Initial emit should not need a full screen sync because no prior Sixel was drawn.
	defaultManager.pendingSync = false
	pp.Display()
	if defaultManager.pendingSync {
		t.Fatal("initial preview Display should not set pendingSync before first emit")
	}
	sim.Show()
	screen.RunPostShowHooks()
	if outBuf.Len() == 0 {
		t.Fatal("expected initial Sixel emission")
	}
	origX := pp.GetView().X
	origW := pp.GetView().Width

	// Dock a left sidebar (like Outline) so Preview shifts right and narrows.
	tab := action.MainTab()
	if tab.Node.DockLeft(28) == 0 {
		t.Fatal("DockLeft failed")
	}
	tab.Resize()
	if pp.GetView().X <= origX || pp.GetView().Width >= origW {
		t.Fatalf("expected preview to shift right and narrow: before=(X=%d,W=%d) after=(X=%d,W=%d)",
			origX, origW, pp.GetView().X, pp.GetView().Width)
	}

	outBuf.Reset()
	pp.Display()
	if !defaultManager.pendingSync {
		t.Fatal("expected pendingSync=true when previously emitted preview shifts geometry")
	}
	sim.Show()
	screen.RunPostShowHooks()
	if defaultManager.pendingSync {
		t.Fatal("expected postShow to consume pendingSync")
	}
	if outBuf.Len() == 0 {
		t.Fatal("expected Sixel re-emission after geometry shift sync")
	}

	// Subsequent idle frame must not trigger another sync or Sixel write.
	outBuf.Reset()
	pp.Display()
	if defaultManager.pendingSync {
		t.Fatal("idle frame after geometry shift should not re-trigger pendingSync")
	}
	sim.Show()
	screen.RunPostShowHooks()
	if outBuf.Len() != 0 {
		t.Fatalf("expected zero bytes on idle frame after shift, got %d", outBuf.Len())
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
