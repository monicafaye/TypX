package outline

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/typst"
)

type outlineSegment struct {
	filePath string
	canonKey string
	items    []Item
}

func canonicalOutlinePath(p string) string {
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

func bufferAbsPath(b *buffer.Buffer) string {
	if b == nil || b.Path == "" {
		return ""
	}
	if b.AbsPath != "" {
		return b.AbsPath
	}
	if abs, err := filepath.Abs(b.Path); err == nil && abs != "" {
		return abs
	}
	return b.Path
}

func fileReadable(p string, activeBuf *buffer.Buffer) bool {
	if p == "" {
		return false
	}
	canon := canonicalOutlinePath(p)
	if canon != "" {
		if activeBuf != nil && activeBuf.Path != "" && canonicalOutlinePath(bufferAbsPath(activeBuf)) == canon {
			return true
		}
		for _, ob := range buffer.OpenBuffers {
			if ob == nil || ob.Path == "" || ob.Type.Scratch {
				continue
			}
			if canonicalOutlinePath(bufferAbsPath(ob)) == canon {
				return true
			}
		}
	}
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func resolveIncludePath(currentFile, rootDir, inc string) string {
	inc = strings.TrimSpace(inc)
	if inc == "" || strings.HasPrefix(inc, "@") {
		return ""
	}
	if strings.HasPrefix(inc, "/") {
		base := rootDir
		if base == "" && currentFile != "" {
			base = filepath.Dir(currentFile)
		}
		if base == "" {
			return ""
		}
		return filepath.Clean(filepath.Join(base, filepath.FromSlash(strings.TrimPrefix(inc, "/"))))
	}
	if currentFile == "" {
		if rootDir == "" {
			return ""
		}
		return filepath.Clean(filepath.Join(rootDir, filepath.FromSlash(inc)))
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), filepath.FromSlash(inc)))
}

func formatHeaderFilePath(filePath, entryPath, rootDir string) string {
	if filePath == "" {
		return ""
	}
	baseDir := rootDir
	if baseDir == "" && entryPath != "" {
		baseDir = filepath.Dir(entryPath)
	}
	if baseDir != "" {
		if rel, err := filepath.Rel(baseDir, filePath); err == nil && rel != "" && rel != "." {
			slashed := filepath.ToSlash(rel)
			if !strings.HasPrefix(slashed, "../../..") {
				return slashed
			}
		}
		if baseResolved, err1 := filepath.EvalSymlinks(baseDir); err1 == nil {
			if fileResolved, err2 := filepath.EvalSymlinks(filePath); err2 == nil {
				if rel, err := filepath.Rel(baseResolved, fileResolved); err == nil && rel != "" && rel != "." {
					slashed := filepath.ToSlash(rel)
					if !strings.HasPrefix(slashed, "../../..") {
						return slashed
					}
				}
			}
		}
	}
	return filepath.Base(filePath)
}

func snapshotOpenBuffers(activeBuf *buffer.Buffer, activeSnap typst.LineSource) map[string]typst.LineSource {
	snaps := make(map[string]typst.LineSource)
	if activeBuf != nil && activeBuf.Path != "" {
		if canon := canonicalOutlinePath(bufferAbsPath(activeBuf)); canon != "" {
			if activeSnap != nil {
				snaps[canon] = activeSnap
			} else {
				snaps[canon] = snapshotLines(activeBuf)
			}
		}
	}
	for _, ob := range buffer.OpenBuffers {
		if ob == nil || ob.Path == "" || ob.Type.Scratch {
			continue
		}
		canon := canonicalOutlinePath(bufferAbsPath(ob))
		if canon == "" {
			continue
		}
		if _, exists := snaps[canon]; !exists {
			snaps[canon] = snapshotLines(ob)
		}
	}
	return snaps
}

func readDiskSource(filePath string) (typst.LineSource, bool) {
	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		return nil, false
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, false
	}
	rawLines := bytes.Split(data, []byte{'\n'})
	lines := make([][]byte, len(rawLines))
	for i, l := range rawLines {
		lines[i] = bytes.TrimSuffix(l, []byte{'\r'})
	}
	return typst.SliceSource(lines), true
}

func openSourceForPath(filePath, canonKey string, activeBuf *buffer.Buffer, activeParser *IncrementalParser) (typst.LineSource, *IncrementalParser, bool) {
	if activeBuf != nil && activeBuf.Path != "" && canonicalOutlinePath(bufferAbsPath(activeBuf)) == canonKey {
		return activeBuf, activeParser, true
	}
	for _, ob := range buffer.OpenBuffers {
		if ob == nil || ob.Path == "" || ob.Type.Scratch {
			continue
		}
		if canonicalOutlinePath(bufferAbsPath(ob)) == canonKey {
			return ob, nil, true
		}
	}
	src, ok := readDiskSource(filePath)
	return src, nil, ok
}

