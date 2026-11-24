package preview

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/monicafaye/TypX/internal/screen"
)

func TestParsePDFAnchorsSynthetic(t *testing.T) {
	// Synthetic PDF exercising:
	//  - Nested /Pages tree with inherited MediaBox + per-page MediaBox override
	//  - /Outlines with nested children, escaped literal parens, UTF-16BE hex string,
	//    /XYZ, /FitH, and /A <</S/GoTo/D ...>> action destinations
	//  - /Names /Dests name tree with /Kids
	//  - Binary stream containing decoy "/Title(Decoy)" and "endobj" text
	pdf := `%PDF-1.7
1 0 obj
<</Type/Catalog/Pages 2 0 R/Outlines 6 0 R/Names<</Dests 20 0 R>>>>
endobj
2 0 obj
<</Type/Pages/Count 3/MediaBox[0 0 600 800]/Kids[3 0 R 4 0 R]>>
endobj
3 0 obj
<</Type/Page/Parent 2 0 R/Contents 30 0 R>>
endobj
4 0 obj
<</Type/Pages/Parent 2 0 R/Count 2/Kids[5 0 R 15 0 R]>>
endobj
5 0 obj
<</Type/Page/Parent 4 0 R/MediaBox[0 0 600 1000]/Contents 30 0 R>>
endobj
15 0 obj
<</Type/Page/Parent 4 0 R>>
endobj
30 0 obj
<</Length 42>>
stream
/Title(Decoy Heading) endobj /XYZ 0 0 0
endstream
endobj
6 0 obj
<</Type/Outlines/First 7 0 R/Last 10 0 R/Count 3>>
endobj
7 0 obj
<</Parent 6 0 R/Next 8 0 R/Title(Intro \(Part 1\))/Dest 11 0 R>>
endobj
8 0 obj
<</Parent 6 0 R/Prev 7 0 R/Next 10 0 R/First 9 0 R/Last 9 0 R/Title<FEFF65E5672C8A9E002003B1>/Dest 12 0 R>>
endobj
9 0 obj
<</Parent 8 0 R/Title(Sub Detail)/Dest[5 0 R/FitH 250]>>
endobj
10 0 obj
<</Parent 6 0 R/Prev 8 0 R/Title(Conclusion)/A<</S/GoTo/D[15 0 R/XYZ 70 600 0]>>>>
endobj
11 0 obj
[3 0 R/XYZ 70 720 0]
endobj
12 0 obj
[5 0 R/XYZ 70 800 0]
endobj
20 0 obj
<</Kids[21 0 R]>>
endobj
21 0 obj
<</Names[(sec:intro)11 0 R(fig:custom)[15 0 R/XYZ 70 200 0]]>>
endobj
%%EOF
`

	anchors := ParsePDFAnchors([]byte(pdf))
	if anchors == nil {
		t.Fatal("expected non-nil DocumentAnchors")
	}
	if len(anchors.Outlines) != 4 {
		t.Fatalf("expected 4 outline entries, got %d: %#v", len(anchors.Outlines), anchors.Outlines)
	}

	assertNear := func(name string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-4 {
			t.Fatalf("%s: got %.5f, want %.5f", name, got, want)
		}
	}

	// Entry 0: Intro (Part 1) on page 0, MediaBox 800, top=720 -> (800-720)/800 = 0.10
	if anchors.Outlines[0].Title != "Intro (Part 1)" || anchors.Outlines[0].Depth != 1 || anchors.Outlines[0].PageIdx != 0 {
		t.Fatalf("unexpected outline[0]: %+v", anchors.Outlines[0])
	}
	assertNear("outline[0].YFrac", anchors.Outlines[0].YFrac, 0.10)

	// Entry 1: UTF-16BE "日本語 α" on page 1, MediaBox 1000, top=800 -> (1000-800)/1000 = 0.20
	if anchors.Outlines[1].Title != "日本語 α" || anchors.Outlines[1].Depth != 1 || anchors.Outlines[1].PageIdx != 1 {
		t.Fatalf("unexpected outline[1]: %+v", anchors.Outlines[1])
	}
	assertNear("outline[1].YFrac", anchors.Outlines[1].YFrac, 0.20)

	// Entry 2: nested child "Sub Detail" on page 1, MediaBox 1000, FitH 250 -> 0.75
	if anchors.Outlines[2].Title != "Sub Detail" || anchors.Outlines[2].Depth != 2 || anchors.Outlines[2].PageIdx != 1 {
		t.Fatalf("unexpected outline[2]: %+v", anchors.Outlines[2])
	}
	assertNear("outline[2].YFrac", anchors.Outlines[2].YFrac, 0.75)

	// Entry 3: "Conclusion" via /A /GoTo on page 2, inherited MediaBox 800, top=600 -> 0.25
	if anchors.Outlines[3].Title != "Conclusion" || anchors.Outlines[3].Depth != 1 || anchors.Outlines[3].PageIdx != 2 {
		t.Fatalf("unexpected outline[3]: %+v", anchors.Outlines[3])
	}
	assertNear("outline[3].YFrac", anchors.Outlines[3].YFrac, 0.25)

	// Named destinations
	if a, ok := anchors.NamedDests["sec:intro"]; !ok || a.PageIdx != 0 {
		t.Fatalf("expected named dest sec:intro on page 0, got %+v (ok=%v)", a, ok)
	}
	if a, ok := anchors.NamedDests["fig:custom"]; !ok || a.PageIdx != 2 {
		t.Fatalf("expected named dest fig:custom on page 2, got %+v (ok=%v)", a, ok)
	} else {
		assertNear("fig:custom.YFrac", a.YFrac, 0.75)
	}
}

