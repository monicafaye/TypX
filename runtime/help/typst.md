# TypX — Typst Authoring Guide

TypX (`typx`) is a Typst-focused terminal editor built on `micro`, combining
fast terminal editing with LyX-inspired math/structure ergonomics, a docked
Document Outline sidebar, and first-class `tinymist` LSP + live browser preview.

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

## 2. LyX-Style Structural & Math Shortcuts

All TypX shortcuts apply in `.typ` buffers and fall back cleanly in other files:

- `Alt-m` (`:math-inline`): Insert `$|$`, wrap selection in `$...$`, or step out
  when already inside inline math.
- `Alt-M` (`Alt-Shift-m`, `:math-display`): Insert a display math block
  `$\n  |\n$`.
- `Alt-b` (`:wrap-bold`): Wrap selection or insert `*...*`.
- `Alt-e` (`:wrap-italic`): Wrap selection or insert `_..._`.
- `Alt-Left` (`:heading-promote`): Promote heading (`== ` -> `= `).
- `Alt-Right` (`:heading-demote`): Demote heading (`= ` -> `== `).
- `Alt-i` (`:insert`): Open the interactive template insert palette.

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

## 4. `tinymist` LSP & Companion Live Preview

TypX integrates with `tinymist` over stdio JSON-RPC 2.0 for diagnostics,
completion, hover docs, document symbols, and live browser preview.

### Installing `tinymist`

- **Windows**: `winget install Myriad-Dreamin.Tinymist`, `scoop install tinymist`,
  or `cargo install --locked tinymist` (VS Code `myriad-dreamin.tinymist`
  extension binaries are also auto-discovered).
- **macOS**: `brew install tinymist` or `cargo install --locked tinymist`.
- **Linux**: `cargo install --locked tinymist` or download a release binary from
  `https://github.com/Myriad-Dreamin/tinymist/releases`.

If `tinymist` is installed in a non-standard path, set:

```
> set typst.tinymistPath /path/to/tinymist
```

Run `typx --check` from your shell to verify discovery and version probing, or
run `typx --no-lsp file.typ` for offline editing.

### LSP & Preview Commands

- `:preview` (`Alt-p`): Start or stop `tinymist` live browser preview with
  debounced cursor synchronization. Opens the OS default browser automatically
  when `typst.previewOpenBrowser` is `true`.
- `:lsp-complete` (`Ctrl-Space`): Trigger `tinymist` autocompletion.
- `:hover` (`Alt-k`): Display `tinymist` hover documentation at the cursor.
- `:diagnostics`: Display all diagnostics for the current `.typ` buffer.
- `:tinymist-status`: Show binary path, LSP status, preview URL, and stderr log.
- `:tinymist-restart`: Restart the `tinymist` subprocess.

---

## 5. TypX Configuration Options (`typst.*`)

See `> help options` for the full list of editor options. TypX-specific keys:

- `typst.conceal` (default `true`): Enable Unicode math/typography concealment.
- `typst.concealMode` (default `"line"`): `"line"` or `"token"`.
- `typst.smartPairs` (default `true`): Typst-aware delimiter auto-pairing.
- `typst.lspEnabled` (default `true`): Enable `tinymist` LSP integration.
- `typst.tinymistPath` (default `""`): Custom path to `tinymist` binary.
- `typst.debounceMs` (default `150`): Debounce delay (ms) for LSP edits.
- `typst.previewOpenBrowser` (default `true`): Auto-open preview URL in browser.
- `typst.outlineWidth` (default `30`): Preferred Outline sidebar width (columns).
- `typst.outlineShowLabels` (default `false`): Show standalone `<labels>` in Outline.

### Recommended Colorschemes

TypX ships dedicated dark and light colorschemes tuned for Typst headings,
concealed math glyphs, and diagnostics:

```
> set colorscheme typx-dark
> set colorscheme typx-light
```
