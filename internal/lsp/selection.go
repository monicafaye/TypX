package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/util"
)

const maxSelectionStackDepth = 64

type selectionSnapshot struct {
	CursorLoc    buffer.Loc
	SelStart     buffer.Loc
	SelEnd       buffer.Loc
	HasSelection bool
}

type selectionStack struct {
	shared    *buffer.SharedBuffer
	version   uint64
	originLoc buffer.Loc
	expected  selectionSnapshot
	frames    []selectionSnapshot
}

func normalizedSelection(c *buffer.Cursor) (buffer.Loc, buffer.Loc, bool) {
	if c == nil {
		return buffer.Loc{}, buffer.Loc{}, false
	}
	a, b := c.CurSelection[0], c.CurSelection[1]
	if b.LessThan(a) {
		a, b = b, a
	}
	return a, b, a != b
}

func isStackValid(bp *action.BufPane, st *selectionStack) bool {
	if bp == nil || bp.Buf == nil || bp.Cursor == nil || st == nil {
		return false
	}
	if st.shared != bp.Buf.SharedBuffer || st.version != bp.Buf.Version() {
		return false
	}
	start, end, hasSel := normalizedSelection(bp.Cursor)
	if bp.Cursor.Loc != st.expected.CursorLoc || hasSel != st.expected.HasSelection {
		return false
	}
	if hasSel && (start != st.expected.SelStart || end != st.expected.SelEnd) {
		return false
	}
	return true
}

func (m *Manager) pruneSelectionStacksLocked() {
	if len(m.selectionStacks) == 0 {
		return
	}
	if action.Tabs == nil || len(action.Tabs.List) == 0 {
		return
	}
	activePanes := make(map[*action.BufPane]bool)
	for _, t := range action.Tabs.List {
		if t == nil {
			continue
		}
		for _, p := range t.Panes {
			if bp, ok := p.(*action.BufPane); ok && bp != nil {
				activePanes[bp] = true
			}
		}
	}
	for bp := range m.selectionStacks {
		if !activePanes[bp] {
			delete(m.selectionStacks, bp)
		}
	}
}

func (m *Manager) invalidateSelectionForShared(shared *buffer.SharedBuffer) {
	if shared == nil {
		return
	}
	m.mu.Lock()
	for bp, st := range m.selectionStacks {
		if st == nil || st.shared == shared {
			delete(m.selectionStacks, bp)
		}
	}
	m.mu.Unlock()
}

func applySelectionSnapshot(bp *action.BufPane, snap selectionSnapshot) {
	if bp == nil || bp.Buf == nil || bp.Cursor == nil {
		return
	}
	if snap.HasSelection {
		bp.Cursor.SetSelectionStart(snap.SelStart)
		bp.Cursor.SetSelectionEnd(snap.SelEnd)
		bp.Cursor.OrigSelection = bp.Cursor.CurSelection
	} else {
		bp.Cursor.ResetSelection()
		bp.Cursor.OrigSelection = [2]buffer.Loc{}
	}
	bp.GotoLoc(snap.CursorLoc)
}

