package lsp

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/display"
	"github.com/monicafaye/TypX/internal/util"
)

// InlinePreviewHandler bridges the LSP preview commands (`:preview`, `Alt+P`)
// to the in-terminal Sixel split-pane preview package without an import cycle.
type InlinePreviewHandler interface {
	SupportsInline() bool
	IsOpen(tab *action.Tab) bool
	Open(bp *action.BufPane, focus bool) bool
	Toggle(bp *action.BufPane) bool
	Close(tab *action.Tab) bool
	ToggleInvert(tab *action.Tab) bool
	ToggleTrim(tab *action.Tab) bool
}

var (
	inlinePreviewMu sync.RWMutex
	inlinePreview   InlinePreviewHandler
)

// SetInlinePreviewHandler registers the in-terminal preview pane handler.
func SetInlinePreviewHandler(h InlinePreviewHandler) {
	inlinePreviewMu.Lock()
	inlinePreview = h
	inlinePreviewMu.Unlock()
}

func getInlinePreviewHandler() InlinePreviewHandler {
	inlinePreviewMu.RLock()
	h := inlinePreview
	inlinePreviewMu.RUnlock()
	return h
}

// ShouldUseInlinePreview reports whether `:preview` / `Alt+P` / `--preview`
// should default to the in-terminal preview pane based on `typst.previewMode`
// and detected terminal graphics capability.
func ShouldUseInlinePreview() bool {
	h := getInlinePreviewHandler()
	if h == nil {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(readGlobalString("typst.previewMode", "auto")))
	switch mode {
	case "inline", "terminal", "sixel":
		return true
	case "browser", "web":
		return false
	default: // "auto"
		return h.SupportsInline()
	}
}

// FindProjectRoot resolves the Typst project root directory for filePath
// (checking typst.toml, root-relative "/..." imports, .git, and "../" imports).
func FindProjectRoot(filePath string) string {
	return findProjectRoot(filePath)
}

// PinnedMain returns the currently pinned main .typ file path, or "" if none.
func PinnedMain() string {
	return defaultManager.getPinnedMain()
}

// SetPinChangeHandler registers a callback invoked on the UI goroutine whenever
// the pinned main file changes via :pin or :unpin.
func SetPinChangeHandler(fn func(string)) {
	defaultManager.setPinChangeHandler(fn)
}

// Options configures the tinymist LSP subsystem at startup.
type Options struct {
	// StartPreview requests immediately starting live preview on the initially
	// opened .typ file (set by the --preview CLI flag).
	StartPreview bool
	// InitialFiles lists CLI file arguments being opened at startup so the LSP
	// handshake can resolve the Typst project root immediately without racing
	// LoadInput.
	InitialFiles []string
}

var (
	defaultManager = newManager()
	registerOnce   sync.Once
)

func getpid() int {
	return os.Getpid()
}

