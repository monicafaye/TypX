package preview

import (
	"math"
	"strings"
	"unicode"
)

const sectionTopPaddingPx = 24

// SectionKind identifies the kind of outline entry targeted for preview sync.
type SectionKind int

const (
	SectionHeading SectionKind = iota
	SectionFigure
	SectionLabel
)

// SectionEntry describes a single document outline entry in document order,
// used to align source outline items with rendered PDF anchors.
type SectionEntry struct {
	Kind     SectionKind
	Level    int
	Title    string
	Label    string
	FilePath string
	Line     int
}

// SectionTarget describes a user-selected outline entry to reposition the preview to.
type SectionTarget struct {
	Kind        SectionKind
	Level       int
	Title       string
	Label       string
	FilePath    string
	Line        int
	TotalLines  int
	AllSections []SectionEntry
}

// ResolveSectionScrollY computes the continuous vertical scrollY (in pixels) that
// brings target cleanly into view near the top of the preview viewport.
//
// Resolution uses three tiers:
//  1. Exact named label destination from PDF /Names /Dests.
//  2. Exact heading bookmark from PDF /Outlines using monotonic alignment.
//  3. Bracketed interpolation between the nearest surrounding PDF anchors
//     (or proportional buffer-line interpolation when no PDF anchors exist).
func (d *DocumentDeck) ResolveSectionScrollY(target SectionTarget, dstW, viewH int, invert, trim bool) int {
	if d == nil || len(d.pages) == 0 {
		return 0
	}
	if dstW < 16 {
		dstW = 16
	}
	if viewH < 16 {
		viewH = 16
	}
	d.ComputeLayout(dstW, trim)

	maxScrollY := d.totalHeight - viewH
	if maxScrollY < 0 {
		maxScrollY = 0
	}

	// Tier 1 & Tier 2: exact anchor for the target itself.
	if anchor, ok := d.resolveExactAnchor(target); ok {
		if y, pageTop, ok := d.anchorToVirtualY(anchor, dstW, invert, trim); ok {
			// Refresh maxScrollY in case ensureScaled changed a trimmed page height.
			maxScrollY = d.totalHeight - viewH
			if maxScrollY < 0 {
				maxScrollY = 0
			}
			scrollY := y - sectionTopPaddingPx
			if scrollY < pageTop {
				scrollY = pageTop
			}
			return clampScrollY(scrollY, maxScrollY)
		}
	}

	// Tier 3: bracketed interpolation between surrounding PDF anchors (e.g., for
	// unlabeled figures, standalone labels, or bookmarked:false headings).
	if y, ok := d.resolveInterpolatedY(target, dstW, invert, trim); ok {
		maxScrollY = d.totalHeight - viewH
		if maxScrollY < 0 {
			maxScrollY = 0
		}
		if target.Line <= 0 {
			return 0
		}
		scrollY := y - sectionTopPaddingPx
		return clampScrollY(scrollY, maxScrollY)
	}

	// Ultimate fallback: proportional buffer line ratio across total document height.
	if target.Line <= 0 || target.TotalLines <= 1 {
		return 0
	}
	ratio := float64(target.Line) / float64(target.TotalLines-1)
	if ratio < 0 {
		ratio = 0
	} else if ratio > 1 {
		ratio = 1
	}
	scrollY := int(ratio*float64(d.totalHeight)) - sectionTopPaddingPx
	return clampScrollY(scrollY, maxScrollY)
}

