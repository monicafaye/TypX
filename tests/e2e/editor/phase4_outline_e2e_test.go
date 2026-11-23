package editor_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/tests/e2e/harness"
)

var ansiSeqRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)

func stripANSI(s string) string {
	return ansiSeqRe.ReplaceAllString(s, "")
}

func TestE2E_Phase4_OutlineInteractiveToggleJumpAndEdgeCases(t *testing.T) {
	harness.RequirePhase23(t)

	doc := strings.Join([]string{
		"= Introduction <sec:intro>",
		"Intro line 1.",
		"Intro line 2.",
		"```typ",
		"= Fake In Fence",
		"```",
		"/* outer /* nested */ = Fake In Comment */",
		"== Methods",
		"#figure(",
		"  rect(),",
		"  caption: [Model Overview],",
		") <fig:model>",
		"Body line.",
		"= Conclusion",
		"Done.",
	}, "\n")

	ed := harness.NewSimEditor(t, harness.SimOptions{
		InitialName: "thesis.typ",
		InitialText: doc,
		Settings: map[string]any{
			"typst.lspEnabled":   false,
			"typst.outlineWidth": float64(30),
		},
		Width:  90,
		Height: 24,
	})

	// 1. Alt+O opens docked Outline on the left and focuses it.
	ed.InjectKey(tcell.KeyRune, 'o', tcell.ModAlt)
	tab := action.MainTab()
	require.Len(t, tab.Panes, 2)
	assert.True(t, action.IsAuxiliaryPane(tab.ActivePane()))
	assert.Equal(t, "Outline", tab.ActivePane().Name())
	assert.Contains(t, ed.ScreenLine(0), "Outline (4)")

	// 2. Navigate down to Conclusion (index 3) and press Enter -> jumps and returns focus.
	ed.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	ed.InjectKey(tcell.KeyRune, 'j', tcell.ModNone)
	ed.InjectKey(tcell.KeyRune, 'j', tcell.ModNone)
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

	assert.False(t, action.IsAuxiliaryPane(tab.ActivePane()))
	assert.Equal(t, 13, ed.Cursor().Y, "expected cursor on '= Conclusion' (line 13)")

	// 3. Narrow resize (< 50 cols) auto-hides Outline cleanly; widening restores it.
	ed.Resize(44, 24)
	require.Len(t, tab.Panes, 1)
	assert.Equal(t, 44, ed.Pane().GetView().Width)

	ed.Resize(90, 24)
	require.Len(t, tab.Panes, 2)
	assert.False(t, action.IsAuxiliaryPane(tab.ActivePane()), "auto-restore must keep editor focused")

	// 4. Multi-tab switching + closing main buffer while Outline is open.
	ed.Pane().AddTab()
	ed.Pump(16)
	require.Len(t, action.Tabs.List, 2)
	action.Tabs.SetActive(0)
	ed.Pump(16)
	require.Len(t, action.MainTab().Panes, 2)

	// Closing the main buffer in Tab 0 while Outline is open must close the tab cleanly.
	ed.Pane().ForceQuit()
	ed.Pump(16)
	require.Len(t, action.Tabs.List, 1)
	assert.False(t, action.IsAuxiliaryPane(action.MainTab().ActivePane()))
}

func TestE2E_Phase4_ConPTY_InteractiveToggleJumpAndCleanQuit(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("ConPTY interactive test runs on Windows")
	}

	typx := harness.BuildTypxBinary(t)
	cfgDir := harness.IsolatedConfigDir(t, map[string]any{
		"basename":           true,
		"typst.lspEnabled":   false,
		"typst.outlineWidth": float64(30),
	})

	workDir := t.TempDir()
	typFile := filepath.Join(workDir, "outline_live.typ")
	doc := strings.Join([]string{
		"= Alpha",
		"First line.",
		"```typ",
		"= Ignored Fence Heading",
		"```",
		"/* outer /* inner */ = Ignored Comment Heading */",
		"== Beta",
		"Second line.",
		"= Conclusion",
		"Final line.",
	}, "\n")
	require.NoError(t, os.WriteFile(typFile, []byte(doc), 0644))

	proc, err := harness.StartConPTY(typx, []string{"--no-lsp", typFile}, map[string]string{
		"TYPX_CONFIG_HOME": cfgDir,
	}, workDir, 90, 26)
	require.NoError(t, err)
	defer proc.Close()

	// Wait for initial render showing document content or filename.
	harness.Eventually(t, 8*time.Second, 50*time.Millisecond, func() (bool, string) {
		plain := stripANSI(proc.Output())
		if strings.Contains(plain, "Alpha") || strings.Contains(plain, "outline_live") {
			return true, ""
		}
		return false, fmt.Sprintf("waiting for initial render; len=%d sample=%q", len(plain), plain)
	})

	// Send Alt+O (\x1bo) to open Outline sidebar.
	require.NoError(t, proc.WriteInput([]byte{0x1b, 'o'}))

	harness.Eventually(t, 5*time.Second, 50*time.Millisecond, func() (bool, string) {
		plain := stripANSI(proc.Output())
		if strings.Contains(plain, "Outline (3)") && strings.Contains(plain, "Conclusion") {
			return true, ""
		}
		return false, fmt.Sprintf("waiting for 'Outline (3)' and 'Conclusion' in ConPTY output; tail=%q", plain)
	})

	// Navigate down twice (Alpha -> Beta -> Conclusion) and press Enter to jump.
	require.NoError(t, proc.WriteInput([]byte{'j', 'j', '\r'}))

	// Wait for active-section marker to move onto Conclusion after Enter jumps & returns focus.
	harness.Eventually(t, 5*time.Second, 50*time.Millisecond, func() (bool, string) {
		plain := stripANSI(proc.Output())
		if strings.Contains(plain, "▎▾ Conclusion") {
			return true, ""
		}
		return false, fmt.Sprintf("waiting for active marker on Conclusion; tail=%q", plain)
	})

	// Send Ctrl+Q (\x11) from the editor while Outline is STILL open -> must exit cleanly (code 0).
	require.NoError(t, proc.WriteInput([]byte{0x11}))
	code, err := proc.WaitExit(5 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, uint32(0), code)
}
