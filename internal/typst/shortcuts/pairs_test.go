package shortcuts

import (
	"testing"

	"github.com/monicafaye/TypX/internal/buffer"
)

func TestPairsAutoCloseAndSkipOver(t *testing.T) {
	cases := []struct {
		open  rune
		close rune
		want  string
	}{
		{'$', '$', "$$"},
		{'[', ']', "[]"},
		{'(', ')', "()"},
		{'{', '}', "{}"},
		{'"', '"', `""`},
	}

	for _, tc := range cases {
		t.Run(string(tc.open), func(t *testing.T) {
			bp := newTestBufPane(t, "", "test.typ")
			if !HandleTypstRune(bp, tc.open) {
				t.Fatalf("HandleTypstRune(%q) returned false", tc.open)
			}
			if got := bufferText(bp.Buf); got != tc.want {
				t.Fatalf("after %q got %q, want %q", tc.open, got, tc.want)
			}
			if bp.Cursor.Loc != (buffer.Loc{X: 1, Y: 0}) {
				t.Fatalf("cursor = %v, want {1 0}", bp.Cursor.Loc)
			}
			// Skip-over closing delimiter
			if !HandleTypstRune(bp, tc.close) {
				t.Fatalf("skip-over %q returned false", tc.close)
			}
			if got := bufferText(bp.Buf); got != tc.want {
				t.Fatalf("skip-over mutated buffer to %q", got)
			}
			if bp.Cursor.Loc != (buffer.Loc{X: 2, Y: 0}) {
				t.Fatalf("after skip-over cursor = %v, want {2 0}", bp.Cursor.Loc)
			}
		})
	}

	// '$' in markup immediately before "$x$" MUST NOT skip-over
	bp := newTestBufPane(t, "$x$", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 0, Y: 0})
	if !HandleTypstRune(bp, '$') {
		t.Fatal("'$' before $x$ in markup should auto-pair")
	}
	if got := bufferText(bp.Buf); got != "$$$x$" {
		t.Fatalf("'$' before $x$ = %q, want %q", got, "$$$x$")
	}
}

func TestPairsSelectionWrapping(t *testing.T) {
	for _, tc := range []struct {
		r    rune
		want string
	}{
		{'$', "$hello$"},
		{'[', "[hello]"},
		{'(', "(hello)"},
		{'{', "{hello}"},
		{'"', `"hello"`},
	} {
		bp := newTestBufPane(t, "hello", "test.typ")
		bp.Cursor.SetSelectionStart(buffer.Loc{X: 0, Y: 0})
		bp.Cursor.SetSelectionEnd(buffer.Loc{X: 5, Y: 0})
		if !HandleTypstRune(bp, tc.r) {
			t.Fatalf("selection wrap %q returned false", tc.r)
		}
		if got := bufferText(bp.Buf); got != tc.want {
			t.Fatalf("selection wrap %q = %q, want %q", tc.r, got, tc.want)
		}
		if bp.Cursor.HasSelection() {
			t.Fatalf("selection should be cleared after wrapping %q", tc.r)
		}
	}

	// Closing delimiter on selection returns false (falls back to normal replace)
	bp := newTestBufPane(t, "hello", "test.typ")
	bp.Cursor.SetSelectionStart(buffer.Loc{X: 0, Y: 0})
	bp.Cursor.SetSelectionEnd(buffer.Loc{X: 5, Y: 0})
	if HandleTypstRune(bp, ')') {
		t.Fatal("closing delimiter ')' on selection should return false")
	}
}

func TestPairsContextSuppression(t *testing.T) {
	// 1. Escaped \$ does not pair; double-backslash \\$ DOES pair
	bp := newTestBufPane(t, `\`, "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 1, Y: 0})
	if HandleTypstRune(bp, '$') {
		t.Fatal(`escaped \$ should return false`)
	}
	bp = newTestBufPane(t, `\\`, "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})
	if !HandleTypstRune(bp, '$') || bufferText(bp.Buf) != `\\$$` {
		t.Fatalf(`even backslash \\$ should pair, got %q`, bufferText(bp.Buf))
	}

	// 2. Inside ModeRaw (inline and fenced) returns false
	bp = newTestBufPane(t, "`a`", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})
	if HandleTypstRune(bp, '(') || HandleTypstRune(bp, '$') {
		t.Fatal("smart pairs should not trigger inside inline raw")
	}
	bp = newTestBufPane(t, "```\n()\n```", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 1, Y: 1})
	if HandleTypstRune(bp, '[') || HandleTypstBackspace(bp) || HandleTypstNewline(bp) {
		t.Fatal("smart pairs should not trigger inside fenced raw block")
	}

	// 3. Inside ModeCode string literal, '(' does not auto-pair
	bp = newTestBufPane(t, `#let s = "hello `, "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 16, Y: 0})
	if HandleTypstRune(bp, '(') {
		t.Fatal("'(' inside code string literal should not auto-pair")
	}

	// 4. Word-adjacent quote or before word char
	bp = newTestBufPane(t, "word", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 4, Y: 0})
	if HandleTypstRune(bp, '"') {
		t.Fatal(`'"' after word char should not auto-pair`)
	}
	bp.Cursor.GotoLoc(buffer.Loc{X: 0, Y: 0})
	if HandleTypstRune(bp, '(') {
		t.Fatal("'(' immediately before word char should not auto-pair")
	}

	// 5. Non-Typst buffer and typst.smartPairs = false
	bpTxt := newTestBufPane(t, "", "notes.txt")
	if HandleTypstRune(bpTxt, '(') || HandleTypstBackspace(bpTxt) || HandleTypstNewline(bpTxt) {
		t.Fatal("smart pairs must return false on non-Typst buffer")
	}
	bpOff := newTestBufPane(t, "()", "test.typ")
	bpOff.Buf.Settings["typst.smartPairs"] = false
	bpOff.Cursor.GotoLoc(buffer.Loc{X: 1, Y: 0})
	if HandleTypstRune(bpOff, '(') || HandleTypstBackspace(bpOff) || HandleTypstNewline(bpOff) {
		t.Fatal("smart pairs must return false when typst.smartPairs is false")
	}
}

