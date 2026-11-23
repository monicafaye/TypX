package typst

import (
	"fmt"
	"testing"
)

func TestModeStringAndSliceSourceBounds(t *testing.T) {
	if ModeMarkup.String() != "MARKUP" {
		t.Fatalf("ModeMarkup.String() = %q, want MARKUP", ModeMarkup.String())
	}
	if ModeMath.String() != "MATH" {
		t.Fatalf("ModeMath.String() = %q, want MATH", ModeMath.String())
	}
	if ModeCode.String() != "CODE" {
		t.Fatalf("ModeCode.String() = %q, want CODE", ModeCode.String())
	}
	if ModeRaw.String() != "RAW" {
		t.Fatalf("ModeRaw.String() = %q, want RAW", ModeRaw.String())
	}
	if Mode(99).String() != "UNKNOWN" {
		t.Fatalf("Mode(99).String() = %q, want UNKNOWN", Mode(99).String())
	}

	src := SliceSource{[]byte("hello"), []byte("$x$")}
	if src.LinesNum() != 2 {
		t.Fatalf("LinesNum() = %d, want 2", src.LinesNum())
	}
	if src.LineBytes(-1) != nil || src.LineBytes(2) != nil {
		t.Fatalf("expected nil for out-of-bounds LineBytes")
	}

	if DetectModeAt(nil, 0, 0) != ModeMarkup {
		t.Fatalf("expected ModeMarkup for nil source")
	}
	if DetectModeAt(SliceSource{}, 0, 0) != ModeMarkup {
		t.Fatalf("expected ModeMarkup for empty source")
	}
	if DetectModeAt(src, -1, 0) != ModeMarkup || DetectModeAt(src, 5, 0) != ModeMarkup {
		t.Fatalf("expected ModeMarkup for out-of-bounds line")
	}
	if DetectModeAt(src, 1, -5) != ModeMarkup {
		t.Fatalf("expected ModeMarkup for clamped negative col before $")
	}
	if DetectModeAt(src, 1, 99) != ModeMarkup {
		t.Fatalf("expected ModeMarkup for clamped EOL col after $x$")
	}
}

func TestInlineAndDisplayMath(t *testing.T) {
	src := SliceSource{
		[]byte("See $a+b$ and $ x + y $ and $$ end"),
	}
	st, spans := ScanLineState(LineState{}, src[0])
	if st.InMath || st.InRaw || st.CommentDepth != 0 || st.CodeDepth != 0 {
		t.Fatalf("unexpected ending LineState: %+v", st)
	}
	if len(spans) != 3 {
		t.Fatalf("len(spans) = %d, want 3: %+v", len(spans), spans)
	}
	want := []MathSpan{
		{Start: 4, End: 9, ContentStart: 5, ContentEnd: 8, Display: false},
		{Start: 14, End: 23, ContentStart: 15, ContentEnd: 22, Display: true},
		{Start: 28, End: 30, ContentStart: 29, ContentEnd: 29, Display: false},
	}
	for i, w := range want {
		if spans[i] != w {
			t.Errorf("span[%d] = %+v, want %+v", i, spans[i], w)
		}
	}

	// Cursor semantics inside $$ (cols 28, 29, 30)
	if m := DetectModeAt(src, 0, 28); m != ModeMarkup {
		t.Errorf("col 28 (before $$) = %v, want MARKUP", m)
	}
	if m := DetectModeAt(src, 0, 29); m != ModeMath {
		t.Errorf("col 29 (inside $|$) = %v, want MATH", m)
	}
	if m := DetectModeAt(src, 0, 30); m != ModeMarkup {
		t.Errorf("col 30 (after $$) = %v, want MARKUP", m)
	}

	// Inline math cursor bounds on $a+b$ (cols 4..9)
	if m := DetectModeAt(src, 0, 4); m != ModeMarkup {
		t.Errorf("col 4 = %v, want MARKUP", m)
	}
	for c := 5; c <= 8; c++ {
		if m := DetectModeAt(src, 0, c); m != ModeMath {
			t.Errorf("col %d = %v, want MATH", c, m)
		}
	}
	if m := DetectModeAt(src, 0, 9); m != ModeMarkup {
		t.Errorf("col 9 = %v, want MARKUP", m)
	}
}

