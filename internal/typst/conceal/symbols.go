package conceal

import (
	"sort"

	"github.com/monicafaye/TypX/internal/util"
)

// SymbolRule defines a Typst token-to-Unicode replacement rule.
type SymbolRule struct {
	Pattern string
	Glyph   rune
}

// GreekLower contains lowercase Greek letters and Typst .alt variants.
var GreekLower = []SymbolRule{
	{Pattern: "alpha", Glyph: 'α'},
	{Pattern: "beta", Glyph: 'β'},
	{Pattern: "gamma", Glyph: 'γ'},
	{Pattern: "delta", Glyph: 'δ'},
	{Pattern: "epsilon", Glyph: 'ε'},
	{Pattern: "epsilon.alt", Glyph: 'ϵ'},
	{Pattern: "zeta", Glyph: 'ζ'},
	{Pattern: "eta", Glyph: 'η'},
	{Pattern: "theta", Glyph: 'θ'},
	{Pattern: "theta.alt", Glyph: 'ϑ'},
	{Pattern: "iota", Glyph: 'ι'},
	{Pattern: "kappa", Glyph: 'κ'},
	{Pattern: "lambda", Glyph: 'λ'},
	{Pattern: "mu", Glyph: 'μ'},
	{Pattern: "nu", Glyph: 'ν'},
	{Pattern: "xi", Glyph: 'ξ'},
	{Pattern: "omicron", Glyph: 'ο'},
	{Pattern: "pi", Glyph: 'π'},
	{Pattern: "pi.alt", Glyph: 'ϖ'},
	{Pattern: "rho", Glyph: 'ρ'},
	{Pattern: "rho.alt", Glyph: 'ϱ'},
	{Pattern: "sigma", Glyph: 'σ'},
	{Pattern: "sigma.alt", Glyph: 'ς'},
	{Pattern: "tau", Glyph: 'τ'},
	{Pattern: "upsilon", Glyph: 'υ'},
	{Pattern: "phi", Glyph: 'φ'},
	{Pattern: "phi.alt", Glyph: 'ϕ'},
	{Pattern: "chi", Glyph: 'χ'},
	{Pattern: "psi", Glyph: 'ψ'},
	{Pattern: "omega", Glyph: 'ω'},
}

// GreekUpper contains uppercase Greek letters.
var GreekUpper = []SymbolRule{
	{Pattern: "Alpha", Glyph: 'Α'},
	{Pattern: "Beta", Glyph: 'Β'},
	{Pattern: "Gamma", Glyph: 'Γ'},
	{Pattern: "Delta", Glyph: 'Δ'},
	{Pattern: "Epsilon", Glyph: 'Ε'},
	{Pattern: "Zeta", Glyph: 'Ζ'},
	{Pattern: "Eta", Glyph: 'Η'},
	{Pattern: "Theta", Glyph: 'Θ'},
	{Pattern: "Iota", Glyph: 'Ι'},
	{Pattern: "Kappa", Glyph: 'Κ'},
	{Pattern: "Lambda", Glyph: 'Λ'},
	{Pattern: "Mu", Glyph: 'Μ'},
	{Pattern: "Nu", Glyph: 'Ν'},
	{Pattern: "Xi", Glyph: 'Ξ'},
	{Pattern: "Omicron", Glyph: 'Ο'},
	{Pattern: "Pi", Glyph: 'Π'},
	{Pattern: "Rho", Glyph: 'Ρ'},
	{Pattern: "Sigma", Glyph: 'Σ'},
	{Pattern: "Tau", Glyph: 'Τ'},
	{Pattern: "Upsilon", Glyph: 'Υ'},
	{Pattern: "Phi", Glyph: 'Φ'},
	{Pattern: "Chi", Glyph: 'Χ'},
	{Pattern: "Psi", Glyph: 'Ψ'},
	{Pattern: "Omega", Glyph: 'Ω'},
}

