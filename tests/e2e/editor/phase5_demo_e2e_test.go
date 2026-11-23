package editor_test

import (
	"fmt"
	"io"
	"net/http"
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
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/lsp"
	"github.com/monicafaye/TypX/tests/e2e/harness"
)

func demoFixturePath(t testing.TB) string {
	t.Helper()
	root, err := harness.RepoRoot()
	require.NoError(t, err)
	p := filepath.Join(root, "samples", "demo.typ")
	_, err = os.Stat(p)
	require.NoError(t, err, "samples/demo.typ must exist")
	return p
}

func findLineIndex(t testing.TB, b *buffer.Buffer, needle string) int {
	t.Helper()
	for i := 0; i < b.LinesNum(); i++ {
		if strings.Contains(string(b.LineBytes(i)), needle) {
			return i
		}
	}
	t.Fatalf("line containing %q not found in buffer", needle)
	return -1
}

func TestE2E_Phase5_DemoFixture_OfflineNoLSP(t *testing.T) {
	harness.RequirePhase23(t)

	demoPath := demoFixturePath(t)
	ed := harness.NewSimEditor(t, harness.SimOptions{
		Files: []string{demoPath},
		Settings: map[string]any{
			"basename":         true,
			"typst.lspEnabled": false,
			"ruler":            false,
		},
		Width:  100,
		Height: 30,
	})

	// 1. Typst filetype and Typst-first defaults.
	b := ed.Buf()
	assert.Equal(t, "typst", b.FileType())
	assert.True(t, b.IsTypst())
	assert.Equal(t, true, b.Settings["softwrap"])
	assert.Equal(t, true, b.Settings["wordwrap"])
	assert.Equal(t, true, b.Settings["tabstospaces"])
	assert.Equal(t, float64(2), b.Settings["tabsize"])

	// Statusline shows MARKUP, word count, and LSP: off.
	status := ed.StatusLineText()
	assert.Contains(t, status, "MARKUP |")
	assert.Contains(t, status, "words |")
	assert.Contains(t, status, "LSP: off |")

	// 2. Live math concealment on samples/demo.typ (word-boundary safety + active-line unconceal).
	// Disable softwrap locally so visual rows map 1:1 to buffer lines for deterministic screen assertions.
	require.NoError(t, b.SetOptionNative("softwrap", false))
	wbLine := findLineIndex(t, b, "$sin(theta) + index + summary != 0$")
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 0, Y: wbLine - 2})
	ed.Pane().Center()
	ed.Pump(4)

	// Find rendered row for wbLine on screen.
	rendered := ""
	for row := 0; row < 28; row++ {
		s := ed.ScreenLine(row)
		if strings.Contains(s, "sin(") && strings.Contains(s, "index") {
			rendered = s
			break
		}
	}
	require.NotEmpty(t, rendered, "expected word-boundary math line to be visible on screen")
	assert.Contains(t, rendered, "$sin(θ) + index + summary ≠ 0$", "theta and != must conceal while sin/index/summary stay intact")

	// Moving cursor onto wbLine must unconceal it to raw Typst source.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 2, Y: wbLine})
	ed.Pump(4)
	assert.Contains(t, ed.StatusLineText(), "MATH |")
	unconcealed := false
	for row := 0; row < 28; row++ {
		if strings.Contains(ed.ScreenLine(row), "$sin(theta) + index + summary != 0$") {
			unconcealed = true
			break
		}
	}
	assert.True(t, unconcealed, "active cursor line must render raw Typst source")

	// Move off wbLine and verify Alt+C toggles concealment off and back on.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 0, Y: wbLine - 2})
	ed.Pump(4)
	ed.InjectKey(tcell.KeyRune, 'c', tcell.ModAlt)
	toggledOff := false
	for row := 0; row < 28; row++ {
		if strings.Contains(ed.ScreenLine(row), "$sin(theta) + index + summary != 0$") {
			toggledOff = true
			break
		}
	}
	assert.True(t, toggledOff, "Alt+C should disable concealment live")
	ed.InjectKey(tcell.KeyRune, 'c', tcell.ModAlt)

	// 3. Document Outline parses headings + figures and ignores fenced/commented fake headings.
	ed.InjectKey(tcell.KeyRune, 'o', tcell.ModAlt)
	tab := action.MainTab()
	require.Len(t, tab.Panes, 2)
	assert.True(t, action.IsAuxiliaryPane(tab.ActivePane()))
	assert.Equal(t, "Outline", tab.ActivePane().Name())

	outlineDump := strings.Builder{}
	for row := 0; row < 28; row++ {
		outlineDump.WriteString(ed.ScreenLine(row))
		outlineDump.WriteByte('\n')
	}
	outText := outlineDump.String()
	// 8 headings + 2 figures = 10 items (fake headings in fence and block comment must be ignored).
	assert.Contains(t, outText, "Outline (10)")
	assert.Contains(t, outText, "Introduction to TypX")
	assert.Contains(t, outText, "Mathematical Typesetting")
	assert.Contains(t, outText, "Diagnostics Verification")
	assert.NotContains(t, outText, "Ignored Heading")

	// Jump to bottom item (Diagnostics Verification) using End + Enter.
	ed.InjectKey(tcell.KeyEnd, 0, tcell.ModNone)
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	assert.False(t, action.IsAuxiliaryPane(tab.ActivePane()))
	diagHeadingLine := findLineIndex(t, b, "= Diagnostics Verification")
	assert.Equal(t, diagHeadingLine, ed.Cursor().Y)

	// Close Outline cleanly.
	ed.InjectKey(tcell.KeyRune, 'o', tcell.ModAlt)
	require.Len(t, tab.Panes, 1)

	// 4. Embedded help topics open cleanly and include TypX documentation.
	ed.RunCommand("help typst")
	ed.Pump(4)
	helpText := string(ed.Buf().Bytes())
	assert.Contains(t, helpText, "TypX — Typst Authoring Guide")
	assert.Contains(t, helpText, ":preview")
	assert.Contains(t, helpText, ":outline")
	assert.Contains(t, helpText, ":conceal")
	assert.Contains(t, helpText, "typst.tinymistPath")

	ed.RunCommand("help options")
	ed.Pump(4)
	optsHelp := string(ed.Buf().Bytes())
	assert.Contains(t, optsHelp, "TYPX_CONFIG_HOME")
	assert.Contains(t, optsHelp, "typst.concealMode")
	assert.Contains(t, optsHelp, "typst.outlineWidth")

	ed.RunCommand("help commands")
	ed.Pump(4)
	cmdsHelp := string(ed.Buf().Bytes())
	assert.Contains(t, cmdsHelp, "tinymist-status")
	assert.Contains(t, cmdsHelp, "insert-figure")

	ed.RunCommand("help defaultkeys")
	ed.Pump(4)
	keysHelp := string(ed.Buf().Bytes())
	assert.Contains(t, keysHelp, "TypX / Typst Authoring")
	assert.Contains(t, keysHelp, "Alt-p")
	assert.Contains(t, keysHelp, "Alt-o")
}