// Init initializes the tinymist LSP subsystem, registers LSP actions, commands,
// keybindings, statusline providers, and buffer observers, and respects
// config.GetGlobalOption("typst.lspEnabled").
// Called on the main goroutine during startup before action.InitBindings,
// action.InitCommands, and LoadInput. Must not block startup.
func Init(opts Options) {
	registerOnce.Do(func() {
		action.RegisterAction("LspComplete", LspCompleteAction())
		action.RegisterAction("LspHover", LspHoverAction())
		action.RegisterAction("TogglePreview", TogglePreviewAction())
		action.RegisterAction("LspFormat", LspFormatAction())
		action.RegisterAction("LspDefinition", LspDefinitionAction())
		action.RegisterAction("LspJumpBack", LspJumpBackAction())
		action.RegisterAction("LspReferences", LspReferencesAction())
		action.RegisterAction("LspRename", LspRenameAction())
		action.RegisterAction("LspCodeAction", LspCodeActionAction())
		action.RegisterAction("LspWorkspaceSymbols", LspWorkspaceSymbolsAction())
		action.RegisterAction("NextDiagnostic", NextDiagnosticAction())
		action.RegisterAction("PrevDiagnostic", PrevDiagnosticAction())

		action.RegisterCommand("diagnostics", DiagnosticsCommand(), nil)
		action.RegisterCommand("tinymist-status", TinymistStatusCommand(), nil)
		action.RegisterCommand("tinymist-restart", TinymistRestartCommand(), nil)
		action.RegisterCommand("preview", PreviewCommand(), previewCompleter)
		action.RegisterCommand("hover", HoverCommand(), nil)
		action.RegisterCommand("lsp-complete", LspCompleteCommand(), nil)
		action.RegisterCommand("pin", PinCommand(), buffer.FileComplete)
		action.RegisterCommand("unpin", UnpinCommand(), nil)
		action.RegisterCommand("export", ExportCommand(), exportCompleter)
		action.RegisterCommand("format", FormatCommand(), nil)
		action.RegisterCommand("definition", DefinitionCommand(), nil)
		action.RegisterCommand("jump-back", JumpBackCommand(), nil)
		action.RegisterCommand("references", ReferencesCommand(), nil)
		action.RegisterCommand("lsp-rename", LspRenameCommand(), nil)
		action.RegisterCommand("code-action", CodeActionCommand(), nil)
		action.RegisterCommand("symbols", WorkspaceSymbolsCommand(), nil)
		action.RegisterCommand("diag-next", DiagNextCommand(), nil)
		action.RegisterCommand("diag-prev", DiagPrevCommand(), nil)

		action.RegisterPreActionHook("Save", defaultManager.handlePreSaveFormat)

		action.RegisterDefaultBinding("buffer", "CtrlSpace", "LspComplete")
		action.RegisterDefaultBinding("buffer", "Alt-k", "LspHover")
		action.RegisterDefaultBinding("buffer", "Alt-p", "TogglePreview|RemoveMultiCursor")
		action.RegisterDefaultBindingWithFallback("buffer", "F12", "LspDefinition")
		action.RegisterDefaultBindingWithFallback("buffer", "Shift-F12", "LspReferences")
		action.RegisterDefaultBindingWithFallback("buffer", "Alt-u", "LspJumpBack")
		action.RegisterDefaultBindingWithFallback("buffer", "F2", "LspRename")
		action.RegisterDefaultBindingWithFallback("buffer", "Alt-a", "LspCodeAction")
		action.RegisterDefaultBindingWithFallback("buffer", "Alt-s", "LspWorkspaceSymbols")
		action.RegisterDefaultBindingWithFallback("buffer", "F8", "NextDiagnostic")
		action.RegisterDefaultBindingWithFallback("buffer", "Shift-F8", "PrevDiagnostic")

		display.SetStatusInfoFn("typst.lsp", StatusLineInfo())
	})

	defaultManager.Init(opts)
}

// Shutdown cleanly terminates any running tinymist LSP or preview subprocesses.
// Safe and idempotent to call even if Init was never called.
func Shutdown() {
	defaultManager.Shutdown()
}

// Check verifies tinymist availability (including a 2-second version probe) and
// writes a diagnostic status line (plus OS install hints if missing) to w.
// Returns true if no hard failure occurred (missing tinymist is advisory because
// TypX supports full offline editing).
func Check(w io.Writer) bool {
	if enabled, ok := config.GetGlobalOption("typst.lspEnabled").(bool); ok && !enabled {
		fmt.Fprintln(w, "Tinymist LSP: [INFO] disabled (--no-lsp or typst.lspEnabled=false)")
		return true
	}

	custom := ""
	if v, ok := config.GetGlobalOption("typst.tinymistPath").(string); ok {
		custom = v
	}

	res := DiscoverWithProbe(context.Background(), custom)
	if res.Path != "" {
		if res.Version != "" {
			fmt.Fprintf(w, "Tinymist LSP: [OK] %s (%s, via %s)\n", res.Path, res.Version, res.Source)
		} else {
			fmt.Fprintf(w, "Tinymist LSP: [OK] %s (via %s)\n", res.Path, res.Source)
		}
		return true
	}

	if res.ConfiguredMissing {
		fmt.Fprintf(w, "Tinymist LSP: [WARN] configured typst.tinymistPath not found: %s\n", custom)
		for _, hint := range InstallHints(runtime.GOOS) {
			fmt.Fprintf(w, "              hint: %s\n", hint)
		}
		return true
	}

	fmt.Fprintln(w, "Tinymist LSP: [WARN] tinymist not found on PATH or standard install locations (offline editing still works)")
	for _, hint := range InstallHints(runtime.GOOS) {
		fmt.Fprintf(w, "              hint: %s\n", hint)
	}
	return true
}

// LspCompleteAction returns the BufKeyAction for Ctrl+Space LSP completion.
func LspCompleteAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		return defaultManager.triggerCompletion(bp)
	}
}

// LspHoverAction returns the BufKeyAction for Alt+K LSP hover documentation.
func LspHoverAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		return defaultManager.triggerHover(bp)
	}
}

