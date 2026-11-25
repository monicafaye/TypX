package features

import (
	"fmt"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/display"
	"github.com/monicafaye/TypX/internal/lsp"
	"github.com/monicafaye/TypX/internal/typst"
	"github.com/monicafaye/TypX/internal/typst/outline"
	"github.com/monicafaye/TypX/internal/typst/preview"
	"github.com/monicafaye/TypX/internal/typst/templates"
)

func buildBottomBarContext() display.BottomBarContext {
	if action.Tabs == nil || len(action.Tabs.List) == 0 {
		return genericBottomBarContext(nil, false)
	}
	tab := action.MainTab()
	if tab == nil {
		return genericBottomBarContext(nil, false)
	}

	// 1. Focused auxiliary panes get dedicated navigation hints.
	if activePane := tab.ActivePane(); activePane != nil {
		switch p := activePane.(type) {
		case *outline.OutlinePane:
			return outlineBottomBarContext(p)
		case *preview.PreviewPane:
			return previewBottomBarContext(p)
		case *templates.TemplatePane:
			return templatesBottomBarContext(p)
		}
	}

	bp := tab.CurPane()
	if bp == nil || bp.Buf == nil {
		return genericBottomBarContext(tab, false)
	}

	if bp.Buf.Type.Kind == buffer.BTHelp.Kind {
		return helpBottomBarContext()
	}

	if isFileManagerBuffer(bp.Buf) {
		return fileManagerBottomBarContext(tab)
	}

	if bp.Buf.IsTypst() {
		return typstBottomBarContext(tab, bp)
	}

	return genericBottomBarContext(tab, bp.Buf.Modified())
}

func isFileManagerBuffer(b *buffer.Buffer) bool {
	if b == nil {
		return false
	}
	if ft, ok := b.Settings["filetype"].(string); ok && ft == "filemanager" {
		return true
	}
	return b.Path == "filemanager" && b.Type.Scratch && b.Type.Readonly
}

func hasFileManagerPane(tab *action.Tab) bool {
	if tab == nil {
		return false
	}
	for _, p := range tab.Panes {
		if bp, ok := p.(*action.BufPane); ok && bp != nil && isFileManagerBuffer(bp.Buf) {
			return true
		}
	}
	return false
}

