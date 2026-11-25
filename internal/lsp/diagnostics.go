package lsp

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/util"
)

func (m *Manager) applyDiagnostics(gen uint64, p PublishDiagnosticsParams) {
	rawPath := URIToPath(p.URI)
	targetKey := canonicalDocKey(rawPath)
	errCount := 0
	var errMsgs []string
	for _, d := range p.Diagnostics {
		if d.Severity == SeverityError || d.Severity == 0 {
			errCount++
			if len(errMsgs) < 10 {
				errMsgs = append(errMsgs, fmt.Sprintf("line %d: %s", d.Range.Start.Line+1, collapseDiagnosticMessage(d.Message)))
			}
		}
	}
	m.mu.Lock()
	if !m.shuttingDown && m.gen == gen && targetKey != "" {
		if m.docErrorCounts == nil {
			m.docErrorCounts = make(map[string]int)
		}
		if m.docErrors == nil {
			m.docErrors = make(map[string][]string)
		}
		if m.docDiagnostics == nil {
			m.docDiagnostics = make(map[string][]Diagnostic)
		}
		if m.docDiagnosticPaths == nil {
			m.docDiagnosticPaths = make(map[string]string)
		}
		if errCount > 0 {
			m.docErrorCounts[targetKey] = errCount
			m.docErrors[targetKey] = errMsgs
		} else {
			delete(m.docErrorCounts, targetKey)
			delete(m.docErrors, targetKey)
		}
		if len(p.Diagnostics) > 0 {
			m.docDiagnostics[targetKey] = append([]Diagnostic(nil), p.Diagnostics...)
			m.docDiagnosticPaths[targetKey] = rawPath
		} else {
			delete(m.docDiagnostics, targetKey)
			delete(m.docDiagnosticPaths, targetKey)
		}
	}
	m.mu.Unlock()

	action.EnqueueUI(func() {
		m.mu.Lock()
		activeGen := m.gen
		shutting := m.shuttingDown
		m.mu.Unlock()
		if shutting || activeGen != gen {
			return
		}

		seen := make(map[*buffer.SharedBuffer]bool)
		var updatedShared *buffer.SharedBuffer
		for _, b := range buffer.OpenBuffers {
			if b == nil || b.Type.Scratch || b.Path == "" {
				continue
			}
			if canonicalDocKey(b.AbsPath) != targetKey {
				continue
			}
			if seen[b.SharedBuffer] {
				continue
			}
			seen[b.SharedBuffer] = true

			if p.Version != nil && *p.Version < int(b.Version()) {
				continue
			}

			b.ClearMessages(diagnosticOwner)
			numLines := b.LinesNum()
			if numLines > 0 {
				for _, d := range p.Diagnostics {
					kind := severityToMsgType(d.Severity)
					startY := util.Clamp(d.Range.Start.Line, 0, numLines-1)
					endY := util.Clamp(d.Range.End.Line, 0, numLines-1)

					startLine := b.LineBytes(startY)
					endLine := b.LineBytes(endY)

					startX := runeColToLocX(startLine, UTF16ToRuneCol(startLine, d.Range.Start.Character))
					endX := runeColToLocX(endLine, UTF16ToRuneCol(endLine, d.Range.End.Character))

					start := buffer.Loc{X: startX, Y: startY}
					end := buffer.Loc{X: endX, Y: endY}
					if end.LessThan(start) {
						end = start
					}
					if start == end {
						if start.X < util.CharacterCount(startLine) {
							end.X = start.X + 1
						}
					}

					msgText := collapseDiagnosticMessage(d.Message)
					b.AddMessage(buffer.NewMessage(diagnosticOwner, msgText, start, end, kind))
				}
			}
			updatedShared = b.SharedBuffer
		}

		if updatedShared != nil {
			syncActivePaneGutter(updatedShared)
		}
	})
}

func clearAllTinymistDiagnostics() {
	seen := make(map[*buffer.SharedBuffer]bool)
	for _, b := range buffer.OpenBuffers {
		if b == nil || seen[b.SharedBuffer] {
			continue
		}
		seen[b.SharedBuffer] = true
		b.ClearMessages(diagnosticOwner)
	}
	if action.InfoBar != nil && !action.InfoBar.HasPrompt && action.InfoBar.HasGutter {
		action.InfoBar.ClearGutter()
	}
}

