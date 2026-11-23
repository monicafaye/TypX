package shortcuts

import (
	"sort"
	"strings"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/util"
)

var typstTemplates = map[string]string{
	"figure":  "#figure(\n  $0,\n  caption: [],\n) <fig:>",
	"table":   "#table(\n  columns: 2,\n  [$0], [],\n)",
	"frac":    "frac($0, )",
	"matrix":  "mat($0, ; , )",
	"cases":   "cases($0, )",
	"code":    "```\n$0\n```",
	"callout": "#block(\n  fill: luma(240),\n  inset: 8pt,\n  radius: 4pt,\n  width: 100%,\n)[$0]",
	"vec":     "vec($0, )",
	"hat":     "hat($0)",
	"bar":     "bar($0)",
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
	ws := leadingWhitespaceBeforeLoc(b, insertLoc)
	indentUnit := safeIndentString(b)

	rawLines := strings.Split(tmpl, "\n")
	outLines := make([]string, len(rawLines))
	for i, line := range rawLines {
		sp := 0
		for sp < len(line) && line[sp] == ' ' {
			sp++
		}
		levels := sp / 2
		rem := sp % 2
		reindented := strings.Repeat(indentUnit, levels) + strings.Repeat(" ", rem) + line[sp:]
		if i > 0 {
			reindented = ws + reindented
		}
		outLines[i] = reindented
	}

	relY := -1
	charCol := 0
	for i, line := range outLines {
		if idx := strings.Index(line, "$0"); idx >= 0 {
			relY = i
			charCol = util.CharacterCountInString(line[:idx])
			outLines[i] = line[:idx] + line[idx+2:]
			break
		}
	}

	rendered := strings.Join(outLines, "\n")
	b.Insert(insertLoc, rendered)

	var targetLoc buffer.Loc
	if relY >= 0 {
		if relY == 0 {
			targetLoc = buffer.Loc{X: insertLoc.X + charCol, Y: insertLoc.Y}
		} else {
			targetLoc = buffer.Loc{X: charCol, Y: insertLoc.Y + relY}
		}
	} else if len(outLines) == 1 {
		targetLoc = buffer.Loc{X: insertLoc.X + util.CharacterCountInString(outLines[0]), Y: insertLoc.Y}
	} else {
		last := outLines[len(outLines)-1]
		targetLoc = buffer.Loc{X: util.CharacterCountInString(last), Y: insertLoc.Y + len(outLines) - 1}
	}
	c.GotoLoc(targetLoc)
}