func typstBottomBarContext(tab *action.Tab, bp *action.BufPane) display.BottomBarContext {
	b := bp.Buf
	mode := detectBufferMode(b)

	previewOn := preview.PaneForTab(tab) != nil || lsp.IsPreviewActive()
	outlineOn := outline.PaneForTab(tab) != nil
	treeOn := hasFileManagerPane(tab)
	templatesOn := templates.PaneForTab(tab) != nil
	concealOn, _ := b.Settings["typst.conceal"].(bool)
	isEmpty := templates.IsBufferBlank(b)

	errCount, warnCount := countDiagnostics(b)
	menuRows := typstKeyMenuRows(previewOn, outlineOn, concealOn, treeOn, templatesOn)
	menuTip := "Click any shortcut or press Alt+G to hide · ^E commands (:help typst)"

	var diagItem *display.BottomBarItem
	if errCount > 0 {
		label := "Error"
		if errCount != 1 {
			label = "Errors"
		}
		diagItem = &display.BottomBarItem{
			Key:      fmt.Sprintf("✖ %d", errCount),
			Label:    label,
			Action:   "command:diagnostics",
			Priority: 0,
			Accent:   "err",
		}
	} else if warnCount > 0 {
		label := "Warn"
		diagItem = &display.BottomBarItem{
			Key:      fmt.Sprintf("▲ %d", warnCount),
			Label:    label,
			Action:   "command:diagnostics",
			Priority: 1,
			Accent:   "warn",
		}
	}

	var badge, badgeAccent string
	var hints []display.BottomBarItem

	switch mode {
	case typst.ModeMath:
		badge = "MATH"
		badgeAccent = "math"
		if diagItem != nil {
			hints = append(hints, *diagItem)
		}
		hints = append(hints,
			display.BottomBarItem{Key: "Alt+I", Label: "Snippets", Action: "InsertPalette", Priority: 0},
			display.BottomBarItem{Key: "^Space", Label: "Complete", Action: "LspComplete", Priority: 1},
			display.BottomBarItem{Key: "Alt+M", Label: "Exit Math", Action: "MathInline", Priority: 1},
			display.BottomBarItem{Key: "Alt+C", Label: "Conceal", Action: "ToggleConceal", Priority: 2, Active: concealOn},
			display.BottomBarItem{Key: "Alt+P", Label: "Preview", Action: "TogglePreview", Priority: 2, Active: previewOn},
			display.BottomBarItem{Key: "Alt+O", Label: "Outline", Action: "ToggleOutline", Priority: 3, Active: outlineOn},
			display.BottomBarItem{Key: "Alt+T", Label: "Files", Action: "command:tree", Priority: 4, Active: treeOn},
			display.BottomBarItem{Key: "^S", Label: "Save", Action: "Save", Priority: 3},
			display.BottomBarItem{Key: "^E", Label: "Cmd", Action: "CommandMode", Priority: 4},
			display.BottomBarItem{Key: "Alt+G", Label: "Keys", Action: "ToggleKeyMenu", Priority: 0, AlignRight: true},
			display.BottomBarItem{Key: "^Q", Label: "Quit", Action: "Quit", Priority: 1, AlignRight: true},
		)

	case typst.ModeCode:
		badge = "CODE"
		badgeAccent = "code"
		if diagItem != nil {
			hints = append(hints, *diagItem)
		}
		hints = append(hints,
			display.BottomBarItem{Key: "^Space", Label: "Complete", Action: "LspComplete", Priority: 0},
			display.BottomBarItem{Key: "Alt+K", Label: "Hover", Action: "LspHover", Priority: 1},
			display.BottomBarItem{Key: "Alt+I", Label: "Insert", Action: "InsertPalette", Priority: 2},
			display.BottomBarItem{Key: "Alt+P", Label: "Preview", Action: "TogglePreview", Priority: 1, Active: previewOn},
			display.BottomBarItem{Key: "Alt+O", Label: "Outline", Action: "ToggleOutline", Priority: 2, Active: outlineOn},
			display.BottomBarItem{Key: "Alt+T", Label: "Files", Action: "command:tree", Priority: 3, Active: treeOn},
			display.BottomBarItem{Key: "^S", Label: "Save", Action: "Save", Priority: 2},
			display.BottomBarItem{Key: "^E", Label: "Cmd", Action: "CommandMode", Priority: 3},
			display.BottomBarItem{Key: "Alt+G", Label: "Keys", Action: "ToggleKeyMenu", Priority: 0, AlignRight: true},
			display.BottomBarItem{Key: "^Q", Label: "Quit", Action: "Quit", Priority: 1, AlignRight: true},
		)

	default:
		if mode == typst.ModeRaw {
			badge = "RAW"
			badgeAccent = "raw"
		}
		if diagItem != nil {
			hints = append(hints, *diagItem)
		}
		if isEmpty {
			hints = append(hints,
				display.BottomBarItem{Key: "Alt+⇧N", Label: "Templates", Action: "ToggleTemplates", Priority: 0, Active: templatesOn},
			)
		}
		hints = append(hints,
			display.BottomBarItem{Key: "^S", Label: "Save", Action: "Save", Priority: 0},
			display.BottomBarItem{Key: "^F", Label: "Find", Action: "Find", Priority: 4},
			display.BottomBarItem{Key: "^E", Label: "Cmd", Action: "CommandMode", Priority: 1},
			display.BottomBarItem{Key: "Alt+P", Label: "Preview", Action: "TogglePreview", Priority: 0, Active: previewOn},
			display.BottomBarItem{Key: "Alt+O", Label: "Outline", Action: "ToggleOutline", Priority: 1, Active: outlineOn},
			display.BottomBarItem{Key: "Alt+T", Label: "Files", Action: "command:tree", Priority: 1, Active: treeOn},
			display.BottomBarItem{Key: "Alt+C", Label: "Conceal", Action: "ToggleConceal", Priority: 2, Active: concealOn},
			display.BottomBarItem{Key: "Alt+M", Label: "Math", Action: "MathInline", Priority: 2},
			display.BottomBarItem{Key: "Alt+I", Label: "Insert", Action: "InsertPalette", Priority: 3},
			display.BottomBarItem{Key: "Alt+K", Label: "Hover", Action: "LspHover", Priority: 5},
			display.BottomBarItem{Key: "Alt+G", Label: "Keys", Action: "ToggleKeyMenu", Priority: 0, AlignRight: true},
			display.BottomBarItem{Key: "^G", Label: "Help", Action: "ToggleHelp", Priority: 4, AlignRight: true},
			display.BottomBarItem{Key: "^Q", Label: "Quit", Action: "Quit", Priority: 1, AlignRight: true},
		)
	}

	return display.BottomBarContext{
		Badge:       badge,
		BadgeAccent: badgeAccent,
		Hints:       hints,
		MenuRows:    menuRows,
		MenuTip:     menuTip,
	}
}