func TestMathEscapesAndMultiline(t *testing.T) {
	escSrc := SliceSource{
		[]byte(`Cost \$100 and \\$x + \$ + y$ done`),
	}
	_, spans := ScanLineState(LineState{}, escSrc[0])
	if len(spans) != 1 {
		t.Fatalf("len(spans) = %d, want 1: %+v", len(spans), spans)
	}
	if spans[0] != (MathSpan{Start: 17, End: 29, ContentStart: 18, ContentEnd: 28, Display: false}) {
		t.Fatalf("unexpected span: %+v", spans[0])
	}

	multi := SliceSource{
		[]byte("before $"),
		[]byte("  alpha + beta"),
		[]byte("$ after"),
	}
	st0, sp0 := ScanLineState(LineState{}, multi[0])
	if !st0.InMath || len(sp0) != 1 || !sp0[0].Display || sp0[0].Start != 7 || sp0[0].ContentStart != 8 || sp0[0].ContentEnd != 8 {
		t.Fatalf("line 0 state=%+v spans=%+v", st0, sp0)
	}
	st1, sp1 := ScanLineState(st0, multi[1])
	if !st1.InMath || len(sp1) != 1 || !sp1[0].Display || sp1[0].Start != 0 || sp1[0].End != 14 {
		t.Fatalf("line 1 state=%+v spans=%+v", st1, sp1)
	}
	st2, sp2 := ScanLineState(st1, multi[2])
	if st2.InMath || len(sp2) != 1 || !sp2[0].Display || sp2[0].Start != 0 || sp2[0].ContentEnd != 0 || sp2[0].End != 1 {
		t.Fatalf("line 2 state=%+v spans=%+v", st2, sp2)
	}

	if m := DetectModeAt(multi, 0, 7); m != ModeMarkup {
		t.Errorf("(0,7) = %v, want MARKUP", m)
	}
	if m := DetectModeAt(multi, 0, 8); m != ModeMath {
		t.Errorf("(0,8) = %v, want MATH", m)
	}
	if m := DetectModeAt(multi, 1, 0); m != ModeMath {
		t.Errorf("(1,0) = %v, want MATH", m)
	}
	if m := DetectModeAt(multi, 2, 0); m != ModeMath {
		t.Errorf("(2,0) = %v, want MATH", m)
	}
	if m := DetectModeAt(multi, 2, 1); m != ModeMarkup {
		t.Errorf("(2,1) = %v, want MARKUP", m)
	}
}

