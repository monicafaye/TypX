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

func TestExportCommandAndExportOnSave(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)
	config.GlobalSettings["typst.lspEnabled"] = false // Export works offline via tinymist compile

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	typPath := filepath.Join(dir, "paper.typ")
	b := openTestBuffer(t, typPath, "= Saved Title\n")
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()

	// Unsaved edit should be shadow-staged while writing to real paper.pdf stem.
	b.Insert(buffer.Loc{X: 0, Y: 0}, "unsaved ")
	require.True(t, b.Modified())

	ExportCommand()(bp, nil) // default pdf
	pdfPath := filepath.Join(dir, "paper.pdf")
	waitForCondition(t, 3*time.Second, func() bool {
		data, err := os.ReadFile(pdfPath)
		return err == nil && string(data) == "MOCK_PDF:unsaved = Saved Title\n"
	}, "expected :export to shadow-stage modified buffer into paper.pdf")

	// Multi-page png defaults to stem-{0p}.png (mock replaces {0p} -> 01).
	ExportCommand()(bp, []string{"png"})
	pngPath := filepath.Join(dir, "paper-01.png")
	waitForCondition(t, 3*time.Second, func() bool {
		_, err := os.Stat(pngPath)
		return err == nil
	}, "expected :export png to produce paper-01.png")

	// Experimental html exit 0 + stderr warning still reports success.
	outHTML := filepath.Join(dir, "custom.html")
	ExportCommand()(bp, []string{outHTML})
	waitForCondition(t, 3*time.Second, func() bool {
		data, err := os.ReadFile(outHTML)
		return err == nil && len(data) > 0 && strings.Contains(action.InfoBar.Msg, "Exported custom.html")
	}, "expected :export custom.html to succeed with warning note")

	// Pinned main redirects export even from subfile; dirty subfile adds warning.
	mainPath := filepath.Join(dir, "main.typ")
	require.NoError(t, os.WriteFile(mainPath, []byte("= Pinned Main\n"), 0644))
	PinCommand()(bp, []string{mainPath})
	ExportCommand()(bp, []string{"svg"})
	pinnedSVG := filepath.Join(dir, "main-01.svg")
	waitForCondition(t, 3*time.Second, func() bool {
		data, err := os.ReadFile(pinnedSVG)
		return err == nil && string(data) == "MOCK_SVG:= Pinned Main\n" &&
			strings.Contains(action.InfoBar.Msg, "unsaved subfiles not included")
	}, "expected :export with pinned main to export main-01.svg and warn on dirty subfile")
	UnpinCommand()(bp, nil)

	// typst.exportOnSave exports on explicit Save, never on AutoSave.
	config.GlobalSettings["typst.exportOnSave"] = "pdf"
	require.NoError(t, os.Remove(pdfPath))
	defaultManager.OnBufferSave(b, buffer.SaveEvent{AutoSave: true})
	time.Sleep(150 * time.Millisecond)
	_, err := os.Stat(pdfPath)
	assert.True(t, os.IsNotExist(err), "exportOnSave must ignore AutoSave")

	require.NoError(t, b.Save())
	buffer.FlushObservers()
	waitForCondition(t, 3*time.Second, func() bool {
		data, err := os.ReadFile(pdfPath)
		return err == nil && len(data) > 0
	}, "expected manual save with typst.exportOnSave=pdf to export paper.pdf")
}
