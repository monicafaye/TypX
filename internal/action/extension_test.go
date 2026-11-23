package action

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/monicafaye/TypX/internal/config"
)

func resetExtensionsForTest(t *testing.T) {
	t.Helper()
	origCmds := extCommands
	origBindings := extDefaultBindings
	origCommands := commands

	extCommands = map[string]Command{}
	extDefaultBindings = map[string]map[string]string{
		"buffer":   {},
		"command":  {},
		"terminal": {},
	}
	commands = nil

	t.Cleanup(func() {
		extCommands = origCmds
		extDefaultBindings = origBindings
		commands = origCommands
	})
}

func TestRegisterCommandOrderAndReload(t *testing.T) {
	resetExtensionsForTest(t)

	calledBefore := false
	RegisterCommand("typx-test-before", func(bp *BufPane, args []string) { calledBefore = true }, nil)

	InitCommands()
	cmd, ok := commands["typx-test-before"]
	require.True(t, ok)
	cmd.action(nil, nil)
	assert.True(t, calledBefore)

	calledAfter := false
	RegisterCommand("typx-test-after", func(bp *BufPane, args []string) { calledAfter = true }, nil)
	cmd, ok = commands["typx-test-after"]
	require.True(t, ok)
	cmd.action(nil, nil)
	assert.True(t, calledAfter)

	// Re-running InitCommands (as :reload does) preserves both extension commands.
	InitCommands()
	assert.Contains(t, commands, "typx-test-before")
	assert.Contains(t, commands, "typx-test-after")
}

func TestRegisterCommandDuplicatePanics(t *testing.T) {
	resetExtensionsForTest(t)

	RegisterCommand("typx-dup-cmd", func(*BufPane, []string) {}, nil)
	assert.Panics(t, func() {
		RegisterCommand("typx-dup-cmd", func(*BufPane, []string) {}, nil)
	})

	InitCommands()
	assert.Panics(t, func() {
		RegisterCommand("save", func(*BufPane, []string) {}, nil)
	})
}

func TestRegisterActionAndDuplicatePanics(t *testing.T) {
	const name = "TypXTestAction"
	delete(BufKeyActions, name)
	t.Cleanup(func() { delete(BufKeyActions, name) })

	RegisterAction(name, func(*BufPane) bool { return true })
	assert.Contains(t, BufKeyActions, name)

	assert.Panics(t, func() {
		RegisterAction(name, func(*BufPane) bool { return false })
	})
	assert.Panics(t, func() {
		RegisterAction("Save", func(*BufPane) bool { return false })
	})
}

func TestRegisterDefaultBindingPrecedenceAndDuplicates(t *testing.T) {
	resetExtensionsForTest(t)

	// Micro's default Alt-m is SpawnMultiCursorSelect (non-darwin) or absent on darwin.
	origBaseAltM, hadBase := bufdefaults["Alt-m"]

	RegisterDefaultBinding("buffer", "Alt-m", "TypXMathInline")
	defaults := DefaultBindings("buffer")
	assert.Equal(t, "TypXMathInline", defaults["Alt-m"])

	// Base map must not be mutated.
	if hadBase {
		assert.Equal(t, origBaseAltM, bufdefaults["Alt-m"])
	} else {
		_, stillAbsent := bufdefaults["Alt-m"]
		assert.False(t, stillAbsent)
	}

	// Second extension claiming same (pane, key) panics.
	assert.Panics(t, func() {
		RegisterDefaultBinding("buffer", "Alt-m", "OtherAction")
	})

	// User bindings.json overrides TypX extension default.
	origCfgDir := config.ConfigDir
	t.Cleanup(func() { config.ConfigDir = origCfgDir })
	tmpDir := t.TempDir()
	config.ConfigDir = tmpDir
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, "bindings.json"),
		[]byte(`{"Alt-m": "Save"}`),
		0644,
	))

	// Register a temporary action so BindKey accepts TypXMathInline if needed.
	const actName = "TypXMathInline"
	delete(BufKeyActions, actName)
	RegisterAction(actName, func(*BufPane) bool { return true })
	t.Cleanup(func() { delete(BufKeyActions, actName) })

	// BindKey writes into config.Bindings["buffer"] via Binder["buffer"].
	origBindings := config.Bindings
	config.Bindings = map[string]map[string]string{
		"buffer":   {},
		"command":  {},
		"terminal": {},
	}
	t.Cleanup(func() { config.Bindings = origBindings })

	InitBindings()
	assert.Equal(t, "Save", config.Bindings["buffer"]["Alt-m"])
}
