# AGENTS.md

Guidance for AI coding agents and contributors working in **TypX** (`github.com/monicafaye/TypX`).

TypX is a standalone Go terminal editor for [Typst](https://typst.app/), forked from `zyedidia/micro` `v2.0.15`. See [ARCHITECTURE.md](ARCHITECTURE.md) for system diagrams and subsystem details.

## Build, Generate & Test Commands

```bash
# Always regenerate embedded syntax headers after editing runtime/syntax/*.yaml
go generate ./runtime

# Build binary (CGO_ENABLED=0 works cross-platform)
go build -trimpath ./cmd/typx

# Run full unit + E2E test suite
go test ./...

# Run race detector (requires CGO / C toolchain)
CGO_ENABLED=1 go test -race ./...
```

- `*.hdr` files in `runtime/syntax/` are generated and `.gitignore`d, yet embedded via `//go:embed` in `runtime/runtime.go`. If you add, remove, or modify any `runtime/syntax/*.yaml`, run `go generate ./runtime` before building or testing.
- Cross-platform builds (`windows/amd64`, `linux/amd64`, `darwin/amd64`, `darwin/arm64`) must remain `CGO_ENABLED=0`-clean.

## Repository Layout

- `cmd/typx/` — CLI flags (`--check`, `--no-lsp`, `--preview`), startup wiring, and main event loop.
- `internal/typst/` — Pure-Go Typst features:
  - `mode.go` — Lexical mode detector (`Markup`, `Math`, `Code`, `Raw`) with line-state caching.
  - `conceal/` — Cursor-aware Unicode math/typography concealment and bidirectional coordinate mapping.
  - `shortcuts/` — Context-aware math/markup wrappers, templates, and smart pairing.
  - `outline/` — Incremental heading/figure/label parser and left-docked Document Outline pane.
  - `preview/` — Native in-terminal continuous Sixel preview pane, PNG deck renderer, and terminal capability detection.
  - `features/` — Wires Typst actions, commands, keybindings, statusline providers, context-aware bottom bar (`bottombar.go`), outline, and preview.
- `internal/lsp/` — Lightweight JSON-RPC 2.0 `tinymist` LSP client, binary discovery, multi-file project root resolution, browser preview proxy, and Windows/POSIX subprocess cleanup.
- `internal/action/`, `internal/buffer/`, `internal/config/`, `internal/display/`, `internal/screen/`, `internal/views/` — Inherited `micro` core with minimal TypX hooks (including `bottombar.go` and `altShiftFallbackKeyEvents` in `events.go`).
- `runtime/` — Embedded syntax grammars, colorschemes (`typx-dark`, `typx-light`), help docs, and bundled Lua plugins (including `filemanager` and Typst-guarded `autoclose`).
- `tests/e2e/` — Headless editor & CLI end-to-end tests with a mock `tinymist` binary (`tests/e2e/cmd/mocktinymist`).

## Critical Invariants (Do Not Break)

1. **Main-Loop Thread Safety (`action.EnqueueUI`)**:
   - `micro`'s core (`buffer.Buffer`, `display`, `action.Tabs`, `action.InfoBar`, `tcell.Screen`) is **not thread-safe**.
   - Any background goroutine (LSP transport, preview compiler, outline refresh) MUST marshal UI/buffer mutations onto the main event loop via `action.EnqueueUI(func() { ... })` (`internal/action/uiqueue.go`). Never mutate buffers or draw directly from worker goroutines.
   - **Async Cancellation Check Ordering**: When running a cancelable worker task (`context.WithCancel`/`WithTimeout`), capture `canceled := ctx.Err() == context.Canceled` *before* calling `cancel()` — calling `cancel()` before the `EnqueueUI` callback inspects `ctx.Err()` silently swallows real errors as cancellations.
2. **Import Cycle Boundaries**:
   - `internal/typst` and `internal/typst/conceal` MUST NOT import `internal/action` or `internal/display` (`internal/display` imports `internal/typst/conceal`).
   - `internal/lsp` and `internal/typst/preview` must not import each other directly; they bridge via `lsp.InlinePreviewHandler` wired in `internal/typst/features/features.go`.
3. **Concealment Coordinate Integrity**:
   - Concealment is display-only and MUST NEVER mutate underlying buffer bytes.
   - `LineView.VisualToBuf` / `BufToVisual` (`internal/typst/conceal/mapper.go`) must preserve accurate mouse-click and vertical-cursor round-trips, respect word and dotted-identifier boundaries (never corrupt identifiers like `sin`, `index`, `summary`, or split `in` inside `in.not`), and leave the active cursor line/token unconcealed per `typst.concealMode`.
   - Compound dotted Typst math symbols (e.g., `in.not`, `subset.eq.not`, `inter.big`) must be registered explicitly in `internal/typst/conceal/symbols.go` for longest-match scanning.
4. **Extension Registration API**:
   - Register new TypX commands, actions, default bindings, buffer hooks, and statusline providers through `action.RegisterCommand`, `action.RegisterAction`, `action.RegisterDefaultBinding`, `buffer.RegisterObserver`, and `display.SetStatusInfoFn` rather than scattering edits across `micro` core files.
5. **Graceful Offline Degradation & Clean Teardown**:
   - TypX must work cleanly without `tinymist` installed (or with `--no-lsp`).
   - All spawned `tinymist` subprocesses must be cleanly reaped on exit (including via Windows Job Objects in `internal/lsp/job_windows.go`) and never write stray bytes to stdout/stderr that corrupt the `tcell` screen.
   - Avoid synchronous blocking `tinymist --version` probes on startup; read `ServerInfo.Version` from the LSP `initialize` response.
6. **`tinymist` Root & Protocol Requirements**:
   - Resolve the Typst project root (`FindProjectRoot`: `typst.toml` -> ancestor matching `#import "/..."` -> `.git` -> `../` depth -> file dir) *before* LSP `initialize` (via `Options.InitialFiles`) to avoid startup `rootUri` races.
   - `tinymist compile` on Windows requires `cmd.Dir = rootDir` alongside `--root` so relative ancestor paths do not trigger `entry file must be in the root directory`.
   - `tinymist.doStartPreview` (`--no-open`, `--task-id`, `--partial-rendering true`, `--root`), `tinymist.scrollPreview`, and `tinymist.doKillPreview` require the explicit task ID string as `args[0]`.
7. **Sixel Post-Show Rendering & ConPTY Ghosting Prevention**:
   - In-terminal Sixel frames are emitted through `screen.RegisterPostShowHook` after `tcell.Screen.Show()` completes so `tcell`'s cell diffing does not overwrite Sixel graphics.
   - On Windows ConPTY, `tcell`'s `Sync()` rewrites cells without issuing `ED 2` (`\x1b[2J`), so ConPTY skips unchanged blank cells and leaves stale Sixel pixels frozen. Any preview geometry shift, pane close, tab switch, or divider drag start MUST trigger `pendingSync` and wipe atomically inside a DEC Mode 2026 synchronized update (`\x1b[?2026h` + `\x1b[0m\x1b[2J` -> `screen.Screen.Sync()` -> Sixel re-emit -> `\x1b[?2026l`).
   - Defer Sixel redraws while `tab.IsResizing()` is true (emit once on mouse release), and always reset SGR (`\x1b[0m`) before clearing Sixel regions so spaces never inherit statusline colors.
8. **Cross-Platform `micro` / UI Conventions**:
   - **Colorscheme `cursor-line` slot**: `micro` stores `cursor-line`'s highlight color in the *foreground* slot — auxiliary panes (`OutlinePane`, `BufWindow`) must decompose `fg` and apply `.Background(fg)` rather than painting `cursor-line` directly as foreground.
   - **Windows `Alt+Shift` key dispatch**: Windows ConPTY/`tcell` delivers `Alt+Shift+<letter>` with `ModAlt | ModShift`, while `micro` bindings parse uppercase `Alt-M` with `ModAlt` only; preserve `altShiftFallbackKeyEvents` (`internal/action/events.go`).
   - **Bundled Lua plugins on Windows**: Close `*os.File` handles returned by `os.Create` (prevents Windows file locks on rename/delete), use `LocFromVisual` (not removed `GetMouseClickLocation`), and keep `autoclose.lua` yielding on `.typ` buffers when `typst.smartPairs` is enabled.
   - **Narrow-pane resilience**: Statuslines, preview footers, and the bottom bar must compact/drop items gracefully in 3-pane layouts instead of clipping mid-word.

## Licensing

TypX is licensed under the **MIT License** (`LICENSE`). Upstream `zyedidia/micro` code remains under its original **MIT License** (`LICENSE-MICRO`), and third-party notices live in `LICENSE-THIRD-PARTY`. Do not remove or alter existing upstream copyright notices.
