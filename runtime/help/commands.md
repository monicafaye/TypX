# Command bar

The command bar is opened by pressing `Ctrl-e`. It is a single-line buffer,
meaning that all keybindings from a normal buffer are supported (as well
as mouse and selection).

When running a command, you can use extra syntax that micro will expand before
running the command. To use an argument with a space in it, put it in
quotes. The command bar parser uses the same rules for parsing arguments that
`/bin/sh` would use (single quotes, double quotes, escaping). The command bar
does not look up environment variables.

# Commands

TypX provides the following commands that can be executed at the command-bar
by pressing `Ctrl-e` and entering the command. Arguments are placed in single
quotes here but these are not necessary when entering the command in TypX.

## TypX / Typst commands

See `> help typst` for a walkthrough of Typst authoring workflows.

* `preview ['inline'|'kitty'|'sixel'|'browser'|'focus'|'invert'|'trim'|'stop']`:
   toggles live preview for the active `.typ` buffer (default key: `Alt-p`).
   Without arguments, `typst.previewMode` (`auto` by default) opens a
   right-docked in-terminal Kitty or Sixel preview when supported, otherwise
   `tinymist` browser preview with debounced cursor synchronization. Subcommands
   toggle the `inline` pane or `browser` preview, open or switch the inline pane
   between `kitty` and `sixel` graphics, `focus` the inline pane, toggle
   `invert` (dark mode) or `trim` (margins), or `stop` any active preview.

* `outline ['on'|'off'|'open'|'close'|'toggle']`: toggles or controls the
   docked Document Outline sidebar (default key: `Alt-o`).

* `templates ['toggle'|'open'|'close'|'focus'|'universe'|'refresh']`: toggles
   or controls the docked Template Gallery sidebar in any buffer, including
   unnamed blank startup buffers without an open `.typ` file (default key:
   `Alt-N` / `Alt-Shift-n`).

* `template ['name'] ['dir'|--preamble]`: opens the Template Gallery if no
   name is given, applies a personal/project template, or scaffolds a
   multi-file/`@local`/`@preview` template into a project folder.

* `template-save 'name' [--preamble] [--force]`: saves the active buffer (or
   only the preamble before the first top-level `=` heading with `--preamble`)
   into `<config-dir>/templates/<name>.typ`.

* `conceal ['on'|'off'|'true'|'false']`: toggles or sets live Unicode math and
   typography concealment for the active `.typ` buffer (default key: `Alt-c`).

* `math-inline`: inserts `$|$`, wraps the active selection in `$...$`, or steps
   out of inline math (default key: `Alt-m`).

* `math-display`: inserts a multi-line display math block `$\n  |\n$`
   (default key: `Alt-M` / `Alt-Shift-m`).

* `wrap-bold`: wraps selection or inserts `*...*` (default key: `Alt-b`).

* `wrap-italic`: wraps selection or inserts `_..._` (default key: `Alt-e`).

* `heading-promote`: promotes a Typst heading by one level (`== ` -> `= `,
   default key: `Alt-Left`).

* `heading-demote`: demotes a Typst heading by one level (`= ` -> `== `,
   default key: `Alt-Right`).

* `insert ['template']`: opens the Typst snippet insert palette (default key:
   `Alt-i`), or directly expands a built-in template (`figure`, `table`, `frac`,
   `matrix`, `cases`, `code`, `callout`, `vec`, `hat`, `bar`). Each template
   is also available directly as `insert-<template>` (e.g., `insert-figure`,
   `insert-table`, `insert-frac`, `insert-matrix`, `insert-cases`,
   `insert-code`).

* `lsp-complete`: triggers `tinymist` LSP autocompletion at the cursor
   (default key: `Ctrl-Space`).

* `hover`: requests `tinymist` LSP hover documentation at the cursor
   (default key: `Alt-k`).

* `definition`: jumps to the definition of the symbol or citation at the cursor
   via `tinymist` (supports `.typ`, `.bib`, and read-only `@preview` packages;
   opens a bottom picker when multiple locations match; default key: `F12`).

* `references`: lists references to the symbol at the cursor across the
   workspace in the bottom `ListPane` (`/` filters, `Enter` jumps; default key:
   `Shift-F12`).

* `jump-back`: returns to the previous cursor location in the tab's definition
   and reference jump stack (default key: `Alt-u`).

* `lsp-rename ['new']`: renames the symbol at the cursor across open buffers
   and workspace files using `prepareRename` and an `InfoBar` prompt if no name
   is supplied (default key: `F2` in `.typ` buffers).

* `code-action`: requests quickfixes, refactors, and server commands at the
   cursor or selection in a bottom `ListPane` (default key: `Alt-a`).

* `symbols ['query']`: searches workspace symbols and headings via `tinymist`
   in the bottom `ListPane` (`/` filters live, `Enter` jumps; default key:
   `Alt-s`).

* `diag-next` / `diag-prev`: jumps to the next or previous diagnostic in the
   current buffer with wrap-around (default keys: `F8`, `Shift-F8`).

* `diagnostics`: opens the interactive bottom-docked `ListPane` showing
   `tinymist` diagnostics for the active buffer and project (`Enter` jumps,
   `/` filters text, `f` toggles errors only, `Esc`/`q` closes).

* `pin ['path']`: pins `path` (or the current `.typ` buffer) as the project's
   main file for `tinymist`, live preview, and `:export` (persisted per root).

* `unpin`: clears the project's pinned main `.typ` file.

* `export ['pdf'|'png'|'svg'|'html'] ['output']`: exports the active or pinned
   `.typ` document via `tinymist compile`.

* `format`: formats the current `.typ` buffer via `tinymist` as a single undo step.

* `tinymist-status`: reports `tinymist` binary discovery path, LSP connection
   state, pinned main file, live preview URL, install hints (if missing), and
   recent stderr logs.

