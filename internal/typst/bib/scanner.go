package bib

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/monicafaye/TypX/internal/buffer"
)

type bibScanMode int

const (
	bibModeMarkup bibScanMode = iota
	bibModeCodeBlock
	bibModeCodeParens
	bibModeCodeStmt
)

type bibScanFrame struct {
	mode          bibScanMode
	closeByte     byte
	postfixMarkup bool
	inString      bool
	inMath        bool
	lastNonWSByte byte
}

// ScanBibliographyCalls scans Typst source bytes for `#bibliography("...")`
// and `#bibliography(("a.bib", "b.yml", ...))` calls outside comments, raw
// backtick spans/blocks, math blocks, code string literals, and backslash escapes.
func ScanBibliographyCalls(src []byte) []string {
	src = bytes.TrimPrefix(src, []byte{0xef, 0xbb, 0xbf})
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	if len(src) == 0 {
		return nil
	}

	var out []string
	inRaw := false
	rawFenceLen := 0
	commentDepth := 0
	stack := []bibScanFrame{{mode: bibModeMarkup}}

	popFrame := func(idx int) int {
		if len(stack) <= 1 {
			return idx
		}
		popped := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if popped.postfixMarkup && len(stack) > 0 && stack[len(stack)-1].mode == bibModeMarkup {
			idx = pushPostfixChain(src, idx, &stack)
		}
		return idx
	}

	for i := 0; i < len(src); {
		b := src[i]

		if inRaw {
			if b == '`' {
				runLen := countBackticks(src, i)
				i += runLen
				if runLen >= rawFenceLen {
					inRaw = false
					rawFenceLen = 0
				}
				continue
			}
			_, sz := utf8.DecodeRune(src[i:])
			i += sz
			continue
		}

		if commentDepth > 0 {
			if b == '/' && i+1 < len(src) && src[i+1] == '*' {
				commentDepth++
				i += 2
				continue
			}
			if b == '*' && i+1 < len(src) && src[i+1] == '/' {
				commentDepth--
				i += 2
				continue
			}
			_, sz := utf8.DecodeRune(src[i:])
			i += sz
			continue
		}

		top := &stack[len(stack)-1]

		if top.inString {
			if b == '\\' && i+1 < len(src) {
				i += 2
				continue
			}
			if b == '"' {
				top.inString = false
				top.lastNonWSByte = '"'
			}
			i++
			continue
		}

		if b == '/' && i+1 < len(src) && src[i+1] == '/' {
			if top.mode != bibModeMarkup || top.inMath || !isURLPrefix(src, i) {
				for i < len(src) && src[i] != '\n' {
					i++
				}
				continue
			}
		}

		if b == '/' && i+1 < len(src) && src[i+1] == '*' {
			commentDepth++
			i += 2
			continue
		}

		if top.inMath {
			if b == '\\' && i+1 < len(src) {
				i += 2
				continue
			}
			if b == '"' {
				top.inString = true
				i++
				continue
			}
			if b == '$' {
				top.inMath = false
				top.lastNonWSByte = '$'
			}
			i++
			continue
		}

		if top.mode == bibModeMarkup {
			if b == '\\' && i+1 < len(src) {
				i++
				_, sz := utf8.DecodeRune(src[i:])
				i += sz
				continue
			}

			if b == '`' {
				runLen := countBackticks(src, i)
				i += runLen
				if runLen != 2 {
					inRaw = true
					rawFenceLen = runLen
				}
				continue
			}

			if b == '$' {
				top.inMath = true
				i++
				continue
			}

			if top.closeByte == ']' {
				if b == '[' {
					stack = append(stack, bibScanFrame{mode: bibModeMarkup, closeByte: ']'})
					i++
					continue
				}
				if b == ']' {
					i = popFrame(i + 1)
					continue
				}
			}

			if b == '#' && i+1 < len(src) {
				next := src[i+1]
				switch {
				case next == '{':
					stack = append(stack, bibScanFrame{mode: bibModeCodeBlock, closeByte: '}'})
					i += 2
					continue
				case next == '(':
					stack = append(stack, bibScanFrame{mode: bibModeCodeParens, closeByte: ')'})
					i += 2
					continue
				case next == '[':
					stack = append(stack, bibScanFrame{mode: bibModeMarkup, closeByte: ']'})
					i += 2
					continue
				case isIdentStartAt(src, i+1):
					ident, endIdent := readIdentAt(src, i+1)
					if ident == "bibliography" {
						j := skipWSAndComments(src, endIdent)
						if j < len(src) && src[j] == '(' {
							paths, nextIdx := parseBibliographyArgs(src, j)
							out = append(out, paths...)
							i = nextIdx
							continue
						}
						i = endIdent
						continue
					}
					if isTypstCodeStmtKeyword(ident) {
						stack = append(stack, bibScanFrame{mode: bibModeCodeStmt})
						i = endIdent
						continue
					}
					i = pushPostfixChain(src, endIdent, &stack)
					continue
				}
			}

			_, sz := utf8.DecodeRune(src[i:])
			i += sz
			continue
		}

		// Code mode: bibModeCodeBlock, bibModeCodeParens, bibModeCodeStmt.
		if b == '`' {
			runLen := countBackticks(src, i)
			i += runLen
			if runLen != 2 {
				inRaw = true
				rawFenceLen = runLen
			}
			continue
		}

		if b == '$' {
			top.inMath = true
			i++
			continue
		}

		if b == '"' {
			top.inString = true
			i++
			continue
		}

		if top.mode == bibModeCodeStmt {
			if b == ';' {
				stack = stack[:len(stack)-1]
				i++
				continue
			}
			if b == '\n' {
				if !codeStmtContinuesAfter(top.lastNonWSByte) {
					stack = stack[:len(stack)-1]
				}
				i++
				continue
			}
		}

		switch b {
		case '{':
			top.lastNonWSByte = '{'
			stack = append(stack, bibScanFrame{mode: bibModeCodeBlock, closeByte: '}'})
			i++
			continue
		case '(':
			top.lastNonWSByte = '('
			stack = append(stack, bibScanFrame{mode: bibModeCodeParens, closeByte: ')'})
			i++
			continue
		case '[':
			top.lastNonWSByte = '['
			stack = append(stack, bibScanFrame{mode: bibModeMarkup, closeByte: ']'})
			i++
			continue
		case '}':
			if top.mode == bibModeCodeBlock {
				i = popFrame(i + 1)
				continue
			}
		case ')':
			if top.mode == bibModeCodeParens {
				i = popFrame(i + 1)
				continue
			}
		}

		if isIdentStartAt(src, i) {
			prevByte := byte(0)
			if i > 0 {
				prevByte = src[i-1]
			}
			ident, endIdent := readIdentAt(src, i)
			if ident == "bibliography" && prevByte != '.' {
				j := skipWSAndComments(src, endIdent)
				if j < len(src) && src[j] == '(' {
					paths, nextIdx := parseBibliographyArgs(src, j)
					out = append(out, paths...)
					stack[len(stack)-1].lastNonWSByte = ')'
					i = nextIdx
					continue
				}
			}
			if endIdent > i {
				top.lastNonWSByte = src[endIdent-1]
			}
			i = endIdent
			continue
		}

		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			top.lastNonWSByte = b
		}
		_, sz := utf8.DecodeRune(src[i:])
		i += sz
	}

	return out
}

