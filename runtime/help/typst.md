# TypX — Typst Authoring Guide

TypX (`typx`) is a terminal editor for Typst forked from `micro`, combining
standard keybindings and live math concealment with structural/math shortcuts, a
docked Document Outline sidebar, a Template Gallery, and `tinymist` LSP +
side-by-side Sixel and browser live preview.

---

## 1. Live Unicode Math Concealment (`:conceal` / `Alt-c`)

In `.typ` buffers, TypX automatically conceals Typst math tokens and safe
markup typography into Unicode glyphs on non-cursor lines:

- **Greek letters**: `alpha` -> `α`, `beta` -> `β`, `theta` -> `θ`, `pi` -> `π`,
  `Gamma` -> `Γ`, `Delta` -> `Δ`, `Lambda` -> `Λ`, `Sigma` -> `Σ`, `Omega` -> `Ω`,
  plus `.alt` variants (`epsilon.alt`, `phi.alt`, etc.).
- **Arrows & relations**: `->` -> `→`, `<-` -> `←`, `<->` -> `↔`, `=>` -> `⇒`,
  `<=>` -> `⇔`, `|->` -> `↦`, `<=` -> `≤`, `>=` -> `≥`, `!=` -> `≠`, `approx` -> `≈`,
  `equiv` -> `≡`, `in` -> `∈`, `subset.eq` -> `⊆`, `union` -> `∪`, `inter` / `sect` -> `∩`,
  `forall` -> `∀`, `exists` -> `∃`, `nabla` -> `∇`, `partial` -> `∂`, `infinity` -> `∞`.
- **Big operators**: `sum` -> `∑`, `product` -> `∏`, `integral` -> `∫`.
- **Simple super/subscripts**: `^0`..`^9`, `^i`, `^n`, `_0`..`_9`, `_i`, `_j`, `_n`.
- **Markup typography**: `---` -> `—`, `--` -> `–`, `...` -> `…`.

Key UX guarantees:
- **Unconceal-on-cursor**: The active line renders raw Typst source by default
  (`typst.concealMode: "line"`). Set `typst.concealMode` to `"token"` to
  unconceal only the token under the cursor.
- **Word-boundary safety**: Ordinary words such as `sin`, `index`, or `summary`
  are never partially concealed.
- **Non-destructive**: Concealment is display-only; saved files always contain
  exact Typst source.
- **Live toggle**: Press `Alt-c` or run `> conceal`, `> conceal on`, or
  `> conceal off`.

---

## 2. Structural & Math Shortcuts

All TypX shortcuts apply in `.typ` buffers and fall back cleanly in other files:

- `Alt-m` (`:math-inline`): Insert `$|$`, wrap selection in `$...$`, or step out
  when already inside inline math.
- `Alt-M` (`Alt-Shift-m`, `:math-display`): Insert a display math block
  `$\n  |\n$`.
- `Alt-b` (`:wrap-bold`): Wrap selection or insert `*...*`.
- `Alt-e` (`:wrap-italic`): Wrap selection or insert `_..._`.
- `Alt-Left` (`:heading-promote`): Promote heading (`== ` -> `= `).
- `Alt-Right` (`:heading-demote`): Demote heading (`= ` -> `== `).
- `Alt-i` (`:insert`): Open the interactive snippet insert palette.

### Built-in Snippets (`:insert <name>` or `:insert-<name>`)

- `figure` (`:insert-figure`) — `#figure(..., caption: []) <fig:>`
- `table` (`:insert-table`) — `#table(columns: 2, [...], [])`
- `frac` (`:insert-frac`) — `frac(, )`
- `matrix` (`:insert-matrix`) — `mat(, ; , )`
- `cases` (`:insert-cases`) — `cases(, )`
- `code` (`:insert-code`) — fenced raw code block
- `callout` (`:insert-callout`) — styled `#block(...)` callout box
- `vec`, `hat`, `bar` — math accent and vector helpers

