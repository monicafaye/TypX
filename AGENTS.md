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

- `cmd/typx/` — CLI flags (`--check`, `--no-lsp`, `--preview`), startup wiring, main event loop, and teardown.
- `internal/typst/` — Pure-Go Typst modules (`mode.go`, `conceal/`, `shortcuts/`, `outline/`, `preview/`, and composition root `features/`).
- `internal/lsp/` — `tinymist` JSON-RPC 2.0 client, discovery, project root resolution, browser preview proxy, and subprocess cleanup.
- `internal/{action,buffer,config,display,screen,views}/` — Inherited `micro` core with narrow TypX seams.
- `runtime/` — Embedded syntax grammars, colorschemes (`typx-dark`, `typx-light`), help docs, and bundled Lua plugins (`filemanager`, Typst-guarded `autoclose`).
- `tests/e2e/` — Headless editor & CLI end-to-end tests with `tests/e2e/cmd/mocktinymist`.

## Repo-Wide Invariants

1. **Main-Loop Thread Safety (`action.EnqueueUI`)**: `micro` core (`buffer.Buffer`, `display`, `action.Tabs`, `action.InfoBar`, `tcell.Screen`) is single-threaded. Marshal all UI/buffer mutations from worker goroutines onto the main loop via `action.EnqueueUI` (`internal/action/uiqueue.go`). For cancelable tasks, capture `canceled := ctx.Err() == context.Canceled` *before* calling `cancel()` so real errors are not swallowed as cancellations.
2. **Module Seams & Import Discipline**: Register commands, actions, bindings, input hooks, buffer observers, statusline segments, and bottom bar hints through `action.Register*`, `buffer.RegisterObserver`, and `display.Set*` rather than scattering edits across `micro` core. Wire cross-module coordination in `internal/typst/features/features.go` so `lsp`, `preview`, and `outline` never import one another, `internal/typst` stays stdlib-only, and `conceal` never imports `action` or `display`.
3. **Offline Degradation & Clean Teardown**: Keep TypX fully usable without `tinymist` (or with `--no-lsp`). Reap all spawned `tinymist` subprocesses on exit (`preview.Shutdown()` → `lsp.Shutdown()`, reinforced by Windows Job Objects) and never leak subprocess stdout/stderr onto the `tcell` screen.

## Subsystem Architecture & Invariants (`ARCHITECTURE.md`)

Read the relevant section of [ARCHITECTURE.md](ARCHITECTURE.md) before editing any of these areas (authoritative single source of truth):

- **Startup, event loop, or teardown order (`cmd/typx/`)** → §2 (`preview.ProbeTerminal()` before `screen.Init()`, `features.Init()` before `lsp.Init()`, `DoEvent()` order, shutdown sequence).
- **`micro` core seams or cross-module wiring (`internal/typst/features/`, `internal/{action,buffer,display,screen,views}/`)** → §1, §3, §4, §6 (import graph, observer contract, seam-to-adapter table).
- **Concealment (`internal/typst/conceal/`)** → §5.1 (zero buffer mutation, `VisualToBuf`/`BufToVisual` round-trips, dotted symbol registration like `in.not`).
- **`tinymist` LSP, roots, or browser preview (`internal/lsp/`)** → §5.2 (`FindProjectRoot` precedence, `Options.InitialFiles`, `ServerInfo.Version` without startup `--version` probe, task ID `args[0]` protocol).
- **Sixel preview, ConPTY ghosting, or PDF anchor sync (`internal/typst/preview/`)** → §5.3 (parallel PNG+PDF compile, 3-tier `ResolveSectionScrollY`, Windows `cmd.Dir = rootDir`, DEC Mode 2026 synchronized `ED 2` wipes, `tab.IsResizing()` deferral).
- **Document Outline (`internal/typst/outline/`)** → §5.4 (`cursor-line` foreground-as-background convention, `EffectiveWidth` clamping, `JumpHandler` preview sync).
- **Shortcuts, bottom bar, or bundled Lua plugins (`internal/typst/shortcuts/`, `runtime/plugins/`)** → §5.5 (Windows `altShiftFallbackKeyEvents`, `os.Create` handle closing, `LocFromVisual`, `autoclose.lua` Typst yield, narrow-pane compaction).

## Licensing

TypX is licensed under the **MIT License** (`LICENSE`). Upstream `zyedidia/micro` code remains under its original **MIT License** (`LICENSE-MICRO`), and third-party notices live in `LICENSE-THIRD-PARTY`. Preserve all existing upstream copyright notices.