// TogglePreviewAction returns the BufKeyAction for Alt+P live preview toggle.
func TogglePreviewAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		if bp == nil || bp.Buf == nil {
			return false
		}
		h := getInlinePreviewHandler()
		if h != nil && bp.Tab() != nil && h.IsOpen(bp.Tab()) {
			h.Close(bp.Tab())
			return true
		}
		if !bp.Buf.IsTypst() {
			return false
		}
		if defaultManager.atomicPreview.Load() {
			return defaultManager.togglePreview(bp)
		}
		if ShouldUseInlinePreview() && h != nil {
			h.Open(bp, false)
			return true
		}
		return defaultManager.togglePreview(bp)
	}
}

// DiagnosticsCommand returns the :diagnostics command handler.
func DiagnosticsCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		defaultManager.showDiagnostics(bp)
	}
}

// TinymistStatusCommand returns the :tinymist-status command handler.
func TinymistStatusCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		defaultManager.showStatus(bp)
	}
}

// TinymistRestartCommand returns the :tinymist-restart command handler.
func TinymistRestartCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		defaultManager.restart()
	}
}

// PreviewCommand returns the :preview [inline|browser|stop|focus|invert|trim] command handler.
func PreviewCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		sub := ""
		if len(args) > 0 {
			sub = strings.ToLower(strings.TrimSpace(args[0]))
		}
		h := getInlinePreviewHandler()

		switch sub {
		case "stop", "close", "off":
			closedAny := false
			if h != nil && bp != nil && bp.Tab() != nil && h.IsOpen(bp.Tab()) {
				h.Close(bp.Tab())
				closedAny = true
			}
			if defaultManager.atomicPreview.Load() {
				defaultManager.stopPreview()
				closedAny = true
			}
			if closedAny {
				notifyInfoMessage("Preview stopped")
			} else {
				notifyInfoMessage("Preview is not running")
			}
			return
		case "invert", "dark":
			if h == nil || bp == nil || bp.Tab() == nil || !h.ToggleInvert(bp.Tab()) {
				notifyInfoMessage("Inline preview is not open")
			}
			return
		case "trim", "crop":
			if h == nil || bp == nil || bp.Tab() == nil || !h.ToggleTrim(bp.Tab()) {
				notifyInfoMessage("Inline preview is not open")
			}
			return
		}

		if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
			notifyInfoMessage("Preview is only available for .typ buffers")
			return
		}

		switch sub {
		case "":
			TogglePreviewAction()(bp)
		case "inline", "terminal", "sixel":
			if h == nil {
				notifyInfoError("Inline preview is unavailable")
				return
			}
			h.Toggle(bp)
		case "focus":
			if h == nil {
				notifyInfoError("Inline preview is unavailable")
				return
			}
			h.Open(bp, true)
		case "browser", "web":
			defaultManager.togglePreview(bp)
		default:
			notifyInfoError(fmt.Sprintf("Invalid preview argument %q (use inline, browser, focus, invert, trim, or stop)", args[0]))
		}
	}
}

var previewArgChoices = []string{"inline", "browser", "focus", "invert", "trim", "stop"}

func previewCompleter(b *buffer.Buffer) ([]string, []string) {
	if b == nil {
		return nil, nil
	}
	c := b.GetActiveCursor()
	if c == nil {
		return nil, nil
	}
	input, argstart := b.GetArg()
	lower := strings.ToLower(input)

	var suggestions []string
	for _, choice := range previewArgChoices {
		if strings.HasPrefix(choice, lower) {
			suggestions = append(suggestions, choice)
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

// HoverCommand returns the :hover command handler.
func HoverCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
			notifyInfoMessage("Hover is only available for .typ buffers")
			return
		}
		defaultManager.triggerHover(bp)
	}
}

// LspCompleteCommand returns the :lsp-complete command handler.
func LspCompleteCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
			notifyInfoMessage("LSP completion is only available for .typ buffers")
			return
		}
		defaultManager.triggerCompletion(bp)
	}
}

// StatusLineInfo returns the $(typst.lsp) statusline provider.
func StatusLineInfo() func(*buffer.Buffer) string {
	return func(b *buffer.Buffer) string {
		return defaultManager.statusLine(b)
	}
}

// IsPreviewActive reports whether the browser companion preview is currently running.
func IsPreviewActive() bool {
	return defaultManager.atomicPreview.Load()
}

