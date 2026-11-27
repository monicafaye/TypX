package lsp

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/util"
)

var (
	reSnippetNested = regexp.MustCompile(`\$\{\d+:([^}]*)\}`)
	reSnippetChoice = regexp.MustCompile(`\$\{\d+\|([^,|}]*)[^}]*\}`)
	reSnippetStop   = regexp.MustCompile(`\$\{?\d+\}?`)
)

type completionCandidate struct {
	label        string
	suffix       string
	rawInsert    string
	prefixStartX int
	isSnippet    bool
}

type activeCompletion struct {
	candidates    []completionCandidate
	reqVersion    uint64
	reqLine       int
	reqLoc        buffer.Loc
	reqLinePrefix string
}

var (
	completionHooksOnce sync.Once
	activeCompletionMu  sync.Mutex
	activeCompletions   = make(map[*buffer.SharedBuffer]*activeCompletion)
)

type completionCleanupObserver struct {
	buffer.NopObserver
}

func (completionCleanupObserver) OnBufferClose(b *buffer.Buffer) {
	if b == nil || b.Shared() || b.SharedBuffer == nil {
		return
	}
	clearActiveCompletion(b.SharedBuffer)
}

// RegisterCompletionHooks registers pre-action hooks for InsertNewline,
// Autocomplete, and Escape so active LSP completions can be committed
// (including snippet tabstop expansion) or reverted. Safe and idempotent to
// call multiple times.
func RegisterCompletionHooks() {
	completionHooksOnce.Do(func() {
		action.RegisterPreActionHook("InsertNewline", handleCompletionInsertNewline)
		action.RegisterPreActionHook("Autocomplete", handleCompletionAutocomplete)
		action.RegisterPreActionHook("Escape", handleCompletionEscape)
		buffer.RegisterObserver(completionCleanupObserver{})
	})
}

func setActiveCompletion(sb *buffer.SharedBuffer, sess *activeCompletion) {
	if sb == nil {
		return
	}
	activeCompletionMu.Lock()
	if sess == nil {
		delete(activeCompletions, sb)
	} else {
		activeCompletions[sb] = sess
	}
	activeCompletionMu.Unlock()
}

func getActiveCompletion(sb *buffer.SharedBuffer) *activeCompletion {
	if sb == nil {
		return nil
	}
	activeCompletionMu.Lock()
	sess := activeCompletions[sb]
	activeCompletionMu.Unlock()
	return sess
}

func clearActiveCompletion(sb *buffer.SharedBuffer) {
	if sb == nil {
		return
	}
	activeCompletionMu.Lock()
	delete(activeCompletions, sb)
	activeCompletionMu.Unlock()
}

func (m *Manager) triggerCompletion(bp *action.BufPane) bool {
	return m.requestCompletion(bp, "", true)
}

func (m *Manager) requestCompletion(bp *action.BufPane, triggerChar string, explicit bool) bool {
	if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
		return false
	}
	if bp.Cursor == nil || bp.Cursor.HasSelection() {
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

	compCtx := &CompletionContext{TriggerKind: 1}
	if !explicit && triggerChar != "" {
		compCtx = &CompletionContext{
			TriggerKind:      2,
			TriggerCharacter: triggerChar,
		}
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		var raw json.RawMessage
		err := c.Call(ctx, "textDocument/completion", CompletionParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: reqLoc.Y, Character: utf16Col},
			Context:      compCtx,
		}, &raw)
		if err != nil {
			if explicit {
				notifyInfoError("LSP completion error: " + err.Error())
			}
			return
		}
		items, perr := ParseCompletionResult(raw)
		if perr != nil {
			if explicit {
				notifyInfoError("LSP completion parse error: " + perr.Error())
			}
			return
		}

		action.EnqueueUI(func() {
			m.mu.Lock()
			stale := m.shuttingDown || m.gen != gen
			m.mu.Unlock()
			if stale || !isBufferOpenAndAt(targetBuf, reqVersion, reqLoc, true) {
				return
			}
			applyCompletionItems(bp, targetBuf, lineCopy, reqVersion, reqLoc, items, explicit)
		})
	}()
	return true
}

