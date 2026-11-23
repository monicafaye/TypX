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
  - `shortcuts/` — LyX-inspired math/markup wrappers, templates, and smart pairing.
  - `outline/` — Incremental heading/figure/label parser and left-docked Document Outline pane.
  - `preview/` — Native in-terminal continuous Sixel preview pane, PNG deck renderer, and terminal capability detection.
  - `features/` — Wires Typst actions, commands, keybindings, statusline providers, outline, and preview.
- `internal/lsp/` — Lightweight JSON-RPC 2.0 `tinymist` LSP client, binary discovery, multi-file project root resolution, browser preview proxy, and Windows/POSIX subprocess cleanup.
- `internal/action/`, `internal/buffer/`, `internal/config/`, `internal/display/`, `internal/screen/`, `internal/views/` — Inherited `micro` core with minimal TypX hooks.
- `runtime/` — Embedded syntax grammars, colorschemes (`typx-dark`, `typx-light`), help docs, and core Lua plugins.
- `tests/e2e/` — Headless editor & CLI end-to-end tests with a mock `tinymist` binary (`tests/e2e/cmd/mocktinymist`).

## Critical Invariants (Do Not Break)

1. **Main-Loop Thread Safety (`action.EnqueueUI`)**:
   - `micro`'s core (`buffer.Buffer`, `display`, `action.Tabs`, `action.InfoBar`, `tcell.Screen`) is **not thread-safe**.
   - Any background goroutine (LSP transport, preview compiler, outline refresh) MUST marshal UI/buffer mutations onto the main event loop via `action.EnqueueUI(func() { ... })` (`internal/action/uiqueue.go`). Never mutate buffers or draw directly from worker goroutines.
2. **Import Cycle Boundaries**:
   - `internal/typst` and `internal/typst/conceal` MUST NOT import `internal/action` or `internal/display` (`internal/display` imports `internal/typst/conceal`).
   - `internal/lsp` and `internal/typst/preview` must not import each other directly; they bridge via `lsp.InlinePreviewHandler` wired in `internal/typst/features/features.go`.
3. **Concealment Coordinate Integrity**:
   - Concealment is display-only and MUST NEVER mutate underlying buffer bytes.
   - `LineView.VisualToBuf` / `BufToVisual` (`internal/typst/conceal/mapper.go`) must preserve accurate mouse-click and vertical-cursor round-trips, respect word boundaries (never corrupt identifiers like `sin`, `index`, `summary`), and leave the active cursor line/token unconcealed per `typst.concealMode`.
4. **Extension Registration API**:
   - Register new TypX commands, actions, default bindings, buffer hooks, and statusline providers through `action.RegisterCommand`, `action.RegisterAction`, `action.RegisterDefaultBinding`, `buffer.RegisterObserver`, and `display.SetStatusInfoFn` rather than scattering edits across `micro` core files.
5. **Graceful Offline Degradation & Clean Teardown**:
   - TypX must work cleanly without `tinymist` installed (or with `--no-lsp`).
   - All spawned `tinymist` subprocesses must be cleanly reaped on exit (including via Windows Job Objects in `internal/lsp/job_windows.go`) and never write stray bytes to stdout/stderr that corrupt the `tcell` screen.
6. **Sixel Post-Show Rendering**:
   - In-terminal Sixel frames are emitted through `screen.RegisterPostShowHook` after `tcell.Screen.Show()` completes so `tcell`'s cell diffing does not overwrite Sixel graphics.

## Licensing

TypX is licensed under **GNU GPLv3** (`LICENSE`). Upstream `zyedidia/micro` code remains under its original **MIT License** (`LICENSE-MICRO`), and third-party notices live in `LICENSE-THIRD-PARTY`. Do not remove or alter existing upstream copyright notices.