// Arrows contains symbolic and dotted Typst math arrow rules.
var Arrows = []SymbolRule{
	{Pattern: "<-->", Glyph: '⟷'},
	{Pattern: "<==>", Glyph: '⟺'},
	{Pattern: "<->", Glyph: '↔'},
	{Pattern: "<=>", Glyph: '⇔'},
	{Pattern: "|->", Glyph: '↦'},
	{Pattern: "-->", Glyph: '⟶'},
	{Pattern: "<--", Glyph: '⟵'},
	{Pattern: "==>", Glyph: '⟹'},
	{Pattern: "<==", Glyph: '⟸'},
	{Pattern: "->", Glyph: '→'},
	{Pattern: "<-", Glyph: '←'},
	{Pattern: "=>", Glyph: '⇒'},
	{Pattern: "arrow.r", Glyph: '→'},
	{Pattern: "arrow.l", Glyph: '←'},
	{Pattern: "arrow.lr", Glyph: '↔'},
	{Pattern: "arrow.r.double", Glyph: '⇒'},
	{Pattern: "arrow.l.double", Glyph: '⇐'},
	{Pattern: "arrow.lr.double", Glyph: '⇔'},
	{Pattern: "arrow.r.bar", Glyph: '↦'},
	{Pattern: "arrow.r.hook", Glyph: '↪'},
	{Pattern: "arrow.l.hook", Glyph: '↩'},
	{Pattern: "arrow.r.twohead", Glyph: '↠'},
	{Pattern: "arrow.r.tail", Glyph: '↣'},
	{Pattern: "arrow.t", Glyph: '↑'},
	{Pattern: "arrow.b", Glyph: '↓'},
	{Pattern: "arrow.t.double", Glyph: '⇑'},
	{Pattern: "arrow.b.double", Glyph: '⇓'},
	{Pattern: "arrow.tb", Glyph: '↕'},
	{Pattern: "arrow.tb.double", Glyph: '⇕'},
	{Pattern: "arrow.tr", Glyph: '↗'},
	{Pattern: "arrow.br", Glyph: '↘'},
	{Pattern: "arrow.tl", Glyph: '↖'},
	{Pattern: "arrow.bl", Glyph: '↙'},
}