func pushPostfixChain(src []byte, pos int, stack *[]bibScanFrame) int {
	for pos < len(src) && src[pos] == '.' && isIdentStartAt(src, pos+1) {
		_, nextEnd := readIdentAt(src, pos+1)
		pos = nextEnd
	}
	if pos < len(src) {
		switch src[pos] {
		case '(':
			*stack = append(*stack, bibScanFrame{
				mode:          bibModeCodeParens,
				closeByte:     ')',
				postfixMarkup: true,
			})
			return pos + 1
		case '[':
			*stack = append(*stack, bibScanFrame{
				mode:          bibModeMarkup,
				closeByte:     ']',
				postfixMarkup: true,
			})
			return pos + 1
		}
	}
	return pos
}

func codeStmtContinuesAfter(b byte) bool {
	switch b {
	case '=', '+', '-', '*', '/', ',', ':', '>':
		return true
	default:
		return false
	}
}

func isTypstCodeStmtKeyword(ident string) bool {
	switch ident {
	case "let", "set", "show", "if", "else", "for", "while", "import", "include", "export", "context", "return":
		return true
	default:
		return false
	}
}

func isIdentStartAt(src []byte, idx int) bool {
	if idx >= len(src) {
		return false
	}
	b := src[idx]
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

func readIdentAt(src []byte, idx int) (string, int) {
	end := idx
	for end < len(src) && isIdentChar(src[end]) {
		end++
	}
	return string(src[idx:end]), end
}

func parseBibliographyArgs(src []byte, openParenIdx int) ([]string, int) {
	j := openParenIdx + 1
	var paths []string

	for j < len(src) {
		j = skipWSAndComments(src, j)
		if j >= len(src) {
			break
		}
		if src[j] == ')' {
			return paths, j + 1
		}
		if src[j] == ',' {
			j++
			continue
		}
		if src[j] == '\\' {
			// A backslash at code top level is malformed call syntax; do not swallow following lines.
			return nil, openParenIdx + 1
		}

		// Skip named arguments (`ident: expr`), e.g. `style: "ieee"`, `title: "Refs"`.
		if isIdentStartAt(src, j) {
			_, idEnd := readIdentAt(src, j)
			afterID := skipWSAndComments(src, idEnd)
			if afterID < len(src) && src[afterID] == ':' {
				next, ok := skipCallArg(src, afterID+1)
				if !ok {
					return nil, openParenIdx + 1
				}
				j = next
				continue
			}
		}

		switch src[j] {
		case '"':
			if s, next, ok := parseTypstString(src, j); ok {
				if s != "" {
					paths = append(paths, s)
				}
				after, argOK := skipCallArg(src, next)
				if !argOK {
					return nil, openParenIdx + 1
				}
				j = after
			} else {
				return nil, openParenIdx + 1
			}
		case '(':
			tuplePaths, next, ok := parseBibTuple(src, j)
			if !ok {
				return nil, openParenIdx + 1
			}
			paths = append(paths, tuplePaths...)
			after, argOK := skipCallArg(src, next)
			if !argOK {
				return nil, openParenIdx + 1
			}
			j = after
		default:
			next, ok := skipCallArg(src, j)
			if !ok {
				return nil, openParenIdx + 1
			}
			j = next
		}
	}

	return nil, openParenIdx + 1
}

func parseBibTuple(src []byte, openParenIdx int) ([]string, int, bool) {
	j := openParenIdx + 1
	var paths []string
	for j < len(src) {
		j = skipWSAndComments(src, j)
		if j >= len(src) {
			return nil, j, false
		}
		if src[j] == ')' {
			return paths, j + 1, true
		}
		if src[j] == ',' {
			j++
			continue
		}
		if src[j] == '\\' {
			return nil, j, false
		}
		if src[j] == '"' {
			if s, next, ok := parseTypstString(src, j); ok {
				if s != "" {
					paths = append(paths, s)
				}
				after, argOK := skipCallArg(src, next)
				if !argOK {
					return nil, next, false
				}
				j = after
				continue
			}
			return nil, j, false
		}
		next, ok := skipCallArg(src, j)
		if !ok {
			return nil, j, false
		}
		j = next
	}
	return nil, len(src), false
}

func skipCallArg(src []byte, i int) (int, bool) {
	parenDepth := 0
	bracketDepth := 0
	braceDepth := 0
	inStr := false
	inRaw := false
	rawFence := 0
	commentDepth := 0

	for i < len(src) {
		b := src[i]
		if inRaw {
			if b == '`' {
				runLen := countBackticks(src, i)
				i += runLen
				if runLen >= rawFence {
					inRaw = false
					rawFence = 0
				}
				continue
			}
			i++
			continue
		}
		if commentDepth > 0 {
			if b == '/' && i+1 < len(src) && src[i+1] == '*' {
				commentDepth++
				i += 2
				continue
			}
			if b == '*' && i+1 < len(src) && src[i+1] == '/' {
				commentDepth--
				i += 2
				continue
			}
			i++
			continue
		}
		if inStr {
			if b == '\\' && i+1 < len(src) {
				i += 2
				continue
			}
			if b == '"' {
				inStr = false
			}
			i++
			continue
		}
		if b == '/' && i+1 < len(src) && src[i+1] == '/' {
			if bracketDepth == 0 || !isURLPrefix(src, i) {
				for i < len(src) && src[i] != '\n' {
					i++
				}
				continue
			}
		}
		if b == '/' && i+1 < len(src) && src[i+1] == '*' {
			commentDepth++
			i += 2
			continue
		}
		if b == '`' {
			runLen := countBackticks(src, i)
			i += runLen
			if runLen != 2 {
				inRaw = true
				rawFence = runLen
			}
			continue
		}
		if bracketDepth > 0 {
			if b == '\\' && i+1 < len(src) {
				i += 2
				continue
			}
			if b == '[' {
				bracketDepth++
			} else if b == ']' {
				bracketDepth--
			}
			i++
			continue
		}
		if b == '\\' {
			return i, false
		}
		if b == '"' {
			inStr = true
			i++
			continue
		}
		switch b {
		case '(':
			parenDepth++
		case ')':
			if parenDepth == 0 && braceDepth == 0 {
				return i, true
			}
			if parenDepth > 0 {
				parenDepth--
			}
		case '[':
			bracketDepth++
		case '{':
			braceDepth++
		case '}':
			if braceDepth > 0 {
				braceDepth--
			}
		case ',':
			if parenDepth == 0 && braceDepth == 0 {
				return i, true
			}
		}
		i++
	}
	return i, false
}

func skipWSAndComments(src []byte, i int) int {
	for i < len(src) {
		b := src[i]
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			i++
			continue
		}
		if b == '/' && i+1 < len(src) && src[i+1] == '/' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		}
		if b == '/' && i+1 < len(src) && src[i+1] == '*' {
			depth := 1
			i += 2
			for i < len(src) && depth > 0 {
				if src[i] == '/' && i+1 < len(src) && src[i+1] == '*' {
					depth++
					i += 2
				} else if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
					depth--
					i += 2
				} else {
					i++
				}
			}
			continue
		}
		break
	}
	return i
}

