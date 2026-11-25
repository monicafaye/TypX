package lsp

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
)

var (
	reSnippetNested = regexp.MustCompile(`\$\{\d+:([^}]*)\}`)
	reSnippetChoice = regexp.MustCompile(`\$\{\d+\|([^,|}]*)[^}]*\}`)
	reSnippetStop   = regexp.MustCompile(`\$\{?\d+\}?`)
)

func (m *Manager) triggerCompletion(bp *action.BufPane) bool {
	if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
		return false
	}
	if bp.Cursor == nil || bp.Cursor.HasSelection() {
		return false
	}

	m.mu.Lock()
	state := m.State()
	c := m.client
	m.mu.Unlock()

	if state != StateReady || c == nil {
		notifyInfoMessage(m.notReadyMessage(state, "LSP: offline"))
		return true
	}

	targetBuf := bp.Buf
	m.flushDocChangeNow(targetBuf)

	reqVersion := targetBuf.Version()
	reqLoc := bp.Cursor.Loc
	rawLine := targetBuf.LineBytes(reqLoc.Y)
	lineCopy := append([]byte(nil), rawLine...)
	utf16Col := RuneColToUTF16(lineCopy, locXToRuneCol(lineCopy, reqLoc.X))
	uri := PathToURI(targetBuf.AbsPath)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		var raw json.RawMessage
		err := c.Call(ctx, "textDocument/completion", CompletionParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: reqLoc.Y, Character: utf16Col},
			Context:      &CompletionContext{TriggerKind: 1},
		}, &raw)
		if err != nil {
			notifyInfoError("LSP completion error: " + err.Error())
			return
		}
		items, perr := ParseCompletionResult(raw)
		if perr != nil {
			notifyInfoError("LSP completion parse error: " + perr.Error())
			return
		}

		action.EnqueueUI(func() {
			if !isBufferOpenAndAt(targetBuf, reqVersion, reqLoc, true) {
				return
			}
			completions, suggestions := buildCompletions(targetBuf, lineCopy, reqLoc, items)
			if len(suggestions) == 0 {
				notifyInfoMessage("No completions")
				return
			}
			targetBuf.Autocomplete(func(*buffer.Buffer) ([]string, []string) {
				return completions, suggestions
			})
		})
	}()
	return true
}

func buildCompletions(b *buffer.Buffer, lineBytes []byte, reqLoc buffer.Loc, items []CompletionItem) ([]string, []string) {
	if len(items) == 0 {
		return nil, nil
	}

	_, wordStart := b.GetWord()
	defaultPrefixStart := reqLoc.X
	if wordStart >= 0 && wordStart <= reqLoc.X {
		defaultPrefixStart = wordStart
	}

	var completions []string
	var suggestions []string
	seen := make(map[string]bool)

	for _, item := range items {
		if len(suggestions) >= maxCompletionItems {
			break
		}
		label := strings.TrimSpace(item.Label)
		if label == "" {
			continue
		}

		prefixStartX := defaultPrefixStart
		rawInsert := item.InsertText
		if item.TextEdit != nil && item.TextEdit.Range.Start.Line == reqLoc.Y && item.TextEdit.Range.End.Line == reqLoc.Y {
			endX := runeColToLocX(lineBytes, UTF16ToRuneCol(lineBytes, item.TextEdit.Range.End.Character))
			startX := runeColToLocX(lineBytes, UTF16ToRuneCol(lineBytes, item.TextEdit.Range.Start.Character))
			if endX == reqLoc.X && startX >= 0 && startX <= reqLoc.X {
				prefixStartX = startX
				rawInsert = item.TextEdit.NewText
			}
		}
		if rawInsert == "" {
			rawInsert = item.Label
		}
		if item.InsertTextFormat == InsertTextFormatSnippet || strings.Contains(rawInsert, "$") {
			rawInsert = stripSnippetPlaceholders(rawInsert)
		}

		typed := ""
		if prefixStartX < reqLoc.X {
			typed = string(b.Substr(buffer.Loc{X: prefixStartX, Y: reqLoc.Y}, reqLoc))
		}

		var suffix string
		if typed == "" {
			suffix = rawInsert
		} else if strings.HasPrefix(rawInsert, typed) {
			suffix = rawInsert[len(typed):]
		} else if foldSuffix, ok := trimFoldPrefix(rawInsert, typed); ok {
			suffix = foldSuffix
		} else {
			continue
		}

		dedupKey := label + "\x00" + suffix
		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true
		completions = append(completions, suffix)
		suggestions = append(suggestions, label)
	}

	return completions, suggestions
}

