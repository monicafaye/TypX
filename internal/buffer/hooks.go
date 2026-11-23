package buffer

import (
	"sync"
)

// ChangeEvent describes all text mutations to one document since the last
// OnBufferChange delivery for a Buffer.
type ChangeEvent struct {
	// Version is b.Version() at delivery time; strictly increasing per SharedBuffer.
	Version uint64
	// StartLine is the smallest 0-based line touched since the last delivery.
	// Lines [0, StartLine) are guaranteed unchanged; lines >= StartLine may have
	// changed or shifted.
	StartLine int
}

// SaveEvent describes a completed buffer save.
type SaveEvent struct {
	// AutoSave is true when triggered by periodic autosave rather than an explicit save.
	AutoSave bool
	// PrevAbsPath is the buffer's AbsPath before this save; it differs from
	// b.AbsPath after a "save as" to a new path.
	PrevAbsPath string
}

// CursorEvent describes an active-cursor movement on a Buffer.
type CursorEvent struct {
	Old Loc
	New Loc
}

// Observer receives buffer lifecycle notifications.
//
// Frozen contract (Phase 2/3):
//   - Every method runs synchronously on the MAIN goroutine. Keep it fast (no I/O);
//     hand heavy work to a background goroutine and marshal UI mutations back via
//     action.EnqueueUI.
//   - Fires for every Buffer (including help, log, and info prompt buffers).
//     Filter with b.IsTypst() or b.Type.
//   - Buffers sharing a file (e.g. same file open in two splits) share a *SharedBuffer.
//     Each Buffer receives its own Open/Change/Close; key per-document state on
//     b.SharedBuffer or b.AbsPath, and check b.Shared() inside OnBufferClose to tell
//     whether the document is still open in another pane.
//   - Per-document ordering: Open -> Change* -> (Save | Change)* -> Close.
//     Pending changes are always flushed BEFORE OnBufferSave and OnBufferClose
//     (including rmtrailingws/eofnewline edits made during save).
//   - OnBufferChange and OnCursorMove are coalesced: at most one of each per Buffer
//     per main-loop iteration. OnBufferChange is always delivered before OnCursorMove.
//   - OnBufferOpen fires at the end of NewBuffer, after the Buffer is in OpenBuffers
//     and its filetype/settings are resolved, but before any BufPane exists.
//   - OnBufferClose is NOT guaranteed to fire at process exit (e.g. SIGTERM/panic);
//     use lsp.Shutdown or exit-time cleanup for process teardown.
//   - Observers may mutate buffers. A mutation made inside OnBufferChange is delivered
//     on the next FlushObservers cycle, never recursively.
type Observer interface {
	OnBufferOpen(b *Buffer)
	OnBufferChange(b *Buffer, ev ChangeEvent)
	OnBufferSave(b *Buffer, ev SaveEvent)
	OnBufferClose(b *Buffer)
	OnCursorMove(b *Buffer, ev CursorEvent)
}

// NopObserver is a no-op Observer implementation that can be embedded so an
// observer only needs to implement the lifecycle methods it cares about.
type NopObserver struct{}

func (NopObserver) OnBufferOpen(*Buffer)                {}
func (NopObserver) OnBufferChange(*Buffer, ChangeEvent) {}
func (NopObserver) OnBufferSave(*Buffer, SaveEvent)     {}
func (NopObserver) OnBufferClose(*Buffer)               {}
func (NopObserver) OnCursorMove(*Buffer, CursorEvent)   {}

type observerEntry struct {
	id  uint64
	obs Observer
}

var (
	observersMu    sync.RWMutex
	observers      []observerEntry
	nextObserverID uint64
)