func typstKeyMenuRows(previewOn, outlineOn, concealOn, treeOn, templatesOn bool) []display.BottomBarMenuRow {
	return []display.BottomBarMenuRow{
		{
			Title: "Editor",
			Items: []display.BottomBarItem{
				{Key: "^S", Label: "Save", Action: "Save", Priority: 0},
				{Key: "^O", Label: "Open", Action: "OpenFile", Priority: 1},
				{Key: "Alt+T", Label: "Files", Action: "command:tree", Priority: 1, Active: treeOn},
				{Key: "^F", Label: "Find", Action: "Find", Priority: 1},
				{Key: "^Z", Label: "Undo", Action: "Undo", Priority: 2},
				{Key: "^Y", Label: "Redo", Action: "Redo", Priority: 3},
				{Key: "^E", Label: "Cmd", Action: "CommandMode", Priority: 0},
				{Key: "^G", Label: "Help", Action: "ToggleHelp", Priority: 2},
				{Key: "^Q", Label: "Quit", Action: "Quit", Priority: 0},
			},
		},
		{
			Title: "Typst",
			Items: []display.BottomBarItem{
				{Key: "Alt+P", Label: "Preview", Action: "TogglePreview", Priority: 0, Active: previewOn},
				{Key: "Alt+O", Label: "Outline", Action: "ToggleOutline", Priority: 0, Active: outlineOn},
				{Key: "Alt+C", Label: "Conceal", Action: "ToggleConceal", Priority: 1, Active: concealOn},
				{Key: "Alt+M", Label: "Math", Action: "MathInline", Priority: 1},
				{Key: "Alt+⇧M", Label: "Display", Action: "MathDisplay", Priority: 3},
				{Key: "Alt+I", Label: "Insert", Action: "InsertPalette", Priority: 2},
				{Key: "Alt+⇧N", Label: "Templates", Action: "ToggleTemplates", Priority: 2, Active: templatesOn},
				{Key: "Alt+B", Label: "Bold", Action: "WrapBold", Priority: 4},
				{Key: "Alt+E", Label: "Italic", Action: "WrapItalic", Priority: 4},
				{Key: "Alt+K", Label: "Hover", Action: "LspHover", Priority: 3},
				{Key: "^Space", Label: "Complete", Action: "LspComplete", Priority: 2},
			},
		},
	}
}

