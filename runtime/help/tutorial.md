# Tutorial

This is a brief intro to TypX's configuration system that will give some
simple examples showing how to configure settings, rebind keys, and use
`init.lua` to configure TypX to your liking.

See `> help typst` for a guide to TypX's Typst-specific authoring features, and
`> help defaultkeys` for a list and explanation of the default keybindings.

### Settings

In TypX, your settings are stored in `settings.json` inside your TypX config
directory (`$TYPX_CONFIG_HOME`, `<os.UserConfigDir()>/typx`, or fallback
`~/.config/typx`), a file created the first time you save a setting. It is a
JSON file holding all modified settings and their values. To change an option,
either edit `settings.json` directly or run a command inside TypX.

Press `Ctrl-e` to go to command mode, and type `set option value` (indicated
below as `> set option value`). For example:

```
> set colorscheme catppuccin-mocha
> set typst.outlineWidth 32
```

Changes take effect immediately and are saved to `settings.json`.

You can also set options locally (only in the current buffer, without modifying
`settings.json`) using `> setlocal option value`, e.g. `> setlocal typst.conceal false`.
In `settings.json`, you can scope options per filetype or glob:

```json
{
    "colorscheme": "catppuccin-mocha",
    "ft:typst": {
        "typst.concealMode": "line",
        "tabsize": 2
    },
    "*.md": {
        "softwrap": true
    }
}
```

If you would like to know more about all the available options, see the
`options` topic (`> help options`).

### Keybindings

Keybindings are configured in `bindings.json` inside your TypX config directory.

For example, to bind `Ctrl-y` to redo and `Alt-p` to toggle preview:

```json
{
    "Ctrl-y": "Redo",
    "Alt-p": "TogglePreview|RemoveMultiCursor"
}
```

You can also bind keys interactively using `> bind key action`.

For more information about keybindings, see `> help keybindings`.

### Configuration with Lua

If you need more power than the JSON files provide, create an `init.lua` file
in your TypX config directory. This Lua script runs at startup as a single-file
plugin (`initlua`).

For example, to bind `F5` to compile the current Typst document with the
`typst` CLI:

```lua
local config = import("micro/config")
local shell = import("micro/shell")

function init()
    config.TryBindKey("F5", "lua:initlua.compile_typst", true)
end

function compile_typst(bp)
    local buf = bp.Buf
    if buf:FileType() == "typst" then
        shell.RunInteractiveShell("typst compile " .. buf.Path, true, false)
    end
end
```

For more information about plugins and the Lua API, see `> help plugins`.
