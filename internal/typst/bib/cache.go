package bib

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/monicafaye/TypX/internal/buffer"
)

const maxCachedBibFiles = 256

type cachedBibFile struct {
	modTime time.Time
	size    int64
	entries []Entry
}

var (
	cacheMu   sync.RWMutex
	fileCache = make(map[string]cachedBibFile)
)

// ClearCache clears all cached parsed bibliography files.
func ClearCache() {
	cacheMu.Lock()
	fileCache = make(map[string]cachedBibFile)
	cacheMu.Unlock()
}

// EntriesForFile parses and caches bibliography entries from a single .bib,
// .yml, or .yaml file, invalidating automatically when mtime or size changes.
func EntriesForFile(path string) []Entry {
	entries := loadBibFile(path)
	if len(entries) == 0 {
		return nil
	}
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out
}

// EntriesForBuffer resolves all bibliography files referenced by b (or fallback
// *.bib files in the project root), loads them through the cache, and returns
// merged entries deduplicated by Key in discovery order.
func EntriesForBuffer(b *buffer.Buffer) []Entry {
	files := ResolveBibliographyFiles(b)
	if len(files) == 0 {
		return nil
	}
	var merged []Entry
	seen := make(map[string]bool)
	for _, f := range files {
		for _, e := range loadBibFile(f) {
			if e.Key == "" || seen[e.Key] {
				continue
			}
			seen[e.Key] = true
			merged = append(merged, e)
		}
	}
	return merged
}

func loadBibFile(path string) []Entry {
	if path == "" {
		return nil
	}
	clean := filepath.Clean(path)
	info, err := os.Stat(clean)
	if err != nil || info.IsDir() {
		cacheMu.Lock()
		delete(fileCache, clean)
		cacheMu.Unlock()
		return nil
	}

	modTime := info.ModTime()
	size := info.Size()

	cacheMu.RLock()
	if cf, ok := fileCache[clean]; ok && cf.modTime.Equal(modTime) && cf.size == size {
		entries := cf.entries
		cacheMu.RUnlock()
		return entries
	}
	cacheMu.RUnlock()

	data, err := os.ReadFile(clean)
	if err != nil {
		cacheMu.Lock()
		delete(fileCache, clean)
		cacheMu.Unlock()
		return nil
	}

	var parsed []Entry
	switch strings.ToLower(filepath.Ext(clean)) {
	case ".bib":
		parsed = ParseBibTeX(data)
	case ".yml", ".yaml":
		parsed = ParseHayagriva(data)
	default:
		return nil
	}

	for i := range parsed {
		parsed[i].Source = clean
	}

	cacheMu.Lock()
	if len(fileCache) >= maxCachedBibFiles {
		fileCache = make(map[string]cachedBibFile)
	}
	fileCache[clean] = cachedBibFile{
		modTime: modTime,
		size:    size,
		entries: parsed,
	}
	cacheMu.Unlock()

	return parsed
}
