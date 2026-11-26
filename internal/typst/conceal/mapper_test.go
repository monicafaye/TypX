package conceal

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/monicafaye/TypX/internal/typst"
	"github.com/monicafaye/TypX/internal/util"
)

func scanOne(line string, activeCols []int) *LineView {
	b := []byte(line)
	st, spans := typst.ScanLineState(typst.LineState{}, b)
	_ = st
	return ScanLine(b, typst.LineState{}, spans, activeCols)
}

func TestLookupSymbolAndTables(t *testing.T) {
	cases := []struct {
		pat  string
		want rune
	}{
		{"alpha", 'α'},
		{"epsilon", 'ε'},
		{"epsilon.alt", 'ϵ'},
		{"Omega", 'Ω'},
		{"->", '→'},
		{"<-->", '⟷'},
		{"approx", '≈'},
		{"dot", '·'},
		{"plus.minus", '±'},
		{"infinity", '∞'},
		{"oo", '∞'},
		{"sum", '∑'},
		{"integral.double", '∬'},
		{"^2", '²'},
		{"^n", 'ⁿ'},
		{"_i", 'ᵢ'},
		{"_j", 'ⱼ'},
		{"---", '—'},
		{"--", '–'},
		{"...", '…'},
	}
	for _, tc := range cases {
		got, ok := LookupSymbol(tc.pat)
		if !ok || got != tc.want {
			t.Errorf("LookupSymbol(%q) = (%q, %v), want (%q, true)", tc.pat, got, ok, tc.want)
		}
	}
	if _, ok := LookupSymbol("sin"); ok {
		t.Errorf("LookupSymbol(\"sin\") should be false")
	}
}