func (m *Manager) triggerSelectExpand(bp *action.BufPane, explicitCmd bool) bool {
	if bp == nil || bp.Buf == nil || bp.Cursor == nil || !bp.Buf.IsTypst() {
		if explicitCmd {
			setInfoMessage("Selection expand is only available for .typ buffers")
		}
		return false
	}
	if bp.Buf.Path == "" || bp.Buf.AbsPath == "" {
		setInfoMessage("Save buffer before expanding selection")
		return true
	}

	m.mu.Lock()
	state := m.State()
	c := m.client
	gen := m.gen
	sessionCtx := m.sessionCtx
	m.mu.Unlock()

	if state != StateReady || c == nil {
		setInfoMessage(m.notReadyMessage(state, "LSP is not ready"))
		return true
	}

	targetBuf := bp.Buf
	m.flushDocChangeNow(targetBuf)

	reqVer := targetBuf.Version()
	shared := targetBuf.SharedBuffer
	curStart, curEnd, hasSel := normalizedSelection(bp.Cursor)
	curLoc := bp.Cursor.Loc

	m.mu.Lock()
	m.pruneSelectionStacksLocked()
	st := m.selectionStacks[bp]
	if st != nil && !isStackValid(bp, st) {
		delete(m.selectionStacks, bp)
		st = nil
	}
	queryLoc := curLoc
	if st != nil {
		queryLoc = st.originLoc
	} else if hasSel {
		queryLoc = curStart
	}
	m.mu.Unlock()

	if n := targetBuf.LinesNum(); n <= 0 {
		queryLoc = buffer.Loc{X: 0, Y: 0}
	} else {
		y := util.Clamp(queryLoc.Y, 0, n-1)
		lineBytes := targetBuf.LineBytes(y)
		x := util.Clamp(queryLoc.X, 0, util.CharacterCount(lineBytes))
		queryLoc = buffer.Loc{X: x, Y: y}
	}

	rawLine := targetBuf.LineBytes(queryLoc.Y)
	utf16Col := RuneColToUTF16(rawLine, locXToRuneCol(rawLine, queryLoc.X))
	uri := PathToURI(targetBuf.AbsPath)

	go func() {
		if sessionCtx == nil {
			sessionCtx = context.Background()
		}
		ctx, cancel := context.WithTimeout(sessionCtx, 2*time.Second)
		var raw json.RawMessage
		err := c.Call(ctx, "textDocument/selectionRange", SelectionRangeParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Positions:    []Position{{Line: queryLoc.Y, Character: utf16Col}},
		}, &raw)
		canceled := ctx.Err() == context.Canceled
		cancel()

		if err != nil {
			if canceled {
				return
			}
			var rpcErr *RPCError
			if errors.As(err, &rpcErr) && rpcErr.Code == -32601 {
				notifyInfoMessage("Selection range not supported by LSP server")
				return
			}
			notifyInfoError(fmt.Sprintf("Selection range failed: %v", err))
			return
		}

		results, parseErr := ParseSelectionRanges(raw)
		if parseErr != nil {
			notifyInfoError(fmt.Sprintf("Selection range parse failed: %v", parseErr))
			return
		}

		action.EnqueueUI(func() {
			m.mu.Lock()
			stale := m.shuttingDown || m.gen != gen
			m.mu.Unlock()
			if stale {
				return
			}
			if !isBufferStillOpenAtVersion(targetBuf, reqVer) || bp.Buf != targetBuf || targetBuf.SharedBuffer != shared || bp.Cursor == nil {
				return
			}

			curStart2, curEnd2, hasSel2 := normalizedSelection(bp.Cursor)
			if curStart2 != curStart || curEnd2 != curEnd || hasSel2 != hasSel || bp.Cursor.Loc != curLoc {
				return
			}

			newStart, newEnd, ok := findNextSelectionRange(targetBuf, results, curStart, curEnd, curLoc, hasSel)
			if !ok {
				setInfoMessage("No larger selection range")
				return
			}

			nextSnap := selectionSnapshot{
				CursorLoc:    newEnd,
				SelStart:     newStart,
				SelEnd:       newEnd,
				HasSelection: true,
			}
			prevSnap := selectionSnapshot{
				CursorLoc:    curLoc,
				SelStart:     curStart,
				SelEnd:       curEnd,
				HasSelection: hasSel,
			}

			applySelectionSnapshot(bp, nextSnap)

			m.mu.Lock()
			if m.selectionStacks == nil {
				m.selectionStacks = make(map[*action.BufPane]*selectionStack)
			}
			st := m.selectionStacks[bp]
			if st == nil || st.shared != shared || st.version != reqVer {
				st = &selectionStack{
					shared:    shared,
					version:   reqVer,
					originLoc: queryLoc,
				}
				m.selectionStacks[bp] = st
			}
			if len(st.frames) >= maxSelectionStackDepth {
				st.frames = append([]selectionSnapshot(nil), st.frames[len(st.frames)-maxSelectionStackDepth+1:]...)
			}
			st.frames = append(st.frames, prevSnap)
			st.version = targetBuf.Version()
			st.expected = nextSnap
			m.mu.Unlock()
		})
	}()
	return true
}

func (m *Manager) triggerSelectShrink(bp *action.BufPane, explicitCmd bool) bool {
	if bp == nil || bp.Buf == nil || bp.Cursor == nil || !bp.Buf.IsTypst() {
		if explicitCmd {
			setInfoMessage("Selection shrink is only available for .typ buffers")
		}
		return false
	}

	m.mu.Lock()
	m.pruneSelectionStacksLocked()
	st := m.selectionStacks[bp]
	if !isStackValid(bp, st) || len(st.frames) == 0 {
		delete(m.selectionStacks, bp)
		m.mu.Unlock()
		setInfoMessage("No selection history")
		return false
	}
	prev := st.frames[len(st.frames)-1]
	st.frames = st.frames[:len(st.frames)-1]
	if len(st.frames) == 0 {
		delete(m.selectionStacks, bp)
	} else {
		st.expected = prev
		st.version = bp.Buf.Version()
	}
	m.mu.Unlock()

	applySelectionSnapshot(bp, prev)
	return true
}

func findNextSelectionRange(b *buffer.Buffer, results []SelectionRange, curStart, curEnd, curLoc buffer.Loc, hasSel bool) (buffer.Loc, buffer.Loc, bool) {
	if len(results) == 0 || b == nil || b.LinesNum() <= 0 {
		return buffer.Loc{}, buffer.Loc{}, false
	}

	var firstValidStart, firstValidEnd buffer.Loc
	hasFirstValid := false

	for node := &results[0]; node != nil; node = node.Parent {
		candStart := lspPositionToLoc(b, node.Range.Start)
		candEnd := lspPositionToLoc(b, node.Range.End)
		if candEnd.LessThan(candStart) {
			candStart, candEnd = candEnd, candStart
		}
		if candStart == candEnd {
			continue
		}
		if !hasSel {
			if !hasFirstValid {
				firstValidStart, firstValidEnd = candStart, candEnd
				hasFirstValid = true
			}
			if !curLoc.LessThan(candStart) && !candEnd.LessThan(curLoc) {
				return candStart, candEnd, true
			}
			continue
		}
		if !curStart.LessThan(candStart) && !candEnd.LessThan(curEnd) &&
			(candStart.LessThan(curStart) || curEnd.LessThan(candEnd)) {
			return candStart, candEnd, true
		}
	}

	if !hasSel && hasFirstValid {
		return firstValidStart, firstValidEnd, true
	}
	return buffer.Loc{}, buffer.Loc{}, false
}