func syncActivePaneGutter(sb *buffer.SharedBuffer) {
	if action.Tabs == nil || len(action.Tabs.List) == 0 || action.InfoBar == nil || action.InfoBar.HasPrompt {
		return
	}
	tab := action.MainTab()
	if tab == nil {
		return
	}
	bp := tab.CurPane()
	if bp == nil || bp.Buf == nil || bp.Buf.SharedBuffer != sb {
		return
	}
	c := bp.Buf.GetActiveCursor()
	if c == nil {
		return
	}
	for _, msg := range bp.Buf.Messages {
		if c.Y == msg.Start.Y || c.Y == msg.End.Y {
			action.InfoBar.GutterMessage(msg.Msg)
			return
		}
	}
	if action.InfoBar.HasGutter {
		action.InfoBar.ClearGutter()
	}
}

func (m *Manager) jumpDiagnostic(bp *action.BufPane, delta int) bool {
	if bp == nil || bp.Buf == nil || bp.Cursor == nil {
		return false
	}
	var msgs []*buffer.Message
	for _, msg := range bp.Buf.Messages {
		if msg != nil && msg.Owner == diagnosticOwner {
			msgs = append(msgs, msg)
		}
	}
	if len(msgs) == 0 {
		setInfoMessage("No diagnostics in buffer")
		return false
	}

	sort.Slice(msgs, func(i, j int) bool {
		if msgs[i].Start.Y != msgs[j].Start.Y {
			return msgs[i].Start.Y < msgs[j].Start.Y
		}
		if msgs[i].Start.X != msgs[j].Start.X {
			return msgs[i].Start.X < msgs[j].Start.X
		}
		return msgKindRank(msgs[i].Kind) < msgKindRank(msgs[j].Kind)
	})

	cur := bp.Cursor.Loc
	chosenIdx := 0
	if delta >= 0 {
		chosenIdx = 0
		for i, msg := range msgs {
			if cur.LessThan(msg.Start) {
				chosenIdx = i
				break
			}
		}
	} else {
		chosenIdx = len(msgs) - 1
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Start.LessThan(cur) {
				chosenIdx = i
				break
			}
		}
	}

	chosen := msgs[chosenIdx]
	applyCursorJump(bp, chosen.Start)
	if action.InfoBar != nil && !action.InfoBar.HasPrompt {
		action.InfoBar.GutterMessage(fmt.Sprintf("[%d/%d] %s", chosenIdx+1, len(msgs), chosen.Msg))
	}
	return true
}

