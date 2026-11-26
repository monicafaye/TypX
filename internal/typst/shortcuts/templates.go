package shortcuts

import (
	"sort"
	"strings"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/util"
)

var typstTemplates = map[string]string{
	"figure":  "#figure(\n  $1,\n  caption: [$2],\n) <fig:$3>$0",
	"table":   "#table(\n  columns: 2,\n  [$1], [$2],\n)$0",
	"frac":    "frac($1, $2)$0",
	"matrix":  "mat($1, $2; $3, $4)$0",
	"cases":   "cases($1, $2)$0",
	"code":    "```\n$1\n```$0",
	"callout": "#block(\n  fill: luma(240),\n  inset: 8pt,\n  radius: 4pt,\n  width: 100%,\n)[$1]$0",
	"vec":     "vec($1, $2)$0",
	"hat":     "hat($1)$0",
	"bar":     "bar($1)$0",
}

var sortedTemplateNames = func() []string {
	names := make([]string, 0, len(typstTemplates))
	for k := range typstTemplates {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}()

// TemplateNames returns a sorted copy of all built-in Typst snippet names.
func TemplateNames() []string {
	out := make([]string, len(sortedTemplateNames))
	copy(out, sortedTemplateNames)
	return out
}

// TemplateBody returns the raw $0 template body for name, if registered.
func TemplateBody(name string) (string, bool) {
	body, ok := typstTemplates[name]
	return body, ok
}

// InsertCompleter implements buffer.Completer for :insert template names.
func InsertCompleter(b *buffer.Buffer) ([]string, []string) {
	if b == nil {
		return nil, nil
	}
	c := b.GetActiveCursor()
	if c == nil {
		return nil, nil
	}
	input, argstart := b.GetArg()

	var suggestions []string
	for _, name := range sortedTemplateNames {
		if strings.HasPrefix(name, input) {
			suggestions = append(suggestions, name)
		}
	}
	if len(suggestions) == 0 {
		return nil, nil
	}

	prefixChars := util.Max(0, c.X-argstart)
	completions := make([]string, len(suggestions))
	for i, s := range suggestions {
		completions[i] = util.SliceEndStr(s, prefixChars)
	}
	return completions, suggestions
}

// NewInsertCmd returns the command handler for :insert [template].
func NewInsertCmd() func(bp *action.BufPane, args []string) {
	return func(bp *action.BufPane, args []string) {
		if !guardTypstCmd(bp) {
			return
		}
		if len(args) == 0 {
			openInsertPalette(bp)
			return
		}
		name := args[0]
		tmpl, ok := typstTemplates[name]
		if !ok {
			if action.InfoBar != nil {
				action.InfoBar.Error("Unknown Typst template: ", name)
			}
			return
		}
		if len(bp.Buf.GetCursors()) <= 1 {
			ExpandSnippetAt(bp, tmpl)
			return
		}
		forEachCursorReverse(bp, func(c *buffer.Cursor) {
			expandTemplateAt(bp.Buf, c, tmpl)
		})
	}
}

// NewInsertTemplateCmd returns the command handler for :insert-<name>.
func NewInsertTemplateCmd(name string) func(bp *action.BufPane, args []string) {
	return func(bp *action.BufPane, args []string) {
		if !guardTypstCmd(bp) {
			return
		}
		tmpl, ok := typstTemplates[name]
		if !ok {
			if action.InfoBar != nil {
				action.InfoBar.Error("Unknown Typst template: ", name)
			}
			return
		}
		if len(bp.Buf.GetCursors()) <= 1 {
			ExpandSnippetAt(bp, tmpl)
			return
		}
		forEachCursorReverse(bp, func(c *buffer.Cursor) {
			expandTemplateAt(bp.Buf, c, tmpl)
		})
	}
}

// NewInsertPaletteAction returns the BufKeyAction for Alt-i (:insert palette).
func NewInsertPaletteAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		if !canEditTypst(bp) {
			return false
		}
		return openInsertPalette(bp)
	}
}

func openInsertPalette(bp *action.BufPane) bool {
	if action.InfoBar == nil {
		return false
	}
	action.InfoBar.Prompt("> ", "insert ", "Command", nil, func(resp string, canceled bool) {
		if canceled {
			return
		}
		resp = strings.TrimSpace(resp)
		if resp == "" {
			return
		}
		target := bp
		if tab := action.MainTab(); tab != nil {
			if cur := tab.CurPane(); cur != nil {
				target = cur
			}
		}
		if !canEditTypst(target) {
			return
		}
		if tmpl, ok := typstTemplates[resp]; ok {
			if len(target.Buf.GetCursors()) <= 1 {
				ExpandSnippetAt(target, tmpl)
				return
			}
			forEachCursorReverse(target, func(c *buffer.Cursor) {
				expandTemplateAt(target.Buf, c, tmpl)
			})
			return
		}
		target.HandleCommand(resp)
	})
	return true
}

func expandTemplateAt(b *buffer.Buffer, c *buffer.Cursor, tmpl string) {
	if c.HasSelection() {
		c.DeleteSelection()
		c.ResetSelection()
	}

	insertLoc := c.Loc
	reindented := reindentSnippet(b, insertLoc, tmpl)
	parsed := ParseSnippet(reindented)
	startOff := locToCharOffset(b, insertLoc)
	b.Insert(insertLoc, parsed.Text)

	if len(parsed.Stops) > 0 {
		c.GotoLoc(charOffsetToLoc(b, startOff+parsed.Stops[0].StartChar))
	} else {
		c.GotoLoc(charOffsetToLoc(b, startOff+util.CharacterCountInString(parsed.Text)))
	}
}
