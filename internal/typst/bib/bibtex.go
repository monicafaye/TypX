package bib

import (
	"bytes"
	"strings"
)

var defaultBibMonths = map[string]string{
	"jan": "January",
	"feb": "February",
	"mar": "March",
	"apr": "April",
	"may": "May",
	"jun": "June",
	"jul": "July",
	"aug": "August",
	"sep": "September",
	"oct": "October",
	"nov": "November",
	"dec": "December",
}

// ParseBibTeX parses BibTeX (.bib) bytes in pure Go, supporting nested braces,
// quoted strings with internal braces and escaped quotes, `#` concatenation,
// `@string` macro expansion, and skipping `@comment` / `@preamble`.
func ParseBibTeX(data []byte) []Entry {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if len(data) == 0 {
		return nil
	}

	macros := make(map[string]string, len(defaultBibMonths))
	for k, v := range defaultBibMonths {
		macros[k] = v
	}

	var entries []Entry
	i := 0
	for i < len(data) {
		// Advance to next '@' (outside '@' everything is BibTeX commentary).
		if data[i] != '@' {
			i++
			continue
		}
		i++ // skip '@'
		typeStart := i
		for i < len(data) && isBibTypeChar(data[i]) {
			i++
		}
		if i == typeStart {
			continue
		}
		entryType := strings.ToLower(string(data[typeStart:i]))
		i = skipBibWS(data, i)
		if i >= len(data) || (data[i] != '{' && data[i] != '(') {
			continue
		}
		openDelim := data[i]
		closeDelim := byte('}')
		if openDelim == '(' {
			closeDelim = ')'
		}
		blockEnd := findBalancedBibBlockEnd(data, i, openDelim, closeDelim, entryType != "comment")

		switch entryType {
		case "comment", "preamble":
			i = blockEnd
			continue
		case "string":
			parseBibStringMacro(data, i+1, blockEnd-1, closeDelim, macros)
			i = blockEnd
			continue
		}

		// Regular entry: read citekey up to first ','.
		pos := skipBibWS(data, i+1)
		limit := blockEnd
		if limit > 0 && data[limit-1] == closeDelim {
			limit--
		}
		keyStart := pos
		for pos < limit && data[pos] != ',' {
			pos++
		}
		if pos >= limit {
			i = blockEnd
			continue
		}
		key := strings.TrimSpace(string(data[keyStart:pos]))
		pos++ // skip ','

		if key == "" {
			i = blockEnd
			continue
		}

		fields := parseBibFields(data, pos, limit, closeDelim, macros)
		authors := formatBibAuthors(fields["author"])
		if authors == "" {
			authors = formatBibAuthors(fields["editor"])
		}
		year := extractYear(fields["year"])
		if year == "" {
			year = extractYear(fields["date"])
		}

		entries = append(entries, Entry{
			Key:     key,
			Type:    entryType,
			Title:   normalizeBibText(fields["title"]),
			Authors: authors,
			Year:    year,
		})

		i = blockEnd
	}

	return entries
}

func parseBibStringMacro(data []byte, start, end int, closeDelim byte, macros map[string]string) {
	pos := skipBibWS(data, start)
	for pos < end {
		if data[pos] == ',' {
			pos = skipBibWS(data, pos+1)
			continue
		}
		nameStart := pos
		for pos < end && isBibFieldChar(data[pos]) {
			pos++
		}
		if pos == nameStart {
			break
		}
		name := strings.ToLower(string(data[nameStart:pos]))
		pos = skipBibWS(data, pos)
		if pos >= end || data[pos] != '=' {
			pos = skipToBibFieldSep(data, pos, end)
			continue
		}
		pos = skipBibWS(data, pos+1)
		val, next := parseBibValue(data, pos, end, closeDelim, macros)
		if name != "" {
			macros[name] = val
		}
		pos = skipBibWS(data, next)
	}
}

