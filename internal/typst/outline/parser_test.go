package outline

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monicafaye/TypX/internal/typst"
)

func TestParseNestedHeadingsAndMetadata(t *testing.T) {
	doc := strings.Join([]string{
		"= Introduction <sec:intro>",
		"Some text here.",
		"== Background /* inline comment */",
		"=== Deep Subsection // trailing comment",
		"==== Level Four",
		"===== Level Five",
		"====== Level Six",
		"======= Too Deep Ignored",
		"===NoSpaceNotHeading",
		"== Using `raw` and $alpha$ in Heading <sec:math>",
		"= Visit https://typst.app/docs // keep url",
		"#heading(level: 2)[Custom Heading] <sec:custom>",
		"#heading(outlined: false)[Hidden Heading]",
	}, "\n")

	items := ParseString(doc, ParseOptions{IncludeLabels: false})

	want := []Item{
		{Kind: KindHeading, Level: 1, Title: "Introduction", Label: "sec:intro", Line: 0, Column: 0},
		{Kind: KindHeading, Level: 2, Title: "Background", Line: 2, Column: 0},
		{Kind: KindHeading, Level: 3, Title: "Deep Subsection", Line: 3, Column: 0},
		{Kind: KindHeading, Level: 4, Title: "Level Four", Line: 4, Column: 0},
		{Kind: KindHeading, Level: 5, Title: "Level Five", Line: 5, Column: 0},
		{Kind: KindHeading, Level: 6, Title: "Level Six", Line: 6, Column: 0},
		{Kind: KindHeading, Level: 2, Title: "Using `raw` and $alpha$ in Heading", Label: "sec:math", Line: 9, Column: 0},
		{Kind: KindHeading, Level: 1, Title: "Visit https://typst.app/docs", Line: 10, Column: 0},
		{Kind: KindHeading, Level: 2, Title: "Custom Heading", Label: "sec:custom", Line: 11, Column: 0},
	}

	if !reflect.DeepEqual(items, want) {
		t.Fatalf("ParseString mismatch:\ngot:  %#v\nwant: %#v", items, want)
	}
}

func TestParseFencedRawBlocksIgnored(t *testing.T) {
	doc := strings.Join([]string{
		"= Before Fence",
		"```typst",
		"= Fake Heading In Fence",
		"== Fake Subheading",
		"#figure(rect(), caption: [Fake Figure])",
		"<fake-label>",
		"/* unclosed comment inside raw should not leak",
		"```",
		"== After 3-Backtick Fence",
		"````markdown",
		"```typ",
		"= Fake Inside 4-Backtick Fence",
		"```",
		"= Still Inside 4-Backtick Fence",
		"````",
		"=== After 4-Backtick Fence",
		"  ```",
		"  = Indented Fenced Heading",
		"  ```",
		"Inline `` empty raw and `= not heading` text",
		"==== Final Heading",
	}, "\n")

	items := ParseString(doc, ParseOptions{IncludeLabels: true})

	want := []Item{
		{Kind: KindHeading, Level: 1, Title: "Before Fence", Line: 0, Column: 0},
		{Kind: KindHeading, Level: 2, Title: "After 3-Backtick Fence", Line: 8, Column: 0},
		{Kind: KindHeading, Level: 3, Title: "After 4-Backtick Fence", Line: 15, Column: 0},
		{Kind: KindHeading, Level: 4, Title: "Final Heading", Line: 20, Column: 0},
	}

	if !reflect.DeepEqual(items, want) {
		t.Fatalf("fenced raw block mismatch:\ngot:  %#v\nwant: %#v", items, want)
	}
}

