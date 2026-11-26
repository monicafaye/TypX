package preview

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/micro-editor/tcell/v2"
	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/screen"
)

func decodeKittyPayloadPNG(t *testing.T, seq []byte) image.Image {
	t.Helper()
	parts := bytes.Split(seq, []byte("\x1b\\"))
	var b64 bytes.Buffer
	chunkCount := 0
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		if !bytes.HasPrefix(p, []byte("\x1b_G")) {
			t.Fatalf("expected Kitty APC prefix, got %q", string(p[:minInt(len(p), 32)]))
		}
		semi := bytes.IndexByte(p, ';')
		if semi < 0 {
			t.Fatalf("missing ';' separator in Kitty chunk %d", chunkCount)
		}
		header := string(p[3:semi])
		payload := p[semi+1:]
		if chunkCount == 0 {
			if !strings.Contains(header, "a=T") ||
				!strings.Contains(header, "f=100") ||
				!strings.Contains(header, "i=1") ||
				!strings.Contains(header, "p=1") ||
				!strings.Contains(header, "q=2") ||
				!strings.Contains(header, "C=1") {
				t.Fatalf("unexpected first chunk header: %q", header)
			}
		}
		if len(payload) > kittyChunkSize {
			t.Fatalf("chunk %d exceeded max size %d: got %d", chunkCount, kittyChunkSize, len(payload))
		}
		if strings.Contains(header, "m=1") && len(payload)%4 != 0 {
			t.Fatalf("non-final chunk %d payload length %d must be a multiple of 4", chunkCount, len(payload))
		}
		b64.Write(payload)
		chunkCount++
	}
	if chunkCount == 0 {
		t.Fatal("decoded zero Kitty chunks")
	}

	pngBytes, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("png decode failed: %v", err)
	}
	return img
}

func TestEncodeKittySingleAndMultiChunk(t *testing.T) {
	if EncodeKitty(nil) != nil {
		t.Fatal("expected nil for nil image")
	}
	if EncodeKitty(image.NewRGBA(image.Rect(0, 0, 0, 0))) != nil {
		t.Fatal("expected nil for empty image")
	}

	// Small 16x16 image with non-Sixel-quantized 24-bit color -> single chunk, lossless PNG.
	small := image.NewRGBA(image.Rect(0, 0, 16, 16))
	wantCol := color.RGBA{R: 123, G: 77, B: 211, A: 255}
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			small.SetRGBA(x, y, wantCol)
		}
	}
	seqSmall := EncodeKitty(small)
	if !bytes.HasPrefix(seqSmall, []byte("\x1b_Ga=T,f=100,i=1,p=1,q=2,C=1,m=0;")) {
		t.Fatalf("expected single-chunk m=0 prefix, got %q", string(seqSmall[:minInt(len(seqSmall), 48)]))
	}
	decodedSmall := decodeKittyPayloadPNG(t, seqSmall)
	if decodedSmall.Bounds().Dx() != 16 || decodedSmall.Bounds().Dy() != 16 {
		t.Fatalf("expected 16x16 decoded PNG, got %v", decodedSmall.Bounds())
	}
	r, g, b, _ := decodedSmall.At(5, 5).RGBA()
	if uint8(r>>8) != wantCol.R || uint8(g>>8) != wantCol.G || uint8(b>>8) != wantCol.B {
		t.Fatalf("expected exact 24-bit color %v, got (%d,%d,%d)", wantCol, r>>8, g>>8, b>>8)
	}

	// Large varied 240x240 image -> forces multiple 4096-byte base64 chunks.
	big := image.NewRGBA(image.Rect(0, 0, 240, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 240; x++ {
			big.SetRGBA(x, y, color.RGBA{
				R: uint8((x*7 + y*13) & 0xff),
				G: uint8((x*17 ^ y*29) & 0xff),
				B: uint8((x*31 + y*5) & 0xff),
				A: 255,
			})
		}
	}
	seqBig := EncodeKitty(big)
	if !bytes.HasPrefix(seqBig, []byte("\x1b_Ga=T,f=100,i=1,p=1,q=2,C=1,m=1;")) {
		t.Fatalf("expected multi-chunk m=1 prefix, got %q", string(seqBig[:minInt(len(seqBig), 48)]))
	}
	if !bytes.Contains(seqBig, []byte("\x1b_Gm=0;")) {
		t.Fatal("expected final chunk marker m=0 in multi-chunk Kitty output")
	}
	decodedBig := decodeKittyPayloadPNG(t, seqBig)
	if decodedBig.Bounds().Dx() != 240 || decodedBig.Bounds().Dy() != 240 {
		t.Fatalf("expected 240x240 decoded PNG, got %v", decodedBig.Bounds())
	}
}