func TestRawBlocksAndInlineRaw(t *testing.T) {
	src := SliceSource{
		[]byte("text `` `a $b$ /* c */` \\` $ok$"),
		[]byte("````typst"),
		[]byte("```not closing"),
		[]byte("$still raw$"),
		[]byte("``````"),
		[]byte("#let s = `raw` + 1"),
	}
	var st LineState
	var spans []MathSpan
	st, spans = ScanLineState(st, src[0])
	if st.InRaw || len(spans) != 1 || spans[0].Start != 27 || spans[0].End != 31 {
		t.Fatalf("line 0 st=%+v spans=%+v", st, spans)
	}
	if m := DetectModeAt(src, 0, 6); m != ModeRaw {
		t.Errorf("(0,6) = %v, want RAW", m)
	}
	if m := DetectModeAt(src, 0, 9); m != ModeRaw {
		t.Errorf("(0,9) = %v, want RAW", m)
	}
	if m := DetectModeAt(src, 0, 22); m != ModeRaw {
		t.Errorf("(0,22) = %v, want RAW", m)
	}
	if m := DetectModeAt(src, 0, 23); m != ModeMarkup {
		t.Errorf("(0,23) = %v, want MARKUP", m)
	}

	st, _ = ScanLineState(st, src[1])
	if !st.InRaw || st.RawFenceLen != 4 {
		t.Fatalf("line 1 st=%+v, want InRaw=true, RawFenceLen=4", st)
	}
	st, _ = ScanLineState(st, src[2])
	if !st.InRaw {
		t.Fatalf("line 2 should stay InRaw because 3 < 4")
	}
	st, spans = ScanLineState(st, src[3])
	if !st.InRaw || len(spans) != 0 {
		t.Fatalf("line 3 should stay InRaw with 0 math spans")
	}
	if m := DetectModeAt(src, 3, 4); m != ModeRaw {
		t.Errorf("(3,4) = %v, want RAW", m)
	}
	st, _ = ScanLineState(st, src[4])
	if st.InRaw || st.RawFenceLen != 0 {
		t.Fatalf("line 4 should close raw block, got %+v", st)
	}

	// Raw inside #let
	if m := DetectModeAt(src, 5, 11); m != ModeRaw {
		t.Errorf("(5,11) = %v, want RAW", m)
	}
	if m := DetectModeAt(src, 5, 16); m != ModeCode {
		t.Errorf("(5,16) = %v, want CODE", m)
	}
}

func TestCommentsAndURLs(t *testing.T) {
	src := SliceSource{
		[]byte("Visit https://typst.app $x$ // $y$"),
		[]byte("/* outer /* inner $x$ */ still"),
		[]byte("comment */ $z$"),
		[]byte("$ a /* $ */ b $"),
	}
	st, sp0 := ScanLineState(LineState{}, src[0])
	if st.CommentDepth != 0 || len(sp0) != 1 || sp0[0].Start != 24 || sp0[0].End != 27 {
		t.Fatalf("line 0 st=%+v sp=%+v", st, sp0)
	}
	st, sp1 := ScanLineState(st, src[1])
	if st.CommentDepth != 1 || len(sp1) != 0 {
		t.Fatalf("line 1 st=%+v sp=%+v", st, sp1)
	}
	st, sp2 := ScanLineState(st, src[2])
	if st.CommentDepth != 0 || len(sp2) != 1 || sp2[0].Start != 11 || sp2[0].End != 14 {
		t.Fatalf("line 2 st=%+v sp=%+v", st, sp2)
	}
	st, sp3 := ScanLineState(st, src[3])
	if st.InMath || st.CommentDepth != 0 || len(sp3) != 1 || sp3[0].Start != 0 || sp3[0].End != 15 {
		t.Fatalf("line 3 st=%+v sp=%+v", st, sp3)
	}
}

func TestStringsInCodeMathAndMarkup(t *testing.T) {
	src := SliceSource{
		[]byte(`#let s = "$ /* //" + "\""`),
		[]byte(`$ x = "a $ b /*" + y $`),
		[]byte(`She said "$alpha$"!`),
	}
	_, sp0 := ScanLineState(LineState{}, src[0])
	if len(sp0) != 0 {
		t.Fatalf("expected 0 math spans in code string, got %+v", sp0)
	}
	_, sp1 := ScanLineState(LineState{}, src[1])
	if len(sp1) != 1 || sp1[0].Start != 0 || sp1[0].End != 22 {
		t.Fatalf("unexpected math span with string: %+v", sp1)
	}
	_, sp2 := ScanLineState(LineState{}, src[2])
	if len(sp2) != 1 || sp2[0].Start != 10 || sp2[0].End != 17 {
		t.Fatalf("expected math inside markup quotes, got %+v", sp2)
	}
}