func TestPairsBackspaceAndNewline(t *testing.T) {
	// Empty pair backspace for all 5 pairs
	for _, s := range []string{"$$", "[]", "()", "{}", `""`} {
		bp := newTestBufPane(t, s, "test.typ")
		bp.Cursor.GotoLoc(buffer.Loc{X: 1, Y: 0})
		if !HandleTypstBackspace(bp) {
			t.Fatalf("HandleTypstBackspace(%q) returned false", s)
		}
		if got := bufferText(bp.Buf); got != "" {
			t.Fatalf("after backspace(%q) got %q, want empty", s, got)
		}
		if bp.Cursor.Loc != (buffer.Loc{X: 0, Y: 0}) {
			t.Fatalf("after backspace(%q) cursor = %v, want {0 0}", s, bp.Cursor.Loc)
		}
	}

	// Non-empty pair backspace returns false
	bp := newTestBufPane(t, "(a)", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})
	if HandleTypstBackspace(bp) {
		t.Fatal("HandleTypstBackspace on non-empty pair should return false")
	}

	// Smart newline for {}, [], (), $$ with autoindent & tabsize
	for _, pair := range []string{"{}", "[]", "()", "$$"} {
		bp := newTestBufPane(t, "  "+pair, "test.typ")
		bp.Buf.Settings["tabsize"] = float64(2)
		bp.Buf.Settings["tabstospaces"] = true
		bp.Cursor.GotoLoc(buffer.Loc{X: 3, Y: 0})
		if !HandleTypstNewline(bp) {
			t.Fatalf("HandleTypstNewline(%q) returned false", pair)
		}
		want := "  " + pair[:1] + "\n    \n  " + pair[1:]
		if got := bufferText(bp.Buf); got != want {
			t.Fatalf("newline(%q) = %q, want %q", pair, got, want)
		}
		if bp.Cursor.Loc != (buffer.Loc{X: 4, Y: 1}) {
			t.Fatalf("newline(%q) cursor = %v, want {4 1}", pair, bp.Cursor.Loc)
		}
	}

	// Smart newline with tabstospaces=false and tab indent
	bp = newTestBufPane(t, "\t{}", "test.typ")
	bp.Buf.Settings["tabstospaces"] = false
	bp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})
	if !HandleTypstNewline(bp) {
		t.Fatal("tab newline returned false")
	}
	if got := bufferText(bp.Buf); got != "\t{\n\t\t\n\t}" {
		t.Fatalf("tab newline = %q", got)
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 2, Y: 1}) {
		t.Fatalf("tab newline cursor = %v, want {2 1}", bp.Cursor.Loc)
	}

	// Smart newline with autoindent=false
	bp = newTestBufPane(t, "  {}", "test.typ")
	bp.Buf.Settings["autoindent"] = false
	bp.Cursor.GotoLoc(buffer.Loc{X: 3, Y: 0})
	if !HandleTypstNewline(bp) {
		t.Fatal("autoindent=false newline returned false")
	}
	if got := bufferText(bp.Buf); got != "  {\n\n}" {
		t.Fatalf("autoindent=false newline = %q, want %q", got, "  {\n\n}")
	}

	// Smart newline does NOT expand ""
	bp = newTestBufPane(t, `""`, "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 1, Y: 0})
	if HandleTypstNewline(bp) {
		t.Fatal(`HandleTypstNewline on "" should return false`)
	}
}

func TestPairsUnicodeCombiningAndNonBMP(t *testing.T) {
	bp := newTestBufPane(t, "𝕏😀 e\u0301 ", "test.typ")
	// chars: 0:'𝕏', 1:'😀', 2:' ', 3:'e\u0301', 4:' ' -> X=5
	bp.Cursor.GotoLoc(buffer.Loc{X: 5, Y: 0})
	if !HandleTypstRune(bp, '$') {
		t.Fatal("unicode '$' pair returned false")
	}
	if got := bufferText(bp.Buf); got != "𝕏😀 e\u0301 $$" {
		t.Fatalf("got %q", got)
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 6, Y: 0}) {
		t.Fatalf("cursor = %v, want {6 0}", bp.Cursor.Loc)
	}
	if !HandleTypstBackspace(bp) {
		t.Fatal("unicode pair backspace returned false")
	}
	if got := bufferText(bp.Buf); got != "𝕏😀 e\u0301 " {
		t.Fatalf("after backspace = %q", got)
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 5, Y: 0}) {
		t.Fatalf("cursor after backspace = %v, want {5 0}", bp.Cursor.Loc)
	}
}

