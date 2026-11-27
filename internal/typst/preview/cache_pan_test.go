package preview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/micro-editor/tcell/v2"
	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/screen"
)

func writeTestPNGFile(t *testing.T, path string, img *image.RGBA) [32]byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode(%s): %v", path, err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return sha256.Sum256(buf.Bytes())
}

func TestLoadDocumentDeckSHA256AndCarryForward(t *testing.T) {
	dir1 := t.TempDir()
	img0 := makeTestPage(200, 300, color.RGBA{255, 255, 255, 255}, color.RGBA{10, 20, 30, 255})
	img1 := makeTestPage(200, 300, color.RGBA{250, 250, 255, 255}, color.RGBA{20, 40, 120, 255})
	img2 := makeTestPage(200, 300, color.RGBA{255, 250, 250, 255}, color.RGBA{160, 20, 20, 255})
	imgNew := makeTestPage(200, 300, color.RGBA{245, 255, 245, 255}, color.RGBA{0, 140, 40, 255})

	h0 := writeTestPNGFile(t, filepath.Join(dir1, "page-1.png"), img0)
	h1 := writeTestPNGFile(t, filepath.Join(dir1, "page-2.png"), img1)
	h2 := writeTestPNGFile(t, filepath.Join(dir1, "page-3.png"), img2)

	oldDeck, err := LoadDocumentDeckFromDir(dir1, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("LoadDocumentDeckFromDir(dir1): %v", err)
	}
	if got, ok := oldDeck.PageHash(0); !ok || got != h0 {
		t.Fatalf("PageHash(0) = (%x, %v), want (%x, true)", got, ok, h0)
	}
	if got, ok := oldDeck.PageHash(1); !ok || got != h1 {
		t.Fatalf("PageHash(1) = (%x, %v), want (%x, true)", got, ok, h1)
	}
	if got, ok := oldDeck.PageHash(2); !ok || got != h2 {
		t.Fatalf("PageHash(2) = (%x, %v), want (%x, true)", got, ok, h2)
	}
	if _, ok := oldDeck.PageHash(-1); ok {
		t.Fatal("expected PageHash(-1) to return false")
	}
	if _, ok := oldDeck.PageHash(3); ok {
		t.Fatal("expected PageHash(3) to return false")
	}

	// Render across page 0 and page 1 only, leaving page 2 lazily uncached (raw == nil).
	if _, err := oldDeck.RenderViewportWithProtocol(200, 350, 0, false, false, ProtocolSixel); err != nil {
		t.Fatalf("RenderViewportWithProtocol: %v", err)
	}
	if oldDeck.pages[0].raw == nil || oldDeck.pages[0].scaled == nil {
		t.Fatal("expected page 0 raw and scaled cached")
	}
	if oldDeck.pages[1].raw == nil || oldDeck.pages[1].scaled == nil {
		t.Fatal("expected page 1 raw and scaled cached")
	}
	if oldDeck.pages[2].raw != nil || oldDeck.pages[2].scaled != nil {
		t.Fatal("expected offscreen page 2 to remain lazily undecoded")
	}

	oldRaw0, oldScaled0 := oldDeck.pages[0].raw, oldDeck.pages[0].scaled
	oldRaw1, oldScaled1 := oldDeck.pages[1].raw, oldDeck.pages[1].scaled

	// New compile inserts a new page at index 1, shifting old page 1 -> index 2 and old page 2 -> index 3.
	dir2 := t.TempDir()
	writeTestPNGFile(t, filepath.Join(dir2, "page-1.png"), img0)
	writeTestPNGFile(t, filepath.Join(dir2, "page-2.png"), imgNew)
	writeTestPNGFile(t, filepath.Join(dir2, "page-3.png"), img1)
	writeTestPNGFile(t, filepath.Join(dir2, "page-4.png"), img2)

	newDeck, err := LoadDocumentDeckFromDir(dir2, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("LoadDocumentDeckFromDir(dir2): %v", err)
	}
	defer newDeck.Close()

	carried := newDeck.CarryForwardFrom(oldDeck)
	oldDeck.Close()

	if carried != 2 {
		t.Fatalf("expected 2 carried pages (0 and 2), got %d", carried)
	}
	if newDeck.pages[0].raw != oldRaw0 || newDeck.pages[0].scaled != oldScaled0 {
		t.Fatal("expected page 0 to reuse exact raw and scaled pointers")
	}
	if newDeck.pages[2].raw != oldRaw1 || newDeck.pages[2].scaled != oldScaled1 {
		t.Fatal("expected shifted page 2 to reuse exact raw and scaled pointers from old page 1")
	}
	if newDeck.pages[1].raw != nil || newDeck.pages[3].raw != nil {
		t.Fatal("expected new/uncached pages 1 and 3 to remain lazily undecoded")
	}

	// Lazy decode of page 3 after oldDeck.Close() must still work using dir2's pngPath.
	rgba3, err := newDeck.DecodePageRGBA(3)
	if err != nil {
		t.Fatalf("DecodePageRGBA(3) after oldDeck.Close() failed: %v", err)
	}
	if rgba3.Bounds().Dx() != 200 || rgba3.Bounds().Dy() != 300 {
		t.Fatalf("unexpected decoded bounds for page 3: %v", rgba3.Bounds())
	}
}