func TestParseProbeResponseKittyAndDual(t *testing.T) {
	// Kitty / Ghostty response: CSI 16t + Kitty OK + DA1 without Sixel attr 4.
	respKitty := []byte("\x1b[6;20;10t\x1b_Gi=31;OK\x1b\\\x1b[?62;22c")
	sixel, kitty, cw, ch, src := parseProbeResponse(respKitty, 120, 40)
	if sixel || !kitty || cw != 10 || ch != 20 || src != "CSI16t" {
		t.Fatalf("got sixel=%v kitty=%v cell=%dx%d (%s), want false true 10x20 (CSI16t)", sixel, kitty, cw, ch, src)
	}

	// BEL-terminated Kitty OK response.
	respBEL := []byte("\x1b_Gi=31;OK\x07\x1b[?62c")
	sixel, kitty, _, _, _ = parseProbeResponse(respBEL, 80, 24)
	if sixel || !kitty {
		t.Fatalf("expected BEL-terminated Kitty OK to set kitty=true, got sixel=%v kitty=%v", sixel, kitty)
	}

	// Kitty error response (e.g. EINVAL) must NOT mark kitty=true.
	respErr := []byte("\x1b_Gi=31;EINVAL:unsupported\x1b\\\x1b[?62c")
	sixel, kitty, _, _, _ = parseProbeResponse(respErr, 80, 24)
	if sixel || kitty {
		t.Fatalf("expected error reply to leave kitty=false, got sixel=%v kitty=%v", sixel, kitty)
	}

	// Dual protocol (WezTerm / Konsole): both Kitty OK and DA1 attr 4 -> prefers Kitty.
	respDual := []byte("\x1b[6;18;9t\x1b_Gi=31;OK\x1b\\\x1b[?65;4;22c")
	sixel, kitty, cw, ch, _ = parseProbeResponse(respDual, 120, 40)
	if !sixel || !kitty || cw != 9 || ch != 18 {
		t.Fatalf("expected both sixel and kitty true, got sixel=%v kitty=%v cell=%dx%d", sixel, kitty, cw, ch)
	}
	capsDual := TerminalCaps{Sixel: sixel, Kitty: kitty, CellW: cw, CellH: ch, Source: "CSI16t", Probed: true}
	if capsDual.Protocol() != ProtocolKitty || !capsDual.SupportsInline() {
		t.Fatalf("expected dual caps to select ProtocolKitty, got %v", capsDual.Protocol())
	}

	// Check() formatting across modes.
	var buf bytes.Buffer
	SetCapsForTest(capsDual)
	Check(&buf)
	if !strings.Contains(buf.String(), "Kitty graphics supported (Sixel also available") {
		t.Fatalf("unexpected Check output for dual caps: %q", buf.String())
	}
	buf.Reset()
	SetCapsForTest(TerminalCaps{Kitty: true, CellW: 10, CellH: 20, Source: "CSI16t", Probed: true})
	Check(&buf)
	if !strings.Contains(buf.String(), "Kitty graphics supported (cell 10x20px") {
		t.Fatalf("unexpected Check output for Kitty caps: %q", buf.String())
	}
	buf.Reset()
	SetCapsForTest(TerminalCaps{Probed: true, Source: "default"})
	Check(&buf)
	if !strings.Contains(buf.String(), "No inline graphics protocol detected") {
		t.Fatalf("unexpected Check output for no graphics: %q", buf.String())
	}
}

