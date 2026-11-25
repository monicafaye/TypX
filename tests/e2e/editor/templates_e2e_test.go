package editor_test

import (
	"context"
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/lsp"
	"github.com/monicafaye/TypX/internal/typst/preview"
	"github.com/monicafaye/TypX/internal/typst/templates"
	"github.com/monicafaye/TypX/tests/e2e/harness"
)

func TestE2E_TemplatesGalleryEmptyHintSaveAndMockTinymistInit(t *testing.T) {
	harness.RequirePhase23(t)

	mockBin := harness.BuildMockTinymist(t)
	mockEnv, _, traceFile, _ := harness.MockEnv(t, harness.MockOptions{})
	for k, v := range mockEnv {
		t.Setenv(k, v)
	}

	ed := harness.NewSimEditor(t, harness.SimOptions{
		InitialName: "blank.typ",
		InitialText: "",
		Settings: map[string]any{
			"colorscheme":          "typx-dark",
			"typst.lspEnabled":     true,
			"typst.tinymistPath":   mockBin,
			"typst.debounceMs":     float64(30),
			"typst.keyHints":       true,
			"typst.templatesWidth": float64(34),
		},
		Width:  110,
		Height: 28,
	})
	t.Cleanup(func() {
		if tab := action.MainTab(); tab != nil {
			templates.CloseForTab(tab)
		}
		lsp.Shutdown()
	})

	lsp.Init(lsp.Options{})
	ed.RunCommand("tinymist-restart")
	require.True(t, ed.PumpUntil(5*time.Second, func() bool {
		return strings.Contains(ed.StatusLineText(), "LSP: ready |")
	}), "expected mocktinymist ready, status: %q", ed.StatusLineText())
	// Dismiss the InfoBar "Restarting tinymist LSP..." message so idle bottom bar hints show.
	ed.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	ed.Pump(2)

	// 1. Empty .typ buffer promotes Alt+⇧N Templates on the bottom bar.
	emptyBar := ed.InfoBarText()
	assert.Contains(t, emptyBar, "Alt+⇧N Templates")

	// Typing hides Templates from the main bottom bar strip; deleting brings it back.
	ed.InjectKey(tcell.KeyRune, 'x', tcell.ModNone)
	ed.Pump(4)
	assert.NotContains(t, ed.InfoBarText(), "Templates")
	ed.InjectKey(tcell.KeyBackspace2, 0, tcell.ModNone)
	ed.Pump(4)
	assert.Contains(t, ed.InfoBarText(), "Alt+⇧N Templates")

	// 2. Alt+Shift+N opens Template Gallery docked on the left with TEMPLATES badge.
	ed.InjectKey(tcell.KeyRune, 'N', tcell.ModAlt|tcell.ModShift)
	tab := action.MainTab()
	require.Len(t, tab.Panes, 2)
	assert.True(t, action.IsAuxiliaryPane(tab.ActivePane()))
	assert.Equal(t, "Templates", tab.ActivePane().Name())
	assert.Contains(t, ed.InfoBarText(), "TEMPLATES")
	assert.Contains(t, ed.ScreenLine(0), "Templates (")

	// Filter with '/' to "notes" and press Enter -> inserts starter and returns focus.
	for _, r := range "/notes" {
		ed.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	assert.Contains(t, ed.InfoBarText(), "Esc Clear Filter")
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

	assert.False(t, action.IsAuxiliaryPane(tab.ActivePane()), "applying starter must return focus to editor")
	bufText := string(ed.Buf().Bytes())
	assert.Contains(t, bufText, "#let note(title, body)")
	assert.Contains(t, bufText, "= Blank")

	// Close gallery cleanly via Alt+Shift+N.
	ed.InjectKey(tcell.KeyRune, 'N', tcell.ModAlt|tcell.ModShift)
	require.Len(t, tab.Panes, 1)

	// 3. :template-save --preamble writes only preamble above first '=' heading, and :template applies it.
	ed.RunCommand("template-save my-preamble --preamble")
	savedPath := filepath.Join(config.ConfigDir, "templates", "my-preamble.typ")
	savedRaw, err := os.ReadFile(savedPath)
	require.NoError(t, err)
	assert.Contains(t, string(savedRaw), "#let note(title, body)")
	assert.NotContains(t, string(savedRaw), "= Blank")

	ed.RunCommand("tab second.typ")
	require.GreaterOrEqual(t, len(action.Tabs.List), 2)
	ed.RunCommand("template my-preamble")
	secondText := string(action.MainTab().CurPane().Buf.Bytes())
	assert.Contains(t, secondText, "#let note(title, body)")
	assert.NotContains(t, secondText, "== Key Ideas")

	// 4. Mocked tinymist.doInitTemplate and tinymist.doGetTemplateEntry via :template.
	scaffoldDir := filepath.Join(t.TempDir(), "mock-paper-proj")
	ed.RunCommand("template @preview/mock-paper:0.1.0 " + scaffoldDir)
	expectedEntry := filepath.Join(scaffoldDir, "main.typ")
	ok := ed.PumpUntil(5*time.Second, func() bool {
		_, statErr := os.Stat(expectedEntry)
		if statErr != nil {
			return false
		}
		cp := action.MainTab().CurPane()
		return cp != nil && cp.Buf != nil && strings.Contains(string(cp.Buf.Bytes()), "= Mock Template")
	})
	require.True(t, ok, "expected scaffolded main.typ to open via tinymist.doInitTemplate")

	// Test in-place preamble insertion via tinymist.doGetTemplateEntry.
	ed.RunCommand("tab third.typ")
	ed.RunCommand("template @preview/mock-paper:0.1.0 --preamble")
	ok = ed.PumpUntil(5*time.Second, func() bool {
		cp := action.MainTab().CurPane()
		return cp != nil && cp.Buf != nil && strings.Contains(string(cp.Buf.Bytes()), `// mock entry for @preview/mock-paper:0.1.0`)
	})
	require.True(t, ok, "expected preamble inserted via tinymist.doGetTemplateEntry")

	traceBytes, err := os.ReadFile(traceFile)
	require.NoError(t, err)
	traceText := string(traceBytes)
	assert.Contains(t, traceText, "tinymist.doInitTemplate")
	assert.Contains(t, traceText, "tinymist.doGetTemplateEntry")
}

type stubTemplatePreviewCompiler struct{}

func (stubTemplatePreviewCompiler) CompilePages(ctx context.Context, req preview.CompileRequest) (*preview.DocumentDeck, error) {
	img := image.NewRGBA(image.Rect(0, 0, 220, 300))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)
	return preview.NewDocumentDeckFromImages([]*image.RGBA{img}, time.Millisecond), nil
}