func TestParseNestedBlockCommentsIgnored(t *testing.T) {
	doc := strings.Join([]string{
		"= Real Heading 1",
		"/* outer comment",
		"= Fake Level 1",
		"/* nested inner comment",
		"== Fake Level 2",
		"/* triple nested",
		"=== Fake Level 3",
		"*/",
		"== Still Inside Level 2",
		"*/",
		"= Still Inside Outer",
		"#figure(image(\"x.png\"), caption: [Fake Commented Figure])",
		"<commented-label>",
		"*/",
		"== Real Heading 2 /* inline /* nested */ comment */",
		"= Real Heading 3 /* opens multiline comment",
		"= Fake After Heading Comment",
		"*/",
		"// /* line comment does not open block comment",
		"=== Real Heading 4",
		"/* = Single Line Commented Heading */",
	}, "\n")

	items := ParseString(doc, ParseOptions{IncludeLabels: true})

	want := []Item{
		{Kind: KindHeading, Level: 1, Title: "Real Heading 1", Line: 0, Column: 0},
		{Kind: KindHeading, Level: 2, Title: "Real Heading 2", Line: 14, Column: 0},
		{Kind: KindHeading, Level: 1, Title: "Real Heading 3", Line: 15, Column: 0},
		{Kind: KindHeading, Level: 3, Title: "Real Heading 4", Line: 19, Column: 0},
	}

	if !reflect.DeepEqual(items, want) {
		t.Fatalf("nested block comment mismatch:\ngot:  %#v\nwant: %#v", items, want)
	}
}

func TestParseDisplayMathNotHeading(t *testing.T) {
	doc := strings.Join([]string{
		"= Equations",
		"$",
		"  f(x)",
		"  = x^2 + 1",
		"  == not a heading either",
		"$",
		"== After Math",
	}, "\n")

	items := ParseString(doc, ParseOptions{})
	want := []Item{
		{Kind: KindHeading, Level: 1, Title: "Equations", Line: 0, Column: 0},
		{Kind: KindHeading, Level: 2, Title: "After Math", Line: 6, Column: 0},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("display math mismatch:\ngot:  %#v\nwant: %#v", items, want)
	}
}

func TestParseFiguresWithAndWithoutCaptions(t *testing.T) {
	doc := strings.Join([]string{
		"= Results",
		"#figure(",
		"  image(\"arch.png\"),",
		"  caption: [System Architecture],",
		") <fig:arch>",
		"#figure(",
		"  table(columns: 2, [A], [B]),",
		"  caption: [",
		"    Multi-line caption with [nested] brackets",
		"    and extra   spaces",
		"  ],",
		")",
		"#figure(rect(), caption: \"String Caption\")",
		"#figure(",
		"  circle(radius: 10pt),",
		")",
		"#figure(",
		"  ```typ",
		"  = Fake Heading Inside Figure Code",
		"  ```,",
		"  caption: [Code Listing],",
		")",
	}, "\n")

	items := ParseString(doc, ParseOptions{IncludeLabels: true})
	want := []Item{
		{Kind: KindHeading, Level: 1, Title: "Results", Line: 0, Column: 0},
		{Kind: KindFigure, Level: 0, Title: "System Architecture", Label: "fig:arch", Line: 1, Column: 0},
		{Kind: KindFigure, Level: 0, Title: "Multi-line caption with [nested] brackets and extra spaces", Line: 5, Column: 0},
		{Kind: KindFigure, Level: 0, Title: "String Caption", Line: 12, Column: 0},
		{Kind: KindFigure, Level: 0, Title: "Figure (line 14)", Line: 13, Column: 0},
		{Kind: KindFigure, Level: 0, Title: "Code Listing", Line: 16, Column: 0},
	}

	if !reflect.DeepEqual(items, want) {
		t.Fatalf("figure parse mismatch:\ngot:  %#v\nwant: %#v", items, want)
	}
}

