package shortcuts

import (
	"sync"
	"testing"

	"github.com/monicafaye/TypX/internal/buffer"
)

func TestModeString(t *testing.T) {
	cases := []struct {
		mode Mode
		want string
	}{
		{ModeMarkup, "MARKUP"},
		{ModeMath, "MATH"},
		{ModeCode, "CODE"},
		{ModeRaw, "RAW"},
		{Mode(99), "MARKUP"},
	}
	for _, tc := range cases {
		if got := tc.mode.String(); got != tc.want {
			t.Errorf("Mode(%d).String() = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func TestFallbackDetectMode(t *testing.T) {
	lines := [][]byte{
		[]byte(`Hello *world* \$5 and $x + y$ end`),
		[]byte(`$`),
		[]byte(`  sum_(i=1)^n x_i // $ not closing`),
		[]byte(`  "dollar $ in str"`),
		[]byte(`$`),
		[]byte("Inline `raw $x$` done"),
		[]byte("```typ"),
		[]byte(`= $not math$`),
		[]byte("```"),
		[]byte(`/* outer /* inner $x$ */ still comment */`),
		[]byte(`#let f(a) = {`),
		[]byte(`  let s = "$not math$"`),
		[]byte(`}`),
		[]byte(`#figure([*markup inside* $z$], caption: "hi")`),
	}

	tests := []struct {
		name string
		line int
		col  int
		want Mode
	}{
		{"markup start", 0, 0, ModeMarkup},
		{"escaped dollar stays markup", 0, 17, ModeMarkup},
		{"inside inline math", 0, 23, ModeMath},
		{"after inline math", 0, 29, ModeMarkup},
		{"multiline math line 2", 2, 5, ModeMath},
		{"multiline math line 3 string", 3, 12, ModeMath},
		{"after multiline math close", 4, 1, ModeMarkup},
		{"inside inline raw", 5, 10, ModeRaw},
		{"after inline raw", 5, 16, ModeMarkup},
		{"inside fenced raw", 7, 3, ModeRaw},
		{"after fenced raw close", 8, 3, ModeMarkup},
		{"nested block comment does not enter math", 9, 25, ModeMarkup},
		{"inside #let block", 11, 5, ModeCode},
		{"after #let block closes", 12, 1, ModeMarkup},
		{"inside #figure([ ... ]) content block", 13, 10, ModeMarkup},
		{"math inside #figure([...])", 13, 26, ModeMath},
		{"code arg inside #figure(...)", 13, 35, ModeCode},
		{"empty lines slice", -1, -1, ModeMarkup},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := lines
			if tc.name == "empty lines slice" {
				in = nil
			}
			if got := DetectMode(in, tc.line, tc.col); got != tc.want {
				t.Errorf("DetectMode(line=%d, col=%d) = %s, want %s", tc.line, tc.col, got, tc.want)
			}
		})
	}
}

func TestSetModeDetectorAndConcurrency(t *testing.T) {
	t.Cleanup(func() { SetModeDetector(nil) })

	SetModeDetector(func(lines [][]byte, line, col int) Mode {
		return ModeMath
	})
	if got := DetectMode([][]byte{[]byte("plain")}, 0, 0); got != ModeMath {
		t.Fatalf("custom detector returned %s, want MATH", got)
	}

	SetModeDetector(nil)
	if got := DetectMode([][]byte{[]byte("plain")}, 0, 0); got != ModeMarkup {
		t.Fatalf("restored fallback returned %s, want MARKUP", got)
	}

	// Concurrent SetModeDetector + DetectMode stress check
	var wg sync.WaitGroup
	sample := [][]byte{[]byte("a $x$ b")}
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if id%2 == 0 {
					SetModeDetector(func(lines [][]byte, line, col int) Mode { return ModeCode })
				} else {
					SetModeDetector(nil)
				}
				_ = DetectMode(sample, 0, 3)
			}
		}(g)
	}
	wg.Wait()
}

func TestDetectModeAtLocUnicodeColumns(t *testing.T) {
	// Combining mark "e\u0301" is 1 character in micro (2 runes in Go) and non-BMP "𝕏😀" are 1 char each (4 bytes each).
	content := "𝕏😀 e\u0301 $x + α$"
	bp := newTestBufPane(t, content, "unicode.typ")

	// Character indices: 0:'𝕏', 1:'😀', 2:' ', 3:'e\u0301', 4:' ', 5:'$', 6:'x'
	if got := detectModeAtLoc(bp.Buf, buffer.Loc{X: 4, Y: 0}); got != ModeMarkup {
		t.Errorf("before '$' (char col 4) mode = %s, want MARKUP", got)
	}
	if got := detectModeAtLoc(bp.Buf, buffer.Loc{X: 6, Y: 0}); got != ModeMath {
		t.Errorf("inside '$x + α$' (char col 6) mode = %s, want MATH", got)
	}
	if got := detectModeAtLoc(bp.Buf, buffer.Loc{X: 12, Y: 0}); got != ModeMarkup {
		t.Errorf("after '$x + α$' (char col 12) mode = %s, want MARKUP", got)
	}
}
