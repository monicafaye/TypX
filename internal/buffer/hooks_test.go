package buffer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/monicafaye/TypX/internal/config"
)

type recordedEvent struct {
	kind    string
	buf     *Buffer
	change  ChangeEvent
	save    SaveEvent
	cursor  CursorEvent
	shared  bool
	isTypst bool
}

type recordingObserver struct {
	events   []recordedEvent
	onChange func(b *Buffer, ev ChangeEvent)
}

func (r *recordingObserver) OnBufferOpen(b *Buffer) {
	r.events = append(r.events, recordedEvent{kind: "open", buf: b, isTypst: b.IsTypst()})
}

func (r *recordingObserver) OnBufferChange(b *Buffer, ev ChangeEvent) {
	r.events = append(r.events, recordedEvent{kind: "change", buf: b, change: ev})
	if r.onChange != nil {
		r.onChange(b, ev)
	}
}

func (r *recordingObserver) OnBufferSave(b *Buffer, ev SaveEvent) {
	r.events = append(r.events, recordedEvent{kind: "save", buf: b, save: ev, isTypst: b.IsTypst()})
}

func (r *recordingObserver) OnBufferClose(b *Buffer) {
	r.events = append(r.events, recordedEvent{kind: "close", buf: b, shared: b.Shared()})
}

func (r *recordingObserver) OnCursorMove(b *Buffer, ev CursorEvent) {
	r.events = append(r.events, recordedEvent{kind: "cursor", buf: b, cursor: ev})
}

func TestObserverOpenChangeCoalesceAndClose(t *testing.T) {
	CloseOpenBuffers()
	rec := &recordingObserver{}
	unreg := RegisterObserver(rec)
	defer unreg()

	b := NewBufferFromString("line0\nline1\nline2\nline3", "doc.typ", BTDefault)
	require.Equal(t, uint64(0), b.Version())
	require.Len(t, rec.events, 1)
	assert.Equal(t, "open", rec.events[0].kind)
	assert.True(t, rec.events[0].isTypst)
	assert.True(t, b.IsTypst())

	// Multiple edits in one frame coalesce into a single ChangeEvent with min StartLine.
	b.Insert(Loc{0, 3}, "a")
	b.Insert(Loc{0, 1}, "b")
	b.Insert(Loc{0, 2}, "c")
	assert.Equal(t, uint64(3), b.Version())

	FlushObservers()
	require.Len(t, rec.events, 2)
	assert.Equal(t, "change", rec.events[1].kind)
	assert.Equal(t, uint64(3), rec.events[1].change.Version)
	assert.Equal(t, 1, rec.events[1].change.StartLine)

	// Second flush with no new mutations emits nothing.
	FlushObservers()
	assert.Len(t, rec.events, 2)

	// Undo, Redo, and Retab all trigger OnBufferChange.
	b.Undo()
	FlushObservers()
	require.Len(t, rec.events, 3)
	assert.Equal(t, "change", rec.events[2].kind)
	assert.Greater(t, rec.events[2].change.Version, uint64(3))

	vBeforeRedo := b.Version()
	b.Redo()
	FlushObservers()
	require.Len(t, rec.events, 4)
	assert.Equal(t, "change", rec.events[3].kind)
	assert.Greater(t, rec.events[3].change.Version, vBeforeRedo)

	vBeforeRetab := b.Version()
	b.Retab()
	FlushObservers()
	require.Len(t, rec.events, 5)
	assert.Equal(t, "change", rec.events[4].kind)
	assert.Equal(t, 0, rec.events[4].change.StartLine)
	assert.Greater(t, rec.events[4].change.Version, vBeforeRetab)

	// Close flushes pending changes first and fires OnBufferClose once.
	b.Insert(Loc{0, 0}, "x")
	b.Close()
	b.Close() // idempotent
	require.Len(t, rec.events, 7)
	assert.Equal(t, "change", rec.events[5].kind)
	assert.Equal(t, "close", rec.events[6].kind)
	assert.False(t, rec.events[6].shared)
}

func TestObserverSharedBuffersAndCloseOpenBuffers(t *testing.T) {
	CloseOpenBuffers()
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.typ")
	require.NoError(t, os.WriteFile(path, []byte("= Title\nbody\n"), 0644))

	rec := &recordingObserver{}
	unreg := RegisterObserver(rec)
	defer unreg()

	b1, err := NewBufferFromFile(path, BTDefault)
	require.NoError(t, err)
	b2, err := NewBufferFromFile(path, BTDefault)
	require.NoError(t, err)
	assert.Same(t, b1.SharedBuffer, b2.SharedBuffer)

	rec.events = nil
	b1.Insert(Loc{0, 1}, "more ")
	FlushObservers()

	// Both buffers sharing the SharedBuffer receive OnBufferChange.
	changeBufs := []*Buffer{}
	for _, ev := range rec.events {
		if ev.kind == "change" {
			changeBufs = append(changeBufs, ev.buf)
		}
	}
	assert.Equal(t, []*Buffer{b1, b2}, changeBufs)

	// Closing b1 sees b1.Shared() == true; closing remaining via CloseOpenBuffers sees false.
	rec.events = nil
	b1.Close()
	require.Len(t, rec.events, 1)
	assert.Equal(t, "close", rec.events[0].kind)
	assert.True(t, rec.events[0].shared)

	rec.events = nil
	CloseOpenBuffers()
	require.Len(t, rec.events, 1)
	assert.Equal(t, "close", rec.events[0].kind)
	assert.Equal(t, b2, rec.events[0].buf)
	assert.False(t, rec.events[0].shared)
}