func TestResolveSectionScrollYAllTiersAndTrimMargins(t *testing.T) {
	// 3 pages of 200x400 each at dstW=200 (pageTops = 0, 414, 828; totalHeight = 1228).
	imgs := []*image.RGBA{
		makeTestPage(200, 400, color.RGBA{255, 255, 255, 255}, color.RGBA{0, 0, 0, 255}),
		makeTestPage(200, 400, color.RGBA{255, 255, 255, 255}, color.RGBA{0, 0, 0, 255}),
		makeTestPage(200, 400, color.RGBA{255, 255, 255, 255}, color.RGBA{0, 0, 0, 255}),
	}
	deck := NewDocumentDeckFromImages(imgs, time.Millisecond)
	defer deck.Close()

	deck.SetAnchors(&DocumentAnchors{
		Outlines: []OutlineAnchor{
			{Title: "Introduction", Depth: 1, PageIdx: 0, YFrac: 0.10},
			{Title: "Examples", Depth: 1, PageIdx: 0, YFrac: 0.60},
			{Title: "Included External Chapter", Depth: 1, PageIdx: 1, YFrac: 0.05}, // extra PDF heading from #include
			{Title: "Proof of α + β", Depth: 1, PageIdx: 1, YFrac: 0.50},
			{Title: "Examples", Depth: 1, PageIdx: 2, YFrac: 0.40}, // duplicate title on page 3!
		},
		NamedDests: map[string]PageAnchor{
			"sec:intro": {PageIdx: 0, YFrac: 0.10},
			"fig:named": {PageIdx: 2, YFrac: 0.25},
		},
	})

	all := []SectionEntry{
		{Kind: SectionHeading, Level: 1, Title: "Introduction", Label: "sec:intro", Line: 0},
		{Kind: SectionHeading, Level: 1, Title: "Examples", Line: 10},
		{Kind: SectionHeading, Level: 1, Title: "Proof of *$alpha + beta$*", Line: 30},
		{Kind: SectionFigure, Level: 0, Title: "Pipeline", Line: 40}, // unlabeled figure between line 30 and line 50
		{Kind: SectionFigure, Level: 0, Title: "Named Fig", Label: "fig:named", Line: 45},
		{Kind: SectionHeading, Level: 1, Title: "Examples", Line: 50},
	}

	viewH := 200
	dstW := 200

	// 1. Exact label destination on page 2 (0-based): pageTop=828, y=828+0.25*400=928 -> 928-24 = 904
	yNamed := deck.ResolveSectionScrollY(SectionTarget{
		Kind: SectionFigure, Title: "Named Fig", Label: "fig:named", Line: 45, TotalLines: 60, AllSections: all,
	}, dstW, viewH, false, false)
	if yNamed != 904 {
		t.Fatalf("Tier 1 named dest: got scrollY=%d, want 904", yNamed)
	}

	// 2. Duplicate heading title "Examples": 1st instance -> page 0 (y=240-24=216), 2nd instance -> page 2 (y=828+160-24=964)
	yEx1 := deck.ResolveSectionScrollY(SectionTarget{
		Kind: SectionHeading, Level: 1, Title: "Examples", Line: 10, TotalLines: 60, AllSections: all,
	}, dstW, viewH, false, false)
	if yEx1 != 216 {
		t.Fatalf("Tier 2 first duplicate heading: got scrollY=%d, want 216", yEx1)
	}
	yEx2 := deck.ResolveSectionScrollY(SectionTarget{
		Kind: SectionHeading, Level: 1, Title: "Examples", Line: 50, TotalLines: 60, AllSections: all,
	}, dstW, viewH, false, false)
	if yEx2 != 964 {
		t.Fatalf("Tier 2 second duplicate heading: got scrollY=%d, want 964", yEx2)
	}

	// 3. Formatted/math heading "Proof of *$alpha + beta$*" aligns to "Proof of α + β" on page 1 (414+200-24=590)
	yMath := deck.ResolveSectionScrollY(SectionTarget{
		Kind: SectionHeading, Level: 1, Title: "Proof of *$alpha + beta$*", Line: 30, TotalLines: 60, AllSections: all,
	}, dstW, viewH, false, false)
	if yMath != 590 {
		t.Fatalf("Tier 2 formatted/math heading: got scrollY=%d, want 590", yMath)
	}

	// 4. Unlabeled figure on line 40 sits halfway between "Proof" (line 30, y=614) and "Named Fig" (line 45, y=928)
	// t = (40-30)/(45-30) = 2/3 -> interpY = 614 + int((2/3)*314) = 614 + 209 = 823 -> minus 24 = 799
	yFig := deck.ResolveSectionScrollY(SectionTarget{
		Kind: SectionFigure, Title: "Pipeline", Line: 40, TotalLines: 60, AllSections: all,
	}, dstW, viewH, false, false)
	if yFig < 780 || yFig > 820 {
		t.Fatalf("Tier 3 bracketed figure interpolation: got scrollY=%d, want ~799", yFig)
	}

	// 5. Top-of-page heading clamps to pageTop (never peeks at previous page gap)
	deck.anchors.Outlines[2] = OutlineAnchor{Title: "Top Of Page 2", Depth: 1, PageIdx: 1, YFrac: 0.02}
	yTopP2 := deck.ResolveSectionScrollY(SectionTarget{
		Kind: SectionHeading, Level: 1, Title: "Top Of Page 2", Line: 20, TotalLines: 60,
	}, dstW, viewH, false, false)
	if yTopP2 != deck.PageTop(1) {
		t.Fatalf("top-of-page heading should clamp to pageTop=%d, got %d", deck.PageTop(1), yTopP2)
	}

	// 6. With trimMargins=true, yFrac maps cleanly into trimmed page coordinates.
	yTrim := deck.ResolveSectionScrollY(SectionTarget{
		Kind: SectionHeading, Level: 1, Title: "Examples", Line: 50, TotalLines: 60, AllSections: all,
	}, dstW, viewH, false, true)
	if yTrim <= 0 {
		t.Fatalf("expected positive trimmed scrollY for page 3 heading, got %d", yTrim)
	}

	// 7. Fallback when no PDF anchors exist -> proportional line ratio across totalHeight.
	deckNoAnchors := NewDocumentDeckFromImages(imgs, time.Millisecond)
	defer deckNoAnchors.Close()
	yFallback := deckNoAnchors.ResolveSectionScrollY(SectionTarget{
		Kind: SectionHeading, Level: 1, Title: "Mid", Line: 50, TotalLines: 101,
	}, dstW, viewH, false, false)
	// 50% of 1228 = 614, minus 24 = 590
	if yFallback != 590 {
		t.Fatalf("proportional fallback: got scrollY=%d, want 590", yFallback)
	}
}