// ResolveCursorVirtualY computes the rendered virtual Y pixel position for
// target (plus the deck's maxScrollY) using the same three-tier PDF anchor
// resolution as ResolveSectionScrollY. Callers can compare virtualY against
// the current viewport window before scrolling to avoid redundant redraws.
func (d *DocumentDeck) ResolveCursorVirtualY(target SectionTarget, dstW, viewH int, invert, trim bool) (virtualY, maxScrollY int, ok bool) {
	if d == nil || len(d.pages) == 0 {
		return 0, 0, false
	}
	if dstW < 16 {
		dstW = 16
	}
	if viewH < 16 {
		viewH = 16
	}
	d.ComputeLayout(dstW, trim)

	computeMax := func() int {
		m := d.totalHeight - viewH
		if m < 0 {
			return 0
		}
		return m
	}

	if anchor, found := d.resolveExactAnchor(target); found {
		if y, _, okY := d.anchorToVirtualY(anchor, dstW, invert, trim); okY {
			return y, computeMax(), true
		}
	}
	if y, found := d.resolveInterpolatedY(target, dstW, invert, trim); found {
		if target.Line <= 0 {
			return 0, computeMax(), true
		}
		return y, computeMax(), true
	}
	if target.Line <= 0 || target.TotalLines <= 1 {
		return 0, computeMax(), true
	}
	ratio := float64(target.Line) / float64(target.TotalLines-1)
	if ratio < 0 {
		ratio = 0
	} else if ratio > 1 {
		ratio = 1
	}
	return int(ratio * float64(d.totalHeight)), computeMax(), true
}

type lineYPoint struct {
	line int
	y    int
}

// ResolveClickToSourceLine maps a virtual Y pixel coordinate (scrollY + localClickYpx)
// back to a 0-based source buffer line in [0, totalLines-1] using monotonic
// interpolation between aligned PDF outline/label anchors (falling back to
// proportional document position when no anchors match).
func (d *DocumentDeck) ResolveClickToSourceLine(clickYpx int, sections []SectionEntry, totalLines int) int {
	if totalLines <= 1 || d == nil || len(d.pages) == 0 {
		return 0
	}

	dstW := d.layoutW
	if dstW <= 0 && d.pages[0] != nil && d.pages[0].srcW > 0 {
		dstW = d.pages[0].srcW
	}
	if dstW < 16 {
		dstW = 16
	}
	trim := d.layoutTrim
	invert := d.layoutInvert
	preTops, preHeights, preTotalH := snapshotDeckLayout(d, dstW, trim)

	var curve []lineYPoint
	if d.anchors != nil && len(sections) > 0 {
		aligned := alignSectionsToAnchors(sections, d.anchors)
		if len(aligned) > 0 {
			for i, s := range sections {
				a, ok := aligned[i]
				if !ok {
					continue
				}
				y, _, okY := d.anchorToVirtualY(a, dstW, invert, trim)
				if !okY {
					continue
				}
				line := s.Line
				if line < 0 {
					line = 0
				} else if line >= totalLines {
					line = totalLines - 1
				}
				if len(curve) > 0 {
					prev := curve[len(curve)-1]
					if line < prev.line || y < prev.y {
						continue
					}
					if line == prev.line && y == prev.y {
						continue
					}
				}
				curve = append(curve, lineYPoint{line: line, y: y})
			}
		}
	}

	totalH := d.totalHeight
	if totalH <= 0 {
		return 0
	}

	effectiveClickYpx := remapVirtualYAcrossLayout(clickYpx, preTops, preHeights, preTotalH, d.pageTops, d.pageHeights, totalH)

	if len(curve) == 0 || (curve[0].line > 0 && curve[0].y > 0) {
		curve = append([]lineYPoint{{line: 0, y: 0}}, curve...)
	}
	last := curve[len(curve)-1]
	if last.line < totalLines-1 && last.y < totalH {
		curve = append(curve, lineYPoint{line: totalLines - 1, y: totalH})
	}

	if len(curve) == 1 {
		return curve[0].line
	}
	if effectiveClickYpx <= curve[0].y {
		return curve[0].line
	}
	if effectiveClickYpx >= curve[len(curve)-1].y {
		return curve[len(curve)-1].line
	}

	for k := 0; k < len(curve)-1; k++ {
		p0, p1 := curve[k], curve[k+1]
		if effectiveClickYpx > p1.y {
			continue
		}
		if p1.y <= p0.y {
			return p0.line
		}
		t := float64(effectiveClickYpx-p0.y) / float64(p1.y-p0.y)
		line := p0.line + int(math.Round(t*float64(p1.line-p0.line)))
		if line < 0 {
			return 0
		}
		if line >= totalLines {
			return totalLines - 1
		}
		return line
	}
	return totalLines - 1
}

