package shortcuts

import (
	"os"
	"testing"

	"github.com/micro-editor/tcell/v2"
	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	ulua "github.com/monicafaye/TypX/internal/lua"
	"github.com/monicafaye/TypX/internal/screen"
	lua "github.com/yuin/gopher-lua"
)

var simScreen tcell.SimulationScreen

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "typx_shortcuts_test_*")
	if err != nil {
		panic(err)
	}

	ulua.L = lua.NewState()
	config.InitConfigDir(tmpDir)
	config.InitRuntimeFiles(false)
	config.InitGlobalSettings()
	config.GlobalSettings["backup"] = false
	config.GlobalSettings["fastdirty"] = true
	config.GlobalSettings["multiopen"] = "tab"

	simScreen, err = screen.InitSimScreen()
	if err != nil {
		panic(err)
	}
	action.InitGlobals()
	action.InitCommands()
	buffer.SetMessager(action.InfoBar)

	code := m.Run()
	buffer.CloseOpenBuffers()
	_ = os.RemoveAll(tmpDir)
	os.Exit(code)
}

func newTestBufPane(t *testing.T, content, path string) *action.BufPane {
	t.Helper()
	SetModeDetector(nil)
	buffer.CloseOpenBuffers()
	t.Cleanup(func() {
		SetModeDetector(nil)
		buffer.CloseOpenBuffers()
	})
	buf := buffer.NewBufferFromString(content, path, buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{buf})
	bp := action.MainTab().CurPane()
	bp.Resize(80, 23)
	return bp
}

func bufferText(b *buffer.Buffer) string {
	return string(b.Bytes())
}
