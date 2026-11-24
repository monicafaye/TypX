package preview

import (
	"bytes"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
)

// PageAnchor locates a position on a rendered page using 0-based page index
// and normalized vertical offset YFrac in [0.0, 1.0] from top to bottom.
type PageAnchor struct {
	PageIdx int
	YFrac   float64
}

// OutlineAnchor represents a single heading bookmark extracted from a compiled PDF.
type OutlineAnchor struct {
	Title   string
	Depth   int // 1-based outline nesting depth
	PageIdx int
	YFrac   float64
}

// DocumentAnchors holds exact rendered positions extracted from a compiled PDF
// (heading bookmarks in document DFS order and named label destinations).
type DocumentAnchors struct {
	Outlines   []OutlineAnchor
	NamedDests map[string]PageAnchor
}

type pdfPageInfo struct {
	index int
	minY  float64
	maxY  float64
}

type pdfParser struct {
	objs       map[int][]byte
	pages      map[int]pdfPageInfo
	namedDests map[string]PageAnchor
}

// ParsePDFAnchors parses indirect objects, page geometry, /Outlines, and
// /Names /Dests from a Typst-compiled PDF without external dependencies.
// Returns nil if no usable outline or destination anchors are found.
func ParsePDFAnchors(pdf []byte) *DocumentAnchors {
	if len(pdf) == 0 {
		return nil
	}
	p := &pdfParser{
		objs:       extractPDFObjects(pdf),
		pages:      make(map[int]pdfPageInfo),
		namedDests: make(map[string]PageAnchor),
	}
	if len(p.objs) == 0 {
		return nil
	}

	catalogNum := p.findCatalog()
	if catalogNum <= 0 {
		return nil
	}
	catalog := p.objs[catalogNum]

	// 1. Walk /Pages tree to establish 0-based page indices and MediaBox heights.
	if pagesRef, ok := findDictRef(catalog, "/Pages"); ok {
		var inheritedBox [4]float64
		hasBox := false
		pageIdx := 0
		visited := make(map[int]bool)
		p.walkPages(pagesRef, inheritedBox, hasBox, &pageIdx, visited)
	}
	if len(p.pages) == 0 {
		return nil
	}

	// 2. Parse /Names -> /Dests name tree (so /Outlines string destinations and
	//    explicit <label> anchors resolve accurately).
	if namesVal, ok := findDictValue(catalog, "/Names"); ok {
		namesDict := p.resolveDictBytes(namesVal)
		if destsVal, ok := findDictValue(namesDict, "/Dests"); ok {
			visited := make(map[int]bool)
			p.walkNameTree(destsVal, visited)
		}
	}
	// Also support legacy /Dests dictionary directly on /Catalog.
	if destsVal, ok := findDictValue(catalog, "/Dests"); ok {
		destsDict := p.resolveDictBytes(destsVal)
		p.parseLegacyDestsDict(destsDict)
	}

	// 3. Walk /Outlines linked tree in document DFS order.
	var outlines []OutlineAnchor
	if outlinesRef, ok := findDictRef(catalog, "/Outlines"); ok {
		outlinesDict := p.objs[outlinesRef]
		if firstRef, ok := findDictRef(outlinesDict, "/First"); ok {
			visited := make(map[int]bool)
			p.walkOutlines(firstRef, 1, visited, &outlines)
		}
	}

	if len(outlines) == 0 && len(p.namedDests) == 0 {
		return nil
	}
	return &DocumentAnchors{
		Outlines:   outlines,
		NamedDests: p.namedDests,
	}
}