func TestRenderViewportCachedRedrawSkipKittyAndSixel(t *testing.T) {
	for _, proto := range []GraphicsProtocol{ProtocolSixel, ProtocolKitty} {
		t.Run(proto.String(), func(t *testing.T) {
			p1 := makeTestPage(200, 300, color.RGBA{255, 255, 255, 255}, color.RGBA{0, 0, 0, 255})
			p2 := makeTestPage(200, 300, color.RGBA{255, 255, 255, 255}, color.RGBA{0, 0, 180, 255})
			p3 := makeTestPage(200, 300, color.RGBA{255, 255, 255, 255}, color.RGBA{180, 0, 0, 255})

			deck1 := NewDocumentDeckFromImages([]*image.RGBA{p1, p2}, 2*time.Millisecond)
			defer deck1.Close()

			vp1, err := deck1.RenderViewportCached(200, 200, 250, 0, 0, false, false, proto, nil)
			if err != nil {
				t.Fatalf("initial RenderViewportCached: %v", err)
			}
			if vp1.Reused || !vp1.HasHash || len(vp1.Payload) == 0 {
				t.Fatalf("expected fresh non-empty frame with hash, got Reused=%v HasHash=%v len=%d", vp1.Reused, vp1.HasHash, len(vp1.Payload))
			}

			// Recompiled deck adds offscreen page 3 while page 1 in viewport is identical.
			deck2 := NewDocumentDeckFromImages([]*image.RGBA{p1, p2, p3}, 2*time.Millisecond)
			defer deck2.Close()
			deck2.CarryForwardFrom(deck1)

			vp2, err := deck2.RenderViewportCached(200, 200, 250, 0, 0, false, false, proto, vp1)
			if err != nil {
				t.Fatalf("cached RenderViewportCached: %v", err)
			}
			if !vp2.Reused {
				t.Fatal("expected Reused=true when visible viewport is pixel-identical across recompile")
			}
			if vp2.PageCount != 3 || vp2.MaxScrollY <= vp1.MaxScrollY {
				t.Fatalf("expected updated PageCount=3 and larger MaxScrollY, got PageCount=%d MaxScrollY=%d (was %d)", vp2.PageCount, vp2.MaxScrollY, vp1.MaxScrollY)
			}
			if &vp2.Payload[0] != &vp1.Payload[0] {
				t.Fatal("expected exact Payload slice reuse on cache hit")
			}

			// Changing scrollY, scrollX, zoom/scaledW, invert, trim, or protocol must miss cache.
			if vpMiss, err := deck2.RenderViewportCached(200, 200, 250, 20, 0, false, false, proto, vp2); err != nil || vpMiss.Reused {
				t.Fatalf("scrollY change should miss cache (err=%v, reused=%v)", err, vpMiss.Reused)
			}
			if vpMiss, err := deck2.RenderViewportCached(300, 200, 250, 0, 40, false, false, proto, vp2); err != nil || vpMiss.Reused {
				t.Fatalf("scaledW/scrollX change should miss cache (err=%v, reused=%v)", err, vpMiss.Reused)
			}
			if vpMiss, err := deck2.RenderViewportCached(200, 200, 250, 0, 0, true, false, proto, vp2); err != nil || vpMiss.Reused {
				t.Fatalf("invert change should miss cache (err=%v, reused=%v)", err, vpMiss.Reused)
			}
			if vpMiss, err := deck2.RenderViewportCached(200, 200, 250, 0, 0, false, true, proto, vp2); err != nil || vpMiss.Reused {
				t.Fatalf("trim change should miss cache (err=%v, reused=%v)", err, vpMiss.Reused)
			}
			otherProto := ProtocolKitty
			if proto == ProtocolKitty {
				otherProto = ProtocolSixel
			}
			if vpMiss, err := deck2.RenderViewportCached(200, 200, 250, 0, 0, false, false, otherProto, vp2); err != nil || vpMiss.Reused {
				t.Fatalf("protocol change should miss cache (err=%v, reused=%v)", err, vpMiss.Reused)
			}
		})
	}
}