func openSourceFromSnapshots(
	filePath, canonKey, activeCanon string,
	activeSnap typst.LineSource,
	activeParser *IncrementalParser,
	openSnaps map[string]typst.LineSource,
) (typst.LineSource, *IncrementalParser, bool) {
	if activeCanon != "" && canonKey == activeCanon && activeSnap != nil {
		return activeSnap, activeParser, true
	}
	if snap, ok := openSnaps[canonKey]; ok && snap != nil {
		return snap, nil, true
	}
	src, ok := readDiskSource(filePath)
	return src, nil, ok
}

func itemBeforeInclude(it Item, inc includeRef) bool {
	if it.Line != inc.line {
		return it.Line < inc.line
	}
	return it.Column < inc.col
}

func stampItemPaths(items []Item, filePath, canonKey string) {
	for i := range items {
		items[i].FilePath = filePath
		items[i].canonPath = canonKey
	}
}

// walkProjectOutline recursively walks entryPath and its statically resolved
// #include targets in document order with cycle guarding, preferring open
// in-memory buffers over disk files.
// Returns (items, isMultiFile, visitedCanonFiles).
func walkProjectOutline(
	entryPath string,
	activeBuf *buffer.Buffer,
	activeParser *IncrementalParser,
	opts ParseOptions,
	rootResolver func(string) string,
) ([]Item, bool, map[string]bool) {
	if entryPath == "" {
		return nil, false, nil
	}

	rootDir := ""
	if rootResolver != nil {
		rootDir = rootResolver(entryPath)
	}
	if rootDir == "" {
		rootDir = filepath.Dir(entryPath)
	}

	return walkProjectOutlineWithLookup(entryPath, rootDir, opts, func(filePath, canonKey string) (typst.LineSource, *IncrementalParser, bool) {
		return openSourceForPath(filePath, canonKey, activeBuf, activeParser)
	})
}

// walkProjectOutlineFromSnapshots walks entryPath and its #include targets using
// pre-captured buffer snapshots and disk reads, safe to execute off the main UI goroutine.
func walkProjectOutlineFromSnapshots(
	entryPath, rootDir, activeCanon string,
	activeSnap typst.LineSource,
	activeParser *IncrementalParser,
	openSnaps map[string]typst.LineSource,
	opts ParseOptions,
) ([]Item, bool, map[string]bool) {
	if entryPath == "" {
		return nil, false, nil
	}
	if rootDir == "" {
		rootDir = filepath.Dir(entryPath)
	}

	return walkProjectOutlineWithLookup(entryPath, rootDir, opts, func(filePath, canonKey string) (typst.LineSource, *IncrementalParser, bool) {
		return openSourceFromSnapshots(filePath, canonKey, activeCanon, activeSnap, activeParser, openSnaps)
	})
}

func walkProjectOutlineWithLookup(
	entryPath, rootDir string,
	opts ParseOptions,
	lookup func(filePath, canonKey string) (typst.LineSource, *IncrementalParser, bool),
) ([]Item, bool, map[string]bool) {
	visited := make(map[string]bool)
	var segments []outlineSegment

	var walkFile func(filePath string)
	walkFile = func(filePath string) {
		canonKey := canonicalOutlinePath(filePath)
		if canonKey == "" || visited[canonKey] {
			return
		}
		src, reusedParser, ok := lookup(filePath, canonKey)
		if !ok || src == nil {
			return
		}
		visited[canonKey] = true

		var items []Item
		var includes []includeRef
		if reusedParser != nil && reusedParser.valid {
			items = reusedParser.assemble(opts)
			includes = reusedParser.includeRefs()
		} else {
			var p IncrementalParser
			items = p.Update(src, 0, opts)
			includes = p.includeRefs()
		}
		stampItemPaths(items, filePath, canonKey)

		ii := 0
		for _, inc := range includes {
			start := ii
			for ii < len(items) && itemBeforeInclude(items[ii], inc) {
				ii++
			}
			if ii > start {
				segItems := make([]Item, ii-start)
				copy(segItems, items[start:ii])
				segments = append(segments, outlineSegment{
					filePath: filePath,
					canonKey: canonKey,
					items:    segItems,
				})
			}

			childPath := resolveIncludePath(filePath, rootDir, inc.path)
			if childPath != "" {
				walkFile(childPath)
			}
		}

		if ii < len(items) {
			segItems := make([]Item, len(items)-ii)
			copy(segItems, items[ii:])
			segments = append(segments, outlineSegment{
				filePath: filePath,
				canonKey: canonKey,
				items:    segItems,
			})
		}
	}

	walkFile(entryPath)

	distinctFiles := len(visited)
	if distinctFiles <= 1 {
		var flat []Item
		for _, seg := range segments {
			flat = append(flat, seg.items...)
		}
		return flat, false, visited
	}

	var out []Item
	lastCanon := ""
	for _, seg := range segments {
		if len(seg.items) == 0 {
			continue
		}
		if seg.canonKey != lastCanon {
			out = append(out, Item{
				Kind:      KindFile,
				Level:     0,
				Title:     formatHeaderFilePath(seg.filePath, entryPath, rootDir),
				FilePath:  seg.filePath,
				canonPath: seg.canonKey,
			})
			lastCanon = seg.canonKey
		}
		out = append(out, seg.items...)
	}
	return out, true, visited
}
