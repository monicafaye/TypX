# TypX — System Architecture (`ARCHITECTURE.md`)

TypX is a single-binary Go terminal editor for [Typst](https://typst.app/), forked from **`zyedidia/micro` (`v2.0.15`)**. Typst modules (`internal/typst/*`, `internal/lsp`) stay decoupled from one another and integrate into `micro` through narrow **seams** wired in one composition root (`internal/typst/features`), while delegating Typst compilation, semantic analysis, and live preview rendering to an out-of-process **`tinymist`** binary.

---

## 1. Module & Dependency Graph

Solid arrows (`-->`) denote Go compile-time imports (`A --> B` means `A` imports `B`). Dashed arrows (`-.->`) denote out-of-process stdio/HTTP IPC or spawned CLI compiles.

```mermaid
flowchart TB
    Cmd["cmd/typx (CLI, Startup, Main Event Loop)"]

    subgraph Composition["Composition Root"]
        Features["internal/typst/features (Seam Wiring, Statusline, BottomBar Provider)"]
    end

    subgraph TypstModules["Typst Modules"]
        Mode["internal/typst (Lexical Mode Detector — stdlib only)"]
        Conceal["internal/typst/conceal (Symbol Table, VisualMap)"]
        Shortcuts["internal/typst/shortcuts (Wrappers, Snippets, Smart Pairs)"]
        Bib["internal/typst/bib (Bibliography Scanner, BibTeX and Hayagriva Parsers)"]
        Outline["internal/typst/outline (Incremental Parser, Project Walker, OutlinePane)"]
        Templates["internal/typst/templates (Offline Registry, Universe Client, TemplatePane)"]
        Preview["internal/typst/preview (Kitty/Sixel, PNG Deck, PDF Anchor Index, PreviewPane)"]
        LSP["internal/lsp (tinymist JSON-RPC Client, Root Resolver, Browser Proxy)"]
    end

    subgraph MicroCore["Inherited micro Core (Narrow Seams Only)"]
        Action["internal/action (BufPane, Tab, UI Queue, Registries, BottomBar Clicks)"]
        Display["internal/display (BufWindow, StatusLine, BottomBar)"]
        Buffer["internal/buffer (LineArray, Messages, Lifecycle Observers)"]
        Screen["internal/screen (tcell Screen, PostShow Hooks)"]
        Views["internal/views (Split Tree + DockLeft/DockRight/DockBottom — stdlib only)"]
        Config["internal/config (Settings, Embedded Runtime, Lua VM)"]
    end

    TinymistLSP["tinymist lsp (stdio JSON-RPC 2.0 + preview server)"]
    TinymistCLI["tinymist compile (per-compile PNG + parallel PDF)"]
    Browser["OS Browser (Optional Companion Preview)"]

    Cmd --> Features
    Cmd --> LSP
    Cmd --> Preview
    Cmd --> Templates
    Cmd --> Action
    Cmd --> Buffer
    Cmd --> Display
    Cmd --> Screen
    Cmd --> Config

    Features --> Mode
    Features --> Conceal
    Features --> Shortcuts
    Features --> Bib
    Features --> Outline
    Features --> Preview
    Features --> Templates
    Features --> LSP
    Features --> Action
    Features --> Buffer
    Features --> Display
    Features --> Config

    Conceal --> Mode
    Shortcuts --> Action
    Shortcuts --> Buffer
    Bib --> Action
    Bib --> Buffer
    Outline --> Mode
    Outline --> Action
    Outline --> Buffer
    Outline --> Display
    Outline --> Screen
    Outline --> Config
    Templates --> Action
    Templates --> Buffer
    Templates --> Display
    Templates --> Screen
    Templates --> Config
    Preview --> Action
    Preview --> Buffer
    Preview --> Display
    Preview --> Screen
    Preview --> Views
    Preview --> Config
    LSP --> Action
    LSP --> Buffer
    LSP --> Display
    LSP --> Config

    Action --> Buffer
    Action --> Display
    Action --> Screen
    Action --> Views
    Action --> Config
    Display --> Conceal
    Display --> Buffer
    Display --> Screen
    Display --> Views
    Display --> Config
    Buffer --> Screen
    Buffer --> Config
    Screen --> Config

    LSP <-.->|"JSON-RPC 2.0 / HTTP+WS"| TinymistLSP
    LSP <-.->|"Local Proxy HTTP+WS"| Browser
    Preview -.->|"exec: --format png & pdf"| TinymistCLI
```

---

## 2. Lifecycle Ordering: Startup, Event Loop & Teardown

Filesystem inspection does not reveal runtime ordering constraints; these sequences are load-bearing:

1. **Config Directory Precedence (`internal/config/config.go`)**:
   `-config-dir` flag → `TYPX_CONFIG_HOME` → `<os.UserConfigDir()>/typx` → `~/.config/typx`.
2. **Startup Sequence (`cmd/typx/typx.go`)**:
   - Parse flags (`-check`, `-no-lsp`, `-preview`, `-new`, `-config-dir`, and dynamic `-<option>` overrides) and initialize config/settings.
   - If `-check`, run `runCheck` (bounded `tinymist` version probe + environment check) and exit without touching the terminal screen.
   - If `-new <template>`, run `handleNewFlag` (printing template catalog on `-new list` and exiting, or scaffolding personal/project/package files non-destructively and passing the created entrypoint to `lsp.Init` and `LoadInput`).
   - Call **`preview.ProbeTerminal()` *before* `screen.Init()`**: terminal capability/cell-pixel queries read stdin/stdout directly before `tcell` takes ownership of the TTY.
   - Initialize `screen`, clipboard, and `config.LoadAllPlugins()`.
   - Run **`features.Init()`** *before* **`lsp.Init(lsp.Options{StartPreview, InitialFiles})`**: `features.Init()` installs `lsp.SetInlinePreviewHandler`, which `lsp.ShouldUseInlinePreview()` reads to decide whether `-preview` opens inline or in the browser. Passing `InitialFiles` lets LSP resolve the Typst project root before `LoadInput` runs.
   - Run `action.InitBindings()` → `action.InitCommands()` → plugin `preinit` → `action.InitGlobals()` → `LoadInput(args)` → `action.InitTabs(b)`.
   - If `-preview` and inline preview is selected, open `preview.Open(cp, false)` on the active `.typ` `BufPane` now that tabs exist.
   - Run plugin `init` → `postinit` → `config.InitColorscheme()` → `StartAutoSave()`, then enter `DoEvent()`.
3. **Single `DoEvent()` Iteration (`cmd/typx/typx.go`)**:
   1. `buffer.FlushObservers()` — delivers coalesced edits/cursor moves before rendering so conceal caches, outline highlights, and LSP state reflect the current frame.
   2. Render all panes, tabs, and `InfoBar` → `screen.Screen.Show()` → **`screen.RunPostShowHooks()`** (emits Kitty or Sixel frames *after* `tcell` cell diffing completes).
   3. Block on `select` across shell jobs, autosave, `screen.Events`, `screen.DrawChan()`, **`action.UIQueueReady()`** (`action.DrainUIQueue()`), and termination signals.
   4. Dispatch terminal events in precedence order: `EventResize` → active `InfoBar` prompt → **`action.HandleBottomBarEvent`** (bottom bar / `Alt+G` key menu) → `action.Tabs.HandleEvent`.
4. **Teardown (`exit(rc)` in `cmd/typx/typx.go`)**:
   `preview.Shutdown()` (cancels active compiles, cleans temp PNG/PDF dirs) → `lsp.Shutdown()` (terminates `tinymist` LSP/preview subprocesses, reinforced on Windows by Job Objects) → `b.Fini()` on unmodified buffers → `screen.Screen.Fini()`.

---

## 3. Seams into `zyedidia/micro`

To keep upstream `micro` diffs minimal and localized, TypX interacts with `micro` core through seven narrow seams:

| Seam | Location | Interface & Invariant |
| --- | --- | --- |
| **Main-Loop UI Queue** | `internal/action/uiqueue.go` | `action.EnqueueUI(func())` schedules callbacks onto the main goroutine (drained in `DoEvent()`). All worker goroutines marshal buffer, pane, tab, `InfoBar`, and screen mutations through this seam. |
| **Buffer Lifecycle & Atomic Edits** | `internal/buffer/hooks.go`, `internal/buffer/eventhandler.go`, `internal/buffer/buffer.go` | `buffer.RegisterObserver(Observer)` delivers `OnBufferOpen`, `OnBufferChange`, `OnBufferSave`, `OnBufferClose`, and `OnCursorMove` synchronously on the main goroutine (changes precede cursor moves and flush before save or close). `EventHandler.ApplyAtomicEdits` groups multi-range LSP edits into one Undo/Redo step. `SharedBuffer.SymbolHighlights` (`[]HighlightRange`), `SetSymbolHighlights`, and `ClearSymbolHighlights` store symbol occurrence ranges rendered inside `BufWindow`'s `HighlightSearch` pass in `internal/display/bufwindow.go` with `selection`, `hlsearch`, `symbol-highlight`, and `cursor-line` precedence. |
| **Extension Registries & Input Hooks** | `internal/action/extension.go` | `RegisterCommand`, `RegisterAction`, `RegisterDefaultBinding` / `RegisterDefaultBindingWithFallback` (precedence: user `bindings.json` > TypX default > `micro` default), `RegisterRuneHook`, and `RegisterPreActionHook` (`Backspace`, `InsertNewline` including the `InsertEnter` alias, `Autocomplete`, `Escape`, `Save`). Registration panics on duplicates so collisions fail loudly in tests. |
| **Statusline & Bottom Bar** | `internal/display/statusline.go`, `internal/display/bottombar.go`, `internal/action/bottombar.go` | `display.SetStatusInfoFn` feeds `$(typst.mode)`, `$(typst.words)`, and `$(typst.lsp)` (with `PIN:` accent rendering). `display.SetBottomBarProvider` supplies context hints and `Alt+G` menu rows; `action.HandleBottomBarEvent` intercepts clicks and menu keys before pane dispatch. |
| **Display-Only Concealment** | `internal/display/bufwindow.go`, `internal/display/softwrap.go` | `BufWindow` renders `conceal.LineView` glyphs on `.typ` buffers without altering buffer bytes, and `LocFromVisual` maps clicks/visual coordinates back to buffer rune indices via `LineView.VisualToBuf`. |
| **Docked Split Panes** | `internal/views/splits.go` | `Node.DockLeft(width)` (Outline/Templates), `Node.DockRight(width)` (Preview), and `Node.DockBottom(height)` (`ListPane` drawer) attach auxiliary panes alongside normal editor splits. |
| **Post-Show & Screen Lifecycle Graphics** | `internal/screen/screen.go` | `screen.RegisterPostShowHook` runs after `tcell.Screen.Show()` so raw terminal graphics (Kitty or Sixel) paint over reserved cells without `tcell` overwriting them; `RegisterPreFiniHook` and `RegisterPostRestartHook` delete and restore Kitty placements around `TempFini`/`TempStart`. |

---

## 4. Cross-Module Seams & Composition Root

`internal/typst/features/features.go` (`features.Init()`) is the single composition root that wires TypX's decoupled modules together. Each seam keeps the owning module deep and testable with fake adapters:

| Seam | Interface | Production Adapter (`features.Init()`) | Purpose |
| --- | --- | --- | --- |
| `lsp.SetInlinePreviewHandler` | `lsp.InlinePreviewHandler` (`SupportsInline`, `IsOpen`, `Open`, `Toggle`, `ToggleProtocol`, `Close`, `ToggleInvert`, `ToggleTrim`, `ToggleSync`, `JumpToPage`) | `inlinePreviewAdapter` delegating to `internal/typst/preview` | Routes `:preview` / `Alt+P` / `--preview` between inline Kitty/Sixel preview and browser preview without `lsp` <-> `preview` imports. |
| `lsp.PinnedMain` & `lsp.SetPinChangeHandler` | `func() string`, `func(func(string))` | `preview.SetMainFileResolver` & `preview.NotifyMainFileChanged` | Redirects inline preview compiles to the pinned project entrypoint on subfile edits/saves without `lsp` ↔ `preview` imports. |
| `preview.SetAnchorNeedChecker` & `preview.SetSectionsProvider` | `AnchorNeedCheckerFunc`, `SectionsProviderFunc` | `outline.PaneForTab` & `outline.ParseString` | Skips parallel PDF anchor compiles when outline and cursor sync are inactive, and supplies document sections for inline cursor sync without `preview` <-> `outline` imports. |
| `preview.SetCompiler` | `preview.Compiler` (`CompilePages(ctx, CompileRequest) (*DocumentDeck, error)`) | `&preview.TinymistCompiler{...}` (swapped for fakes in tests) | Isolates PNG/PDF subprocess compilation from `PreviewPane` state and viewport rendering. |
| `preview.TinymistCompiler.DiscoverFn` & `preview.SetRootResolver` | `TinymistDiscoveryFunc`, `RootResolverFunc` | `lsp.Discover` and `lsp.FindProjectRoot` | Reuses LSP binary discovery and multi-file root resolution without importing `internal/lsp`. |
| `outline.SetSymbolProvider` | `outline.SymbolProvider` | `lsp.RequestDocumentSymbols` via `convertLSPSymbols` | Enriches the pure-Go outline parser with `tinymist` `textDocument/documentSymbol` results when LSP is active. |
| `outline.SetJumpHandler` | `outline.JumpHandler` | `syncPreviewToOutlineItem` calling `preview.PreviewPane.ScrollToSection` and `lsp.ScrollPreviewTo` | Repositions inline and browser preview when an outline entry is selected without coupling `outline` to `preview` or `lsp`. |
| `outline.SetMainFileResolver` & `bib.SetMainFileResolver` | `func(string) string` | `lsp.PinnedMain` | Supplies the pinned main file path to multi-file outline traversal and bibliography resolution without importing `internal/lsp`. |
| `outline.SetFileOpener` | `func(*action.BufPane, string, int, int) *action.BufPane` | `lsp.OpenFileAt` | Opens cross-file Document Outline targets honoring `typst.gotoOpenMode` without coupling `outline` to `internal/lsp`. |
| `shortcuts.SetRootResolver`, `outline.SetRootResolver` & `bib.SetRootResolver` | `func(string) string` | `lsp.FindProjectRoot` | Resolves project roots for `.typx/snippets.json`, multi-file `#include` resolution, and root bibliography discovery without importing `internal/lsp`. |
| `shortcuts.SetPicker` & `bib.SetPicker` | `PickerFunc` | `openBottomPicker` calling `lsp.OpenListPaneWithOptions` | Reuses the bottom-docked `ListPane` drawer for `:insert`, `:symbol`, and `:cite` without coupling `shortcuts` or `bib` to `internal/lsp`. |
| `templates.SetExternalBackend`, `templates.SetRootResolver` & `templates.SetPreviewProvider` | `templates.ExternalBackend`, `func(string) string`, `templates.PreviewProvider` | `lspTemplateAdapter`, `lsp.FindProjectRoot`, `previewTemplateAdapter` | Lets `TemplatePane` generate `@preview` or `@local` projects via `tinymist` and render live gallery previews in `PreviewPane` without importing `internal/lsp` or `internal/typst/preview`. |
| `shortcuts.SetModeDetector` | `func(lines [][]byte, line, col int) shortcuts.Mode` | `typst.DetectModeAt` (with rune-to-character cluster column mapping) | Injects rune-aware lexical mode detection; `shortcuts/mode.go` delegates directly to `internal/typst.DetectModeAt` while preserving test swappability. |
| `lsp.SetSnippetExpander` | `lsp.SnippetExpanderFunc` | `shortcuts.ExpandSnippetReplacing` | Expands single-match and multi-candidate `Enter` LSP snippet completions into live multi-stop `Tab` and `Shift+Tab` sessions without `lsp` and `shortcuts` cross-imports. |

---

## 5. Modules (Interface, Implementation & Invariants)

### 5.1 Lexical Mode & Concealment (`internal/typst`, `internal/typst/conceal`)

- **Interface**: `typst.ScanLineState` / `DetectModeAtLine` / `DetectModeAt` (cached per-line lexical state: `Markup`, `Math`, `Code`, `Raw`); `conceal.BuildLineView` returning `LineView` with `VisualToBuf` / `BufToVisual`; `conceal.ToggleSetting`; `conceal.SymbolCatalog` returning a defensive slice of `CatalogSymbol` (`Name`, `Glyph`, `Category`) across 8 categories for `:symbol`.
- **Implementation**: Line-entry state caching tracks multi-line `$ ... $` blocks and raw fences in `O(1)` per visible line. Longest-match symbol scanning covers Unicode math symbols, operators (`->`, `<=`, `!=`, `and`, `or`, `tack.r`, `angle.l`, `floor.l`, `ceil.l`), compound dotted Typst identifiers (`in.not`, `subset.eq.not`, `inter.big`), math alphabet calls (`bb(...)`, `cal(...)`), and single or parenthesized multi-character scripts (`^(n+1)`, `_(i=1)`) via multi-rune `Span.Glyphs` while enforcing non-identifier boundaries so words like `sin`, `index`, or `summary` stay intact.
- **Invariants**:
  - Concealment is strictly display-only and never mutates buffer bytes.
  - Active cursor line (default `typst.concealMode == "line"`) or active token (`"token"`) bypasses concealment.
  - Register compound dotted Typst math symbols explicitly in `internal/typst/conceal/symbols.go` so longest-match scanning does not split prefixes like `in` inside `in.not`.

### 5.2 `tinymist` LSP, Editing, Search & Browser Preview (`internal/lsp`)

- **Interface**: `lsp.Init(Options)`, `lsp.Shutdown()`, `lsp.Check(io.Writer)`, `lsp.Discover`, `lsp.FindProjectRoot`, `lsp.PinnedMain`, `lsp.SetPinChangeHandler`, `lsp.SetSnippetExpander`, `lsp.RegisterCompletionHooks`, `lsp.RequestDocumentSymbols`, `lsp.ScrollPreviewTo`, `lsp.OpenFileAt`, `lsp.IsOffline`, `lsp.InitTemplate`, `lsp.GetTemplateEntry`, `lsp.IsPreviewActive`, `lsp.ShouldUseInlinePreview`, `ApplyWorkspaceEdit`, `ListPane`, `OpenListPaneWithOptions`, plus actions and commands (`:preview`, `:diagnostics`, `:diag-next` / `F8`, `:diag-prev` / `Shift-F8`, `:definition` / `F12`, `:references` / `Shift-F12`, `:jump-back` / `Alt+U`, `:lsp-rename` / `F2`, `:code-action` / `Alt+A`, `:symbols` / `Alt+S`, `:select-expand` / `Alt+W` (`Alt-w`), `:select-shrink` / `Alt+Shift+W` (`Alt-W`), `:format`, `:export`, `:pin`, `:unpin`, `:tinymist-status`, `:tinymist-restart`, `:hover` / `Alt+K`, `:signature-help` / `Alt+H`, `:lsp-complete` / `Ctrl+Space`).
- **Implementation**:
  - Split by responsibility across `manager.go`, `sync.go`, `diagnostics.go`, `completion.go`, `signature.go`, `preview_lsp.go`, `root.go`, `edits.go`, `workspace_edit.go`, `listpane.go`, `pin.go`, `export.go`, `format.go`, `nav.go`, `selection.go`, `rename.go`, `codeaction.go`, and `symbols.go`.
  - Binary discovery checks `typst.tinymistPath`, `PATH`, and OS-specific install paths. Startup reads `ServerInfo.Version` from `initialize` rather than running a blocking `--version` probe, and passes `formatterMode` (`typst.formatter`). Missing binaries transition to `StateMissing` (`LSP: no tinymist` in the statusline, distinct from explicit `--no-lsp` `StateOff`) and emit a single non-blocking OS install hint on the first explicit LSP/preview invocation.
  - Stdio JSON-RPC 2.0 transport with UTF-16 to rune column conversion, debounced document sync plus synchronous `flushDocChangeNow` before RPC requests, and atomic multi-edit application (`ApplyTextEdits` through `ApplyAtomicEdits`) for single-step undo formatting.
  - Pre-flight validated `ApplyWorkspaceEdit` applies multi-file `WorkspaceEdit` (`changes` and `documentChanges` `TextDocumentEdit`) across open buffers (single-step undo per buffer) and unopened disk files (preserving CRLF/LF line endings via `SafeWrite`), and handles server-initiated `workspace/applyEdit` requests without deadlocking during shutdown.
  - Multi-candidate completion in `completion.go` stores candidate metadata (`rawInsert`, `prefixStartX`, `isSnippet`, `reqVersion`, `reqLine`) alongside `ShowSuggestions` and `Autocomplete`. `RegisterCompletionHooks` hooks `InsertNewline` (`Enter`) to commit the highlighted candidate (or index `0` from auto-suggest) and expand snippets via `shortcuts.ExpandSnippetReplacing`, and hooks `Escape` (`Esc`) to revert previewed suffixes from `CycleAutocomplete` while leaving non-LSP completers untouched.
  - Interactive bottom-docked `ListPane` (`DockBottom`) with `/` live text filtering and configurable `ListPaneOptions` (`Title`, `StartFiltering`, `InitialQuery`, `EnterLabel`, `Badge`, `DiagnosticsMode`, `CloseOnSelect`) powers `:diagnostics` (`f` errors-only toggle), multi-target `:definition`, `:references`, `:code-action`, `:symbols`, and the `:insert`, `:symbol`, and `:cite` pickers via `features`, paired with per-tab destination jump stacks (`:jump-back`) and `@preview` cache read-only guards.
  - `textDocument/documentHighlight` in `preview_lsp.go` debounces requests (`150 ms`) on `OnCursorMove` when `typst.documentHighlight` is enabled, discards stale responses by buffer version and generation, applies ranges via `SharedBuffer.SetSymbolHighlights` on the main loop (`action.EnqueueUI`), and clears highlights on edit, close, setting toggle, or moving off a symbol.
  - `textDocument/selectionRange` in `selection.go` maintains a per-pane selection history stack (up to 64 frames, invalidated on edit or cursor jump outside the stack) for `:select-expand` (`Alt-w`) and `:select-shrink` (`Alt-W`).
  - Symbol renaming (`:lsp-rename` / `F2`) uses `textDocument/prepareRename` with placeholder prompt fallback, and registers `:lsp-rename` rather than `:rename` to preserve the bundled `filemanager` plugin's `:rename` command (`F2` falls back to `Save` outside `.typ` buffers).
  - Project pin persistence (`<ConfigDir>/pins.json` + `tinymist.pinMain` with raw path `[absPath]` / `[null]`), synchronous-pre-save formatting hook (`typst.formatOnSave`, 1s timeout fallback), and subprocess `:export` / `typst.exportOnSave` (`tinymist compile` with shadow staging).
  - `FindProjectRoot` resolves multi-file project roots in order: `typst.toml`, ancestor matching `#import "/..."`, `.git`, `../` depth, and file directory.
  - Companion browser preview launches `tinymist.doStartPreview` (`--no-open`, `--task-id`, `--partial-rendering true`, `--root`) fronted by a local HTTP/WebSocket proxy, and handles server-to-client `tinymist/preview/scrollSource` and `editorScrollTo` reverse-sync notifications via `action.EnqueueUI` and `jumpToFilePosition` (honoring `typst.gotoOpenMode` and UTF-16 column conversion).
- **Invariants**:
  - Offline editing works cleanly when `tinymist` is absent or `--no-lsp` is set; `:export`, `:diag-next`/`prev`, `:jump-back`, and offline `:pin` degrade gracefully without an active LSP server.
  - `tinymist.pinMain` passes raw filesystem paths `[absPath]` (never `file://` URIs); `tinymist.doStartPreview`, `tinymist.scrollPreview`, and `tinymist.doKillPreview` require the explicit task ID string as `args[0]`.
  - Pre-save format failures or timeouts never block `Save()` (guarded via per-buffer `formattingOnSave` re-entry map on the main loop).
  - Child processes never leak stdout/stderr onto the `tcell` screen and are reaped on exit (including Windows Job Objects with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`).

### 5.3 Native In-Terminal Preview (Kitty & Sixel) and PDF Anchor Sync (`internal/typst/preview`)

- **Interface**: `preview.ProbeTerminal`, `preview.SupportsInline`, `preview.SupportsKitty`, `preview.SupportsSixel`, `preview.ActiveProtocol`, `preview.ParseProtocol`, `preview.UseTmuxPassthrough`, `preview.Init`, `preview.Shutdown`, `preview.Open` / `Toggle` / `ToggleWithProtocol` / `CloseForTab` / `PaneForTab`, `preview.OpenForOverride` / `ClearOverrideForTab` / `CompileTypstToImages`, `PreviewPane.ScrollToSection(SectionTarget)`, `PreviewPane.ScrollToCursorLine`, `PreviewPane.JumpToPage`, `PreviewPane.ScrollX`, `PreviewPane.MaxScrollX`, `PreviewPane.ZoomPct`, `DocumentDeck.CarryForwardFrom`, `DocumentDeck.CompositeViewport`, `DocumentDeck.RenderViewportCached`, `DocumentDeck.ResolveSectionScrollY`, `DocumentDeck.ResolveCursorVirtualY`, `DocumentDeck.ResolveClickToSourceLine`, and `ParsePDFAnchors([]byte) *DocumentAnchors`.
- **Implementation**:
  - **Terminal probe**: Cross-platform Kitty graphics (APC `a=q` 1x1 test action), Sixel (DA1 attribute 4), and cell-pixel detection (CSI `16t`/`14t`, `TIOCGWINSZ`) across Kitty, Ghostty, WezTerm, Konsole, Windows Terminal, foot, iTerm2, and xterm. Prefers Kitty graphics when both Kitty and Sixel are available, supports explicit override via `typst.previewProtocol` (`"auto"`, `"kitty"`, `"sixel"`) and `:preview kitty` / `:preview sixel`, skips raw escape probing on `dumb`/`linux` terminals, omits the Kitty APC query inside GNU Screen and macOS Terminal.app, and wraps Kitty probe/transmit/delete APC sequences in per-chunk tmux DCS passthrough (`\x1bPtmux;...\x1b\\`) when running inside `tmux`.
  - **Conditional parallel PNG + PDF compile (`TinymistCompiler.CompilePages`)**: Stages unsaved buffers to a sibling shadow file so relative/root imports still resolve, runs `tinymist compile --format png` for per-page raster images, computes a SHA-256 content hash for each `page-*.png`, and in parallel runs a non-fatal `tinymist compile --format pdf` to extract rendered coordinates (`ParsePDFAnchors`) only when needed (when Document Outline is open, `typst.previewSyncCursor` is active, a section jump is pending, or an inline double-click/`Ctrl+Click` triggers an on-demand anchor compile; skipped via `CompileRequest.SkipAnchors` otherwise).
  - **Three-tier section jump and click-to-source (`ResolveSectionScrollY`, `ResolveCursorVirtualY`, `ResolveClickToSourceLine`)**: Resolves outline targets and debounced cursor-line sync (`typst.previewSyncCursor` / `f` / `:preview sync`, with a visible-band deadband to skip redundant redraws) via (1) exact `<label>` named destination from `/Names /Dests`, (2) DP monotonic alignment of source headings against PDF `/Outlines` bookmarks, and (3) bracketed interpolation between surrounding PDF anchors or proportional buffer-line fallback. Double-clicking (within `400 ms` and `2` cells) or `Ctrl+Click` inside `PreviewPane` calls `ResolveClickToSourceLine` to interpolate the clicked pixel row back to a source line, centers the linked editor cursor, and returns focus to the editor. Also supports direct page jumps via `:preview page <n>` / `:preview <n>`.
  - **Incremental page caching, zoomed panning & dual graphics output**: `DocumentDeck.CarryForwardFrom` reuses decoded `raw` and `scaled` RGBA images whenever a page slot's SHA-256 PNG hash matches the prior deck, and `RenderViewportCached` hashes the composited viewport parameters (visible page hashes, `scrollY`, `scrollX`, `zoomPct`, `invert`, `trimMargins`, target pixel size) to skip pixel-identical Kitty and Sixel frame emission. Resamples with Catmull-Rom filtering (`golang.org/x/image/draw`) and encodes either 24-bit lossless PNG chunks via the Kitty Graphics Protocol (`a=T,f=100,i=1,p=1,q=2,C=1`, 4096-byte base64 chunks) or 248-color RLE Sixel streams. Supports clamped vertical scrolling (`scrollY`), `40%` to `250%` zoom (`0` resets zoom to `100%` and `scrollX` to `0`), horizontal canvas cropping (`scrollX` / `MaxScrollX` via `h`/`l`, `tcell.WheelLeft`/`tcell.WheelRight`, and zoom-aware `Left`/`Right`, while `[`/`]` always jump pages), margin trimming, dark-mode inversion, keyboard (`<`/`>`) plus mouse-drag width resizing inside a `DockRight` pane, and temporary override decks for Template Gallery live preview.
- **Invariants**:
  - Set `cmd.Dir = rootDir` alongside `--root` when invoking `tinymist compile` so Windows relative ancestor checks do not fail.
  - Capture `canceled := ctx.Err() == context.Canceled` *before* calling `cancel()` when dispatching compile results to `action.EnqueueUI`.
  - Always pass `q=2` on Kitty transmit and delete sequences so terminal responses never leak into `tcell`'s stdin reader. Explicitly emit `\x1b_Ga=d,d=I,i=1,q=2\x1b\\` on frame replacement, pane close, tab switch, divider drag start, placeholder/error transitions, `TempFini`, and `Shutdown()`.
  - On Sixel / Windows ConPTY, any preview geometry shift, pane close, tab switch, or divider drag start triggers `pendingSync` and wipes atomically inside DEC Mode 2026 (`\x1b[?2026h` + `\x1b[0m\x1b[2J` -> `screen.Screen.Sync()` -> Sixel emit -> `\x1b[?2026l`). Always reset SGR (`\x1b[0m`) before clearing Sixel regions and defer redraws while `tab.IsResizing()` is true.

### 5.4 Document Outline Sidebar (`internal/typst/outline`)

- **Interface**: `outline.Init`, `outline.SetSymbolProvider`, `outline.SetJumpHandler`, `outline.SetMainFileResolver`, `outline.SetRootResolver`, `outline.SetFileOpener`, `outline.EffectiveWidth`, and `OutlinePane` (`action.Pane`, `action.AuxiliaryPane`, `display.Window`).
- **Implementation**: Pure-Go incremental parser extracts `= Headings`, `#figure(...)` captions, and `<label>` anchors while skipping raw fences and nested `/* ... */` comments (switching to async snapshot parsing above 5,000 lines), optionally enriched by LSP symbols. `project.go` adds a lex-aware `#include "..."` scanner (ignoring comments, raw blocks, math, and strings) and a cycle-guarded recursive walker rooted at the pinned main file (`MainFileResolver`) or the active file when it contains `#include` statements. When multiple files are present, `OutlinePane` interleaves `KindFile` relative-path header rows, provides a `p` toggle between `[Project]` and `[File]` views, preserves active-file cursor tracking, and opens cross-file jumps through `FileOpener` (`lsp.OpenFileAt`, honoring `typst.gotoOpenMode`) plus `JumpHandler` (which skips `KindFile` rows during preview sync). Renders in a `DockLeft` pane (`Alt+O` / `:outline`) with interactive `/` filtering.
- **Invariants**:
  - `micro` stores `cursor-line`'s highlight color in the foreground slot — decompose `fg` and apply `.Background(fg)` for active outline rows.
  - Enforce minimum width clamping (`EffectiveWidth`) so the sidebar hides cleanly below `MinTerminalWidth` (50 columns) without starving editor panes.

### 5.5 Shortcuts, Bibliography, Bottom Bar & Bundled Plugins (`internal/typst/shortcuts`, `internal/typst/bib`, `internal/typst/features`, `runtime/plugins/`)

- **Interface**: `shortcuts.RegisterAll`, `shortcuts.SetPicker`, `shortcuts.SetRootResolver`, `shortcuts.HandleTypstRune` / `HandleTypstBackspace` / `HandleTypstNewline` / `HandleSnippetEscape`, `shortcuts.ExpandSnippetAt` / `ExpandSnippetReplacing`; `bib.RegisterAll`, `bib.SetPicker`, `bib.SetMainFileResolver`, `bib.SetRootResolver`, `bib.SuggestEntries`; `features.Init` (wires statusline `$(typst.mode)` / `$(typst.words)` caches, `offlineCiteObserver`, `openBottomPicker`, and `buildBottomBarContext`).
- **Implementation**:
  - Context-aware math/markup actions (`Alt+M`, `Alt+Shift+M`, `Alt+B`, `Alt+E`, `Alt+Left`/`Alt+Right`), mode-aware smart pairing (`$`, `[`, `(`, `{`, `"`), and markup list continuation on `Enter` (`-`, `+`, `N.`, `/`).
  - Three-layer custom snippet loader (`custom_snippets.go`) merges built-in snippets (`templates.go`), user `<config-dir>/snippets.json`, and project `<project-root>/.typx/snippets.json` (accepting `"name": "body"` strings and `{"body", "description"}` objects, reporting malformed JSON as a non-fatal infobar warning). `Alt+I` and bare `:insert` open a filterable bottom `ListPane` (`SNIPPETS` badge), while `:insert <name>` and `:insert-<name>` expand with `Tab` / `Shift+Tab` / `Esc` navigation.
  - Math symbol picker (`symbol_picker.go`, `Alt+Y` / `:symbol [query]`) reads `conceal.SymbolCatalog()` and opens a filterable bottom `ListPane` (`SYMBOLS` badge) inserting raw `name` in math or raw mode and `$name$` in markup or code mode.
  - Pure-Go bibliography package (`internal/typst/bib`, zero external YAML dependencies) scans `#bibliography(...)` calls in the active buffer and pinned main file (falling back to root `*.bib` files), parses BibTeX (`.bib`) and Hayagriva YAML (`.yml`, `.yaml`) entries with `(path, mtime, size)` caching, powers `:cite [query]` (`Alt+R`) via a bottom `ListPane` (`CITE` badge, inserting `@key` or `key` after `@`), and supplies offline `@` citation suggestions via `offlineCiteObserver` when `lsp.IsOffline()` is true.
  - Priority-filtered, mouse-clickable bottom bar + `Alt+G` key menu tailored to active context (Markup, Math, Code, Outline, Preview, Files, Help) with live toggle indicators and clickable diagnostic pills.
  - Bundled MIT-licensed `filemanager` plugin (`Alt+T` / `:tree`) and Typst-guarded `autoclose` plugin.
- **Invariants**:
  - Preserve `altShiftFallbackKeyEvents` (`internal/action/events.go`) so Windows ConPTY `ModAlt | ModShift` events match uppercase `Alt-M` bindings.
  - In bundled Lua plugins: close `*os.File` handles from `os.Create` (prevents Windows locks), use `LocFromVisual`, and keep `autoclose.lua` yielding on `.typ` buffers when `typst.smartPairs` is enabled.
  - Statuslines, preview footers, and the bottom bar must compact or drop low-priority items gracefully in narrow 3-pane layouts.

### 5.6 Template Gallery & Project Scaffolding (`internal/typst/templates`)

- **Interface**: `templates.Init`, `templates.SetExternalBackend`, `templates.SetRootResolver`, `templates.SetPreviewProvider`, `templates.DiscoverOffline`, `templates.ResolveByName`, `templates.ScaffoldToFile`, `templates.ScaffoldToDir`, `templates.SavePersonalTemplate`, `UniverseClient`, `TemplatePane` (`action.Pane`, `action.AuxiliaryPane`, `display.Window`), plus commands/actions (`:templates`, `:template`, `:template-save`, `ToggleTemplates` / `Alt+Shift+N`).
- **Implementation**:
  - Offline registry scans project-local (`.typx/templates/`), personal (`<ConfigDir>/templates/`), and OS Typst `@local` / cached `@preview` package directories (with a pure-Go `typst.toml` parser, never invoking `tinymist.getResources([])`).
  - `UniverseClient` loads and caches Typst Universe `@preview/index.json` only on demand (ETag + 24h TTL stripped cache) and provides a zip-slip-guarded `.tar.gz` extraction fallback into Typst's package cache directory.
  - `TemplatePane` renders grouped template sections in a `DockLeft` pane with an inline clickable search bar (`/` or `Ctrl+F`, `:templates search [query]`), relevance-ranked multi-term and fuzzy search across names, titles, descriptions, categories, keywords, disciplines, authors, and sources (with `cat:`/`tag:`/`author:`/`src:` qualifiers, match highlighting, `Tab` browse mode, and inline Typst Universe load-to-search), `cursor-line` fg-as-bg selection highlighting, 3-row footer preview, default-on debounced live visual preview in Kitty or Sixel terminals (full-res `.webp` Typst Universe thumbnail decode with `-small.webp` 404 fallback, opaque paper-white compositing, Catmull-Rom scaling via `golang.org/x/image/draw`, and SHA-256 PNG cache for personal/project templates; `v` toggle or read-only `.typ` source fallback), and async `tinymist.doInitTemplate` / `tinymist.doGetTemplateEntry` execution marshaling UI updates via `action.EnqueueUI`. It opens without requiring an active `.typ` file: applying a template or preamble into an unnamed editable buffer sets `filetype` to `typst`, while non-Typst or read-only buffers open the result in a new `.typ` tab.
- **Invariants**:
  - Never perform network calls at startup; all Universe/package operations run off the main loop and capture `canceled := ctx.Err() == context.Canceled` before `cancel()`.
  - Never overwrite non-empty destination files or directories unless explicitly forced/confirmed, and reject symlink/parent-traversal tar entries.
  - Bind uppercase `Alt-N` only (`Alt-n` remains `micro`'s `SpawnMultiCursor`), promote the `Templates` bottom bar hint when the active `.typ` buffer or unnamed editable buffer is blank, and expose `Templates` in the generic `Alt+G` key menu.

---

## 6. Import Dependency Rules

Verified against `go list ./...`. Route new cross-module interactions through `internal/typst/features` seams rather than adding direct imports:

- **`internal/typst` (`mode.go`) & `internal/views`**: Remain stdlib-only.
- **`internal/typst/conceal`**: Imports only `internal/typst` and `internal/util` (never `internal/action` or `internal/display`, since `display` imports `conceal`).
- **`internal/lsp`, `internal/typst/preview`, `internal/typst/outline`, `internal/typst/templates`, `internal/typst/shortcuts`, and `internal/typst/bib`**: Never import one another directly; coordinate through `features.Init()` seams (`InlinePreviewHandler`, `Compiler`/discovery/root resolver, `SymbolProvider`, `JumpHandler`, `SetMainFileResolver`, `SetRootResolver`, `SetFileOpener`, `SetPicker`, `ExternalBackend`, `PreviewProvider`, `SnippetExpanderFunc`). `internal/typst/bib` uses only Go stdlib plus `internal/{action,buffer,util}` (zero external YAML dependencies), and `internal/typst/shortcuts` reads `conceal.SymbolCatalog` without importing `internal/lsp`.
- **Inherited `micro` core (`action`, `buffer`, `config`, `display`, `screen`, `views`)**: Never import `internal/lsp`, `internal/typst/features`, `bib`, `outline`, `preview`, `shortcuts`, or `templates`. The sole Typst import inside `micro` core is `internal/display` importing `internal/typst/conceal`.
