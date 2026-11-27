package bib

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseHayagriva(t *testing.T) {
	yamlData := "\xef\xbb\xbf" + strings.Join([]string{
		"---",
		"# File header comment",
		"vaswani2017:",
		"  type: article",
		"  title: >",
		"    Attention Is",
		"    All You Need",
		"  author:",
		"    - Vaswani, Ashish",
		`    - { name: "Shazeer, Noam" }`,
		"    - given-name: Niki",
		"      family-name: Parmar",
		"      affiliation:",
		"        name: Google Research",
		"  date: 2017-06-12",
		"  url: https://example.com#section # inline comment",
		"  parent:",
		"    type: periodical",
		"    title: Advances in Neural Information Processing Systems",
		"    date: 1999",
		"",
		"flow_entry:",
		"  type: book",
		"  title: \"Single-line # preserved and ''ok''\"",
		"  editor: [\"Alice Smith\", { given: Bob, family: Jones }]",
		"  year: 2021",
		"",
		"multiline_quote:",
		"  type: misc",
		"  title: 'Line one",
		"    with ''single'' quotes' # comment after quote",
		"  author: Solo Author",
		"  date: 2024",
		"...",
	}, "\r\n")

	got := ParseHayagriva([]byte(yamlData))
	want := []Entry{
		{
			Key:     "vaswani2017",
			Type:    "article",
			Title:   "Attention Is All You Need",
			Authors: "Vaswani, Ashish; Shazeer, Noam; Niki Parmar",
			Year:    "2017",
		},
		{
			Key:     "flow_entry",
			Type:    "book",
			Title:   "Single-line # preserved and ''ok''",
			Authors: "Alice Smith; Bob Jones",
			Year:    "2021",
		},
		{
			Key:     "multiline_quote",
			Type:    "misc",
			Title:   "Line one with 'single' quotes",
			Authors: "Solo Author",
			Year:    "2024",
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseHayagriva mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestParseHayagrivaAuthorEdgeCases(t *testing.T) {
	yamlData := strings.Join([]string{
		"knuth1984:",
		"  type: book",
		"  author:",
		"  - Donald Knuth",
		"  - Leslie Lamport",
		"  title: Typesetting",
		"  year: 1984",
		"",
		"single_map:",
		"  type: article",
		"  author:",
		"    given-name: Ada",
		"    family-name: Lovelace",
		"    affiliation:",
		"      name: Analytical Engine",
		"  title: Notes",
		"  year: 1843",
		"",
		"affil_list:",
		"  type: article",
		"  title: Paper",
		"  author:",
		"    - name: Jane Doe",
		"      affiliation:",
		"        - name: MIT",
		"        - name: CSAIL",
		"    - affiliation:",
		"        - name: Stanford",
		"      name: John Roe",
		"  year: 2024",
		"",
		"single_editor_map:",
		"  type: proceedings",
		"  editor:",
		"    name: Grace Hopper",
		"    affiliation:",
		"      - name: Navy",
		"  title: Compilers",
		"  year: 1952",
	}, "\n")

	got := ParseHayagriva([]byte(yamlData))
	want := []Entry{
		{
			Key:     "knuth1984",
			Type:    "book",
			Title:   "Typesetting",
			Authors: "Donald Knuth; Leslie Lamport",
			Year:    "1984",
		},
		{
			Key:     "single_map",
			Type:    "article",
			Title:   "Notes",
			Authors: "Ada Lovelace",
			Year:    "1843",
		},
		{
			Key:     "affil_list",
			Type:    "article",
			Title:   "Paper",
			Authors: "Jane Doe; John Roe",
			Year:    "2024",
		},
		{
			Key:     "single_editor_map",
			Type:    "proceedings",
			Title:   "Compilers",
			Authors: "Grace Hopper",
			Year:    "1952",
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseHayagriva author edge cases mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}
