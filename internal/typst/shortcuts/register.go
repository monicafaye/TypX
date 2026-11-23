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

	bindWithFallback("Alt-m", "MathInline")
	bindWithFallback("Alt-M", "MathDisplay")
	bindWithFallback("Alt-i", "InsertPalette")
	bindWithFallback("Alt-b", "WrapBold")
	bindWithFallback("Alt-e", "WrapItalic")
	bindWithFallback("AltLeft", "HeadingPromote")
	bindWithFallback("AltRight", "HeadingDemote")
}

func bindWithFallback(key, typxAction string) {
	defaults := action.DefaultBindings("buffer")
	if prev := defaults[key]; prev != "" {
		action.RegisterDefaultBinding("buffer", key, typxAction+"|"+prev)
		return
	}
	action.RegisterDefaultBinding("buffer", key, typxAction)
}