func applyCompletionItems(bp *action.BufPane, targetBuf *buffer.Buffer, lineBytes []byte, reqVersion uint64, reqLoc buffer.Loc, items []CompletionItem, explicit bool) {
	if targetBuf == nil {
		return
	}
	targetBP := activeBufPaneForBuffer(targetBuf)
	if targetBP == nil && bp != nil && bp.Buf == targetBuf {
		targetBP = bp
	}

	candidates := buildCompletionCandidates(targetBuf, lineBytes, reqLoc, items)
	if len(candidates) == 0 {
		clearActiveCompletion(targetBuf.SharedBuffer)
		if explicit {
			notifyInfoMessage("No completions")
		}
		return
	}

	if explicit && len(candidates) == 1 && candidates[0].isSnippet && targetBP != nil {
		if expand := getSnippetExpander(); expand != nil {
			start := buffer.Loc{X: candidates[0].prefixStartX, Y: reqLoc.Y}
			if expand(targetBP, start, reqLoc, candidates[0].rawInsert) {
				clearActiveCompletion(targetBuf.SharedBuffer)
				return
			}
		}
	}

	reqLinePrefix := ""
	if reqLoc.Y >= 0 && reqLoc.Y < targetBuf.LinesNum() {
		reqLinePrefix = string(targetBuf.Substr(buffer.Loc{X: 0, Y: reqLoc.Y}, reqLoc))
	}

	completions := make([]string, len(candidates))
	suggestions := make([]string, len(candidates))
	for i, cand := range candidates {
		completions[i] = cand.suffix
		suggestions[i] = cand.label
	}
	completer := func(*buffer.Buffer) ([]string, []string) {
		return completions, suggestions
	}
	if explicit {
		targetBuf.Autocomplete(completer)
	} else {
		targetBuf.ShowSuggestions(completer)
	}

	if targetBuf.HasSuggestions {
		setActiveCompletion(targetBuf.SharedBuffer, &activeCompletion{
			candidates:    candidates,
			reqVersion:    reqVersion,
			reqLine:       reqLoc.Y,
			reqLoc:        reqLoc,
			reqLinePrefix: reqLinePrefix,
		})
	} else {
		clearActiveCompletion(targetBuf.SharedBuffer)
	}
}

func slicesMatchCandidates(completions, suggestions []string, cands []completionCandidate) bool {
	if len(completions) != len(cands) || len(suggestions) != len(cands) {
		return false
	}
	for i, c := range cands {
		if completions[i] != c.suffix || suggestions[i] != c.label {
			return false
		}
	}
	return true
}