func snapshotDeckLayout(d *DocumentDeck, dstW int, trim bool) (tops, heights []int, totalH int) {
	if d == nil || len(d.pages) == 0 {
		return nil, nil, 0
	}
	if len(d.pageTops) != len(d.pages) || d.layoutW != dstW || d.layoutTrim != trim {
		d.ComputeLayout(dstW, trim)
	}
	tops = append([]int(nil), d.pageTops...)
	heights = append([]int(nil), d.pageHeights...)
	totalH = d.totalHeight
	return tops, heights, totalH
}

func layoutShifted(preTops, preHeights []int, preTotalH int, postTops, postHeights []int, postTotalH int) bool {
	if preTotalH != postTotalH || len(preTops) != len(postTops) || len(preHeights) != len(postHeights) {
		return true
	}
	for i := range preTops {
		if preTops[i] != postTops[i] || preHeights[i] != postHeights[i] {
			return true
		}
	}
	return false
}

func remapVirtualYAcrossLayout(y int, preTops, preHeights []int, preTotalH int, postTops, postHeights []int, postTotalH int) int {
	if len(preTops) == 0 || len(preTops) != len(postTops) || len(preHeights) != len(postHeights) || preTotalH <= 0 || postTotalH <= 0 {
		return y
	}
	if !layoutShifted(preTops, preHeights, preTotalH, postTops, postHeights, postTotalH) {
		return y
	}
	if y <= 0 {
		return y
	}
	if y >= preTotalH {
		return postTotalH + (y - preTotalH)
	}
	for k := 0; k < len(preTops); k++ {
		pTop := preTops[k]
		pH := preHeights[k]
		pBot := pTop + pH
		if y >= pTop && y <= pBot {
			if postHeights[k] == pH {
				return postTops[k] + (y - pTop)
			}
			if pH <= 0 {
				return postTops[k]
			}
			rel := float64(y-pTop) / float64(pH)
			return postTops[k] + int(math.Round(rel*float64(postHeights[k])))
		}
		if k+1 < len(preTops) && y > pBot && y < preTops[k+1] {
			preGap := preTops[k+1] - pBot
			postGap := postTops[k+1] - (postTops[k] + postHeights[k])
			if preGap == postGap {
				return postTops[k] + postHeights[k] + (y - pBot)
			}
			if preGap <= 0 {
				return postTops[k] + postHeights[k]
			}
			rel := float64(y-pBot) / float64(preGap)
			return postTops[k] + postHeights[k] + int(math.Round(rel*float64(postGap)))
		}
	}
	return y
}

func clampScrollY(y, maxY int) int {
	if y < 0 {
		return 0
	}
	if y > maxY {
		return maxY
	}
	return y
}

func (d *DocumentDeck) anchorToVirtualY(anchor PageAnchor, dstW int, invert, trim bool) (y int, pageTop int, ok bool) {
	if d == nil || anchor.PageIdx < 0 || anchor.PageIdx >= len(d.pages) {
		return 0, 0, false
	}
	if d.layoutW != dstW || d.layoutTrim != trim || len(d.pageTops) != len(d.pages) {
		d.ComputeLayout(dstW, trim)
	}
	p := d.pages[anchor.PageIdx]
	if trim {
		if sc, err := p.ensureScaled(dstW, invert, trim); err == nil && sc != nil {
			if sc.Bounds().Dy() != d.pageHeights[anchor.PageIdx] {
				d.ComputeLayout(dstW, trim)
			}
		}
	}
	pageTop = d.pageTops[anchor.PageIdx]
	pageH := d.pageHeights[anchor.PageIdx]

	yFrac := anchor.YFrac
	if yFrac < 0 {
		yFrac = 0
	} else if yFrac > 1 {
		yFrac = 1
	}

	if trim && p.cropH > 0 && p.srcH > 0 {
		rawY := yFrac * float64(p.srcH)
		relCrop := (rawY - float64(p.cropTop)) / float64(p.cropH)
		if relCrop < 0 {
			relCrop = 0
		} else if relCrop > 1 {
			relCrop = 1
		}
		return pageTop + int(relCrop*float64(pageH)), pageTop, true
	}
	return pageTop + int(yFrac*float64(pageH)), pageTop, true
}

