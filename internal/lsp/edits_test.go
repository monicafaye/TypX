package lsp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/buffer"
)

func TestApplyTextEditsSingleMultiLineUTF16AndEOF(t *testing.T) {
	setupLSPEnv(t, 0)
	b := buffer.NewBufferFromString("a😀b\n=   Heading\nlast", "edit.typ", buffer.BTDefault)
	defer b.Close()

	// Unsorted edits:
	// 1. Line 0: replace "b" after surrogate emoji "😀" (UTF-16 col 3..4) with "X\r\nY"
	// 2. Line 1: collapse "=   Heading" spaces (col 1..4) -> " "
	// 3. EOF sentinel (Line 3, Char 0) -> "\n"
	edits := []TextEdit{
		{
			Range:   Range{Start: Position{Line: 0, Character: 3}, End: Position{Line: 0, Character: 4}},
			NewText: "X\r\nY",
		},
		{
			Range:   Range{Start: Position{Line: 3, Character: 0}, End: Position{Line: 3, Character: 0}},
			NewText: "\n",
		},
		{
			Range:   Range{Start: Position{Line: 1, Character: 1}, End: Position{Line: 1, Character: 4}},
			NewText: " ",
		},
	}

	n, err := ApplyTextEdits(b, edits)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, "a😀X\nY\n= Heading\nlast\n", string(b.Bytes()))

	// Single Ctrl+Z undo restores exact original buffer.
	assert.True(t, b.Undo())
	assert.Equal(t, "a😀b\n=   Heading\nlast", string(b.Bytes()))
	assert.Equal(t, 0, b.EventHandler.UndoStack.Len())

	// Single Redo restores formatted buffer.
	assert.True(t, b.Redo())
	assert.Equal(t, "a😀X\nY\n= Heading\nlast\n", string(b.Bytes()))
}

func TestApplyTextEditsRejectsOverlappingAndEmpty(t *testing.T) {
	setupLSPEnv(t, 0)
	b := buffer.NewBufferFromString("abcdef\n", "overlap.typ", buffer.BTDefault)
	defer b.Close()

	n, err := ApplyTextEdits(b, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	n, err = ApplyTextEdits(b, []TextEdit{
		{Range: Range{Start: Position{Line: 0, Character: 1}, End: Position{Line: 0, Character: 4}}, NewText: "X"},
		{Range: Range{Start: Position{Line: 0, Character: 3}, End: Position{Line: 0, Character: 5}}, NewText: "Y"},
	})
	require.Error(t, err)
	assert.Equal(t, 0, n)
	assert.Equal(t, "abcdef\n", string(b.Bytes()))
}
