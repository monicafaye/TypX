package shortcuts

import (
	"strings"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/typst/conceal"
	"github.com/monicafaye/TypX/internal/util"
)

// NewSymbolPickerAction returns the BufKeyAction for Alt-y (:symbol picker).
func NewSymbolPickerAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		if !canEditTypst(bp) {
			return false
		}
		return openSymbolPicker(bp, "")
	}
}

// NewSymbolCmd returns the command handler for :symbol [query].
func NewSymbolCmd() func(bp *action.BufPane, args []string) {
	return func(bp *action.BufPane, args []string) {
		if !guardTypstCmd(bp) {
			return
		}
		query := strings.TrimSpace(strings.Join(args, " "))
		openSymbolPicker(bp, query)
	}
}

func openSymbolPicker(bp *action.BufPane, initialQuery string) bool {
	if !canEditTypst(bp) {
		return false
	}
	catalog := conceal.SymbolCatalog()
	if picker := getPicker(); picker != nil {
		items := make([]PickerItem, 0, len(catalog))
		for _, sym := range catalog {
			name := sym.Name
			items = append(items, PickerItem{
				Badge:     sym.Category,
				Primary:   name,
				Secondary: string(sym.Glyph),
				OnSelect: func() {
					target := resolveEditableTypstPane(bp)
					if target != nil {
						insertSymbolInPane(target, name)
					}
				},
			})
		}
		return picker(bp, "Math Symbols", "SYMBOLS", initialQuery, items)
	}

	if action.InfoBar == nil {
		return false
	}
	action.InfoBar.Prompt("Symbol: ", initialQuery, "Symbol", nil, func(resp string, canceled bool) {
		if canceled {
			return
		}
		q := strings.TrimSpace(resp)
		if q == "" {
			return
		}
		target := resolveEditableTypstPane(bp)
		if target == nil {
			return
		}
		sym, ok := findCatalogSymbol(catalog, q)
		if !ok {
			if action.InfoBar != nil {
				action.InfoBar.Error("Unknown Typst symbol: ", q)
			}
			return
		}
		insertSymbolInPane(target, sym.Name)
	})
	return true
}

func findCatalogSymbol(catalog []conceal.CatalogSymbol, query string) (conceal.CatalogSymbol, bool) {
	for _, sym := range catalog {
		if sym.Name == query {
			return sym, true
		}
	}
	for _, sym := range catalog {
		if strings.EqualFold(sym.Name, query) {
			return sym, true
		}
	}
	lq := strings.ToLower(query)
	for _, sym := range catalog {
		if string(sym.Glyph) == query || strings.Contains(strings.ToLower(sym.Name), lq) {
			return sym, true
		}
	}
	return conceal.CatalogSymbol{}, false
}

func insertSymbolInPane(bp *action.BufPane, name string) {
	if !canEditTypst(bp) || name == "" {
		return
	}
	forEachCursorReverse(bp, func(c *buffer.Cursor) {
		insertSymbolAtCursor(bp.Buf, c, name)
	})
}

func insertSymbolAtCursor(b *buffer.Buffer, c *buffer.Cursor, name string) {
	if b == nil || c == nil || name == "" {
		return
	}
	if c.HasSelection() {
		c.DeleteSelection()
		c.ResetSelection()
	}
	insertLoc := c.Loc
	mode := detectModeAtLoc(b, insertLoc)
	text := name
	if mode != ModeMath && mode != ModeRaw {
		text = "$" + name + "$"
	}
	startOff := locToCharOffset(b, insertLoc)
	b.Insert(insertLoc, text)
	c.GotoLoc(charOffsetToLoc(b, startOff+util.CharacterCountInString(text)))
}