func (d *DocumentDeck) resolveExactAnchor(target SectionTarget) (PageAnchor, bool) {
	if d == nil || d.anchors == nil {
		return PageAnchor{}, false
	}
	// Tier 1: exact PDF named destination by <label>.
	if target.Label != "" && len(d.anchors.NamedDests) > 0 {
		if a, ok := d.anchors.NamedDests[target.Label]; ok {
			return a, true
		}
	}
	if target.Kind != SectionHeading || len(d.anchors.Outlines) == 0 {
		return PageAnchor{}, false
	}

	// Tier 2: align all document headings against PDF /Outlines in order.
	if len(target.AllSections) > 0 {
		aligned := alignSectionsToAnchors(target.AllSections, d.anchors)
		// Find the matching entry in AllSections (preferring exact file + line + kind + title).
		bestIdx := -1
		for i, s := range target.AllSections {
			if s.Kind != target.Kind {
				continue
			}
			if target.FilePath != "" && s.FilePath != "" && s.FilePath != target.FilePath {
				continue
			}
			if s.Line == target.Line && s.Title == target.Title {
				bestIdx = i
				break
			}
			if s.Line == target.Line && bestIdx < 0 {
				bestIdx = i
			}
		}
		if bestIdx >= 0 {
			if a, ok := aligned[bestIdx]; ok {
				return a, true
			}
		}
	}

	// Direct title fallback when AllSections wasn't supplied.
	wantNorm := normalizeHeadingTitle(target.Title)
	bestScore := 0
	var best PageAnchor
	for _, oa := range d.anchors.Outlines {
		sc := headingMatchScore(wantNorm, target.Level, normalizeHeadingTitle(oa.Title), oa.Depth)
		if sc > bestScore {
			bestScore = sc
			best = PageAnchor{PageIdx: oa.PageIdx, YFrac: oa.YFrac}
		}
	}
	if bestScore >= 40 {
		return best, true
	}
	return PageAnchor{}, false
}

func (d *DocumentDeck) resolveInterpolatedY(target SectionTarget, dstW int, invert, trim bool) (int, bool) {
	if d == nil || d.anchors == nil || len(target.AllSections) == 0 {
		return 0, false
	}
	aligned := alignSectionsToAnchors(target.AllSections, d.anchors)
	if len(aligned) == 0 {
		return 0, false
	}

	if target.FilePath != "" {
		if y, ok := d.resolveMultiFileInterpolatedY(target, aligned, dstW, invert, trim); ok {
			return y, true
		}
	}

	// Find closest anchored section at or before target.Line, and closest after target.Line.
	hasPrev := false
	prevLine := 0
	var prevAnchor PageAnchor

	hasNext := false
	nextLine := 0
	var nextAnchor PageAnchor

	for i, s := range target.AllSections {
		a, ok := aligned[i]
		if !ok {
			continue
		}
		if s.Line <= target.Line {
			if !hasPrev || s.Line >= prevLine {
				hasPrev = true
				prevLine = s.Line
				prevAnchor = a
			}
		} else {
			if !hasNext || s.Line < nextLine {
				hasNext = true
				nextLine = s.Line
				nextAnchor = a
			}
		}
	}

	if !hasPrev && !hasNext {
		return 0, false
	}

	prevY := 0
	if hasPrev {
		if y, _, ok := d.anchorToVirtualY(prevAnchor, dstW, invert, trim); ok {
			prevY = y
		} else {
			hasPrev = false
		}
	}

	nextY := d.totalHeight
	if hasNext {
		if y, _, ok := d.anchorToVirtualY(nextAnchor, dstW, invert, trim); ok {
			nextY = y
		} else {
			hasNext = false
		}
	}

	if !hasPrev && !hasNext {
		return 0, false
	}
	if !hasPrev {
		prevLine = 0
		prevY = 0
	}
	if !hasNext {
		nextLine = target.TotalLines - 1
		if nextLine <= target.Line {
			nextLine = target.Line + 1
		}
		nextY = d.totalHeight
	}
	if nextY < prevY {
		nextY = prevY
	}
	span := nextLine - prevLine
	if span <= 0 {
		return prevY, true
	}
	t := float64(target.Line-prevLine) / float64(span)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return prevY + int(t*float64(nextY-prevY)), true
}

