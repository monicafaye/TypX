package shortcuts

import (
	"strings"
	"testing"

	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/pkg/highlight"
)

func loadTypstSyntaxDef(t *testing.T) *highlight.Def {
	t.Helper()
	rtFile := config.FindRuntimeFile(config.RTSyntax, "typst")
	if rtFile == nil {
		t.Fatal("runtime syntax file 'typst' not found")
	}
	data, err := rtFile.Data()
	if err != nil {
		t.Fatalf("reading typst.yaml: %v", err)
	}
	f, err := highlight.ParseFile(data)
	if err != nil {
		t.Fatalf("highlight.ParseFile(typst.yaml): %v", err)
	}
	hdr, err := highlight.MakeHeaderYaml(data)
	if err != nil {
		t.Fatalf("highlight.MakeHeaderYaml(typst.yaml): %v", err)
	}
	if hdr.FileType != "typst" {
		t.Fatalf("expected filetype 'typst', got %q", hdr.FileType)
	}
	if !hdr.MatchFileName("paper.typ") || hdr.MatchFileName("paper.md") {
		t.Fatalf("filename detection mismatch: paper.typ=%v paper.md=%v",
			hdr.MatchFileName("paper.typ"), hdr.MatchFileName("paper.md"))
	}
	def, err := highlight.ParseDef(f, hdr)
	if err != nil {
		t.Fatalf("highlight.ParseDef(typst.yaml): %v", err)
	}
	return def
}

func groupAt(m highlight.LineMatch, col int) string {
	bestCol := -1
	var bestGroup highlight.Group
	for k, g := range m {
		if k <= col && k > bestCol {
			bestCol = k
			bestGroup = g
		}
	}
	if bestCol < 0 || bestGroup == 0 {
		return ""
	}
	return bestGroup.String()
}

