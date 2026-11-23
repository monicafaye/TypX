package display

import (
	"strings"
	"testing"

	"github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/info"
	"github.com/monicafaye/TypX/internal/screen"
)

func simRowText(sim tcell.SimulationScreen, y int) string {
	w, _ := sim.Size()
	var sb strings.Builder
	for x := 0; x < w; {
		r, _, _, width := sim.GetContent(x, y)
		if r == 0 {
			r = ' '
		}
		sb.WriteRune(r)
		if width < 1 {
			width = 1
		}
		x += width
	}
	return strings.TrimRight(sb.String(), " ")
}

func TestSelectItemsByPriorityPreservesOrder(t *testing.T) {
	items := []BottomBarItem{
		{Key: "^S", Label: "Save", Priority: 0},      // w = 7
		{Key: "^F", Label: "Find", Priority: 3},      // w = 7
		{Key: "Alt+P", Label: "Preview", Priority: 0}, // w = 13
		{Key: "Alt+O", Label: "Outline", Priority: 1}, // w = 13
		{Key: "Alt+K", Label: "Hover", Priority: 4},   // w = 11
	}

	// Width 38 fits priority 0 + 1 items: Save (7) + 2 + Preview (13) + 2 + Outline (13) = 37
	picked := selectItemsByPriority(items, 38, 2)
	require.Len(t, picked, 3)
	assert.Equal(t, []string{"Save", "Preview", "Outline"}, []string{picked[0].Label, picked[1].Label, picked[2].Label})
	assert.LessOrEqual(t, totalItemsWidth(picked, 2), 38)

	// Narrow width 24 fits only priority 0 items: Save (7) + 2 + Preview (13) = 22
	narrow := selectItemsByPriority(items, 24, 2)
	require.Len(t, narrow, 2)
	assert.Equal(t, []string{"Save", "Preview"}, []string{narrow[0].Label, narrow[1].Label})
}

func TestIsReadableContrast(t *testing.T) {
	white := tcell.NewRGBColor(255, 255, 255)
	black := tcell.NewRGBColor(0, 0, 0)
	nearWhite := tcell.NewRGBColor(240, 240, 240)

	assert.True(t, isReadableContrast(white, black, 4.5))
	assert.False(t, isReadableContrast(white, nearWhite, 3.0))
	assert.False(t, isReadableContrast(tcell.ColorDefault, black, 3.0))
}

func TestInfoWindowIdleBottomBarAndHitboxes(t *testing.T) {
	origScreen := screen.Screen
	origColors := config.Colorscheme
	origProvider := bottomBarProvider
	origHints := config.GlobalSettings["typst.keyHints"]
	origKeyMenu := config.GlobalSettings["keymenu"]
	origInfoBar := config.GlobalSettings["infobar"]
	t.Cleanup(func() {
		screen.Screen = origScreen
		config.Colorscheme = origColors
		bottomBarProvider = origProvider
		config.GlobalSettings["typst.keyHints"] = origHints
		config.GlobalSettings["keymenu"] = origKeyMenu
		config.GlobalSettings["infobar"] = origInfoBar
	})

	sim, err := screen.InitSimScreen()
	require.NoError(t, err)
	defer sim.Fini()
	sim.SetSize(100, 10)

	config.GlobalSettings["infobar"] = true
	config.GlobalSettings["keymenu"] = false
	config.GlobalSettings["typst.keyHints"] = true
	config.Colorscheme, err = config.LoadColorscheme("typx-dark", nil)
	require.NoError(t, err)

	SetBottomBarProvider(func() BottomBarContext {
		return BottomBarContext{
			Badge:       "MATH",
			BadgeAccent: "math",
			Hints: []BottomBarItem{
				{Key: "Alt+I", Label: "Snippets", Action: "InsertPalette", Priority: 0},
				{Key: "Alt+P", Label: "Preview", Action: "TogglePreview", Priority: 0, Active: true},
				{Key: "Alt+G", Label: "Keys", Action: "ToggleKeyMenu", Priority: 0, AlignRight: true},
			},
			MenuRows: []BottomBarMenuRow{
				{
					Title: "Editor",
					Items: []BottomBarItem{
						{Key: "^S", Label: "Save", Action: "Save", Priority: 0},
					},
				},
				{
					Title: "Typst",
					Items: []BottomBarItem{
						{Key: "Alt+O", Label: "Outline", Action: "ToggleOutline", Priority: 0, Active: true},
					},
				},
			},
			MenuTip: "Tip text",
		}
	})

	ib := info.NewBuffer()
	iw := NewInfoWindow(ib)
	iw.Resize(100, 9)

	// 1. Idle bottom bar renders badge, active dot, right-aligned Keys, and hitboxes.
	iw.Display()
	sim.Show()
	row := simRowText(sim, 9)
	assert.Contains(t, row, "MATH")
	assert.Contains(t, row, "Alt+I Snippets")
	assert.Contains(t, row, "Alt+P Preview●")
	assert.Contains(t, row, "Alt+G Keys")

	previewIdx := strings.Index(row, "Alt+P")
	require.Greater(t, previewIdx, 0)
	act, ok := iw.HitAt(previewIdx+2, 9)
	assert.True(t, ok)
	assert.Equal(t, "TogglePreview", act)

	// 2. Expanded key menu renders both rows above y=9 with clickable items + KEYS tip.
	config.GlobalSettings["keymenu"] = true
	iw.Display()
	sim.Show()
	assert.Contains(t, simRowText(sim, 7), "Editor")
	assert.Contains(t, simRowText(sim, 7), "^S Save")
	assert.Contains(t, simRowText(sim, 8), "Typst")
	assert.Contains(t, simRowText(sim, 8), "Alt+O Outline●")
	assert.Contains(t, simRowText(sim, 9), "KEYS")
	assert.Contains(t, simRowText(sim, 9), "Alt+G Hide")

	outlineIdx := strings.Index(simRowText(sim, 8), "Alt+O")
	require.Greater(t, outlineIdx, 0)
	act, ok = iw.HitAt(outlineIdx+1, 8)
	assert.True(t, ok)
	assert.Equal(t, "ToggleOutline", act)

	// 3. Active prompt renders prompt + helper hint without collision.
	config.GlobalSettings["keymenu"] = false
	ib.Prompt("> ", "preview", "Command", nil, nil)
	iw.Display()
	sim.Show()
	promptRow := simRowText(sim, 9)
	assert.True(t, strings.HasPrefix(promptRow, "> preview"))
	assert.Contains(t, promptRow, "Tab complete")
	ib.Reset()

	// 4. Active message renders message + clickable Esc dismiss.
	ib.Message("Saved paper.typ")
	iw.Display()
	sim.Show()
	msgRow := simRowText(sim, 9)
	assert.Contains(t, msgRow, "Saved paper.typ")
	assert.Contains(t, msgRow, "Esc dismiss")
	escIdx := strings.Index(msgRow, "Esc dismiss")
	require.Greater(t, escIdx, 0)
	act, ok = iw.HitAt(escIdx+1, 9)
	assert.True(t, ok)
	assert.Equal(t, "dismiss", act)

	// 5. typst.keyHints=false leaves idle bottom bar blank.
	ib.Reset()
	config.GlobalSettings["typst.keyHints"] = false
	iw.Display()
	sim.Show()
	assert.Equal(t, "", simRowText(sim, 9))
}

