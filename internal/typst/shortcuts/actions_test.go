package shortcuts

import (
	"testing"

	"github.com/monicafaye/TypX/internal/buffer"
)

func TestActionsNonTypstAndReadonlyReturnFalse(t *testing.T) {
	actions := []struct {
		name string
		fn   func() bool
	}{
		{"MathInline", func() bool {
			bp := newTestBufPane(t, "== Hello", "notes.txt")
			return NewMathInlineAction()(bp)
		}},
		{"MathDisplay", func() bool {
			bp := newTestBufPane(t, "== Hello", "notes.md")
			return NewMathDisplayAction()(bp)
		}},
		{"WrapBold", func() bool {
			bp := newTestBufPane(t, "Hello", "notes.txt")
			return NewWrapBoldAction()(bp)
		}},
		{"WrapItalic", func() bool {
			bp := newTestBufPane(t, "Hello", "notes.txt")
			return NewWrapItalicAction()(bp)
		}},
		{"HeadingPromote", func() bool {
			bp := newTestBufPane(t, "== Hello", "notes.md")
			return NewHeadingPromoteAction()(bp)
		}},
		{"HeadingDemote", func() bool {
			bp := newTestBufPane(t, "= Hello", "notes.md")
			return NewHeadingDemoteAction()(bp)
		}},
		{"ReadonlyTypst", func() bool {
			bp := newTestBufPane(t, "= Hello", "doc.typ")
			bp.Buf.Type.Readonly = true
			return NewWrapBoldAction()(bp)
		}},
	}

	for _, tc := range actions {
		t.Run(tc.name, func(t *testing.T) {
			if tc.fn() {
				t.Fatalf("%s returned true on non-Typst/readonly buffer; want false for fallback", tc.name)
			}
		})
	}
}

func TestMathInlineAction(t *testing.T) {
	// 1. Insert empty $|$ and toggle off
	bp := newTestBufPane(t, "hello ", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 6, Y: 0})
	act := NewMathInlineAction()
	if !act(bp) {
		t.Fatal("MathInline returned false")
	}
	if got := bufferText(bp.Buf); got != "hello $$" {
		t.Fatalf("after insert = %q, want %q", got, "hello $$")
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 7, Y: 0}) {
		t.Fatalf("cursor = %v, want {7 0}", bp.Cursor.Loc)
	}
	// Toggle empty $|$ off
	if !act(bp) {
		t.Fatal("MathInline toggle off returned false")
	}
	if got := bufferText(bp.Buf); got != "hello " {
		t.Fatalf("after toggle off = %q, want %q", got, "hello ")
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 6, Y: 0}) {
		t.Fatalf("cursor after toggle off = %v, want {6 0}", bp.Cursor.Loc)
	}

	// 2. Step out when inside existing $a + b$
	bp = newTestBufPane(t, "let $a + b$ end", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 7, Y: 0})
	if !act(bp) {
		t.Fatal("MathInline step-out returned false")
	}
	if got := bufferText(bp.Buf); got != "let $a + b$ end" {
		t.Fatalf("step-out mutated text: %q", got)
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 11, Y: 0}) {
		t.Fatalf("step-out cursor = %v, want {11 0}", bp.Cursor.Loc)
	}

	// 3. Selection wrap and unwrap (including backward selection & non-BMP UTF-8)
	bp = newTestBufPane(t, "pre 𝕏😀 e\u0301 post", "test.typ")
	// chars: 0..3:"pre ", 4:'𝕏', 5:'😀', 6:' ', 7:'e\u0301', 8:' '
	bp.Cursor.SetSelectionStart(buffer.Loc{X: 8, Y: 0})
	bp.Cursor.SetSelectionEnd(buffer.Loc{X: 4, Y: 0}) // backward selection covering "𝕏😀 e\u0301"
	if !act(bp) {
		t.Fatal("MathInline wrap selection returned false")
	}
	if got := bufferText(bp.Buf); got != "pre $𝕏😀 e\u0301$ post" {
		t.Fatalf("wrapped = %q, want %q", got, "pre $𝕏😀 e\u0301$ post")
	}
	if bp.Cursor.HasSelection() {
		t.Fatal("selection should be cleared after wrap")
	}

	// Select "$𝕏😀 e\u0301$" and unwrap
	bp.Cursor.SetSelectionStart(buffer.Loc{X: 4, Y: 0})
	bp.Cursor.SetSelectionEnd(buffer.Loc{X: 10, Y: 0})
	if !act(bp) {
		t.Fatal("MathInline unwrap selection returned false")
	}
	if got := bufferText(bp.Buf); got != "pre 𝕏😀 e\u0301 post" {
		t.Fatalf("unwrapped = %q, want %q", got, "pre 𝕏😀 e\u0301 post")
	}
}

