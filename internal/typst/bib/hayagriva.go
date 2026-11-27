package bib

import (
	"bytes"
	"strings"
)

// ParseHayagriva parses Hayagriva YAML (.yml / .yaml) bibliography entries in
// pure Go without external YAML dependencies. It tracks entry indentation so
// nested maps (such as `parent:`) never clobber entry-level `title` or `date`.
func ParseHayagriva(data []byte) []Entry {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if len(data) == 0 {
		return nil
	}
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	var entries []Entry
	type rawEntry struct {
		key      string
		typ      string
		title    string
		authors  []string
		editors  []string
		year     string
		date     string
		hasEntry bool
	}

	var cur rawEntry
	entryIndent := -1

	flush := func() {
		if !cur.hasEntry || cur.key == "" {
			cur = rawEntry{}
			entryIndent = -1
			return
		}
		authors := cur.authors
		if len(authors) == 0 {
			authors = cur.editors
		}
		year := extractYear(cur.year)
		if year == "" {
			year = extractYear(cur.date)
		}
		entries = append(entries, Entry{
			Key:     cur.key,
			Type:    cur.typ,
			Title:   collapseWhitespace(cur.title),
			Authors: strings.Join(authors, "; "),
			Year:    year,
		})
		cur = rawEntry{}
		entryIndent = -1
	}

	i := 0
	for i < len(lines) {
		stripped := stripYAMLComment(lines[i])
		trimmed := strings.TrimSpace(stripped)
		if trimmed == "" || trimmed == "---" || trimmed == "..." {
			i++
			continue
		}

		indent := countYAMLIndent(stripped)
		if indent == 0 {
			key, val, ok := splitYAMLMapping(trimmed)
			if !ok {
				i++
				continue
			}
			flush()
			cur = rawEntry{
				key:      unquoteYAMLScalar(key),
				hasEntry: true,
			}
			// Support rare single-line flow-map entry: `key: { type: article, ... }`.
			if strings.HasPrefix(val, "{") {
				m := parseYAMLFlowMap(val)
				if v := m["type"]; v != "" {
					cur.typ = v
				}
				if v := m["title"]; v != "" {
					cur.title = v
				}
				if v := m["year"]; v != "" {
					cur.year = v
				}
				if v := m["date"]; v != "" {
					cur.date = v
				}
				if v := m["author"]; v != "" {
					cur.authors = append(cur.authors, v)
				}
				if v := m["editor"]; v != "" {
					cur.editors = append(cur.editors, v)
				}
			}
			i++
			continue
		}

		if !cur.hasEntry {
			i++
			continue
		}

		if entryIndent < 0 {
			entryIndent = indent
		}

		if indent != entryIndent {
			i++
			continue
		}

		field, valPart, ok := splitYAMLMapping(trimmed)
		if !ok {
			i++
			continue
		}
		field = strings.ToLower(unquoteYAMLScalar(field))

		switch field {
		case "type":
			val, next := parseYAMLScalarValue(lines, i, entryIndent, valPart)
			cur.typ = val
			i = next
		case "title":
			if valPart == "" {
				// Support nested `title:\n  value: ...` if used.
				nestedTitle, next := parseNestedValueField(lines, i+1, entryIndent)
				if nestedTitle != "" {
					cur.title = nestedTitle
				}
				i = next
			} else {
				val, next := parseYAMLScalarValue(lines, i, entryIndent, valPart)
				cur.title = val
				i = next
			}
		case "year":
			val, next := parseYAMLScalarValue(lines, i, entryIndent, valPart)
			cur.year = val
			i = next
		case "date":
			val, next := parseYAMLScalarValue(lines, i, entryIndent, valPart)
			cur.date = val
			i = next
		case "author", "authors":
			items, next := parseYAMLAuthorList(lines, i, entryIndent, valPart)
			cur.authors = append(cur.authors, items...)
			i = next
		case "editor":
			items, next := parseYAMLAuthorList(lines, i, entryIndent, valPart)
			cur.editors = append(cur.editors, items...)
			i = next
		default:
			i++
		}
	}

	flush()
	return entries
}

