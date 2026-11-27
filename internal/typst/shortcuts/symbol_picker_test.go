package shortcuts

import (
	"strings"
	"testing"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
)

func TestSymbolPickerActionAndCommand(t *testing.T) {
	t.Cleanup(func() {
		SetPicker(nil)
		if action.InfoBar != nil {
			if action.InfoBar.HasPrompt {
				action.InfoBar.AbortCommand()
			}
			action.InfoBar.Msg = ""
			action.InfoBar.HasError = false
		}
	})

	// Non-Typst and readonly return false
	bpTxt := newTestBufPane(t, "hello", "notes.txt")
	if NewSymbolPickerAction()(bpTxt) {
		t.Fatal("SymbolPicker should return false on non-Typst buffer")
	}
	bpRO := newTestBufPane(t, "hello", "doc.typ")
	bpRO.Buf.Type.Readonly = true
	if NewSymbolPickerAction()(bpRO) {
		t.Fatal("SymbolPicker should return false on readonly buffer")
	}

	var gotTitle, gotBadge, gotQuery string
	var gotItems []PickerItem
	SetPicker(func(bp *action.BufPane, title, badge, initialQuery string, items []PickerItem) bool {
		gotTitle = title
		gotBadge = badge
		gotQuery = initialQuery
		gotItems = items
		return true
	})

	bp := newTestBufPane(t, "", "doc.typ")
	if !NewSymbolPickerAction()(bp) {
		t.Fatal("SymbolPicker returned false on Typst buffer")
	}
	if gotTitle != "Math Symbols" || gotBadge != "SYMBOLS" || gotQuery != "" {
		t.Fatalf("picker header = (%q, %q, %q)", gotTitle, gotBadge, gotQuery)
	}
	if len(gotItems) < 150 {
		t.Fatalf("expected full symbol catalog in picker, got %d items", len(gotItems))
	}

	var foundSubset bool
	for _, it := range gotItems {
		if it.Primary == "subset.eq" {
			foundSubset = true
			if it.Badge != "sets" || it.Secondary != "⊆" {
				t.Fatalf("subset.eq item = %+v", it)
			}
		}
	}
	if !foundSubset {
		t.Fatal("subset.eq missing from symbol picker items")
	}

	// :symbol query forwards initialQuery to picker
	NewSymbolCmd()(bp, []string{"subset", "eq"})
	if gotQuery != "subset eq" {
		t.Fatalf(":symbol initialQuery = %q, want %q", gotQuery, "subset eq")
	}
}