func fileManagerBottomBarContext(tab *action.Tab) display.BottomBarContext {
	previewOn := preview.PaneForTab(tab) != nil || lsp.IsPreviewActive()
	outlineOn := outline.PaneForTab(tab) != nil
	templatesOn := templates.PaneForTab(tab) != nil
	return display.BottomBarContext{
		Badge: "FILES",
		Hints: []display.BottomBarItem{
			{Key: "↑/↓", Label: "Navigate", Priority: 1},
			{Key: "Enter", Label: "Open", Action: "InsertNewline", Priority: 0},
			{Key: "→/←", Label: "Expand/Fold", Priority: 1},
			{Key: "^W", Label: "Editor", Action: "NextSplit", Priority: 0},
			{Key: "^E", Label: "Cmd", Action: "CommandMode", Priority: 2},
			{Key: "Alt+T", Label: "Close", Action: "command:tree", Priority: 0},
			{Key: "Alt+G", Label: "Keys", Action: "ToggleKeyMenu", Priority: 2, AlignRight: true},
			{Key: "^Q", Label: "Quit", Action: "Quit", Priority: 1, AlignRight: true},
		},
		MenuRows: typstKeyMenuRows(previewOn, outlineOn, true, true, templatesOn),
		MenuTip:  "File tree focused · Enter/Tab/Click open · →/← expand/collapse · ^E touch/mkdir/rename/rm",
	}
}

func outlineBottomBarContext(op *outline.OutlinePane) display.BottomBarContext {
	var hints []display.BottomBarItem
	if op.IsFiltering() {
		hints = []display.BottomBarItem{
			{Key: "Enter", Label: "Jump", Action: "pane:Enter", Priority: 0},
			{Key: "↑/↓", Label: "Select", Priority: 1},
			{Key: "Esc", Label: "Clear Filter", Action: "pane:Esc", Priority: 0},
			{Key: "Alt+O", Label: "Close", Action: "ToggleOutline", Priority: 1, AlignRight: true},
		}
	} else {
		hints = []display.BottomBarItem{
			{Key: "↑/↓", Label: "Navigate", Priority: 1},
			{Key: "Enter", Label: "Jump", Action: "pane:Enter", Priority: 0},
			{Key: "/", Label: "Filter", Action: "pane:/", Priority: 0},
			{Key: "Esc", Label: "Editor", Action: "pane:Esc", Priority: 0},
			{Key: "Alt+O", Label: "Close", Action: "ToggleOutline", Priority: 1},
			{Key: "Alt+G", Label: "Keys", Action: "ToggleKeyMenu", Priority: 2, AlignRight: true},
			{Key: "^Q", Label: "Quit", Action: "Quit", Priority: 3, AlignRight: true},
		}
	}
	return display.BottomBarContext{
		Badge:    "OUTLINE",
		Hints:    hints,
		MenuRows: typstKeyMenuRows(false, true, true, false, false),
		MenuTip:  "Outline focused · Enter to jump · / to filter · Esc returns to editor",
	}
}

func templatesBottomBarContext(tp *templates.TemplatePane) display.BottomBarContext {
	var hints []display.BottomBarItem
	if tp.IsFiltering() {
		hints = []display.BottomBarItem{
			{Key: "Enter", Label: "Apply", Action: "pane:Enter", Priority: 0},
			{Key: "↑/↓", Label: "Select", Priority: 1},
			{Key: "Esc", Label: "Clear Filter", Action: "pane:Esc", Priority: 0},
			{Key: "Alt+⇧N", Label: "Close", Action: "ToggleTemplates", Priority: 1, AlignRight: true},
		}
	} else {
		hints = []display.BottomBarItem{
			{Key: "Enter", Label: "Apply", Action: "pane:Enter", Priority: 0},
			{Key: "v", Label: "Preview", Action: "pane:v", Priority: 1, Active: tp.IsLivePreview()},
			{Key: "p", Label: "Preamble", Action: "pane:p", Priority: 1},
			{Key: "n", Label: "Scaffold", Action: "pane:n", Priority: 2},
			{Key: "/", Label: "Filter", Action: "pane:/", Priority: 0},
			{Key: "u", Label: "Universe", Action: "pane:u", Priority: 1},
			{Key: "Esc", Label: "Editor", Action: "pane:Esc", Priority: 0},
			{Key: "q", Label: "Close", Action: "pane:q", Priority: 1, AlignRight: true},
			{Key: "Alt+G", Label: "Keys", Action: "ToggleKeyMenu", Priority: 2, AlignRight: true},
		}
	}
	return display.BottomBarContext{
		Badge:    "TEMPLATES",
		Hints:    hints,
		MenuRows: typstKeyMenuRows(false, false, true, false, true),
		MenuTip:  "Templates focused · Enter apply · v preview · p preamble · n scaffold · u load Universe · / filter · Esc returns to editor",
	}
}

