package shortcuts

import (
	"runtime"
	"strings"
	"testing"

	"github.com/micro-editor/tcell/v2"
	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
)

func TestRegisterAllWiringFallbacksAndDuplicatePanic(t *testing.T) {
	RegisterAll()
	action.InitBindings()

	expectedActions := []string{
		"MathInline",
		"MathDisplay",
		"WrapBold",
		"WrapItalic",
		"HeadingPromote",
		"HeadingDemote",
		"InsertPalette",
		"SymbolPicker",
	}
	for _, name := range expectedActions {
		if _, ok := action.BufKeyActions[name]; !ok {
			t.Errorf("action %q missing from action.BufKeyActions after RegisterAll()", name)
		}
	}

	defaults := action.DefaultBindings("buffer")
	expectedBindings := map[string]string{
		"Alt-m":    "MathInline|SpawnMultiCursorSelect",
		"Alt-M":    "MathDisplay",
		"Alt-i":    "InsertPalette",
		"Alt-y":    "SymbolPicker",
		"Alt-b":    "WrapBold|WordLeft",
		"Alt-e":    "WrapItalic|EndOfLine",
		"AltLeft":  "HeadingPromote|",
		"AltRight": "HeadingDemote|",
	}
	if runtime.GOOS == "darwin" {
		expectedBindings["AltLeft"] += "WordLeft"
		expectedBindings["AltRight"] += "WordRight"
	} else {
		expectedBindings["AltLeft"] += "StartOfTextToggle"
		expectedBindings["AltRight"] += "EndOfLine"
	}
	for key, want := range expectedBindings {
		if got := defaults[key]; got != want {
			t.Errorf("DefaultBindings(\"buffer\")[%q] = %q, want %q", key, got, want)
		}
	}

	// Execute registered colon commands via HandleCommand
	bp := newTestBufPane(t, "hi", "doc.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})
	bp.HandleCommand("math-inline")
	if got := bufferText(bp.Buf); got != "hi$$" {
		t.Fatalf(":math-inline command failed: %q", got)
	}
	bp.HandleCommand("insert-frac")
	if got := bufferText(bp.Buf); got != "hi$frac(, )$" {
		t.Fatalf(":insert-frac command failed: %q", got)
	}

	// Verify "|" fallback on non-Typst buffer: Alt-b runs WordLeft without mutating text
	bpTxt := newTestBufPane(t, "hello world", "notes.txt")
	bpTxt.Cursor.GotoLoc(buffer.Loc{X: 11, Y: 0})
	bpTxt.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'b', tcell.ModAlt, ""))
	if got := bufferText(bpTxt.Buf); got != "hello world" {
		t.Fatalf("Alt-b on .txt mutated buffer: %q", got)
	}
	if bpTxt.Cursor.X >= 11 {
		t.Fatalf("Alt-b fallback WordLeft did not move cursor left: X=%d", bpTxt.Cursor.X)
	}

	// Verify Alt-b on .typ runs WrapBold
	bpTyp := newTestBufPane(t, "hello", "notes.typ")
	bpTyp.Cursor.SetSelectionStart(buffer.Loc{X: 0, Y: 0})
	bpTyp.Cursor.SetSelectionEnd(buffer.Loc{X: 5, Y: 0})
	bpTyp.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'b', tcell.ModAlt, ""))
	if got := bufferText(bpTyp.Buf); got != "*hello*" {
		t.Fatalf("Alt-b on .typ = %q, want %q", got, "*hello*")
	}

	// Verify AltLeft fallback on non-heading .typ line moves cursor without editing
	bpNonHead := newTestBufPane(t, "  hello", "notes.typ")
	bpNonHead.Cursor.GotoLoc(buffer.Loc{X: 5, Y: 0})
	bpNonHead.HandleEvent(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModAlt, ""))
	if got := bufferText(bpNonHead.Buf); got != "  hello" {
		t.Fatalf("AltLeft on non-heading mutated text: %q", got)
	}
	if bpNonHead.Cursor.X == 5 {
		t.Fatalf("AltLeft fallback did not move cursor from X=5")
	}

	// Verify Alt-y and :symbol invoke registered SymbolPicker / symbol command
	var symbolCalls int
	SetPicker(func(_ *action.BufPane, title, badge, initialQuery string, _ []PickerItem) bool {
		symbolCalls++
		if title != "Math Symbols" || badge != "SYMBOLS" {
			t.Fatalf("unexpected symbol picker header: %q %q", title, badge)
		}
		return true
	})
	t.Cleanup(func() { SetPicker(nil) })
	bpSym := newTestBufPane(t, "", "sym.typ")
	bpSym.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'y', tcell.ModAlt, ""))
	bpSym.HandleCommand("symbol alpha")
	if symbolCalls != 2 {
		t.Fatalf("expected Alt-y and :symbol to invoke picker twice, got %d", symbolCalls)
	}

	// Duplicate RegisterAll() must panic loudly per extension.go contract
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected duplicate RegisterAll() to panic")
		}
		if !strings.Contains(r.(string), "duplicate") {
			t.Fatalf("unexpected panic message: %v", r)
		}
	}()
	RegisterAll()
}