func stripYAMLComment(line string) string {
	inSingle := false
	inDouble := false
	for i := 0; i < len(line); i++ {
		b := line[i]
		if inSingle {
			if b == '\'' {
				if i+1 < len(line) && line[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if b == '\\' && i+1 < len(line) {
				i++
				continue
			}
			if b == '"' {
				inDouble = false
			}
			continue
		}
		if b == '\'' {
			inSingle = true
			continue
		}
		if b == '"' {
			inDouble = true
			continue
		}
		if b == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return line[:i]
		}
	}
	return line
}

func countYAMLIndent(line string) int {
	n := 0
	for i := 0; i < len(line); i++ {
		if line[i] == ' ' {
			n++
		} else if line[i] == '\t' {
			n += 2
		} else {
			break
		}
	}
	return n
}

func splitYAMLMapping(trimmed string) (string, string, bool) {
	inSingle := false
	inDouble := false
	for i := 0; i < len(trimmed); i++ {
		b := trimmed[i]
		if inSingle {
			if b == '\'' {
				if i+1 < len(trimmed) && trimmed[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if b == '\\' && i+1 < len(trimmed) {
				i++
				continue
			}
			if b == '"' {
				inDouble = false
			}
			continue
		}
		if b == '\'' {
			inSingle = true
			continue
		}
		if b == '"' {
			inDouble = true
			continue
		}
		if b == ':' && (i+1 == len(trimmed) || trimmed[i+1] == ' ' || trimmed[i+1] == '\t') {
			key := strings.TrimSpace(trimmed[:i])
			val := strings.TrimSpace(trimmed[i+1:])
			return key, val, key != ""
		}
	}
	return "", "", false
}

func parseYAMLScalarValue(lines []string, idx, entryIndent int, valPart string) (string, int) {
	if valPart == "" {
		return "", idx + 1
	}
	if valPart[0] == '|' || valPart[0] == '>' {
		fold := valPart[0] == '>'
		return parseYAMLBlockScalar(lines, idx+1, entryIndent, fold)
	}
	if valPart[0] == '"' || valPart[0] == '\'' {
		quote := valPart[0]
		if hasClosingYAMLQuote(valPart, quote) {
			return unquoteYAMLScalar(valPart), idx + 1
		}
		// Multi-line quoted scalar: scan until matching closing quote, ignoring trailing comments after closing quote.
		var sb strings.Builder
		sb.WriteString(valPart)
		j := idx + 1
		for j < len(lines) {
			line := lines[j]
			if countYAMLIndent(line) <= entryIndent && strings.TrimSpace(line) != "" {
				break
			}
			sb.WriteByte(' ')
			seg := strings.TrimSpace(line)
			if endPos, closed := findClosingYAMLQuoteOffset(seg, quote); closed {
				sb.WriteString(seg[:endPos+1])
				j++
				break
			}
			sb.WriteString(seg)
			j++
		}
		return unquoteYAMLScalar(sb.String()), j
	}

	// Plain scalar (with optional indented plain continuation lines).
	var parts []string
	parts = append(parts, valPart)
	j := idx + 1
	for j < len(lines) {
		st := stripYAMLComment(lines[j])
		tr := strings.TrimSpace(st)
		if tr == "" {
			break
		}
		if countYAMLIndent(st) <= entryIndent {
			break
		}
		if strings.HasPrefix(tr, "- ") || tr == "-" {
			break
		}
		if _, _, isMap := splitYAMLMapping(tr); isMap {
			break
		}
		parts = append(parts, tr)
		j++
	}
	return strings.Join(parts, " "), j
}

func parseYAMLBlockScalar(lines []string, start, entryIndent int, fold bool) (string, int) {
	type blkLine struct {
		indent int
		raw    string
		blank  bool
	}
	var collected []blkLine
	minIndent := -1
	j := start
	for j < len(lines) {
		raw := lines[j]
		tr := strings.TrimSpace(raw)
		if tr == "" {
			collected = append(collected, blkLine{blank: true})
			j++
			continue
		}
		ind := countYAMLIndent(raw)
		if ind <= entryIndent {
			break
		}
		if minIndent < 0 || ind < minIndent {
			minIndent = ind
		}
		collected = append(collected, blkLine{indent: ind, raw: raw})
		j++
	}
	// Trim trailing blank lines.
	for len(collected) > 0 && collected[len(collected)-1].blank {
		collected = collected[:len(collected)-1]
	}
	if len(collected) == 0 {
		return "", j
	}
	var out []string
	for _, cl := range collected {
		if cl.blank {
			out = append(out, "")
			continue
		}
		s := cl.raw
		if minIndent > 0 && len(s) >= minIndent {
			s = s[minIndent:]
		}
		out = append(out, strings.TrimRight(s, " \t"))
	}
	if fold {
		return collapseWhitespace(strings.Join(out, " ")), j
	}
	return strings.Join(out, "\n"), j
}

func parseNestedValueField(lines []string, start, entryIndent int) (string, int) {
	j := start
	val := ""
	for j < len(lines) {
		st := stripYAMLComment(lines[j])
		tr := strings.TrimSpace(st)
		if tr == "" {
			j++
			continue
		}
		ind := countYAMLIndent(st)
		if ind <= entryIndent {
			break
		}
		if k, v, ok := splitYAMLMapping(tr); ok && strings.EqualFold(unquoteYAMLScalar(k), "value") {
			parsed, next := parseYAMLScalarValue(lines, j, ind, v)
			val = parsed
			j = next
			continue
		}
		j++
	}
	return val, j
}

func parseYAMLAuthorList(lines []string, idx, entryIndent int, valPart string) ([]string, int) {
	if valPart != "" {
		if strings.HasPrefix(valPart, "[") {
			// Inline flow list (possibly spanning multiple lines until closing ']').
			var sb strings.Builder
			sb.WriteString(valPart)
			j := idx + 1
			for !hasBalancedFlowList(sb.String()) && j < len(lines) {
				st := stripYAMLComment(lines[j])
				if strings.TrimSpace(st) != "" && countYAMLIndent(st) <= entryIndent {
					break
				}
				sb.WriteByte(' ')
				sb.WriteString(strings.TrimSpace(st))
				j++
			}
			return parseYAMLFlowAuthorList(sb.String()), j
		}
		if strings.HasPrefix(valPart, "{") {
			var sb strings.Builder
			sb.WriteString(valPart)
			j := idx + 1
			for !hasBalancedBraces(sb.String()) && j < len(lines) {
				st := stripYAMLComment(lines[j])
				if strings.TrimSpace(st) != "" && countYAMLIndent(st) <= entryIndent {
					break
				}
				sb.WriteByte(' ')
				sb.WriteString(strings.TrimSpace(st))
				j++
			}
			if name := formatYAMLPersonMap(parseYAMLFlowMap(sb.String())); name != "" {
				return []string{name}, j
			}
			return nil, j
		}
		val, next := parseYAMLScalarValue(lines, idx, entryIndent, valPart)
		if val == "" {
			return nil, next
		}
		return []string{val}, next
	}

	var authors []string
	listIndent := -1
	j := idx + 1
	for j < len(lines) {
		st := stripYAMLComment(lines[j])
		tr := strings.TrimSpace(st)
		if tr == "" {
			j++
			continue
		}
		ind := countYAMLIndent(st)
		if ind < entryIndent {
			break
		}
		if ind == entryIndent && tr != "-" && !strings.HasPrefix(tr, "- ") {
			break
		}
		if listIndent >= 0 {
			if ind < listIndent {
				break
			}
			if ind > listIndent {
				j++
				continue
			}
		}
		isSeqItem := tr == "-" || strings.HasPrefix(tr, "- ")
		if !isSeqItem {
			// Single-object author/editor mapping directly under `author:` / `editor:`.
			if len(authors) == 0 && listIndent < 0 {
				if _, _, isMap := splitYAMLMapping(tr); isMap {
					name, next := parseYAMLPersonBlock(lines, j, entryIndent, "", -1)
					if name != "" {
						authors = append(authors, name)
					}
					return authors, next
				}
			}
			j++
			continue
		}
		if listIndent < 0 {
			listIndent = ind
		}
		if tr == "-" {
			// Multi-line person map starting on next line.
			name, next := parseYAMLPersonBlock(lines, j+1, ind, "", -1)
			if name != "" {
				authors = append(authors, name)
			}
			j = next
			continue
		}
		if strings.HasPrefix(tr, "- ") {
			itemRest := strings.TrimSpace(tr[2:])
			if strings.HasPrefix(itemRest, "{") {
				var sb strings.Builder
				sb.WriteString(itemRest)
				k := j + 1
				for !hasBalancedBraces(sb.String()) && k < len(lines) {
					nst := stripYAMLComment(lines[k])
					if strings.TrimSpace(nst) != "" && countYAMLIndent(nst) <= ind {
						break
					}
					sb.WriteByte(' ')
					sb.WriteString(strings.TrimSpace(nst))
					k++
				}
				if name := formatYAMLPersonMap(parseYAMLFlowMap(sb.String())); name != "" {
					authors = append(authors, name)
				}
				j = k
				continue
			}
			if _, _, isMap := splitYAMLMapping(itemRest); isMap {
				firstCol := ind + 2
				if dashOff := strings.IndexByte(st, '-'); dashOff >= 0 {
					firstCol = countYAMLIndent(st[:dashOff]) + 1
					for k := dashOff + 1; k < len(st) && (st[k] == ' ' || st[k] == '\t'); k++ {
						if st[k] == '\t' {
							firstCol += 2
						} else {
							firstCol++
						}
					}
				}
				name, next := parseYAMLPersonBlock(lines, j+1, ind, itemRest, firstCol)
				if name != "" {
					authors = append(authors, name)
				}
				j = next
				continue
			}
			val, next := parseYAMLScalarValue(lines, j, ind, itemRest)
			if val != "" {
				authors = append(authors, val)
			}
			j = next
			continue
		}
		j++
	}
	return authors, j
}

func parseYAMLPersonBlock(lines []string, start, listIndent int, firstLine string, firstCol int) (string, int) {
	fields := make(map[string]string, 4)
	addKV := func(k, v string) {
		key := strings.ToLower(unquoteYAMLScalar(k))
		if _, exists := fields[key]; !exists {
			fields[key] = unquoteYAMLScalar(v)
		}
	}
	personIndent := -1
	if firstLine != "" {
		if firstCol >= 0 {
			personIndent = firstCol
		} else {
			personIndent = listIndent + 2
		}
		if k, v, ok := splitYAMLMapping(firstLine); ok {
			addKV(k, v)
		}
	}
	j := start
	for j < len(lines) {
		st := stripYAMLComment(lines[j])
		tr := strings.TrimSpace(st)
		if tr == "" {
			j++
			continue
		}
		ind := countYAMLIndent(st)
		if ind <= listIndent {
			break
		}
		if tr == "-" || strings.HasPrefix(tr, "- ") {
			j++
			continue
		}
		if personIndent < 0 {
			personIndent = ind
		}
		if ind == personIndent {
			if k, v, ok := splitYAMLMapping(tr); ok {
				addKV(k, v)
			}
		}
		j++
	}
	return formatYAMLPersonMap(fields), j
}

func formatYAMLPersonMap(fields map[string]string) string {
	if name := strings.TrimSpace(fields["name"]); name != "" {
		return name
	}
	given := strings.TrimSpace(fields["given-name"])
	if given == "" {
		given = strings.TrimSpace(fields["given"])
	}
	family := strings.TrimSpace(fields["family-name"])
	if family == "" {
		family = strings.TrimSpace(fields["surname"])
	}
	if family == "" {
		family = strings.TrimSpace(fields["family"])
	}
	switch {
	case given != "" && family != "":
		return given + " " + family
	case family != "":
		return family
	case given != "":
		return given
	default:
		return ""
	}
}

func parseYAMLFlowAuthorList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "[") {
		return nil
	}
	raw = strings.TrimPrefix(raw, "[")
	if end := strings.LastIndexByte(raw, ']'); end >= 0 {
		raw = raw[:end]
	}
	parts := splitYAMLFlowItems(raw)
	var authors []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "{") {
			if name := formatYAMLPersonMap(parseYAMLFlowMap(p)); name != "" {
				authors = append(authors, name)
			}
			continue
		}
		if v := unquoteYAMLScalar(p); v != "" {
			authors = append(authors, v)
		}
	}
	return authors
}

