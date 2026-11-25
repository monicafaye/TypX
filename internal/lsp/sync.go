package lsp

import (
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/util"
)

type openDoc struct {
	key           string
	uri           string
	absPath       string
	shared        *buffer.SharedBuffer
	opened        bool
	lastQueuedVer uint64
	lastSentVer   uint64
	latestText    string
	timer         *time.Timer
}

// OnBufferOpen tracks newly opened .typ buffers and sends textDocument/didOpen.
func (m *Manager) OnBufferOpen(b *buffer.Buffer) {
	if !isTrackableTypst(b) {
		return
	}
	m.refreshMainSettings()

	text := string(b.Bytes())
	ver := b.Version()
	absPath := b.AbsPath
	key := canonicalDocKey(absPath)
	uri := PathToURI(absPath)
	shared := b.SharedBuffer

	m.mu.Lock()
	if m.shuttingDown || !m.lspEnabled {
		m.mu.Unlock()
		return
	}
	if existing, ok := m.docs[key]; ok {
		if existing.shared == shared {
			m.mu.Unlock()
			return
		}
		if existing.timer != nil {
			existing.timer.Stop()
		}
	}
	doc := &openDoc{
		key:           key,
		uri:           uri,
		absPath:       absPath,
		shared:        shared,
		lastQueuedVer: ver,
		latestText:    text,
	}
	m.docs[key] = doc

	var c *Client
	startPreviewNow := false
	openBrowser := m.previewOpenBrowser
	previewTarget := absPath
	var pendingPin string
	if m.pinnedMain == "" && !m.pinExplicitlyCleared && absPath != "" {
		if root := findProjectRoot(absPath); root != "" {
			if p := loadPersistedPin(root); p != "" {
				m.pinnedMain = p
				m.pinnedRoot = root
				m.atomicPinnedMain.Store(p)
				pendingPin = p
			}
		}
	}
	if m.pinnedMain != "" {
		previewTarget = m.pinnedMain
	}
	if m.State() == StateReady && m.client != nil {
		doc.opened = true
		doc.lastSentVer = ver
		c = m.client
		if m.startPreviewPend {
			m.startPreviewPend = false
			startPreviewNow = true
		}
	}
	m.mu.Unlock()

	if c != nil {
		_ = c.Notify("textDocument/didOpen", DidOpenTextDocumentParams{
			TextDocument: TextDocumentItem{
				URI:        uri,
				LanguageID: "typst",
				Version:    int(ver),
				Text:       text,
			},
		})
		if pendingPin != "" {
			go func(client *Client, p string) {
				_ = sendPinMain(client, p)
			}(c, pendingPin)
		}
	}
	if startPreviewNow {
		go m.startPreview(previewTarget, openBrowser)
	}
}