type blockingAnchorCompiler struct {
	mu      sync.Mutex
	release chan struct{}
	pages   int
	anchors *DocumentAnchors
}

func (c *blockingAnchorCompiler) CompilePages(ctx context.Context, req CompileRequest) (*DocumentDeck, error) {
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	imgs := make([]*image.RGBA, c.pages)
	for i := 0; i < c.pages; i++ {
		imgs[i] = makeTestPage(400, 600, color.RGBA{255, 255, 255, 255}, color.RGBA{10, 10, 10, 255})
	}
	d := NewDocumentDeckFromImages(imgs, time.Millisecond)
	d.SetAnchors(c.anchors)
	return d, nil
}

func TestPreviewPaneScrollToSectionImmediateAndPending(t *testing.T) {
	sim, bp := initPreviewTestEnv(t)
	SetCapsForTest(TerminalCaps{Sixel: true, CellW: 10, CellH: 20, Source: "test", Probed: true})

	release := make(chan struct{})
	anchors := &DocumentAnchors{
		Outlines: []OutlineAnchor{
			{Title: "Intro", Depth: 1, PageIdx: 0, YFrac: 0.08},
			{Title: "Middle", Depth: 1, PageIdx: 1, YFrac: 0.45},
			{Title: "Conclusion", Depth: 1, PageIdx: 2, YFrac: 0.12},
		},
	}
	fc := &blockingAnchorCompiler{release: release, pages: 3, anchors: anchors}
	SetCompiler(fc)
	var outBuf bytes.Buffer
	SetOutput(&outBuf)
	Init()

	pp, ok := Open(bp, false)
	if !ok || pp == nil {
		t.Fatal("preview.Open failed")
	}

	// While initial compile is still blocked, jump to Conclusion (page 3).
	// Should queue pendingSection and apply once compile finishes.
	movedNow := pp.ScrollToSection(SectionTarget{
		Kind:       SectionHeading,
		Level:      1,
		Title:      "Conclusion",
		Line:       40,
		TotalLines: 50,
	})
	if movedNow {
		t.Fatal("expected ScrollToSection to defer while deck is nil")
	}
	if pp.pendingSection == nil {
		t.Fatal("expected pendingSection to be queued while compiling")
	}

	close(release)
	waitForCondition(t, 2*time.Second, func() bool {
		return !pp.IsCompiling() && pp.viewport != nil
	})

	fp, lp, total := pp.PageRange()
	if total != 3 || fp != 3 || lp != 3 {
		t.Fatalf("expected pending section jump to land on page 3/3, got p.%d-%d/%d (scrollY=%d)", fp, lp, total, pp.ScrollY())
	}
	if pp.pendingSection != nil {
		t.Fatal("expected pendingSection cleared after compile landed")
	}

	// Emit frame, then jump immediately to Middle (page 2) -> should move immediately and mark Sixel dirty.
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	outBuf.Reset()

	if !pp.ScrollToSection(SectionTarget{
		Kind:       SectionHeading,
		Level:      1,
		Title:      "Middle",
		Line:       20,
		TotalLines: 50,
	}) {
		t.Fatal("expected immediate ScrollToSection to move viewport to Middle")
	}
	fp, _, _ = pp.PageRange()
	if fp != 2 {
		t.Fatalf("expected Middle jump to land on page 2, got p.%d (scrollY=%d)", fp, pp.ScrollY())
	}
	pp.Display()
	sim.Show()
	screen.RunPostShowHooks()
	if outBuf.Len() == 0 {
		t.Fatal("expected ScrollToSection to emit updated Sixel frame")
	}

	// Jump back to Intro (page 1) -> scrollY near top of page 1.
	if !pp.ScrollToSection(SectionTarget{
		Kind:       SectionHeading,
		Level:      1,
		Title:      "Intro",
		Line:       0,
		TotalLines: 50,
	}) {
		t.Fatal("expected ScrollToSection to return to Intro")
	}
	fp, _, _ = pp.PageRange()
	if fp != 1 {
		t.Fatalf("expected Intro jump to land on page 1, got p.%d", fp)
	}
}

