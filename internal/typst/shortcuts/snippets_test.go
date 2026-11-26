package shortcuts

import (
	"testing"

	"github.com/monicafaye/TypX/internal/buffer"
)

func TestParseSnippetSyntax(t *testing.T) {
	// Placeholders, choices, escapes, and stop ordering (1..N then 0)
	p := ParseSnippet(`#table(columns: ${1:2}, align: ${2|left,center,right|}, price: \$5, ${3:body})$0`)
	wantText := `#table(columns: 2, align: left, price: $5, body)`
	if p.Text != wantText {
		t.Fatalf("Text = %q, want %q", p.Text, wantText)
	}
	if len(p.Stops) != 4 {
		t.Fatalf("Stops len = %d, want 4 (%+v)", len(p.Stops), p.Stops)
	}
	if p.Stops[0].Index != 1 || p.Text[p.Stops[0].StartChar:p.Stops[0].EndChar] != "2" {
		t.Fatalf("stop[0] = %+v", p.Stops[0])
	}
	if p.Stops[1].Index != 2 || p.Text[p.Stops[1].StartChar:p.Stops[1].EndChar] != "left" {
		t.Fatalf("stop[1] = %+v", p.Stops[1])
	}
	if p.Stops[2].Index != 3 || p.Text[p.Stops[2].StartChar:p.Stops[2].EndChar] != "body" {
		t.Fatalf("stop[2] = %+v", p.Stops[2])
	}
	if p.Stops[3].Index != 0 || p.Stops[3].StartChar != len(wantText) {
		t.Fatalf("stop[3] = %+v", p.Stops[3])
	}

	// Implicit $0 appended at end when positive stops omit $0
	p2 := ParseSnippet(`fn(${1:a}, ${2:b})`)
	if len(p2.Stops) != 3 || p2.Stops[2].Index != 0 || p2.Stops[2].StartChar != len(p2.Text) {
		t.Fatalf("implicit $0 stops = %+v", p2.Stops)
	}
}

func TestSnippetTabstopNavigationAndEditing(t *testing.T) {
	bp := newTestBufPane(t, "", "test.typ")
	next := NewSnippetNextAction()
	prev := NewSnippetPrevAction()

	// Expand :insert frac -> frac($1, $2)$0
	NewInsertCmd()(bp, []string{"frac"})
	if !HasActiveSnippetSession(bp.Buf) {
		t.Fatal("expected active snippet session after :insert frac")
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 5, Y: 0}) {
		t.Fatalf("initial cursor = %v, want {5 0}", bp.Cursor.Loc)
	}

	// Type numerator "a + b", then Tab to denominator ($2)
	bp.Buf.Insert(bp.Cursor.Loc, "a + b")
	if !next(bp) {
		t.Fatal("SnippetNext to $2 returned false")
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 12, Y: 0}) {
		t.Fatalf("cursor at $2 = %v, want {12 0}", bp.Cursor.Loc)
	}

	// Type denominator "c", Shift+Tab back to numerator (selects "a + b")
	bp.Buf.Insert(bp.Cursor.Loc, "c")
	if !prev(bp) {
		t.Fatal("SnippetPrev back to $1 returned false")
	}
	if string(bp.Cursor.GetSelection()) != "a + b" {
		t.Fatalf("selection after Shift+Tab = %q, want %q", string(bp.Cursor.GetSelection()), "a + b")
	}

	// Tab forward to $2 (selects "c"), then Tab to $0 (ends session)
	if !next(bp) {
		t.Fatal("SnippetNext to $2 returned false")
	}
	if string(bp.Cursor.GetSelection()) != "c" {
		t.Fatalf("selection at $2 = %q, want %q", string(bp.Cursor.GetSelection()), "c")
	}
	if !next(bp) {
		t.Fatal("SnippetNext to $0 returned false")
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 14, Y: 0}) {
		t.Fatalf("cursor at $0 = %v, want {14 0}", bp.Cursor.Loc)
	}
	if HasActiveSnippetSession(bp.Buf) {
		t.Fatal("session should end upon reaching final empty $0 stop")
	}
	if next(bp) {
		t.Fatal("SnippetNext should return false once session ended")
	}
}

func TestSnippetPlaceholderSelectionEscapeAndFallbackGuards(t *testing.T) {
	bp := newTestBufPane(t, "prefix ", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 7, Y: 0})
	next := NewSnippetNextAction()

	if !ExpandSnippetAt(bp, `table(columns: ${1:2}, ${2:body})$0`) {
		t.Fatal("ExpandSnippetAt returned false")
	}
	if string(bp.Cursor.GetSelection()) != "2" {
		t.Fatalf("first placeholder selection = %q, want %q", string(bp.Cursor.GetSelection()), "2")
	}

	// While autocomplete suggestions are open, SnippetNext must yield to Autocomplete
	bp.Buf.HasSuggestions = true
	if next(bp) {
		t.Fatal("SnippetNext must return false while HasSuggestions is true")
	}
	bp.Buf.HasSuggestions = false

	// Replace selected placeholder with multi-line text, then Tab to $2
	bp.Cursor.DeleteSelection()
	bp.Buf.Insert(bp.Cursor.Loc, "3,\n  gutter: 4pt")
	if !next(bp) {
		t.Fatal("SnippetNext after multi-line edit returned false")
	}
	if string(bp.Cursor.GetSelection()) != "body" {
		t.Fatalf("second placeholder selection = %q, want %q", string(bp.Cursor.GetSelection()), "body")
	}

	// Escape clears active session without blocking micro's Escape/Deselect chain
	if HandleSnippetEscape(bp) {
		t.Fatal("HandleSnippetEscape should return false so Deselect runs")
	}
	if HasActiveSnippetSession(bp.Buf) {
		t.Fatal("session should be cleared after Escape")
	}

	// Moving cursor outside active stop auto-cancels stale session on Tab
	bp.Cursor.GotoLoc(buffer.Loc{X: 0, Y: 0})
	ExpandSnippetAt(bp, `f($1, $2)$0`)
	bp.Cursor.GotoLoc(buffer.Loc{X: 0, Y: 0})
	if next(bp) {
		t.Fatal("SnippetNext should cancel and return false when cursor moved outside active stop")
	}
	if HasActiveSnippetSession(bp.Buf) {
		t.Fatal("out-of-bounds Tab should clear stale session")
	}
}
