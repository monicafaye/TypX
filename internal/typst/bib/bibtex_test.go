package bib

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseBibTeX(t *testing.T) {
	bibData := "\xef\xbb\xbf" + strings.Join([]string{
		"Outside commentary @fake ignored here.",
		`@comment{ @article{ignored_comment, title = {Should Not Appear}, year = {2099}} }`,
		`@preamble{ "\providecommand{\noopsort}[1]{}" }`,
		`@string{icml = "ICML"}`,
		`@string{pub = "Proceedings of " # icml}`,
		``,
		`@article{vaswani2017,`,
		`  author = {Vaswani, Ashish and Shazeer, Noam and Parmar, Niki},`,
		`  title = {{Attention Is All You Need} in {NLP}},`,
		`  year = {2017},`,
		`}`,
		``,
		`@book(knuth1984,`,
		`  editor = "Donald E. Knuth",`,
		`  title = "The {\"{Q}uote\"} \& {Nested {Braces}} (Vol. 1)",`,
		`  date = {1984-05-01},`,
		`)`,
		``,
		`@inproceedings{macro2023,`,
		`  author = {{World Health Organization} and Doe, Jane AND Smith, John},`,
		`  title = pub # ": Neural Nets",`,
		`  year = 2023,`,
		`}`,
		``,
		`@misc{empty_year, title = {No Year Entry}}`,
	}, "\n")

	got := ParseBibTeX([]byte(bibData))
	want := []Entry{
		{
			Key:     "vaswani2017",
			Type:    "article",
			Title:   "Attention Is All You Need in NLP",
			Authors: "Vaswani, Ashish; Shazeer, Noam; Parmar, Niki",
			Year:    "2017",
		},
		{
			Key:     "knuth1984",
			Type:    "book",
			Title:   `The "Quote" & Nested Braces (Vol. 1)`,
			Authors: "Donald E. Knuth",
			Year:    "1984",
		},
		{
			Key:     "macro2023",
			Type:    "inproceedings",
			Title:   "Proceedings of ICML: Neural Nets",
			Authors: "World Health Organization; Doe, Jane; Smith, John",
			Year:    "2023",
		},
		{
			Key:   "empty_year",
			Type:  "misc",
			Title: "No Year Entry",
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseBibTeX mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestParseBibTeXUnmatchedQuotesInBracesAndComments(t *testing.T) {
	t.Run("unmatched quote inside braced field value", func(t *testing.T) {
		bibData := strings.Join([]string{
			`@article{floppy1990,`,
			`  author = {Doe, Jane},`,
			`  title = {Testing a 5" Floppy Drive},`,
			`  year = {1990},`,
			`}`,
			`@article{next2020,`,
			`  author = {Roe, John},`,
			`  title = {Next Paper},`,
			`  year = {2020},`,
			`}`,
		}, "\n")

		got := ParseBibTeX([]byte(bibData))
		want := []Entry{
			{
				Key:     "floppy1990",
				Type:    "article",
				Title:   `Testing a 5" Floppy Drive`,
				Authors: "Doe, Jane",
				Year:    "1990",
			},
			{
				Key:     "next2020",
				Type:    "article",
				Title:   "Next Paper",
				Authors: "Roe, John",
				Year:    "2020",
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ParseBibTeX braced quote mismatch:\ngot:  %#v\nwant: %#v", got, want)
		}
	})

	t.Run("unmatched quote inside @comment and braced @preamble", func(t *testing.T) {
		bibData := strings.Join([]string{
			`@comment{ ignore "unclosed quote here @article{fake, title={Fake}} }`,
			`@preamble{ {\newcommand{\inch}{5"}} }`,
			`@article{real2021,`,
			`  author = {Alice},`,
			`  title = {Real Entry},`,
			`  year = {2021},`,
			`}`,
		}, "\n")

		got := ParseBibTeX([]byte(bibData))
		want := []Entry{
			{
				Key:     "real2021",
				Type:    "article",
				Title:   "Real Entry",
				Authors: "Alice",
				Year:    "2021",
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ParseBibTeX @comment quote mismatch:\ngot:  %#v\nwant: %#v", got, want)
		}
	})
}