func TestSymbolPickerModeAwareInsertion(t *testing.T) {
	t.Cleanup(func() { SetPicker(nil) })

	pickSymbol := func(bp *action.BufPane, name string) {
		t.Helper()
		SetPicker(func(_ *action.BufPane, _, _, _ string, items []PickerItem) bool {
			for _, it := range items {
				if it.Primary == name {
					it.OnSelect()
					return true
				}
			}
			t.Fatalf("symbol %q not found in picker", name)
			return false
		})
		if !NewSymbolPickerAction()(bp) {
			t.Fatalf("NewSymbolPickerAction failed for %q", name)
		}
	}

	// 1. Markup mode wraps in $...$ and places cursor after closing $
	bpMarkup := newTestBufPane(t, "Given ", "doc.typ")
	bpMarkup.Cursor.GotoLoc(buffer.Loc{X: 6, Y: 0})
	pickSymbol(bpMarkup, "subset.eq")
	if got := bufferText(bpMarkup.Buf); got != "Given $subset.eq$" {
		t.Fatalf("markup insert = %q", got)
	}
	if bpMarkup.Cursor.Loc != (buffer.Loc{X: 17, Y: 0}) {
		t.Fatalf("markup cursor = %v, want {17 0}", bpMarkup.Cursor.Loc)
	}

	// 2. Math mode inserts raw identifier without $...$
	bpMath := newTestBufPane(t, "$A  B$", "doc.typ")
	bpMath.Cursor.GotoLoc(buffer.Loc{X: 3, Y: 0})
	pickSymbol(bpMath, "subset.eq")
	if got := bufferText(bpMath.Buf); got != "$A subset.eq B$" {
		t.Fatalf("math insert = %q", got)
	}
	if bpMath.Cursor.Loc != (buffer.Loc{X: 12, Y: 0}) {
		t.Fatalf("math cursor = %v, want {12 0}", bpMath.Cursor.Loc)
	}

	// 3. Code mode wraps in $...$
	bpCode := newTestBufPane(t, "#let rel = ", "doc.typ")
	bpCode.Cursor.GotoLoc(buffer.Loc{X: 11, Y: 0})
	pickSymbol(bpCode, "alpha")
	if got := bufferText(bpCode.Buf); got != "#let rel = $alpha$" {
		t.Fatalf("code insert = %q", got)
	}
	if bpCode.Cursor.Loc != (buffer.Loc{X: 18, Y: 0}) {
		t.Fatalf("code cursor = %v, want {18 0}", bpCode.Cursor.Loc)
	}

	// 4. Raw mode inserts raw identifier without $...$
	bpRaw := newTestBufPane(t, "`raw `", "doc.typ")
	bpRaw.Cursor.GotoLoc(buffer.Loc{X: 5, Y: 0})
	pickSymbol(bpRaw, "alpha")
	if got := bufferText(bpRaw.Buf); got != "`raw alpha`" {
		t.Fatalf("raw insert = %q", got)
	}

	// 5. Active selection replacement
	bpSel := newTestBufPane(t, "replace me here", "doc.typ")
	bpSel.Cursor.SetSelectionStart(buffer.Loc{X: 8, Y: 0})
	bpSel.Cursor.SetSelectionEnd(buffer.Loc{X: 10, Y: 0})
	pickSymbol(bpSel, "infinity")
	if got := bufferText(bpSel.Buf); got != "replace $infinity$ here" {
		t.Fatalf("selection insert = %q", got)
	}
	if bpSel.Cursor.HasSelection() || bpSel.Cursor.Loc != (buffer.Loc{X: 18, Y: 0}) {
		t.Fatalf("selection cursor = %v hasSel=%v", bpSel.Cursor.Loc, bpSel.Cursor.HasSelection())
	}

	// 6. Multi-byte and combining Unicode prefix offsets
	bpUni := newTestBufPane(t, "𝕏😀 e\u0301 ", "doc.typ")
	bpUni.Cursor.GotoLoc(buffer.Loc{X: 5, Y: 0})
	pickSymbol(bpUni, "subset.eq")
	if got := bufferText(bpUni.Buf); got != "𝕏😀 e\u0301 $subset.eq$" {
		t.Fatalf("unicode insert = %q", got)
	}
	if bpUni.Cursor.Loc != (buffer.Loc{X: 16, Y: 0}) {
		t.Fatalf("unicode cursor = %v, want {16 0}", bpUni.Cursor.Loc)
	}

	// 7. Multi-cursor across Markup (line 0) and Math (line 1)
	bpMC := newTestBufPane(t, "text \n$x $", "doc.typ")
	bpMC.Buf.GetActiveCursor().GotoLoc(buffer.Loc{X: 5, Y: 0})
	c1 := buffer.NewCursor(bpMC.Buf, buffer.Loc{X: 3, Y: 1})
	c1.Num = 1
	bpMC.Buf.AddCursor(c1)
	pickSymbol(bpMC, "alpha")
	if got := bufferText(bpMC.Buf); got != "text $alpha$\n$x alpha$" {
		t.Fatalf("multi-cursor insert = %q, want %q", got, "text $alpha$\n$x alpha$")
	}

	// 8. Inserting symbol inside an active snippet tabstop preserves snippet session
	bpSnip := newTestBufPane(t, "", "doc.typ")
	if !ExpandSnippetAt(bpSnip, "fn($1, $2)$0") {
		t.Fatal("ExpandSnippetAt failed")
	}
	pickSymbol(bpSnip, "alpha")
	if !HasActiveSnippetSession(bpSnip.Buf) {
		t.Fatal("symbol insertion should not clear active snippet session")
	}
	if got := bufferText(bpSnip.Buf); got != "fn($alpha$, )" {
		t.Fatalf("snippet + symbol = %q", got)
	}
}

func TestSymbolPickerInfoBarFallback(t *testing.T) {
	t.Cleanup(func() {
		SetPicker(nil)
		if action.InfoBar != nil {
			if action.InfoBar.HasPrompt {
				action.InfoBar.AbortCommand()
			}
			action.InfoBar.Msg = ""
			action.InfoBar.HasError = false
		}
	})

	SetPicker(nil)
	bp := newTestBufPane(t, "", "doc.typ")
	NewSymbolCmd()(bp, []string{"subset.eq"})
	if !action.InfoBar.HasPrompt {
		t.Fatal("expected InfoBar prompt fallback when picker is nil")
	}
	action.InfoBar.ExecuteCommand()
	if got := bufferText(bp.Buf); got != "$subset.eq$" {
		t.Fatalf("InfoBar fallback symbol = %q, want %q", got, "$subset.eq$")
	}

	// Unknown symbol reports error without mutating buffer
	NewSymbolCmd()(bp, []string{"not_a_real_symbol_xyz"})
	action.InfoBar.ExecuteCommand()
	if !action.InfoBar.HasError || !strings.Contains(action.InfoBar.Msg, "Unknown Typst symbol") {
		t.Fatalf("expected Unknown Typst symbol error, got HasError=%v Msg=%q", action.InfoBar.HasError, action.InfoBar.Msg)
	}
	if got := bufferText(bp.Buf); got != "$subset.eq$" {
		t.Fatalf("unknown symbol mutated buffer: %q", got)
	}
}
