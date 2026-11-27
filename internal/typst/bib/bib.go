package bib

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/util"
)

// Entry represents a normalized citation record from BibTeX or Hayagriva YAML.
type Entry struct {
	Key     string
	Type    string
	Title   string
	Authors string
	Year    string
	Source  string
}

// PickerItem describes one selectable entry passed to the bottom-docked picker.
type PickerItem struct {
	Badge     string
	Primary   string
	Secondary string
	OnSelect  func()
}

// PickerFunc opens a bottom-docked filterable picker UI without coupling bib to internal/lsp.
type PickerFunc func(bp *action.BufPane, title, badge, initialQuery string, items []PickerItem) bool

var (
	seamMu           sync.RWMutex
	pickerFn         PickerFunc
	mainFileResolver func(string) string
	rootResolver     func(string) string

	initOnce sync.Once
)

// SetPicker installs the UI picker callback (wired in features.Init()).
func SetPicker(fn PickerFunc) {
	seamMu.Lock()
	pickerFn = fn
	seamMu.Unlock()
}

func getPicker() PickerFunc {
	seamMu.RLock()
	fn := pickerFn
	seamMu.RUnlock()
	return fn
}

// SetMainFileResolver installs the optional pinned-main-file resolver callback.
func SetMainFileResolver(fn func(string) string) {
	seamMu.Lock()
	mainFileResolver = fn
	seamMu.Unlock()
}

func getMainFileResolver() func(string) string {
	seamMu.RLock()
	fn := mainFileResolver
	seamMu.RUnlock()
	return fn
}

// SetRootResolver installs the optional project-root resolver callback.
func SetRootResolver(fn func(string) string) {
	seamMu.Lock()
	rootResolver = fn
	seamMu.Unlock()
}

func getRootResolver() func(string) string {
	seamMu.RLock()
	fn := rootResolver
	seamMu.RUnlock()
	return fn
}

// Init registers bibliography actions, commands, and bindings once.
func Init() {
	initOnce.Do(RegisterAll)
}

// RegisterAll registers CitePicker (Alt-r) and `:cite [query]`.
// Intended to be called once from features.Init() (or Init()).
func RegisterAll() {
	action.RegisterAction("CitePicker", NewCitePickerAction())
	action.RegisterCommand("cite", NewCiteCmd(), CiteCompleter)
	action.RegisterDefaultBindingWithFallback("buffer", "Alt-r", "CitePicker")
}

// NewCitePickerAction returns the BufKeyAction bound to Alt-r.
// Returns false on non-Typst or read-only buffers so fallback bindings apply.
func NewCitePickerAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() || bp.Buf.Type.Readonly {
			return false
		}
		openCitePicker(bp, "")
		return true
	}
}

// NewCiteCmd returns the command handler for `:cite [query]`.
func NewCiteCmd() func(bp *action.BufPane, args []string) {
	return func(bp *action.BufPane, args []string) {
		if bp == nil || bp.Buf == nil {
			return
		}
		if bp.Buf.Type.Readonly {
			if action.InfoBar != nil {
				action.InfoBar.Error("Buffer is read-only")
			}
			return
		}
		if !bp.Buf.IsTypst() {
			if action.InfoBar != nil {
				action.InfoBar.Error("Command requires a Typst buffer")
			}
			return
		}
		initialQuery := strings.TrimSpace(strings.Join(args, " "))
		openCitePicker(bp, initialQuery)
	}
}

func openCitePicker(bp *action.BufPane, initialQuery string) bool {
	if bp == nil || bp.Buf == nil {
		return false
	}
	entries := EntriesForBuffer(bp.Buf)
	if len(entries) == 0 {
		if action.InfoBar != nil {
			action.InfoBar.Message("No bibliography entries found")
		}
		return true
	}

	items := make([]PickerItem, 0, len(entries))
	for _, entry := range entries {
		e := entry
		badge := e.Year
		if badge == "" {
			badge = "@"
		}
		primary := formatEntryPrimary(e)
		key := e.Key
		items = append(items, PickerItem{
			Badge:     badge,
			Primary:   primary,
			Secondary: "@" + key,
			OnSelect: func() {
				insertCitation(bp, key)
			},
		})
	}

	picker := getPicker()
	if picker == nil {
		if action.InfoBar != nil {
			action.InfoBar.Message(fmt.Sprintf("Citation picker unavailable (%d bibliography entries)", len(entries)))
		}
		return false
	}
	return picker(bp, "Cite", "CITE", initialQuery, items)
}