### Typst-Aware Smart Pairing (`typst.smartPairs`)

When `typst.smartPairs` is `true` (default), TypX auto-closes `$`, `[`, `(`,
`{`, and `"`, skips over closing delimiters when retyped, and deletes empty
pairs on Backspace—while staying out of the way inside raw spans and strings.

---

## 3. Document Outline Sidebar (`:outline` / `Alt-o`)

Press `Alt-o` or run `> outline` to toggle the docked Document Outline sidebar
on the left edge of the active tab.

- Extracts `= Headings` (levels 1–6), `#figure(...)` captions, and (when
  `typst.outlineShowLabels` is `true`) standalone `<labels>`.
- Ignores heading-like lines inside fenced raw blocks and `/* ... */` comments.
- Works immediately offline via a pure-Go incremental parser, and enriches with
  `tinymist` `textDocument/documentSymbol` when LSP is connected.
- Automatically hides on narrow terminals (`< 50` columns) and restores when
  widened.

### Outline Sidebar Controls

- `Up` / `Down` or `k` / `j`: Move selection.
- `Home` / `End` / `PgUp` / `PgDn`: Jump or page through entries.
- `Enter` or Mouse Click: Jump editor to item, center view, and focus editor.
- `/`: Filter entries interactively (`Esc` clears filter, `Enter` accepts).
- `Esc` or `Tab`: Return focus to the editor without jumping.

---

## 4. Document & Project Templates (`:templates` / `Alt-N` / `--new`)

Press `Alt-Shift-n` (`Alt-N`) or run `> templates` to open the docked Template
Gallery sidebar, or start a document directly from the command line:

```
typx --new list
typx --new my-notes hw3.typ
typx --new @preview/charged-ieee my-paper
```

### Template Sources (Precedence Order)

1. **Project**: `.typx/templates/` in the active project root (`.typ` files or
   folders with `main.typ` / `typst.toml`).
2. **Personal**: `<config-dir>/templates/` (supports optional leading
   `// @title:`, `// @desc:`, `// @category:` comments and `{{title}}`,
   `{{author}}`, `{{date}}` variables).
3. **Local & Cached**: Typst `@local` and already-cached `@preview` template
   packages discovered from Typst's package directories.
4. **Typst Universe (`@preview`)**: Loaded on demand with `u` in the Gallery or
   `> templates universe` and cached locally with ETag/TTL.

### Template Commands & Gallery Controls

- `> template [name] [dir|--preamble]`: Open Gallery, insert a personal or
  project template, scaffold a multi-file/package project, or insert just its
  preamble.
- `> template-save <name> [--preamble] [--force]`: Save the active buffer (or
  its preamble above the first `=` heading) to `<config-dir>/templates/<name>.typ`.
- Inside the Gallery: `Up`/`Down` (`k`/`j`) select, `v` toggles live visual
  preview (on by default in Kitty or Sixel terminals, or a read-only `.typ`
  source split otherwise), `/` filters, `Enter` applies/scaffolds, `p` inserts
  preamble/entrypoint in place, `n` scaffolds a new folder, `u` loads/refreshes
  Typst Universe, `Esc` returns to editor, `q` closes.

---

## 5. `tinymist` LSP & Live Preview (Kitty, Sixel & Browser)

TypX integrates with `tinymist` for diagnostics, completion, hover docs,
document symbols, and live preview, either as a docked in-terminal pane
(Kitty Graphics Protocol or Sixel) or an auto-reloading browser preview with
cursor synchronization.

### Installing `tinymist`

The TypX installer scripts (`scripts/install.ps1` on Windows and
`scripts/install.sh` on macOS and Linux) install `tinymist` automatically if it
is missing. To install `tinymist` on its own:

- On Windows, run `winget install Myriad-Dreamin.Tinymist`,
  `scoop install typst-community/tinymist`, or
  `cargo install --locked tinymist`. TypX also discovers VS Code
  `myriad-dreamin.tinymist` extension binaries automatically.