func (m *Manager) showDiagnostics(bp *action.BufPane) {
	if bp == nil || bp.Buf == nil {
		return
	}

	activePath := bp.Buf.AbsPath
	activeKey := canonicalDocKey(activePath)

	m.mu.Lock()
	diagSnapshot := make(map[string][]Diagnostic, len(m.docDiagnostics))
	pathSnapshot := make(map[string]string, len(m.docDiagnosticPaths))
	for k, list := range m.docDiagnostics {
		if len(list) > 0 {
			diagSnapshot[k] = append([]Diagnostic(nil), list...)
			pathSnapshot[k] = m.docDiagnosticPaths[k]
		}
	}
	m.mu.Unlock()

	if activeKey == "" {
		activeKey = bp.Buf.GetName()
	}
	if _, ok := diagSnapshot[activeKey]; !ok {
		fallback := bufferMessagesToDiagnostics(bp.Buf)
		if len(fallback) > 0 {
			diagSnapshot[activeKey] = fallback
			if activePath != "" {
				pathSnapshot[activeKey] = activePath
			} else {
				pathSnapshot[activeKey] = bp.Buf.GetName()
			}
		}
	}

	if len(diagSnapshot) == 0 {
		CloseListPaneForTab(bp.Tab())
		setInfoMessage("No diagnostics")
		return
	}

	otherKeys := make([]string, 0, len(diagSnapshot))
	for k := range diagSnapshot {
		if k == activeKey {
			continue
		}
		otherKeys = append(otherKeys, k)
	}
	sort.Slice(otherKeys, func(i, j int) bool {
		return pathSnapshot[otherKeys[i]] < pathSnapshot[otherKeys[j]]
	})

	orderedKeys := make([]string, 0, len(diagSnapshot))
	if _, ok := diagSnapshot[activeKey]; ok {
		orderedKeys = append(orderedKeys, activeKey)
	}
	orderedKeys = append(orderedKeys, otherKeys...)

	errCount := 0
	warnCount := 0
	var items []ListItem

	for _, key := range orderedKeys {
		filePath := pathSnapshot[key]
		if filePath == "" {
			filePath = key
		}
		diags := diagSnapshot[key]
		sort.SliceStable(diags, func(i, j int) bool {
			ri, rj := severityRank(diags[i].Severity), severityRank(diags[j].Severity)
			if ri != rj {
				return ri < rj
			}
			if diags[i].Range.Start.Line != diags[j].Range.Start.Line {
				return diags[i].Range.Start.Line < diags[j].Range.Start.Line
			}
			return diags[i].Range.Start.Character < diags[j].Range.Start.Character
		})

		baseName := filepath.Base(filePath)
		for _, d := range diags {
			sev := d.Severity
			if sev == 0 {
				sev = SeverityError
			}
			switch sev {
			case SeverityError:
				errCount++
			case SeverityWarning:
				warnCount++
			}

			diagCopy := d
			targetPath := filePath
			isMemOnly := activePath == "" && key == activeKey
			items = append(items, ListItem{
				FilePath:  targetPath,
				Line:      diagCopy.Range.Start.Line,
				Col:       diagCopy.Range.Start.Character,
				Severity:  sev,
				Badge:     severityBadge(sev),
				Primary:   collapseDiagnosticMessage(diagCopy.Message),
				Secondary: fmt.Sprintf("%s:%d:%d", baseName, diagCopy.Range.Start.Line+1, diagCopy.Range.Start.Character+1),
				OnSelect: func() {
					if isMemOnly {
						applyCursorJump(bp, buffer.Loc{
							X: diagCopy.Range.Start.Character,
							Y: diagCopy.Range.Start.Line,
						})
						return
					}
					m.jumpToLocation(bp, Location{
						URI:   PathToURI(targetPath),
						Range: diagCopy.Range,
					})
				},
			})
		}
	}

	if len(items) == 0 {
		CloseListPaneForTab(bp.Tab())
		setInfoMessage("No diagnostics")
		return
	}

	title := fmt.Sprintf("Diagnostics (%d err, %d warn)", errCount, warnCount)
	OpenListPane(bp, title, true, items)
}

func bufferMessagesToDiagnostics(b *buffer.Buffer) []Diagnostic {
	if b == nil {
		return nil
	}
	var out []Diagnostic
	for _, msg := range b.Messages {
		if msg == nil || msg.Owner != diagnosticOwner {
			continue
		}
		startUTF16 := msg.Start.X
		if msg.Start.Y >= 0 && msg.Start.Y < b.LinesNum() {
			line := b.LineBytes(msg.Start.Y)
			startUTF16 = RuneColToUTF16(line, locXToRuneCol(line, msg.Start.X))
		}
		endUTF16 := msg.End.X
		if msg.End.Y >= 0 && msg.End.Y < b.LinesNum() {
			line := b.LineBytes(msg.End.Y)
			endUTF16 = RuneColToUTF16(line, locXToRuneCol(line, msg.End.X))
		}
		out = append(out, Diagnostic{
			Range: Range{
				Start: Position{Line: msg.Start.Y, Character: startUTF16},
				End:   Position{Line: msg.End.Y, Character: endUTF16},
			},
			Severity: msgTypeToSeverity(msg.Kind),
			Message:  msg.Msg,
		})
	}
	return out
}

func severityToMsgType(sev int) buffer.MsgType {
	switch sev {
	case SeverityError:
		return buffer.MTError
	case SeverityWarning:
		return buffer.MTWarning
	default:
		return buffer.MTInfo
	}
}

func msgTypeToSeverity(kind buffer.MsgType) int {
	switch kind {
	case buffer.MTError:
		return SeverityError
	case buffer.MTWarning:
		return SeverityWarning
	default:
		return SeverityInformation
	}
}

func severityRank(sev int) int {
	switch sev {
	case SeverityError, 0:
		return 0
	case SeverityWarning:
		return 1
	case SeverityInformation:
		return 2
	default:
		return 3
	}
}

func msgKindRank(k buffer.MsgType) int {
	switch k {
	case buffer.MTError:
		return 0
	case buffer.MTWarning:
		return 1
	default:
		return 2
	}
}

func severityBadge(sev int) string {
	switch sev {
	case SeverityError, 0:
		return "ERR"
	case SeverityWarning:
		return "WARN"
	case SeverityHint:
		return "HINT"
	default:
		return "INFO"
	}
}

func collapseDiagnosticMessage(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}