// Operators contains math operators, relations, logic, and set symbols.
var Operators = []SymbolRule{
	{Pattern: "<=", Glyph: '≤'},
	{Pattern: ">=", Glyph: '≥'},
	{Pattern: "!=", Glyph: '≠'},
	{Pattern: "~=", Glyph: '≅'},
	{Pattern: "+-", Glyph: '±'},
	{Pattern: "-+", Glyph: '∓'},
	{Pattern: "<<", Glyph: '≪'},
	{Pattern: ">>", Glyph: '≫'},
	{Pattern: ":=", Glyph: '≔'},
	{Pattern: "||", Glyph: '‖'},
	{Pattern: "approx", Glyph: '≈'},
	{Pattern: "approx.not", Glyph: '≉'},
	{Pattern: "eq.not", Glyph: '≠'},
	{Pattern: "equiv", Glyph: '≡'},
	{Pattern: "equiv.not", Glyph: '≢'},
	{Pattern: "times", Glyph: '×'},
	{Pattern: "dot", Glyph: '·'},
	{Pattern: "dot.c", Glyph: '⋅'},
	{Pattern: "dots", Glyph: '…'},
	{Pattern: "dots.c", Glyph: '⋯'},
	{Pattern: "dots.h", Glyph: '…'},
	{Pattern: "dots.v", Glyph: '⋮'},
	{Pattern: "dots.down", Glyph: '⋱'},
	{Pattern: "plus.minus", Glyph: '±'},
	{Pattern: "minus.plus", Glyph: '∓'},
	{Pattern: "plus.circle", Glyph: '⊕'},
	{Pattern: "times.circle", Glyph: '⊗'},
	{Pattern: "infinity", Glyph: '∞'},
	{Pattern: "oo", Glyph: '∞'},
	{Pattern: "in", Glyph: '∈'},
	{Pattern: "in.not", Glyph: '∉'},
	{Pattern: "in.rev", Glyph: '∋'},
	{Pattern: "in.rev.not", Glyph: '∌'},
	{Pattern: "notin", Glyph: '∉'},
	{Pattern: "subset", Glyph: '⊂'},
	{Pattern: "subset.not", Glyph: '⊄'},
	{Pattern: "subseteq", Glyph: '⊆'},
	{Pattern: "subset.eq", Glyph: '⊆'},
	{Pattern: "subset.eq.not", Glyph: '⊈'},
	{Pattern: "subset.neq", Glyph: '⊊'},
	{Pattern: "supset", Glyph: '⊃'},
	{Pattern: "supset.not", Glyph: '⊅'},
	{Pattern: "supseteq", Glyph: '⊇'},
	{Pattern: "supset.eq", Glyph: '⊇'},
	{Pattern: "supset.eq.not", Glyph: '⊉'},
	{Pattern: "supset.neq", Glyph: '⊋'},
	{Pattern: "union", Glyph: '∪'},
	{Pattern: "sect", Glyph: '∩'},
	{Pattern: "inter", Glyph: '∩'},
	{Pattern: "emptyset", Glyph: '∅'},
	{Pattern: "without", Glyph: '∖'},
	{Pattern: "forall", Glyph: '∀'},
	{Pattern: "exists", Glyph: '∃'},
	{Pattern: "exists.not", Glyph: '∄'},
	{Pattern: "nabla", Glyph: '∇'},
	{Pattern: "partial", Glyph: '∂'},
	{Pattern: "prop", Glyph: '∝'},
	{Pattern: "perp", Glyph: '⊥'},
	{Pattern: "degree", Glyph: '°'},
	{Pattern: "hbar", Glyph: 'ℏ'},
	{Pattern: "ell", Glyph: 'ℓ'},
	{Pattern: "aleph", Glyph: 'ℵ'},
	{Pattern: "NN", Glyph: 'ℕ'},
	{Pattern: "ZZ", Glyph: 'ℤ'},
	{Pattern: "QQ", Glyph: 'ℚ'},
	{Pattern: "RR", Glyph: 'ℝ'},
	{Pattern: "CC", Glyph: 'ℂ'},
	{Pattern: "and", Glyph: '∧'},
	{Pattern: "or", Glyph: '∨'},
	{Pattern: "not", Glyph: '¬'},
	{Pattern: "xor", Glyph: '⊕'},
	{Pattern: "top", Glyph: '⊤'},
	{Pattern: "bot", Glyph: '⊥'},
	{Pattern: "tack", Glyph: '⊢'},
	{Pattern: "tack.r", Glyph: '⊢'},
	{Pattern: "tack.l", Glyph: '⊣'},
	{Pattern: "tack.r.not", Glyph: '⊬'},
	{Pattern: "tack.r.double", Glyph: '⊨'},
	{Pattern: "models", Glyph: '⊨'},
	{Pattern: "therefore", Glyph: '∴'},
	{Pattern: "because", Glyph: '∵'},
	{Pattern: "qed", Glyph: '∎'},
	{Pattern: "angle", Glyph: '∠'},
	{Pattern: "angle.l", Glyph: '⟨'},
	{Pattern: "angle.r", Glyph: '⟩'},
	{Pattern: "chevron.l", Glyph: '⟨'},
	{Pattern: "chevron.r", Glyph: '⟩'},
	{Pattern: "floor.l", Glyph: '⌊'},
	{Pattern: "floor.r", Glyph: '⌋'},
	{Pattern: "ceil.l", Glyph: '⌈'},
	{Pattern: "ceil.r", Glyph: '⌉'},
	{Pattern: "div", Glyph: '÷'},
	{Pattern: "ast", Glyph: '∗'},
	{Pattern: "star", Glyph: '⋆'},
	{Pattern: "bullet", Glyph: '•'},
	{Pattern: "compose", Glyph: '∘'},
	{Pattern: "circle.tiny", Glyph: '∘'},
	{Pattern: "tilde.op", Glyph: '∼'},
	{Pattern: "tilde.eq", Glyph: '≃'},
	{Pattern: "tilde.equiv", Glyph: '≅'},
	{Pattern: "approx.eq", Glyph: '≊'},
	{Pattern: "prec", Glyph: '≺'},
	{Pattern: "prec.eq", Glyph: '≼'},
	{Pattern: "succ", Glyph: '≻'},
	{Pattern: "succ.eq", Glyph: '≽'},
	{Pattern: "lt.eq", Glyph: '≤'},
	{Pattern: "gt.eq", Glyph: '≥'},
	{Pattern: "lt.double", Glyph: '≪'},
	{Pattern: "gt.double", Glyph: '≫'},
	{Pattern: "parallel", Glyph: '∥'},
	{Pattern: "divides", Glyph: '∣'},
	{Pattern: "divides.not", Glyph: '∤'},
	{Pattern: "dagger", Glyph: '†'},
	{Pattern: "dagger.double", Glyph: '‡'},
	{Pattern: "planck.reduce", Glyph: 'ℏ'},
	{Pattern: "Re", Glyph: 'ℜ'},
	{Pattern: "Im", Glyph: 'ℑ'},
}

// BigOperators contains summation, product, integral, and big set operators.
var BigOperators = []SymbolRule{
	{Pattern: "sum", Glyph: '∑'},
	{Pattern: "product", Glyph: '∏'},
	{Pattern: "coproduct", Glyph: '∐'},
	{Pattern: "integral", Glyph: '∫'},
	{Pattern: "integral.double", Glyph: '∬'},
	{Pattern: "integral.triple", Glyph: '∭'},
	{Pattern: "integral.cont", Glyph: '∮'},
	{Pattern: "union.big", Glyph: '⋃'},
	{Pattern: "sect.big", Glyph: '⋂'},
	{Pattern: "inter.big", Glyph: '⋂'},
}

