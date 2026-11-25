package buffer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestApplyAtomicEditsMultiLineSingleUndoRedo(t *testing.T) {
	b := NewBufferFromString("line zero\nline one\nline two\n", "test.typ", BTDefault)
	defer b.Close()

	// Reverse-sorted non-overlapping edits including multi-line replacement.
	edits := []BatchEdit{
		{
			Start: Loc{X: 0, Y: 2},
			End:   Loc{X: 8, Y: 2},
			Text:  []byte("LINE\nTWO"),
		},
		{
			Start: Loc{X: 5, Y: 0},
			End:   Loc{X: 9, Y: 0},
			Text:  []byte("ZERO\nEXTRA"),
		},
	}

	b.EventHandler.ApplyAtomicEdits(edits)
	want := "line ZERO\nEXTRA\nline one\nLINE\nTWO\n"
	assert.Equal(t, want, string(b.Bytes()))

	// Verify all pushed undo events share one exact timestamp so Undo never splits across 1s buckets.
	top := b.EventHandler.UndoStack.Peek()
	assert.NotNil(t, top)
	var sharedTime time.Time
	for cur := b.EventHandler.UndoStack.Peek(); cur != nil; {
		if sharedTime.IsZero() {
			sharedTime = cur.Time
		} else {
			assert.Equal(t, sharedTime, cur.Time)
		}
		break
	}

	// Force the UndoStack timestamp right on a 1000ms boundary edge and ensure Undo still collapses all events.
	boundary := time.Unix(1700000, 999*int64(time.Millisecond))
	var popped []*TextEvent
	for b.EventHandler.UndoStack.Len() > 0 {
		ev := b.EventHandler.UndoStack.Pop()
		ev.Time = boundary
		popped = append(popped, ev)
	}
	for i := len(popped) - 1; i >= 0; i-- {
		b.EventHandler.UndoStack.Push(popped[i])
	}

	assert.True(t, b.Undo())
	assert.Equal(t, "line zero\nline one\nline two\n", string(b.Bytes()))
	assert.Equal(t, 0, b.EventHandler.UndoStack.Len(), "entire batch must undo in a single step")

	assert.True(t, b.Redo())
	assert.Equal(t, want, string(b.Bytes()))
	assert.Equal(t, 0, b.EventHandler.RedoStack.Len(), "entire batch must redo in a single step")
}
