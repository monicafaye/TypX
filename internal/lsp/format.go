package lsp

import (
	"context"
	"time"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
)

func bufferFormattingOptions(b *buffer.Buffer) FormattingOptions {
	tabSize := 2
	insertSpaces := true
	if b != nil {
		switch v := b.Settings["tabsize"].(type) {
		case float64:
			if v > 0 {
				tabSize = int(v)
			}
		case int:
			if v > 0 {
				tabSize = v
			}
		}
		if v, ok := b.Settings["tabstospaces"].(bool); ok {
			insertSpaces = v
		}
	}
	return FormattingOptions{
		TabSize:      tabSize,
		InsertSpaces: insertSpaces,
	}
}

func (m *Manager) triggerFormat(bp *action.BufPane, explicitCmd bool) bool {
	if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
		if explicitCmd {
			notifyInfoMessage("Formatting is only available for .typ buffers")
		}
		return false
	}
	if bp.Buf.Type.Readonly {
		notifyInfoMessage("Buffer is read-only")
		return true
	}

	m.mu.Lock()
	state := m.State()
	c := m.client
	gen := m.gen
	m.mu.Unlock()

	if state != StateReady || c == nil {
		notifyInfoMessage(m.notReadyMessage(state, "LSP: offline (cannot format)"))
		return true
	}

	targetBuf := bp.Buf
	m.flushDocChangeNow(targetBuf)
	reqVer := targetBuf.Version()
	uri := PathToURI(targetBuf.AbsPath)
	opts := bufferFormattingOptions(targetBuf)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var edits []TextEdit
		err := c.Call(ctx, "textDocument/formatting", DocumentFormattingParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Options:      opts,
		}, &edits)

		action.EnqueueUI(func() {
			m.mu.Lock()
			stale := m.shuttingDown || m.gen != gen
			m.mu.Unlock()
			if stale || !isBufferStillOpenAtVersion(targetBuf, reqVer) {
				if err == nil {
					notifyInfoMessage("Buffer modified during format")
				}
				return
			}
			if err != nil {
				if action.InfoBar != nil {
					action.InfoBar.Error("LSP format error: " + err.Error())
				}
				return
			}
			n, applyErr := ApplyTextEdits(targetBuf, edits)
			if applyErr != nil {
				if action.InfoBar != nil {
					action.InfoBar.Error("Format failed: " + applyErr.Error())
				}
				return
			}
			if action.InfoBar != nil {
				if n == 0 {
					action.InfoBar.Message("Already formatted")
				} else {
					action.InfoBar.Message("Formatted")
				}
			}
		})
	}()
	return true
}

func (m *Manager) handlePreSaveFormat(bp *action.BufPane) bool {
	if bp == nil || bp.Buf == nil || bp.Buf.SharedBuffer == nil {
		return false
	}
	if m.formattingOnSave != nil && m.formattingOnSave[bp.Buf.SharedBuffer] {
		return false
	}
	if !bp.Buf.IsTypst() || !readGlobalBool("typst.formatOnSave", false) {
		return false
	}
	if bp.Buf.Path == "" || bp.Buf.Type.Readonly {
		return false
	}

	m.mu.Lock()
	state := m.State()
	c := m.client
	gen := m.gen
	m.mu.Unlock()
	if state != StateReady || c == nil {
		return false
	}

	targetPane := bp
	targetBuf := bp.Buf
	shared := targetBuf.SharedBuffer
	m.flushDocChangeNow(targetBuf)
	reqVer := targetBuf.Version()
	uri := PathToURI(targetBuf.AbsPath)
	opts := bufferFormattingOptions(targetBuf)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		var edits []TextEdit
		err := c.Call(ctx, "textDocument/formatting", DocumentFormattingParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Options:      opts,
		}, &edits)

		action.EnqueueUI(func() {
			// Verify the buffer & pane are still live before saving.
			stillOpen := false
			for _, ob := range buffer.OpenBuffers {
				if ob == targetBuf {
					stillOpen = true
					break
				}
			}
			if !stillOpen || targetPane.Buf != targetBuf {
				return
			}

			m.mu.Lock()
			activeGen := m.gen
			shutting := m.shuttingDown
			m.mu.Unlock()

			if !shutting && activeGen == gen && err == nil && targetBuf.Version() == reqVer && len(edits) > 0 {
				_, _ = ApplyTextEdits(targetBuf, edits)
			}

			if m.formattingOnSave == nil {
				m.formattingOnSave = make(map[*buffer.SharedBuffer]bool)
			}
			m.formattingOnSave[shared] = true
			targetPane.Save()
			delete(m.formattingOnSave, shared)
		})
	}()
	return true
}