func parseBibFields(data []byte, start, end int, closeDelim byte, macros map[string]string) map[string]string {
	fields := make(map[string]string, 6)
	pos := start
	for pos < end {
		pos = skipBibWSAndCommas(data, pos, end)
		if pos >= end {
			break
		}
		nameStart := pos
		for pos < end && isBibFieldChar(data[pos]) {
			pos++
		}
		if pos == nameStart {
			pos++
			continue
		}
		name := strings.ToLower(string(data[nameStart:pos]))
		pos = skipBibWS(data, pos)
		if pos >= end || data[pos] != '=' {
			pos = skipToBibFieldSep(data, pos, end)
			continue
		}
		pos = skipBibWS(data, pos+1)
		val, next := parseBibValue(data, pos, end, closeDelim, macros)
		if name == "author" || name == "editor" || name == "title" || name == "year" || name == "date" {
			fields[name] = val
		}
		pos = next
	}
	return fields
}

func parseBibValue(data []byte, pos, end int, closeDelim byte, macros map[string]string) (string, int) {
	var sb strings.Builder
	for pos < end {
		pos = skipBibWS(data, pos)
		if pos >= end {
			break
		}
		b := data[pos]
		switch {
		case b == '{':
			val, next := readBracedBibAtom(data, pos, end)
			sb.WriteString(val)
			pos = next
		case b == '"':
			val, next := readQuotedBibAtom(data, pos, end)
			sb.WriteString(val)
			pos = next
		default:
			tokStart := pos
			for pos < end {
				c := data[pos]
				if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' || c == '#' || c == '%' || c == closeDelim {
					break
				}
				pos++
			}
			tok := string(data[tokStart:pos])
			if exp, ok := macros[strings.ToLower(tok)]; ok {
				sb.WriteString(exp)
			} else {
				sb.WriteString(tok)
			}
		}

		pos = skipBibWS(data, pos)
		if pos < end && data[pos] == '#' {
			pos++
			continue
		}
		break
	}
	return sb.String(), pos
}

func readBracedBibAtom(data []byte, openIdx, end int) (string, int) {
	depth := 1
	i := openIdx + 1
	start := i
	for i < end && depth > 0 {
		b := data[i]
		if b == '\\' && i+1 < end {
			i += 2
			continue
		}
		if b == '{' {
			depth++
		} else if b == '}' {
			depth--
			if depth == 0 {
				return string(data[start:i]), i + 1
			}
		}
		i++
	}
	return string(data[start:i]), i
}

func readQuotedBibAtom(data []byte, openIdx, end int) (string, int) {
	braceDepth := 0
	i := openIdx + 1
	start := i
	for i < end {
		b := data[i]
		if b == '\\' && i+1 < end {
			i += 2
			continue
		}
		if b == '{' {
			braceDepth++
		} else if b == '}' {
			if braceDepth > 0 {
				braceDepth--
			}
		} else if b == '"' && braceDepth == 0 {
			return string(data[start:i]), i + 1
		}
		i++
	}
	return string(data[start:i]), i
}

func findBalancedBibBlockEnd(data []byte, openIdx int, openDelim, closeDelim byte, allowQuotes bool) int {
	braceDepth := 0
	parenDepth := 0
	if openDelim == '{' {
		braceDepth = 1
	} else {
		parenDepth = 1
	}
	inStr := false
	strBraceDepth := 0
	i := openIdx + 1
	for i < len(data) && (braceDepth > 0 || parenDepth > 0) {
		b := data[i]
		if b == '\\' && i+1 < len(data) {
			i += 2
			continue
		}
		if inStr {
			if b == '{' {
				strBraceDepth++
			} else if b == '}' {
				if strBraceDepth > 0 {
					strBraceDepth--
				}
			} else if b == '"' && strBraceDepth == 0 {
				inStr = false
			}
			i++
			continue
		}
		if allowQuotes && b == '"' && ((openDelim == '{' && braceDepth == 1) || (openDelim == '(' && braceDepth == 0)) {
			inStr = true
			strBraceDepth = 0
			i++
			continue
		}
		if b == '{' {
			braceDepth++
		} else if b == '}' {
			if braceDepth > 0 {
				braceDepth--
			}
		} else if openDelim == '(' && braceDepth == 0 {
			if b == '(' {
				parenDepth++
			} else if b == ')' {
				parenDepth--
			}
		}
		i++
	}
	return i
}

