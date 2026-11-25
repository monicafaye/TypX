package templates

import (
	"errors"
	"fmt"
	"strings"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/util"
)

// Init registers Template Gallery actions, commands, default keybinding (Alt-N),
// and tab/resize/focus hooks. Safe to call multiple times (executes once).
func Init() {
	initOnce.Do(func() {
		action.RegisterAction("ToggleTemplates", NewToggleTemplatesAction())
		action.RegisterCommand("templates", NewTemplatesCmd(), templatesCompleter)
		action.RegisterCommand("template", NewTemplateCmd(), templateNameCompleter)
		action.RegisterCommand("template-save", NewTemplateSaveCmd(), nil)
		// Bind uppercase Alt-N (Alt+Shift+N) only; lowercase Alt-n belongs to micro's SpawnMultiCursor.
		bindWithFallback("Alt-N", "ToggleTemplates")

		buffer.RegisterObserver(defaultManager)
		action.RegisterPreResizeHook(defaultManager.onPreResize)
		action.RegisterPostResizeHook(defaultManager.onPostResize)
		action.RegisterTabSwitchHook(defaultManager.onTabSwitch)
		action.RegisterPaneFocusHook(defaultManager.onPaneFocus)
	})
}

func bindWithFallback(key, typxAction string) {
	defaults := action.DefaultBindings("buffer")
	if prev := defaults[key]; prev != "" {
		action.RegisterDefaultBinding("buffer", key, typxAction+"|"+prev)
		return
	}
	action.RegisterDefaultBinding("buffer", key, typxAction)
}

// NewToggleTemplatesAction returns the BufKeyAction bound to Alt+Shift+N.
func NewToggleTemplatesAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		if bp == nil || bp.Buf == nil {
			return false
		}
		tab := bp.Tab()
		if defaultManager.paneForTab(tab) != nil {
			defaultManager.CloseForTab(tab)
			return true
		}
		if !bp.Buf.IsTypst() {
			return false
		}
		defaultManager.Open(bp, true)
		return true
	}
}

// NewTemplatesCmd returns the `:templates [toggle|open|close|focus|universe|refresh]` command handler.
func NewTemplatesCmd() func(bp *action.BufPane, args []string) {
	return func(bp *action.BufPane, args []string) {
		if bp == nil || bp.Tab() == nil {
			return
		}
		tab := bp.Tab()
		sub := "toggle"
		if len(args) > 0 {
			sub = strings.ToLower(strings.TrimSpace(args[0]))
		}
		switch sub {
		case "toggle", "":
			if defaultManager.paneForTab(tab) != nil {
				defaultManager.CloseForTab(tab)
				return
			}
			defaultManager.Open(bp, true)
		case "open", "on":
			defaultManager.Open(bp, true)
		case "close", "off":
			delete(defaultManager.autoHidden, tab)
			defaultManager.CloseForTab(tab)
		case "focus":
			if tp := defaultManager.paneForTab(tab); tp != nil {
				for i, p := range tab.Panes {
					if p == tp {
						tab.SetActive(i)
						return
					}
				}
			}
			defaultManager.Open(bp, true)
		case "universe":
			defaultManager.Open(bp, true)
			defaultManager.LoadUniverseAsync(false)
		case "refresh":
			defaultManager.Open(bp, true)
			defaultManager.LoadUniverseAsync(true)
		default:
			notifyError(fmt.Sprintf("Invalid templates argument %q (use open, close, focus, universe, refresh, or toggle)", args[0]))
		}
	}
}

var templatesArgChoices = []string{"toggle", "open", "close", "focus", "universe", "refresh"}

func templatesCompleter(b *buffer.Buffer) ([]string, []string) {
	return completeFromList(b, templatesArgChoices)
}

