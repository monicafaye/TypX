package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
)

func TestPinAndUnpinMainFilePersistenceAndRestart(t *testing.T) {
	dir, logPath := setupLSPEnv(t, 0)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "typst.toml"), []byte("[package]\n"), 0644))
	mainPath := filepath.Join(dir, "main.typ")
	subDir := filepath.Join(dir, "chapters")
	require.NoError(t, os.MkdirAll(subDir, 0755))
	chPath := filepath.Join(subDir, "ch1.typ")

	require.NoError(t, os.WriteFile(mainPath, []byte("#include \"chapters/ch1.typ\"\n"), 0644))
	bCh := openTestBuffer(t, chPath, "= Chapter 1\n")

	Init(Options{InitialFiles: []string{chPath}})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()
	action.InitTabs([]*buffer.Buffer{bCh})
	bp := action.MainTab().CurPane()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected StateReady")

	var pinEvents []string
	SetPinChangeHandler(func(p string) {
		pinEvents = append(pinEvents, p)
	})
	t.Cleanup(func() { SetPinChangeHandler(nil) })

	// Pin main.typ via relative arg from chapters/ch1.typ.
	PinCommand()(bp, []string{"../main.typ"})
	assert.Equal(t, filepath.Clean(mainPath), PinnedMain())
	assert.Contains(t, StatusLineInfo()(bCh), "PIN: main.typ | ")
	assert.Equal(t, []string{filepath.Clean(mainPath)}, pinEvents)

	waitForCondition(t, 3*time.Second, func() bool {
		return strings.Contains(readMockLog(logPath), `executeCommand:tinymist.pinMain:["`)
	}, "expected tinymist.pinMain RPC with raw filesystem path")
	logText := readMockLog(logPath)
	assert.NotContains(t, logText, "tinymist.pinMain:[\"file://", "pinMain must send filesystem path, not URI")

	// Restart tinymist and verify pin is re-applied automatically.
	beforeLen := len(logText)
	TinymistRestartCommand()(bp, nil)
	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady &&
			strings.Contains(readMockLog(logPath)[beforeLen:], "executeCommand:tinymist.pinMain:")
	}, "expected :tinymist-restart to re-send pinMain")

	// Persisted pin loads on fresh Init for same project.
	Shutdown()
	Init(Options{InitialFiles: []string{chPath}})
	assert.Equal(t, filepath.Clean(mainPath), PinnedMain())
	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected fresh Init StateReady")

	// Unpin clears state, pins.json, and sends [null].
	UnpinCommand()(bp, nil)
	assert.Equal(t, "", PinnedMain())
	assert.NotContains(t, StatusLineInfo()(bCh), "PIN:")
	waitForCondition(t, 3*time.Second, func() bool {
		return strings.Contains(readMockLog(logPath), "executeCommand:tinymist.pinMain:[null]")
	}, "expected unpin to send [null]")

	// Pinned main also persists locally when LSP is off (--no-lsp).
	config.GlobalSettings["typst.lspEnabled"] = false
	Init(Options{InitialFiles: []string{chPath}})
	PinCommand()(bp, []string{mainPath})
	assert.Equal(t, filepath.Clean(mainPath), PinnedMain())
	assert.Contains(t, StatusLineInfo()(bCh), "PIN: main.typ | ")
	UnpinCommand()(bp, nil)
}
