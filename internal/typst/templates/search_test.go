package templates

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchTemplatesMultiTermRankingQualifiersAndFuzzy(t *testing.T) {
	t.Parallel()

	catalog := []Template{
		{
			ID:          "personal/notes",
			Name:        "notes",
			Title:       "Study Notes",
			Description: "Clean lecture and seminar notes",
			Category:    "notes",
			Keywords:    []string{"lecture", "course"},
			Source:      SourcePersonal,
		},
		{
			ID:          "@preview/charged-ieee:0.1.4",
			Name:        "charged-ieee",
			Title:       "charged-ieee (0.1.4)",
			Description: "Two-column IEEE conference paper template",
			Category:    "paper",
			Categories:  []string{"paper", "report"},
			Keywords:    []string{"ieee", "two-column", "conference"},
			Disciplines: []string{"engineering", "computer-science"},
			Authors:     []string{"Typst GmbH <hi@typst.app>"},
			Version:     "0.1.4",
			Source:      SourceUniverse,
			PackageSpec: "@preview/charged-ieee:0.1.4",
		},
		{
			ID:          "@preview/ieee-lite:0.2.0",
			Name:        "ieee-lite",
			Title:       "ieee-lite (0.2.0)",
			Description: "Minimal article",
			Category:    "paper",
			Categories:  []string{"paper"},
			Authors:     []string{"Alice"},
			Version:     "0.2.0",
			Source:      SourceUniverse,
			PackageSpec: "@preview/ieee-lite:0.2.0",
		},
		{
			ID:          "@preview/ieee:1.0.0",
			Name:        "ieee",
			Title:       "ieee (1.0.0)",
			Description: "Exact name match",
			Category:    "paper",
			Categories:  []string{"paper"},
			Version:     "1.0.0",
			Source:      SourceUniverse,
			PackageSpec: "@preview/ieee:1.0.0",
		},
		{
			ID:          "@preview/clean-math-paper:0.3.0",
			Name:        "clean-math-paper",
			Title:       "clean-math-paper (0.3.0)",
			Description: "Academic math article citing IEEE style",
			Category:    "paper",
			Categories:  []string{"paper"},
			Keywords:    []string{"mathematics", "theorem"},
			Disciplines: []string{"mathematics"},
			Authors:     []string{"Bob"},
			Version:     "0.3.0",
			Source:      SourceUniverse,
			PackageSpec: "@preview/clean-math-paper:0.3.0",
		},
	}

	// 1. Empty query returns all items in original order with no highlights.
	all := SearchTemplates(catalog, "   ")
	require.Len(t, all, len(catalog))
	assert.Equal(t, "notes", all[0].Template.Name)
	assert.Empty(t, all[0].NameMatches)

	// 2. Ranking: exact name ("ieee") > prefix ("ieee-lite") > word-boundary ("charged-ieee") > description ("clean-math-paper").
	ranked := SearchTemplates(catalog, "ieee")
	require.Len(t, ranked, 4)
	assert.Equal(t, "ieee", ranked[0].Template.Name)
	assert.Equal(t, []int{0, 1, 2, 3}, ranked[0].NameMatches)
	assert.Equal(t, "ieee-lite", ranked[1].Template.Name)
	assert.Equal(t, []int{0, 1, 2, 3}, ranked[1].NameMatches)
	assert.Equal(t, "charged-ieee", ranked[2].Template.Name)
	assert.Equal(t, []int{8, 9, 10, 11}, ranked[2].NameMatches)
	assert.Equal(t, "clean-math-paper", ranked[3].Template.Name)
	assert.Empty(t, ranked[3].NameMatches)

	// 3. Multi-term AND search across different fields (name + keyword + discipline).
	multi := SearchTemplates(catalog, "ieee two-column engineering")
	require.Len(t, multi, 1)
	assert.Equal(t, "charged-ieee", multi[0].Template.Name)

	// 4. Field qualifiers: cat:, tag:, author:, src:.
	byCat := SearchTemplates(catalog, "cat:report")
	require.Len(t, byCat, 1)
	assert.Equal(t, "charged-ieee", byCat[0].Template.Name)

	byTag := SearchTemplates(catalog, "tag:theorem")
	require.Len(t, byTag, 1)
	assert.Equal(t, "clean-math-paper", byTag[0].Template.Name)

	byAuthor := SearchTemplates(catalog, "author:alice")
	require.Len(t, byAuthor, 1)
	assert.Equal(t, "ieee-lite", byAuthor[0].Template.Name)

	bySrc := SearchTemplates(catalog, "src:personal lecture")
	require.Len(t, bySrc, 1)
	assert.Equal(t, "notes", bySrc[0].Template.Name)

	// 5. Package spec with ':' is NOT mistaken for a field qualifier.
	bySpec := SearchTemplates(catalog, "@preview/charged-ieee:0.1.4")
	require.Len(t, bySpec, 1)
	assert.Equal(t, "charged-ieee", bySpec[0].Template.Name)

	// 6. Fuzzy subsequence fallback on Name.
	fuzzy := SearchTemplates(catalog, "cmp")
	require.Len(t, fuzzy, 1)
	assert.Equal(t, "clean-math-paper", fuzzy[0].Template.Name)
	assert.Equal(t, []int{0, 6, 11}, fuzzy[0].NameMatches)
}

func TestFuzzySubsequenceDoesNotMissValidMatchAfterBoundaryLookahead(t *testing.T) {
	t.Parallel()

	items := []Template{
		{ID: "a", Name: "xab-a"},
	}
	res := SearchTemplates(items, "ab")
	require.Len(t, res, 1)
	assert.Equal(t, []int{1, 2}, res[0].NameMatches)

	// Subsequence where first char also appears later at a word boundary.
	items2 := []Template{
		{ID: "b", Name: "xa-zb-a"},
	}
	res2 := SearchTemplates(items2, "ab")
	require.Len(t, res2, 1)
	assert.Equal(t, []int{1, 4}, res2[0].NameMatches)
}

func TestParseCommentMetadataFullTags(t *testing.T) {
	t.Parallel()

	src := "// @title: Lab Report\n// @desc: Weekly lab notes\n// @category: academic\n// @tags: physics, experiment, lab\n\n= {{title}}\n"
	meta := ParseCommentMetadataFull(src)
	assert.Equal(t, "Lab Report", meta.Title)
	assert.Equal(t, "Weekly lab notes", meta.Description)
	assert.Equal(t, "academic", meta.Category)
	assert.Equal(t, []string{"physics", "experiment", "lab"}, meta.Keywords)
	assert.Equal(t, "= {{title}}\n", meta.Body)
}
