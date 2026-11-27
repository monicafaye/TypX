package outline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/micro-editor/tcell/v2"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/typst"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestWalkProjectOutlineInterleavingCyclesAndOpenBuffers(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.typ")
	ch1Path := filepath.Join(dir, "chapters", "ch1.typ")
	ch2Path := filepath.Join(dir, "chapters", "ch2.typ")
	sharedPath := filepath.Join(dir, "shared.typ")
	linkPath := filepath.Join(dir, "loop_link.typ")

	writeTestFile(t, mainPath, strings.Join([]string{
		"= Preface",
		`#include "chapters/ch1.typ"`,
		`#include "/chapters/ch2.typ"`,
		`#include "main.typ"`, // self-include cycle
		`#include "loop_link.typ"`,
		"= Epilogue",
	}, "\n"))
	writeTestFile(t, ch1Path, strings.Join([]string{
		"= Chapter One",
		`#include "../main.typ"`,   // back-edge cycle
		`#include "../shared.typ"`, // diamond 1
		"== Section 1.1",
	}, "\n"))
	writeTestFile(t, ch2Path, strings.Join([]string{
		"= Chapter Two",
		`#include "../shared.typ"`, // diamond 2 (already visited)
	}, "\n"))
	writeTestFile(t, sharedPath, "= Shared Disk Title\n")

	// Symlink cycle back to main.typ (best-effort if OS permits symlinks).
	_ = os.Symlink(mainPath, linkPath)

	// Open shared.typ in memory with unsaved edits; walker must prefer OpenBuffers over disk.
	buffer.CloseOpenBuffers()
	t.Cleanup(func() {
		buffer.CloseOpenBuffers()
		buffer.FlushObservers()
	})
	sharedBuf := buffer.NewBufferFromString("= Shared Unsaved Title\n", sharedPath, buffer.BTDefault)
	_ = sharedBuf

	items, isMulti, walked := walkProjectOutline(mainPath, nil, nil, ParseOptions{}, func(p string) string {
		return dir
	})
	if !isMulti {
		t.Fatal("expected multi-file outline")
	}
	if len(walked) < 4 {
		t.Fatalf("expected at least 4 visited files, got %d", len(walked))
	}

	// Verify exact document order and KindFile headers.
	var gotSummary []string
	for _, it := range items {
		gotSummary = append(gotSummary, it.Kind.String()+":"+it.Title)
	}
	wantSummary := []string{
		"file:main.typ",
		"heading:Preface",
		"file:chapters/ch1.typ",
		"heading:Chapter One",
		"file:shared.typ",
		"heading:Shared Unsaved Title",
		"file:chapters/ch1.typ",
		"heading:Section 1.1",
		"file:chapters/ch2.typ",
		"heading:Chapter Two",
		"file:main.typ",
		"heading:Epilogue",
	}
	if strings.Join(gotSummary, "|") != strings.Join(wantSummary, "|") {
		t.Fatalf("walkProjectOutline order mismatch:\ngot:  %v\nwant: %v", gotSummary, wantSummary)
	}

	// Empty project path / nonexistent path safety.
	if emptyItems, multi, _ := walkProjectOutline("", nil, nil, ParseOptions{}, nil); len(emptyItems) != 0 || multi {
		t.Fatalf("expected empty result for empty path, got %#v multi=%v", emptyItems, multi)
	}
	_ = typst.SliceSource(nil)
}