func parseTypstString(src []byte, openIdx int) (string, int, bool) {
	if openIdx >= len(src) || src[openIdx] != '"' {
		return "", openIdx, false
	}
	var sb strings.Builder
	i := openIdx + 1
	for i < len(src) {
		b := src[i]
		if b == '"' {
			return sb.String(), i + 1, true
		}
		if b == '\\' && i+1 < len(src) {
			next := src[i+1]
			switch next {
			case '\\', '"':
				sb.WriteByte(next)
				i += 2
			case 'n':
				sb.WriteByte('\n')
				i += 2
			case 'r':
				sb.WriteByte('\r')
				i += 2
			case 't':
				sb.WriteByte('\t')
				i += 2
			default:
				r, sz := utf8.DecodeRune(src[i+1:])
				sb.WriteRune(r)
				i += 1 + sz
			}
			continue
		}
		r, sz := utf8.DecodeRune(src[i:])
		sb.WriteRune(r)
		i += sz
	}
	return "", openIdx, false
}

func countBackticks(src []byte, idx int) int {
	n := 0
	for idx+n < len(src) && src[idx+n] == '`' {
		n++
	}
	return n
}

func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}

func isURLPrefix(src []byte, slashIdx int) bool {
	prefix := src[:slashIdx]
	if len(prefix) >= 5 && bytes.Equal(prefix[len(prefix)-5:], []byte("http:")) {
		return len(prefix) == 5 || !isIdentChar(prefix[len(prefix)-6])
	}
	if len(prefix) >= 6 && bytes.Equal(prefix[len(prefix)-6:], []byte("https:")) {
		return len(prefix) == 6 || !isIdentChar(prefix[len(prefix)-7])
	}
	return false
}