// OnBufferChange snapshots buffer text on the main goroutine and schedules a
// debounced textDocument/didChange notification.
func (m *Manager) OnBufferChange(b *buffer.Buffer, ev buffer.ChangeEvent) {
	if !isTrackableTypst(b) {
		return
	}
	debounceMs := readGlobalInt("typst.debounceMs", 150)
	text := string(b.Bytes())
	ver := ev.Version
	if ver == 0 {
		ver = b.Version()
	}
	absPath := b.AbsPath
	key := canonicalDocKey(absPath)
	uri := PathToURI(absPath)

	m.mu.Lock()
	m.debounceMs = debounceMs
	if m.shuttingDown || !m.lspEnabled {
		m.mu.Unlock()
		return
	}
	doc, ok := m.docs[key]
	if !ok {
		doc = &openDoc{
			key:           key,
			uri:           uri,
			absPath:       absPath,
			shared:        b.SharedBuffer,
			lastQueuedVer: ver,
			latestText:    text,
		}
		m.docs[key] = doc
		if m.State() == StateReady && m.client != nil {
			doc.opened = true
			doc.lastSentVer = ver
			c := m.client
			m.mu.Unlock()
			_ = c.Notify("textDocument/didOpen", DidOpenTextDocumentParams{
				TextDocument: TextDocumentItem{
					URI:        uri,
					LanguageID: "typst",
					Version:    int(ver),
					Text:       text,
				},
			})
			return
		}
		m.mu.Unlock()
		return
	}

	if ver <= doc.lastQueuedVer {
		m.mu.Unlock()
		return
	}
	doc.lastQueuedVer = ver
	doc.latestText = text
	if doc.timer != nil {
		doc.timer.Stop()
		doc.timer = nil
	}

	if !doc.opened || m.State() != StateReady || m.client == nil {
		m.mu.Unlock()
		return
	}

	if debounceMs <= 0 {
		doc.lastSentVer = ver
		c := m.client
		m.mu.Unlock()
		_ = c.Notify("textDocument/didChange", DidChangeTextDocumentParams{
			TextDocument: VersionedTextDocumentIdentifier{
				URI:     uri,
				Version: int(ver),
			},
			ContentChanges: []TextDocumentContentChangeEvent{{Text: text}},
		})
		return
	}

	gen := m.gen
	doc.timer = time.AfterFunc(time.Duration(debounceMs)*time.Millisecond, func() {
		m.flushDocChange(key, gen, ver)
	})
	m.mu.Unlock()
}

func (m *Manager) flushDocChange(key string, gen uint64, targetVer uint64) {
	m.mu.Lock()
	if m.shuttingDown || m.gen != gen || m.State() != StateReady || m.client == nil {
		m.mu.Unlock()
		return
	}
	doc, ok := m.docs[key]
	if !ok || !doc.opened {
		m.mu.Unlock()
		return
	}
	if doc.lastSentVer >= doc.lastQueuedVer || doc.lastQueuedVer < targetVer {
		m.mu.Unlock()
		return
	}
	ver := doc.lastQueuedVer
	text := doc.latestText
	uri := doc.uri
	doc.lastSentVer = ver
	doc.timer = nil
	c := m.client
	m.mu.Unlock()

	_ = c.Notify("textDocument/didChange", DidChangeTextDocumentParams{
		TextDocument: VersionedTextDocumentIdentifier{
			URI:     uri,
			Version: int(ver),
		},
		ContentChanges: []TextDocumentContentChangeEvent{{Text: text}},
	})
}

// flushDocChangeNow cancels any pending debounce timer for b and immediately
// dispatches textDocument/didOpen or textDocument/didChange on the wire so
// subsequent RPC requests (formatting, definition, completion, etc.) observe
// the exact current buffer state. Must be called on the main goroutine.
func (m *Manager) flushDocChangeNow(b *buffer.Buffer) {
	if !isTrackableTypst(b) {
		return
	}
	text := string(b.Bytes())
	ver := b.Version()
	absPath := b.AbsPath
	key := canonicalDocKey(absPath)
	uri := PathToURI(absPath)

	m.mu.Lock()
	if m.shuttingDown || !m.lspEnabled || m.State() != StateReady || m.client == nil {
		m.mu.Unlock()
		return
	}
	c := m.client
	doc, ok := m.docs[key]
	if !ok {
		doc = &openDoc{
			key:           key,
			uri:           uri,
			absPath:       absPath,
			shared:        b.SharedBuffer,
			opened:        true,
			lastQueuedVer: ver,
			lastSentVer:   ver,
			latestText:    text,
		}
		m.docs[key] = doc
		m.mu.Unlock()
		_ = c.Notify("textDocument/didOpen", DidOpenTextDocumentParams{
			TextDocument: TextDocumentItem{
				URI:        uri,
				LanguageID: "typst",
				Version:    int(ver),
				Text:       text,
			},
		})
		return
	}

	if doc.timer != nil {
		doc.timer.Stop()
		doc.timer = nil
	}
	if ver > doc.lastQueuedVer {
		doc.lastQueuedVer = ver
		doc.latestText = text
	}
	if !doc.opened {
		doc.opened = true
		sendVer := doc.lastQueuedVer
		sendText := doc.latestText
		doc.lastSentVer = sendVer
		m.mu.Unlock()
		_ = c.Notify("textDocument/didOpen", DidOpenTextDocumentParams{
			TextDocument: TextDocumentItem{
				URI:        uri,
				LanguageID: "typst",
				Version:    int(sendVer),
				Text:       sendText,
			},
		})
		return
	}
	if doc.lastSentVer >= doc.lastQueuedVer {
		m.mu.Unlock()
		return
	}
	sendVer := doc.lastQueuedVer
	sendText := doc.latestText
	doc.lastSentVer = sendVer
	m.mu.Unlock()

	_ = c.Notify("textDocument/didChange", DidChangeTextDocumentParams{
		TextDocument: VersionedTextDocumentIdentifier{
			URI:     uri,
			Version: int(sendVer),
		},
		ContentChanges: []TextDocumentContentChangeEvent{{Text: sendText}},
	})
}

