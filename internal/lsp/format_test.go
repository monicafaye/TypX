package lsp

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
)

func TestFormatCommandAndSingleUndo(t *testing.T) {
	dir, _ := setupLSPEnv(t, 200)

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected StateReady")

	raw := "=  Title\n\n#{\n  let  x=1\n}\n"
	typPath := filepath.Join(dir, "fmt.typ")
	b := openTestBuffer(t, typPath, raw)
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()

	// Edit with pending debounce (makes "=   Title") and immediately run :format —
	// flushDocChangeNow must synchronize before formatting so tinymist sees the edit.
	b.Insert(buffer.Loc{X: 2, Y: 0}, " ")
	buffer.FlushObservers()
	if top := b.EventHandler.UndoStack.Peek(); top != nil {
		top.Time = top.Time.Add(-2 * time.Second)
	}
	FormatCommand()(bp, nil)

	want := "= Title\n\n#{\n  let x = 1\n}\n"
	waitForCondition(t, 3*time.Second, func() bool {
		return string(b.Bytes()) == want
	}, "expected :format to apply both multi-line edits")
	assert.Equal(t, "Formatted", action.InfoBar.Msg)

	// Single Ctrl+Z undo restores text before :format.
	assert.True(t, b.Undo())
	assert.Equal(t, "=   Title\n\n#{\n  let  x=1\n}\n", string(b.Bytes()))
}

func TestFormatOnSaveFormatsBeforeDiskAndFallsBackOnErrorOrTimeout(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)
	config.GlobalSettings["typst.formatOnSave"] = true

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected StateReady")

	raw := "=   Title\n#{\n  let  x=1\n}\n"
	typPath := filepath.Join(dir, "savefmt.typ")
	b := openTestBuffer(t, typPath, raw)
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()

	// 1. Save formats buffer BEFORE writing to disk.
	bp.Save()
	want := "= Title\n#{\n  let x = 1\n}\n"
	waitForCondition(t, 3*time.Second, func() bool {
		disk, err := os.ReadFile(typPath)
		return err == nil && string(disk) == want && !b.Modified()
	}, "expected typst.formatOnSave to write formatted text to disk")

	// 2. If formatting errors or times out, Save still writes user's edits cleanly.
	t.Setenv("TYPX_MOCK_FORMAT_FAIL", "1")
	TinymistRestartCommand()(bp, nil)
	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected restart ready")

	b.Insert(buffer.Loc{X: 0, Y: 0}, "// unformatted ")
	bp.Save()
	waitForCondition(t, 3*time.Second, func() bool {
		disk, err := os.ReadFile(typPath)
		return err == nil && string(disk) == "// unformatted "+want && !b.Modified()
	}, "expected Save to complete even when formatting fails")
}