func TestParseLabelsToggle(t *testing.T) {
	doc := strings.Join([]string{
		"= Intro <sec:intro>",
		"$ E = m c^2 $ <eq:energy>",
		"Visit <https://typst.app> or check a < b > c or \\<escaped>.",
		"<standalone_anchor>",
		"`<raw-label>`",
		"/* <comment-label> */",
	}, "\n")

	withoutLabels := ParseString(doc, ParseOptions{IncludeLabels: false})
	if len(withoutLabels) != 1 || withoutLabels[0].Kind != KindHeading || withoutLabels[0].Label != "sec:intro" {
		t.Fatalf("unexpected items with IncludeLabels=false: %#v", withoutLabels)
	}

	withLabels := ParseString(doc, ParseOptions{IncludeLabels: true})
	want := []Item{
		{Kind: KindHeading, Level: 1, Title: "Intro", Label: "sec:intro", Line: 0, Column: 0},
		{Kind: KindLabel, Level: 0, Title: "eq:energy", Label: "eq:energy", Line: 1, Column: 14},
		{Kind: KindLabel, Level: 0, Title: "standalone_anchor", Label: "standalone_anchor", Line: 3, Column: 0},
	}
	if !reflect.DeepEqual(withLabels, want) {
		t.Fatalf("IncludeLabels=true mismatch:\ngot:  %#v\nwant: %#v", withLabels, want)
	}
}

func TestIncrementalParserMatchesFullParse(t *testing.T) {
	toSource := func(lines []string) typst.SliceSource {
		b := make([][]byte, len(lines))
		for i, l := range lines {
			b[i] = []byte(l)
		}
		return typst.SliceSource(b)
	}

	opts := ParseOptions{IncludeLabels: true}
	ip := NewIncrementalParser()

	lines := []string{
		"= Title",
		"Body text",
		"== Section A",
		"#figure(",
		"  rect(),",
		"  caption: [Initial Caption],",
		") <fig:a>",
		"== Section B",
		"<anchor-b>",
		"=== Subsection B1",
	}

	check := func(step string, startLine int) {
		t.Helper()
		src := toSource(lines)
		got := ip.Update(src, startLine, opts)
		want := Parse(src, opts)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("step %s: incremental != full:\ngot:  %#v\nwant: %#v", step, got, want)
		}
	}

	check("initial", 0)

	// 1. Edit single heading text in-place (same line count).
	lines[2] = "== Section A Renamed"
	check("rename-heading", 2)

	// 2. Edit caption inside multi-line #figure (several lines after #figure().
	lines[5] = "  caption: [Updated Figure Caption],"
	check("edit-figure-caption", 5)

	// 3. Open an unclosed block comment above Section B.
	lines[7] = "/* == Commented Section B"
	check("open-block-comment", 7)

	// 4. Close the block comment on line 8.
	lines[8] = "*/ <anchor-b>"
	check("close-block-comment", 8)

	// 5. Insert a fenced raw block (changing line count).
	lines = append(lines[:2], append([]string{"```typ", "= Fake Inside Fence", "```"}, lines[2:]...)...)
	check("insert-fenced-block", 2)

	// 6. Delete lines (shrinking line count).
	lines = append(lines[:2], lines[5:]...)
	check("delete-fenced-block", 2)
}