// RegisterObserver registers o to receive buffer lifecycle events in
// registration order. Safe to call from any goroutine (copy-on-write under a
// mutex). Register during initialization (before LoadInput) so OnBufferOpen
// observes the initially opened buffers.
//
// The returned unregister function removes o and is safe to call multiple times,
// including from inside an Observer callback.
func RegisterObserver(o Observer) (unregister func()) {
	if o == nil {
		return func() {}
	}

	observersMu.Lock()
	nextObserverID++
	id := nextObserverID
	next := make([]observerEntry, len(observers)+1)
	copy(next, observers)
	next[len(observers)] = observerEntry{id: id, obs: o}
	observers = next
	observersMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			observersMu.Lock()
			defer observersMu.Unlock()
			for i, e := range observers {
				if e.id == id {
					next := make([]observerEntry, len(observers)-1)
					copy(next, observers[:i])
					copy(next[i:], observers[i+1:])
					observers = next
					return
				}
			}
		})
	}
}

func snapshotObservers() []observerEntry {
	observersMu.RLock()
	s := observers
	observersMu.RUnlock()
	return s
}

// Version returns the monotonically increasing mutation counter for this
// SharedBuffer. Safe to read from any goroutine (atomic), though buffer text
// itself may only be accessed on the main goroutine.
func (b *SharedBuffer) Version() uint64 {
	return b.version.Load()
}

// IsTypst reports whether this buffer's resolved filetype is "typst".
// Main goroutine only.
func (b *Buffer) IsTypst() bool {
	if b == nil || b.Settings == nil {
		return false
	}
	ft, ok := b.Settings["filetype"].(string)
	return ok && ft == "typst"
}

func (b *SharedBuffer) recordChange(startLine int) {
	if startLine < 0 {
		startLine = 0
	}
	b.version.Add(1)
	for _, buf := range OpenBuffers {
		if buf == nil || buf.closed || buf.SharedBuffer != b {
			continue
		}
		if !buf.hasPendingChange || startLine < buf.pendingStartLine {
			buf.pendingStartLine = startLine
			buf.hasPendingChange = true
		}
	}
}

func (b *Buffer) activeCursorLoc() Loc {
	if len(b.cursors) == 0 {
		return Loc{0, 0}
	}
	c := b.GetActiveCursor()
	if c == nil {
		return Loc{0, 0}
	}
	return c.Loc
}

func (b *Buffer) initObserverState() {
	b.hasPendingChange = false
	b.pendingStartLine = 0
	b.lastCursor = b.activeCursorLoc()
	b.cursorInitialized = true
}

func (b *Buffer) flushPendingChange() {
	if !b.hasPendingChange {
		return
	}
	ev := ChangeEvent{
		Version:   b.Version(),
		StartLine: b.pendingStartLine,
	}
	b.hasPendingChange = false
	b.pendingStartLine = 0
	for _, e := range snapshotObservers() {
		e.obs.OnBufferChange(b, ev)
	}
}

func (b *Buffer) flushCursorMove() {
	cur := b.activeCursorLoc()
	if !b.cursorInitialized {
		b.lastCursor = cur
		b.cursorInitialized = true
		return
	}
	if cur == b.lastCursor {
		return
	}
	ev := CursorEvent{Old: b.lastCursor, New: cur}
	b.lastCursor = cur
	for _, e := range snapshotObservers() {
		e.obs.OnCursorMove(b, ev)
	}
}

func notifyBufferOpen(b *Buffer) {
	for _, e := range snapshotObservers() {
		e.obs.OnBufferOpen(b)
	}
}

func notifyBufferSave(b *Buffer, ev SaveEvent) {
	for _, e := range snapshotObservers() {
		e.obs.OnBufferSave(b, ev)
	}
}

func notifyBufferClose(b *Buffer) {
	for _, e := range snapshotObservers() {
		e.obs.OnBufferClose(b)
	}
}

// FlushObservers delivers coalesced OnBufferChange and OnCursorMove events
// for all currently open buffers. OnBufferChange is always delivered before
// OnCursorMove. Called once per main-loop iteration (and directly in tests).
func FlushObservers() {
	if len(OpenBuffers) == 0 {
		return
	}

	bufs := make([]*Buffer, len(OpenBuffers))
	copy(bufs, OpenBuffers)

	for _, b := range bufs {
		if b == nil || b.closed {
			continue
		}
		b.flushPendingChange()
	}

	for _, b := range bufs {
		if b == nil || b.closed {
			continue
		}
		b.flushCursorMove()
	}
}
