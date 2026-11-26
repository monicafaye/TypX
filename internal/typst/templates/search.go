package templates

import (
	"sort"
	"strings"
	"unicode"
)

// SearchResult holds a matched template, its relevance score, and the rune
// indices in Template.Name that matched the query for UI highlighting.
type SearchResult struct {
	Template    Template
	Score       int
	NameMatches []int
}

type searchTerm struct {
	field string
	value string
}

// SearchTemplates filters and ranks templates against query.
// Queries support case-insensitive multi-term AND matching across name, title,
// description, categories, keywords, disciplines, authors, package spec, and
// source, optional field qualifiers (cat:, tag:, author:, src:), and fuzzy
// subsequence fallback on template names.
func SearchTemplates(items []Template, query string) []SearchResult {
	terms, plainPhrase := parseSearchQuery(query)
	if len(terms) == 0 {
		out := make([]SearchResult, len(items))
		for i, t := range items {
			out[i] = SearchResult{Template: t}
		}
		return out
	}

	out := make([]SearchResult, 0, len(items))
	for _, t := range items {
		score, nameMatches, ok := scoreTemplate(t, terms, plainPhrase)
		if !ok {
			continue
		}
		out = append(out, SearchResult{
			Template:    t,
			Score:       score,
			NameMatches: nameMatches,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		ni := strings.ToLower(out[i].Template.Name)
		nj := strings.ToLower(out[j].Template.Name)
		if ni != nj {
			return ni < nj
		}
		return out[i].Template.ID < out[j].Template.ID
	})
	return out
}

func parseSearchQuery(raw string) ([]searchTerm, string) {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return nil, ""
	}
	terms := make([]searchTerm, 0, len(fields))
	var plainParts []string
	for _, tok := range fields {
		lower := strings.ToLower(tok)
		if colon := strings.IndexByte(lower, ':'); colon > 0 && colon < len(lower)-1 && !strings.HasPrefix(lower, "@") {
			if canonical, ok := canonicalSearchField(lower[:colon]); ok {
				val := lower[colon+1:]
				terms = append(terms, searchTerm{field: canonical, value: val})
				continue
			}
		}
		terms = append(terms, searchTerm{value: lower})
		plainParts = append(plainParts, lower)
	}
	phrase := ""
	if len(plainParts) > 1 {
		phrase = strings.Join(plainParts, " ")
	}
	return terms, phrase
}

func canonicalSearchField(raw string) (string, bool) {
	switch raw {
	case "cat", "category", "categories":
		return "cat", true
	case "tag", "tags", "kw", "keyword", "keywords":
		return "tag", true
	case "author", "authors", "by":
		return "author", true
	case "src", "source":
		return "src", true
	default:
		return "", false
	}
}

func scoreTemplate(t Template, terms []searchTerm, plainPhrase string) (int, []int, bool) {
	nameRunes := toLowerRunes(t.Name)
	var nameHitMask []bool
	if len(nameRunes) > 0 {
		nameHitMask = make([]bool, len(nameRunes))
	}

	totalScore := 0
	for _, term := range terms {
		if term.field != "" {
			s := scoreFieldTerm(t, term.field, term.value)
			if s <= 0 {
				return 0, nil, false
			}
			totalScore += s
			continue
		}
		s, matchedIdxs := scorePlainTerm(t, nameRunes, term.value)
		if s <= 0 {
			return 0, nil, false
		}
		totalScore += s
		for _, idx := range matchedIdxs {
			if idx >= 0 && idx < len(nameHitMask) {
				nameHitMask[idx] = true
			}
		}
	}

	if plainPhrase != "" {
		if strings.Contains(strings.ToLower(t.Name), plainPhrase) ||
			strings.Contains(strings.ToLower(t.Title), plainPhrase) ||
			strings.Contains(strings.ToLower(t.Description), plainPhrase) {
			totalScore += 50
		}
	}

	var nameMatches []int
	for i, hit := range nameHitMask {
		if hit {
			nameMatches = append(nameMatches, i)
		}
	}
	return totalScore, nameMatches, true
}

