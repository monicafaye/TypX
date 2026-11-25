package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
)

func (m *Manager) triggerCodeAction(bp *action.BufPane, explicitCmd bool) bool {
	if bp == nil || bp.Buf == nil || bp.Cursor == nil || !bp.Buf.IsTypst() {
		if explicitCmd {
			setInfoMessage("Code actions are only available for .typ buffers")
		}
		return false
	}
	if bp.Buf.AbsPath == "" {
		setInfoMessage("Save buffer before requesting code actions")
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
	key := canonicalDocKey(absPath)
	uri := PathToURI(absPath)
	reqVer := targetBuf.Version()
	shared := targetBuf.SharedBuffer
	originTab := bp.Tab()

	reqRange := codeActionRange(targetBuf, bp.Cursor)
	ctxDiags := m.diagnosticsInRange(key, reqRange)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var raw json.RawMessage
		err := c.Call(ctx, "textDocument/codeAction", CodeActionParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Range:        reqRange,
			Context: CodeActionContext{
				Diagnostics: ctxDiags,
				TriggerKind: 1, // Invoked explicitly
			},
		}, &raw)
		canceled := ctx.Err() == context.Canceled
		cancel()

		if err != nil {
			if canceled {
				return
			}
			notifyInfoError(fmt.Sprintf("Code action request failed: %v", err))
			return
		}

		actions, parseErr := ParseCodeActions(raw)
		if parseErr != nil {
			notifyInfoError(fmt.Sprintf("Code action parse failed: %v", parseErr))
			return
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
			if len(actions) == 0 {
				setInfoMessage("No code actions available")
				return
			}

			items := make([]ListItem, 0, len(actions))
			for _, ca := range actions {
				actionItem := ca
				items = append(items, ListItem{
					Badge:     codeActionBadge(actionItem.Kind, actionItem.IsPreferred),
					Primary:   actionItem.Title,
					Secondary: actionItem.Kind,
					OnSelect: func() {
						CloseListPaneForTab(originTab)
						m.executeCodeAction(targetBuf, gen, reqVer, actionItem)
					},
				})
			}
			OpenListPane(bp, "Code Actions", false, items)
		})
	}()
	return true
}

func (m *Manager) executeCodeAction(targetBuf *buffer.Buffer, gen uint64, reqVer uint64, ca CodeAction) {
	if !isBufferStillOpenAtVersion(targetBuf, reqVer) {
		setInfoMessage("Buffer modified; code action canceled")
		return
	}
	if ca.Edit != nil {
		if _, _, err := ApplyWorkspaceEdit(*ca.Edit); err != nil {
			setInfoError("Code action failed: " + err.Error())
			return
		}
		if ca.Command == nil || ca.Command.Command == "" {
			setInfoMessage("Applied: " + ca.Title)
			return
		}
	}

	if ca.Command == nil || ca.Command.Command == "" {
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

	cmdName := ca.Command.Command
	cmdArgs := ca.Command.Arguments
	title := ca.Title

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := c.Call(ctx, "workspace/executeCommand", ExecuteCommandParams{
			Command:   cmdName,
			Arguments: cmdArgs,
		}, nil)
		canceled := ctx.Err() == context.Canceled
		cancel()

		if err != nil {
			if canceled {
				return
			}
			notifyInfoError(fmt.Sprintf("Code action command failed: %v", err))
			return
		}
		notifyInfoMessage("Applied: " + title)
	}()
}

func (m *Manager) diagnosticsInRange(key string, req Range) []Diagnostic {
	m.mu.Lock()
	all := append([]Diagnostic(nil), m.docDiagnostics[key]...)
	m.mu.Unlock()

	out := make([]Diagnostic, 0)
	for _, d := range all {
		if d.Range.Start.Line <= req.End.Line && d.Range.End.Line >= req.Start.Line {
			out = append(out, d)
		}
	}
	return out
}

func codeActionRange(b *buffer.Buffer, c *buffer.Cursor) Range {
	if b == nil || c == nil {
		return Range{}
	}
	locToPos := func(loc buffer.Loc) Position {
		var lineBytes []byte
		if loc.Y >= 0 && loc.Y < b.LinesNum() {
			lineBytes = b.LineBytes(loc.Y)
		}
		return Position{
			Line:      loc.Y,
			Character: RuneColToUTF16(lineBytes, locXToRuneCol(lineBytes, loc.X)),
		}
	}
	if c.HasSelection() {
		a, z := c.CurSelection[0], c.CurSelection[1]
		if z.LessThan(a) {
			a, z = z, a
		}
		return Range{Start: locToPos(a), End: locToPos(z)}
	}
	p := locToPos(c.Loc)
	return Range{Start: p, End: p}
}

func codeActionBadge(kind string, preferred bool) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	badge := "ACT"
	switch {
	case strings.HasPrefix(k, "quickfix"):
		badge = "FIX"
	case strings.HasPrefix(k, "refactor"):
		badge = "REF"
	case strings.HasPrefix(k, "source"):
		badge = "SRC"
	}
	if preferred {
		badge += "*"
	}
	return badge
}