func skipToBibFieldSep(data []byte, pos, end int) int {
	braceDepth := 0
	inStr := false
	strBraceDepth := 0
	for pos < end {
		b := data[pos]
		if b == '\\' && pos+1 < end {
			pos += 2
			continue
		}
		if inStr {
			if b == '{' {
				strBraceDepth++
			} else if b == '}' && strBraceDepth > 0 {
				strBraceDepth--
			} else if b == '"' && strBraceDepth == 0 {
				inStr = false
			}
			pos++
			continue
		}
		if b == '"' && braceDepth == 0 {
			inStr = true
			strBraceDepth = 0
			pos++
			continue
		}
		if b == '{' {
			braceDepth++
		} else if b == '}' {
			if braceDepth > 0 {
				braceDepth--
			}
		} else if b == ',' && braceDepth == 0 {
			return pos + 1
		}
		pos++
	}
	return pos
}

func skipBibWS(data []byte, i int) int {
	for i < len(data) {
		b := data[i]
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			i++
			continue
		}
		if b == '%' {
			for i < len(data) && data[i] != '\n' {
				i++
			}
			continue
		}
		break
	}
	return i
}

func skipBibWSAndCommas(data []byte, i, end int) int {
	for i < end {
		b := data[i]
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == ',' {
			i++
			continue
		}
		if b == '%' {
			for i < end && data[i] != '\n' {
				i++
			}
			continue
		}
		break
	}
	return i
}

func isBibTypeChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}

func isBibFieldChar(b byte) bool {
	return isBibTypeChar(b) || b == ':'
}

func normalizeBibText(s string) string {
	if s == "" {
		return ""
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b == '\\' && i+1 < len(s) {
			next := s[i+1]
			switch next {
			case '{', '}', '&', '%', '_', '#', '$', '"', '\\':
				sb.WriteByte(next)
				i++
				continue
			}
		}
		if b == '{' || b == '}' {
			continue
		}
		sb.WriteByte(b)
	}
	return collapseWhitespace(sb.String())
}

func formatBibAuthors(raw string) string {
	raw = collapseWhitespace(raw)
	if raw == "" {
		return ""
	}
	parts := splitBibAuthors(raw)
	var out []string
	for _, p := range parts {
		clean := normalizeBibText(p)
		if clean != "" {
			out = append(out, clean)
		}
	}
	return strings.Join(out, "; ")
}

func splitBibAuthors(s string) []string {
	var parts []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b == '\\' && i+1 < len(s) {
			i++
			continue
		}
		if b == '{' {
			depth++
			continue
		}
		if b == '}' {
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth == 0 && i+5 <= len(s) && s[i] == ' ' && s[i+4] == ' ' {
			mid := s[i+1 : i+4]
			if strings.EqualFold(mid, "and") {
				parts = append(parts, s[start:i])
				start = i + 5
				i += 4
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func collapseWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func extractYear(s string) string {
	s = normalizeBibText(s)
	if s == "" {
		return ""
	}
	for i := 0; i+4 <= len(s); i++ {
		if i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
			continue
		}
		if i+4 < len(s) && s[i+4] >= '0' && s[i+4] <= '9' {
			continue
		}
		chunk := s[i : i+4]
		if chunk[0] >= '1' && chunk[0] <= '2' &&
			chunk[1] >= '0' && chunk[1] <= '9' &&
			chunk[2] >= '0' && chunk[2] <= '9' &&
			chunk[3] >= '0' && chunk[3] <= '9' {
			return chunk
		}
	}
	return ""
}