func TestStatusLineSemanticStylingAndCleanOverflow(t *testing.T) {
	win, sim, cleanup := newTestWin(t, "= Heading\n$alpha$\n", "paper.typ", 100, 10)
	defer cleanup()

	var err error
	config.Colorscheme, err = config.LoadColorscheme("typx-dark", nil)
	require.NoError(t, err)

	win.Buf.Settings["statusline"] = true
	win.Buf.Settings["statusformatl"] = "paper.typ + (1,1) | MATH | 12 words | LSP: ready | ERR: 2 | PREVIEW: ON | ft:typst"
	win.Buf.Settings["statusformatr"] = "50% | utf-8"
	win.SetActive(true)

	sl := NewStatusLine(win)
	sl.Display()
	sim.Show()

	y := win.Height + win.Y - 1
	row := simRowText(sim, y)
	assert.Contains(t, row, "paper.typ + (1,1) | MATH | 12 words | LSP: ready | ERR: 2 | PREVIEW: ON | ft:typst")
	assert.Contains(t, row, "50% | utf-8")

	// Semantic styles are distinct from base statusline style.
	baseStyle := config.Colorscheme["statusline"]
	mathIdx := strings.Index(row, "MATH")
	errIdx := strings.Index(row, "ERR: 2")
	lspIdx := strings.Index(row, "LSP: ready")
	pipeIdx := strings.Index(row, "|")
	require.Greater(t, mathIdx, 0)
	require.Greater(t, errIdx, 0)
	require.Greater(t, lspIdx, 0)
	require.Greater(t, pipeIdx, 0)

	_, _, mathStyle, _ := sim.GetContent(mathIdx, y)
	_, _, errStyle, _ := sim.GetContent(errIdx, y)
	_, _, lspStyle, _ := sim.GetContent(lspIdx, y)
	_, _, pipeStyle, _ := sim.GetContent(pipeIdx, y)

	assert.NotEqual(t, baseStyle, mathStyle, "MATH segment should use semantic mode style")
	assert.NotEqual(t, baseStyle, errStyle, "ERR segment should use semantic error style")
	assert.NotEqual(t, baseStyle, lspStyle, "LSP: ready should use semantic ok style")
	assert.NotEqual(t, baseStyle, pipeStyle, "separator '|' should be dimmed")

	// Narrow pane cleanly ellipsizes left text and omits colliding right text.
	sim.SetSize(36, 10)
	sim.Clear()
	win.Resize(36, 10)
	sl.Display()
	sim.Show()
	narrowRow := simRowText(sim, y)
	assert.True(t, strings.HasSuffix(narrowRow, "…"), "expected clean ellipsis on overflow, got %q", narrowRow)
	assert.NotContains(t, narrowRow, "utf-8", "rightText must not collide with overflowing leftText")
}