func scoreFieldTerm(t Template, field, q string) int {
	switch field {
	case "cat":
		best := scoreTextList([]string{t.Category}, q, 300, 220, 150)
		if s := scoreTextList(t.Categories, q, 300, 220, 150); s > best {
			best = s
		}
		return best
	case "tag":
		best := scoreTextList(t.Keywords, q, 300, 220, 150)
		if s := scoreTextList(t.Disciplines, q, 280, 200, 140); s > best {
			best = s
		}
		return best
	case "author":
		return scoreTextList(t.Authors, q, 280, 220, 150)
	case "src":
		return scoreSourceMatch(t.Source, q)
	default:
		return 0
	}
}

func scoreSourceMatch(src SourceKind, q string) int {
	candidates := []string{src.String(), src.SectionTitle()}
	switch src {
	case SourceProject:
		candidates = append(candidates, "project")
	case SourcePersonal:
		candidates = append(candidates, "personal", "user")
	case SourceLocalPkg:
		candidates = append(candidates, "local", "@local")
	case SourceCachedPkg:
		candidates = append(candidates, "cached", "preview", "@preview")
	case SourceUniverse:
		candidates = append(candidates, "universe", "preview", "@preview", "online")
	}
	return scoreTextList(candidates, q, 260, 200, 140)
}

func scorePlainTerm(t Template, nameRunes []rune, q string) (int, []int) {
	qRunes := toLowerRunes(q)
	best := 0
	var matchedName []int

	// 1. Template name (highest weight + highlight spans).
	if len(nameRunes) > 0 && len(qRunes) > 0 {
		if runesEqual(nameRunes, qRunes) {
			best = 1000
			matchedName = rangeSlice(0, len(nameRunes))
		} else if runesHasPrefix(nameRunes, qRunes) {
			best = 700
			matchedName = rangeSlice(0, len(qRunes))
		} else if idx, atBoundary := findRuneSubstr(nameRunes, qRunes); idx >= 0 {
			if atBoundary {
				best = 550
			} else {
				best = 450
			}
			matchedName = rangeSlice(idx, idx+len(qRunes))
		}
	}

	// 2. Exact ID or PackageSpec match.
	if s := scoreString(t.ID, q, 900, 320, 280); s > best {
		best = s
	}
	if s := scoreString(t.PackageSpec, q, 900, 320, 280); s > best {
		best = s
	}

	// 3. Title.
	if s := scoreStringWithBoundary(t.Title, q, 650, 420, 360, 300); s > best {
		best = s
	}

	// 4. Categories, keywords, disciplines.
	if s := scoreTextList([]string{t.Category}, q, 340, 260, 200); s > best {
		best = s
	}
	if s := scoreTextList(t.Categories, q, 340, 260, 200); s > best {
		best = s
	}
	if s := scoreTextList(t.Keywords, q, 320, 240, 180); s > best {
		best = s
	}
	if s := scoreTextList(t.Disciplines, q, 300, 220, 160); s > best {
		best = s
	}

	// 5. Description.
	if s := scoreStringWithBoundary(t.Description, q, 200, 160, 140, 100); s > best {
		best = s
	}

	// 6. Authors & source.
	if s := scoreTextList(t.Authors, q, 140, 110, 90); s > best {
		best = s
	}
	if s := scoreSourceMatch(t.Source, q); s > 0 {
		// Plain source token ranks below name/title/category/description matches.
		if 80 > best {
			best = 80
		}
	}

	// 7. Fuzzy subsequence fallback on Name when no direct match succeeded.
	if best == 0 && len(qRunes) >= 2 && len(nameRunes) >= len(qRunes) {
		if score, idxs, ok := fuzzySubsequenceMatch(nameRunes, qRunes); ok {
			best = score
			matchedName = idxs
		}
	}

	return best, matchedName
}

