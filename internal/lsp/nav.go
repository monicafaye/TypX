package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/screen"
	"github.com/monicafaye/TypX/internal/util"
)

const maxJumpStackDepth = 64

type jumpOrigin struct {
	AbsPath string
	Loc     buffer.Loc
}

func (m *Manager) pushJumpOrigin(tab *action.Tab, absPath string, loc buffer.Loc) {
	if tab == nil || absPath == "" {
		return
	}
	var activeTabs map[*action.Tab]bool
	if action.Tabs != nil {
		activeTabs = make(map[*action.Tab]bool, len(action.Tabs.List))
		for _, t := range action.Tabs.List {
			activeTabs[t] = true
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.jumpStacks == nil {
		m.jumpStacks = make(map[*action.Tab][]jumpOrigin)
	}
	if activeTabs != nil {
		for k := range m.jumpStacks {
			if !activeTabs[k] {
				delete(m.jumpStacks, k)
			}
		}
	}
	stack := append(m.jumpStacks[tab], jumpOrigin{
		AbsPath: absPath,
		Loc:     loc,
	})
	if len(stack) > maxJumpStackDepth {
		stack = append([]jumpOrigin(nil), stack[len(stack)-maxJumpStackDepth:]...)
	}
	m.jumpStacks[tab] = stack
}

func (m *Manager) popJumpOrigin(tab *action.Tab) (jumpOrigin, bool) {
	if tab == nil {
		return jumpOrigin{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	stack := m.jumpStacks[tab]
	if len(stack) == 0 {
		return jumpOrigin{}, false
	}
	last := stack[len(stack)-1]
	stack = stack[:len(stack)-1]
	if len(stack) == 0 {
		delete(m.jumpStacks, tab)
	} else {
		m.jumpStacks[tab] = stack
	}
	return last, true
}

func (m *Manager) triggerDefinition(bp *action.BufPane, explicitCmd bool) bool {
	if bp == nil || bp.Buf == nil || bp.Cursor == nil || !bp.Buf.IsTypst() {
		if explicitCmd {
			setInfoMessage("Go to definition is only available for .typ buffers")
		}
		return false
	}
	if bp.Buf.AbsPath == "" {
		setInfoMessage("Save buffer before go to definition")
		return true
	}

	m.mu.Lock()
	state := m.State()
	c := m.client
	gen := m.gen
	m.mu.Unlock()

	if state != StateReady || c == nil {
		setInfoMessage("LSP is not ready")
		return true
	}

	targetBuf := bp.Buf
	m.flushDocChangeNow(targetBuf)

	absPath := targetBuf.AbsPath
	uri := PathToURI(absPath)
	reqVer := targetBuf.Version()
	cursorLoc := bp.Cursor.Loc
	rawLine := targetBuf.LineBytes(cursorLoc.Y)
	utf16Col := RuneColToUTF16(rawLine, locXToRuneCol(rawLine, cursorLoc.X))
	shared := targetBuf.SharedBuffer
	originTab := bp.Tab()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var raw json.RawMessage
		err := c.Call(ctx, "textDocument/definition", TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: cursorLoc.Y, Character: utf16Col},
		}, &raw)
		canceled := ctx.Err() == context.Canceled
		cancel()

		if err != nil {
			if canceled {
				return
			}
			notifyInfoError(fmt.Sprintf("Definition failed: %v", err))
			return
		}

		locs, parseErr := ParseLocations(raw)
		if parseErr != nil {
			notifyInfoError(fmt.Sprintf("Definition parse failed: %v", parseErr))
			return
		}
		locs = dedupeLocations(locs)

		action.EnqueueUI(func() {
			m.mu.Lock()
			stale := m.shuttingDown || m.gen != gen
			m.mu.Unlock()
			if stale {
				return
			}
			if !isBufferStillOpenAtVersion(targetBuf, reqVer) || targetBuf.SharedBuffer != shared {
				return
			}

			if len(locs) == 0 {
				setInfoMessage("No definition found")
				return
			}
			if len(locs) == 1 {
				if destBP := m.jumpToLocation(bp, locs[0]); destBP != nil {
					m.pushJumpOrigin(destBP.Tab(), absPath, cursorLoc)
				}
				return
			}

			items := make([]ListItem, 0, len(locs))
			for _, loc := range locs {
				targetLoc := loc
				targetPath := URIToPath(targetLoc.URI)
				if targetPath == "" {
					continue
				}
				preview := readLocationLinePreview(targetPath, targetLoc.Range.Start.Line)
				items = append(items, ListItem{
					FilePath:  targetPath,
					Line:      targetLoc.Range.Start.Line,
					Col:       targetLoc.Range.Start.Character,
					Badge:     "DEF",
					Primary:   preview,
					Secondary: fmt.Sprintf("%s:%d:%d", filepath.Base(targetPath), targetLoc.Range.Start.Line+1, targetLoc.Range.Start.Character+1),
					OnSelect: func() {
						CloseListPaneForTab(originTab)
						if destBP := m.jumpToLocation(bp, targetLoc); destBP != nil {
							m.pushJumpOrigin(destBP.Tab(), absPath, cursorLoc)
						}
					},
				})
			}
			if len(items) == 0 {
				setInfoMessage("No definition found")
				return
			}
			OpenListPane(bp, "Definitions", false, items)
		})
	}()
	return true
}

func (m *Manager) triggerReferences(bp *action.BufPane, explicitCmd bool) bool {
	if bp == nil || bp.Buf == nil || bp.Cursor == nil || !bp.Buf.IsTypst() {
		if explicitCmd {
			setInfoMessage("References are only available for .typ buffers")
		}
		return false
	}
	if bp.Buf.AbsPath == "" {
		setInfoMessage("Save buffer before finding references")
		return true
	}

	m.mu.Lock()
	state := m.State()
	c := m.client
	gen := m.gen
	m.mu.Unlock()

	if state != StateReady || c == nil {
		setInfoMessage("LSP is not ready")
		return true
	}

	targetBuf := bp.Buf
	m.flushDocChangeNow(targetBuf)

	absPath := targetBuf.AbsPath
	activeKey := canonicalDocKey(absPath)
	uri := PathToURI(absPath)
	reqVer := targetBuf.Version()
	cursorLoc := bp.Cursor.Loc
	rawLine := targetBuf.LineBytes(cursorLoc.Y)
	utf16Col := RuneColToUTF16(rawLine, locXToRuneCol(rawLine, cursorLoc.X))
	shared := targetBuf.SharedBuffer

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var raw json.RawMessage
		err := c.Call(ctx, "textDocument/references", ReferenceParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: cursorLoc.Y, Character: utf16Col},
			Context:      ReferenceContext{IncludeDeclaration: true},
		}, &raw)
		canceled := ctx.Err() == context.Canceled
		cancel()

		if err != nil {
			if canceled {
				return
			}
			notifyInfoError(fmt.Sprintf("References failed: %v", err))
			return
		}

		locs, parseErr := ParseLocations(raw)
		if parseErr != nil {
			notifyInfoError(fmt.Sprintf("References parse failed: %v", parseErr))
			return
		}
		locs = dedupeLocations(locs)

		action.EnqueueUI(func() {
			m.mu.Lock()
			stale := m.shuttingDown || m.gen != gen
			m.mu.Unlock()
			if stale {
				return
			}
			if !isBufferStillOpenAtVersion(targetBuf, reqVer) || targetBuf.SharedBuffer != shared {
				return
			}

			if len(locs) == 0 {
				setInfoMessage("No references found")
				return
			}

			sort.SliceStable(locs, func(i, j int) bool {
				pi := URIToPath(locs[i].URI)
				pj := URIToPath(locs[j].URI)
				ki := canonicalDocKey(pi) == activeKey
				kj := canonicalDocKey(pj) == activeKey
				if ki != kj {
					return ki
				}
				if pi != pj {
					return pi < pj
				}
				if locs[i].Range.Start.Line != locs[j].Range.Start.Line {
					return locs[i].Range.Start.Line < locs[j].Range.Start.Line
				}
				return locs[i].Range.Start.Character < locs[j].Range.Start.Character
			})

			items := make([]ListItem, 0, len(locs))
			for _, loc := range locs {
				targetLoc := loc
				targetPath := URIToPath(targetLoc.URI)
				if targetPath == "" {
					continue
				}
				preview := readLocationLinePreview(targetPath, targetLoc.Range.Start.Line)
				items = append(items, ListItem{
					FilePath:  targetPath,
					Line:      targetLoc.Range.Start.Line,
					Col:       targetLoc.Range.Start.Character,
					Badge:     "REF",
					Primary:   preview,
					Secondary: fmt.Sprintf("%s:%d:%d", filepath.Base(targetPath), targetLoc.Range.Start.Line+1, targetLoc.Range.Start.Character+1),
					OnSelect: func() {
						if destBP := m.jumpToLocation(bp, targetLoc); destBP != nil {
							m.pushJumpOrigin(destBP.Tab(), absPath, cursorLoc)
						}
					},
				})
			}
			if len(items) == 0 {
				setInfoMessage("No references found")
				return
			}
			OpenListPane(bp, "References", false, items)
		})
	}()
	return true
}