func TestPairsMultiCursor(t *testing.T) {
	bp := newTestBufPane(t, "a \nb ", "test.typ")
	bp.Buf.GetActiveCursor().GotoLoc(buffer.Loc{X: 2, Y: 0})
	c1 := buffer.NewCursor(bp.Buf, buffer.Loc{X: 2, Y: 1})
	c1.Num = 1
	bp.Buf.AddCursor(c1)

	if !HandleTypstRune(bp, '(') {
		t.Fatal("multi-cursor HandleTypstRune('(') returned false")
	}
	if got := bufferText(bp.Buf); got != "a ()\nb ()" {
		t.Fatalf("multi-cursor pair = %q, want %q", got, "a ()\nb ()")
	}
}

func TestSmartListContinuationAndExit(t *testing.T) {
	// 1. Bullet list continuation, nested indent, and empty-item exit
	bp := newTestBufPane(t, "  - first item", "test.typ")
	bp.Cursor.GotoLoc(buffer.Loc{X: 14, Y: 0})
	if !HandleTypstNewline(bp) {
		t.Fatal("bullet list newline returned false")
	}
	if got := bufferText(bp.Buf); got != "  - first item\n  - " {
		t.Fatalf("bullet continuation = %q", got)
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 4, Y: 1}) {
		t.Fatalf("bullet continuation cursor = %v, want {4 1}", bp.Cursor.Loc)
	}
	// Second Enter on empty item exits list cleanly
	if !HandleTypstNewline(bp) {
		t.Fatal("empty bullet exit returned false")
	}
	if got := bufferText(bp.Buf); got != "  - first item\n" {
		t.Fatalf("empty bullet exit = %q, want %q", got, "  - first item\n")
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 0, Y: 1}) {
		t.Fatalf("empty bullet exit cursor = %v, want {0 1}", bp.Cursor.Loc)
	}

	// 2. Auto-numbered (+), explicit numbered (1. -> 2.), and term list (/ Term: desc -> / )
	cases := []struct {
		name      string
		in        string
		cursorX   int
		want      string
		wantLoc   buffer.Loc
	}{
		{"plus", "+ alpha", 7, "+ alpha\n+ ", buffer.Loc{X: 2, Y: 1}},
		{"numbered", "  1. first", 10, "  1. first\n  2. ", buffer.Loc{X: 5, Y: 1}},
		{"term", "/ Typst: fast", 13, "/ Typst: fast\n/ ", buffer.Loc{X: 2, Y: 1}},
		{"mid-line split", "- foo bar", 5, "- foo\n-  bar", buffer.Loc{X: 2, Y: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bp := newTestBufPane(t, tc.in, "test.typ")
			bp.Cursor.GotoLoc(buffer.Loc{X: tc.cursorX, Y: 0})
			if !HandleTypstNewline(bp) {
				t.Fatalf("HandleTypstNewline(%q) returned false", tc.name)
			}
			if got := bufferText(bp.Buf); got != tc.want {
				t.Fatalf("%s = %q, want %q", tc.name, got, tc.want)
			}
			if bp.Cursor.Loc != tc.wantLoc {
				t.Fatalf("%s cursor = %v, want %v", tc.name, bp.Cursor.Loc, tc.wantLoc)
			}
		})
	}

	// 3. Non-list lines and mode/setting suppression
	nonList := []struct {
		name string
		in   string
		x    int
	}{
		{"en-dash", "-- heading", 10},
		{"em-dash", "---", 3},
		{"line comment", "// comment", 10},
		{"math mode", "$ - x $", 5},
		{"code mode", "#let a = 1", 10},
		{"raw fence", "```\n- item\n```", 6},
		{"cursor before marker", "- item", 0},
	}
	for _, tc := range nonList {
		t.Run("skip_"+tc.name, func(t *testing.T) {
			bp := newTestBufPane(t, tc.in, "test.typ")
			y := 0
			if tc.name == "raw fence" {
				y = 1
			}
			bp.Cursor.GotoLoc(buffer.Loc{X: tc.x, Y: y})
			if HandleTypstNewline(bp) {
				t.Fatalf("HandleTypstNewline(%q) should return false", tc.name)
			}
		})
	}

	// autoindent=false suppresses list continuation
	bpNoAI := newTestBufPane(t, "- item", "test.typ")
	bpNoAI.Buf.Settings["autoindent"] = false
	bpNoAI.Cursor.GotoLoc(buffer.Loc{X: 6, Y: 0})
	if HandleTypstNewline(bpNoAI) {
		t.Fatal("list continuation should respect autoindent=false")
	}
}
