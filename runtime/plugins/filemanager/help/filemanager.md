# Filemanager Plugin

The built-in `filemanager` plugin provides an integrated file-tree sidebar for
browsing directories and opening, creating, renaming, and deleting files without
leaving TypX.

Press **`Alt-t`** or run **`> tree`** (`Ctrl-e` -> `tree`) to toggle the file
tree sidebar.

---

## Basics

- The top line displays the current directory path.
- `..` moves up to the parent directory.
- Directories end with `/` and are highlighted using the `filemanager` syntax:
  - `+` marks a collapsed directory.
  - `-` marks an expanded directory.

---

## Options (`settings.json`)

| Option                       | Default | Description                                                          |
|------------------------------|---------|----------------------------------------------------------------------|
| `filemanager.showdotfiles`   | `true`  | Show dotfiles (hide when `false`).                                   |
| `filemanager.showignored`    | `true`  | Show `.gitignore`d files (hide when `false`, requires `git`).        |
| `filemanager.compressparent` | `true`  | Jump to parent directory when `Left` is pressed on a child file.     |
| `filemanager.foldersfirst`   | `true`  | Sort folders above files.                                            |
| `filemanager.openonstart`    | `false` | Automatically open the file tree on startup.                         |

---

## Commands and Keybindings

| Command  | Keybinding(s)              | Description                                                   | API for `bindings.json`               |
|----------|----------------------------|---------------------------------------------------------------|---------------------------------------|
| `tree`   | `Alt-t`                    | Open or close the file tree sidebar                           | `filemanager.toggle_tree`             |
| —        | `Enter`, `Tab`, `MouseLeft`| Open file in a vertical split, enter dir, or go up on `..`    | `filemanager.try_open_at_cursor`      |
| —        | `Right`                    | Expand directory inline                                       | `filemanager.uncompress_at_cursor`    |
| —        | `Left`                     | Collapse directory (or jump to parent)                        | `filemanager.compress_at_cursor`      |
| —        | `Shift-Up`                 | Jump to the item's parent directory                           | `filemanager.goto_parent_dir`         |
| —        | `Alt-{` / `Alt-}`          | Jump to previous / next directory in view                     | `filemanager.goto_prev_dir` / `goto_next_dir` |
| `rm`     | —                          | Prompt to delete the file or directory under the cursor       | `filemanager.prompt_delete_at_cursor` |
| `rename` | —                          | Rename the item under the cursor (`> rename newname`)         | `filemanager.rename_at_cursor`        |
| `touch`  | —                          | Create a new file in the selected directory (`> touch name`)  | `filemanager.new_file`                |
| `mkdir`  | —                          | Create a new directory (`> mkdir dirname`)                    | `filemanager.new_dir`                 |

Use `Ctrl-w` to switch focus between the file tree and editor splits, and
`Ctrl-q` or `Alt-t` to close the file tree.