func (m *Manager) jumpBack(bp *action.BufPane, explicitCmd bool) bool {
	if bp == nil || bp.Tab() == nil {
		return false
	}
	origin, ok := m.popJumpOrigin(bp.Tab())
	if !ok {
		setInfoMessage("Jump stack is empty")
		return false
	}
	return m.jumpToFileLoc(bp, origin.AbsPath, origin.Loc) != nil
}

func (m *Manager) jumpToLocation(bp *action.BufPane, loc Location) *action.BufPane {
	targetPath := URIToPath(loc.URI)
	if targetPath == "" {
		setInfoError("Invalid target location")
		return nil
	}
	return m.jumpToFilePosition(bp, targetPath, loc.Range.Start)
}

func (m *Manager) jumpToFilePosition(bp *action.BufPane, targetPath string, pos Position) *action.BufPane {
	pane := m.openOrFocusTargetPane(bp, targetPath)
	if pane == nil || pane.Buf == nil {
		return nil
	}
	loc := positionToBufferLoc(pane.Buf, pos)
	applyCursorJump(pane, loc)
	return pane
}

func (m *Manager) jumpToFileLoc(bp *action.BufPane, targetPath string, loc buffer.Loc) *action.BufPane {
	pane := m.openOrFocusTargetPane(bp, targetPath)
	if pane == nil || pane.Buf == nil {
		return nil
	}
	applyCursorJump(pane, loc)
	return pane
}

