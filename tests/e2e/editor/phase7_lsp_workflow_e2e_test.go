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
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/lsp"
	"github.com/monicafaye/TypX/tests/e2e/harness"
)

func bootSimWithMockTinymist(t *testing.T, mockBin string, files []string, extraSettings map[string]any) (*harness.SimEditor, string) {
	t.Helper()

	mockEnv, _, traceFile, _ := harness.MockEnv(t, harness.MockOptions{})
	for k, v := range mockEnv {
		t.Setenv(k, v)
	}

	settings := map[string]any{
		"basename":                 true,
		"typst.lspEnabled":         true,
		"typst.tinymistPath":       mockBin,
		"typst.debounceMs":         30,
		"typst.previewOpenBrowser": false,
	}
	for k, v := range extraSettings {
		settings[k] = v
	}

	ed := harness.NewSimEditor(t, harness.SimOptions{
		Files:    files,
		Settings: settings,
		Width:    100,
		Height:   24,
	})
	t.Cleanup(func() { lsp.Shutdown() })

	lsp.Init(lsp.Options{})
	ed.RunCommand("tinymist-restart")

	ready := ed.PumpUntil(5*time.Second, func() bool {
		if !strings.Contains(ed.StatusLineText(), "LSP: ready |") {
			return false
		}
		recs, err := harness.ReadMockTrace(traceFile)
		if err != nil {
			return false
		}
		return harness.HasTraceMethod(recs, "textDocument/didOpen")
	})
	require.True(t, ready, "timed out waiting for mock tinymist ready + didOpen; statusline=%q", ed.StatusLineText())
	return ed, traceFile
}

func TestE2E_PinAndUnpin(t *testing.T) {
	harness.RequirePhase23(t)
	mockBin := harness.BuildMockTinymist(t)

	workDir := t.TempDir()
	mainPath := filepath.Join(workDir, "main.typ")
	chapPath := filepath.Join(workDir, "chapter.typ")
	require.NoError(t, os.WriteFile(mainPath, []byte("= Main\n#include \"chapter.typ\"\n"), 0644))
	require.NoError(t, os.WriteFile(chapPath, []byte("== Chapter\nHello\n"), 0644))

	ed, traceFile := bootSimWithMockTinymist(t, mockBin, []string{chapPath}, nil)

	ed.RunCommand("pin main.typ")
	pinnedOK := ed.PumpUntil(4*time.Second, func() bool {
		if !strings.Contains(ed.StatusLineText(), "PIN: main.typ |") {
			return false
		}
		recs, err := harness.ReadMockTrace(traceFile)
		if err != nil {
			return false
		}
		for _, r := range recs {
			if r.Event == "pinMain" && strings.EqualFold(filepath.Clean(r.Note), filepath.Clean(mainPath)) {
				return true
			}
		}
		return false
	})
	assert.True(t, pinnedOK, "expected PIN statusline and tinymist.pinMain trace, statusline=%q", ed.StatusLineText())

	ed.RunCommand("unpin")
	unpinnedOK := ed.PumpUntil(4*time.Second, func() bool {
		if strings.Contains(ed.StatusLineText(), "PIN:") {
			return false
		}
		recs, err := harness.ReadMockTrace(traceFile)
		if err != nil {
			return false
		}
		seenPin := false
		for _, r := range recs {
			if r.Event == "pinMain" {
				if r.Note != "" {
					seenPin = true
				} else if seenPin {
					return true
				}
			}
		}
		return false
	})
	assert.True(t, unpinnedOK, "expected PIN cleared and null tinymist.pinMain trace")
}

func TestE2E_ExportAndExportOnSave(t *testing.T) {
	harness.RequirePhase23(t)
	mockBin := harness.BuildMockTinymist(t)

	workDir := t.TempDir()
	docPath := filepath.Join(workDir, "report.typ")
	pdfPath := filepath.Join(workDir, "report.pdf")
	require.NoError(t, os.WriteFile(docPath, []byte("= Report\nInitial content\n"), 0644))

	ed, traceFile := bootSimWithMockTinymist(t, mockBin, []string{docPath}, nil)

	ed.RunCommand("export pdf")
	exported := ed.PumpUntil(5*time.Second, func() bool {
		data, err := os.ReadFile(pdfPath)
		if err != nil || !strings.HasPrefix(string(data), "%PDF-1.4") {
			return false
		}
		recs, err := harness.ReadMockTrace(traceFile)
		return err == nil && harness.HasTraceEvent(recs, "compile")
	})
	require.True(t, exported, "expected :export pdf to create %s via tinymist compile", pdfPath)

	// Remove PDF, enable typst.exportOnSave, edit and Ctrl+S to verify auto-export.
	require.NoError(t, os.Remove(pdfPath))
	config.GlobalSettings["typst.exportOnSave"] = "pdf"

	ed.Buf().Insert(ed.Buf().End(), "Updated line\n")
	ed.InjectKey(tcell.KeyCtrlS, 0, tcell.ModCtrl)

	reexported := ed.PumpUntil(5*time.Second, func() bool {
		if ed.Buf().Modified() {
			return false
		}
		data, err := os.ReadFile(pdfPath)
		return err == nil && strings.HasPrefix(string(data), "%PDF-1.4")
	})
	assert.True(t, reexported, "expected typst.exportOnSave=pdf to rebuild PDF on Ctrl+S")
}