// ScrollPreviewTo immediately requests the active browser companion preview to
// scroll to the given 0-based line and rune column in b (bypassing cursor
// debounce and working even if the cursor was already on that line).
// Returns false if browser preview is inactive or LSP is offline.
func ScrollPreviewTo(b *buffer.Buffer, line, runeCol int) bool {
	return defaultManager.scrollPreviewTo(b, line, runeCol)
}

// RequestDocumentSymbols asynchronously requests LSP textDocument/documentSymbol
// for the given Typst buffer if tinymist is ready. Returns false immediately if
// LSP is offline or the buffer is not trackable. When symbols arrive, cb runs on
// the main UI goroutine via action.EnqueueUI only if the buffer version still matches.
func RequestDocumentSymbols(b *buffer.Buffer, cb func(version uint64, symbols []DocumentSymbol)) bool {
	return defaultManager.requestDocumentSymbols(b, cb)
}

// PinCommand returns the :pin [path] command handler.
func PinCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		defaultManager.pinCommand(bp, args)
	}
}

// UnpinCommand returns the :unpin command handler.
func UnpinCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		defaultManager.unpinCommand(bp)
	}
}

// ExportCommand returns the :export [pdf|png|svg|html] [output] command handler.
func ExportCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		defaultManager.exportCommand(bp, args)
	}
}

// LspFormatAction returns the BufKeyAction for formatting the active Typst buffer.
func LspFormatAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		return defaultManager.triggerFormat(bp, false)
	}
}

// FormatCommand returns the :format command handler.
func FormatCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		defaultManager.triggerFormat(bp, true)
	}
}

// LspDefinitionAction returns the BufKeyAction for jumping to definition (F12).
func LspDefinitionAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		return defaultManager.triggerDefinition(bp, false)
	}
}

// DefinitionCommand returns the :definition command handler.
func DefinitionCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		defaultManager.triggerDefinition(bp, true)
	}
}

// LspJumpBackAction returns the BufKeyAction for jumping back to the prior location (Alt-u).
func LspJumpBackAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		return defaultManager.jumpBack(bp, false)
	}
}

// JumpBackCommand returns the :jump-back command handler.
func JumpBackCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		defaultManager.jumpBack(bp, true)
	}
}

// NextDiagnosticAction returns the BufKeyAction for jumping to the next diagnostic (F8).
func NextDiagnosticAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		return defaultManager.jumpDiagnostic(bp, 1)
	}
}

// PrevDiagnosticAction returns the BufKeyAction for jumping to the previous diagnostic (Shift-F8).
func PrevDiagnosticAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		return defaultManager.jumpDiagnostic(bp, -1)
	}
}

// DiagNextCommand returns the :diag-next command handler.
func DiagNextCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		defaultManager.jumpDiagnostic(bp, 1)
	}
}

// DiagPrevCommand returns the :diag-prev command handler.
func DiagPrevCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		defaultManager.jumpDiagnostic(bp, -1)
	}
}

// LspReferencesAction returns the BufKeyAction for finding references (Shift-F12).
func LspReferencesAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		return defaultManager.triggerReferences(bp, false)
	}
}

// ReferencesCommand returns the :references command handler.
func ReferencesCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		defaultManager.triggerReferences(bp, true)
	}
}

// LspRenameAction returns the BufKeyAction for LSP symbol rename (F2).
func LspRenameAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		return defaultManager.triggerRename(bp, "", false)
	}
}

// LspRenameCommand returns the :lsp-rename [newName] command handler.
func LspRenameCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		newName := ""
		if len(args) > 0 {
			newName = strings.TrimSpace(args[0])
		}
		defaultManager.triggerRename(bp, newName, true)
	}
}

// LspCodeActionAction returns the BufKeyAction for LSP code actions (Alt-a).
func LspCodeActionAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		return defaultManager.triggerCodeAction(bp, false)
	}
}

// CodeActionCommand returns the :code-action command handler.
func CodeActionCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		defaultManager.triggerCodeAction(bp, true)
	}
}

// LspWorkspaceSymbolsAction returns the BufKeyAction for workspace symbol search (Alt-s).
func LspWorkspaceSymbolsAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		return defaultManager.triggerWorkspaceSymbols(bp, "", false)
	}
}

// WorkspaceSymbolsCommand returns the :symbols [query] command handler.
func WorkspaceSymbolsCommand() func(*action.BufPane, []string) {
	return func(bp *action.BufPane, args []string) {
		query := strings.TrimSpace(strings.Join(args, " "))
		defaultManager.triggerWorkspaceSymbols(bp, query, true)
	}
}