func formatEntryPrimary(e Entry) string {
	switch {
	case e.Authors != "" && e.Title != "":
		return e.Authors + " — " + e.Title
	case e.Title != "":
		return e.Title
	case e.Authors != "":
		return e.Authors
	case e.Type != "":
		return e.Type
	default:
		return e.Key
	}
}

func insertCitation(bp *action.BufPane, key string) {
	if bp == nil || bp.Buf == nil || bp.Buf.Type.Readonly || key == "" {
		return
	}
	c := bp.Cursor
	if c == nil {
		c = bp.Buf.GetActiveCursor()
	}
	if c == nil {
		return
	}
	if c.HasSelection() {
		c.DeleteSelection()
		c.ResetSelection()
	}
	pos := c.Loc
	lineLen := util.CharacterCount(bp.Buf.LineBytes(pos.Y))
	hasAt := pos.X > 0 && pos.X-1 < lineLen && bp.Buf.RuneAt(buffer.Loc{X: pos.X - 1, Y: pos.Y}) == '@'
	text := "@" + key
	if hasAt {
		text = key
	}
	bp.Buf.Insert(pos, text)
	if bp.BWindow != nil {
		bp.Relocate()
	}
}

// CiteCompleter implements buffer.Completer for `:cite [query]`, reading
// bibliography entries from the active editor buffer rather than InfoBar.
func CiteCompleter(b *buffer.Buffer) ([]string, []string) {
	if b == nil {
		return nil, nil
	}
	c := b.GetActiveCursor()
	if c == nil {
		return nil, nil
	}
	docBuf := activeDocumentBuffer()
	if docBuf == nil || !docBuf.IsTypst() {
		return nil, nil
	}

	input, argstart := b.GetArg()
	prefixChars := util.Max(0, c.X-argstart)
	return matchEntryKeys(EntriesForBuffer(docBuf), input, prefixChars)
}

func activeDocumentBuffer() *buffer.Buffer {
	if action.Tabs == nil {
		return nil
	}
	tab := action.MainTab()
	if tab == nil {
		return nil
	}
	cp := tab.CurPane()
	if cp == nil {
		return nil
	}
	return cp.Buf
}

// SuggestEntries returns `(completions, suggestions)` for offline `@prefix`
// citation completion via `b.ShowSuggestions` / `b.CycleAutocomplete`.
// `completions[i]` contains only the untyped suffix after `prefix`, while
// `suggestions[i]` contains the full citation key.
func SuggestEntries(b *buffer.Buffer, prefix string) ([]string, []string) {
	if b == nil || !b.IsTypst() {
		return nil, nil
	}
	entries := EntriesForBuffer(b)
	if len(entries) == 0 {
		return nil, nil
	}
	prefixChars := util.CharacterCountInString(prefix)
	return matchEntryKeys(entries, prefix, prefixChars)
}

func matchEntryKeys(entries []Entry, prefix string, prefixChars int) ([]string, []string) {
	if len(entries) == 0 {
		return nil, nil
	}
	lowerPrefix := strings.ToLower(prefix)
	var exact []string
	var folded []string
	for _, e := range entries {
		if e.Key == "" {
			continue
		}
		if prefix == "" || strings.HasPrefix(e.Key, prefix) {
			exact = append(exact, e.Key)
		} else if strings.HasPrefix(strings.ToLower(e.Key), lowerPrefix) {
			folded = append(folded, e.Key)
		}
	}
	sort.Strings(exact)
	sort.Strings(folded)
	suggestions := append(exact, folded...)
	if len(suggestions) == 0 {
		return nil, nil
	}
	completions := make([]string, len(suggestions))
	for i, key := range suggestions {
		completions[i] = util.SliceEndStr(key, prefixChars)
	}
	return completions, suggestions
}