func TestE2E_FormatAndFormatOnSave(t *testing.T) {
	harness.RequirePhase23(t)
	mockBin := harness.BuildMockTinymist(t)

	workDir := t.TempDir()
	docPath := filepath.Join(workDir, "fmt.typ")
	unformatted := "=   Title\n#{\n  let  x=1\n}\n"
	formatted := "= Title\n#{\n  let x = 1\n}\n"
	require.NoError(t, os.WriteFile(docPath, []byte(unformatted), 0644))

	ed, _ := bootSimWithMockTinymist(t, mockBin, []string{docPath}, nil)

	ed.RunCommand("format")
	fmtOK := ed.PumpUntil(4*time.Second, func() bool {
		return string(ed.Buf().Bytes()) == formatted
	})
	require.True(t, fmtOK, "expected :format to format buffer, got %q", string(ed.Buf().Bytes()))

	// Single Ctrl+Z undo restores unformatted content atomically.
	ed.InjectKey(tcell.KeyCtrlZ, 0, tcell.ModCtrl)
	assert.Equal(t, unformatted, string(ed.Buf().Bytes()))

	// Enable formatOnSave and press Ctrl+S -> writes formatted text to disk.
	config.GlobalSettings["typst.formatOnSave"] = true
	ed.InjectKey(tcell.KeyCtrlS, 0, tcell.ModCtrl)

	savedFormatted := ed.PumpUntil(4*time.Second, func() bool {
		if ed.Buf().Modified() {
			return false
		}
		disk, err := os.ReadFile(docPath)
		return err == nil && string(disk) == formatted
	})
	assert.True(t, savedFormatted, "expected typst.formatOnSave to write formatted file on Ctrl+S")
}

func TestE2E_DefinitionTypAndBibAndJumpBack(t *testing.T) {
	harness.RequirePhase23(t)
	mockBin := harness.BuildMockTinymist(t)

	workDir := t.TempDir()
	mainPath := filepath.Join(workDir, "main.typ")
	helperPath := filepath.Join(workDir, "helper.typ")
	bibPath := filepath.Join(workDir, "refs.bib")
	require.NoError(t, os.WriteFile(helperPath, []byte("#let greet() = [hi]\n"), 0644))
	require.NoError(t, os.WriteFile(bibPath, []byte("@book{knuth84, title={TeX}}\n"), 0644))
	require.NoError(t, os.WriteFile(mainPath, []byte("#import \"helper.typ\": greet\nSee @knuth84\n"), 0644))

	ed, _ := bootSimWithMockTinymist(t, mockBin, []string{mainPath}, nil)

	// 1. :definition on line 0 ("helper") jumps to helper.typ in a new tab.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 10, Y: 0})
	ed.RunCommand("definition")
	jumpedTyp := ed.PumpUntil(4*time.Second, func() bool {
		return strings.EqualFold(filepath.Clean(ed.Buf().AbsPath), filepath.Clean(helperPath))
	})
	require.True(t, jumpedTyp, "expected :definition to open helper.typ, got %q", ed.Buf().AbsPath)

	// 2. Alt+U jumps back to main.typ at original cursor.
	ed.InjectKey(tcell.KeyRune, 'u', tcell.ModAlt)
	assert.True(t, strings.EqualFold(filepath.Clean(ed.Buf().AbsPath), filepath.Clean(mainPath)))
	assert.Equal(t, buffer.Loc{X: 10, Y: 0}, ed.Cursor())

	// 3. F12 on line 1 ("@knuth84") jumps to refs.bib.
	ed.Pane().Cursor.GotoLoc(buffer.Loc{X: 6, Y: 1})
	ed.InjectKey(tcell.KeyF12, 0, tcell.ModNone)
	jumpedBib := ed.PumpUntil(4*time.Second, func() bool {
		return strings.EqualFold(filepath.Clean(ed.Buf().AbsPath), filepath.Clean(bibPath))
	})
	assert.True(t, jumpedBib, "expected F12 to jump to refs.bib, got %q", ed.Buf().AbsPath)
}

func TestE2E_InteractiveDiagnosticsListPaneAndF8(t *testing.T) {
	harness.RequirePhase23(t)
	mockBin := harness.BuildMockTinymist(t)

	workDir := t.TempDir()
	docPath := filepath.Join(workDir, "diag.typ")
	require.NoError(t, os.WriteFile(docPath, []byte("first ERR_TRIGGER\nmiddle ok\nsecond ERR_TRIGGER\n"), 0644))

	ed, _ := bootSimWithMockTinymist(t, mockBin, []string{docPath}, nil)
	ed.WaitDiagnostics(5*time.Second, 2)

	// Open interactive :diagnostics ListPane drawer after moving off the error line so both
	// the ListPane drawer and DIAGNOSTICS bottom bar badge are visible cleanly.
	ed.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	ed.RunCommand("diagnostics")
	lp := lsp.ListPaneForTab(action.MainTab())
	require.NotNil(t, lp, "expected :diagnostics to open ListPane")
	assert.Equal(t, lp, action.MainTab().ActivePane())

	screenDump := strings.Join(ed.AllScreenLines(), "\n")
	assert.Contains(t, screenDump, "Diagnostics (2 err, 0 warn)")
	assert.Contains(t, screenDump, "ERR_TRIGGER detected")
	assert.Contains(t, screenDump, "DIAGNOSTICS")

	// Move down to second diagnostic and press Enter to jump to line 2.
	ed.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	ed.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	assert.Equal(t, 2, ed.Cursor().Y)
	assert.Equal(t, 7, ed.Cursor().X)

	// Close ListPane cleanly, then verify F8 wraps from line 2 -> line 0 and :diag-prev goes back to line 2.
	assert.True(t, lsp.CloseListPaneForTab(action.MainTab()))
	ed.InjectKey(tcell.KeyF8, 0, tcell.ModNone)
	assert.Equal(t, 0, ed.Cursor().Y)
	assert.Equal(t, 6, ed.Cursor().X)

	ed.RunCommand("diag-prev")
	assert.Equal(t, 2, ed.Cursor().Y)
	assert.Equal(t, 7, ed.Cursor().X)
}