func previewBottomBarContext(pp *preview.PreviewPane) display.BottomBarContext {
	hints := []display.BottomBarItem{
		{Key: "↑/↓", Label: "Scroll", Priority: 1},
		{Key: "[/]", Label: "Page", Priority: 2},
		{Key: "</>", Label: "Width", Priority: 2},
		{Key: "+/-", Label: "Zoom", Priority: 3},
		{Key: "t", Label: "Dark", Action: "pane:t", Priority: 0, Active: pp.IsInverted()},
		{Key: "m", Label: "Trim", Action: "pane:m", Priority: 1, Active: pp.IsTrimMargins()},
		{Key: "r", Label: "Reload", Action: "pane:r", Priority: 3},
		{Key: "Esc", Label: "Editor", Action: "pane:Esc", Priority: 0},
		{Key: "q", Label: "Close", Action: "pane:q", Priority: 0, AlignRight: true},
		{Key: "Alt+G", Label: "Keys", Action: "ToggleKeyMenu", Priority: 3, AlignRight: true},
	}
	return display.BottomBarContext{
		Badge:       "PREVIEW",
		BadgeAccent: "math",
		Hints:       hints,
		MenuRows:    typstKeyMenuRows(true, false, true, false, false),
		MenuTip:     "Preview focused · Wheel/j/k scroll · </> width · t invert · m trim · Esc returns to editor",
	}
}

func helpBottomBarContext() display.BottomBarContext {
	return display.BottomBarContext{
		Badge: "HELP",
		Hints: []display.BottomBarItem{
			{Key: "↑/↓", Label: "Scroll", Priority: 1},
			{Key: "^F", Label: "Find", Action: "Find", Priority: 0},
			{Key: "^W", Label: "Next Pane", Action: "NextSplit", Priority: 1},
			{Key: "^Q", Label: "Close Help", Action: "Quit", Priority: 0},
			{Key: "Alt+G", Label: "Keys", Action: "ToggleKeyMenu", Priority: 2, AlignRight: true},
		},
		MenuTip: "Viewing built-in help · ^Q closes help pane · ^E opens command bar",
	}
}

func genericBottomBarContext(tab *action.Tab, modified bool) display.BottomBarContext {
	saveLabel := "Save"
	if modified {
		saveLabel = "Save*"
	}
	treeOn := hasFileManagerPane(tab)
	return display.BottomBarContext{
		Hints: []display.BottomBarItem{
			{Key: "^S", Label: saveLabel, Action: "Save", Priority: 0},
			{Key: "Alt+T", Label: "Files", Action: "command:tree", Priority: 2, Active: treeOn},
			{Key: "^O", Label: "Open", Action: "OpenFile", Priority: 3},
			{Key: "^F", Label: "Find", Action: "Find", Priority: 1},
			{Key: "^E", Label: "Cmd", Action: "CommandMode", Priority: 0},
			{Key: "^Z", Label: "Undo", Action: "Undo", Priority: 2},
			{Key: "^Y", Label: "Redo", Action: "Redo", Priority: 4},
			{Key: "Alt+G", Label: "Keys", Action: "ToggleKeyMenu", Priority: 0, AlignRight: true},
			{Key: "^G", Label: "Help", Action: "ToggleHelp", Priority: 3, AlignRight: true},
			{Key: "^Q", Label: "Quit", Action: "Quit", Priority: 1, AlignRight: true},
		},
		MenuTip: "Click any shortcut or press Alt+G to hide · ^E for commands",
	}
}

func countDiagnostics(b *buffer.Buffer) (errors, warnings int) {
	if b == nil {
		return 0, 0
	}
	for _, m := range b.Messages {
		if m == nil {
			continue
		}
		switch m.Kind {
		case buffer.MTError:
			errors++
		case buffer.MTWarning:
			warnings++
		}
	}
	return errors, warnings
}