type mutablePageCompiler struct {
	mu    sync.Mutex
	calls int
	pages []*image.RGBA
}

func (m *mutablePageCompiler) CompilePages(ctx context.Context, req CompileRequest) (*DocumentDeck, error) {
	m.mu.Lock()
	m.calls++
	pages := append([]*image.RGBA(nil), m.pages...)
	m.mu.Unlock()
	return NewDocumentDeckFromImages(pages, 2*time.Millisecond), nil
}

func (m *mutablePageCompiler) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func TestPreviewPaneRedrawSkipAcrossRecompileAndManualReload(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps TerminalCaps
	}{
		{name: "Sixel", caps: TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true}},
		{name: "Kitty", caps: TerminalCaps{Kitty: true, CellW: 10, CellH: 20, Source: "test", Probed: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sim, bp := initPreviewTestEnv(t)
			SetCapsForTest(tc.caps)
			t.Cleanup(func() { SetCapsForTest(TerminalCaps{}) })

			p1 := makeTestPage(400, 600, color.RGBA{255, 255, 255, 255}, color.RGBA{10, 10, 10, 255})
			p2 := makeTestPage(400, 600, color.RGBA{255, 255, 255, 255}, color.RGBA{10, 10, 180, 255})
			p3 := makeTestPage(400, 600, color.RGBA{255, 255, 255, 255}, color.RGBA{180, 10, 10, 255})

			mc := &mutablePageCompiler{pages: []*image.RGBA{p1, p2}}
			SetCompiler(mc)
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

			// Initial frame emits graphics payload.
			pp.Display()
			sim.Show()
			screen.RunPostShowHooks()
			if outBuf.Len() == 0 {
				t.Fatal("expected initial frame emission")
			}

			// Recompile with offscreen page 3 added (visible page 1 unchanged) -> 0 bytes emitted!
			mc.mu.Lock()
			mc.pages = []*image.RGBA{p1, p2, p3}
			mc.mu.Unlock()
			outBuf.Reset()
			pp.ScheduleCompile(true)
			waitForCondition(t, 2*time.Second, func() bool {
				return mc.CallCount() >= 2 && !pp.IsCompiling()
			})
			pp.Display()
			sim.Show()
			screen.RunPostShowHooks()
			if outBuf.Len() != 0 {
				t.Fatalf("expected 0 emitted bytes when recompiled viewport is pixel-identical, got %d", outBuf.Len())
			}
			if _, _, total := pp.PageRange(); total != 3 {
				t.Fatalf("expected PageRange total updated to 3 despite skipped redraw, got %d", total)
			}

			// Manual 'r' reload MUST still force emission even when pixels are identical.
			outBuf.Reset()
			pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'r', 0, ""))
			waitForCondition(t, 2*time.Second, func() bool {
				return mc.CallCount() >= 3 && !pp.IsCompiling()
			})
			pp.Display()
			sim.Show()
			screen.RunPostShowHooks()
			if outBuf.Len() == 0 {
				t.Fatal("expected manual 'r' reload to re-emit frame even with identical pixels")
			}
		})
	}
}