func TestTypstSyntaxHeaderAndPatterns(t *testing.T) {
	def := loadTypstSyntaxDef(t)
	h := highlight.NewHighlighter(def)

	// Verify .hdr if generated
	if hdrFile := config.FindRuntimeFile(config.RTSyntaxHeader, "typst"); hdrFile != nil {
		hdrData, err := hdrFile.Data()
		if err != nil {
			t.Fatalf("reading typst.hdr: %v", err)
		}
		hdr, err := highlight.MakeHeader(hdrData)
		if err != nil {
			t.Fatalf("highlight.MakeHeader(typst.hdr): %v", err)
		}
		if hdr.FileType != "typst" || !hdr.MatchFileName("notes.typ") || hdr.MatchFileName("notes.txt") {
			t.Fatalf("unexpected typst.hdr behavior: filetype=%q", hdr.FileType)
		}
	}

	doc := strings.Join([]string{
		"= Introduction <sec:intro>",
		"== Subheading with 1.2pt",
		"Text *bold* and _italic_ and my_var_name and `code\\` end.",
		"Visit https://example.com/a/b.html. for @smith2024 or a@b.com.",
		"- list item",
		"+ enum item",
		"/ term: desc",
		"// TODO: fix line comment",
		"/* block TODO: check */",
		`Price is \$5 and inline $x + frac(1, 2)$ and $"str $"$.`,
		"$",
		"  sum_(i=1)^n x_i -> alpha",
		"$",
		"```rust",
		"= not a heading",
		"```",
		"````",
		"```nested```",
		"````",
		"#let x = 12pt",
		"#figure([*cap*], caption: \"hi\")",
	}, "\n")

	matches := h.HighlightString(doc)
	lines := strings.Split(doc, "\n")
	if len(matches) != len(lines) {
		t.Fatalf("expected %d match lines, got %d", len(lines), len(matches))
	}

	// Line 0: "= Introduction <sec:intro>" -> heading statement, label identifier
	if g := groupAt(matches[0], 0); g != "statement" {
		t.Errorf("heading '=' group = %q, want statement", g)
	}
	if g := groupAt(matches[0], strings.Index(lines[0], "<sec:intro>")); g != "identifier" {
		t.Errorf("<sec:intro> group = %q, want identifier", g)
	}

	// Line 2: *bold*, _italic_, my_var_name untouched, `code\` closed cleanly before " end."
	if g := groupAt(matches[2], strings.Index(lines[2], "*bold*")); g != "special" {
		t.Errorf("*bold* group = %q, want special", g)
	}
	if g := groupAt(matches[2], strings.Index(lines[2], "_italic_")); g != "type" {
		t.Errorf("_italic_ group = %q, want type", g)
	}
	if g := groupAt(matches[2], strings.Index(lines[2], "my_var_name")); g != "" {
		t.Errorf("my_var_name should not be emphasized, got group %q", g)
	}
	if g := groupAt(matches[2], strings.Index(lines[2], "`code\\`")+1); g != "constant.string.raw" {
		t.Errorf("inline raw group = %q, want constant.string.raw", g)
	}
	if g := groupAt(matches[2], strings.Index(lines[2], "end.")); g != "" {
		t.Errorf("text after `code\\` should exit raw region, got %q", g)
	}

	// Line 3: URL must NOT become comment; @smith2024 is identifier; a@b.com is NOT citation
	urlIdx := strings.Index(lines[3], "https://")
	if g := groupAt(matches[3], urlIdx); g != "underlined" {
		t.Errorf("URL scheme group = %q, want underlined", g)
	}
	if g := groupAt(matches[3], urlIdx+len("https://example.com")); g != "underlined" {
		t.Errorf("URL path group = %q, want underlined", g)
	}
	if g := groupAt(matches[3], strings.Index(lines[3], "@smith2024")); g != "identifier" {
		t.Errorf("@smith2024 group = %q, want identifier", g)
	}
	if g := groupAt(matches[3], strings.Index(lines[3], "@b.com")); g != "" {
		t.Errorf("email a@b.com should not highlight as citation, got %q", g)
	}

	// Lines 4-6: list markers
	for _, li := range []int{4, 5, 6} {
		if g := groupAt(matches[li], 0); g != "special" {
			t.Errorf("line %d list marker group = %q, want special", li, g)
		}
	}

	// Lines 7-8: comments + todo
	if g := groupAt(matches[7], 0); g != "comment" {
		t.Errorf("line comment group = %q, want comment", g)
	}
	if g := groupAt(matches[7], strings.Index(lines[7], "TODO:")); g != "todo" {
		t.Errorf("line comment TODO group = %q, want todo", g)
	}
	if g := groupAt(matches[8], 0); g != "comment" {
		t.Errorf("block comment group = %q, want comment", g)
	}
	if g := groupAt(matches[8], strings.Index(lines[8], "TODO:")); g != "todo" {
		t.Errorf("block comment TODO group = %q, want todo", g)
	}

	// Line 9: \$5 not math; inline math inner tokens (frac, 1, +, "str $") ARE highlighted!
	if g := groupAt(matches[9], strings.Index(lines[9], "5 and")); g != "constant.number" {
		t.Errorf("5 after \\$ should be constant.number outside math, got %q", g)
	}
	if g := groupAt(matches[9], strings.Index(lines[9], "frac")); g != "type" {
		t.Errorf("math frac group = %q, want type (limit-group must not hide inner rules)", g)
	}
	if g := groupAt(matches[9], strings.Index(lines[9], "+")); g != "statement" {
		t.Errorf("math '+' group = %q, want statement", g)
	}
	if g := groupAt(matches[9], strings.Index(lines[9], `"str $"`)+1); g != "constant.string" {
		t.Errorf("math string group = %q, want constant.string", g)
	}
	if g := groupAt(matches[9], len(lines[9])-1); g != "" {
		t.Errorf("trailing '.' after $\"str $\"$ should exit math, got %q", g)
	}

	// Line 11: multi-line display math
	if g := groupAt(matches[11], strings.Index(lines[11], "->")); g != "statement" {
		t.Errorf("multiline math '->' group = %q, want statement", g)
	}
	if g := groupAt(matches[11], strings.Index(lines[11], "1")); g != "constant.number" {
		t.Errorf("multiline math '1' group = %q, want constant.number", g)
	}

	// Lines 13-15: 3-backtick fenced raw block
	if g := groupAt(matches[14], 0); g != "constant.string.raw" {
		t.Errorf("fenced raw inner line group = %q, want constant.string.raw", g)
	}

	// Lines 16-18: 4-backtick fenced raw block protects inner ```nested```
	if g := groupAt(matches[17], 4); g != "constant.string.raw" {
		t.Errorf("4-backtick inner line group = %q, want constant.string.raw", g)
	}

	// Lines 19-20: code keywords, units, builtins, strings
	if g := groupAt(matches[19], 0); g != "statement" {
		t.Errorf("#let group = %q, want statement", g)
	}
	if g := groupAt(matches[19], strings.Index(lines[19], "12pt")); g != "constant.number" {
		t.Errorf("12pt group = %q, want constant.number", g)
	}
	if g := groupAt(matches[20], 0); g != "special" {
		t.Errorf("#figure group = %q, want special", g)
	}
	if g := groupAt(matches[20], strings.Index(lines[20], "*cap*")); g != "special" {
		t.Errorf("nested *cap* inside #figure group = %q, want special", g)
	}
	if g := groupAt(matches[20], strings.Index(lines[20], `"hi"`)); g != "constant.string" {
		t.Errorf(`"hi" inside #figure group = %q, want constant.string`, g)
	}
}

