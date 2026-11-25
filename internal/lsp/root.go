package lsp

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	reParentImport  = regexp.MustCompile(`(?m)#(?:import|include)\s+"((?:\.\./)+)`)
	reRootRelImport = regexp.MustCompile(`(?m)#(?:import|include)\s+"/([^"\r\n]+)"`)
)

// initialRootURI derives the Typst workspace root URI from CLI file arguments
// before LoadInput runs so tinymist's initialize handshake gets the right root
// even when opening nested chapter files (e.g. courses/X/lectures/01-sets.typ).
func initialRootURI(args []string) string {
	var fallback string
	for _, a := range args {
		if a == "" || strings.HasPrefix(a, "+") {
			continue
		}
		if strings.EqualFold(filepath.Ext(a), ".typ") {
			if root := findProjectRoot(a); root != "" {
				return PathToURI(root)
			}
		}
		if fallback == "" {
			fallback = a
		}
	}
	if fallback != "" {
		if root := findProjectRoot(fallback); root != "" {
			return PathToURI(root)
		}
	}
	return ""
}

// findProjectRoot resolves the Typst project root for filePath so multi-file
// projects (e.g. chapters/01-sets.typ importing ../template.typ or
// /templates/course-note.typ) can compile and preview without root errors.
func findProjectRoot(filePath string) string {
	if filePath == "" {
		return ""
	}
	abs, err := filepath.Abs(filePath)
	if err != nil {
		abs = filepath.Clean(filePath)
	}
	dir := filepath.Dir(abs)

	// 1. Explicit Typst project manifest (typst.toml) walking upward.
	if root := findMarkerDir(dir, "typst.toml"); root != "" {
		return root
	}

	// 2. Root-relative imports (#import "/templates/course-note.typ") resolved
	// against the nearest ancestor directory that contains the target file.
	head := readFileHead(abs, 64*1024)
	if len(head) > 0 {
		if root := findRootFromRootRelativeImports(dir, head); root != "" {
			return root
		}
	}

	// 3. Nearest Git repository root (.git).
	if root := findMarkerDir(dir, ".git"); root != "" {
		return root
	}

	// 4. Relative parent imports (#import "../../template.typ") when no marker exists.
	if len(head) > 0 {
		if up := maxParentImportDepthBytes(head); up > 0 {
			cur := dir
			for i := 0; i < up; i++ {
				parent := filepath.Dir(cur)
				if parent == cur {
					break
				}
				cur = parent
			}
			return cur
		}
	}
	return dir
}

// findProjectRootDir walks upward from dir looking for a typst.toml or .git
// marker, returning dir if none is found.
func findProjectRootDir(dir string) string {
	if dir == "" {
		return ""
	}
	cleaned := filepath.Clean(dir)
	if root := findMarkerDir(cleaned, "typst.toml"); root != "" {
		return root
	}
	if root := findMarkerDir(cleaned, ".git"); root != "" {
		return root
	}
	return cleaned
}

func findMarkerDir(dir, marker string) string {
	cur := filepath.Clean(dir)
	for {
		if _, err := os.Stat(filepath.Join(cur, marker)); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return ""
}

func readFileHead(filePath string, limit int) []byte {
	f, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, _ := f.Read(buf)
	if n <= 0 {
		return nil
	}
	return buf[:n]
}

func findRootFromRootRelativeImports(dir string, content []byte) string {
	matches := reRootRelImport.FindAllSubmatch(content, -1)
	if len(matches) == 0 {
		return ""
	}
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		rel := filepath.FromSlash(string(m[1]))
		cur := filepath.Clean(dir)
		for {
			if _, err := os.Stat(filepath.Join(cur, rel)); err == nil {
				return cur
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
	}
	return ""
}

func maxParentImportDepthBytes(content []byte) int {
	matches := reParentImport.FindAllSubmatch(content, -1)
	maxUp := 0
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		up := strings.Count(string(m[1]), "../")
		if up > maxUp {
			maxUp = up
		}
	}
	return maxUp
}
