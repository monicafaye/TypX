package conceal

import "testing"

func TestSymbolCatalogCoverageAndCategories(t *testing.T) {
	cat := SymbolCatalog()
	if len(cat) == 0 {
		t.Fatal("SymbolCatalog() returned empty slice")
	}

	validCats := map[string]bool{
		"greek":         true,
		"arrows":        true,
		"relations":     true,
		"sets":          true,
		"logic":         true,
		"brackets":      true,
		"operators":     true,
		"big-operators": true,
	}

	seen := make(map[string]CatalogSymbol, len(cat))
	catCounts := make(map[string]int, len(validCats))
	for _, sym := range cat {
		if sym.Name == "" {
			t.Fatal("SymbolCatalog() contains entry with empty Name")
		}
		if sym.Glyph == 0 {
			t.Fatalf("SymbolCatalog() entry %q has zero Glyph", sym.Name)
		}
		if !validCats[sym.Category] {
			t.Fatalf("SymbolCatalog() entry %q has unexpected Category %q", sym.Name, sym.Category)
		}
		if prev, dup := seen[sym.Name]; dup {
			t.Fatalf("duplicate symbol %q in SymbolCatalog() (%+v vs %+v)", sym.Name, prev, sym)
		}
		seen[sym.Name] = sym
		catCounts[sym.Category]++
	}

	for wantCat := range validCats {
		if catCounts[wantCat] == 0 {
			t.Errorf("category %q has 0 entries in SymbolCatalog()", wantCat)
		}
	}

	checkRules := func(group string, rules []SymbolRule) {
		t.Helper()
		for _, r := range rules {
			if isIdentifierSymbol(r.Pattern) {
				got, ok := seen[r.Pattern]
				if !ok {
					t.Errorf("%s identifier %q missing from SymbolCatalog()", group, r.Pattern)
					continue
				}
				if got.Glyph != r.Glyph {
					t.Errorf("%s %q glyph = %q, want %q", group, r.Pattern, got.Glyph, r.Glyph)
				}
			} else {
				if _, ok := seen[r.Pattern]; ok {
					t.Errorf("%s shorthand %q should not appear in SymbolCatalog()", group, r.Pattern)
				}
			}
		}
	}

	checkRules("GreekLower", GreekLower)
	checkRules("GreekUpper", GreekUpper)
	checkRules("Arrows", Arrows)
	checkRules("Operators", Operators)
	checkRules("BigOperators", BigOperators)
}

func TestSymbolCatalogDefensiveCopy(t *testing.T) {
	first := SymbolCatalog()
	if len(first) == 0 {
		t.Fatal("SymbolCatalog() returned empty slice")
	}
	origName := first[0].Name
	origGlyph := first[0].Glyph
	first[0].Name = "mutated"
	first[0].Glyph = '?'

	second := SymbolCatalog()
	if second[0].Name != origName || second[0].Glyph != origGlyph {
		t.Fatalf("SymbolCatalog() shared backing array: got %+v, want Name=%q Glyph=%q", second[0], origName, origGlyph)
	}
}
