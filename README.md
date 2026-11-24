<p align="center">
  <img src="assets/typx-logo.svg" alt="TypX logo" width="320" />
</p>

<h1 align="center">TypX</h1>

<p align="center">
  <a href="https://github.com/monicafaye/TypX/releases"><img src="https://img.shields.io/badge/platforms-Windows%20%7C%20macOS%20%7C%20Linux-2b8a9e?style=flat-square" alt="Platforms: Windows, macOS, Linux" /></a>
  <a href="https://typst.app/"><img src="https://img.shields.io/badge/built%20for-Typst-239dad?style=flat-square" alt="Built for Typst" /></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-GPLv3-4c9a2a?style=flat-square" alt="License: GPLv3" /></a>
  <img src="https://img.shields.io/badge/go-1.23%2B-00add8?style=flat-square" alt="Go 1.23+" />
</p>

TypX (`typx`) is a terminal editor for [Typst](https://typst.app/), forked from [`zyedidia/micro`](https://github.com/zyedidia/micro). It combines live math concealment, a document outline, `tinymist` LSP integration, and side-by-side live preview in a single Go binary with standard keybindings and no plugin configuration.

<p align="center">
  <img src="assets/typx-preview.png" alt="TypX in Windows Terminal showing the Document Outline sidebar, Typst editor with live Unicode math concealment, and native in-terminal Sixel live preview" width="100%" /><br />
  <sub><em>Document Outline (<code>Alt+O</code>), live Unicode math concealment (<code>Alt+C</code>), and native in-terminal Sixel preview (<code>Alt+P</code>).</em></sub>
</p>

TypX includes:

- **Micro ergonomics.** Standard keybindings (`Ctrl+S`, `Ctrl+C`, `Ctrl+V`, `Ctrl+Z`, `Ctrl+F`), mouse support, splits, tabs, and Lua plugins work out of the box.
- **Typst syntax and themes.** The built-in `typst.yaml` grammar highlights Markup, Math, and Code modes alongside `typx-dark` and `typx-light` colorschemes and companion formats (`bibtex`, `markdown`, `yaml`, `toml`, `json`, `ini`, `patch`).
- **Cursor-aware Unicode math concealment.** Non-cursor lines display Greek letters, arrows, relations, set and logic operators, big operators, simple super/subscripts, and typographic dashes (`---`, `--`, `...`), while the active line stays unconcealed for editing. Concealment never alters the `.typ` file on disk.
- **Structural and math shortcuts.** Keybindings insert inline or display math (`Alt+M` and `Alt+Shift+M`), bold or italic wrappers (`Alt+B` and `Alt+E`), heading level changes (`Alt+Left` and `Alt+Right`), indentation-aware templates (`:insert` or `Alt+I`), and Typst-aware smart pairs.
- **Document Outline sidebar.** Press `Alt+O` or run `:outline` to toggle a docked sidebar powered by an incremental Go parser, with optional `tinymist` `textDocument/documentSymbol` enrichment, live cursor tracking, keyboard or mouse jump, and `/` filtering.
- **File Manager sidebar.** Press `Alt+T` or run `:tree` to browse directories, expand folders inline, open files in splits, and run `:touch`, `:mkdir`, `:rename`, and `:rm`.
- **In-terminal Sixel and browser preview.** Press `Alt+P` or run `:preview inline` for a right-docked Sixel preview in Sixel-capable terminals with zoom, color inversion, and margin trimming, or run `:preview browser` for an auto-reloading browser preview with cursor sync.
- **`tinymist` LSP and multi-file projects.** TypX resolves project roots from `typst.toml`, root-relative `/...` imports, `.git`, or `../` imports, and provides gutter diagnostics, `:diagnostics`, completion (`Ctrl+Space`), and hover docs (`Alt+K` or `:hover`). Pass `--no-lsp` to run offline without `tinymist`.

---

## Installation

### Prebuilt binaries

Prebuilt archives for `windows/amd64`, `darwin/arm64`, `darwin/amd64`, `linux/amd64`, and `linux/arm64` with SHA-256 checksums are published on [GitHub Releases](https://github.com/monicafaye/TypX/releases). Extract the archive and place `typx` (`typx.exe` on Windows) on your `PATH`.

### Building from source

Requires Go 1.23 or newer.

```bash
git clone https://github.com/monicafaye/TypX
cd TypX
go generate ./runtime
go build -trimpath ./cmd/typx
```

Or use `make`, which runs `go generate ./runtime` and injects version, commit hash, and compile date metadata:

```bash
make build      # builds ./typx (or typx.exe on Windows)
make install    # installs to $GOBIN or $GOPATH/bin
```

### Cross-platform builds

TypX embeds all runtime assets (syntax grammars, colorschemes, help files, and core Lua plugins) via `go:embed` and compiles with `CGO_ENABLED=0`:

```bash
go generate ./runtime

# Windows (amd64)
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o binaries/typx-windows-amd64.exe ./cmd/typx

# Linux (amd64)
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o binaries/typx-linux-amd64 ./cmd/typx

# macOS (Apple Silicon / Intel)
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o binaries/typx-darwin-arm64 ./cmd/typx
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o binaries/typx-darwin-amd64 ./cmd/typx
```

Or build all four targets at once with `make build-cross`.

---

## `tinymist` setup for LSP and live preview

TypX works offline for syntax highlighting, math concealment, structural and math shortcuts, and the Document Outline. Installing [tinymist](https://github.com/Myriad-Dreamin/tinymist) adds live diagnostics, completion, hover info, outline symbol enrichment, and live preview (both in-terminal Sixel and companion browser preview).

### Install `tinymist`

- **Windows:**
  ```powershell
  winget install Myriad-Dreamin.Tinymist
  # or
  scoop install tinymist
  # or
  cargo install --locked tinymist
  ```
- **macOS:**
  ```bash
  brew install tinymist
  # or
  cargo install --locked tinymist
  ```
- **Linux:**
  ```bash
  cargo install --locked tinymist
  # or download a prebuilt binary from https://github.com/Myriad-Dreamin/tinymist/releases
  ```

### Automatic discovery order

TypX searches for `tinymist` (`tinymist.exe` on Windows) in this order:

1. Explicit `typst.tinymistPath` in TypX `settings.json`, or `-typst.tinymistPath <path>` on the CLI.
2. `PATH` (`tinymist` or `tinymist.exe`).
3. Standard OS locations:
   - **Windows:** `%USERPROFILE%\.cargo\bin`, `%LOCALAPPDATA%\Microsoft\WinGet\Links`, `%USERPROFILE%\scoop\shims`, and installed VS Code `myriad-dreamin.tinymist` extensions.
   - **macOS:** `/opt/homebrew/bin`, `/usr/local/bin`, `~/.cargo/bin`, `~/.local/bin`, and VS Code extensions.
   - **Linux:** `~/.cargo/bin`, `~/.local/bin`, `/usr/local/bin`, `/usr/bin`, and VS Code extensions.

Verify your environment, including Sixel terminal detection and `tinymist` discovery, at any time:

```bash
typx --check
```

Inside TypX, press `Ctrl+E` and run `tinymist-status`, or `tinymist-restart` to restart the language server.

---

## Quick start and CLI usage

Open any `.typ` document with TypX:

```bash
typx paper.typ            # open with LSP, math concealment, and outline
typx --no-lsp paper.typ   # offline mode without starting tinymist
typx --preview paper.typ  # open file and start live preview
```

### CLI flags

| Flag | Description |
|---|---|
| `--check` | Verify config directory, embedded Typst syntax, Unicode and Sixel terminal capability, and `tinymist` discovery and version, then exit. |
| `--no-lsp` | Launch without starting `tinymist` (offline mode). |
| `--preview` | Start live preview on the opened `.typ` file (inline Sixel or browser per `typst.previewMode`). |
| `--version` | Show TypX version, upstream `micro` base version, commit hash, and compile date. |
| `--options` | List all configuration options and their default values. |
| `-config-dir <dir>` | Use a custom configuration directory for this session. |
| `-<option> <value>` | Override any option for this session (for example, `typx -colorscheme typx-dark paper.typ`). |
| `+LINE[:COL]` | Open buffer positioned at a specific line and optional column. |

---

## Default keybindings and commands

Press `Ctrl+E` to open the command bar (`>` prompt). All TypX keybindings apply in `.typ` buffers, falling back to standard `micro` actions in other file types, and can be customized in `bindings.json`.

### TypX and Typst shortcuts

| Feature | Command | Default Keybinding | Description |
|---|---|---|---|
| Live Preview | `:preview [inline\|browser\|focus\|invert\|trim\|stop]` | `Alt+P` | Toggle live preview (`auto` chooses in-terminal Sixel when supported, otherwise browser preview). |
| Document Outline | `:outline` | `Alt+O` | Toggle the docked Document Outline sidebar. |
| File Manager | `:tree` | `Alt+T` | Toggle the built-in file tree sidebar (`:touch`, `:mkdir`, `:rename`, `:rm`). |
| Math Concealment | `:conceal [on\|off]` | `Alt+C` | Toggle live Unicode math and symbol concealment in the active buffer. |
| Inline Math | `:math-inline` | `Alt+M` | Insert `$|$`, wrap selection in `$...$`, or exit inline math. |
| Display Math | `:math-display` | `Alt+Shift+M` | Insert a multi-line display math block `$\n  |\n$`. |
| Insert Palette | `:insert [name]` | `Alt+I` | Open the snippet template palette (`figure`, `table`, `frac`, `matrix`, `cases`, `code`, `callout`, `vec`, `hat`, `bar`). |
| Bold | `:wrap-bold` | `Alt+B` | Wrap selection or insert `*bold*` delimiters. |
| Italic | `:wrap-italic` | `Alt+E` | Wrap selection or insert `_italic_` delimiters. |
| Promote Heading | `:heading-promote` | `Alt+Left` | Decrease heading level (`== ` to `= `). |
| Demote Heading | `:heading-demote` | `Alt+Right` | Increase heading level (`= ` to `== `). |
| LSP Completion | `:lsp-complete` | `Ctrl+Space` | Trigger `tinymist` autocomplete popup. |
| LSP Hover | `:hover` | `Alt+K` | Show `tinymist` documentation or type info at cursor. |
| Diagnostics List | `:diagnostics` | None | Show all `tinymist` diagnostics for the current `.typ` buffer. |
| Tinymist Status | `:tinymist-status` | None | Display `tinymist` binary path, LSP state, preview URL, and stderr tail. |
| Restart Tinymist | `:tinymist-restart` | None | Restart the `tinymist` language server subprocess. |

Direct snippet commands are also registered for every template: `:insert-figure`, `:insert-table`, `:insert-frac`, `:insert-matrix`, `:insert-cases`, `:insert-code`, `:insert-callout`, `:insert-vec`, `:insert-hat`, and `:insert-bar`.

### In-terminal Sixel preview navigation

When the right-docked Sixel preview pane is focused via `:preview inline` or `:preview focus`:

| Key or Action | Behavior |
|---|---|
| `Up` / `Down`, `k` / `j`, `w` / `s` | Scroll up or down through pages. |
| `PgUp` / `PgDn`, `Ctrl+U` / `Ctrl+D`, `Space` | Page up or down through the document. |
| `[` / `]` or `Left` / `Right` | Jump to previous or next page. |
| `g` / `G` or `Home` / `End` | Jump to top or bottom of document. |
| `+` / `-` | Zoom preview in or out. |
| `t` | Toggle dark-mode color inversion (`typst.previewInvert`). |
| `m` | Toggle page margin trimming (`typst.previewTrimMargins`). |
| `r` | Force recompile and redraw preview. |
| `Esc`, `Enter`, `Tab`, `Ctrl+W` | Return focus to the editor pane. |
| `q` or `Alt+P` | Close the preview pane. |

### Document Outline navigation

When the Document Outline sidebar (`Alt+O`) is focused:

| Key or Action | Behavior |
|---|---|
| `Up` / `Down` or `k` / `j` | Move selection through headings, figures, and optional labels. |
| `Home` / `End` / `PgUp` / `PgDn` | Jump to top, bottom, or page through the outline. |
| `Enter` or Mouse Click | Jump the linked editor pane to that item, center the view, and return focus to the editor. |
| `/` | Filter outline entries (`Esc` clears filter, `Enter` confirms). |
| `Esc` or `Tab` | Return focus to the editor without jumping. |
| `Alt+O` or `:outline` | Close the outline sidebar. |

---

## Configuration

TypX resolves its configuration directory in this order:

1. `$TYPX_CONFIG_HOME` (if set)
2. `<os.UserConfigDir()>/typx` (for example, `%APPDATA%\typx` on Windows, `~/Library/Application Support/typx` on macOS, or `$XDG_CONFIG_HOME/typx` / `~/.config/typx` on Linux)
3. Fallback: `~/.config/typx`

### TypX options in `settings.json`

| Option | Scope | Default | Description |
|---|---|---|---|
| `typst.conceal` | local/global | `true` | Enable live Unicode math and typography concealment in `.typ` buffers. |
| `typst.concealMode` | local/global | `"line"` | Unconceal policy: `"line"` unconceals the active cursor line, and `"token"` unconceals only the token under the cursor. |
| `typst.smartPairs` | local/global | `true` | Typst-aware smart pairing, skip-over, and pair-backspace for `$`, `[`, `(`, `{`, `"`. |
| `typst.lspEnabled` | global | `true` | Enable `tinymist` LSP integration (`false` is equivalent to `--no-lsp`). |
| `typst.tinymistPath` | global | `""` | Explicit path to `tinymist` or `tinymist.exe` (empty uses auto-discovery). |
| `typst.debounceMs` | global | `150` | Debounce interval in milliseconds for `textDocument/didChange` updates. |
| `typst.previewMode` | global | `"auto"` | Live preview mode: `"auto"` (inline Sixel when supported, else browser), `"inline"`, or `"browser"`. |
| `typst.previewWidth` | global | `48` | Preferred width percentage for the docked in-terminal Sixel preview pane. |
| `typst.previewPPI` | global | `144` | Render resolution (PPI) for in-terminal Sixel preview pages. |
| `typst.previewInvert` | global | `false` | Invert colors in the in-terminal Sixel preview for dark terminals. |
| `typst.previewTrimMargins` | global | `false` | Trim excess outer page margins in the in-terminal Sixel preview. |
| `typst.previewOpenBrowser` | global | `true` | Open the local preview URL in the OS browser when browser preview starts. |
| `typst.outlineWidth` | global | `30` | Preferred width in columns for the Document Outline sidebar (auto-hides if terminal width `< 50`). |
| `typst.outlineShowLabels` | global | `false` | Include standalone `<label>` anchors in the Document Outline alongside headings and figures. |

Example `settings.json`:

```json
{
  "colorscheme": "typx-dark",
  "typst.conceal": true,
  "typst.concealMode": "line",
  "typst.previewMode": "auto",
  "typst.previewInvert": true,
  "typst.outlineWidth": 32,
  "typst.outlineShowLabels": false
}
```

### Built-in `.typ` filetype defaults

When editing `.typ` files, TypX applies these defaults unless overridden globally or in an `"ft:typst"` block in `settings.json`:

- `softwrap: true`, `wordwrap: true`
- `tabstospaces: true`, `tabsize: 2`
- `autoindent: true`, `ruler: true`
- TypX statusline showing Typst lexical mode (`MARKUP`, `MATH`, `CODE`, or `RAW`), word count, and `tinymist` LSP or Preview status (`LSP: ready`, `LSP: off`, `PREVIEW: ON`).

### Colorschemes

Try TypX's dedicated themes via `Ctrl+E`:

```text
> set colorscheme typx-dark
> set colorscheme typx-light
```

---

## Terminal compatibility notes

- **Windows.** Use [Windows Terminal](https://apps.microsoft.com/detail/9n0dx20hk701), which supports Sixel graphics and truecolor, with a Unicode-capable font such as Cascadia Mono, JetBrains Mono, Iosevka, or JuliaMono for full math glyph rendering.
- **macOS.** Use a Sixel-capable terminal such as WezTerm or iTerm2 for in-terminal preview (other terminals fall back to browser preview). Configure the Option key to act as Meta/Alt so `Alt+*` shortcuts work:
  - In iTerm2, open Preferences, Profiles, Keys, and set Left Option Key to `Esc+`.
  - In Terminal.app, open Preferences, Profiles, Keyboard, and enable Use Option as Meta key.
- **Linux.** Sixel preview works in terminals such as foot, WezTerm, Konsole, and xterm with Sixel enabled. Install `wl-clipboard` on Wayland, or `xclip` or `xsel` on X11, for system clipboard integration.

---

## Built-in documentation

Inside TypX, press `Ctrl+E` and run `help` or open a specific topic:

- `> help typst` opens the TypX authoring guide covering `tinymist`, `:preview`, `:outline`, `:conceal`, shortcuts, and options.
- `> help filemanager` documents the built-in file tree sidebar commands, keybindings, and options.
- `> help defaultkeys` lists default TypX and editor keybindings.
- `> help commands` lists all command-bar commands.
- `> help options` lists all global, local, and `typst.*` configuration options.
- `> help colors` lists colorschemes including `typx-dark`, `typx-light`, and built-in themes.
- `> help keybindings` explains how to customize `bindings.json` and bindable actions.
- `> help tutorial` walks through basic configuration.

---

## Architecture and contributing

- [ARCHITECTURE.md](ARCHITECTURE.md) describes the system diagram, package layout, and subsystem designs, including the concealment coordinate mapper, `tinymist` JSON-RPC client, Sixel renderer, and incremental outline parser.
- [AGENTS.md](AGENTS.md) lists build and test commands (`go generate ./runtime`, `go test ./...`, `CGO_ENABLED=1 go test -race ./...`) and architectural invariants.

---

## Credits and license

TypX is licensed under the [GNU General Public License v3.0](LICENSE).

TypX is derived from [zyedidia/micro](https://github.com/zyedidia/micro) (`v2.0.15`), whose original code is licensed under the MIT License in [LICENSE-MICRO](LICENSE-MICRO), and uses [tinymist](https://github.com/Myriad-Dreamin/tinymist) for Typst language intelligence and live preview. See [LICENSE-THIRD-PARTY](LICENSE-THIRD-PARTY) for third-party dependency licenses.