func TestObserverSaveOrderingAndPrevAbsPath(t *testing.T) {
	CloseOpenBuffers()
	dir := t.TempDir()
	origCfg := config.ConfigDir
	config.ConfigDir = dir
	t.Cleanup(func() { config.ConfigDir = origCfg })

	p1 := filepath.Join(dir, "draft.txt")
	p2 := filepath.Join(dir, "final.typ")

	rec := &recordingObserver{}
	unreg := RegisterObserver(rec)
	defer unreg()

	b := NewBufferFromString("hello", p1, BTDefault)
	b.Settings["savecursor"] = false
	b.Settings["saveundo"] = false
	defer b.Close()

	rec.events = nil
	b.Insert(Loc{5, 0}, " world")
	require.NoError(t, b.Save())

	// Pending change (plus eofnewline if enabled) must be flushed BEFORE OnBufferSave.
	require.Len(t, rec.events, 2)
	assert.Equal(t, "change", rec.events[0].kind)
	assert.Equal(t, "save", rec.events[1].kind)
	assert.False(t, rec.events[1].save.AutoSave)
	assert.Equal(t, b.AbsPath, rec.events[1].save.PrevAbsPath)

	// SaveAs to a .typ path reports old AbsPath in PrevAbsPath and resolves filetype to typst.
	oldAbs := b.AbsPath
	rec.events = nil
	require.NoError(t, b.SaveAs(p2))
	require.Len(t, rec.events, 1)
	assert.Equal(t, "save", rec.events[0].kind)
	assert.Equal(t, oldAbs, rec.events[0].save.PrevAbsPath)
	assert.NotEqual(t, oldAbs, b.AbsPath)
	assert.True(t, rec.events[0].isTypst)
}

func TestObserverCursorMoveOrderingAndCoalescing(t *testing.T) {
	CloseOpenBuffers()
	rec := &recordingObserver{}
	unreg := RegisterObserver(rec)
	defer unreg()

	b := NewBufferFromString("hello\nworld\n", "c.typ", BTDefault)
	defer b.Close()
	rec.events = nil

	// Move cursor multiple times in one frame -> single coalesced CursorEvent.
	b.GetActiveCursor().GotoLoc(Loc{2, 0})
	b.GetActiveCursor().GotoLoc(Loc{3, 1})
	FlushObservers()
	require.Len(t, rec.events, 1)
	assert.Equal(t, "cursor", rec.events[0].kind)
	assert.Equal(t, Loc{0, 0}, rec.events[0].cursor.Old)
	assert.Equal(t, Loc{3, 1}, rec.events[0].cursor.New)

	// No movement -> no event.
	rec.events = nil
	FlushObservers()
	assert.Empty(t, rec.events)

	// Simultaneous edit + cursor move -> OnBufferChange strictly before OnCursorMove.
	b.Insert(Loc{3, 1}, "!")
	b.GetActiveCursor().GotoLoc(Loc{4, 1})
	FlushObservers()
	require.Len(t, rec.events, 2)
	assert.Equal(t, "change", rec.events[0].kind)
	assert.Equal(t, "cursor", rec.events[1].kind)
	assert.Equal(t, Loc{3, 1}, rec.events[1].cursor.Old)
	assert.Equal(t, Loc{4, 1}, rec.events[1].cursor.New)
}

func TestObserverNonRecursiveMutationAndUnregister(t *testing.T) {
	CloseOpenBuffers()
	depth := 0
	maxDepth := 0
	mutatedOnce := false

	rec := &recordingObserver{}
	rec.onChange = func(b *Buffer, ev ChangeEvent) {
		depth++
		if depth > maxDepth {
			maxDepth = depth
		}
		if !mutatedOnce {
			mutatedOnce = true
			b.Insert(Loc{0, 0}, "!")
		}
		depth--
	}
	unreg := RegisterObserver(rec)

	b := NewBufferFromString("abc", "r.typ", BTDefault)
	defer b.Close()
	rec.events = nil

	countKind := func(kind string) int {
		n := 0
		for _, ev := range rec.events {
			if ev.kind == kind {
				n++
			}
		}
		return n
	}

	b.Insert(Loc{0, 0}, "x")
	FlushObservers()
	assert.Equal(t, 1, maxDepth, "OnBufferChange must not recurse")
	assert.Equal(t, 1, countKind("change"))

	// Mutation made inside OnBufferChange arrives on the next FlushObservers cycle.
	FlushObservers()
	assert.Equal(t, 2, countKind("change"))

	// Unregister stops delivery and is idempotent.
	unreg()
	unreg()
	rec.events = nil
	b.Insert(Loc{0, 0}, "y")
	FlushObservers()
	assert.Empty(t, rec.events)
}

func TestTypstBufferDefaultsAppliedOnOpen(t *testing.T) {
	CloseOpenBuffers()
	b := NewBufferFromString("= Hello\n", "paper.typ", BTDefault)
	defer b.Close()

	assert.True(t, b.IsTypst())
	assert.Equal(t, true, b.Settings["softwrap"])
	assert.Equal(t, true, b.Settings["wordwrap"])
	assert.Equal(t, true, b.Settings["tabstospaces"])
	assert.Equal(t, float64(2), b.Settings["tabsize"])
}