func TestCodeAndContentSwitching(t *testing.T) {
	src := SliceSource{
		[]byte("#let x = 1; markup"),
		[]byte("#if cond [hi] else [there] after"),
		[]byte("#sym.arrow.r and #x. and #emph[bold] text"),
		[]byte("#figure("),
		[]byte(`  image("a.png"),`),
		[]byte("  caption: ["),
		[]byte("    See $alpha$"),
		[]byte("  ],"),
		[]byte(") done"),
	}

	cases := []struct {
		line, col int
		want      Mode
	}{
		{0, 0, ModeMarkup},
		{0, 1, ModeCode},
		{0, 10, ModeCode},
		{0, 11, ModeMarkup},
		{1, 9, ModeCode},
		{1, 10, ModeMarkup},
		{1, 13, ModeCode},
		{1, 19, ModeCode},
		{1, 20, ModeMarkup},
		{1, 26, ModeMarkup},
		{2, 1, ModeCode},
		{2, 11, ModeCode},
		{2, 12, ModeMarkup},
		{2, 18, ModeCode},
		{2, 19, ModeMarkup},
		{2, 30, ModeCode},
		{2, 31, ModeMarkup},
		{2, 36, ModeMarkup},
		{4, 2, ModeCode},
		{6, 4, ModeMarkup},
		{6, 9, ModeMath},
		{7, 3, ModeCode},
		{8, 0, ModeCode},
		{8, 1, ModeMarkup},
	}

	for _, tc := range cases {
		if got := DetectModeAt(src, tc.line, tc.col); got != tc.want {
			t.Errorf("DetectModeAt(%d, %d) = %v, want %v (line=%q)", tc.line, tc.col, got, tc.want, string(src[tc.line]))
		}
	}
}

func TestUnicodeCombiningMarksAndEmojiColumns(t *testing.T) {
	// 'e' + U+0301 = 1 char cluster, '😀' (4-byte UTF-8) = 1 char cluster
	line := []byte("e\u0301 😀 $α + β$ ok")
	// Cols: 0='é', 1=' ', 2='😀', 3=' ', 4='$', 5='α', 6=' ', 7='+', 8=' ', 9='β', 10='$', 11=' ', 12='o', 13='k'
	_, spans := ScanLineState(LineState{}, line)
	if len(spans) != 1 {
		t.Fatalf("len(spans) = %d, want 1", len(spans))
	}
	want := MathSpan{Start: 4, End: 11, ContentStart: 5, ContentEnd: 10, Display: false}
	if spans[0] != want {
		t.Fatalf("span = %+v, want %+v", spans[0], want)
	}

	src := SliceSource{line}
	if m := DetectModeAt(src, 0, 4); m != ModeMarkup {
		t.Errorf("col 4 = %v, want MARKUP", m)
	}
	if m := DetectModeAt(src, 0, 5); m != ModeMath {
		t.Errorf("col 5 = %v, want MATH", m)
	}
	if m := DetectModeAt(src, 0, 10); m != ModeMath {
		t.Errorf("col 10 = %v, want MATH", m)
	}
	if m := DetectModeAt(src, 0, 11); m != ModeMarkup {
		t.Errorf("col 11 = %v, want MARKUP", m)
	}
}

func BenchmarkDetectModeAt20kLines(b *testing.B) {
	lines := make(SliceSource, 20000)
	for i := range lines {
		switch i % 4 {
		case 0:
			lines[i] = []byte(fmt.Sprintf("= Heading %d with $alpha + beta$ and -- dash", i))
		case 1:
			lines[i] = []byte("#let val = 42 // comment")
		case 2:
			lines[i] = []byte("Regular Typst paragraph with `inline` code.")
		case 3:
			lines[i] = []byte("$ sum_(i=1)^n i^2 = (n(n+1)(2n+1))/6 $")
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if m := DetectModeAt(lines, len(lines)-1, 5); m != ModeMath {
			b.Fatalf("unexpected mode: %v", m)
		}
	}
}