func (d *DocumentDeck) resolveMultiFileInterpolatedY(target SectionTarget, aligned map[int]PageAnchor, dstW int, invert, trim bool) (int, bool) {
	// Locate target entry in document order if present in AllSections.
	targetIdx := -1
	for i, s := range target.AllSections {
		if s.FilePath != "" && s.FilePath != target.FilePath {
			continue
		}
		if s.Kind == target.Kind && s.Line == target.Line && s.Title == target.Title {
			targetIdx = i
			break
		}
		if s.Line == target.Line && targetIdx < 0 {
			targetIdx = i
		}
	}

	prevIdx, nextIdx := -1, -1
	if targetIdx >= 0 {
		for i := targetIdx; i >= 0; i-- {
			if _, ok := aligned[i]; ok {
				prevIdx = i
				break
			}
		}
		for i := targetIdx + 1; i < len(target.AllSections); i++ {
			if _, ok := aligned[i]; ok {
				nextIdx = i
				break
			}
		}
	} else {
		// Target line not in AllSections: bracket within target.FilePath first.
		firstFileIdx, lastFileIdx := -1, -1
		for i, s := range target.AllSections {
			if s.FilePath != target.FilePath {
				continue
			}
			if firstFileIdx < 0 {
				firstFileIdx = i
			}
			lastFileIdx = i
			if _, ok := aligned[i]; !ok {
				continue
			}
			if s.Line <= target.Line {
				if prevIdx < 0 || s.Line >= target.AllSections[prevIdx].Line {
					prevIdx = i
				}
			} else {
				if nextIdx < 0 || s.Line < target.AllSections[nextIdx].Line {
					nextIdx = i
				}
			}
		}
		if prevIdx < 0 && firstFileIdx > 0 {
			for i := firstFileIdx - 1; i >= 0; i-- {
				if _, ok := aligned[i]; ok {
					prevIdx = i
					break
				}
			}
		}
		if nextIdx < 0 && lastFileIdx >= 0 {
			for i := lastFileIdx + 1; i < len(target.AllSections); i++ {
				if _, ok := aligned[i]; ok {
					nextIdx = i
					break
				}
			}
		}
	}

	if prevIdx < 0 && nextIdx < 0 {
		return 0, false
	}

	prevY := 0
	hasPrev := false
	if prevIdx >= 0 {
		if y, _, ok := d.anchorToVirtualY(aligned[prevIdx], dstW, invert, trim); ok {
			prevY = y
			hasPrev = true
		}
	}
	nextY := d.totalHeight
	hasNext := false
	if nextIdx >= 0 {
		if y, _, ok := d.anchorToVirtualY(aligned[nextIdx], dstW, invert, trim); ok {
			nextY = y
			hasNext = true
		}
	}
	if !hasPrev && !hasNext {
		return 0, false
	}
	if nextY < prevY {
		nextY = prevY
	}

	// If both brackets are in target.FilePath, interpolate by line numbers.
	prevSameFile := hasPrev && target.AllSections[prevIdx].FilePath == target.FilePath
	nextSameFile := hasNext && target.AllSections[nextIdx].FilePath == target.FilePath
	if prevSameFile && nextSameFile {
		prevLine := target.AllSections[prevIdx].Line
		nextLine := target.AllSections[nextIdx].Line
		span := nextLine - prevLine
		if span <= 0 {
			return prevY, true
		}
		t := float64(target.Line-prevLine) / float64(span)
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
		return prevY + int(t*float64(nextY-prevY)), true
	}

	// Otherwise interpolate by document-order index across files.
	if targetIdx >= 0 {
		lo := prevIdx
		if !hasPrev {
			lo = -1
		}
		hi := nextIdx
		if !hasNext {
			hi = len(target.AllSections)
		}
		span := hi - lo
		if span <= 0 {
			return prevY, true
		}
		t := float64(targetIdx-lo) / float64(span)
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
		return prevY + int(t*float64(nextY-prevY)), true
	}
	if hasPrev {
		return prevY, true
	}
	return nextY, true
}