func resolveActiveCompletion(bp *action.BufPane) (*activeCompletion, int, buffer.Loc, bool) {
	if bp == nil || bp.Buf == nil || bp.Buf.SharedBuffer == nil || !bp.Buf.HasSuggestions {
		if bp != nil && bp.Buf != nil && bp.Buf.SharedBuffer != nil {
			clearActiveCompletion(bp.Buf.SharedBuffer)
		}
		return nil, 0, buffer.Loc{}, false
	}

	sess := getActiveCompletion(bp.Buf.SharedBuffer)
	if sess == nil {
		return nil, 0, buffer.Loc{}, false
	}

	// Ignore non-LSP completers (e.g. BufferComplete or offline bibliography)
	// without clearing their HasSuggestions state.
	if !slicesMatchCandidates(bp.Buf.Completions, bp.Buf.Suggestions, sess.candidates) {
		clearActiveCompletion(bp.Buf.SharedBuffer)
		return nil, 0, buffer.Loc{}, false
	}

	b := bp.Buf
	if !b.IsTypst() || b.Type.Readonly || b.NumCursors() != 1 || bp.Cursor == nil || bp.Cursor.HasSelection() {
		clearActiveCompletion(b.SharedBuffer)
		b.HasSuggestions = false
		return nil, 0, buffer.Loc{}, false
	}
	if sess.reqLine < 0 || sess.reqLine >= b.LinesNum() {
		clearActiveCompletion(b.SharedBuffer)
		b.HasSuggestions = false
		return nil, 0, buffer.Loc{}, false
	}
	if util.CharacterCount(b.LineBytes(sess.reqLine)) < sess.reqLoc.X ||
		string(b.Substr(buffer.Loc{X: 0, Y: sess.reqLine}, sess.reqLoc)) != sess.reqLinePrefix {
		clearActiveCompletion(b.SharedBuffer)
		b.HasSuggestions = false
		return nil, 0, buffer.Loc{}, false
	}

	if b.CurSuggestion == -1 {
		if b.Version() != sess.reqVersion || bp.Cursor.Loc != sess.reqLoc {
			clearActiveCompletion(b.SharedBuffer)
			b.HasSuggestions = false
			return nil, 0, buffer.Loc{}, false
		}
		return sess, 0, sess.reqLoc, true
	}

	if b.CurSuggestion >= 0 && b.CurSuggestion < len(sess.candidates) {
		cand := sess.candidates[b.CurSuggestion]
		expectedEnd := sess.reqLoc.Move(util.CharacterCountInString(cand.suffix), b)
		if bp.Cursor.Loc != expectedEnd || string(b.Substr(sess.reqLoc, bp.Cursor.Loc)) != cand.suffix {
			clearActiveCompletion(b.SharedBuffer)
			b.HasSuggestions = false
			return nil, 0, buffer.Loc{}, false
		}
		return sess, b.CurSuggestion, bp.Cursor.Loc, true
	}

	clearActiveCompletion(b.SharedBuffer)
	b.HasSuggestions = false
	return nil, 0, buffer.Loc{}, false
}

func handleCompletionInsertNewline(bp *action.BufPane) bool {
	sess, idx, curLoc, ok := resolveActiveCompletion(bp)
	if !ok {
		return false
	}
	cand := sess.candidates[idx]
	curSel := bp.Buf.CurSuggestion

	clearActiveCompletion(bp.Buf.SharedBuffer)
	bp.Buf.HasSuggestions = false

	if cand.isSnippet {
		if expand := getSnippetExpander(); expand != nil {
			start := buffer.Loc{X: cand.prefixStartX, Y: sess.reqLine}
			end := sess.reqLoc
			if curSel >= 0 {
				end = curLoc
			}
			if expand(bp, start, end, cand.rawInsert) {
				bp.Buf.HasSuggestions = false
				return true
			}
		}
	}

	if curSel < 0 && cand.suffix != "" {
		bp.Buf.Insert(sess.reqLoc, cand.suffix)
		if bp.Cursor != nil {
			bp.Cursor.StoreVisualX()
		}
		if bp.BWindow != nil {
			bp.Relocate()
		}
	}
	bp.Buf.HasSuggestions = false
	return true
}

func handleCompletionAutocomplete(bp *action.BufPane) bool {
	if bp == nil || bp.Buf == nil || !bp.Buf.HasSuggestions || bp.Buf.CurSuggestion >= 0 {
		return false
	}
	sess, idx, _, ok := resolveActiveCompletion(bp)
	if !ok || len(sess.candidates) != 1 || idx != 0 || !sess.candidates[0].isSnippet {
		return false
	}
	cand := sess.candidates[0]
	expand := getSnippetExpander()
	if expand == nil {
		return false
	}
	clearActiveCompletion(bp.Buf.SharedBuffer)
	bp.Buf.HasSuggestions = false
	start := buffer.Loc{X: cand.prefixStartX, Y: sess.reqLine}
	if expand(bp, start, sess.reqLoc, cand.rawInsert) {
		bp.Buf.HasSuggestions = false
		return true
	}
	return false
}

func handleCompletionEscape(bp *action.BufPane) bool {
	sess, _, curLoc, ok := resolveActiveCompletion(bp)
	if !ok {
		return false
	}
	curSel := bp.Buf.CurSuggestion

	clearActiveCompletion(bp.Buf.SharedBuffer)
	if curSel >= 0 && sess.reqLoc != curLoc {
		bp.Buf.Remove(sess.reqLoc, curLoc)
		if bp.Cursor != nil {
			bp.Cursor.GotoLoc(sess.reqLoc)
			bp.Cursor.ResetSelection()
			bp.Cursor.StoreVisualX()
		}
		if bp.BWindow != nil {
			bp.Relocate()
		}
	}
	bp.Buf.HasSuggestions = false
	return true
}