// extractPDFObjects scans top-level "<num> <gen> obj ... endobj" records while
// skipping binary stream payloads so compressed content streams never confuse parsing.
func extractPDFObjects(pdf []byte) map[int][]byte {
	objs := make(map[int][]byte)
	n := len(pdf)
	i := 0
	for i < n {
		// Advance to start of a candidate object header: integer followed by whitespace,
		// generation integer, whitespace, and "obj".
		if pdf[i] < '0' || pdf[i] > '9' {
			i++
			continue
		}
		// Must be at start of file or preceded by a delimiter/whitespace.
		if i > 0 && !isPDFWhitespace(pdf[i-1]) && !isPDFDelimiter(pdf[i-1]) {
			i++
			continue
		}
		numStart := i
		for i < n && pdf[i] >= '0' && pdf[i] <= '9' {
			i++
		}
		objNum, err := strconv.Atoi(string(pdf[numStart:i]))
		if err != nil || objNum <= 0 {
			continue
		}
		if i >= n || !isPDFWhitespace(pdf[i]) {
			continue
		}
		for i < n && isPDFWhitespace(pdf[i]) {
			i++
		}
		genStart := i
		for i < n && pdf[i] >= '0' && pdf[i] <= '9' {
			i++
		}
		if genStart == i || i >= n || !isPDFWhitespace(pdf[i]) {
			continue
		}
		for i < n && isPDFWhitespace(pdf[i]) {
			i++
		}
		if i+3 > n || string(pdf[i:i+3]) != "obj" {
			continue
		}
		if i+3 < n && !isPDFWhitespace(pdf[i+3]) && !isPDFDelimiter(pdf[i+3]) {
			continue
		}
		bodyStart := i + 3
		i = bodyStart

		// Scan until "endobj", skipping any "stream ... endstream" payload.
		bodyEnd := -1
		for i < n {
			if pdf[i] == '(' {
				i = skipPDFLiteralString(pdf, i)
				continue
			}
			if pdf[i] == 's' && i+6 <= n && string(pdf[i:i+6]) == "stream" {
				// Confirm preceded by whitespace or '>' (dict end) and followed by EOL.
				prevOK := i == bodyStart || isPDFWhitespace(pdf[i-1]) || pdf[i-1] == '>'
				after := i + 6
				eolOK := (after < n && pdf[after] == '\n') || (after+1 < n && pdf[after] == '\r' && pdf[after+1] == '\n')
				if prevOK && eolOK {
					// Keep dictionary portion before stream; skip binary stream bytes.
					streamStart := i
					endStream := bytes.Index(pdf[after:], []byte("endstream"))
					if endStream < 0 {
						bodyEnd = streamStart
						i = n
						break
					}
					endObj := bytes.Index(pdf[after+endStream+len("endstream"):], []byte("endobj"))
					bodyEnd = streamStart
					if endObj >= 0 {
						i = after + endStream + len("endstream") + endObj + len("endobj")
					} else {
						i = after + endStream + len("endstream")
					}
					break
				}
			}
			if pdf[i] == 'e' && i+6 <= n && string(pdf[i:i+6]) == "endobj" {
				prevOK := i == bodyStart || isPDFWhitespace(pdf[i-1]) || isPDFDelimiter(pdf[i-1])
				nextOK := i+6 == n || isPDFWhitespace(pdf[i+6]) || isPDFDelimiter(pdf[i+6])
				if prevOK && nextOK {
					bodyEnd = i
					i += 6
					break
				}
			}
			i++
		}
		if bodyEnd >= bodyStart {
			objs[objNum] = bytes.TrimSpace(pdf[bodyStart:bodyEnd])
		}
	}
	return objs
}

func (p *pdfParser) findCatalog() int {
	for num, body := range p.objs {
		if hasDictNameValue(body, "/Type", "/Catalog") {
			return num
		}
	}
	return 0
}