// alignSectionsToAnchors maps indices in sections to their exact PDF PageAnchors.
func alignSectionsToAnchors(sections []SectionEntry, anchors *DocumentAnchors) map[int]PageAnchor {
	res := make(map[int]PageAnchor)
	if anchors == nil {
		return res
	}

	// 1. Named destinations resolve any labeled section directly.
	if len(anchors.NamedDests) > 0 {
		for i, s := range sections {
			if s.Label != "" {
				if a, ok := anchors.NamedDests[s.Label]; ok {
					res[i] = a
				}
			}
		}
	}

	if len(anchors.Outlines) == 0 {
		return res
	}

	// 2. Collect heading entries in document order.
	var srcIdx []int
	var srcNorm []string
	for i, s := range sections {
		if s.Kind == SectionHeading {
			srcIdx = append(srcIdx, i)
			srcNorm = append(srcNorm, normalizeHeadingTitle(s.Title))
		}
	}
	m := len(srcIdx)
	n := len(anchors.Outlines)
	if m == 0 {
		return res
	}

	pdfNorm := make([]string, n)
	for j, oa := range anchors.Outlines {
		pdfNorm[j] = normalizeHeadingTitle(oa.Title)
	}

	// Monotonic sequence alignment (DP) so duplicate heading titles and extra
	// included/unbookmarked headings align in exact document order.
	const maxDPCells = 250_000
	if m*n <= maxDPCells {
		// dp[i][j] = best alignment score for src[i:] and pdf[j:]
		cols := n + 1
		dp := make([]int, (m+1)*cols)
		choice := make([]uint8, m*n) // 1=match, 2=skip src, 3=skip pdf

		for i := m - 1; i >= 0; i-- {
			si := srcIdx[i]
			for j := n - 1; j >= 0; j-- {
				best := dp[(i+1)*cols+j]
				act := uint8(2)

				if skipPDF := dp[i*cols+(j+1)]; skipPDF > best {
					best = skipPDF
					act = 3
				}

				sc := headingMatchScore(srcNorm[i], sections[si].Level, pdfNorm[j], anchors.Outlines[j].Depth)
				// Also give a strong bonus if both share the exact same named destination position.
				if a, ok := res[si]; ok && a.PageIdx == anchors.Outlines[j].PageIdx {
					sc += 120
				}
				if sc >= 35 {
					if take := sc + dp[(i+1)*cols+(j+1)]; take > best {
						best = take
						act = 1
					}
				}
				dp[i*cols+j] = best
				choice[i*n+j] = act
			}
		}

		// Reconstruct matched pairs in order.
		matchedSrc := make([]int, m)
		for i := range matchedSrc {
			matchedSrc[i] = -1
		}
		for i, j := 0, 0; i < m && j < n; {
			switch choice[i*n+j] {
			case 1:
				matchedSrc[i] = j
				i++
				j++
			case 2:
				i++
			default:
				j++
			}
		}

		// Fill any 1-to-1 sandwiched gaps (e.g., heading containing pure math/macros
		// whose rendered PDF text differs completely from raw source syntax, while
		// sitting between identical-count matched neighbors).
		fillSandwichedGaps(matchedSrc, n, sections, srcIdx, anchors.Outlines)

		for i, j := range matchedSrc {
			if j >= 0 {
				si := srcIdx[i]
				if _, already := res[si]; !already {
					oa := anchors.Outlines[j]
					res[si] = PageAnchor{PageIdx: oa.PageIdx, YFrac: oa.YFrac}
				}
			}
		}
		return res
	}

	// Linear greedy fallback for extraordinarily large outlines (>500 headings).
	j := 0
	for i := 0; i < m && j < n; i++ {
		si := srcIdx[i]
		for k := j; k < n && k < j+8; k++ {
			if headingMatchScore(srcNorm[i], sections[si].Level, pdfNorm[k], anchors.Outlines[k].Depth) >= 40 {
				if _, already := res[si]; !already {
					oa := anchors.Outlines[k]
					res[si] = PageAnchor{PageIdx: oa.PageIdx, YFrac: oa.YFrac}
				}
				j = k + 1
				break
			}
		}
	}
	return res
}