// OnBufferSave handles Save and SaveAs transitions, flushing pending edits
// before sending textDocument/didSave.
func (m *Manager) OnBufferSave(b *buffer.Buffer, ev buffer.SaveEvent) {
	if b == nil {
		return
	}
	m.refreshMainSettings()

	newKey := canonicalDocKey(b.AbsPath)
	if ev.PrevAbsPath != "" {
		oldKey := canonicalDocKey(ev.PrevAbsPath)
		if oldKey != newKey {
			var oldClient *Client
			var oldURI string
			var oldOpened bool
			m.mu.Lock()
			if oldDoc, ok := m.docs[oldKey]; ok {
				if oldDoc.timer != nil {
					oldDoc.timer.Stop()
					oldDoc.timer = nil
				}
				delete(m.docs, oldKey)
				oldOpened = oldDoc.opened
				oldURI = oldDoc.uri
				if m.State() == StateReady {
					oldClient = m.client
				}
			}
			m.mu.Unlock()
			if oldClient != nil && oldOpened {
				_ = oldClient.Notify("textDocument/didClose", DidCloseTextDocumentParams{
					TextDocument: TextDocumentIdentifier{URI: oldURI},
				})
			}
			if !isTrackableTypst(b) {
				b.ClearMessages(diagnosticOwner)
				return
			}
		}
	}

	if !isTrackableTypst(b) {
		return
	}
	if !ev.AutoSave {
		m.maybeExportOnSave(b)
	}

	text := string(b.Bytes())
	ver := b.Version()
	absPath := b.AbsPath
	uri := PathToURI(absPath)

	m.mu.Lock()
	if m.shuttingDown || !m.lspEnabled {
		m.mu.Unlock()
		return
	}
	doc, ok := m.docs[newKey]
	if !ok {
		doc = &openDoc{
			key:           newKey,
			uri:           uri,
			absPath:       absPath,
			shared:        b.SharedBuffer,
			lastQueuedVer: ver,
			latestText:    text,
		}
		m.docs[newKey] = doc
	} else {
		if doc.timer != nil {
			doc.timer.Stop()
			doc.timer = nil
		}
		doc.lastQueuedVer = ver
		doc.latestText = text
	}

	if m.State() != StateReady || m.client == nil {
		m.mu.Unlock()
		return
	}

	c := m.client
	if !doc.opened {
		doc.opened = true
		doc.lastSentVer = ver
		m.mu.Unlock()
		_ = c.Notify("textDocument/didOpen", DidOpenTextDocumentParams{
			TextDocument: TextDocumentItem{
				URI:        uri,
				LanguageID: "typst",
				Version:    int(ver),
				Text:       text,
			},
		})
		_ = c.Notify("textDocument/didSave", DidSaveTextDocumentParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Text:         &text,
		})
		return
	}

	needChange := doc.lastSentVer < ver
	if needChange {
		doc.lastSentVer = ver
	}
	m.mu.Unlock()

	if needChange {
		_ = c.Notify("textDocument/didChange", DidChangeTextDocumentParams{
			TextDocument: VersionedTextDocumentIdentifier{
				URI:     uri,
				Version: int(ver),
			},
			ContentChanges: []TextDocumentContentChangeEvent{{Text: text}},
		})
	}
	_ = c.Notify("textDocument/didSave", DidSaveTextDocumentParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Text:         &text,
	})
}