func (p *pdfParser) walkPages(objNum int, inheritedBox [4]float64, hasBox bool, pageIdx *int, visited map[int]bool) {
	if objNum <= 0 || visited[objNum] {
		return
	}
	visited[objNum] = true
	body, ok := p.objs[objNum]
	if !ok {
		return
	}

	box := inheritedBox
	boxPresent := hasBox
	if mbVal, ok := findDictValue(body, "/MediaBox"); ok {
		if parsed, ok := p.parseRect(mbVal); ok {
			box = parsed
			boxPresent = true
		}
	}

	if hasDictNameValue(body, "/Type", "/Page") {
		minY, maxY := 0.0, 841.8898 // default A4 height fallback if MediaBox absent
		if boxPresent && box[3] > box[1] {
			minY, maxY = box[1], box[3]
		} else if boxPresent && box[1] > box[3] {
			minY, maxY = box[3], box[1]
		}
		p.pages[objNum] = pdfPageInfo{
			index: *pageIdx,
			minY:  minY,
			maxY:  maxY,
		}
		*pageIdx++
		return
	}

	if kidsVal, ok := findDictValue(body, "/Kids"); ok {
		arr := p.resolveArrayBytes(kidsVal)
		for _, kidRef := range parseRefList(arr) {
			p.walkPages(kidRef, box, boxPresent, pageIdx, visited)
		}
	}
}

func (p *pdfParser) walkNameTree(val []byte, visited map[int]bool) {
	val = bytes.TrimSpace(val)
	if ref, ok := parseSingleRef(val); ok {
		if visited[ref] {
			return
		}
		visited[ref] = true
		val = p.objs[ref]
	}
	if len(extractDictBody(val)) == 0 {
		return
	}
	if kidsVal, ok := findDictValue(val, "/Kids"); ok {
		arr := p.resolveArrayBytes(kidsVal)
		for _, kidRef := range parseRefList(arr) {
			if !visited[kidRef] {
				visited[kidRef] = true
				p.walkNameTree(p.objs[kidRef], visited)
			}
		}
	}
	if namesVal, ok := findDictValue(val, "/Names"); ok {
		arr := p.resolveArrayBytes(namesVal)
		p.parseNamesArray(arr)
	}
}

func (p *pdfParser) parseNamesArray(arr []byte) {
	inner := stripArrayBrackets(arr)
	pos := 0
	for pos < len(inner) {
		pos = skipPDFWS(inner, pos)
		if pos >= len(inner) {
			break
		}
		name, nextPos, ok := readPDFString(inner, pos)
		if !ok {
			break
		}
		pos = skipPDFWS(inner, nextPos)
		if pos >= len(inner) {
			break
		}
		valTok, endPos := readPDFValue(inner, pos)
		if endPos <= pos {
			break
		}
		pos = endPos
		if anchor, ok := p.resolveDest(valTok, 0); ok && name != "" {
			p.namedDests[name] = anchor
		}
	}
}

func (p *pdfParser) parseLegacyDestsDict(dict []byte) {
	inner := extractDictBody(dict)
	pos := 0
	for pos < len(inner) {
		pos = skipPDFWS(inner, pos)
		if pos >= len(inner) || inner[pos] != '/' {
			break
		}
		keyEnd := readPDFNameEnd(inner, pos)
		key := decodePDFName(string(inner[pos+1 : keyEnd]))
		pos = skipPDFWS(inner, keyEnd)
		valTok, endPos := readPDFValue(inner, pos)
		if endPos <= pos {
			break
		}
		pos = endPos
		if anchor, ok := p.resolveDest(valTok, 0); ok && key != "" {
			p.namedDests[key] = anchor
		}
	}
}