func TestMathDisplayAction(t *testing.T) {
	act := NewMathDisplayAction()

	// 1. Indentation-aware display block (spaces)
	bp := newTestBufPane(t, "  ", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})
	if !act(bp) {
		t.Fatal("MathDisplay returned false")
	}
	want := "  $\n    \n  $"
	if got := bufferText(bp.Buf); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 4, Y: 1}) {
		t.Fatalf("cursor = %v, want {4 1}", bp.Cursor.Loc)
	}

	// 2. Tab indentation (tabstospaces = false)
	bp = newTestBufPane(t, "\t", "test.typ")
	bp.Buf.Settings["tabstospaces"] = false
	bp.Cursor.GotoLoc(buffer.Loc{X: 1, Y: 0})
	if !act(bp) {
		t.Fatal("MathDisplay tab indent returned false")
	}
	wantTab := "\t$\n\t\t\n\t$"
	if got := bufferText(bp.Buf); got != wantTab {
		t.Fatalf("got %q, want %q", got, wantTab)
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 2, Y: 1}) {
		t.Fatalf("cursor = %v, want {2 1}", bp.Cursor.Loc)
	}

	// 3. Toggle inline $x+y$ <-> display $ x+y $
	bp = newTestBufPane(t, "$x+y$", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})
	if !act(bp) {
		t.Fatal("MathDisplay toggle inline->display returned false")
	}
	if got := bufferText(bp.Buf); got != "$ x+y $" {
		t.Fatalf("inline->display = %q, want %q", got, "$ x+y $")
	}
	if !act(bp) {
		t.Fatal("MathDisplay toggle display->inline returned false")
	}
	if got := bufferText(bp.Buf); got != "$x+y$" {
		t.Fatalf("display->inline = %q, want %q", got, "$x+y$")
	}

	// 4. Selection wrap & unwrap
	bp = newTestBufPane(t, "  alpha + beta", "test.typ")
	bp.Cursor.SetSelectionStart(buffer.Loc{X: 2, Y: 0})
	bp.Cursor.SetSelectionEnd(buffer.Loc{X: 14, Y: 0})
	if !act(bp) {
		t.Fatal("MathDisplay selection wrap returned false")
	}
	if got := bufferText(bp.Buf); got != "  $\n    alpha + beta\n  $" {
		t.Fatalf("selection wrap = %q", got)
	}
	bp.Cursor.SetSelectionStart(buffer.Loc{X: 2, Y: 0})
	bp.Cursor.SetSelectionEnd(buffer.Loc{X: 3, Y: 2})
	if !act(bp) {
		t.Fatal("MathDisplay selection unwrap returned false")
	}
	if got := bufferText(bp.Buf); got != "  alpha + beta" {
		t.Fatalf("selection unwrap = %q, want %q", got, "  alpha + beta")
	}
}