- On macOS, run `brew install tinymist` or `cargo install --locked tinymist`.
- On Linux, run
  `curl --proto '=https' --tlsv1.2 -LsSf https://github.com/Myriad-Dreamin/tinymist/releases/latest/download/tinymist-installer.sh | sh`,
  run `cargo install --locked tinymist`, or download a release binary from
  `https://github.com/Myriad-Dreamin/tinymist/releases`.

If `tinymist` is installed in a non-standard path, set:

```
> set typst.tinymistPath /path/to/tinymist
```

Run `typx --check` from your shell to verify Kitty or Sixel terminal detection
and `tinymist` discovery, or run `typx --no-lsp file.typ` for offline editing.

### LSP & Preview Commands

- `:preview [inline|kitty|sixel|browser|focus|invert|trim|stop]` (`Alt-p`):
  Toggle live preview. With no arguments, `typst.previewMode` (`"auto"` by
  default) uses the right-docked inline pane when Kitty graphics or Sixel is
  supported and otherwise launches `tinymist` browser preview (auto-opening the
  OS browser when `typst.previewOpenBrowser` is `true`). Use `inline` or
  `browser` to toggle a specific mode, `kitty` or `sixel` to open or switch the
  inline graphics protocol, `focus` to focus the inline pane, `invert` / `trim`
  to toggle dark-mode inversion or margin trimming, and `stop` to close any
  active preview.
- `:lsp-complete` (`Ctrl-Space`): Trigger `tinymist` autocompletion.
- `:hover` (`Alt-k`): Display `tinymist` hover documentation at the cursor.
- `:definition` (`F12`): Jump to the definition of the symbol or citation under
  the cursor (supports `.typ`, `.bib`, and read-only `@preview` packages; opens
  a bottom `ListPane` picker when multiple targets exist).
- `:references` (`Shift-F12`): List references across the workspace in the
  bottom `ListPane` (`/` filters live, `Enter` jumps and pushes to jump stack).
- `:jump-back` (`Alt-u`): Jump back through the tab's definition/reference jump stack.
- `:lsp-rename [new]` (`F2`): Rename the symbol under the cursor across open
  buffers and workspace files (uses `textDocument/prepareRename`).
- `:code-action` (`Alt-a`): Open available quickfixes and refactorings at the
  cursor or selection.
- `:symbols [query]` (`Alt-s`): Search workspace symbols and headings (`/`
  filters live, `Enter` jumps).
- `:diag-next` (`F8`) / `:diag-prev` (`Shift-F8`): Jump to the next or previous
  diagnostic in the current buffer (wraps around).
- `:diagnostics`: Open the interactive bottom-docked diagnostics drawer across
  the active file and workspace (`Enter` jumps, `/` filters text, `f` toggles
  errors only, `Esc`/`q` closes).
- `:pin [path]` / `:unpin`: Pin or unpin the project's main `.typ` file for
  `tinymist`, Sixel/browser live preview, and `:export` (shown as `PIN: <file>`
  in the statusline and persisted per project root).
- `:export [pdf|png|svg|html] [output]`: Export the active document (or pinned
  main file) via `tinymist compile`.
- `:format`: Format the current `.typ` buffer via `tinymist` as a single
  undoable edit (can also run automatically via `typst.formatOnSave`).
- `:tinymist-status`: Show binary path, LSP status, pinned main, preview URL,
  and stderr log.
- `:tinymist-restart`: Restart the `tinymist` subprocess.

### In-Terminal Sixel Preview Controls

When the right-docked Sixel preview pane is opened with `:preview inline` or
focused with `:preview focus` (jumping from Document Outline also syncs the
preview scroll position):