func TestE2E_TemplateGalleryLiveVisualPreview(t *testing.T) {
	harness.RequirePhase23(t)

	preview.SetCapsForTest(preview.TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})
	preview.SetCompiler(stubTemplatePreviewCompiler{})
	templates.SetPreviewCacheDir(filepath.Join(t.TempDir(), "preview-cache"))

	ed := harness.NewSimEditor(t, harness.SimOptions{
		InitialName: "live-gallery.typ",
		InitialText: "= Existing Doc\n",
		Settings: map[string]any{
			"colorscheme":          "typx-dark",
			"typst.lspEnabled":     false,
			"typst.keyHints":       true,
			"typst.templatesWidth": float64(30),
			"typst.previewWidth":   float64(42),
		},
		Width:  130,
		Height: 32,
	})
	t.Cleanup(func() {
		if tab := action.MainTab(); tab != nil {
			templates.CloseForTab(tab)
			preview.CloseForTab(tab)
		}
		preview.SetCapsForTest(preview.TerminalCaps{})
		preview.SetCompiler(&preview.TinymistCompiler{})
		templates.SetPreviewCacheDir("")
	})

	tab := action.MainTab()
	bp := ed.Pane()

	// 1. Opening gallery on Sixel terminal auto-opens PreviewPane in override mode.
	ed.InjectKey(tcell.KeyRune, 'N', tcell.ModAlt|tcell.ModShift)
	require.True(t, ed.PumpUntil(3*time.Second, func() bool {
		pp := preview.PaneForTab(tab)
		return pp != nil && pp.HasOverride() && !pp.IsCompiling()
	}), "expected live gallery preview pane to render starter automatically")

	pp := preview.PaneForTab(tab)
	require.NotNil(t, pp)
	assert.Equal(t, "Minimal Document", pp.OverrideTitle())
	assert.Contains(t, ed.InfoBarText(), "v Preview●")

	// 2. Pressing 'j' moves to next starter and updates preview override title.
	ed.InjectKey(tcell.KeyRune, 'j', tcell.ModNone)
	require.True(t, ed.PumpUntil(3*time.Second, func() bool {
		cur := preview.PaneForTab(tab)
		return cur != nil && cur.HasOverride() && cur.OverrideTitle() == "Academic Article" && !cur.IsCompiling()
	}), "expected moving down in gallery to update preview to Academic Article")

	// 3. Pressing 'v' toggles live preview off and closes the auto-opened PreviewPane; 'v' again restores it.
	ed.InjectKey(tcell.KeyRune, 'v', tcell.ModNone)
	require.Nil(t, preview.PaneForTab(tab), "toggling live preview off should close auto-opened preview pane")
	assert.NotContains(t, ed.InfoBarText(), "v Preview●")

	ed.InjectKey(tcell.KeyRune, 'v', tcell.ModNone)
	require.True(t, ed.PumpUntil(3*time.Second, func() bool {
		cur := preview.PaneForTab(tab)
		return cur != nil && cur.HasOverride() && !cur.IsCompiling()
	}), "toggling live preview back on should reopen preview pane")

	// Close gallery cleanly -> auto-opened PreviewPane closes.
	ed.InjectKey(tcell.KeyRune, 'q', tcell.ModNone)
	require.Nil(t, preview.PaneForTab(tab))
	require.Nil(t, templates.PaneForTab(tab))

	// 4. If document PreviewPane was already open before gallery, closing gallery preserves it!
	docPP, ok := preview.Open(bp, false)
	require.True(t, ok)
	require.True(t, ed.PumpUntil(3*time.Second, func() bool {
		return !docPP.IsCompiling()
	}))

	ed.InjectKey(tcell.KeyRune, 'N', tcell.ModAlt|tcell.ModShift)
	require.True(t, ed.PumpUntil(3*time.Second, func() bool {
		return docPP.HasOverride() && !docPP.IsCompiling()
	}))

	ed.InjectKey(tcell.KeyRune, 'q', tcell.ModNone)
	assert.Same(t, docPP, preview.PaneForTab(tab), "pre-existing document preview pane must remain open")
	assert.False(t, docPP.HasOverride(), "closing gallery must restore underlying document preview deck")
}