func (m *Manager) openOrFocusTargetPane(bp *action.BufPane, targetPath string) *action.BufPane {
	targetKey := canonicalDocKey(targetPath)
	if targetKey == "" {
		return nil
	}

	// 1. Same buffer in provided pane.
	if bp != nil && bp.Buf != nil && canonicalDocKey(bp.Buf.AbsPath) == targetKey {
		focusTabAndPane(bp.Tab(), bp)
		return bp
	}

	// 2. Already open in any tab or split pane.
	if action.Tabs != nil {
		for ti, t := range action.Tabs.List {
			if t == nil {
				continue
			}
			for pi, p := range t.Panes {
				pane, ok := p.(*action.BufPane)
				if !ok || pane == nil || pane.Buf == nil || pane.Buf.Type.Scratch {
					continue
				}
				if canonicalDocKey(pane.Buf.AbsPath) == targetKey {
					action.Tabs.SetActive(ti)
					t.SetActive(pi)
					return pane
				}
			}
		}
	}

	// 3. Unopened file on disk.
	info, err := os.Stat(targetPath)
	if err != nil || info.IsDir() {
		setInfoError(fmt.Sprintf("Target file not found: %s", targetPath))
		return nil
	}

	b, err := buffer.NewBufferFromFileWithCommand(targetPath, buffer.BTDefault, buffer.Command{})
	if err != nil {
		setInfoError(fmt.Sprintf("Failed to open %s: %v", filepath.Base(targetPath), err))
		return nil
	}
	if isTypstPackageCachePath(targetPath) {
		_ = b.SetOptionNative("readonly", true)
		b.Type.Readonly = true
	}

	mode := strings.ToLower(strings.TrimSpace(readGlobalString("typst.gotoOpenMode", "tab")))
	if mode == "current" && bp != nil && bp.Buf != nil && bp.Buf.Modified() && !bp.Buf.Shared() {
		mode = "tab"
	}

	switch mode {
	case "vsplit":
		if bp != nil && bp.Tab() != nil {
			newBP := bp.VSplitIndex(b, true)
			if newBP != nil {
				return newBP
			}
		}
	case "current":
		if bp != nil {
			bp.OpenBuffer(b)
			focusTabAndPane(bp.Tab(), bp)
			return bp
		}
	}

	return openBufferInNewTabPane(b)
}

