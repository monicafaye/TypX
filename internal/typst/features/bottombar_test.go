package features

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/display"
	"github.com/monicafaye/TypX/internal/typst/templates"
)

func TestBottomBarEmptyBufferTemplatesHintAndMenu(t *testing.T) {
	initTestEnv(t)
	t.Cleanup(func() {
		if tab := action.MainTab(); tab != nil {
			templates.CloseForTab(tab)
		}
		buffer.CloseOpenBuffers()
		action.DrainUIQueue()
	})

	// 1. Empty .typ buffer promotes Alt+⇧N Templates at Priority 0.
	emptyBuf := buffer.NewBufferFromString("", "blank.typ", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{emptyBuf})
	action.Tabs.Resize()

	ctx := buildBottomBarContext()
	foundHint := false
	for _, h := range ctx.Hints {
		if h.Action == "ToggleTemplates" {
			foundHint = true
			assert.Equal(t, 0, h.Priority)
			assert.Equal(t, "Templates", h.Label)
		}
	}
	assert.True(t, foundHint, "expected ToggleTemplates hint on empty .typ buffer")

	// Also verify Alt+G Typst key menu row contains ToggleTemplates.
	foundMenu := false
	for _, row := range ctx.MenuRows {
		if row.Title == "Typst" {
			for _, it := range row.Items {
				if it.Action == "ToggleTemplates" {
					foundMenu = true
				}
			}
		}
	}
	assert.True(t, foundMenu, "expected ToggleTemplates in Typst Alt+G key menu")

	// 2. Typing content hides ToggleTemplates from the main bottom bar strip.
	emptyBuf.Insert(buffer.Loc{X: 0, Y: 0}, "= Heading\n")
	ctxAfter := buildBottomBarContext()
	for _, h := range ctxAfter.Hints {
		assert.NotEqual(t, "ToggleTemplates", h.Action)
	}

	// 3. Focusing TemplatePane switches bottom bar to TEMPLATES context.
	bp := action.MainTab().CurPane()
	tp, ok := templates.Open(bp, true)
	require.True(t, ok)
	require.NotNil(t, tp)

	tpCtx := buildBottomBarContext()
	assert.Equal(t, "TEMPLATES", tpCtx.Badge)
	assert.Contains(t, tpCtx.Hints, display.BottomBarItem{Key: "Enter", Label: "Apply", Action: "pane:Enter", Priority: 0})
	assert.Contains(t, tpCtx.Hints, display.BottomBarItem{Key: "u", Label: "Universe", Action: "pane:u", Priority: 1})
}

func TestBottomBarTemplatesHintWithoutTypFile(t *testing.T) {
	initTestEnv(t)
	t.Cleanup(func() {
		if tab := action.MainTab(); tab != nil {
			templates.CloseForTab(tab)
		}
		buffer.CloseOpenBuffers()
		action.DrainUIQueue()
	})

	// 1. Blank unnamed buffer promotes Alt+⇧N Templates at Priority 0.
	unnamed := buffer.NewBufferFromString("", "", buffer.BTDefault)
	require.False(t, unnamed.IsTypst())
	action.InitTabs([]*buffer.Buffer{unnamed})
	action.Tabs.Resize()

	ctx := buildBottomBarContext()
	foundHint := false
	for _, h := range ctx.Hints {
		if h.Action == "ToggleTemplates" {
			foundHint = true
			assert.Equal(t, 0, h.Priority)
			assert.Equal(t, "Templates", h.Label)
		}
	}
	assert.True(t, foundHint, "expected ToggleTemplates hint on blank unnamed buffer")

	// Typing into unnamed buffer hides Templates from the main strip, but keeps it in Alt+G key menu.
	unnamed.Insert(buffer.Loc{X: 0, Y: 0}, "hello\n")
	ctxTyped := buildBottomBarContext()
	for _, h := range ctxTyped.Hints {
		assert.NotEqual(t, "ToggleTemplates", h.Action)
	}
	foundMenu := false
	for _, row := range ctxTyped.MenuRows {
		for _, it := range row.Items {
			if it.Action == "ToggleTemplates" {
				foundMenu = true
			}
		}
	}
	assert.True(t, foundMenu, "expected ToggleTemplates in generic Alt+G key menu")

	// 2. Non-Typst named file (even if blank) keeps Templates in Alt+G menu without crowding the strip.
	mdBuf := buffer.NewBufferFromString("", "README.md", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{mdBuf})
	action.Tabs.Resize()

	mdCtx := buildBottomBarContext()
	for _, h := range mdCtx.Hints {
		assert.NotEqual(t, "ToggleTemplates", h.Action)
	}
	foundMDMenu := false
	for _, row := range mdCtx.MenuRows {
		for _, it := range row.Items {
			if it.Action == "ToggleTemplates" {
				foundMDMenu = true
			}
		}
	}
	assert.True(t, foundMDMenu, "expected ToggleTemplates in Alt+G key menu for non-Typst files")
}