func TestWordBoundariesAndLongestMatch(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"untouched_words", "$sin(x) + index + summary + alpha1 + #in_set + dots1$", "$sin(x) + index + summary + alpha1 + #in_set + dots1$"},
		{"dot_prefix_suffix", "$foo.in + dots.foo + alpha.$", "$foo.in + dots.foo + α.$"},
		{"concealed_words", "$alpha + in + sum + dot + dots + plus.minus$", "$α + ∈ + ∑ + · + … + ±$"},
		{"longest_arrows", "$<--> <==> <-> <=> --> <-- ==> <== -> <- =>$", "$⟷ ⟺ ↔ ⇔ ⟶ ⟵ ⟹ ⟸ → ← ⇒$"},
		{"longest_relations", "$<= >= != ~= notin in in.not in.rev in.rev.not subseteq subset.eq.not subset.neq subset.not subset integral.double integral dots.c$", "$≤ ≥ ≠ ≅ ∉ ∈ ∉ ∋ ∌ ⊆ ⊈ ⊊ ⊄ ⊂ ∬ ∫ ⋯$"},
		{"dotted_negations", "$a in.not A, x eq.not y, p equiv.not q, u approx.not v, exists.not n$", "$a ∉ A, x ≠ y, p ≢ q, u ≉ v, ∄ n$"},
		{"greek_alt", "$epsilon + epsilon.alt + phi + phi.alt + theta.alt$", "$ε + ϵ + φ + ϕ + ϑ$"},
		{"blackboard_bold", "$NN subset ZZ subset QQ subset RR subset CC$", "$ℕ ⊂ ℤ ⊂ ℚ ⊂ ℝ ⊂ ℂ$"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lv := scanOne(tc.in, nil)
			if got := string(lv.DisplayText); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSuperAndSubscripts(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"$x^2 + x_0 + x_i^2 + a_j + y^n + z_n$", "$x² + x₀ + xᵢ² + aⱼ + yⁿ + zₙ$"},
		{"$x^23 + a_10 + a_index + x^^2 + x__i$", "$x^23 + a_10 + a_index + x^^2 + x__i$"},
		{`$\^2 + \_i$`, `$\^2 + \_i$`},
		{"$x^2.$", "$x².$"},
		{"$x^(n+1) + a_(k-1) + sum_(i = 1)^m x_k$", "$xⁿ⁺¹ + aₖ₋₁ + ∑ᵢ₌₁ᵐ xₖ$"},
		{"$x^(n + ?) + a_(i, j) + x^() + a_()$", "$x^(n + ?) + a_(i, j) + x^() + a_()$"},
	}
	for _, tc := range cases {
		lv := scanOne(tc.in, nil)
		if got := string(lv.DisplayText); got != tc.want {
			t.Errorf("input %q: got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMathAlphabetsAndExpandedOperators(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bb_and_cal", "$bb(R) + bb(N) + bb(1) + cal(L) + cal(F) + mybb(R) + foo.cal(L) + bb() + cal(a)$", "$ℝ + ℕ + 𝟙 + ℒ + ℱ + mybb(R) + foo.cal(L) + bb() + cal(a)$"},
		{"delimiters", "$angle.l x, y angle.r + floor.l t floor.r + ceil.l u ceil.r$", "$⟨ x, y ⟩ + ⌊ t ⌋ + ⌈ u ⌉$"},
		{"logic_and_proof", "$p and q or not r tack.r s models t therefore qed$", "$p ∧ q ∨ ¬ r ⊢ s ⊨ t ∴ ∎$"},
		{"relations_and_arrows", "$a div b + f compose g + x prec.eq y + u divides.not v + arrow.r.hook$", "$a ÷ b + f ∘ g + x ≼ y + u ∤ v + ↪$"},
		{"code_keywords_untouched", `#if a and b or not c { $a and b$ }`, `#if a and b or not c { $a ∧ b$ }`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lv := scanOne(tc.in, nil)
			if got := string(lv.DisplayText); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMathSubscriptOperatorAndCodeIdentifiers(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "subscript_operator_regression",
			in:   "$integral_0^x + sum_i^n + sum_(i=1)^n + alpha_0 + beta_i + x^2_0 + a_alpha + #in_set$",
			want: "$∫₀^x + ∑ᵢⁿ + ∑ᵢ₌₁ⁿ + α₀ + βᵢ + x²₀ + a_α + #in_set$",
		},
		{
			name: "untouched_words",
			in:   "$sin(x) + index + summary + alpha1 + dots1$",
			want: "$sin(x) + index + summary + alpha1 + dots1$",
		},
		{
			name: "untouched_a_index",
			in:   "$a_index$",
			want: "$a_index$",
		},
		{
			name: "untouched_double_scripts",
			in:   "$x^^2 + x__i$",
			want: "$x^^2 + x__i$",
		},
		{
			name: "untouched_dotted_in",
			in:   "$foo.in$",
			want: "$foo.in$",
		},
		{
			name: "code_identifiers_in_math",
			in:   "$#in_set + #alpha_1 + #my-alpha + #x->y$",
			want: "$#in_set + #alpha_1 + #my-alpha + #x→y$",
		},
		{
			name: "hash_non_ident_and_escape_unaffected",
			in:   `$<label> + #<label> + #(alpha) + \#alpha$`,
			want: `$<label> + #<label> + #(α) + \#α$`,
		},
		{
			name: "multi_digit_and_dotted_subscripts",
			in:   "$alpha_12 + beta_1 + in.not_0 + integral.double_0^1$",
			want: "$α_12 + β₁ + ∉₀ + ∬₀¹$",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lv := scanOne(tc.in, nil)
			if got := string(lv.DisplayText); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMarkupTypographyAndExclusions(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"em_en_ellipsis", "Hello -- world --- wait...", "Hello – world — wait…"},
		{"four_dashes_dots", "---- ..... .. <!-- -->", "---- ..... .. <!-- -->"},
		{"escaped_markup", `\- \-- \--- \...`, `\- \-- \--- \...`},
		{"raw_inline", "See `--flag` and `---` and `...` ok", "See `--flag` and `---` and `...` ok"},
		{"url_excluded", "Visit https://example.com/a--b/... now", "Visit https://example.com/a--b/... now"},
		{"comments_excluded", "ok // -- --- ...\n", "ok // -- --- ...\n"},
		{"block_comment_excluded", "a /* -- --- ... */ b -- c", "a /* -- --- ... */ b – c"},
		{"code_excluded", "#let x = a--b; text -- ok", "#let x = a--b; text – ok"},
		{"math_dash_vs_ellipsis", "$a -- b$ vs $1, ..., n$", "$a -- b$ vs $1, …, n$"},
		{"math_string_comment", `$ "alpha -> beta" + /* gamma */ alpha $`, `$ "alpha -> beta" + /* gamma */ α $`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lv := scanOne(tc.in, nil)
			if got := string(lv.DisplayText); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCoordinateRoundTripUTF8CombiningWideAndTabs(t *testing.T) {
	lines := []string{
		"",
		"$alpha + beta$",
		"\t中$alpha + beta$ z",
		"e\u0301 😀 $𝑥 + alpha -> beta$ --- end",
		"a\u0301\u0327 -- b\u0300",
		"$alpha + \U0001D538$ ok",
		"$integral_0^x + sum_i^n + #in_set + x^2_0$",
		"$sum_(i=1)^n x^(n+1) + bb(R) + cal(L) + angle.l u, v angle.r$",
	}

	for _, line := range lines {
		b := []byte(line)
		charLen := util.CharacterCount(b)
		lv := scanOne(line, nil)

		if lv.OriginalLen != charLen {
			t.Fatalf("%q: OriginalLen=%d, want %d", line, lv.OriginalLen, charLen)
		}
		if len(lv.BufToVis) != lv.OriginalLen+1 {
			t.Fatalf("%q: len(BufToVis)=%d, want %d", line, len(lv.BufToVis), lv.OriginalLen+1)
		}
		if len(lv.VisToBuf) != len(lv.DisplayText)+1 {
			t.Fatalf("%q: len(VisToBuf)=%d, want %d", line, len(lv.VisToBuf), len(lv.DisplayText)+1)
		}

		// EOL sentinel round-trip
		if got := lv.BufToVisual(lv.OriginalLen); got != len(lv.DisplayText) {
			t.Errorf("%q: BufToVisual(EOL)=%d, want %d", line, got, len(lv.DisplayText))
		}
		if got := lv.VisualToBuf(len(lv.DisplayText)); got != lv.OriginalLen {
			t.Errorf("%q: VisualToBuf(EOL)=%d, want %d", line, got, lv.OriginalLen)
		}

		// Every buffer char round-trips to itself (if unconcealed) or span.BufStart (if concealed)
		for col := 0; col < lv.OriginalLen; col++ {
			vis := lv.BufToVisual(col)
			back := lv.VisualToBuf(vis)
			if sp, ok := lv.SpanAtBufCol(col); ok {
				if back != sp.BufStart {
					t.Errorf("%q: col=%d in span %+v: VisualToBuf(BufToVisual)=%d, want %d", line, col, sp, back, sp.BufStart)
				}
			} else if back != col {
				t.Errorf("%q: col=%d unconcealed: VisualToBuf(BufToVisual)=%d, want %d", line, col, back, col)
			}
		}

		// Out-of-bounds clamping
		if lv.VisualToBuf(-5) != 0 || lv.VisualToBuf(9999) != lv.OriginalLen {
			t.Errorf("%q: VisualToBuf clamping failed", line)
		}
		if lv.BufToVisual(-5) != 0 || lv.BufToVisual(9999) != len(lv.DisplayText) {
			t.Errorf("%q: BufToVisual clamping failed", line)
		}
	}

	// Verify combining mark rune in DisplayText maps back to base char's bufCol
	lv := scanOne("e\u0301 $alpha$", nil)
	// DisplayText: ['e', '\u0301', ' ', '$', 'α', '$'] -> VisToBuf[0]=0, VisToBuf[1]=0, VisToBuf[2]=1
	if len(lv.DisplayText) != 6 {
		t.Fatalf("DisplayText len=%d (%q), want 6", len(lv.DisplayText), string(lv.DisplayText))
	}
	if lv.VisualToBuf(0) != 0 || lv.VisualToBuf(1) != 0 || lv.VisualToBuf(2) != 1 {
		t.Errorf("combining mark VisToBuf = %v, want [0 0 1 ...]", lv.VisToBuf)
	}
}

func TestTokenModeActiveCursorColsAndCacheBypass(t *testing.T) {
	src := typst.SliceSource{[]byte("$alpha + beta$")}
	// alpha is [1, 6), beta is [9, 13)
	cases := []struct {
		col  int
		want string
	}{
		{0, "$α + β$"},
		{1, "$alpha + β$"},
		{3, "$alpha + β$"},
		{6, "$alpha + β$"},
		{7, "$α + β$"},
		{9, "$α + beta$"},
		{13, "$α + beta$"},
		{14, "$α + β$"},
	}

	cache := NewBufferCache()
	for _, tc := range cases {
		lv := cache.LineView("doc", 1, src, 0, []int{tc.col})
		if got := string(lv.DisplayText); got != tc.want {
			t.Errorf("col=%d (same version=1): got %q, want %q", tc.col, got, tc.want)
		}
	}

	// Cached view with nil activeCursorCols remains concealed
	lvNil := cache.LineView("doc", 1, src, 0, nil)
	if got := string(lvNil.DisplayText); got != "$α + β$" {
		t.Errorf("nil activeCursorCols: got %q, want $α + β$", got)
	}
}

func TestBufferCacheVersioningInvalidationBoundsAndConcurrency(t *testing.T) {
	cache := NewBufferCache()

	src1 := typst.SliceSource{
		[]byte("$"),
		[]byte("  alpha + beta"),
		[]byte("$"),
	}
	lv1 := cache.LineView("docA", 1, src1, 1, nil)
	if got := string(lv1.DisplayText); got != "  α + β" {
		t.Fatalf("multiline math line 1 got %q, want %q", got, "  α + β")
	}

	// Version bump invalidates cached state/views
	src2 := typst.SliceSource{
		[]byte("plain"),
		[]byte("  alpha + beta"),
		[]byte("end"),
	}
	lv2 := cache.LineView("docA", 2, src2, 1, nil)
	if got := string(lv2.DisplayText); got != "  alpha + beta" {
		t.Fatalf("after version bump line 1 got %q, want plain", got)
	}

	// Explicit Invalidate
	cache.Invalidate("docA")
	lv3 := cache.LineView("docA", 2, src1, 1, nil)
	if got := string(lv3.DisplayText); got != "  α + β" {
		t.Fatalf("after Invalidate line 1 got %q", got)
	}

	// Out-of-bounds and non-comparable docKey safety
	if lvOOB := cache.LineView("docA", 2, src1, -1, nil); lvOOB.OriginalLen != 0 || lvOOB.VisualToBuf(0) != 0 {
		t.Errorf("unexpected OOB LineView: %+v", lvOOB)
	}
	if lvOOB := cache.LineView("docA", 2, src1, 99, nil); lvOOB.OriginalLen != 0 || lvOOB.BufToVisual(0) != 0 {
		t.Errorf("unexpected OOB LineView: %+v", lvOOB)
	}
	if lvNonComp := cache.LineView([]string{"non-comparable"}, 1, src1, 1, nil); string(lvNonComp.DisplayText) != "  α + β" {
		t.Errorf("non-comparable docKey failed: %q", string(lvNonComp.DisplayText))
	}

	// 20,000-line bounded cache check + concurrent readers
	big := make(typst.SliceSource, 20000)
	for i := range big {
		big[i] = []byte(fmt.Sprintf("Line %d $alpha + beta$ -- ok", i))
	}
	for i := 0; i < 1200; i++ {
		cache.LineView("big", 1, big, i*15, nil)
	}
	cache.mu.Lock()
	viewCount := len(cache.docs["big"].views)
	cache.mu.Unlock()
	if viewCount > defaultMaxViews {
		t.Fatalf("viewCount=%d exceeded defaultMaxViews=%d", viewCount, defaultMaxViews)
	}

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				lv := cache.LineView("big", 1, big, (id*100+i)%20000, nil)
				if !strings.Contains(string(lv.DisplayText), "α") {
					t.Errorf("unexpected DisplayText: %q", string(lv.DisplayText))
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestToggleSetting(t *testing.T) {
	if _, err := ToggleSetting(nil); err == nil {
		t.Fatalf("expected error on nil map")
	}

	s := map[string]any{}
	// Missing key defaults to true, so toggle yields false
	got, err := ToggleSetting(s)
	if err != nil || got != false || s["typst.conceal"] != false {
		t.Fatalf("first toggle on missing key = (%v, %v), map=%v", got, err, s)
	}
	got, err = ToggleSetting(s)
	if err != nil || got != true || s["typst.conceal"] != true {
		t.Fatalf("second toggle = (%v, %v), map=%v", got, err, s)
	}

	for _, tc := range []struct {
		arg  string
		want bool
	}{
		{"off", false},
		{"ON", true},
		{"false", false},
		{"true", true},
	} {
		got, err = ToggleSetting(s, tc.arg)
		if err != nil || got != tc.want || s["typst.conceal"] != tc.want {
			t.Errorf("ToggleSetting(%q) = (%v, %v), want %v", tc.arg, got, err, tc.want)
		}
	}

	// Invalid arg must not mutate map
	s["typst.conceal"] = true
	if _, err := ToggleSetting(s, "maybe"); err == nil || s["typst.conceal"] != true {
		t.Errorf("invalid arg should error and preserve true, err=%v map=%v", err, s)
	}
	if _, err := ToggleSetting(s, "on", "extra"); err == nil {
		t.Errorf("expected error for extra args")
	}
	s["typst.conceal"] = "corrupt"
	if _, err := ToggleSetting(s); err == nil {
		t.Errorf("expected error for non-bool value in map")
	}
}

func BenchmarkBufferCache20kLines(b *testing.B) {
	big := make(typst.SliceSource, 20000)
	for i := range big {
		big[i] = []byte(fmt.Sprintf("Line %d $alpha + beta -> gamma$ -- ok", i))
	}
	cache := NewBufferCache()
	// Warm state prefix once
	cache.LineView("bench", 1, big, 19999, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cache.LineView("bench", 1, big, i%20000, nil)
	}
}