func TestEnvIndicatesKittyRespectsMultiplexers(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("STY", "")
	t.Setenv("KITTY_WINDOW_ID", "1")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("TERM", "xterm-256color")

	if ok, name := envIndicatesKitty(); !ok || name != "Kitty" {
		t.Fatalf("expected Kitty detection, got ok=%v name=%q", ok, name)
	}

	// Inside tmux, inherited KITTY_WINDOW_ID must not falsely report Kitty graphics.
	t.Setenv("TMUX", "/tmp/tmux-1000/default,123,0")
	if ok, _ := envIndicatesKitty(); ok {
		t.Fatal("expected envIndicatesKitty=false inside tmux")
	}

	// Ghostty detection outside multiplexers.
	t.Setenv("TMUX", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM_PROGRAM", "ghostty")
	if ok, name := envIndicatesKitty(); !ok || name != "Ghostty" {
		t.Fatalf("expected Ghostty detection, got ok=%v name=%q", ok, name)
	}
	if q := probeQueryForEnv(); q != terminalProbeQuery {
		t.Fatalf("expected full Kitty+DA1 probe query on Ghostty, got %q", q)
	}

	// tmux and macOS Terminal.app must omit the Kitty APC query.
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	if q := probeQueryForEnv(); q != baseProbeQuery {
		t.Fatalf("expected base probe query on Apple_Terminal, got %q", q)
	}
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	if q := probeQueryForEnv(); q != baseProbeQuery {
		t.Fatalf("expected base probe query inside tmux, got %q", q)
	}

	// dumb and linux terminals must skip raw terminal probing entirely.
	t.Setenv("TERM", "dumb")
	if !skipRawTerminalProbe() {
		t.Fatal("expected skipRawTerminalProbe=true for TERM=dumb")
	}
	t.Setenv("TERM", "xterm-256color")
	if skipRawTerminalProbe() {
		t.Fatal("expected skipRawTerminalProbe=false for TERM=xterm-256color")
	}
}

func TestPreviewPaneKittyLifecycleAndExplicitDelete(t *testing.T) {
	sim, bp := initPreviewTestEnv(t)
	SetCapsForTest(TerminalCaps{Kitty: true, CellW: 10, CellH: 20, Source: "test", Probed: true})
	t.Cleanup(func() { SetCapsForTest(TerminalCaps{}) })

	fc := &fakeCompiler{pages: 3}
	SetCompiler(fc)
	var outBuf bytes.Buffer
	SetOutput(&outBuf)
	Init()

	pp, ok := Open(bp, false)
	if !ok || pp == nil {
		t.Fatal("preview.Open failed on Kitty terminal")
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return !pp.IsCompiling() && pp.viewport != nil
	})
	if pp.viewport.Protocol != ProtocolKitty {
		t.Fatalf("expected ProtocolKitty viewport, got %v", pp.viewport.Protocol)
	}

	// 1. Initial frame emits synchronized Kitty APC transmit without Sixel or ED 2.
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	frame1 := outBuf.String()
	if !strings.Contains(frame1, "\x1b[?2026h") ||
		!strings.Contains(frame1, "\x1b_Ga=T,f=100,i=1,p=1,q=2,C=1,m=") ||
		!strings.Contains(frame1, "\x1b[?2026l") {
		t.Fatalf("expected synchronized Kitty frame, got len=%d", len(frame1))
	}
	if strings.Contains(frame1, "\x1bP0;1;0q") {
		t.Fatal("must not emit Sixel on Kitty terminal")
	}
	if strings.Contains(frame1, "\x1b[2J") {
		t.Fatal("initial Kitty frame must not emit ED 2 screen erase")
	}
	if strings.Contains(frame1, kittyDeleteSeq) {
		t.Fatal("initial Kitty frame should not emit a redundant delete before first placement")
	}

	// 2. Idle redraw emits zero bytes.
	outBuf.Reset()
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	if outBuf.Len() != 0 {
		t.Fatalf("expected zero bytes on idle Kitty redraw, got %d", outBuf.Len())
	}

	// 3. Scroll deletes previous Kitty image and transmits replacement atomically.
	outBuf.Reset()
	if !pp.ScrollBy(scrollStepPx) {
		t.Fatal("expected ScrollBy to move viewport")
	}
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	scrollOut := outBuf.String()
	delIdx := strings.Index(scrollOut, kittyDeleteSeq)
	txIdx := strings.Index(scrollOut, "\x1b_Ga=T,f=100,i=1,p=1,q=2,C=1,m=")
	if delIdx < 0 || txIdx < 0 || delIdx >= txIdx {
		t.Fatalf("expected Kitty delete before replacement transmit, delIdx=%d txIdx=%d", delIdx, txIdx)
	}

	// 4. Geometry shift (dock left sidebar) replaces Kitty placement without ED 2 wipe.
	tab := action.MainTab()
	if tab.Node.DockLeft(26) == 0 {
		t.Fatal("DockLeft failed")
	}
	tab.Resize()
	outBuf.Reset()
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	shiftOut := outBuf.String()
	if !strings.Contains(shiftOut, kittyDeleteSeq) || !strings.Contains(shiftOut, "\x1b_Ga=T,f=100,i=1,p=1,q=2,C=1,m=") {
		t.Fatal("expected Kitty delete + re-emit after geometry shift")
	}
	if strings.Contains(shiftOut, "\x1b[2J") {
		t.Fatal("Kitty geometry shift should not emit full-screen ED 2 erase")
	}

	// 5. Mouse divider drag start deletes Kitty image cleanly without ED 2; drag release re-emits.
	dividerX := pp.GetView().X - 1
	dividerY := pp.GetView().Y + 2
	outBuf.Reset()
	tab.HandleEvent(tcell.NewEventMouse(dividerX, dividerY, tcell.Button1, 0, ""))
	sim.Clear()
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	dragStart := outBuf.String()
	if !strings.Contains(dragStart, kittyDeleteSeq) {
		t.Fatalf("expected Kitty delete on drag start, got %q", dragStart)
	}
	if strings.Contains(dragStart, "\x1b_Ga=T") || strings.Contains(dragStart, "\x1b[2J") {
		t.Fatalf("drag start should only delete Kitty image, got %q", dragStart)
	}

	outBuf.Reset()
	tab.HandleEvent(tcell.NewEventMouse(dividerX+4, dividerY, tcell.Button1, 0, ""))
	sim.Clear()
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	if outBuf.Len() != 0 {
		t.Fatalf("expected zero bytes on intermediate drag frame, got %d", outBuf.Len())
	}

	outBuf.Reset()
	tab.HandleEvent(tcell.NewEventMouse(dividerX+4, dividerY, tcell.ButtonNone, 0, ""))
	sim.Clear()
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	if !strings.Contains(outBuf.String(), "\x1b_Ga=T,f=100,i=1,p=1,q=2,C=1,m=") {
		t.Fatal("expected fresh Kitty frame after drag release")
	}

	// 6. Override loading state deletes Kitty image so placeholder text is visible.
	outBuf.Reset()
	ovPane, ok := OpenForOverride(bp, "Gallery Item")
	if !ok || ovPane != pp {
		t.Fatal("expected OpenForOverride to work on Kitty-only terminal")
	}
	sim.Clear()
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	if !strings.Contains(outBuf.String(), kittyDeleteSeq) {
		t.Fatalf("expected ShowOverrideLoading to delete Kitty placement, got %q", outBuf.String())
	}

	// Deliver override image -> emits Kitty frame, then ClearOverride restores document deck.
	outBuf.Reset()
	pp.ShowOverrideImages("Gallery Item", []*image.RGBA{makeTestPage(200, 300, color.RGBA{255, 255, 255, 255}, color.RGBA{10, 20, 30, 255})})
	sim.Clear()
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	if !strings.Contains(outBuf.String(), "\x1b_Ga=T,f=100,i=1,p=1,q=2,C=1,m=") {
		t.Fatal("expected Kitty transmit for override image")
	}
	ClearOverrideForTab(tab)
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()

	// 7. PreFini deletes active Kitty image immediately; PostRestart schedules fresh redraw.
	outBuf.Reset()
	defaultManager.onPreFini()
	if !strings.Contains(outBuf.String(), kittyDeleteSeq) {
		t.Fatalf("expected onPreFini to delete active Kitty image, got %q", outBuf.String())
	}
	outBuf.Reset()
	defaultManager.onPostRestart()
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	if !strings.Contains(outBuf.String(), "\x1b_Ga=T,f=100,i=1,p=1,q=2,C=1,m=") {
		t.Fatal("expected onPostRestart to re-emit Kitty frame")
	}

	// 8. Closing preview pane emits explicit Kitty delete without ED 2, and idle frame emits 0 bytes.
	outBuf.Reset()
	pp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'q', 0, ""))
	sim.Show()
	screen.RunPostShowHooks()
	closeOut := outBuf.String()
	if !strings.Contains(closeOut, kittyDeleteSeq) {
		t.Fatalf("expected explicit Kitty delete on close, got %q", closeOut)
	}
	if strings.Contains(closeOut, "\x1b[2J") {
		t.Fatal("closing Kitty preview should not emit full-screen ED 2")
	}

	outBuf.Reset()
	sim.Show()
	screen.RunPostShowHooks()
	if outBuf.Len() != 0 {
		t.Fatalf("expected zero bytes on idle frame after Kitty close, got %d", outBuf.Len())
	}

	// 9. Shutdown immediately deletes any active Kitty image.
	pp2, ok := Open(bp, false)
	if !ok || pp2 == nil {
		t.Fatal("second preview.Open failed")
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return !pp2.IsCompiling() && pp2.viewport != nil
	})
	pp2.Display()
	sim.Show()
	screen.RunPostShowHooks()

	outBuf.Reset()
	Shutdown()
	if !strings.Contains(outBuf.String(), kittyDeleteSeq) {
		t.Fatalf("expected Shutdown to emit Kitty delete, got %q", outBuf.String())
	}
}