func TestCompositeViewportHorizontalCropAndZoomPan(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	red := color.RGBA{R: 220, G: 20, B: 20, A: 255}
	blue := color.RGBA{R: 20, G: 20, B: 220, A: 255}
	draw.Draw(img, image.Rect(0, 0, 100, 100), &image.Uniform{C: red}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(100, 0, 200, 100), &image.Uniform{C: blue}, image.Point{}, draw.Src)

	deck := NewDocumentDeckFromImages([]*image.RGBA{img}, time.Millisecond)
	defer deck.Close()

	vcLeft, err := deck.CompositeViewport(200, 100, 100, 0, 0, false, false)
	if err != nil {
		t.Fatalf("CompositeViewport left: %v", err)
	}
	if vcLeft.DstW != 100 || vcLeft.ScaledW != 200 || vcLeft.ScrollX != 0 || vcLeft.MaxScrollX != 100 {
		t.Fatalf("unexpected left metrics: %+v", vcLeft)
	}
	if got := vcLeft.Image.RGBAAt(10, 50); got != red {
		t.Fatalf("expected left pixel red %+v, got %+v", red, got)
	}

	vcRight, err := deck.CompositeViewport(200, 100, 100, 0, 9999, false, false)
	if err != nil {
		t.Fatalf("CompositeViewport right: %v", err)
	}
	if vcRight.ScrollX != 100 || vcRight.MaxScrollX != 100 {
		t.Fatalf("expected ScrollX clamped to 100, got %d (max %d)", vcRight.ScrollX, vcRight.MaxScrollX)
	}
	if got := vcRight.Image.RGBAAt(10, 50); got != blue {
		t.Fatalf("expected panned pixel blue %+v, got %+v", blue, got)
	}
}

