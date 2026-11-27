package shortcuts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
)

// PickerItem describes a single selectable item for a bottom-docked picker UI.
type PickerItem struct {
	Badge     string
	Primary   string
	Secondary string
	OnSelect  func()
}

// PickerFunc opens a bottom-docked picker UI and returns true if displayed.
type PickerFunc func(bp *action.BufPane, title, badge, initialQuery string, items []PickerItem) bool

var (
	pickerMu       sync.RWMutex
	pickerFn       PickerFunc
	rootResolverMu sync.RWMutex
	rootResolverFn func(string) string
)

// SetPicker registers the bottom-docked picker callback used by :insert and :symbol.
func SetPicker(fn PickerFunc) {
	pickerMu.Lock()
	pickerFn = fn
	pickerMu.Unlock()
}

func getPicker() PickerFunc {
	pickerMu.RLock()
	fn := pickerFn
	pickerMu.RUnlock()
	return fn
}

// SetRootResolver registers a callback mapping a file path to its Typst project root.
func SetRootResolver(fn func(string) string) {
	rootResolverMu.Lock()
	rootResolverFn = fn
	rootResolverMu.Unlock()
}

func getRootResolver() func(string) string {
	rootResolverMu.RLock()
	fn := rootResolverFn
	rootResolverMu.RUnlock()
	return fn
}

// SnippetEntry describes a resolved snippet from built-in, user, or project layers.
type SnippetEntry struct {
	Name        string
	Body        string
	Description string
	Source      string // "built-in", "user", "project"
}

func resolveSnippetDocBuffer(b *buffer.Buffer) *buffer.Buffer {
	if b != nil && b.Path != "" && b.Type.Kind != buffer.BTInfo.Kind && !b.Type.Scratch {
		return b
	}
	if action.Tabs != nil && len(action.Tabs.List) > 0 {
		if tab := action.MainTab(); tab != nil {
			if cur := tab.CurPane(); cur != nil && cur.Buf != nil &&
				cur.Buf.Path != "" && cur.Buf.Type.Kind != buffer.BTInfo.Kind && !cur.Buf.Type.Scratch {
				return cur.Buf
			}
		}
	}
	return nil
}

func resolveSnippetProjectRoot(b *buffer.Buffer) string {
	doc := resolveSnippetDocBuffer(b)
	if doc == nil {
		return ""
	}
	filePath := doc.AbsPath
	if filePath == "" {
		filePath = doc.Path
	}
	if filePath == "" {
		return ""
	}
	rr := getRootResolver()
	if rr == nil {
		return ""
	}
	return rr(filePath)
}

func loadSnippetFile(path, source string) ([]SnippetEntry, []string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("%s: %v", path, err)}
	}

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return nil, []string{fmt.Sprintf("invalid snippets JSON in %s: %v", path, err)}
	}
	if rawMap == nil {
		return nil, []string{fmt.Sprintf("invalid snippets JSON in %s: expected object", path)}
	}

	keys := make([]string, 0, len(rawMap))
	for k := range rawMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var entries []SnippetEntry
	var warnings []string
	for _, name := range keys {
		if strings.TrimSpace(name) == "" {
			warnings = append(warnings, fmt.Sprintf("empty snippet name in %s", path))
			continue
		}
		raw := rawMap[name]

		var bodyPtr *string
		if err := json.Unmarshal(raw, &bodyPtr); err == nil && bodyPtr != nil {
			entries = append(entries, SnippetEntry{
				Name:   name,
				Body:   *bodyPtr,
				Source: source,
			})
			continue
		}

		var obj struct {
			Body        *string `json:"body"`
			Description string  `json:"description"`
		}
		if err := json.Unmarshal(raw, &obj); err == nil && obj.Body != nil {
			entries = append(entries, SnippetEntry{
				Name:        name,
				Body:        *obj.Body,
				Description: obj.Description,
				Source:      source,
			})
			continue
		}

		warnings = append(warnings, fmt.Sprintf("invalid snippet %q in %s", name, path))
	}
	return entries, warnings
}

func loadSnippetsForBuffer(b *buffer.Buffer, warn bool) []SnippetEntry {
	merged := make(map[string]SnippetEntry, len(typstTemplates))
	for name, body := range typstTemplates {
		merged[name] = SnippetEntry{
			Name:   name,
			Body:   body,
			Source: "built-in",
		}
	}

	var warnings []string
	if config.ConfigDir != "" {
		userPath := filepath.Join(config.ConfigDir, "snippets.json")
		entries, warns := loadSnippetFile(userPath, "user")
		warnings = append(warnings, warns...)
		for _, e := range entries {
			merged[e.Name] = e
		}
	}

	if root := resolveSnippetProjectRoot(b); root != "" {
		projPath := filepath.Join(root, ".typx", "snippets.json")
		entries, warns := loadSnippetFile(projPath, "project")
		warnings = append(warnings, warns...)
		for _, e := range entries {
			merged[e.Name] = e
		}
	}

	if warn && len(warnings) > 0 && action.InfoBar != nil {
		action.InfoBar.Error(strings.Join(warnings, "; "))
	}

	out := make([]SnippetEntry, 0, len(merged))
	for _, e := range merged {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func lookupSnippetForBuffer(b *buffer.Buffer, name string, warn bool) (SnippetEntry, bool) {
	for _, e := range loadSnippetsForBuffer(b, warn) {
		if e.Name == name {
			return e, true
		}
	}
	return SnippetEntry{}, false
}

func singleLinePreview(s string, maxRunes int) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	collapsed := strings.Join(strings.Fields(s), " ")
	if maxRunes <= 0 {
		return collapsed
	}
	rs := []rune(collapsed)
	if len(rs) <= maxRunes {
		return collapsed
	}
	if maxRunes == 1 {
		return "…"
	}
	return string(rs[:maxRunes-1]) + "…"
}

func resolveEditableTypstPane(fallback *action.BufPane) *action.BufPane {
	if action.Tabs != nil && len(action.Tabs.List) > 0 {
		if tab := action.MainTab(); tab != nil {
			if cur := tab.CurPane(); canEditTypst(cur) {
				return cur
			}
		}
	}
	if canEditTypst(fallback) {
		return fallback
	}
	return nil
}

func expandSnippetInPane(bp *action.BufPane, body string) {
	if !canEditTypst(bp) {
		return
	}
	if len(bp.Buf.GetCursors()) <= 1 {
		ExpandSnippetAt(bp, body)
		return
	}
	forEachCursorReverse(bp, func(c *buffer.Cursor) {
		expandTemplateAt(bp.Buf, c, body)
	})
}