func (m *Manager) triggerHover(bp *action.BufPane) bool {
	if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
		return false
	}
	if bp.Cursor == nil {
		return false
	}

	m.mu.Lock()
	state := m.State()
	c := m.client
	m.mu.Unlock()

	if state != StateReady || c == nil {
		notifyInfoMessage(m.notReadyMessage(state, "LSP: offline"))
		return true
	}

	targetBuf := bp.Buf
	m.flushDocChangeNow(targetBuf)

	reqVersion := targetBuf.Version()
	reqLoc := bp.Cursor.Loc
	rawLine := targetBuf.LineBytes(reqLoc.Y)
	lineCopy := append([]byte(nil), rawLine...)
	utf16Col := RuneColToUTF16(lineCopy, locXToRuneCol(lineCopy, reqLoc.X))
	uri := PathToURI(targetBuf.AbsPath)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		var hov Hover
		err := c.Call(ctx, "textDocument/hover", HoverParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: reqLoc.Y, Character: utf16Col},
		}, &hov)
		if err != nil {
			notifyInfoError("LSP hover error: " + err.Error())
			return
		}
		text := cleanHoverText(ExtractHoverText(hov.Contents))

		action.EnqueueUI(func() {
			if !isBufferOpenAndAt(targetBuf, reqVersion, reqLoc, false) {
				return
			}
			if text == "" {
				notifyInfoMessage("No hover info")
				return
			}
			if !strings.Contains(text, "\n") && len(text) <= 120 {
				notifyInfoMessage(text)
				return
			}
			m.showSplitText(bp, &m.hoverBuf, "Hover", text)
		})
	}()
	return true
}

// requestDocumentSymbols asynchronously queries textDocument/documentSymbol for b
// when the LSP client is connected and ready. Calls cb on the main UI goroutine
// via action.EnqueueUI only if b is still open and at the same Version.
func (m *Manager) requestDocumentSymbols(b *buffer.Buffer, cb func(version uint64, symbols []DocumentSymbol)) bool {
	if b == nil || cb == nil || !isTrackableTypst(b) {
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

	targetBuf := b
	reqVersion := b.Version()
	uri := PathToURI(b.AbsPath)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()

		var raw json.RawMessage
		err := c.Call(ctx, "textDocument/documentSymbol", DocumentSymbolParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
		}, &raw)
		if err != nil {
			return
		}
		syms, perr := ParseDocumentSymbols(raw)
		if perr != nil || len(syms) == 0 {
			return
		}

		action.EnqueueUI(func() {
			m.mu.Lock()
			stale := m.shuttingDown || m.gen != gen
			m.mu.Unlock()
			if stale {
				return
			}
			if !isBufferStillOpenAtVersion(targetBuf, reqVersion) {
				return
			}
			cb(reqVersion, syms)
		})
	}()
	return true
}

func cleanHoverText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func stripSnippetPlaceholders(s string) string {
	s = reSnippetChoice.ReplaceAllString(s, "$1")
	s = reSnippetNested.ReplaceAllString(s, "$1")
	s = reSnippetStop.ReplaceAllString(s, "")
	return s
}

func trimFoldPrefix(s, prefix string) (string, bool) {
	pr := []rune(prefix)
	sr := []rune(s)
	if len(sr) < len(pr) {
		return "", false
	}
	if strings.EqualFold(string(sr[:len(pr)]), prefix) {
		return string(sr[len(pr):]), true
	}
	return "", false
}