// Superscripts maps single-character math superscripts (^c) to Unicode.
var Superscripts = []SymbolRule{
	{Pattern: "^0", Glyph: '⁰'},
	{Pattern: "^1", Glyph: '¹'},
	{Pattern: "^2", Glyph: '²'},
	{Pattern: "^3", Glyph: '³'},
	{Pattern: "^4", Glyph: '⁴'},
	{Pattern: "^5", Glyph: '⁵'},
	{Pattern: "^6", Glyph: '⁶'},
	{Pattern: "^7", Glyph: '⁷'},
	{Pattern: "^8", Glyph: '⁸'},
	{Pattern: "^9", Glyph: '⁹'},
	{Pattern: "^i", Glyph: 'ⁱ'},
	{Pattern: "^m", Glyph: 'ᵐ'},
	{Pattern: "^n", Glyph: 'ⁿ'},
}

// Subscripts maps single-character math subscripts (_c) to Unicode.
var Subscripts = []SymbolRule{
	{Pattern: "_0", Glyph: '₀'},
	{Pattern: "_1", Glyph: '₁'},
	{Pattern: "_2", Glyph: '₂'},
	{Pattern: "_3", Glyph: '₃'},
	{Pattern: "_4", Glyph: '₄'},
	{Pattern: "_5", Glyph: '₅'},
	{Pattern: "_6", Glyph: '₆'},
	{Pattern: "_7", Glyph: '₇'},
	{Pattern: "_8", Glyph: '₈'},
	{Pattern: "_9", Glyph: '₉'},
	{Pattern: "_i", Glyph: 'ᵢ'},
	{Pattern: "_j", Glyph: 'ⱼ'},
	{Pattern: "_k", Glyph: 'ₖ'},
	{Pattern: "_m", Glyph: 'ₘ'},
	{Pattern: "_n", Glyph: 'ₙ'},
}

// MarkupTypography contains safe typographic replacements for Markup mode.
var MarkupTypography = []SymbolRule{
	{Pattern: "---", Glyph: '—'},
	{Pattern: "--", Glyph: '–'},
	{Pattern: "...", Glyph: '…'},
}

type symbolEntry struct {
	pattern      string
	runes        []rune
	glyph        rune
	wordBoundary bool
}

var (
	mathBuckets   map[rune][]symbolEntry
	superMap      map[rune]rune
	subMap        map[rune]rune
	superParenMap map[rune]rune
	subParenMap   map[rune]rune
	bbMap         map[rune]rune
	calMap        map[rune]rune
)