func TestEnrichWithSymbols(t *testing.T) {
	local := []Item{
		{Kind: KindHeading, Level: 1, Title: "Introduction", Label: "sec:intro", Line: 0, Column: 0},
		{Kind: KindFigure, Level: 0, Title: "Overview Diagram", Label: "fig:ov", Line: 4, Column: 0},
		{Kind: KindLabel, Level: 0, Title: "eq:one", Label: "eq:one", Line: 8, Column: 10},
	}

	// Empty symbols falls back unchanged.
	if got := EnrichWithSymbols(local, nil, ParseOptions{}); !reflect.DeepEqual(got, local) {
		t.Fatalf("nil symbols should preserve local verbatim: %#v", got)
	}

	// Hierarchical LSP symbols: matching line preserves label metadata, new dynamic
	// heading on line 6 is merged in order, non-heading variable symbol is ignored.
	syms := []Symbol{
		{
			Name:   "Introduction",
			Kind:   lspSymbolKindNamespace,
			Line:   0,
			Column: 0,
			Children: []Symbol{
				{
					Name:   "Generated Subsection",
					Kind:   lspSymbolKindNamespace,
					Line:   6,
					Column: 0,
				},
				{
					Name:   "myVar",
					Kind:   13, // SymbolKindVariable -> ignored
					Line:   7,
					Column: 0,
				},
			},
		},
	}

	got := EnrichWithSymbols(local, syms, ParseOptions{IncludeLabels: true})
	want := []Item{
		{Kind: KindHeading, Level: 1, Title: "Introduction", Label: "sec:intro", Line: 0, Column: 0},
		{Kind: KindFigure, Level: 0, Title: "Overview Diagram", Label: "fig:ov", Line: 4, Column: 0},
		{Kind: KindHeading, Level: 2, Title: "Generated Subsection", Line: 6, Column: 0},
		{Kind: KindLabel, Level: 0, Title: "eq:one", Label: "eq:one", Line: 8, Column: 10},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EnrichWithSymbols mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestParsePerformance5000Lines(t *testing.T) {
	var lines [][]byte
	for i := 0; i < 5000; i++ {
		switch i % 50 {
		case 0:
			lines = append(lines, []byte("= Section Heading"))
		case 10:
			lines = append(lines, []byte("== Subsection Heading"))
		case 20:
			lines = append(lines, []byte("#figure(rect(), caption: [Sample Plot])"))
		default:
			lines = append(lines, []byte("Regular paragraph text with $alpha + beta -> gamma$ inline math."))
		}
	}
	src := typst.SliceSource(lines)

	start := time.Now()
	items := Parse(src, ParseOptions{})
	elapsed := time.Since(start)

	if len(items) != 300 {
		t.Fatalf("expected 300 items, got %d", len(items))
	}
	if elapsed > parsePerfBudget {
		t.Fatalf("5000-line parse too slow: %v (budget %v)", elapsed, parsePerfBudget)
	}
}

func TestParseIncludesLexicalRulesAndIncremental(t *testing.T) {
	doc := strings.Join([]string{
		`#include "chapters/intro.typ"`,
		`#include("./methods.typ")`,
		`#include /* inline /* nested */ comment */ ( /* c */ "results.typ" )`,
		`#include "escaped\"quote\\dir.typ"`,
		`#include "first.typ"; #include("second.typ") // #include "commented_out.typ"`,
		`// #include "line_comment.typ"`,
		`/* block comment #include "block_comment.typ" */`,
		"/* multi-line",
		`#include "multiline_comment.typ"`,
		"*/",
		"`#include \"inline_raw.typ\"`",
		"````typ",
		"```",
		`#include "fenced_raw.typ"`,
		"```",
		"````",
		`$ #include "math.typ" $`,
		`"#include \"string_literal.typ\""`,
		`\#include "escaped_hash.typ"`,
		`#include_custom "not_include.typ"`,
		`#include(dynamicVar)`,
		`"Prose quote opening on line 1`,
		`and closing here." #include "prose_after_quote.typ"`,
		`5" display #include "after_inch_quote.typ"`,
		`#include "appendix.typ"`,
	}, "\n")

	got := ParseIncludesString(doc)
	want := []string{
		"chapters/intro.typ",
		"./methods.typ",
		"results.typ",
		`escaped"quote\dir.typ`,
		"first.typ",
		"second.typ",
		"prose_after_quote.typ",
		"after_inch_quote.typ",
		"appendix.typ",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseIncludesString mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}

	// Verify incremental updates on #include lines match full parse.
	lines := strings.Split(doc, "\n")
	toSource := func(ls []string) typst.SliceSource {
		b := make([][]byte, len(ls))
		for i, l := range ls {
			b[i] = []byte(l)
		}
		return typst.SliceSource(b)
	}
	ip := NewIncrementalParser()
	ip.Update(toSource(lines), 0, ParseOptions{})
	if !reflect.DeepEqual(ip.Includes(), want) {
		t.Fatalf("initial incremental includes mismatch: got %#v want %#v", ip.Includes(), want)
	}

	// Replace line 1 in-place and verify incremental convergence updates includes.
	lines[1] = `#include("./methods_v2.typ")`
	ip.Update(toSource(lines), 1, ParseOptions{})
	wantUpdated := append([]string(nil), want...)
	wantUpdated[1] = "./methods_v2.typ"
	if !reflect.DeepEqual(ip.Includes(), wantUpdated) {
		t.Fatalf("updated incremental includes mismatch: got %#v want %#v", ip.Includes(), wantUpdated)
	}
}