func (p *pdfParser) walkOutlines(objNum, depth int, visited map[int]bool, out *[]OutlineAnchor) {
	for objNum > 0 && !visited[objNum] {
		visited[objNum] = true
		body, ok := p.objs[objNum]
		if !ok {
			return
		}

		title := ""
		if titleVal, ok := findDictValue(body, "/Title"); ok {
			titleVal = p.resolveBytes(titleVal)
			if s, _, ok := readPDFString(titleVal, 0); ok {
				title = strings.TrimSpace(s)
			}
		}

		var anchor PageAnchor
		hasAnchor := false
		if destVal, ok := findDictValue(body, "/Dest"); ok {
			anchor, hasAnchor = p.resolveDest(destVal, 0)
		} else if actVal, ok := findDictValue(body, "/A"); ok {
			actDict := p.resolveDictBytes(actVal)
			if dVal, ok := findDictValue(actDict, "/D"); ok {
				anchor, hasAnchor = p.resolveDest(dVal, 0)
			}
		}

		if hasAnchor {
			*out = append(*out, OutlineAnchor{
				Title:   title,
				Depth:   depth,
				PageIdx: anchor.PageIdx,
				YFrac:   anchor.YFrac,
			})
		}

		if firstChild, ok := findDictRef(body, "/First"); ok {
			p.walkOutlines(firstChild, depth+1, visited, out)
		}

		nextRef, ok := findDictRef(body, "/Next")
		if !ok {
			break
		}
		objNum = nextRef
	}
}

func (p *pdfParser) resolveDest(val []byte, depth int) (PageAnchor, bool) {
	if depth > 6 {
		return PageAnchor{}, false
	}
	val = bytes.TrimSpace(val)
	if len(val) == 0 {
		return PageAnchor{}, false
	}
	if ref, ok := parseSingleRef(val); ok {
		return p.resolveDest(p.objs[ref], depth+1)
	}
	// Dictionary destination: << /D [...] >>
	if bytes.HasPrefix(val, []byte("<<")) {
		if dVal, ok := findDictValue(val, "/D"); ok {
			return p.resolveDest(dVal, depth+1)
		}
		return PageAnchor{}, false
	}
	// Named destination string or name
	if val[0] == '(' || val[0] == '<' {
		if name, _, ok := readPDFString(val, 0); ok {
			a, found := p.namedDests[name]
			return a, found
		}
		return PageAnchor{}, false
	}
	if val[0] == '/' {
		end := readPDFNameEnd(val, 0)
		name := decodePDFName(string(val[1:end]))
		a, found := p.namedDests[name]
		return a, found
	}
	if val[0] != '[' {
		return PageAnchor{}, false
	}

	// Destination array: [ pageRef /XYZ left top zoom ] or /FitH top, /FitBH top, /FitR l b r t, /Fit
	inner := stripArrayBrackets(val)
	pos := skipPDFWS(inner, 0)
	pageTok, nextPos := readPDFValue(inner, pos)
	if nextPos <= pos {
		return PageAnchor{}, false
	}
	pageRef, ok := parseSingleRef(pageTok)
	if !ok {
		return PageAnchor{}, false
	}
	pinfo, ok := p.pages[pageRef]
	if !ok {
		return PageAnchor{}, false
	}

	pos = skipPDFWS(inner, nextPos)
	modeTok, nextPos := readPDFValue(inner, pos)
	mode := string(bytes.TrimSpace(modeTok))

	// Read remaining numeric/null tokens.
	var args []string
	pos = nextPos
	for pos < len(inner) {
		pos = skipPDFWS(inner, pos)
		if pos >= len(inner) {
			break
		}
		tok, end := readPDFValue(inner, pos)
		if end <= pos {
			break
		}
		args = append(args, string(bytes.TrimSpace(tok)))
		pos = end
	}

	var topY float64
	hasTop := false
	switch mode {
	case "/XYZ":
		// args: [left, top, zoom]
		if len(args) >= 2 && args[1] != "null" {
			if v, err := strconv.ParseFloat(args[1], 64); err == nil {
				topY = v
				hasTop = true
			}
		}
	case "/FitH", "/FitBH":
		// args: [top]
		if len(args) >= 1 && args[0] != "null" {
			if v, err := strconv.ParseFloat(args[0], 64); err == nil {
				topY = v
				hasTop = true
			}
		}
	case "/FitR":
		// args: [left, bottom, right, top]
		if len(args) >= 4 && args[3] != "null" {
			if v, err := strconv.ParseFloat(args[3], 64); err == nil {
				topY = v
				hasTop = true
			}
		}
	}

	yFrac := 0.0
	if hasTop && pinfo.maxY > pinfo.minY {
		yFrac = (pinfo.maxY - topY) / (pinfo.maxY - pinfo.minY)
		if math.IsNaN(yFrac) || math.IsInf(yFrac, 0) {
			yFrac = 0
		} else if yFrac < 0 {
			yFrac = 0
		} else if yFrac > 1 {
			yFrac = 1
		}
	}
	return PageAnchor{PageIdx: pinfo.index, YFrac: yFrac}, true
}