func openBufferInNewTabPane(b *buffer.Buffer) *action.BufPane {
	if b == nil || action.Tabs == nil {
		return nil
	}
	w, h := 80, 24
	if screen.Screen != nil {
		w, h = screen.Screen.Size()
	}
	iOffset := config.GetInfoBarOffset()
	tp := action.NewTabFromBuffer(0, 0, w, h-1-iOffset, b)
	action.Tabs.AddTab(tp)
	action.Tabs.SetActive(len(action.Tabs.List) - 1)
	action.Tabs.Resize()
	return tp.CurPane()
}

func focusTabAndPane(tab *action.Tab, pane *action.BufPane) {
	if tab == nil || pane == nil {
		return
	}
	if action.Tabs != nil {
		for ti, t := range action.Tabs.List {
			if t == tab {
				action.Tabs.SetActive(ti)
				break
			}
		}
	}
	for pi, p := range tab.Panes {
		if p == pane {
			tab.SetActive(pi)
			break
		}
	}
}

func positionToBufferLoc(b *buffer.Buffer, pos Position) buffer.Loc {
	if b == nil || b.LinesNum() == 0 {
		return buffer.Loc{X: 0, Y: 0}
	}
	y := util.Clamp(pos.Line, 0, b.LinesNum()-1)
	lineBytes := b.LineBytes(y)
	x := runeColToLocX(lineBytes, UTF16ToRuneCol(lineBytes, pos.Character))
	x = util.Clamp(x, 0, util.CharacterCount(lineBytes))
	return buffer.Loc{X: x, Y: y}
}

func applyCursorJump(pane *action.BufPane, loc buffer.Loc) {
	if pane == nil || pane.Buf == nil {
		return
	}
	numLines := pane.Buf.LinesNum()
	if numLines <= 0 {
		loc = buffer.Loc{X: 0, Y: 0}
	} else {
		y := util.Clamp(loc.Y, 0, numLines-1)
		x := util.Clamp(loc.X, 0, util.CharacterCount(pane.Buf.LineBytes(y)))
		loc = buffer.Loc{X: x, Y: y}
	}
	if pane.Cursor != nil {
		pane.Cursor.ResetSelection()
		pane.Cursor.GotoLoc(loc)
	}
	pane.Relocate()
	pane.Center()
}

func isTypstPackageCachePath(targetPath string) bool {
	slashed := strings.ToLower(filepath.ToSlash(filepath.Clean(targetPath)))
	return strings.Contains(slashed, "/typst/packages/preview/")
}

func dedupeLocations(locs []Location) []Location {
	if len(locs) <= 1 {
		return locs
	}
	type locKey struct {
		path string
		line int
		col  int
	}
	seen := make(map[locKey]bool, len(locs))
	out := make([]Location, 0, len(locs))
	for _, loc := range locs {
		k := locKey{
			path: canonicalDocKey(URIToPath(loc.URI)),
			line: loc.Range.Start.Line,
			col:  loc.Range.Start.Character,
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, loc)
	}
	return out
}

func readLocationLinePreview(targetPath string, lineIdx int) string {
	if lineIdx < 0 {
		return ""
	}
	key := canonicalDocKey(targetPath)
	for _, b := range buffer.OpenBuffers {
		if b != nil && canonicalDocKey(b.AbsPath) == key {
			if lineIdx < b.LinesNum() {
				return strings.TrimSpace(string(b.LineBytes(lineIdx)))
			}
			return ""
		}
	}
	f, err := os.Open(targetPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for cur := 0; sc.Scan(); cur++ {
		if cur == lineIdx {
			return strings.TrimSpace(sc.Text())
		}
		if cur > 2000 {
			break
		}
	}
	return ""
}

func setInfoMessage(msg string) {
	if action.InfoBar != nil {
		action.InfoBar.Message(msg)
	}
}

func setInfoError(msg string) {
	if action.InfoBar != nil {
		action.InfoBar.Error(msg)
	}
}