func TestE2E_Phase5_DemoFixture_WithRealTinymist(t *testing.T) {
	harness.RequirePhase23(t)

	disc := lsp.Discover("")
	if disc.Path == "" {
		t.Skip("tinymist binary not installed on host; skipping real-tinymist smoke test")
	}

	// Work on an isolated copy of samples/demo.typ so editing tests never touch the repo fixture.
	rawDemo, err := os.ReadFile(demoFixturePath(t))
	require.NoError(t, err)
	workDir := t.TempDir()
	demoCopy := filepath.Join(workDir, "demo.typ")
	require.NoError(t, os.WriteFile(demoCopy, rawDemo, 0644))

	ed := harness.NewSimEditor(t, harness.SimOptions{
		Files: []string{demoCopy},
		Settings: map[string]any{
			"basename":                 true,
			"typst.lspEnabled":         true,
			"typst.tinymistPath":       disc.Path,
			"typst.debounceMs":         50,
			"typst.previewOpenBrowser": false,
		},
		Width:  100,
		Height: 28,
	})
	t.Cleanup(func() { lsp.Shutdown() })

	lsp.Init(lsp.Options{})
	ed.RunCommand("tinymist-restart")

	// 1. Wait for real tinymist to initialize and report LSP: ready.
	ready := ed.PumpUntil(10*time.Second, func() bool {
		return strings.Contains(ed.StatusLineText(), "LSP: ready |")
	})
	require.True(t, ready, "expected real tinymist to reach LSP: ready, got status %q", ed.StatusLineText())

	// 2. Uncomment the intentional diagnostic line -> real tinymist must report the diagnostic.
	diagLine := findLineIndex(t, ed.Buf(), "#typx_intentional_diagnostic_demo")
	if strings.HasPrefix(string(ed.Buf().LineBytes(diagLine)), "// ") {
		ed.Buf().Remove(buffer.Loc{X: 0, Y: diagLine}, buffer.Loc{X: 3, Y: diagLine})
	}
	msgs := ed.WaitDiagnostics(10*time.Second, 1)
	require.GreaterOrEqual(t, len(msgs), 1)
	foundIntentional := false
	for _, m := range msgs {
		if m.Start.Y == diagLine {
			foundIntentional = true
		} else {
			t.Errorf("unexpected diagnostic on line %d (expected only intentional line %d): %s", m.Start.Y, diagLine, m.Msg)
		}
	}
	assert.True(t, foundIntentional, "expected real tinymist diagnostic on intentional error line %d", diagLine)

	// 3. Hover via Alt+K on '#figure(' returns real hover docs from tinymist.
	figLine := findLineIndex(t, ed.Buf(), "#figure(")
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 2, Y: figLine})
	ed.InjectKey(tcell.KeyRune, 'k', tcell.ModAlt)
	gotHover := ed.PumpUntil(5*time.Second, func() bool {
		msg, _, _, _ := ed.InfoBarState()
		return strings.TrimSpace(msg) != "" && !strings.Contains(strings.ToLower(msg), "no hover")
	})
	assert.True(t, gotHover, "expected non-empty hover response from real tinymist, got %q", ed.InfoBarText())

	// 4. Outline sidebar stays healthy when LSP documentSymbol enrichment is active.
	ed.InjectKey(tcell.KeyRune, 'o', tcell.ModAlt)
	ed.Pump(8)
	require.Len(t, action.MainTab().Panes, 2)
	assert.Contains(t, ed.ScreenLine(0), "Outline")
	ed.InjectKey(tcell.KeyRune, 'o', tcell.ModAlt)
	require.Len(t, action.MainTab().Panes, 1)

	// 5. Comment out the intentional diagnostic line -> real tinymist clears diagnostics cleanly!
	ed.Buf().Insert(buffer.Loc{X: 0, Y: diagLine}, "// ")
	ed.WaitNoDiagnostics(10 * time.Second)

	// 6. Real tinymist live preview via Alt+P uses doStartPreview (--no-open),
	// serves the rendered demo.typ preview page, and stops cleanly on second Alt+P.
	ed.InjectKey(tcell.KeyRune, 'p', tcell.ModAlt)
	previewStarted := ed.PumpUntil(10*time.Second, func() bool {
		return strings.Contains(ed.StatusLineText(), "PREVIEW: ON |")
	})
	require.True(t, previewStarted, "expected PREVIEW: ON in statusline, got %q", ed.StatusLineText())

	var previewURL string
	rePreviewURL := regexp.MustCompile(`Preview: active \(lsp\) (http://127\.0\.0\.1:\d+)`)
	gotURL := ed.PumpUntil(5*time.Second, func() bool {
		ed.RunCommand("tinymist-status")
		ed.Pump(2)
		if m := rePreviewURL.FindStringSubmatch(string(ed.Buf().Bytes())); len(m) == 2 {
			previewURL = m[1]
			return true
		}
		return false
	})
	require.True(t, gotURL, "expected active lsp preview URL in :tinymist-status")

	resp, err := http.Get(previewURL)
	require.NoError(t, err, "expected preview HTTP server at %s to respond", previewURL)
	bodyBytes, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(bodyBytes), "<title>demo.typ</title>")

	// Switch back to demo.typ pane and stop preview cleanly via Alt+P.
	ed.RunCommand("quit")
	ed.Pump(2)
	ed.InjectKey(tcell.KeyRune, 'p', tcell.ModAlt)
	previewStopped := ed.PumpUntil(5*time.Second, func() bool {
		return !strings.Contains(ed.StatusLineText(), "PREVIEW: ON")
	})
	assert.True(t, previewStopped, "expected PREVIEW: ON cleared after second Alt+P")

	// 7. :diagnostics and :tinymist-status execute cleanly.
	ed.RunCommand("diagnostics")
	ed.Pump(4)
	ed.RunCommand("tinymist-status")
	ed.Pump(4)
	statusBuf := string(ed.Buf().Bytes())
	assert.Contains(t, statusBuf, "tinymist")
	assert.Contains(t, statusBuf, "ready")

	// 8. Clean shutdown.
	lsp.Shutdown()
}