func (m *Manager) scheduleAutoCompletion(bp *action.BufPane, triggerChar string) {
	if bp == nil || bp.Buf == nil || bp.Cursor == nil || bp.Cursor.HasSelection() {
		return
	}
	if !readGlobalBool("typst.autoComplete", true) {
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
	if m.autoCompleteTimer != nil {
		m.autoCompleteTimer.Stop()
	}
	gen := m.gen
	m.autoCompleteTimer = time.AfterFunc(autoCompleteDelay, func() {
		action.EnqueueUI(func() {
			m.mu.Lock()
			stale := m.shuttingDown || m.gen != gen
			m.mu.Unlock()
			if stale || !isBufferOpenAndAt(targetBuf, reqVersion, reqLoc, true) {
				return
			}
			curBP := activeBufPaneForBuffer(targetBuf)
			if curBP == nil {
				return
			}
			m.requestCompletion(curBP, triggerChar, false)
		})
	})
	m.mu.Unlock()
}

func buildCompletions(b *buffer.Buffer, lineBytes []byte, reqLoc buffer.Loc, items []CompletionItem) ([]string, []string) {
	candidates := buildCompletionCandidates(b, lineBytes, reqLoc, items)
	if len(candidates) == 0 {
		return nil, nil
	}
	completions := make([]string, len(candidates))
	suggestions := make([]string, len(candidates))
	for i, cand := range candidates {
		completions[i] = cand.suffix
		suggestions[i] = cand.label
	}
	return completions, suggestions
}

func buildCompletionCandidates(b *buffer.Buffer, lineBytes []byte, reqLoc buffer.Loc, items []CompletionItem) []completionCandidate {
	if len(items) == 0 {
		return nil
	}

	_, wordStart := b.GetWord()
	defaultPrefixStart := reqLoc.X
	if wordStart >= 0 && wordStart <= reqLoc.X {
		defaultPrefixStart = wordStart
	}

	var candidates []completionCandidate
	seen := make(map[string]bool)

	for _, item := range items {
		if len(candidates) >= maxCompletionItems {
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
		isSnippet := item.InsertTextFormat == InsertTextFormatSnippet
		plainInsert := rawInsert
		if isSnippet || (item.InsertTextFormat == 0 && strings.Contains(rawInsert, "$")) {
			plainInsert = stripSnippetPlaceholders(rawInsert)
			if plainInsert != rawInsert {
				isSnippet = true
			}
		}

		typed := ""
		if prefixStartX < reqLoc.X {
			typed = string(b.Substr(buffer.Loc{X: prefixStartX, Y: reqLoc.Y}, reqLoc))
		}

		var suffix string
		if typed == "" {
			suffix = plainInsert
		} else if strings.HasPrefix(plainInsert, typed) {
			suffix = plainInsert[len(typed):]
		} else if foldSuffix, ok := trimFoldPrefix(plainInsert, typed); ok {
			suffix = foldSuffix
		} else {
			continue
		}

		dedupKey := label + "\x00" + suffix
		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true
		candidates = append(candidates, completionCandidate{
			label:        label,
			suffix:       suffix,
			rawInsert:    rawInsert,
			prefixStartX: prefixStartX,
			isSnippet:    isSnippet,
		})
	}

	return candidates
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
	for i := 0; i < 4; i++ {
		next := reSnippetNested.ReplaceAllString(s, "$1")
		if next == s {
			break
		}
		s = next
	}
	s = reSnippetStop.ReplaceAllString(s, "")
	if strings.ContainsRune(s, '\\') {
		var b strings.Builder
		b.Grow(len(s))
		for i := 0; i < len(s); i++ {
			if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '$' || s[i+1] == '}' || s[i+1] == '\\') {
				i++
			}
			b.WriteByte(s[i])
		}
		s = b.String()
	}
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