func TestWrapBoldAndItalicActions(t *testing.T) {
	bold := NewWrapBoldAction()
	italic := NewWrapItalicAction()

	// Empty insert & toggle off
	bp := newTestBufPane(t, "hi ", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 3, Y: 0})
	if !bold(bp) || bufferText(bp.Buf) != "hi **" || bp.Cursor.Loc != (buffer.Loc{X: 4, Y: 0}) {
		t.Fatalf("bold insert failed: %q cursor=%v", bufferText(bp.Buf), bp.Cursor.Loc)
	}
	if !bold(bp) || bufferText(bp.Buf) != "hi " || bp.Cursor.Loc != (buffer.Loc{X: 3, Y: 0}) {
		t.Fatalf("bold toggle off failed: %q cursor=%v", bufferText(bp.Buf), bp.Cursor.Loc)
	}

	// Selection wrap + unwrap with Unicode
	bp = newTestBufPane(t, "a 𝕏😀 b", "test.typ")
	bp.Cursor.SetSelectionStart(buffer.Loc{X: 2, Y: 0})
	bp.Cursor.SetSelectionEnd(buffer.Loc{X: 4, Y: 0})
	if !italic(bp) {
		t.Fatal("italic wrap failed")
	}
	if got := bufferText(bp.Buf); got != "a _𝕏😀_ b" {
		t.Fatalf("italic wrap = %q, want %q", got, "a _𝕏😀_ b")
	}
	// Cursor inside _𝕏😀_ without selection unwraps it
	bp.Cursor.GotoLoc(buffer.Loc{X: 4, Y: 0})
	if !italic(bp) {
		t.Fatal("italic unwrap inside failed")
	}
	if got := bufferText(bp.Buf); got != "a 𝕏😀 b" {
		t.Fatalf("italic unwrap inside = %q, want %q", got, "a 𝕏😀 b")
	}

	// Snake_case identifier should NOT unwrap when cursor is inside foo_bar_baz
	bp = newTestBufPane(t, "foo_bar_baz", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 5, Y: 0})
	if !italic(bp) {
		t.Fatal("italic inside snake_case failed")
	}
	if got := bufferText(bp.Buf); got != "foo_b__ar_baz" {
		t.Fatalf("snake_case should insert __ rather than unwrapping identifier, got %q", got)
	}
}

func TestHeadingPromoteAndDemote(t *testing.T) {
	promote := NewHeadingPromoteAction()
	demote := NewHeadingDemoteAction()

	// Non-heading line returns false so "|" fallback works
	bp := newTestBufPane(t, "Plain paragraph\n=notheading", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 3, Y: 0})
	if promote(bp) || demote(bp) {
		t.Fatal("expected false on non-heading line")
	}
	bp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 1})
	if promote(bp) || demote(bp) {
		t.Fatal("expected false on '=notheading' without space")
	}

	// Heading inside fenced raw block returns false
	bp = newTestBufPane(t, "```\n== Not a heading\n```", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 5, Y: 1})
	if promote(bp) || demote(bp) {
		t.Fatal("expected false inside fenced raw block")
	}

	// Promote / demote with boundaries 1..6 and leading whitespace + UTF-8
	bp = newTestBufPane(t, "  = 中文 𝕏😀", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 7, Y: 0})

	// Promote at level 1 clamps at 1
	if !promote(bp) {
		t.Fatal("promote at level 1 should return true (clamped)")
	}
	if got := bufferText(bp.Buf); got != "  = 中文 𝕏😀" {
		t.Fatalf("promote at level 1 changed text: %q", got)
	}

	// Demote 1 -> 2 -> ... -> 6 -> clamped at 6
	for level := 2; level <= 6; level++ {
		if !demote(bp) {
			t.Fatalf("demote to %d returned false", level)
		}
	}
	if got := bufferText(bp.Buf); got != "  ====== 中文 𝕏😀" {
		t.Fatalf("after demote to 6: %q", got)
	}
	if !demote(bp) {
		t.Fatal("demote at level 6 should return true (clamped)")
	}
	if got := bufferText(bp.Buf); got != "  ====== 中文 𝕏😀" {
		t.Fatalf("demote past 6 changed text: %q", got)
	}

	// Promote 6 -> 5
	if !promote(bp) {
		t.Fatal("promote 6->5 returned false")
	}
	if got := bufferText(bp.Buf); got != "  ===== 中文 𝕏😀" {
		t.Fatalf("after promote 6->5: %q", got)
	}
}