// OnBufferClose sends textDocument/didClose only when the last buffer sharing
// SharedBuffer closes (!b.Shared()).
func (m *Manager) OnBufferClose(b *buffer.Buffer) {
	if b == nil || b.Shared() {
		return
	}
	if b.Path == "" || b.Type.Scratch {
		return
	}
	key := canonicalDocKey(b.AbsPath)

	m.mu.Lock()
	doc, ok := m.docs[key]
	if !ok {
		m.mu.Unlock()
		return
	}
	if doc.timer != nil {
		doc.timer.Stop()
		doc.timer = nil
	}
	delete(m.docs, key)
	delete(m.docErrorCounts, key)
	delete(m.docErrors, key)
	if m.pinnedMain == "" {
		delete(m.docDiagnostics, key)
		delete(m.docDiagnosticPaths, key)
	}

	var c *Client
	opened := doc.opened
	uri := doc.uri
	if m.State() == StateReady {
		c = m.client
	}
	m.mu.Unlock()

	if c != nil && opened {
		_ = c.Notify("textDocument/didClose", DidCloseTextDocumentParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
		})
	}
}

func isTrackableTypst(b *buffer.Buffer) bool {
	return b != nil && b.IsTypst() && b.Path != "" && !b.Type.Scratch
}

func canonicalDocKey(p string) string {
	if p == "" {
		return ""
	}
	cleaned := filepath.Clean(p)
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}

func isBufferOpenAndAt(target *buffer.Buffer, reqVersion uint64, reqLoc buffer.Loc, checkNoSel bool) bool {
	if target == nil || !target.IsTypst() || target.Version() != reqVersion {
		return false
	}
	found := false
	for _, b := range buffer.OpenBuffers {
		if b == target {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	c := target.GetActiveCursor()
	if c == nil || c.Loc != reqLoc {
		return false
	}
	if checkNoSel && c.HasSelection() {
		return false
	}
	return true
}

func isBufferStillOpenAtVersion(target *buffer.Buffer, reqVersion uint64) bool {
	if target == nil || target.Version() != reqVersion {
		return false
	}
	for _, b := range buffer.OpenBuffers {
		if b == target {
			return true
		}
	}
	return false
}

func runeColToLocX(line []byte, runeCol int) int {
	if runeCol <= 0 {
		return 0
	}
	locX := 0
	runes := 0
	off := 0
	for off < len(line) && runes < runeCol {
		_, combc, size := util.DecodeCharacter(line[off:])
		if size <= 0 {
			break
		}
		runes += 1 + len(combc)
		off += size
		if runes <= runeCol {
			locX++
		}
	}
	return locX
}

func locXToRuneCol(line []byte, locX int) int {
	if locX <= 0 {
		return 0
	}
	runes := 0
	off := 0
	for i := 0; i < locX && off < len(line); i++ {
		_, combc, size := util.DecodeCharacter(line[off:])
		if size <= 0 {
			break
		}
		runes += 1 + len(combc)
		off += size
	}
	return runes
}

func readGlobalBool(key string, def bool) bool {
	v, ok := config.GetGlobalOption(key).(bool)
	if !ok {
		return def
	}
	return v
}

func readGlobalString(key string, def string) string {
	v, ok := config.GetGlobalOption(key).(string)
	if !ok {
		return def
	}
	return v
}

func readGlobalInt(key string, def int) int {
	raw := config.GetGlobalOption(key)
	switch v := raw.(type) {
	case float64:
		if v < 0 {
			return 0
		}
		return int(v)
	case int:
		if v < 0 {
			return 0
		}
		return v
	default:
		return def
	}
}