func TestE2E_Phase5_ConPTY_DemoFixture_OfflineAndOnline(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("ConPTY interactive test runs on Windows")
	}

	typx := harness.BuildTypxBinary(t)
	demoPath := demoFixturePath(t)
	workDir := filepath.Dir(demoPath)

	t.Run("offline --no-lsp", func(t *testing.T) {
		cfgDir := harness.IsolatedConfigDir(t, map[string]any{
			"basename":           true,
			"typst.outlineWidth": float64(30),
		})
		proc, err := harness.StartConPTY(typx, []string{"--no-lsp", demoPath}, map[string]string{
			"TYPX_CONFIG_HOME": cfgDir,
		}, workDir, 100, 28)
		require.NoError(t, err)
		defer proc.Close()

		harness.Eventually(t, 8*time.Second, 50*time.Millisecond, func() (bool, string) {
			plain := stripANSI(proc.Output())
			if strings.Contains(plain, "demo.typ") && strings.Contains(plain, "LSP: off") {
				return true, ""
			}
			return false, fmt.Sprintf("waiting for demo.typ & LSP: off; tail=%q", plain)
		})

		// Toggle Outline (Alt+O) and confirm it renders sections from samples/demo.typ.
		require.NoError(t, proc.WriteInput([]byte{0x1b, 'o'}))
		harness.Eventually(t, 5*time.Second, 50*time.Millisecond, func() (bool, string) {
			plain := stripANSI(proc.Output())
			if strings.Contains(plain, "Outline (10)") {
				return true, ""
			}
			return false, fmt.Sprintf("waiting for Outline (10); tail=%q", plain)
		})

		// Return focus to editor (Esc), toggle conceal (Alt+C), and quit cleanly (Ctrl+Q).
		require.NoError(t, proc.WriteInput([]byte{0x1b}))
		time.Sleep(100 * time.Millisecond)
		require.NoError(t, proc.WriteInput([]byte{0x1b, 'c'}))
		time.Sleep(100 * time.Millisecond)
		require.NoError(t, proc.WriteInput([]byte{0x11}))

		code, err := proc.WaitExit(5 * time.Second)
		require.NoError(t, err)
		assert.Equal(t, uint32(0), code)
	})

	t.Run("online with real tinymist", func(t *testing.T) {
		disc := lsp.Discover("")
		if disc.Path == "" {
			t.Skip("tinymist binary not installed on host; skipping online ConPTY smoke test")
		}

		cfgDir := harness.IsolatedConfigDir(t, map[string]any{
			"basename":                 true,
			"typst.lspEnabled":         true,
			"typst.tinymistPath":       disc.Path,
			"typst.previewOpenBrowser": false,
		})
		proc, err := harness.StartConPTY(typx, []string{demoPath}, map[string]string{
			"TYPX_CONFIG_HOME": cfgDir,
		}, workDir, 100, 28)
		require.NoError(t, err)
		defer proc.Close()

		// Wait for initial render and LSP startup, then open Outline (Alt+O) to force a
		// full statusline repaint so diff-rendered cells show the complete "LSP: ready" text.
		harness.Eventually(t, 8*time.Second, 50*time.Millisecond, func() (bool, string) {
			plain := stripANSI(proc.Output())
			if strings.Contains(plain, "demo.typ") && strings.Contains(plain, "LSP:") {
				return true, ""
			}
			return false, fmt.Sprintf("waiting for initial render with LSP status; tail=%q", plain)
		})

		harness.Eventually(t, 12*time.Second, 250*time.Millisecond, func() (bool, string) {
			// Toggle Outline open (or refresh) to repaint statusline in a new column range.
			_ = proc.WriteInput([]byte{0x1b, 'o'})
			time.Sleep(150 * time.Millisecond)
			plain := stripANSI(proc.Output())
			if strings.Contains(plain, "LSP: ready") {
				return true, ""
			}
			return false, fmt.Sprintf("waiting for real tinymist LSP: ready in ConPTY; tail=%q", plain)
		})

		// Return focus to editor (Esc) if Outline is focused, then clean quit via Ctrl+Q.
		require.NoError(t, proc.WriteInput([]byte{0x1b}))
		time.Sleep(100 * time.Millisecond)
		require.NoError(t, proc.WriteInput([]byte{0x11}))
		code, err := proc.WaitExit(6 * time.Second)
		require.NoError(t, err)
		assert.Equal(t, uint32(0), code)
	})
}