func TestTinymistCompilerExtractsRealPDFAnchors(t *testing.T) {
	bin, err := exec.LookPath("tinymist")
	if err != nil {
		// Also check VS Code extension location on developer machines if present.
		if home, herr := os.UserHomeDir(); herr == nil {
			exeName := "tinymist"
			if runtime.GOOS == "windows" {
				exeName = "tinymist.exe"
			}
			matches, _ := filepath.Glob(filepath.Join(home, ".vscode", "extensions", "myriad-dreamin.tinymist-*", "out", exeName))
			if len(matches) > 0 {
				bin = matches[0]
			}
		}
	}
	if bin == "" {
		t.Skip("tinymist binary not available in test environment")
	}

	tmpDir := t.TempDir()
	docPath := filepath.Join(tmpDir, "multi.typ")
	doc := `= First Section <sec:first>
Intro text.

#pagebreak()

= Second Section <sec:second>
#v(260pt)
== Deep Subsection

#pagebreak()

= Third Section
End text.
`
	if err := os.WriteFile(docPath, []byte(doc), 0644); err != nil {
		t.Fatal(err)
	}

	comp := &TinymistCompiler{
		DiscoverFn: func(string) (string, error) { return bin, nil },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	deck, err := comp.CompilePages(ctx, CompileRequest{
		FilePath: docPath,
		RootDir:  tmpDir,
		PPI:      96,
	})
	if err != nil {
		t.Fatalf("CompilePages failed: %v", err)
	}
	defer deck.Close()

	if deck.PageCount() != 3 {
		t.Fatalf("expected 3 compiled PNG pages, got %d", deck.PageCount())
	}
	anchors := deck.Anchors()
	if anchors == nil {
		t.Fatal("expected non-nil PDF anchors from real tinymist compilation")
	}
	if len(anchors.Outlines) != 4 {
		t.Fatalf("expected 4 PDF outline anchors, got %d: %#v", len(anchors.Outlines), anchors.Outlines)
	}
	if anchors.Outlines[0].PageIdx != 0 || anchors.Outlines[1].PageIdx != 1 || anchors.Outlines[2].PageIdx != 1 || anchors.Outlines[3].PageIdx != 2 {
		t.Fatalf("unexpected page indices in PDF anchors: %#v", anchors.Outlines)
	}
	if anchors.Outlines[2].YFrac <= anchors.Outlines[1].YFrac+0.2 {
		t.Fatalf("expected Deep Subsection to be well below Second Section on page 2: %+v vs %+v", anchors.Outlines[2], anchors.Outlines[1])
	}
	if _, ok := anchors.NamedDests["sec:second"]; !ok {
		t.Fatalf("expected sec:second in PDF named destinations, got %#v", anchors.NamedDests)
	}
}
