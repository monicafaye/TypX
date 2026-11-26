package templates

import (
	"fmt"
	"strings"
)

// TypstPackageMeta holds relevant [package] fields from typst.toml.
type TypstPackageMeta struct {
	Name        string
	Version     string
	Entrypoint  string
	Description string
	Authors     []string
	Categories  []string
	Disciplines []string
	Keywords    []string
}

// TypstTemplateMeta holds relevant [template] fields from typst.toml.
type TypstTemplateMeta struct {
	Path       string
	Entrypoint string
	Thumbnail  string
}

// TypstManifest represents parsed package and template sections of typst.toml.
type TypstManifest struct {
	Package  TypstPackageMeta
	Template TypstTemplateMeta
}

// HasTemplate reports whether typst.toml declares both template.path and template.entrypoint.
func (m TypstManifest) HasTemplate() bool {
	return m.Template.Path != "" && m.Template.Entrypoint != ""
}

// ParseTypstTOML extracts [package] and [template] fields from typst.toml bytes
// without external dependencies.
func ParseTypstTOML(data []byte) (TypstManifest, error) {
	src := strings.TrimPrefix(string(data), "\xef\xbb\xbf")
	src = strings.ReplaceAll(src, "\r\n", "\n")

	var out TypstManifest
	section := ""
	lines := strings.Split(src, "\n")

	for i := 0; i < len(lines); i++ {
		line := stripTOMLComment(lines[i])
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			continue
		}

		eq := strings.IndexByte(trimmed, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:eq])
		valRaw := strings.TrimSpace(trimmed[eq+1:])

		// Join multi-line inline arrays `[ ... ]`.
		for strings.HasPrefix(valRaw, "[") && !hasBalancedBrackets(valRaw) && i+1 < len(lines) {
			i++
			next := strings.TrimSpace(stripTOMLComment(lines[i]))
			if next != "" {
				valRaw += " " + next
			}
		}

		switch section {
		case "package":
			switch key {
			case "name":
				if s, ok := parseTOMLString(valRaw); ok {
					out.Package.Name = s
				}
			case "version":
				if s, ok := parseTOMLString(valRaw); ok {
					out.Package.Version = s
				}
			case "entrypoint":
				if s, ok := parseTOMLString(valRaw); ok {
					out.Package.Entrypoint = s
				}
			case "description":
				if s, ok := parseTOMLString(valRaw); ok {
					out.Package.Description = s
				}
			case "authors":
				out.Package.Authors = parseTOMLStringArray(valRaw)
			case "categories":
				out.Package.Categories = parseTOMLStringArray(valRaw)
			case "disciplines":
				out.Package.Disciplines = parseTOMLStringArray(valRaw)
			case "keywords":
				out.Package.Keywords = parseTOMLStringArray(valRaw)
			}
		case "template":
			switch key {
			case "path":
				if s, ok := parseTOMLString(valRaw); ok {
					out.Template.Path = s
				}
			case "entrypoint":
				if s, ok := parseTOMLString(valRaw); ok {
					out.Template.Entrypoint = s
				}
			case "thumbnail":
				if s, ok := parseTOMLString(valRaw); ok {
					out.Template.Thumbnail = s
				}
			}
		}
	}

	if out.Package.Name == "" && out.Template.Path == "" && out.Template.Entrypoint == "" {
		return out, fmt.Errorf("typst.toml missing [package] or [template] metadata")
	}
	return out, nil
}

func stripTOMLComment(line string) string {
	var quote byte
	escaped := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if quote == '"' {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				quote = 0
			}
			continue
		}
		if quote == '\'' {
			if ch == '\'' {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			continue
		}
		if ch == '#' {
			return line[:i]
		}
	}
	return line
}

func hasBalancedBrackets(s string) bool {
	depth := 0
	var quote byte
	escaped := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote == '"' {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				quote = 0
			}
			continue
		}
		if quote == '\'' {
			if ch == '\'' {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			continue
		}
		if ch == '[' {
			depth++
		} else if ch == ']' {
			depth--
			if depth <= 0 {
				return true
			}
		}
	}
	return false
}

func parseTOMLString(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 {
		return "", false
	}
	if raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		return raw[1 : len(raw)-1], true
	}
	if raw[0] == '"' && raw[len(raw)-1] == '"' {
		var b strings.Builder
		escaped := false
		for i := 1; i < len(raw)-1; i++ {
			ch := raw[i]
			if escaped {
				switch ch {
				case 'n':
					b.WriteByte('\n')
				case 'r':
					b.WriteByte('\r')
				case 't':
					b.WriteByte('\t')
				case '\\', '"':
					b.WriteByte(ch)
				default:
					b.WriteByte(ch)
				}
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			b.WriteByte(ch)
		}
		return b.String(), true
	}
	return "", false
}

func parseTOMLStringArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '[' || raw[len(raw)-1] != ']' {
		return nil
	}
	inner := raw[1 : len(raw)-1]
	var items []string
	var cur strings.Builder
	var quote byte
	escaped := false

	flush := func() {
		if tok := strings.TrimSpace(cur.String()); tok != "" {
			if s, ok := parseTOMLString(tok); ok {
				items = append(items, s)
			}
		}
		cur.Reset()
	}

	for i := 0; i < len(inner); i++ {
		ch := inner[i]
		if quote == '"' {
			cur.WriteByte(ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				quote = 0
			}
			continue
		}
		if quote == '\'' {
			cur.WriteByte(ch)
			if ch == '\'' {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			cur.WriteByte(ch)
			continue
		}
		if ch == ',' {
			flush()
			continue
		}
		cur.WriteByte(ch)
	}
	flush()
	return items
}