// fillSandwichedGaps maps unmatched headings when an unmatched run of length k
// in src aligns 1:1 with an unmatched run of the exact same length k in pdf
// (with matching heading depth), handling headings composed solely of Typst math
// or dynamic expressions.
func fillSandwichedGaps(matchedSrc []int, pdfLen int, sections []SectionEntry, srcIdx []int, pdfOutlines []OutlineAnchor) {
	m := len(matchedSrc)
	// Fast path: if counts are equal and nothing matched by text, check if every
	// heading depth aligns 1:1 (e.g. purely mathematical or macro headings).
	i := 0
	prevPDF := -1
	for i < m {
		if matchedSrc[i] >= 0 {
			prevPDF = matchedSrc[i]
			i++
			continue
		}
		runStart := i
		for i < m && matchedSrc[i] < 0 {
			i++
		}
		runEnd := i // exclusive
		nextPDF := pdfLen
		if runEnd < m {
			nextPDF = matchedSrc[runEnd]
		}
		srcRunLen := runEnd - runStart
		pdfRunLen := nextPDF - (prevPDF + 1)
		if srcRunLen > 0 && srcRunLen == pdfRunLen {
			depthsMatch := true
			for k := 0; k < srcRunLen; k++ {
				srcLvl := sections[srcIdx[runStart+k]].Level
				pdfDepth := pdfOutlines[prevPDF+1+k].Depth
				if srcLvl > 0 && pdfDepth > 0 && srcLvl != pdfDepth {
					depthsMatch = false
					break
				}
			}
			if depthsMatch {
				for k := 0; k < srcRunLen; k++ {
					matchedSrc[runStart+k] = prevPDF + 1 + k
				}
			}
		}
		if runEnd < m {
			prevPDF = matchedSrc[runEnd]
		}
	}
}

func headingMatchScore(srcNorm string, srcLevel int, pdfNorm string, pdfDepth int) int {
	if srcNorm == "" || pdfNorm == "" {
		return 0
	}
	depthBonus := 0
	if srcLevel > 0 && pdfDepth > 0 && srcLevel == pdfDepth {
		depthBonus = 10
	}
	if srcNorm == pdfNorm {
		return 100 + depthBonus
	}
	if strings.Contains(pdfNorm, srcNorm) || strings.Contains(srcNorm, pdfNorm) {
		shortLen := len(srcNorm)
		longLen := len(pdfNorm)
		if longLen < shortLen {
			shortLen, longLen = longLen, shortLen
		}
		if shortLen >= 3 && shortLen*2 >= longLen {
			return 70 + depthBonus
		}
	}
	// Word-token overlap (handles e.g. "Proof of $alpha$" vs "Proof of α").
	srcWords := strings.Fields(srcNorm)
	pdfWords := strings.Fields(pdfNorm)
	if len(srcWords) == 0 || len(pdfWords) == 0 {
		return 0
	}
	pdfSet := make(map[string]bool, len(pdfWords))
	for _, w := range pdfWords {
		pdfSet[w] = true
	}
	shared := 0
	for _, w := range srcWords {
		if pdfSet[w] {
			shared++
		}
	}
	if shared == 0 {
		return 0
	}
	denom := len(srcWords)
	if len(pdfWords) > denom {
		denom = len(pdfWords)
	}
	score := (shared * 65) / denom
	if score >= 30 {
		return score + depthBonus
	}
	return 0
}

func normalizeHeadingTitle(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSpace := true
	for _, r := range s {
		switch r {
		case '*', '_', '`', '$', '\\', '[', ']':
			// Strip common Typst inline markup delimiters so raw source headings
			// match rendered PDF bookmark titles.
			continue
		}
		if unicode.IsSpace(r) {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
			continue
		}
		b.WriteRune(unicode.ToLower(r))
		inSpace = false
	}
	return strings.TrimSpace(b.String())
}