// NewTemplateCmd returns the `:template [name] [dir|--preamble]` command handler.
func NewTemplateCmd() func(bp *action.BufPane, args []string) {
	return func(bp *action.BufPane, args []string) {
		if bp == nil || bp.Buf == nil {
			return
		}
		if len(args) == 0 {
			defaultManager.Open(bp, true)
			return
		}
		query := strings.TrimSpace(args[0])
		if query == "" {
			defaultManager.Open(bp, true)
			return
		}
		var targetDir string
		preambleOnly := false
		for _, a := range args[1:] {
			switch a {
			case "--preamble", "-p":
				preambleOnly = true
			default:
				if targetDir == "" && !strings.HasPrefix(a, "-") {
					targetDir = a
				}
			}
		}

		defaultManager.preloadCachedUniverse()
		all := defaultManager.allTemplates(bp)
		tmpl, ok := ResolveByName(all, query)
		if !ok {
			if strings.HasPrefix(query, "@preview/") || strings.HasPrefix(query, "@local/") {
				ns, name, ver, valid := ParsePackageSpec(query)
				if !valid {
					notifyError("Invalid package spec: " + query)
					return
				}
				src := SourceUniverse
				if ns == "local" {
					src = SourceLocalPkg
				}
				tmpl = Template{
					ID:          query,
					Name:        name,
					Title:       query,
					Version:     ver,
					Source:      src,
					IsMultiFile: true,
					Entrypoint:  "main.typ",
					PackageSpec: query,
				}
			} else {
				notifyError("Unknown template: " + query)
				return
			}
		}

		if preambleOnly {
			defaultManager.InsertPreamble(bp, bp.Tab(), tmpl)
			return
		}
		if targetDir != "" {
			defaultManager.ScaffoldProjectAsync(bp.Tab(), bp, tmpl, targetDir)
			return
		}
		defaultManager.ApplyTemplate(bp, bp.Tab(), tmpl)
	}
}

func templateNameCompleter(b *buffer.Buffer) ([]string, []string) {
	defaultManager.preloadCachedUniverse()
	var bp *action.BufPane
	if action.Tabs != nil {
		if mt := action.MainTab(); mt != nil {
			bp = mt.CurPane()
		}
	}
	all := defaultManager.allTemplates(bp)
	seen := make(map[string]bool, len(all)*2)
	var choices []string
	addChoice := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		choices = append(choices, s)
	}
	for _, t := range all {
		addChoice(t.Name)
		addChoice(t.ID)
		if t.PackageSpec != "" {
			addChoice(t.PackageSpec)
		}
	}
	return completeFromList(b, choices)
}

// NewTemplateSaveCmd returns the `:template-save <name> [--preamble] [--force]` handler.
func NewTemplateSaveCmd() func(bp *action.BufPane, args []string) {
	return func(bp *action.BufPane, args []string) {
		if bp == nil || bp.Buf == nil {
			return
		}
		var name string
		preambleOnly := false
		force := false
		for _, arg := range args {
			switch arg {
			case "--preamble", "-p":
				preambleOnly = true
			case "--force", "-f":
				force = true
			default:
				if strings.HasPrefix(arg, "-") {
					notifyError("Unknown flag for template-save: " + arg)
					return
				}
				if name == "" {
					name = arg
				}
			}
		}

		content := string(bp.Buf.Bytes())
		saveWithName := func(tmplName string) {
			tmplName = strings.TrimSpace(tmplName)
			if tmplName == "" {
				return
			}
			saved, err := SavePersonalTemplate(config.ConfigDir, tmplName, content, preambleOnly, force)
			if err != nil {
				if errors.Is(err, ErrTargetExists) && !force && action.InfoBar != nil {
					action.InfoBar.YNPrompt("Overwrite existing template "+tmplName+"? (y,n,esc)", func(yes, canceled bool) {
						if canceled || !yes {
							return
						}
						overwritten, owErr := SavePersonalTemplate(config.ConfigDir, tmplName, content, preambleOnly, true)
						if owErr != nil {
							notifyError("Failed to save template: " + owErr.Error())
							return
						}
						defaultManager.refreshAllPanes()
						notifyMessage("Saved template: " + overwritten)
					})
					return
				}
				notifyError("Failed to save template: " + err.Error())
				return
			}
			defaultManager.refreshAllPanes()
			notifyMessage("Saved template: " + saved)
		}

		if name != "" {
			saveWithName(name)
			return
		}
		if action.InfoBar == nil {
			return
		}
		prompt := "Save template name: "
		if preambleOnly {
			prompt = "Save preamble template name: "
		}
		action.InfoBar.Prompt(prompt, "", "TemplateSaveName", nil, func(resp string, canceled bool) {
			if canceled {
				return
			}
			saveWithName(resp)
		})
	}
}

func completeFromList(b *buffer.Buffer, choices []string) ([]string, []string) {
	if b == nil {
		return nil, nil
	}
	c := b.GetActiveCursor()
	if c == nil {
		return nil, nil
	}
	input, argstart := b.GetArg()
	lower := strings.ToLower(input)

	var suggestions []string
	for _, choice := range choices {
		if strings.HasPrefix(strings.ToLower(choice), lower) {
			suggestions = append(suggestions, choice)
		}
	}
	if len(suggestions) == 0 {
		return nil, nil
	}

	prefixChars := util.Max(0, c.X-argstart)
	completions := make([]string, len(suggestions))
	for i, s := range suggestions {
		completions[i] = util.SliceEndStr(s, prefixChars)
	}
	return completions, suggestions
}