- `Up` / `Down`, `k` / `j`, `w` / `s`, or Mouse Wheel: Scroll through pages.
- `PgUp` / `PgDn`, `Ctrl-u` / `Ctrl-d`, or `Space`: Page up or down.
- `[` / `]` or `Left` / `Right`: Jump to previous or next page.
- `g` / `G` or `Home` / `End`: Jump to top or bottom of document.
- `+` / `-`: Zoom preview in or out.
- `<` / `>`: Widen or narrow the docked preview pane.
- `t`: Toggle dark-mode color inversion (`typst.previewInvert`).
- `m`: Toggle page margin trimming (`typst.previewTrimMargins`).
- `r`: Force recompile and redraw preview.
- `Esc`, `Enter`, `Tab`, or `Ctrl-w`: Return focus to the editor pane.
- `q` or `Alt-p`: Close the preview pane.

---

## 6. TypX Configuration Options (`typst.*`)

See `> help options` for the full list of editor options. TypX-specific keys:

- `typst.conceal` (default `true`): Enable Unicode math/typography concealment.
- `typst.concealMode` (default `"line"`): `"line"` or `"token"`.
- `typst.smartPairs` (default `true`): Typst-aware delimiter auto-pairing.
- `typst.keyHints` (default `true`): Show clickable context-aware shortcut hints on the bottom bar.
- `typst.lspEnabled` (default `true`): Enable `tinymist` LSP integration.
- `typst.tinymistPath` (default `""`): Custom path to `tinymist` binary.
- `typst.debounceMs` (default `150`): Debounce delay (ms) for LSP edits.
- `typst.previewMode` (default `"auto"`): Live preview mode (`"auto"`, `"inline"`, or `"browser"`).
- `typst.previewProtocol` (default `"auto"`): Inline graphics protocol (`"auto"`, `"kitty"`, or `"sixel"`).
- `typst.previewWidth` (default `48`): Preferred width percentage (`20` to `80`) for the docked preview pane.
- `typst.previewPPI` (default `144`): Render resolution in PPI (`72` to `300`) for inline preview pages.
- `typst.previewInvert` (default `false`): Invert colors in the in-terminal preview for dark terminals.
- `typst.previewTrimMargins` (default `false`): Trim excess outer page margins in the in-terminal preview.
- `typst.previewOpenBrowser` (default `true`): Auto-open preview URL in browser when browser preview starts.
- `typst.outlineWidth` (default `30`): Preferred Outline sidebar width (columns).
- `typst.outlineShowLabels` (default `false`): Show standalone `<labels>` in Outline.
- `typst.templatesWidth` (default `36`): Preferred Template Gallery sidebar width (columns).
- `typst.exportOnSave` (default `"off"`): Auto-export on explicit save (`"off"`, `"pdf"`, `"png"`, `"svg"`, `"html"`).
- `typst.formatOnSave` (default `false`): Format `.typ` buffers before explicit save (with 1s timeout fallback).
- `typst.formatter` (default `"typstyle"`): Formatter backend passed to `tinymist` (`"typstyle"`, `"typstfmt"`, `"disable"`).
- `typst.gotoOpenMode` (default `"tab"`): How `:definition` / `:diagnostics` open unopened files (`"tab"`, `"vsplit"`, `"current"`).
- `typst.listPaneHeight` (default `10`): Preferred height in rows for bottom-docked drawers (`ListPane`).

### Colorschemes

Built-in dark and light colorschemes with full Typst syntax, concealed math,
and diagnostics styling include `default` (`monokai`), `catppuccin-mocha`,
`tokyo-night`, `nord`, `rose-pine`, `kanagawa`, `everforest-dark`,
`github-dark`, `dracula-tc`, `gruvbox-tc`, `one-dark`, `solarized-tc`,
`github-light`, `catppuccin-latte`, `gruvbox-light`, `solarized-light`,
`rose-pine-dawn`, and `everforest-light`:

```
> set colorscheme catppuccin-mocha
> set colorscheme github-light
```
