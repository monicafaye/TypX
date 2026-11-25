package editor_test

import (
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