func TestOutlinePaneMultiFileToggleFilterSyncAndJump(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.typ")
	ch1Path := filepath.Join(dir, "chapters", "ch1.typ")

	mainContent := strings.Join([]string{
		"= Preface",
		"Line 1",
		"Line 2",
		"Line 3",
		`#include "chapters/ch1.typ"`,
		"= Epilogue",
	}, "\n")
	ch1Content := strings.Join([]string{
		"Preamble inside chapter",
		"= Chapter One",
		"Body text",
		"== Deep Dive",
	}, "\n")
	writeTestFile(t, mainPath, mainContent)
	writeTestFile(t, ch1Path, ch1Content)

	// Open ch1.typ as the active tab, with main.typ pinned as project entrypoint.
	panes := setupTabs(t, 90, 24, struct{ name, text string }{
		name: ch1Path,
		text: ch1Content,
	})
	ch1Pane := panes[0]
	tab := action.MainTab()

	SetRootResolver(func(string) string { return dir })
	SetMainFileResolver(func(string) string { return mainPath })

	// Track LSP enrichment calls: must NOT enrich when isMultiFile is true, MUST enrich in [File] mode.
	var lspCalls int
	SetSymbolProvider(func(b *buffer.Buffer, cb func(version uint64, symbols []Symbol)) bool {
		lspCalls++
		return true
	})

	op, ok := Open(ch1Pane, true)
	if !ok || op == nil {
		t.Fatal("failed to open multi-file outline")
	}
	if !op.IsProjectMode() || !op.IsMultiFile() {
		t.Fatalf("expected projectMode=true and isMultiFile=true, got project=%v multi=%v", op.IsProjectMode(), op.IsMultiFile())
	}
	if lspCalls != 0 {
		t.Fatalf("expected EnrichWithSymbols/LSP skipped in multi-file mode, got %d calls", lspCalls)
	}

	renderAll()
	ov := op.GetView()
	rendered := paneScreenText(ov.X, ov.Y, ov.Width, ov.Height)
	for _, want := range []string{"Outline [Project] (4)", "▸ main.typ", "Preface", "▸ chapters/ch1.typ", "Chapter One", "Epilogue", "p:file"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected rendered multi-file outline to contain %q, got:\n%s", want, rendered)
		}
	}

	// Cursor in ch1.typ on line 3 ("== Deep Dive") must highlight ch1.typ's Deep Dive, NOT main.typ's Epilogue!
	ch1Pane.Cursor.GotoLoc(buffer.Loc{X: 0, Y: 3})
	buffer.FlushObservers()
	activeIdx := op.ActiveItemIndex()
	vis := op.VisibleItems()
	if activeIdx < 0 || activeIdx >= len(vis) || vis[activeIdx].Title != "Deep Dive" {
		t.Fatalf("expected active item 'Deep Dive', got idx=%d vis=%#v", activeIdx, vis)
	}

	// Cursor on line 0 (before first heading in ch1.typ) must set activeIdx == -1, not match Preface at line 0 of main.typ.
	ch1Pane.Cursor.GotoLoc(buffer.Loc{X: 0, Y: 0})
	buffer.FlushObservers()
	if op.ActiveItemIndex() != -1 {
		t.Fatalf("expected activeIdx=-1 before ch1.typ's first heading, got %d (%#v)", op.ActiveItemIndex(), vis[op.ActiveItemIndex()])
	}

	// Navigation with g / G / j / k never lands on KindFile headers.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'g', tcell.ModNone, ""))
	if vis[op.SelectedIndex()].Kind == KindFile || vis[op.SelectedIndex()].Title != "Preface" {
		t.Fatalf("expected 'g' to skip KindFile and select 'Preface', got idx=%d item=%#v", op.SelectedIndex(), vis[op.SelectedIndex()])
	}
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone, ""))
	if vis[op.SelectedIndex()].Kind == KindFile || vis[op.SelectedIndex()].Title != "Chapter One" {
		t.Fatalf("expected 'j' to skip KindFile and select 'Chapter One', got idx=%d item=%#v", op.SelectedIndex(), vis[op.SelectedIndex()])
	}
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'k', tcell.ModNone, ""))
	if vis[op.SelectedIndex()].Title != "Preface" {
		t.Fatalf("expected 'k' to skip KindFile back to 'Preface', got idx=%d item=%#v", op.SelectedIndex(), vis[op.SelectedIndex()])
	}

	// Filtering "/Deep" keeps parent KindFile header "chapters/ch1.typ" and skips KindFile for selection.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '/', tcell.ModNone, ""))
	for _, r := range "Deep" {
		action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone, ""))
	}
	filteredVis := op.VisibleItems()
	if len(filteredVis) != 2 || filteredVis[0].Kind != KindFile || filteredVis[0].Title != "chapters/ch1.typ" || filteredVis[1].Title != "Deep Dive" {
		t.Fatalf("unexpected filtered multi-file items: %#v", filteredVis)
	}
	if op.SelectedIndex() != 1 {
		t.Fatalf("expected selection snapped past KindFile to idx 1, got %d", op.SelectedIndex())
	}
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, ""))

	// Press 'p' to toggle to [File] mode (shows only ch1.typ's 2 headings and enables LSP enrichment).
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'p', tcell.ModNone, ""))
	renderAll()
	if op.IsProjectMode() || op.IsMultiFile() {
		t.Fatalf("expected projectMode=false and isMultiFile=false after 'p', got project=%v multi=%v", op.IsProjectMode(), op.IsMultiFile())
	}
	if lspCalls != 1 {
		t.Fatalf("expected LSP enrichment requested in [File] mode, got %d calls", lspCalls)
	}
	fileRendered := paneScreenText(ov.X, ov.Y, ov.Width, ov.Height)
	if !strings.Contains(fileRendered, "Outline [File] (2)") || !strings.Contains(fileRendered, "p:proj") {
		t.Fatalf("expected [File] header and p:proj footer, got:\n%s", fileRendered)
	}

	// Toggle 'p' back to [Project] mode.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'p', tcell.ModNone, ""))
	if !op.IsProjectMode() || !op.IsMultiFile() {
		t.Fatal("expected restored [Project] mode")
	}

	// Cross-file jump using SetFileOpener passes destBP to SetJumpHandler.
	var openedFile string
	var openedLine int
	var jumpedPane *action.BufPane
	var jumpedTitle string
	mainBuf := buffer.NewBufferFromString(mainContent, mainPath, buffer.BTDefault)
	mainBP := action.NewBufPaneFromBuf(mainBuf, tab)
	SetFileOpener(func(bp *action.BufPane, filePath string, line, col int) *action.BufPane {
		openedFile = filePath
		openedLine = line
		mainBuf.GetActiveCursor().GotoLoc(buffer.Loc{X: col, Y: line})
		return mainBP
	})
	SetJumpHandler(func(bp *action.BufPane, item Item, allItems []Item) {
		jumpedPane = bp
		jumpedTitle = item.Title
	})

	// Select "Preface" (from main.typ, while ch1.typ is active) and press Enter.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'g', tcell.ModNone, ""))
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	if filepath.Clean(openedFile) != filepath.Clean(mainPath) || openedLine != 0 {
		t.Fatalf("expected cross-file opener for %s line 0, got %s line %d", mainPath, openedFile, openedLine)
	}
	if jumpedPane != mainBP || jumpedTitle != "Preface" {
		t.Fatalf("expected JumpHandler called with destBP and Preface, got pane=%v title=%q", jumpedPane, jumpedTitle)
	}
}

