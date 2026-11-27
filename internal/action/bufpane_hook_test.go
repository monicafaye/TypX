package action

import (
	"testing"

	"github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	lua "github.com/yuin/gopher-lua"

	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	ulua "github.com/monicafaye/TypX/internal/lua"
	"github.com/monicafaye/TypX/internal/screen"
)

func setupHookPane(t *testing.T, text string) *BufPane {
	t.Helper()

	origCfgDir := config.ConfigDir
	origGlobal := config.GlobalSettings
	origBindings := config.Bindings
	origLuaL := ulua.L
	origScreen := screen.Screen
	origTabs := Tabs
	origInfoBar := InfoBar

	copiedHooks := make(map[string][]func(bp *BufPane) bool, len(extPreActionHooks))
	for k, v := range extPreActionHooks {
		copiedHooks[k] = append([]func(bp *BufPane) bool(nil), v...)
	}
	extPreActionHooks = map[string][]func(bp *BufPane) bool{}

	if ulua.L == nil {
		ulua.L = lua.NewState()
	}
	config.ConfigDir = t.TempDir()
	config.InitRuntimeFiles(false)
	_ = config.InitGlobalSettings()
	config.GlobalSettings["backup"] = false
	config.GlobalSettings["fastdirty"] = true
	config.Bindings = map[string]map[string]string{
		"buffer":   {},
		"command":  {},
		"terminal": {},
	}

	sim, err := screen.InitSimScreen()
	require.NoError(t, err)
	sim.SetSize(80, 24)

	InitBindings()
	InitCommands()
	InitGlobals()

	b := buffer.NewBufferFromString(text, "doc.typ", buffer.BTDefault)
	InitTabs([]*buffer.Buffer{b})
	bp := MainTab().CurPane()
	require.NotNil(t, bp)

	t.Cleanup(func() {
		buffer.CloseOpenBuffers()
		sim.Fini()
		extPreActionHooks = copiedHooks
		config.ConfigDir = origCfgDir
		config.GlobalSettings = origGlobal
		config.Bindings = origBindings
		ulua.L = origLuaL
		screen.Screen = origScreen
		Tabs = origTabs
		InfoBar = origInfoBar
		DrainUIQueue()
	})

	return bp
}

func TestHasSuggestionsPreservedForInsertNewlineAndEscapeHooks(t *testing.T) {
	t.Run("InsertNewline consuming hook sees HasSuggestions true and skips newline", func(t *testing.T) {
		bp := setupHookPane(t, "hello")
		bp.Cursor.GotoLoc(buffer.Loc{X: 5, Y: 0})
		bp.Buf.HasSuggestions = true

		sawHasSuggestions := false
		RegisterPreActionHook("InsertNewline", func(p *BufPane) bool {
			sawHasSuggestions = p.Buf.HasSuggestions
			p.Buf.HasSuggestions = false
			return true
		})

		ok := bp.execAction(BufKeyAction((*BufPane).InsertNewline), "InsertNewline", nil)
		assert.True(t, ok)
		assert.True(t, sawHasSuggestions)
		assert.False(t, bp.Buf.HasSuggestions)
		assert.Equal(t, "hello", string(bp.Buf.Bytes()))
	})

	t.Run("InsertEnter alias consuming hook sees HasSuggestions true", func(t *testing.T) {
		bp := setupHookPane(t, "hello")
		bp.Cursor.GotoLoc(buffer.Loc{X: 5, Y: 0})
		bp.Buf.HasSuggestions = true

		sawHasSuggestions := false
		RegisterPreActionHook("InsertNewline", func(p *BufPane) bool {
			sawHasSuggestions = p.Buf.HasSuggestions
			p.Buf.HasSuggestions = false
			return true
		})

		ok := bp.execAction(BufKeyAction((*BufPane).InsertNewline), "InsertEnter", nil)
		assert.True(t, ok)
		assert.True(t, sawHasSuggestions)
		assert.Equal(t, "hello", string(bp.Buf.Bytes()))
	})

	t.Run("InsertNewline unconsumed hook clears HasSuggestions and inserts newline", func(t *testing.T) {
		bp := setupHookPane(t, "hello")
		bp.Cursor.GotoLoc(buffer.Loc{X: 5, Y: 0})
		bp.Buf.HasSuggestions = true

		sawHasSuggestions := false
		RegisterPreActionHook("InsertNewline", func(p *BufPane) bool {
			sawHasSuggestions = p.Buf.HasSuggestions
			return false
		})

		ok := bp.execAction(BufKeyAction((*BufPane).InsertNewline), "InsertNewline", nil)
		assert.True(t, ok)
		assert.True(t, sawHasSuggestions)
		assert.False(t, bp.Buf.HasSuggestions)
		assert.Equal(t, "hello\n", string(bp.Buf.Bytes()))
	})

	t.Run("Escape consuming hook via Esc key chain sees HasSuggestions true", func(t *testing.T) {
		bp := setupHookPane(t, "hello")
		bp.Buf.HasSuggestions = true

		sawHasSuggestions := false
		RegisterPreActionHook("Escape", func(p *BufPane) bool {
			sawHasSuggestions = p.Buf.HasSuggestions
			p.Buf.HasSuggestions = false
			return true
		})

		handled := bp.DoKeyEvent(keyEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, "")))
		assert.True(t, handled)
		assert.True(t, sawHasSuggestions)
		assert.False(t, bp.Buf.HasSuggestions)
	})

	t.Run("Escape unconsumed hook clears HasSuggestions", func(t *testing.T) {
		bp := setupHookPane(t, "hello")
		bp.Buf.HasSuggestions = true

		sawHasSuggestions := false
		RegisterPreActionHook("Escape", func(p *BufPane) bool {
			sawHasSuggestions = p.Buf.HasSuggestions
			return false
		})

		ok := bp.execAction(BufKeyAction((*BufPane).Escape), "Escape", nil)
		assert.True(t, ok)
		assert.True(t, sawHasSuggestions)
		assert.False(t, bp.Buf.HasSuggestions)
	})

	t.Run("non-exempt action clears HasSuggestions before pre-action hooks", func(t *testing.T) {
		bp := setupHookPane(t, "hello")
		bp.Cursor.GotoLoc(buffer.Loc{X: 5, Y: 0})
		bp.Buf.HasSuggestions = true

		hookRan := false
		sawHasSuggestions := true
		RegisterPreActionHook("Backspace", func(p *BufPane) bool {
			hookRan = true
			sawHasSuggestions = p.Buf.HasSuggestions
			return false
		})

		bp.execAction(BufKeyAction((*BufPane).Backspace), "Backspace", nil)
		assert.True(t, hookRan)
		assert.False(t, sawHasSuggestions)
		assert.False(t, bp.Buf.HasSuggestions)
	})
}