// ResolveBibliographyFiles discovers and resolves bibliography file paths for b
// by scanning `#bibliography(...)` in b and the pinned main file (checking
// buffer.OpenBuffers first), falling back to `*.bib` in the project root when
// no `#bibliography(...)` call is declared.
func ResolveBibliographyFiles(b *buffer.Buffer) []string {
	activePath := bufferAbsPath(b)
	rootDir := ""
	if rfn := getRootResolver(); rfn != nil {
		rootDir = rfn(activePath)
	}
	if rootDir == "" && activePath != "" {
		rootDir = filepath.Dir(activePath)
	}

	mainPath := ""
	if mfn := getMainFileResolver(); mfn != nil {
		mainPath = mfn(activePath)
	}
	if rootDir == "" && mainPath != "" {
		rootDir = filepath.Dir(mainPath)
	}

	type declaredRef struct {
		raw     string
		baseDir string
	}
	var declared []declaredRef

	if b != nil {
		baseDir := rootDir
		if activePath != "" {
			baseDir = filepath.Dir(activePath)
		}
		for _, p := range ScanBibliographyCalls(b.Bytes()) {
			declared = append(declared, declaredRef{raw: p, baseDir: baseDir})
		}
	}

	if mainPath != "" && canonicalBibPath(mainPath) != canonicalBibPath(activePath) {
		if mainBytes := readOpenOrDiskBytes(mainPath); len(mainBytes) > 0 {
			mainBase := filepath.Dir(mainPath)
			for _, p := range ScanBibliographyCalls(mainBytes) {
				declared = append(declared, declaredRef{raw: p, baseDir: mainBase})
			}
		}
	}

	var resolved []string
	seen := make(map[string]bool)
	addPath := func(p string) {
		if p == "" {
			return
		}
		key := canonicalBibPath(p)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		resolved = append(resolved, filepath.Clean(p))
	}

	if len(declared) > 0 {
		for _, ref := range declared {
			addPath(resolveBibPath(ref.raw, ref.baseDir, rootDir))
		}
		return resolved
	}

	// Fallback: *.bib files in rootDir (non-recursive, sorted lexicographically).
	if rootDir != "" {
		entries, err := os.ReadDir(rootDir)
		if err == nil {
			var names []string
			for _, ent := range entries {
				if ent.IsDir() {
					continue
				}
				if strings.EqualFold(filepath.Ext(ent.Name()), ".bib") {
					names = append(names, ent.Name())
				}
			}
			sort.Strings(names)
			for _, name := range names {
				addPath(filepath.Join(rootDir, name))
			}
		}
	}

	return resolved
}