func parseYAMLFlowMap(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "{")
	if end := strings.LastIndexByte(raw, '}'); end >= 0 {
		raw = raw[:end]
	}
	out := make(map[string]string)
	for _, item := range splitYAMLFlowItems(raw) {
		item = strings.TrimSpace(item)
		if k, v, ok := splitYAMLMapping(item); ok {
			out[strings.ToLower(unquoteYAMLScalar(k))] = unquoteYAMLScalar(v)
		}
	}
	return out
}

func splitYAMLFlowItems(s string) []string {
	var parts []string
	braceDepth := 0
	bracketDepth := 0
	inSingle := false
	inDouble := false
	start := 0
	for i := 0; i < len(s); i++ {
		b := s[i]
		if inSingle {
			if b == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if b == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if b == '"' {
				inDouble = false
			}
			continue
		}
		switch b {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '{':
			braceDepth++
		case '}':
			if braceDepth > 0 {
				braceDepth--
			}
		case '[':
			bracketDepth++
		case ']':
			if bracketDepth > 0 {
				bracketDepth--
			}
		case ',':
			if braceDepth == 0 && bracketDepth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func hasBalancedFlowList(s string) bool {
	return hasBalancedDelims(s, '[', ']')
}

func hasBalancedBraces(s string) bool {
	return hasBalancedDelims(s, '{', '}')
}

func hasBalancedDelims(s string, openCh, closeCh byte) bool {
	depth := 0
	inSingle := false
	inDouble := false
	for i := 0; i < len(s); i++ {
		b := s[i]
		if inSingle {
			if b == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if b == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if b == '"' {
				inDouble = false
			}
			continue
		}
		if b == '\'' {
			inSingle = true
		} else if b == '"' {
			inDouble = true
		} else if b == openCh {
			depth++
		} else if b == closeCh {
			depth--
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

func findClosingYAMLQuoteOffset(s string, quote byte) (int, bool) {
	for i := 0; i < len(s); i++ {
		if quote == '\'' {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				return i, true
			}
		} else {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if s[i] == '"' {
				return i, true
			}
		}
	}
	return -1, false
}

func hasClosingYAMLQuote(s string, quote byte) bool {
	if len(s) < 2 || s[0] != quote {
		return false
	}
	for i := 1; i < len(s); i++ {
		if quote == '\'' {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				return i == len(s)-1
			}
		} else {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if s[i] == '"' {
				return i == len(s)-1
			}
		}
	}
	return false
}

func unquoteYAMLScalar(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}
	if s[0] == '\'' && s[len(s)-1] == '\'' {
		inner := s[1 : len(s)-1]
		return strings.ReplaceAll(inner, "''", "'")
	}
	if s[0] == '"' && s[len(s)-1] == '"' {
		inner := s[1 : len(s)-1]
		var sb strings.Builder
		sb.Grow(len(inner))
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) {
				next := inner[i+1]
				switch next {
				case '\\', '"':
					sb.WriteByte(next)
				case 'n':
					sb.WriteByte('\n')
				case 'r':
					sb.WriteByte('\r')
				case 't':
					sb.WriteByte('\t')
				default:
					sb.WriteByte(next)
				}
				i++
				continue
			}
			sb.WriteByte(inner[i])
		}
		return sb.String()
	}
	return s
}