func TestOutlineCrossFileFallbackOpenModesAndIncludedRefresh(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.typ")
	ch1Path := filepath.Join(dir, "ch1.typ")

	writeTestFile(t, mainPath, "= Main Top\n#include \"ch1.typ\"\n")
	writeTestFile(t, ch1Path, "Intro line\n= Chapter Target\nBody\n")

	for _, mode := range []string{"tab", "vsplit", "current"} {
		t.Run("gotoOpenMode_"+mode, func(t *testing.T) {
			panes := setupTabs(t, 100, 24, struct{ name, text string }{
				name: mainPath,
				text: "= Main Top\n#include \"ch1.typ\"\n",
			})
			mainBP := panes[0]
			tab0 := action.MainTab()
			config.GlobalSettings["typst.gotoOpenMode"] = mode
			t.Cleanup(func() {
				config.GlobalSettings["typst.gotoOpenMode"] = "tab"
			})

			var jumpedBP *action.BufPane
			SetJumpHandler(func(bp *action.BufPane, item Item, allItems []Item) {
				jumpedBP = bp
			})

			op, ok := Open(mainBP, true)
			if !ok || op == nil {
				t.Fatal("failed to open outline")
			}

			// Move from Main Top -> Chapter Target (in ch1.typ) and press Enter using built-in fallback opener.
			action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
			action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))

			if jumpedBP == nil || jumpedBP.Buf == nil {
				t.Fatal("expected JumpHandler to receive opened destBP")
			}
			if canonicalOutlinePath(bufferAbsPath(jumpedBP.Buf)) != canonicalOutlinePath(ch1Path) {
				t.Fatalf("expected destBP for %s, got %s", ch1Path, jumpedBP.Buf.AbsPath)
			}
			if jumpedBP.Cursor.Y != 1 {
				t.Fatalf("expected cursor at line 1 in ch1.typ, got %d", jumpedBP.Cursor.Y)
			}

			switch mode {
			case "tab":
				if len(action.Tabs.List) != 2 || action.MainTab() == tab0 {
					t.Fatalf("expected new tab active in tab mode, tabs=%d", len(action.Tabs.List))
				}
			case "vsplit":
				if jumpedBP.Tab() != tab0 || len(tab0.Panes) != 3 {
					t.Fatalf("expected vsplit in tab0 (3 panes total), got panes=%d", len(tab0.Panes))
				}
			case "current":
				if jumpedBP != mainBP || mainBP.Tab() != tab0 {
					t.Fatalf("expected current pane reused in current mode")
				}
			}
		})
	}

	// Test live refresh when an included file open in another tab changes.
	t.Run("included_buffer_change_refreshes_outline", func(t *testing.T) {
		panes := setupTabs(t, 90, 24,
			struct{ name, text string }{mainPath, "= Main Top\n#include \"ch1.typ\"\n"},
			struct{ name, text string }{ch1Path, "= Initial Chapter\n"},
		)
		mainBP := panes[0]
		ch1BP := panes[1]

		op, ok := Open(mainBP, false)
		if !ok || op == nil {
			t.Fatal("failed to open outline")
		}

		ch1BP.Buf.Insert(ch1BP.Buf.End(), "== Added Subheading\n")
		buffer.FlushObservers()

		deadline := time.Now().Add(1 * time.Second)
		found := false
		for time.Now().Before(deadline) {
			action.DrainUIQueue()
			for _, it := range op.Items() {
				if it.Title == "Added Subheading" {
					found = true
					break
				}
			}
			if found {
				break
			}
			select {
			case <-action.UIQueueReady():
			case <-time.After(10 * time.Millisecond):
			}
		}
		if !found {
			t.Fatalf("expected included file edit to refresh outline, got %#v", op.Items())
		}
	})
}
