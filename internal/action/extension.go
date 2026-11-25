package action

import (
	"fmt"

	"github.com/monicafaye/TypX/internal/buffer"
)

var (
	extCommands        = map[string]Command{}
	extDefaultBindings = map[string]map[string]string{
		"buffer":   {},
		"command":  {},
		"terminal": {},
	}
	extRuneHooks      []func(bp *BufPane, r rune) bool
	extPreActionHooks = map[string][]func(bp *BufPane) bool{}
)

// RegisterRuneHook registers a callback invoked at the start of BufPane.DoRuneInsert.
// If any registered hook returns true, normal character insertion is skipped.
func RegisterRuneHook(fn func(bp *BufPane, r rune) bool) {
	if fn == nil {
		panic("action.RegisterRuneHook: hook must not be nil")
	}
	extRuneHooks = append(extRuneHooks, fn)
}

// RegisterPreActionHook registers a callback invoked at the start of a named
// built-in BufPane action (such as "Backspace" or "InsertNewline").
// If any registered hook returns true, the built-in action body is skipped.
func RegisterPreActionHook(actionName string, fn func(bp *BufPane) bool) {
	if actionName == "" {
		panic("action.RegisterPreActionHook: action name must not be empty")
	}
	if fn == nil {
		panic("action.RegisterPreActionHook: hook for " + actionName + " must not be nil")
	}
	extPreActionHooks[actionName] = append(extPreActionHooks[actionName], fn)
}

func runRuneHooks(bp *BufPane, r rune) bool {
	for _, fn := range extRuneHooks {
		if fn(bp, r) {
			return true
		}
	}
	return false
}

func runPreActionHooks(actionName string, bp *BufPane) bool {
	for _, fn := range extPreActionHooks[actionName] {
		if fn(bp) {
			return true
		}
	}
	return false
}

// RegisterCommand registers a ":name" command for TypX extensions.
// Order-independent with respect to InitCommands (and preserved across :reload).
// Panics on empty name, nil fn, or duplicate registration against a built-in
// or another extension command so Phase 2/3 collisions fail loudly in tests.
func RegisterCommand(name string, fn func(bp *BufPane, args []string), completer buffer.Completer) {
	if name == "" {
		panic("action.RegisterCommand: command name must not be empty")
	}
	if fn == nil {
		panic("action.RegisterCommand: command handler for " + name + " must not be nil")
	}
	if _, dup := extCommands[name]; dup {
		panic("action.RegisterCommand: duplicate extension command " + name)
	}
	if commands != nil {
		if _, dup := commands[name]; dup {
			panic("action.RegisterCommand: command " + name + " collides with built-in command")
		}
	}

	cmd := Command{action: fn, completer: completer}
	extCommands[name] = cmd
	if commands != nil {
		commands[name] = cmd
	}
}

// RegisterAction registers a bindable BufPane action name (usable in
// bindings.json, Lua, and RegisterDefaultBinding).
// Panics on empty name, nil fn, or duplicate registration against a built-in
// or another extension action.
func RegisterAction(name string, fn BufKeyAction) {
	if name == "" {
		panic("action.RegisterAction: action name must not be empty")
	}
	if fn == nil {
		panic("action.RegisterAction: action handler for " + name + " must not be nil")
	}
	if _, dup := BufKeyActions[name]; dup {
		panic("action.RegisterAction: duplicate action " + name)
	}
	BufKeyActions[name] = fn
}

// RegisterDefaultBinding registers a default keybinding for a pane
// ("buffer", "command", or "terminal").
// Precedence: user bindings.json > TypX extension default > micro default.
// Must be called before InitBindings (preserved across :reload).
// Panics if two extensions claim the same (pane, key).
func RegisterDefaultBinding(pane, key, action string) {
	paneMap, ok := extDefaultBindings[pane]
	if !ok {
		panic(fmt.Sprintf("action.RegisterDefaultBinding: unknown pane %q", pane))
	}
	if key == "" || action == "" {
		panic("action.RegisterDefaultBinding: key and action must not be empty")
	}
	if prev, dup := paneMap[key]; dup {
		panic(fmt.Sprintf("action.RegisterDefaultBinding: duplicate binding for (%q, %q): %q vs %q", pane, key, prev, action))
	}
	paneMap[key] = action
}

// RegisterDefaultBindingWithFallback registers a buffer/pane default binding
// that chains onto any pre-existing micro default binding via "|", allowing
// TypX handlers returning false to fall through to micro's original action.
func RegisterDefaultBindingWithFallback(pane, key, typxAction string) {
	defaults := DefaultBindings(pane)
	if prev := defaults[key]; prev != "" {
		RegisterDefaultBinding(pane, key, typxAction+"|"+prev)
		return
	}
	RegisterDefaultBinding(pane, key, typxAction)
}

func mergeExtCommands() {
	for name, cmd := range extCommands {
		if _, dup := commands[name]; dup {
			panic("action.InitCommands: extension command " + name + " collides with built-in command")
		}
		commands[name] = cmd
	}
}
