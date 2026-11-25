package lsp

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/util"
)

type resolvedEdit struct {
	start buffer.Loc
	end   buffer.Loc
	text  []byte
}

// ApplyTextEdits converts LSP UTF-16 TextEdits against b, verifies they do not
// overlap, and applies them in reverse document order as a single atomic undo
// step. Returns the number of non-empty edits applied.
func ApplyTextEdits(b *buffer.Buffer, edits []TextEdit) (int, error) {
	if b == nil || b.EventHandler == nil {
		return 0, errors.New("buffer unavailable")
	}
	if len(edits) == 0 {
		return 0, nil
	}

	resolved := make([]resolvedEdit, 0, len(edits))
	for i, ed := range edits {
		start := lspPositionToLoc(b, ed.Range.Start)
		end := lspPositionToLoc(b, ed.Range.End)
		if end.LessThan(start) {
			return 0, fmt.Errorf("invalid text edit %d: end before start", i)
		}
		norm := strings.ReplaceAll(ed.NewText, "\r\n", "\n")
		norm = strings.ReplaceAll(norm, "\r", "")
		if start == end && norm == "" {
			continue
		}
		resolved = append(resolved, resolvedEdit{
			start: start,
			end:   end,
			text:  []byte(norm),
		})
	}
	if len(resolved) == 0 {
		return 0, nil
	}

	// Sort in reverse document order (bottom-to-top, right-to-left).
	sort.SliceStable(resolved, func(i, j int) bool {
		if resolved[i].start.Y != resolved[j].start.Y {
			return resolved[i].start.Y > resolved[j].start.Y
		}
		if resolved[i].start.X != resolved[j].start.X {
			return resolved[i].start.X > resolved[j].start.X
		}
		if resolved[i].end.Y != resolved[j].end.Y {
			return resolved[i].end.Y > resolved[j].end.Y
		}
		return resolved[i].end.X > resolved[j].end.X
	})

	for i := 0; i+1 < len(resolved); i++ {
		later := resolved[i]
		earlier := resolved[i+1]
		if later.start.LessThan(earlier.end) {
			return 0, errors.New("overlapping LSP text edits")
		}
	}

	batch := make([]buffer.BatchEdit, len(resolved))
	for i, r := range resolved {
		batch[i] = buffer.BatchEdit{
			Start: r.start,
			End:   r.end,
			Text:  r.text,
		}
	}

	b.EventHandler.ApplyAtomicEdits(batch)
	b.RelocateCursors()
	return len(batch), nil
}

func lspPositionToLoc(b *buffer.Buffer, pos Position) buffer.Loc {
	if b == nil || b.LinesNum() <= 0 || pos.Line < 0 {
		return buffer.Loc{X: 0, Y: 0}
	}
	numLines := b.LinesNum()
	if pos.Line >= numLines {
		return b.End()
	}
	lineBytes := b.LineBytes(pos.Line)
	x := runeColToLocX(lineBytes, UTF16ToRuneCol(lineBytes, pos.Character))
	maxX := util.CharacterCount(lineBytes)
	x = util.Clamp(x, 0, maxX)
	return buffer.Loc{X: x, Y: pos.Line}
}
