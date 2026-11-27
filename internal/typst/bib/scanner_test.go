package bib

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/monicafaye/TypX/internal/buffer"
)

func TestScanBibliographyCalls(t *testing.T) {
	doc := "\xef\xbb\xbf" + strings.Join([]string{
		`#bibliography("refs.bib")`,
		`#bibliography(("a.bib", "b.yml",), style: "ieee")`,
		`#bibliography(`,
		`  // comment inside call`,
		`  ("multi/one.bib", /* nested /* block */ */ "multi/two.yaml"),`,
		`  full: true,`,
		`)`,
		`// #bibliography("line_comment.bib")`,
		`/* #bibliography("block_comment.bib") /* nested */ */`,
		"`#bibliography(\"inline_raw.bib\")`",
		"````typ",
		"```",
		`#bibliography("fenced_raw.bib")`,
		"```",
		"````",
		`"#bibliography(\"string_literal.bib\")"`,
		`\#bibliography("escaped.bib")`,
		`$ #bibliography("math.bib") $`,
		`#bibliography_custom("not_bib.bib")`,
		`#bibliography("tail\"quote\\dir.bib")`,
	}, "\r\n")

	got := ScanBibliographyCalls([]byte(doc))
	want := []string{
		"refs.bib",
		"a.bib",
		"b.yml",
		"multi/one.bib",
		"multi/two.yaml",
		`tail"quote\dir.bib`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanBibliographyCalls mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestResolveBibliographyFilesRootMainOpenBufferAndFallback(t *testing.T) {
	dir := t.TempDir()
	rootBib := filepath.Join(dir, "bib", "root.bib")
	subBib := filepath.Join(dir, "chapters", "local.bib")
	fallbackAlpha := filepath.Join(dir, "alpha.bib")
	fallbackZeta := filepath.Join(dir, "zeta.bib")
	mainPath := filepath.Join(dir, "main.typ")
	chPath := filepath.Join(dir, "chapters", "ch1.typ")

	for _, p := range []string{rootBib, subBib, fallbackAlpha, fallbackZeta} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("@misc{k, title={T}, year={2024}}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(mainPath, []byte(`#bibliography("/bib/root.bib")`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	SetRootResolver(func(string) string { return dir })
	SetMainFileResolver(func(string) string { return mainPath })
	t.Cleanup(func() {
		SetRootResolver(nil)
		SetMainFileResolver(nil)
		buffer.CloseOpenBuffers()
	})

	// Unsaved edit in main.typ open in buffer.OpenBuffers must override disk main.typ.
	mainBuf := buffer.NewBufferFromString(`#bibliography(("/bib/root.bib", "chapters/local.bib"))`+"\n", mainPath, buffer.BTDefault)
	_ = mainBuf

	chBuf := buffer.NewBufferFromString("= Chapter 1\n", chPath, buffer.BTDefault)
	got := ResolveBibliographyFiles(chBuf)
	want := []string{filepath.Clean(rootBib), filepath.Clean(subBib)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveBibliographyFiles with pinned open main mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}

	// When no #bibliography call exists in active buffer or pinned main file, fallback to sorted *.bib in rootDir.
	mainBuf.Replace(mainBuf.Start(), mainBuf.End(), "= Plain Main\n")
	gotFallback := ResolveBibliographyFiles(chBuf)
	wantFallback := []string{filepath.Clean(fallbackAlpha), filepath.Clean(fallbackZeta)}
	if !reflect.DeepEqual(gotFallback, wantFallback) {
		t.Fatalf("ResolveBibliographyFiles root *.bib fallback mismatch:\ngot:  %#v\nwant: %#v", gotFallback, wantFallback)
	}
}

func TestScanBibliographyCallsMarkupQuotesAndLeadingNamedArgs(t *testing.T) {
	t.Run("unmatched quote in markup prose does not hide bibliography", func(t *testing.T) {
		doc := strings.Join([]string{
			`= Hardware`,
			`We tested a 15" display.`,
			`The #widget has a 15" screen.`,
			`#emph[Another 15" display]`,
			`#let s = "#bibliography(\"fake.bib\")"`,
			`#let multi = "line1`,
			`#bibliography(\"fake_multiline.bib\")"`,
			`#foo("#bibliography(\"fake_arg.bib\")")`,
			`#{ "#bibliography(\"fake_block.bib\")" }`,
			`#let content_bib = [`,
			`  #bibliography("in_content.bib")`,
			`]`,
			`#bibliography("refs.bib")`,
		}, "\n")

		got := ScanBibliographyCalls([]byte(doc))
		want := []string{"in_content.bib", "refs.bib"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ScanBibliographyCalls mismatch:\ngot:  %#v\nwant: %#v", got, want)
		}
	})

	t.Run("leading named arguments before positional source", func(t *testing.T) {
		doc := strings.Join([]string{
			`#bibliography(style: "ieee", "refs.bib")`,
			`#bibliography(title: [15" Refs], full: true, ("a.bib", "b.yml"), style: "apa")`,
			`#bibliography(title: "Bibliography", "c.bib")`,
		}, "\n")

		got := ScanBibliographyCalls([]byte(doc))
		want := []string{"refs.bib", "a.bib", "b.yml", "c.bib"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ScanBibliographyCalls leading named args mismatch:\ngot:  %#v\nwant: %#v", got, want)
		}
	})
}
