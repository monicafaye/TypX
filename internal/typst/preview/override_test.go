package preview

import (
	"context"
	"image"
	"image/color"
	"image/draw"
	"testing"
	"time"

	"github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	ulua "github.com/monicafaye/TypX/internal/lua"
	"github.com/monicafaye/TypX/internal/screen"
	lua "github.com/yuin/gopher-lua"
)

type stubOverrideCompiler struct {
	pages int
	calls int
}

func (s *stubOverrideCompiler) CompilePages(ctx context.Context, req CompileRequest) (*DocumentDeck, error) {
	s.calls++
	imgs := make([]*image.RGBA, s.pages)
	for i := 0; i < s.pages; i++ {
		img := image.NewRGBA(image.Rect(0, 0, 200, 300))
		draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)
		imgs[i] = img
	}
	return NewDocumentDeckFromImages(imgs, time.Millisecond), nil
}

func initOverrideTestEnv(t *testing.T) {
	t.Helper()
	if ulua.L == nil {
		ulua.L = lua.NewState()
	}
	config.ConfigDir = t.TempDir()
	config.InitRuntimeFiles(false)
	_ = config.InitGlobalSettings()
	config.GlobalSettings["backup"] = false
	config.GlobalSettings["fastdirty"] = true
	if screen.Screen == nil {
		sim, err := screen.InitSimScreen()
		require.NoError(t, err)
		sim.SetSize(120, 30)
	} else if sim, ok := screen.Screen.(tcell.SimulationScreen); ok {
		sim.SetSize(120, 30)
	}
	Init()
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()
	buffer.SetMessager(action.InfoBar)
	t.Cleanup(func() {
		Shutdown()
		SetCapsForTest(TerminalCaps{})
		SetCompiler(&TinymistCompiler{})
		buffer.CloseOpenBuffers()
		action.DrainUIQueue()
	})
}

func TestOverrideAutoOpenLifecycleAndNonSixelNoOp(t *testing.T) {
	initOverrideTestEnv(t)

	b := buffer.NewBufferFromString("= Doc\n", "doc.typ", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{b})
	action.Tabs.Resize()
	tab := action.MainTab()
	bp := tab.CurPane()

	// Without Sixel capability, OpenForOverride is a silent no-op.
	SetCapsForTest(TerminalCaps{Sixel: false, Probed: true})
	pp, ok := OpenForOverride(bp, "Notes")
	assert.False(t, ok)
	assert.Nil(t, pp)
	assert.Len(t, tab.Panes, 1)

	// Enable Sixel -> OpenForOverride docks right in override mode.
	SetCapsForTest(TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Probed: true})
	pp, ok = OpenForOverride(bp, "Notes")
	require.True(t, ok)
	require.NotNil(t, pp)
	assert.Len(t, tab.Panes, 2)
	assert.True(t, pp.HasOverride())
	assert.True(t, pp.IsCompiling())
	assert.Equal(t, "Notes", pp.OverrideTitle())

	// Delivering RGBA images updates deck & marks Sixel dirty.
	img := image.NewRGBA(image.Rect(0, 0, 200, 300))
	pp.ShowOverrideImages("Notes", []*image.RGBA{img})
	assert.False(t, pp.IsCompiling())
	assert.Equal(t, "", pp.LastError())
	_, _, total := pp.PageRange()
	assert.Equal(t, 1, total)

	// ShowOverrideError records error cleanly without panicking.
	pp.ShowOverrideError("Broken", "compile error line 1\nmore details")
	assert.Equal(t, "compile error line 1", pp.LastError())

	// ClearOverrideForTab closes the pane since it was auto-opened by override.
	ClearOverrideForTab(tab)
	assert.Nil(t, PaneForTab(tab))
	assert.Len(t, tab.Panes, 1)
}

func TestOverridePreservesExistingDocumentPreviewAndCompileHelper(t *testing.T) {
	initOverrideTestEnv(t)
	SetCapsForTest(TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Probed: true})
	comp := &stubOverrideCompiler{pages: 2}
	SetCompiler(comp)

	b := buffer.NewBufferFromString("= Paper\n", "paper.typ", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{b})
	action.Tabs.Resize()
	tab := action.MainTab()
	bp := tab.CurPane()

	// Open persistent document preview first and wait for 2-page deck.
	pp, ok := Open(bp, false)
	require.True(t, ok)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		action.DrainUIQueue()
		if !pp.IsCompiling() && pp.MaxScrollY() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, _, total := pp.PageRange()
	require.Equal(t, 2, total)
	require.True(t, pp.ScrollBy(40))
	savedScroll := pp.ScrollY()
	require.Greater(t, savedScroll, 0)

	// OpenForOverride reuses existing pane without replacing document deck.
	ovPane, ok := OpenForOverride(bp, "Slides")
	require.True(t, ok)
	assert.Equal(t, pp, ovPane)
	assert.True(t, pp.HasOverride())

	img := image.NewRGBA(image.Rect(0, 0, 200, 300))
	pp.ShowOverrideImages("Slides", []*image.RGBA{img})
	_, _, ovTotal := pp.PageRange()
	assert.Equal(t, 1, ovTotal)
	assert.Equal(t, 0, pp.ScrollY())

	// ClearOverrideForTab restores original 2-page document deck & scroll offset!
	ClearOverrideForTab(tab)
	assert.NotNil(t, PaneForTab(tab))
	assert.False(t, pp.HasOverride())
	_, _, restoredTotal := pp.PageRange()
	assert.Equal(t, 2, restoredTotal)
	assert.Equal(t, savedScroll, pp.ScrollY())

	// CompileTypstToImages decodes capped page count cleanly.
	imgs, err := CompileTypstToImages(context.Background(), CompileRequest{
		FilePath: "sample.typ",
		Content:  []byte("= Hello\n"),
		Modified: true,
	}, 1)
	require.NoError(t, err)
	assert.Len(t, imgs, 1)
	assert.Equal(t, image.Rect(0, 0, 200, 300), imgs[0].Bounds())
}

func TestScaleAreaResamplingAndDarkInvert(t *testing.T) {
	t.Parallel()

	// Create a 10x10 diagonal gradient source image so upscaling and downscaling
	// produce smooth interpolated intermediate pixels rather than blocky steps.
	src := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			v := uint8((x + y) * 14)
			src.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}

	up := scaleArea(src, 40, false)
	require.Equal(t, image.Rect(0, 0, 40, 40), up.Bounds())
	assert.Equal(t, uint8(255), up.RGBAAt(20, 20).A)
	// Adjacent pixels on an upscaled gradient should interpolate smoothly.
	left := int(up.RGBAAt(18, 20).R)
	mid := int(up.RGBAAt(19, 20).R)
	right := int(up.RGBAAt(20, 20).R)
	assert.GreaterOrEqual(t, right, mid)
	assert.GreaterOrEqual(t, mid, left)

	// Pure white with dark-theme inversion maps to #1b1e28 and opaque alpha.
	white := image.NewRGBA(image.Rect(0, 0, 8, 8))
	draw.Draw(white, white.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	inv := scaleArea(white, 4, true)
	require.Equal(t, image.Rect(0, 0, 4, 4), inv.Bounds())
	px := inv.RGBAAt(2, 2)
	assert.Equal(t, color.RGBA{27, 30, 40, 255}, px)
}
