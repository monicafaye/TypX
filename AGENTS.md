# AGENTS.md

Guidance for AI coding agents and contributors working in **TypX** (`github.com/monicafaye/TypX`), a standalone Go terminal editor for [Typst](https://typst.app/) forked from `zyedidia/micro` `v2.0.15`.

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

- `*.hdr` files in `runtime/syntax/` are generated and `.gitignore`d, yet embedded via `//go:embed` in `runtime/runtime.go`. Run `go generate ./runtime` before building or testing whenever any `runtime/syntax/*.yaml` changes.
- Cross-platform builds (`windows/amd64`, `linux/amd64`, `darwin/amd64`, `darwin/arm64`) must remain `CGO_ENABLED=0`-clean.

## Top-Level Layout

- `cmd/typx/`: CLI flags (`--check`, `--new`, `--no-lsp`, `--preview`), startup wiring, main event loop, and teardown.
- `internal/typst/`: Pure-Go Typst modules (`mode.go`, `conceal/`, `shortcuts/`, `bib/`, `outline/`, `templates/`, `preview/`, and composition root `features/`).
- `internal/lsp/`: `tinymist` JSON-RPC 2.0 client, discovery, project root resolution, browser preview proxy, and subprocess cleanup.
- `internal/{action,buffer,config,display,screen,views}/`: Inherited `micro` core with narrow TypX seams.
- `runtime/`: Embedded syntax grammars, colorschemes, help docs, and bundled Lua plugins (`filemanager`, Typst-guarded `autoclose`).
- `tests/e2e/`: Headless editor and CLI end-to-end tests with `tests/e2e/cmd/mocktinymist`.

## Repo-Wide Invariants

1. **Main-Loop Thread Safety (`action.EnqueueUI`)**: `micro` core (`buffer.Buffer`, `display`, `action.Tabs`, `action.InfoBar`, `tcell.Screen`) is single-threaded. Marshal all UI/buffer mutations from worker goroutines onto the main loop via `action.EnqueueUI` (`internal/action/uiqueue.go`). For cancelable tasks, capture `canceled := ctx.Err() == context.Canceled` *before* calling `cancel()` so real errors are not swallowed as cancellations.
2. **Module Seams & Import Discipline**: Register commands, actions, bindings, input hooks, buffer observers, statusline segments, and bottom bar hints through `action.Register*`, `buffer.RegisterObserver`, and `display.Set*` rather than scattering edits across `micro` core. Wire cross-module coordination in `internal/typst/features/features.go` so `lsp`, `preview`, `outline`, `templates`, `shortcuts`, and `bib` never import one another, `internal/typst` (`mode.go`) stays stdlib-only, `bib` uses only Go stdlib plus `internal/{action,buffer,util}`, `conceal` never imports `action` or `display`, and `shortcuts` reads `conceal.SymbolCatalog` without importing `lsp`.
3. **Offline Degradation & Clean Teardown**: Keep TypX fully usable without `tinymist` (or with `--no-lsp`). Reap all spawned `tinymist` subprocesses on exit (`preview.Shutdown()` → `lsp.Shutdown()`, reinforced by Windows Job Objects) and never leak subprocess stdout/stderr onto the `tcell` screen.

## Subsystem Architecture & Invariants (`ARCHITECTURE.md`)

Read the relevant section of [ARCHITECTURE.md](ARCHITECTURE.md) before editing any of these areas (authoritative single source of truth):

- **Startup, event loop, or teardown order (`cmd/typx/`)**, see §2 for `preview.ProbeTerminal()` before `screen.Init()`, `features.Init()` before `lsp.Init()`, `DoEvent()` order, and shutdown sequence.
- **`micro` core seams or cross-module wiring (`internal/typst/features/`, `internal/{action,buffer,display,screen,views}/`)**, see §1, §3, §4, and §6 for the import graph, observer contract, and seam-to-adapter table.
- **Concealment (`internal/typst/conceal/`)**, see §5.1 for zero buffer mutation, `VisualToBuf`/`BufToVisual` round-trips, dotted symbol registration like `in.not`, parenthesized scripts, math alphabets, and `SymbolCatalog` for `:symbol`.
- **`tinymist` LSP, roots, or browser preview (`internal/lsp/`)**, see §5.2 for `FindProjectRoot` precedence, `Options.InitialFiles`, `ServerInfo.Version` without startup `--version` probe, task ID `args[0]` protocol, `ApplyWorkspaceEdit`, multi-candidate completion hooks, `documentHighlight`, `selectionRange`, and browser preview reverse sync.
- **In-terminal preview (Kitty & Sixel), ConPTY ghosting, or PDF anchor sync (`internal/typst/preview/`)**, see §5.3 for Kitty Graphics Protocol `q=2`/`a=d` cleanup, `typst.previewProtocol`, tmux passthrough, conditional parallel PNG+PDF compile, SHA-256 page and viewport caching, zoomed panning, 3-tier `ResolveSectionScrollY` and click-to-source sync, Windows `cmd.Dir = rootDir`, DEC Mode 2026 synchronized `ED 2` wipes, and `tab.IsResizing()` deferral.
- **Document Outline (`internal/typst/outline/`)**, see §5.4 for the `cursor-line` foreground-as-background convention, `EffectiveWidth` clamping, multi-file `#include` traversal with `[Project]`/`[File]` toggle, and `JumpHandler` preview sync.
- **Shortcuts, bibliography, bottom bar, or bundled Lua plugins (`internal/typst/shortcuts/`, `internal/typst/bib/`, `runtime/plugins/`)**, see §5.5 for Windows `altShiftFallbackKeyEvents`, 3-layer custom snippets, math symbol picker, pure-Go BibTeX/Hayagriva scanner and offline `@` suggestions, `os.Create` handle closing, `LocFromVisual`, `autoclose.lua` Typst yield, and narrow-pane compaction.
- **Template Gallery & project creation (`internal/typst/templates/`)**, see §5.6 for offline discovery, on-demand Universe catalog, ranked search bar and qualifiers, no startup network calls, guarded canceled preview cache writes, non-destructive writes, opening without an open `.typ` file, and uppercase `Alt-N` binding.

## Licensing

TypX is licensed under the **MIT License** (`LICENSE`). Upstream `zyedidia/micro` code remains under its original **MIT License** (`LICENSE-MICRO`), and third-party notices live in `LICENSE-THIRD-PARTY`. Preserve all existing upstream copyright notices.
