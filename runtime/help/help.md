# TypX help text

TypX (`typx`) is a standalone, lightweight terminal editor for Typst, forked
from `micro` and inspired by LyX's WYSIWYM authoring workflow.

TypX can be controlled by commands entered on the command bar, or with
keybindings. To open the command bar, press `Ctrl-e`: the `>` prompt will
display. From now on, when the documentation shows a command to run (such as
`> help`), press `Ctrl-e` and type the command followed by enter.

For Typst-specific authoring features (`tinymist`, `:preview`, `:outline`,
`:conceal`, LyX shortcuts, and `typst.*` options), run `> help typst`.
For a list of the default keybindings, run `> help defaultkeys`.
For more information on keybindings, see `> help keybindings`.
To toggle a short list of important keybindings, press `Alt-g`.

## Quick-start

To quit, press `Ctrl-q`. Save by pressing `Ctrl-s`. Press `Ctrl-e`, as previously
mentioned, to start typing commands. To see which commands are available, at the
prompt, press tab, or view the help topic with `> help commands`.

Key Typst authoring shortcuts in `.typ` buffers:

* `Alt-p` (`> preview`): Toggle `tinymist` live browser preview
* `Alt-o` (`> outline`): Toggle the docked Document Outline sidebar
* `Alt-c` (`> conceal`): Toggle live Unicode math concealment
* `Alt-m` / `Alt-M`: Insert inline (`$...$`) or display math block
* `Alt-i` (`> insert`): Open the LyX-style template insert palette
* `Ctrl-Space` / `Alt-k`: `tinymist` completion and hover documentation

Move the cursor around with the mouse or with the arrow keys. Enter text simply
by pressing character keys.

If you want a Typst-tuned theme, try `> set colorscheme typx-dark` or
`> set colorscheme typx-light`. You can press tab to see all available
colorschemes, or see more information with `> help colors`.

Press `Ctrl-w` to move between splits, and type `> vsplit filename` or
`> hsplit filename` to open a new split.

## Accessing more help

TypX has a built-in help system which can be accessed with the `> help` command.

To view help for the various available topics, press `Ctrl-e` to access command
mode and type in `> help` followed by a topic. Typing just `> help` will open
this page.

Here are the available help topics:

* `typst`: TypX's Typst authoring guide (`tinymist`, `:preview`, `:outline`,
   `:conceal`, LyX shortcuts, templates, and `typst.*` options)
* `tutorial`: A brief tutorial which gives an overview of all the other help
   topics
* `keybindings`: Gives a full list of the default keybindings as well as how to
   rebind them
* `defaultkeys`: Gives a more straight-forward list of the hotkey commands and
   what they do
* `commands`: Gives a list of all the commands and what they do
* `options`: Gives a list of all the options you can customize
* `plugins`: Explains how TypX's Lua plugin system works and how to create your
   own plugins
* `colors`: Explains colorschemes and syntax highlighting (`typx-dark`,
   `typx-light`, and custom themes)

For example, to open the Typst authoring guide you would run `> help typst`.
