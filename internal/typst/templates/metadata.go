package templates

import (
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// CommentMetadata holds parsed leading metadata comments from a .typ template.
type CommentMetadata struct {
	Title       string
	Description string
	Category    string
	Keywords    []string
	Body        string
}

// ParseCommentMetadata extracts leading `// @title:`, `// @desc:`, and
// `// @category:` lines from src and returns the remaining template body.
func ParseCommentMetadata(src string) (title, desc, category, body string) {
	meta := ParseCommentMetadataFull(src)
	return meta.Title, meta.Description, meta.Category, meta.Body
}

// ParseCommentMetadataFull extracts leading `// @title:`, `// @desc:`,
// `// @category:`, and `// @tags:` / `// @keywords:` lines from src.
func ParseCommentMetadataFull(src string) CommentMetadata {
	norm := strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(norm, "\n")

	var meta CommentMetadata
	idx := 0
	foundAny := false
	for idx < len(lines) {
		trimmed := strings.TrimSpace(lines[idx])
		if !strings.HasPrefix(trimmed, "//") {
			break
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "//"))
		switch {
		case strings.HasPrefix(rest, "@title:"):
			meta.Title = strings.TrimSpace(strings.TrimPrefix(rest, "@title:"))
			foundAny = true
			idx++
		case strings.HasPrefix(rest, "@desc:"):
			meta.Description = strings.TrimSpace(strings.TrimPrefix(rest, "@desc:"))
			foundAny = true
			idx++
		case strings.HasPrefix(rest, "@category:"):
			meta.Category = strings.TrimSpace(strings.TrimPrefix(rest, "@category:"))
			foundAny = true
			idx++
		case strings.HasPrefix(rest, "@tags:"):
			meta.Keywords = append(meta.Keywords, splitMetadataList(strings.TrimPrefix(rest, "@tags:"))...)
			foundAny = true
			idx++
		case strings.HasPrefix(rest, "@keywords:"):
			meta.Keywords = append(meta.Keywords, splitMetadataList(strings.TrimPrefix(rest, "@keywords:"))...)
			foundAny = true
			idx++
		default:
			goto doneMeta
		}
	}

doneMeta:
	if !foundAny {
		meta.Body = norm
		return meta
	}
	if idx < len(lines) && strings.TrimSpace(lines[idx]) == "" {
		idx++
	}
	meta.Body = strings.Join(lines[idx:], "\n")
	return meta
}

func splitMetadataList(raw string) []string {
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// DefaultAuthor resolves a sensible default author string from environment variables.
func DefaultAuthor() string {
	for _, key := range []string{"TYPX_AUTHOR", "USER", "USERNAME"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return "Author"
}

// DeriveTitle builds a readable title from a target file or directory path.
func DeriveTitle(targetPath string) string {
	if strings.TrimSpace(targetPath) == "" {
		return "Untitled Document"
	}
	base := filepath.Base(targetPath)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" || stem == "." || stem == string(filepath.Separator) {
		return "Untitled Document"
	}
	if strings.EqualFold(stem, "main") {
		parent := filepath.Base(filepath.Dir(targetPath))
		if parent != "" && parent != "." && parent != string(filepath.Separator) {
			stem = parent
		}
	}
	return humanizeSlug(stem)
}

func humanizeSlug(slug string) string {
	cleaned := strings.NewReplacer("-", " ", "_", " ").Replace(slug)
	words := strings.Fields(cleaned)
	if len(words) == 0 {
		return "Untitled Document"
	}
	for i, w := range words {
		runes := []rune(w)
		runes[0] = unicode.ToUpper(runes[0])
		words[i] = string(runes)
	}
	return strings.Join(words, " ")
}

// ApplyVars substitutes {{title}}, {{author}}, and {{date}} placeholders in src.
func ApplyVars(src string, vars TemplateVars) string {
	title := strings.TrimSpace(vars.Title)
	if title == "" {
		title = "Untitled Document"
	}
	author := strings.TrimSpace(vars.Author)
	if author == "" {
		author = DefaultAuthor()
	}
	date := strings.TrimSpace(vars.Date)
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}

	replacer := strings.NewReplacer(
		"{{title}}", title,
		"{{author}}", author,
		"{{date}}", date,
	)
	return replacer.Replace(src)
}

// ExtractPreamble returns everything preceding the first top-level Typst heading
// (`= Title`), skipping raw code fences and nested block comments. If no top-level
// heading is found, the full normalized body is returned.
func ExtractPreamble(src string) string {
	_, _, _, body := ParseCommentMetadata(src)
	lines := strings.Split(body, "\n")

	var fenceDelim string
	blockCommentDepth := 0
	cutIdx := len(lines)

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if fenceDelim != "" {
			if strings.HasPrefix(trimmed, fenceDelim) {
				fenceDelim = ""
			}
			continue
		}

		// Track nested /* ... */ block comments before checking headings.
		if blockCommentDepth == 0 {
			if delim := leadingFence(trimmed); delim != "" {
				// Single-line fence ```code``` stays closed on same line.
				rest := trimmed[len(delim):]
				if !strings.Contains(rest, delim) {
					fenceDelim = delim
				}
				continue
			}
			if isTopLevelHeading(trimmed) {
				cutIdx = i
				break
			}
		}

		blockCommentDepth = advanceBlockCommentDepth(line, blockCommentDepth)
	}

	preamble := strings.TrimRight(strings.Join(lines[:cutIdx], "\n"), " \t\r\n")
	if preamble == "" {
		return ""
	}
	return preamble + "\n"
}

func leadingFence(trimmed string) string {
	if !strings.HasPrefix(trimmed, "```") {
		return ""
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == '`' {
		n++
	}
	return trimmed[:n]
}

func isTopLevelHeading(trimmed string) bool {
	if trimmed == "=" {
		return true
	}
	return len(trimmed) >= 2 && trimmed[0] == '=' && (trimmed[1] == ' ' || trimmed[1] == '\t')
}

func advanceBlockCommentDepth(line string, depth int) int {
	inString := false
	escaped := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		if depth == 0 && i+1 < len(line) && ch == '/' && line[i+1] == '/' {
			break
		}
		if ch == '"' && depth == 0 {
			inString = true
			continue
		}
		if i+1 < len(line) && ch == '/' && line[i+1] == '*' {
			depth++
			i++
			continue
		}
		if depth > 0 && i+1 < len(line) && ch == '*' && line[i+1] == '/' {
			depth--
			i++
			continue
		}
	}
	return depth
}