func TestPreviewPaneZoom250PanClampingKeyRoutingAndFooter(t *testing.T) {
	sim, bp := initPreviewTestEnv(t)
	SetCapsForTest(TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})
	t.Cleanup(func() { SetCapsForTest(TerminalCaps{}) })

	fc := &fakeCompiler{pages: 3}
	SetCompiler(fc)
	var outBuf bytes.Buffer
	SetOutput(&outBuf)
	Init()

	pp, ok := Open(bp, true)
	if !ok || pp == nil {
		t.Fatal("Open failed")
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return !pp.IsCompiling() && pp.viewport != nil
	})

	// At default 100% zoom, MaxScrollX == 0 and Right/Left jump pages.
	if pp.ZoomPct() != 100 || pp.MaxScrollX() != 0 {
		t.Fatalf("expected initial zoom=100 MaxScrollX=0, got zoom=%d maxX=%d", pp.ZoomPct(), pp.MaxScrollX())
	}
	pp.HandleEvent(tcell.NewEventKey(tcell.KeyRight, 0, 0, ""))
	if fp, _, _ := pp.PageRange(); fp != 2 {
		t.Fatalf("expected KeyRight at 100%% zoom to jump to page 2, got p.%d", fp)
	}
	pp.HandleEvent(tcell.NewEventKey(tcell.KeyLeft, 0, 0, ""))
	if fp, _, _ := pp.PageRange(); fp != 1 {
		t.Fatalf("expected KeyLeft at 100%% zoom to jump back to page 1, got p.%d", fp)
	}

	// Zoom up to 250% cap.
	for i := 0; i < 20; i++ {
		pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '+', 0, ""))
	}
	if pp.ZoomPct() != 250 {
		t.Fatalf("expected zoom capped at 250%%, got %d%%", pp.ZoomPct())
	}
	if pp.MaxScrollX() <= 0 {
		t.Fatalf("expected MaxScrollX > 0 at 250%% zoom, got %d", pp.MaxScrollX())
	}

	// Footer shows zoom percentage when zoomed != 100%.
	sim.Clear()
	pp.drawFooter(pp.view.Y + pp.view.Height - 1)
	sim.Show()
	var footer strings.Builder
	for x := pp.view.X; x < pp.view.X+pp.view.Width; x++ {
		r, _, _, _ := sim.GetContent(x, pp.view.Y+pp.view.Height-1)
		footer.WriteRune(r)
	}
	if !strings.Contains(footer.String(), "z:250%") {
		t.Fatalf("expected footer to display z:250%%, got %q", footer.String())
	}

	// At 250% zoom, KeyRight / 'l' pan horizontally while '[' / ']' still jump pages.
	pp.HandleEvent(tcell.NewEventKey(tcell.KeyRight, 0, 0, ""))
	if pp.ScrollX() <= 0 {
		t.Fatalf("expected KeyRight at 250%% zoom to pan right, got ScrollX=%d", pp.ScrollX())
	}
	if fp, _, _ := pp.PageRange(); fp != 1 {
		t.Fatalf("expected KeyRight pan to stay on page 1, got p.%d", fp)
	}
	prevX := pp.ScrollX()
	pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'l', 0, ""))
	if pp.ScrollX() <= prevX {
		t.Fatalf("expected 'l' to pan further right from %d, got %d", prevX, pp.ScrollX())
	}
	pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'h', 0, ""))
	if pp.ScrollX() != prevX {
		t.Fatalf("expected 'h' to pan left back to %d, got %d", prevX, pp.ScrollX())
	}
	pp.HandleEvent(tcell.NewEventKey(tcell.KeyLeft, 0, 0, ""))
	if pp.ScrollX() != 0 {
		t.Fatalf("expected KeyLeft to pan back to 0, got %d", pp.ScrollX())
	}

	// ']' / '[' jump pages regardless of zoom level.
	pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, ']', 0, ""))
	if fp, _, _ := pp.PageRange(); fp != 2 {
		t.Fatalf("expected ']' at 250%% zoom to jump to page 2, got p.%d", fp)
	}
	pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '[', 0, ""))
	if fp, _, _ := pp.PageRange(); fp != 1 {
		t.Fatalf("expected '[' at 250%% zoom to jump to page 1, got p.%d", fp)
	}

	// Pan to right edge, then zoom out one step -> ScrollX clamps cleanly to new MaxScrollX.
	for i := 0; i < 20; i++ {
		pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'l', 0, ""))
	}
	maxAt250 := pp.MaxScrollX()
	if pp.ScrollX() != maxAt250 {
		t.Fatalf("expected ScrollX at MaxScrollX=%d, got %d", maxAt250, pp.ScrollX())
	}
	pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '-', 0, ""))
	if pp.ZoomPct() != 235 {
		t.Fatalf("expected zoom 235%% after '-', got %d%%", pp.ZoomPct())
	}
	if pp.ScrollX() != pp.MaxScrollX() || pp.ScrollX() >= maxAt250 {
		t.Fatalf("expected ScrollX clamped to new MaxScrollX (%d < %d), got %d", pp.MaxScrollX(), maxAt250, pp.ScrollX())
	}

	// Override saves and restores ScrollX.
	savedX := pp.ScrollX()
	pp.ShowOverrideImages("tmpl", []*image.RGBA{makeTestPage(200, 300, color.RGBA{255, 255, 255, 255}, color.RGBA{0, 0, 0, 255})})
	if pp.ScrollX() != 0 {
		t.Fatalf("expected override ScrollX=0, got %d", pp.ScrollX())
	}
	pp.ClearOverride()
	if pp.ScrollX() != savedX {
		t.Fatalf("expected ClearOverride to restore ScrollX=%d, got %d", savedX, pp.ScrollX())
	}

	// '0' resets zoom to 100% and ScrollX to 0.
	pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '0', 0, ""))
	if pp.ZoomPct() != 100 || pp.ScrollX() != 0 || pp.MaxScrollX() != 0 {
		t.Fatalf("expected '0' reset (zoom=100, scrollX=0, maxX=0), got zoom=%d scrollX=%d maxX=%d", pp.ZoomPct(), pp.ScrollX(), pp.MaxScrollX())
	}

	// Zoom back up, pan right, narrow pane via '>' -> ScrollX clamps cleanly on resize.
	for i := 0; i < 6; i++ {
		pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '+', 0, ""))
	}
	for i := 0; i < 20; i++ {
		pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'l', 0, ""))
	}
	maxBeforeResize := pp.MaxScrollX()
	if pp.ScrollX() != maxBeforeResize || maxBeforeResize <= 0 {
		t.Fatalf("expected ScrollX at MaxScrollX (%d > 0), got %d", maxBeforeResize, pp.ScrollX())
	}
	if !pp.ResizePaneBy(-resizeStepCols) {
		t.Fatal("expected ResizePaneBy(-resizeStepCols) to narrow preview")
	}
	if pp.MaxScrollX() >= maxBeforeResize || pp.ScrollX() != pp.MaxScrollX() {
		t.Fatalf("expected ScrollX clamped to narrower MaxScrollX (%d < %d), got %d", pp.MaxScrollX(), maxBeforeResize, pp.ScrollX())
	}

	// Switching to a second Typst buffer resets ScrollX to 0.
	b2 := buffer.NewBufferFromString("= Second Doc\n", "second.typ", buffer.BTDefault)
	b2.Settings["filetype"] = "typst"
	bp2 := action.NewBufPaneFromBuf(b2, action.MainTab())
	action.MainTab().AddPane(bp2, 0)
	defaultManager.onPaneFocus(action.MainTab(), bp2)
	if pp.ScrollX() != 0 {
		t.Fatalf("expected target switch to reset ScrollX=0, got %d", pp.ScrollX())
	}
}