func init() {
	mathBuckets = make(map[rune][]symbolEntry)
	addMathRules := func(rules []SymbolRule) {
		for _, rule := range rules {
			rs := []rune(rule.Pattern)
			if len(rs) == 0 {
				continue
			}
			wb := util.IsWordChar(rs[0]) || util.IsWordChar(rs[len(rs)-1])
			entry := symbolEntry{
				pattern:      rule.Pattern,
				runes:        rs,
				glyph:        rule.Glyph,
				wordBoundary: wb,
			}
			mathBuckets[rs[0]] = append(mathBuckets[rs[0]], entry)
		}
	}
	addMathRules(GreekLower)
	addMathRules(GreekUpper)
	addMathRules(Arrows)
	addMathRules(Operators)
	addMathRules(BigOperators)

	for first, list := range mathBuckets {
		sort.Slice(list, func(i, j int) bool {
			if len(list[i].runes) != len(list[j].runes) {
				return len(list[i].runes) > len(list[j].runes)
			}
			return list[i].pattern < list[j].pattern
		})
		mathBuckets[first] = list
	}

	superMap = make(map[rune]rune, len(Superscripts))
	for _, rule := range Superscripts {
		rs := []rune(rule.Pattern)
		if len(rs) == 2 && rs[0] == '^' {
			superMap[rs[1]] = rule.Glyph
		}
	}

	subMap = make(map[rune]rune, len(Subscripts))
	for _, rule := range Subscripts {
		rs := []rune(rule.Pattern)
		if len(rs) == 2 && rs[0] == '_' {
			subMap[rs[1]] = rule.Glyph
		}
	}

	superParenMap = map[rune]rune{
		'0': '⁰', '1': '¹', '2': '²', '3': '³', '4': '⁴',
		'5': '⁵', '6': '⁶', '7': '⁷', '8': '⁸', '9': '⁹',
		'+': '⁺', '-': '⁻', '=': '⁼',
		'a': 'ᵃ', 'b': 'ᵇ', 'c': 'ᶜ', 'd': 'ᵈ', 'e': 'ᵉ',
		'f': 'ᶠ', 'g': 'ᵍ', 'h': 'ʰ', 'i': 'ⁱ', 'j': 'ʲ',
		'k': 'ᵏ', 'l': 'ˡ', 'm': 'ᵐ', 'n': 'ⁿ', 'o': 'ᵒ',
		'p': 'ᵖ', 'r': 'ʳ', 's': 'ˢ', 't': 'ᵗ', 'u': 'ᵘ',
		'v': 'ᵛ', 'w': 'ʷ', 'x': 'ˣ', 'y': 'ʸ', 'z': 'ᶻ',
	}

	subParenMap = map[rune]rune{
		'0': '₀', '1': '₁', '2': '₂', '3': '₃', '4': '₄',
		'5': '₅', '6': '₆', '7': '₇', '8': '₈', '9': '₉',
		'+': '₊', '-': '₋', '=': '₌',
		'a': 'ₐ', 'e': 'ₑ', 'h': 'ₕ', 'i': 'ᵢ', 'j': 'ⱼ',
		'k': 'ₖ', 'l': 'ₗ', 'm': 'ₘ', 'n': 'ₙ', 'o': 'ₒ',
		'p': 'ₚ', 'r': 'ᵣ', 's': 'ₛ', 't': 'ₜ', 'u': 'ᵤ',
		'v': 'ᵥ', 'x': 'ₓ',
	}

	// Blackboard-bold (double-struck) uppercase A-Z and digits 0-9 for bb(...).
	bbMap = map[rune]rune{
		'A': '𝔸', 'B': '𝔹', 'C': 'ℂ', 'D': '𝔻', 'E': '𝔼', 'F': '𝔽', 'G': '𝔾',
		'H': 'ℍ', 'I': '𝕀', 'J': '𝕁', 'K': '𝕂', 'L': '𝕃', 'M': '𝕄', 'N': 'ℕ',
		'O': '𝕆', 'P': 'ℙ', 'Q': 'ℚ', 'R': 'ℝ', 'S': '𝕊', 'T': '𝕋', 'U': '𝕌',
		'V': '𝕍', 'W': '𝕎', 'X': '𝕏', 'Y': '𝕐', 'Z': 'ℤ',
		'0': '𝟘', '1': '𝟙', '2': '𝟚', '3': '𝟛', '4': '𝟜',
		'5': '𝟝', '6': '𝟞', '7': '𝟟', '8': '𝟠', '9': '𝟡',
	}

	// Calligraphic / mathematical script uppercase A-Z for cal(...).
	calMap = map[rune]rune{
		'A': '𝒜', 'B': 'ℬ', 'C': '𝒞', 'D': '𝒟', 'E': 'ℰ', 'F': 'ℱ', 'G': '𝒢',
		'H': 'ℋ', 'I': 'ℐ', 'J': '𝒥', 'K': '𝒦', 'L': 'ℒ', 'M': 'ℳ', 'N': '𝒩',
		'O': '𝒪', 'P': '𝒫', 'Q': '𝒬', 'R': 'ℛ', 'S': '𝒮', 'T': '𝒯', 'U': '𝒰',
		'V': '𝒱', 'W': '𝒲', 'X': '𝒳', 'Y': '𝒴', 'Z': '𝒵',
	}
}

// LookupSymbol looks up a Typst math or markup pattern in the curated tables.
func LookupSymbol(pattern string) (rune, bool) {
	if r, ok := lookupInSlice(GreekLower, pattern); ok {
		return r, true
	}
	if r, ok := lookupInSlice(GreekUpper, pattern); ok {
		return r, true
	}
	if r, ok := lookupInSlice(Arrows, pattern); ok {
		return r, true
	}
	if r, ok := lookupInSlice(Operators, pattern); ok {
		return r, true
	}
	if r, ok := lookupInSlice(BigOperators, pattern); ok {
		return r, true
	}
	if r, ok := lookupInSlice(Superscripts, pattern); ok {
		return r, true
	}
	if r, ok := lookupInSlice(Subscripts, pattern); ok {
		return r, true
	}
	if r, ok := lookupInSlice(MarkupTypography, pattern); ok {
		return r, true
	}
	return 0, false
}

func lookupInSlice(rules []SymbolRule, pattern string) (rune, bool) {
	for _, rule := range rules {
		if rule.Pattern == pattern {
			return rule.Glyph, true
		}
	}
	return 0, false
}