func scoreTextList(items []string, q string, exactScore, prefixScore, substrScore int) int {
	best := 0
	for _, it := range items {
		if s := scoreString(it, q, exactScore, prefixScore, substrScore); s > best {
			best = s
		}
	}
	return best
}

func scoreString(text, q string, exactScore, prefixScore, substrScore int) int {
	if text == "" || q == "" {
		return 0
	}
	lower := strings.ToLower(text)
	if lower == q {
		return exactScore
	}
	if strings.HasPrefix(lower, q) {
		return prefixScore
	}
	if strings.Contains(lower, q) {
		return substrScore
	}
	return 0
}

func scoreStringWithBoundary(text, q string, exactScore, prefixScore, boundaryScore, substrScore int) int {
	if text == "" || q == "" {
		return 0
	}
	tRunes := toLowerRunes(text)
	qRunes := toLowerRunes(q)
	if runesEqual(tRunes, qRunes) {
		return exactScore
	}
	if runesHasPrefix(tRunes, qRunes) {
		return prefixScore
	}
	if idx, atBoundary := findRuneSubstr(tRunes, qRunes); idx >= 0 {
		if atBoundary {
			return boundaryScore
		}
		return substrScore
	}
	return 0
}

func toLowerRunes(s string) []rune {
	runes := []rune(s)
	out := make([]rune, len(runes))
	for i, r := range runes {
		out[i] = unicode.ToLower(r)
	}
	return out
}

func runesEqual(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func runesHasPrefix(s, prefix []rune) bool {
	if len(prefix) > len(s) {
		return false
	}
	for i := range prefix {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}

func isWordSep(r rune) bool {
	return r == '-' || r == '_' || r == '/' || r == ' ' || r == '.' || r == ':'
}

// findRuneSubstr finds needle in haystack, preferring a match at a word boundary.
func findRuneSubstr(haystack, needle []rune) (idx int, atBoundary bool) {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1, false
	}
	firstAny := -1
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		if i == 0 || isWordSep(haystack[i-1]) {
			return i, true
		}
		if firstAny < 0 {
			firstAny = i
		}
	}
	return firstAny, false
}

// fuzzySubsequenceMatch checks whether all runes of needle appear in order inside
// haystack, preferring word-boundary starts and consecutive character runs while
// guaranteeing that a valid match is never dropped by greedy lookahead.
func fuzzySubsequenceMatch(haystack, needle []rune) (int, []int, bool) {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return 0, nil, false
	}

	// Backward pass computes the latest feasible index in haystack for each needle rune.
	lastPos := make([]int, len(needle))
	hi := len(haystack) - 1
	for ni := len(needle) - 1; ni >= 0; ni-- {
		found := -1
		for i := hi; i >= ni; i-- {
			if haystack[i] == needle[ni] {
				found = i
				break
			}
		}
		if found < 0 {
			return 0, nil, false
		}
		lastPos[ni] = found
		hi = found - 1
	}

	idxs := make([]int, 0, len(needle))
	lo := 0
	for ni, nr := range needle {
		limit := lastPos[ni]
		found := -1
		for i := lo; i <= limit; i++ {
			if haystack[i] != nr {
				continue
			}
			if found < 0 {
				found = i
			}
			isConsec := ni > 0 && i == idxs[ni-1]+1
			isBoundary := i == 0 || isWordSep(haystack[i-1])
			if isConsec || isBoundary {
				found = i
				break
			}
		}
		idxs = append(idxs, found)
		lo = found + 1
	}

	score := 40
	for i, pos := range idxs {
		if pos == 0 || isWordSep(haystack[pos-1]) {
			score += 12
		}
		if i > 0 {
			gap := pos - idxs[i-1] - 1
			if gap == 0 {
				score += 10
			} else {
				score -= gap
			}
		}
	}
	if score < 10 {
		score = 10
	}
	return score, idxs, true
}

func rangeSlice(start, end int) []int {
	if end <= start {
		return nil
	}
	out := make([]int, end-start)
	for i := range out {
		out[i] = start + i
	}
	return out
}