func resolveBibPath(rawPath, baseDir, rootDir string) string {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" || strings.HasPrefix(rawPath, "@") ||
		strings.HasPrefix(rawPath, "http://") || strings.HasPrefix(rawPath, "https://") {
		return ""
	}

	// Check Typst root-relative `/...` BEFORE filepath.IsAbs (since Unix treats `/...` as OS-absolute).
	if strings.HasPrefix(rawPath, "/") {
		if rootDir != "" {
			cand := filepath.Clean(filepath.Join(rootDir, filepath.FromSlash(strings.TrimPrefix(rawPath, "/"))))
			if isExistingFile(cand) {
				return cand
			}
			if filepath.IsAbs(rawPath) && isExistingFile(rawPath) {
				return filepath.Clean(rawPath)
			}
			return cand
		}
		if filepath.IsAbs(rawPath) {
			return filepath.Clean(rawPath)
		}
		return ""
	}

	if filepath.IsAbs(rawPath) {
		return filepath.Clean(rawPath)
	}

	if baseDir != "" {
		cand1 := filepath.Clean(filepath.Join(baseDir, filepath.FromSlash(rawPath)))
		if isExistingFile(cand1) {
			return cand1
		}
		if rootDir != "" && rootDir != baseDir {
			cand2 := filepath.Clean(filepath.Join(rootDir, filepath.FromSlash(rawPath)))
			if isExistingFile(cand2) {
				return cand2
			}
		}
		return cand1
	}

	if rootDir != "" {
		return filepath.Clean(filepath.Join(rootDir, filepath.FromSlash(rawPath)))
	}
	return filepath.Clean(filepath.FromSlash(rawPath))
}

func readOpenOrDiskBytes(filePath string) []byte {
	canon := canonicalBibPath(filePath)
	if canon != "" {
		for _, ob := range buffer.OpenBuffers {
			if ob == nil || ob.Path == "" || ob.Type.Scratch {
				continue
			}
			if canonicalBibPath(bufferAbsPath(ob)) == canon {
				return ob.Bytes()
			}
		}
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	return data
}

func bufferAbsPath(b *buffer.Buffer) string {
	if b == nil || b.Path == "" {
		return ""
	}
	if b.AbsPath != "" {
		return filepath.Clean(b.AbsPath)
	}
	if abs, err := filepath.Abs(b.Path); err == nil && abs != "" {
		return filepath.Clean(abs)
	}
	return filepath.Clean(b.Path)
}

func canonicalBibPath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil || abs == "" {
		abs = filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && resolved != "" {
		abs = resolved
	}
	abs = filepath.Clean(abs)
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs
}

func isExistingFile(p string) bool {
	if p == "" {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