func (p *pdfParser) resolveBytes(val []byte) []byte {
	val = bytes.TrimSpace(val)
	if ref, ok := parseSingleRef(val); ok {
		return bytes.TrimSpace(p.objs[ref])
	}
	return val
}

func (p *pdfParser) resolveDictBytes(val []byte) []byte {
	return p.resolveBytes(val)
}

func (p *pdfParser) resolveArrayBytes(val []byte) []byte {
	return p.resolveBytes(val)
}

func (p *pdfParser) parseRect(val []byte) ([4]float64, bool) {
	var rect [4]float64
	arr := stripArrayBrackets(p.resolveArrayBytes(val))
	fields := strings.Fields(string(arr))
	if len(fields) < 4 {
		return rect, false
	}
	for i := 0; i < 4; i++ {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return rect, false
		}
		rect[i] = v
	}
	return rect, true
}

// --- Low-level PDF lexical helpers ---

func isPDFWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\f' || b == 0
}

func isPDFDelimiter(b byte) bool {
	switch b {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func skipPDFWS(b []byte, pos int) int {
	for pos < len(b) {
		if isPDFWhitespace(b[pos]) {
			pos++
			continue
		}
		if b[pos] == '%' {
			for pos < len(b) && b[pos] != '\n' && b[pos] != '\r' {
				pos++
			}
			continue
		}
		break
	}
	return pos
}

func skipPDFLiteralString(b []byte, pos int) int {
	if pos >= len(b) || b[pos] != '(' {
		return pos
	}
	depth := 1
	pos++
	for pos < len(b) && depth > 0 {
		switch b[pos] {
		case '\\':
			pos += 2
			continue
		case '(':
			depth++
		case ')':
			depth--
		}
		pos++
	}
	return pos
}

func readPDFNameEnd(b []byte, pos int) int {
	if pos < len(b) && b[pos] == '/' {
		pos++
	}
	for pos < len(b) && !isPDFWhitespace(b[pos]) && !isPDFDelimiter(b[pos]) {
		pos++
	}
	return pos
}

func decodePDFName(s string) string {
	if !strings.ContainsRune(s, '#') {
		return s
	}
	var out bytes.Buffer
	out.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				out.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

func extractDictBody(raw []byte) []byte {
	raw = bytes.TrimSpace(raw)
	start := bytes.Index(raw, []byte("<<"))
	if start < 0 {
		return nil
	}
	depth := 0
	i := start
	for i < len(raw) {
		if raw[i] == '(' {
			i = skipPDFLiteralString(raw, i)
			continue
		}
		if raw[i] == '<' && i+1 < len(raw) && raw[i+1] == '<' {
			depth++
			i += 2
			continue
		}
		if raw[i] == '>' && i+1 < len(raw) && raw[i+1] == '>' {
			depth--
			if depth == 0 {
				return raw[start+2 : i]
			}
			i += 2
			continue
		}
		i++
	}
	return nil
}

func stripArrayBrackets(raw []byte) []byte {
	raw = bytes.TrimSpace(raw)
	if len(raw) >= 2 && raw[0] == '[' && raw[len(raw)-1] == ']' {
		return raw[1 : len(raw)-1]
	}
	return raw
}

// readPDFValue reads one complete PDF value starting at pos (including indirect
// reference triples "N G R", dicts "<<...>>", arrays "[...]", strings, names, numbers).
func readPDFValue(b []byte, pos int) ([]byte, int) {
	pos = skipPDFWS(b, pos)
	if pos >= len(b) {
		return nil, pos
	}
	start := pos
	switch b[pos] {
	case '(':
		end := skipPDFLiteralString(b, pos)
		return b[start:end], end
	case '<':
		if pos+1 < len(b) && b[pos+1] == '<' {
			depth := 0
			i := pos
			for i < len(b) {
				if b[i] == '(' {
					i = skipPDFLiteralString(b, i)
					continue
				}
				if b[i] == '<' && i+1 < len(b) && b[i+1] == '<' {
					depth++
					i += 2
					continue
				}
				if b[i] == '>' && i+1 < len(b) && b[i+1] == '>' {
					depth--
					i += 2
					if depth == 0 {
						return b[start:i], i
					}
					continue
				}
				i++
			}
			return b[start:], len(b)
		}
		// Hex string <...>
		end := bytes.IndexByte(b[pos+1:], '>')
		if end < 0 {
			return b[start:], len(b)
		}
		return b[start : pos+1+end+1], pos + 1 + end + 1
	case '[':
		depth := 0
		i := pos
		for i < len(b) {
			if b[i] == '(' {
				i = skipPDFLiteralString(b, i)
				continue
			}
			if b[i] == '[' {
				depth++
			} else if b[i] == ']' {
				depth--
				if depth == 0 {
					return b[start : i+1], i + 1
				}
			}
			i++
		}
		return b[start:], len(b)
	case '/':
		end := readPDFNameEnd(b, pos)
		return b[start:end], end
	default:
		end := pos
		for end < len(b) && !isPDFWhitespace(b[end]) && !isPDFDelimiter(b[end]) {
			end++
		}
		// Check if this is an indirect reference "N G R".
		if isAllDigits(b[start:end]) {
			p1 := skipPDFWS(b, end)
			e1 := p1
			for e1 < len(b) && b[e1] >= '0' && b[e1] <= '9' {
				e1++
			}
			if e1 > p1 && (e1 == len(b) || isPDFWhitespace(b[e1])) {
				p2 := skipPDFWS(b, e1)
				if p2 < len(b) && b[p2] == 'R' && (p2+1 == len(b) || isPDFWhitespace(b[p2+1]) || isPDFDelimiter(b[p2+1])) {
					return b[start : p2+1], p2 + 1
				}
			}
		}
		return b[start:end], end
	}
}

func isAllDigits(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func findDictValue(raw []byte, targetKey string) ([]byte, bool) {
	body := extractDictBody(raw)
	if len(body) == 0 {
		return nil, false
	}
	pos := 0
	for pos < len(body) {
		pos = skipPDFWS(body, pos)
		if pos >= len(body) {
			break
		}
		if body[pos] != '/' {
			// Skip stray token safely
			_, end := readPDFValue(body, pos)
			if end <= pos {
				break
			}
			pos = end
			continue
		}
		keyEnd := readPDFNameEnd(body, pos)
		key := string(body[pos:keyEnd])
		val, valEnd := readPDFValue(body, keyEnd)
		if valEnd <= keyEnd {
			break
		}
		if key == targetKey {
			return val, true
		}
		pos = valEnd
	}
	return nil, false
}

func hasDictNameValue(raw []byte, key, want string) bool {
	v, ok := findDictValue(raw, key)
	return ok && string(bytes.TrimSpace(v)) == want
}

func findDictRef(raw []byte, key string) (int, bool) {
	v, ok := findDictValue(raw, key)
	if !ok {
		return 0, false
	}
	return parseSingleRef(v)
}

func parseSingleRef(tok []byte) (int, bool) {
	fields := strings.Fields(string(bytes.TrimSpace(tok)))
	if len(fields) != 3 || fields[2] != "R" {
		return 0, false
	}
	num, err1 := strconv.Atoi(fields[0])
	_, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil || num <= 0 {
		return 0, false
	}
	return num, true
}

func parseRefList(arr []byte) []int {
	inner := stripArrayBrackets(arr)
	var refs []int
	pos := 0
	for pos < len(inner) {
		pos = skipPDFWS(inner, pos)
		if pos >= len(inner) {
			break
		}
		tok, end := readPDFValue(inner, pos)
		if end <= pos {
			break
		}
		if num, ok := parseSingleRef(tok); ok {
			refs = append(refs, num)
		}
		pos = end
	}
	return refs
}

// readPDFString decodes either a literal PDF string `(...)` or a hex string `<...>`,
// supporting UTF-16BE BOM (`\xFE\xFF`) and standard PDF escapes.
func readPDFString(b []byte, pos int) (string, int, bool) {
	pos = skipPDFWS(b, pos)
	if pos >= len(b) {
		return "", pos, false
	}
	if b[pos] == '(' {
		end := skipPDFLiteralString(b, pos)
		if end <= pos+1 || b[end-1] != ')' {
			return "", end, false
		}
		raw := unescapePDFLiteral(b[pos+1 : end-1])
		return decodePDFTextBytes(raw), end, true
	}
	if b[pos] == '<' && (pos+1 >= len(b) || b[pos+1] != '<') {
		closeRel := bytes.IndexByte(b[pos+1:], '>')
		if closeRel < 0 {
			return "", len(b), false
		}
		end := pos + 1 + closeRel + 1
		var hexBuf bytes.Buffer
		for _, c := range b[pos+1 : pos+1+closeRel] {
			if !isPDFWhitespace(c) {
				hexBuf.WriteByte(c)
			}
		}
		if hexBuf.Len()%2 == 1 {
			hexBuf.WriteByte('0')
		}
		decoded := make([]byte, hex.DecodedLen(hexBuf.Len()))
		n, err := hex.Decode(decoded, hexBuf.Bytes())
		if err != nil {
			return "", end, false
		}
		return decodePDFTextBytes(decoded[:n]), end, true
	}
	return "", pos, false
}

func unescapePDFLiteral(in []byte) []byte {
	out := make([]byte, 0, len(in))
	for i := 0; i < len(in); i++ {
		if in[i] != '\\' {
			out = append(out, in[i])
			continue
		}
		if i+1 >= len(in) {
			break
		}
		i++
		switch in[i] {
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case '(', ')', '\\':
			out = append(out, in[i])
		case '\n':
			// line continuation
		case '\r':
			if i+1 < len(in) && in[i+1] == '\n' {
				i++
			}
		default:
			if in[i] >= '0' && in[i] <= '7' {
				val := int(in[i] - '0')
				for k := 0; k < 2 && i+1 < len(in) && in[i+1] >= '0' && in[i+1] <= '7'; k++ {
					i++
					val = val*8 + int(in[i]-'0')
				}
				out = append(out, byte(val&0xFF))
			} else {
				out = append(out, in[i])
			}
		}
	}
	return out
}

func decodePDFTextBytes(raw []byte) string {
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		body := raw[2:]
		u16 := make([]uint16, 0, len(body)/2)
		for i := 0; i+1 < len(body); i += 2 {
			u16 = append(u16, uint16(body[i])<<8|uint16(body[i+1]))
		}
		return string(utf16.Decode(u16))
	}
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		return string(raw[3:])
	}
	// PDFDocEncoding / Latin-1 fallback -> UTF-8
	var sb strings.Builder
	sb.Grow(len(raw))
	for _, b := range raw {
		sb.WriteRune(rune(b))
	}
	return sb.String()
}
