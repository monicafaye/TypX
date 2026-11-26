package lsp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
)

const (
	autoSignatureDelay  = 120 * time.Millisecond
	autoCompleteDelay   = 150 * time.Millisecond
)

// LspSignatureHelpAction returns the BufKeyAction for Alt+H LSP signature help.
func LspSignatureHelpAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		return defaultManager.triggerSignatureHelp(bp, "", true)
	}
}

// SignatureHelpCommand returns the :signature-help command handler.
func SignatureHelpCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
			notifyInfoMessage("Signature help is only available for .typ buffers")
			return
		}
		defaultManager.triggerSignatureHelp(bp, "", true)
	}
}

func (m *Manager) triggerSignatureHelp(bp *action.BufPane, triggerChar string, explicit bool) bool {
	if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
		return false
	}
	if bp.Cursor == nil {
		return false
	}

	m.mu.Lock()
	state := m.State()
	c := m.client
	gen := m.gen
	m.mu.Unlock()

	if state != StateReady || c == nil {
		if explicit {
			notifyInfoMessage(m.notReadyMessage(state, "LSP: offline"))
		}
		return explicit
	}

	targetBuf := bp.Buf
	m.flushDocChangeNow(targetBuf)

	reqVersion := targetBuf.Version()
	reqLoc := bp.Cursor.Loc
	rawLine := targetBuf.LineBytes(reqLoc.Y)
	lineCopy := append([]byte(nil), rawLine...)
	utf16Col := RuneColToUTF16(lineCopy, locXToRuneCol(lineCopy, reqLoc.X))
	uri := PathToURI(targetBuf.AbsPath)

	triggerKind := SignatureHelpTriggerInvoked
	if !explicit && triggerChar != "" {
		triggerKind = SignatureHelpTriggerCharacter
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()

		var raw json.RawMessage
		err := c.Call(ctx, "textDocument/signatureHelp", SignatureHelpParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: reqLoc.Y, Character: utf16Col},
			Context: &SignatureHelpContext{
				TriggerKind:      triggerKind,
				TriggerCharacter: triggerChar,
				IsRetrigger:      false,
			},
		}, &raw)
		if err != nil {
			if explicit {
				notifyInfoError("LSP signature help error: " + err.Error())
			}
			return
		}

		sh, perr := ParseSignatureHelp(raw)
		if perr != nil {
			if explicit {
				notifyInfoError("LSP signature help parse error: " + perr.Error())
			}
			return
		}
		formatted := FormatSignatureHelp(sh)

		action.EnqueueUI(func() {
			m.mu.Lock()
			stale := m.shuttingDown || m.gen != gen
			m.mu.Unlock()
			if stale || !isBufferOpenAndAt(targetBuf, reqVersion, reqLoc, false) {
				return
			}
			if formatted == "" {
				if explicit {
					notifyInfoMessage("No signature help")
				} else {
					m.clearAutoSignatureMessage()
				}
				return
			}
			if explicit {
				m.mu.Lock()
				m.lastSigMsg = formatted
				m.mu.Unlock()
				notifyInfoMessage(formatted)
				return
			}
			if action.InfoBar != nil && !action.InfoBar.HasPrompt && !action.InfoBar.HasError {
				m.mu.Lock()
				m.lastSigMsg = formatted
				m.mu.Unlock()
				action.InfoBar.Message(formatted)
			}
		})
	}()
	return true
}

func (m *Manager) scheduleAutoSignatureHelp(bp *action.BufPane, triggerChar string) {
	if bp == nil || bp.Buf == nil || bp.Cursor == nil || !readGlobalBool("typst.signatureHelp", true) {
		return
	}
	targetBuf := bp.Buf
	reqVersion := targetBuf.Version()
	reqLoc := bp.Cursor.Loc

	m.mu.Lock()
	if m.shuttingDown || m.State() != StateReady || m.client == nil {
		m.mu.Unlock()
		return
	}
	if m.sigHelpTimer != nil {
		m.sigHelpTimer.Stop()
	}
	gen := m.gen
	m.sigHelpTimer = time.AfterFunc(autoSignatureDelay, func() {
		action.EnqueueUI(func() {
			m.mu.Lock()
			stale := m.shuttingDown || m.gen != gen
			m.mu.Unlock()
			if stale || !isBufferOpenAndAt(targetBuf, reqVersion, reqLoc, false) {
				return
			}
			curBP := activeBufPaneForBuffer(targetBuf)
			if curBP == nil {
				return
			}
			m.triggerSignatureHelp(curBP, triggerChar, false)
		})
	})
	m.mu.Unlock()
}

func (m *Manager) clearAutoSignatureMessage() {
	m.mu.Lock()
	if m.sigHelpTimer != nil {
		m.sigHelpTimer.Stop()
		m.sigHelpTimer = nil
	}
	prev := m.lastSigMsg
	m.lastSigMsg = ""
	m.mu.Unlock()

	if prev != "" && action.InfoBar != nil && !action.InfoBar.HasPrompt && action.InfoBar.Msg == prev {
		action.InfoBar.Message("")
	}
}

func activeBufPaneForBuffer(b *buffer.Buffer) *action.BufPane {
	if b == nil || action.Tabs == nil || len(action.Tabs.List) == 0 {
		return nil
	}
	tab := action.MainTab()
	if tab == nil {
		return nil
	}
	bp := tab.CurPane()
	if bp == nil || bp.Buf == nil || bp.Buf.SharedBuffer != b.SharedBuffer {
		return nil
	}
	return bp
}
