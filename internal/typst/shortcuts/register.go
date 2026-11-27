package shortcuts

import "github.com/monicafaye/TypX/internal/action"

// RegisterAll registers TypX's M2 bindable actions, colon commands, and
// OS-aware default buffer keybindings (preserving existing micro defaults via
// "|" fallback chains). Intended to be called once from features.Init().
func RegisterAll() {
	action.RegisterAction("MathInline", NewMathInlineAction())
	action.RegisterAction("MathDisplay", NewMathDisplayAction())
	action.RegisterAction("WrapBold", NewWrapBoldAction())
	action.RegisterAction("WrapItalic", NewWrapItalicAction())
	action.RegisterAction("HeadingPromote", NewHeadingPromoteAction())
	action.RegisterAction("HeadingDemote", NewHeadingDemoteAction())
	action.RegisterAction("InsertPalette", NewInsertPaletteAction())
	action.RegisterAction("SymbolPicker", NewSymbolPickerAction())
	action.RegisterAction("SnippetNext", NewSnippetNextAction())
	action.RegisterAction("SnippetPrev", NewSnippetPrevAction())

	action.RegisterCommand("math-inline", NewMathInlineCmd(), nil)
	action.RegisterCommand("math-display", NewMathDisplayCmd(), nil)
	action.RegisterCommand("wrap-bold", NewWrapBoldCmd(), nil)
	action.RegisterCommand("wrap-italic", NewWrapItalicCmd(), nil)
	action.RegisterCommand("heading-promote", NewHeadingPromoteCmd(), nil)
	action.RegisterCommand("heading-demote", NewHeadingDemoteCmd(), nil)
	action.RegisterCommand("insert", NewInsertCmd(), InsertCompleter)
	for _, name := range TemplateNames() {
		action.RegisterCommand("insert-"+name, NewInsertTemplateCmd(name), nil)
	}
	action.RegisterCommand("symbol", NewSymbolCmd(), nil)

	action.RegisterDefaultBindingWithFallback("buffer", "Alt-m", "MathInline")
	action.RegisterDefaultBindingWithFallback("buffer", "Alt-M", "MathDisplay")
	action.RegisterDefaultBindingWithFallback("buffer", "Alt-i", "InsertPalette")
	action.RegisterDefaultBindingWithFallback("buffer", "Alt-y", "SymbolPicker")
	action.RegisterDefaultBindingWithFallback("buffer", "Alt-b", "WrapBold")
	action.RegisterDefaultBindingWithFallback("buffer", "Alt-e", "WrapItalic")
	action.RegisterDefaultBindingWithFallback("buffer", "AltLeft", "HeadingPromote")
	action.RegisterDefaultBindingWithFallback("buffer", "AltRight", "HeadingDemote")
	action.RegisterDefaultBindingWithFallback("buffer", "Tab", "SnippetNext")
	action.RegisterDefaultBindingWithFallback("buffer", "Backtab", "SnippetPrev")
}