func TestPreviewProtocolOverrideAndLiveSwitch(t *testing.T) {
	sim, bp := initPreviewTestEnv(t)
	// Dual-protocol terminal (e.g. WezTerm / Konsole): auto prefers Kitty.
	SetCapsForTest(TerminalCaps{Kitty: true, Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})
	t.Cleanup(func() {
		SetCapsForTest(TerminalCaps{})
		config.GlobalSettings["typst.previewProtocol"] = "auto"
	})

	SetCompiler(&fakeCompiler{pages: 2})
	var outBuf bytes.Buffer
	SetOutput(&outBuf)
	Init()

	// 1. Setting typst.previewProtocol="sixel" overrides auto Kitty preference.
	config.GlobalSettings["typst.previewProtocol"] = "sixel"
	if got := ActiveProtocol(); got != ProtocolSixel {
		t.Fatalf("expected ActiveProtocol=Sixel when configured, got %v", got)
	}
	// Even with no detected caps, explicit typst.previewProtocol enables SupportsInline.
	SetCapsForTest(TerminalCaps{Probed: true, Source: "default"})
	if !SupportsInline() {
		t.Fatal("expected SupportsInline=true when typst.previewProtocol is explicitly set")
	}

	// Restore dual caps and reset to auto.
	SetCapsForTest(TerminalCaps{Kitty: true, Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})
	config.GlobalSettings["typst.previewProtocol"] = "auto"

	// 2. Open with auto (Kitty), then live-switch to Sixel via ToggleWithProtocol.
	pp, ok := Open(bp, false)
	if !ok || pp == nil {
		t.Fatal("preview.Open failed")
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return !pp.IsCompiling() && pp.viewport != nil
	})
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	if !strings.Contains(outBuf.String(), "\x1b_Ga=T,f=100,i=1,p=1,q=2,C=1,m=") {
		t.Fatal("expected initial dual-protocol frame to use Kitty")
	}

	// Switch open pane from Kitty -> Sixel: must delete Kitty placement before Sixel emit.
	outBuf.Reset()
	if !ToggleWithProtocol(bp, ProtocolSixel) {
		t.Fatal("expected ToggleWithProtocol(ProtocolSixel) to switch open pane")
	}
	if PaneForTab(action.MainTab()) == nil {
		t.Fatal("switching protocol must keep preview pane open")
	}
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	switchToSixel := outBuf.String()
	delIdx := strings.Index(switchToSixel, kittyDeleteSeq)
	sixIdx := strings.Index(switchToSixel, "\x1bP0;1;0q")
	if delIdx < 0 || sixIdx < 0 || delIdx >= sixIdx {
		t.Fatalf("expected Kitty delete before Sixel emit on Kitty->Sixel switch, delIdx=%d sixIdx=%d", delIdx, sixIdx)
	}

	// Switch open pane from Sixel -> Kitty: must trigger synchronized ED 2 wipe + Kitty emit.
	outBuf.Reset()
	if !ToggleWithProtocol(bp, ProtocolKitty) {
		t.Fatal("expected ToggleWithProtocol(ProtocolKitty) to switch back to Kitty")
	}
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	switchToKitty := outBuf.String()
	if !strings.Contains(switchToKitty, "\x1b[2J") || !strings.Contains(switchToKitty, "\x1b_Ga=T,f=100,i=1,p=1,q=2,C=1,m=") {
		t.Fatal("expected synchronized ED 2 wipe + Kitty emit on Sixel->Kitty switch")
	}

	// Calling ToggleWithProtocol with the already-active protocol closes the pane.
	if !ToggleWithProtocol(bp, ProtocolKitty) {
		t.Fatal("expected ToggleWithProtocol on active protocol to close pane")
	}
	if PaneForTab(action.MainTab()) != nil {
		t.Fatal("expected preview pane to close when toggling active protocol")
	}

	// Live global option change (`> set typst.previewProtocol sixel`) updates an open auto pane.
	pp2, ok := Open(bp, false)
	if !ok || pp2 == nil {
		t.Fatal("second preview.Open failed")
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return !pp2.IsCompiling() && pp2.viewport != nil
	})
	pp2.Display()
	sim.Show()
	screen.RunPostShowHooks()
	if pp2.viewport.Protocol != ProtocolKitty {
		t.Fatalf("expected Kitty viewport before option change, got %v", pp2.viewport.Protocol)
	}

	outBuf.Reset()
	config.GlobalSettings["typst.previewProtocol"] = "sixel"
	pp2.Display()
	sim.Show()
	screen.RunPostShowHooks()
	if pp2.viewport.Protocol != ProtocolSixel {
		t.Fatalf("expected Sixel viewport after global option change, got %v", pp2.viewport.Protocol)
	}
	if !strings.Contains(outBuf.String(), kittyDeleteSeq) || !strings.Contains(outBuf.String(), "\x1bP0;1;0q") {
		t.Fatal("expected Kitty delete + Sixel emit after live typst.previewProtocol change")
	}
}