* `tinymist-restart`: restarts the `tinymist` LSP server subprocess.

## General editor commands

* `bind 'key' 'action'`: creates a keybinding from key to action. See the
   `keybindings` documentation for more information about binding keys.
   This command will modify `bindings.json` and overwrite any bindings to
   `key` that already exist.

* `help ['topic'] ['flags']`: opens the corresponding help topics.
   If no topic is provided opens the default help screen. If multiple topics are
   provided (separated via ` `) they are opened all as splits.
   Help topics are stored as `.md` files in the `runtime/help` directory of
   the source tree, which is embedded in the final binary.
   The `flags` are optional.
   * `-hsplit`: Opens the help topic in a horizontal split
   * `-vsplit`: Opens the help topic in a vertical split

   The default split type is defined by the global `helpsplit` option.

* `save ['filename']`: saves the current buffer. If the file is provided it
   will 'save as' the filename.

* `quit`: quits micro.

* `goto 'line[:col]'`: goes to the given absolute line (and optional column)
   number.
   A negative number can be passed to go inward from the end of the file.
   Example: -5 goes to the 5th-last line in the file.

* `jump 'line[:col]'`: goes to the given relative number from the current
   line (and optional absolute column) number.
   Example: -5 jumps 5 lines up in the file, while (+)3 jumps 3 lines down.

* `replace 'search' 'value' ['flags']`: This will replace `search` with `value`.
   The `flags` are optional. Possible flags are:
   * `-a`: Replace all occurrences at once
   * `-l`: Do a literal search instead of a regex search

   Note that `search` must be a valid regex (unless `-l` is passed). If one
   of the arguments does not have any spaces in it, you may omit the quotes.

   In case the search is done non-literal (without `-l`), the 'value'
   is interpreted as a template:
   * `$3` or `${3}` substitutes the submatch of the 3rd (capturing group)
   * `$foo` or `${foo}` substitutes the submatch of the (?P<foo>named group)
   * You have to write `$$` to substitute a literal dollar.

* `replaceall 'search' 'value'`: this will replace all occurrences of `search`
   with `value` without user confirmation.

   See `replace` command for more information.

* `set 'option' 'value'`: sets the option to value. See the `options` help
   topic for a list of options you can set. This will modify your
   `settings.json` with the new value.

* `setlocal 'option' 'value'`: sets the option to value locally (only in the
   current buffer). This will *not* modify `settings.json`.

* `toggle 'option'`: toggles the option. Only works with options that accept
   exactly two values. This will modify your `settings.json` with the new value.

* `togglelocal 'option'`: toggles the option locally (only in the
   current buffer). Only works with options that accept exactly two values.
   This will *not* modify `settings.json`.

* `reset 'option'`: resets the given option to its default value.

* `show 'option'`: shows the current value of the given option.

* `showkey 'key'`: Show the action(s) bound to a given key. For example
   running `> showkey Ctrl-c` will display `Copy`.

* `run 'sh-command'`: runs the given shell command in the background. The
   command's output will be displayed in one line when it finishes running.

* `vsplit ['filename']`: opens a vertical split with `filename`. If no filename
   is provided, a vertical split is opened with an empty buffer. If multiple
   files are provided (separated via ` `) they are opened all as splits.

* `hsplit ['filename']`: same as `vsplit` but opens a horizontal split instead
   of a vertical split.

* `tab ['filename']`: opens the given file in a new tab. If no filename
   is provided, a tab is opened with an empty buffer. If multiple files are
   provided (separated via ` `) they are opened all as tabs.

* `tabmove '[-+]n'`: Moves the active tab to another slot. `n` is an integer.
   If `n` is prefixed with `-` or `+`, then it represents a relative position
   (e.g. `tabmove +2` moves the tab to the right by `2`). If `n` has no prefix,
   it represents an absolute position (e.g. `tabmove 2` moves the tab to slot `2`).

* `tabswitch 'tab'`: This command will switch to the specified tab. The `tab`
   can either be a tab number, or a name of a tab.

* `textfilter 'sh-command'`: filters the current selection through a shell
   command as standard input and replaces the selection with the stdout of
   the shell command.  For example, to sort a list of numbers, first select
   them, and then execute `> textfilter sort -n`.

* `log`: opens a log of all messages and debug statements.

* `plugin list`: lists all installed plugins.

* `plugin install 'pl'`: install a plugin.

* `plugin remove 'pl'`: remove a plugin.

* `plugin update ['pl']`: update a plugin (if no arguments are provided
   updates all plugins).

* `plugin search 'pl'`: search available plugins for a keyword.

* `plugin available`: show available plugins that can be installed.

* `reload`: reloads all runtime files (settings, keybindings, syntax files,
   colorschemes, plugins). All plugins will be unloaded by running their
   `deinit()` function (if it exists), and then loaded again by calling the
   `preinit()`, `init()` and `postinit()` functions (if they exist).

* `cd 'path'`: Change the working directory to the given `path`.

* `pwd`: Print the current working directory.

* `open 'filename'`: Open a file in the current buffer.

* `reopen`: Reopens the current file from disk.

* `retab`: Replaces all leading tabs with spaces or leading spaces with tabs
   depending on the value of `tabstospaces`.

* `raw`: micro will open a new tab and show the escape sequence for every event
   it receives from the terminal. This shows you what micro actually sees from
   the terminal and helps you see which bindings aren't possible and why. This
   is most useful for debugging keybindings.

* `term ['exec']`: Open a terminal emulator running the given executable. If no
   executable is given, this will open the default shell in the terminal
   emulator.

---

The following commands are provided by the default plugins:

* `lint`: Lint the current file for errors.
* `comment`: automatically comment or uncomment current selection or line.