func TestMultiCursorActions(t *testing.T) {
	bp := newTestBufPane(t, "alpha\nbeta", "test.typ")
	c0 := bp.Buf.GetActiveCursor()
	c0.SetSelectionStart(buffer.Loc{X: 0, Y: 0})
	c0.SetSelectionEnd(buffer.Loc{X: 5, Y: 0})

	c1 := buffer.NewCursor(bp.Buf, buffer.Loc{X: 0, Y: 1})
	c1.Num = 1
	c1.SetSelectionStart(buffer.Loc{X: 0, Y: 1})
	c1.SetSelectionEnd(buffer.Loc{X: 4, Y: 1})
	bp.Buf.AddCursor(c1)

	if !NewWrapBoldAction()(bp) {
		t.Fatal("multi-cursor WrapBold returned false")
	}
	if got := bufferText(bp.Buf); got != "*alpha*\n*beta*" {
		t.Fatalf("multi-cursor WrapBold = %q, want %q", got, "*alpha*\n*beta*")
	}

	// Multi-cursor heading demote (two cursors on same heading line deduplicated + one on another)
	bp = newTestBufPane(t, "= One\n== Two", "test.typ")
	bp.Buf.GetActiveCursor().GotoLoc(buffer.Loc{X: 0, Y: 0})
	cSame := buffer.NewCursor(bp.Buf, buffer.Loc{X: 3, Y: 0})
	cSame.Num = 1
	bp.Buf.AddCursor(cSame)
	cNext := buffer.NewCursor(bp.Buf, buffer.Loc{X: 4, Y: 1})
	cNext.Num = 2
	bp.Buf.AddCursor(cNext)

	if !NewHeadingDemoteAction()(bp) {
		t.Fatal("multi-cursor HeadingDemote returned false")
	}
	if got := bufferText(bp.Buf); got != "== One\n=== Two" {
		t.Fatalf("multi-cursor HeadingDemote = %q, want %q", got, "== One\n=== Two")
	}
}

func TestShortcutCommands(t *testing.T) {
	bp := newTestBufPane(t, "== Heading\nword", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 0, Y: 0})
	NewHeadingPromoteCmd()(bp, nil)
	if got := string(bp.Buf.LineBytes(0)); got != "= Heading" {
		t.Fatalf(":heading-promote = %q, want %q", got, "= Heading")
	}
	NewHeadingDemoteCmd()(bp, nil)
	if got := string(bp.Buf.LineBytes(0)); got != "== Heading" {
		t.Fatalf(":heading-demote = %q, want %q", got, "== Heading")
	}

	bp.Cursor.SetSelectionStart(buffer.Loc{X: 0, Y: 1})
	bp.Cursor.SetSelectionEnd(buffer.Loc{X: 4, Y: 1})
	NewWrapBoldCmd()(bp, nil)
	if got := string(bp.Buf.LineBytes(1)); got != "*word*" {
		t.Fatalf(":wrap-bold = %q, want %q", got, "*word*")
	}

	bp.Cursor.SetSelectionStart(buffer.Loc{X: 0, Y: 1})
	bp.Cursor.SetSelectionEnd(buffer.Loc{X: 6, Y: 1})
	NewWrapItalicCmd()(bp, nil)
	if got := string(bp.Buf.LineBytes(1)); got != "_*word*_" {
		t.Fatalf(":wrap-italic = %q, want %q", got, "_*word*_")
	}

	bp.Cursor.GotoLoc(buffer.Loc{X: 8, Y: 1})
	NewMathInlineCmd()(bp, nil)
	if got := string(bp.Buf.LineBytes(1)); got != "_*word*_$$" {
		t.Fatalf(":math-inline = %q, want %q", got, "_*word*_$$")
	}
	NewMathDisplayCmd()(bp, nil)
	if got := bufferText(bp.Buf); got != "== Heading\n_*word*_$\n  \n$" {
		t.Fatalf(":math-display = %q", got)
	}
}
