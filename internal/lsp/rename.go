package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
)

func (m *Manager) triggerRename(bp *action.BufPane, newNameArg string, explicitCmd bool) bool {
	if bp == nil || bp.Buf == nil || bp.Cursor == nil || !bp.Buf.IsTypst() {
		if explicitCmd {
			setInfoMessage("Rename is only available for .typ buffers")
		}
		return false
	}
	if bp.Buf.Type.Readonly {
		setInfoMessage("Buffer is read-only")
		return true
	}
	if bp.Buf.AbsPath == "" {
		setInfoMessage("Save buffer before renaming")
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

	uri := PathToURI(targetBuf.AbsPath)
	reqVer := targetBuf.Version()
	cursorLoc := bp.Cursor.Loc
	rawLine := targetBuf.LineBytes(cursorLoc.Y)
	utf16Pos := Position{
		Line:      cursorLoc.Y,
		Character: RuneColToUTF16(rawLine, locXToRuneCol(rawLine, cursorLoc.X)),
	}
	shared := targetBuf.SharedBuffer
	explicitNewName := strings.TrimSpace(newNameArg)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var raw json.RawMessage
		err := c.Call(ctx, "textDocument/prepareRename", TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     utf16Pos,
		}, &raw)
		canceled := ctx.Err() == context.Canceled
		cancel()

		var placeholder string
		var rng *Range
		canRename := true

		if err != nil {
			if canceled {
				return
			}
			var rpcErr *RPCError
			if errors.As(err, &rpcErr) && rpcErr.Code == -32601 {
				// Server does not support prepareRename; fall back to word at cursor.
				canRename = true
			} else {
				notifyInfoError(fmt.Sprintf("Rename failed: %v", err))
				return
			}
		} else {
			ph, r, ok, parseErr := ParsePrepareRename(raw)
			if parseErr != nil {
				notifyInfoError(fmt.Sprintf("Prepare rename parse error: %v", parseErr))
				return
			}
			placeholder, rng, canRename = ph, r, ok
		}

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
			if !canRename {
				setInfoMessage("Symbol cannot be renamed")
				return
			}

			if placeholder == "" && rng != nil {
				startLoc := positionToBufferLoc(targetBuf, rng.Start)
				endLoc := positionToBufferLoc(targetBuf, rng.End)
				if startLoc.Y == endLoc.Y && startLoc.LessThan(endLoc) {
					placeholder = string(targetBuf.Substr(startLoc, endLoc))
				}
			}
			if placeholder == "" {
				placeholder = wordAtCursor(targetBuf, cursorLoc)
			}

			if explicitNewName != "" {
				m.executeRename(targetBuf, gen, reqVer, uri, utf16Pos, explicitNewName)
				return
			}

			if action.InfoBar == nil {
				return
			}
			action.InfoBar.Prompt("Rename to: ", placeholder, "Rename", nil, func(resp string, canceled bool) {
				if canceled {
					return
				}
				next := strings.TrimSpace(resp)
				if next == "" || next == placeholder {
					setInfoMessage("Rename canceled")
					return
				}
				m.executeRename(targetBuf, gen, reqVer, uri, utf16Pos, next)
			})
		})
	}()
	return true
}

func (m *Manager) executeRename(targetBuf *buffer.Buffer, gen uint64, reqVer uint64, uri string, pos Position, newName string) {
	if !isBufferStillOpenAtVersion(targetBuf, reqVer) {
		setInfoMessage("Buffer modified; rename canceled")
		return
	}

	m.mu.Lock()
	state := m.State()
	c := m.client
	curGen := m.gen
	shutting := m.shuttingDown
	m.mu.Unlock()
	if shutting || curGen != gen || state != StateReady || c == nil {
		setInfoMessage("LSP is not ready")
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var we WorkspaceEdit
		err := c.Call(ctx, "textDocument/rename", RenameParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     pos,
			NewName:      newName,
		}, &we)
		canceled := ctx.Err() == context.Canceled
		cancel()

		if err != nil {
			if canceled {
				return
			}
			notifyInfoError(fmt.Sprintf("Rename failed: %v", err))
			return
		}

		action.EnqueueUI(func() {
			m.mu.Lock()
			stale := m.shuttingDown || m.gen != gen
			m.mu.Unlock()
			if stale {
				return
			}
			if !isBufferStillOpenAtVersion(targetBuf, reqVer) {
				setInfoMessage("Buffer modified during rename")
				return
			}
			files, applied, applyErr := ApplyWorkspaceEdit(we)
			if applyErr != nil {
				setInfoError("Rename failed: " + applyErr.Error())
				return
			}
			if applied == 0 {
				setInfoMessage("No rename edits returned")
				return
			}
			setInfoMessage(fmt.Sprintf("Renamed to %q (%d edit(s) in %d file(s))", newName, applied, files))
		})
	}()
}

func wordAtCursor(b *buffer.Buffer, loc buffer.Loc) string {
	if b == nil || loc.Y < 0 || loc.Y >= b.LinesNum() {
		return ""
	}
	line := string(b.LineBytes(loc.Y))
	if line == "" {
		return ""
	}
	runes := []rune(line)
	idx := locXToRuneCol([]byte(line), loc.X)
	if idx >= len(runes) {
		idx = len(runes) - 1
	}
	if idx < 0 {
		return ""
	}
	isIdent := func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'
	}
	if !isIdent(runes[idx]) {
		if idx > 0 && isIdent(runes[idx-1]) {
			idx--
		} else {
			return ""
		}
	}
	start := idx
	for start > 0 && isIdent(runes[start-1]) {
		start--
	}
	end := idx + 1
	for end < len(runes) && isIdent(runes[end]) {
		end++
	}
	return string(runes[start:end])
}