func TestBundledColorschemes(t *testing.T) {
	requiredGroups := []string{
		"default", "comment", "todo",
		"identifier", "identifier.class", "identifier.macro", "identifier.var",
		"constant", "constant.bool", "constant.bool.true", "constant.bool.false",
		"constant.number", "constant.specialChar",
		"constant.string", "constant.string.char", "constant.string.raw", "constant.string.url",
		"statement", "preproc", "type", "type.keyword", "type.extended", "special",
		"symbol", "symbol.brackets", "symbol.operator", "symbol.tag", "symbol.math", "conceal",
		"underlined", "error", "ignore",
		"gutter-error", "gutter-warning", "gutter-info",
		"line-number", "current-line-number", "cursor-line", "color-column",
		"selection", "hlsearch", "match-brace",
		"statusline", "statusline.inactive", "statusline.suggestions",
		"statusline.dim", "statusline.ok", "statusline.warn", "statusline.err", "statusline.accent",
		"statusline.mode.markup", "statusline.mode.math", "statusline.mode.code", "statusline.mode.raw",
		"bottombar", "bottombar.key", "bottombar.active",
		"bottombar.badge", "bottombar.badge.math", "bottombar.badge.code", "bottombar.badge.raw",
		"bottombar.err", "bottombar.warn", "bottombar.dim",
		"tabbar", "tabbar.active", "message", "error-message",
		"divider", "scrollbar", "indent-char", "tab-error", "trailingws",
		"diff-added", "diff-modified", "diff-deleted",
	}

	fullSchemes := []string{
		"default",
		"catppuccin-mocha", "catppuccin-latte",
		"github-dark", "github-light",
		"tokyo-night", "nord",
		"rose-pine", "rose-pine-dawn",
		"kanagawa",
		"everforest-dark", "everforest-light",
		"gruvbox-tc", "gruvbox-light",
		"solarized-tc", "solarized-light",
		"monokai", "dracula-tc", "one-dark",
	}

	for _, removed := range []string{"typx-dark", "typx-light"} {
		if config.ColorschemeExists(removed) {
			t.Errorf("removed colorscheme %q should not exist", removed)
		}
	}

	for _, name := range fullSchemes {
		t.Run(name, func(t *testing.T) {
			var parsed []string
			scheme, err := config.LoadColorscheme(name, &parsed)
			if err != nil {
				t.Fatalf("LoadColorscheme(%q) failed: %v", name, err)
			}
			for _, grp := range requiredGroups {
				if _, ok := scheme[grp]; !ok {
					t.Errorf("%s missing required group %q", name, grp)
				}
			}

			defFg, defBg, _ := scheme["default"].Decompose()
			curLineFg, _, _ := scheme["cursor-line"].Decompose()
			if defBg == curLineFg {
				t.Errorf("%s cursor-line color should differ from default bg", name)
			}
			if defFg == defBg {
				t.Errorf("%s default fg and bg must differ", name)
			}

			// symbol.math and comment must inherit default background so cursor-line
			// and region boundary matches remain visually clean.
			_, mathBg, _ := scheme["symbol.math"].Decompose()
			_, commentBg, _ := scheme["comment"].Decompose()
			if mathBg != defBg {
				t.Errorf("%s symbol.math bg (%v) should match default bg (%v)", name, mathBg, defBg)
			}
			if commentBg != defBg {
				t.Errorf("%s comment bg (%v) should match default bg (%v)", name, commentBg, defBg)
			}
		})
	}
}
