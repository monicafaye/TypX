package features

import (
	"context"
	"fmt"
	"image"
	"runtime"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/display"
	"github.com/monicafaye/TypX/internal/lsp"
	"github.com/monicafaye/TypX/internal/typst"
	"github.com/monicafaye/TypX/internal/typst/conceal"
	"github.com/monicafaye/TypX/internal/typst/outline"
	"github.com/monicafaye/TypX/internal/typst/preview"
	"github.com/monicafaye/TypX/internal/typst/shortcuts"
	"github.com/monicafaye/TypX/internal/typst/templates"
	"github.com/monicafaye/TypX/internal/util"
)

var (
	initOnce sync.Once

	statusCacheMu sync.Mutex
	wordCache     = make(map[*buffer.SharedBuffer]wordCacheEntry)
	modeCache     = make(map[*buffer.SharedBuffer]modeCacheEntry)
)

type wordCacheEntry struct {
	version uint64
	words   int
}

type modeCacheEntry struct {
	version uint64
	line    int
	col     int
	mode    typst.Mode
	states  []typst.LineState
}

type cacheEvictObserver struct {
	buffer.NopObserver
}

func (cacheEvictObserver) OnBufferClose(b *buffer.Buffer) {
	if b == nil || b.Shared() || b.SharedBuffer == nil {
		return
	}
	statusCacheMu.Lock()
	delete(wordCache, b.SharedBuffer)
	delete(modeCache, b.SharedBuffer)
	statusCacheMu.Unlock()
}

// Init registers TypX's Phase 2 Typst-native features (actions, commands,
// default keybindings, smart-pair hooks, buffer observers, and statusline
// providers). Called on the main goroutine during startup before
// action.InitBindings, action.InitCommands, and LoadInput.
func Init() {
	initOnce.Do(func() {
		// 1. Wire internal/typst.DetectModeAt as the canonical mode detector for shortcuts.
		shortcuts.SetModeDetector(func(lines [][]byte, line, runeCol int) shortcuts.Mode {
			charCol := runeCol
			if line >= 0 && line < len(lines) {
				charCol = runeColToCharCol(lines[line], runeCol)
			}
			return shortcuts.Mode(typst.DetectModeAt(typst.SliceSource(lines), line, charCol))
		})

		// 2. Register concealment toggle action, :conceal command, and Alt-c binding.
		action.RegisterAction("ToggleConceal", NewToggleConcealAction())
		action.RegisterCommand("conceal", NewConcealCmd(), concealCompleter)
		action.RegisterDefaultBindingWithFallback("buffer", "Alt-c", "ToggleConceal")

		// 3. Register LyX-style actions, commands, templates, and bindings.
		shortcuts.RegisterAll()

		// 4. Hook Typst-aware smart pairing, list continuation, and snippet Escape into editing actions.
		action.RegisterRuneHook(shortcuts.HandleTypstRune)
		action.RegisterPreActionHook("Backspace", shortcuts.HandleTypstBackspace)
		action.RegisterPreActionHook("InsertNewline", shortcuts.HandleTypstNewline)
		action.RegisterPreActionHook("Escape", shortcuts.HandleSnippetEscape)
		lsp.SetSnippetExpander(shortcuts.ExpandSnippetReplacing)

		// 5. Register Phase 2 statusline providers, bottom bar provider, and cache eviction observers.
		display.SetStatusInfoFn("typst.mode", modeStatusInfo)
		display.SetStatusInfoFn("typst.words", wordCountStatusInfo)
		display.SetBottomBarProvider(buildBottomBarContext)
		buffer.RegisterObserver(cacheEvictObserver{})
		buffer.RegisterObserver(shortcuts.SnippetEvictObserver())

		// 6. Wire Phase 4 Document Outline sidebar with optional tinymist documentSymbol enrichment.
		outline.SetSymbolProvider(func(b *buffer.Buffer, cb func(version uint64, symbols []outline.Symbol)) bool {
			return lsp.RequestDocumentSymbols(b, func(ver uint64, syms []lsp.DocumentSymbol) {
				cb(ver, convertLSPSymbols(syms))
			})
		})
		outline.SetJumpHandler(syncPreviewToOutlineItem)
		outline.Init()

		// 7. Wire native in-terminal continuous Sixel preview pane.
		preview.SetCompiler(&preview.TinymistCompiler{
			DiscoverFn: func(cfg string) (string, error) {
				res := lsp.Discover(cfg)
				if res.Path == "" {
					if res.ConfiguredMissing {
						return "", fmt.Errorf("configured typst.tinymistPath not found: %s", cfg)
					}
					return "", fmt.Errorf("tinymist binary not found — install with: %s", lsp.PrimaryInstallHint(runtime.GOOS))
				}
				return res.Path, nil
			},
			ConfigPath: func() string {
				if v, ok := config.GetGlobalOption("typst.tinymistPath").(string); ok {
					return v
				}
				return ""
			},
		})
		preview.SetRootResolver(lsp.FindProjectRoot)
		preview.SetMainFileResolver(func(string) string { return lsp.PinnedMain() })
		preview.Init()
		lsp.SetInlinePreviewHandler(inlinePreviewAdapter{})
		lsp.SetPinChangeHandler(func(string) { preview.NotifyMainFileChanged() })

		// 8. Wire Template Gallery & scaffolding with optional tinymist template RPCs
		// and live in-terminal Sixel visual preview.
		templates.SetExternalBackend(lspTemplateAdapter{})
		templates.SetRootResolver(lsp.FindProjectRoot)
		templates.SetPreviewProvider(previewTemplateAdapter{})
		templates.Init()
	})
}

type previewTemplateAdapter struct{}

func (previewTemplateAdapter) SupportsVisual() bool {
	return preview.SupportsInline()
}

func (previewTemplateAdapter) OpenLoading(tab *action.Tab, bp *action.BufPane, title string) bool {
	if bp == nil && tab != nil {
		bp = tab.CurPane()
	}
	if bp == nil {
		return false
	}
	_, ok := preview.OpenForOverride(bp, title)
	return ok
}

func (previewTemplateAdapter) ShowImages(tab *action.Tab, title string, imgs []*image.RGBA) {
	if pp := preview.PaneForTab(tab); pp != nil {
		pp.ShowOverrideImages(title, imgs)
	}
}

func (previewTemplateAdapter) ShowError(tab *action.Tab, title, errMsg string) {
	if pp := preview.PaneForTab(tab); pp != nil {
		pp.ShowOverrideError(title, errMsg)
	}
}

func (previewTemplateAdapter) Clear(tab *action.Tab) {
	preview.ClearOverrideForTab(tab)
}

func (previewTemplateAdapter) CompileTypst(ctx context.Context, filePath, rootDir string, content []byte) ([]*image.RGBA, error) {
	return preview.CompileTypstToImages(ctx, preview.CompileRequest{
		FilePath: filePath,
		RootDir:  rootDir,
		Content:  content,
		Modified: len(content) > 0,
	}, 1)
}

type lspTemplateAdapter struct{}

func (lspTemplateAdapter) InitTemplate(ctx context.Context, spec, targetDir string) (string, error) {
	return lsp.InitTemplate(ctx, spec, targetDir)
}

func (lspTemplateAdapter) GetTemplateEntry(ctx context.Context, spec string) (string, error) {
	return lsp.GetTemplateEntry(ctx, spec)
}

type inlinePreviewAdapter struct{}

func (inlinePreviewAdapter) SupportsInline() bool { return preview.SupportsInline() }
func (inlinePreviewAdapter) IsOpen(tab *action.Tab) bool {
	return preview.PaneForTab(tab) != nil
}
func (inlinePreviewAdapter) Open(bp *action.BufPane, focus bool) bool {
	_, ok := preview.Open(bp, focus)
	return ok
}
func (inlinePreviewAdapter) Toggle(bp *action.BufPane) bool {
	return preview.Toggle(bp)
}
func (inlinePreviewAdapter) ToggleProtocol(bp *action.BufPane, protocol string) bool {
	return preview.ToggleWithProtocol(bp, preview.ParseProtocol(protocol))
}
func (inlinePreviewAdapter) Close(tab *action.Tab) bool {
	return preview.CloseForTab(tab)
}
func (inlinePreviewAdapter) ToggleInvert(tab *action.Tab) bool {
	pp := preview.PaneForTab(tab)
	if pp == nil {
		return false
	}
	pp.ToggleInvert()
	return true
}
func (inlinePreviewAdapter) ToggleTrim(tab *action.Tab) bool {
	pp := preview.PaneForTab(tab)
	if pp == nil {
		return false
	}
	pp.ToggleTrimMargins()
	return true
}

func syncPreviewToOutlineItem(bp *action.BufPane, item outline.Item, allItems []outline.Item) {
	if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
		return
	}
	if tab := bp.Tab(); tab != nil {
		if pp := preview.PaneForTab(tab); pp != nil {
			entries := make([]preview.SectionEntry, len(allItems))
			for i, it := range allItems {
				entries[i] = preview.SectionEntry{
					Kind:  convertOutlineKind(it.Kind),
					Level: it.Level,
					Title: it.Title,
					Label: it.Label,
					Line:  it.Line,
				}
			}
			pp.ScrollToSection(preview.SectionTarget{
				Kind:        convertOutlineKind(item.Kind),
				Level:       item.Level,
				Title:       item.Title,
				Label:       item.Label,
				Line:        item.Line,
				TotalLines:  bp.Buf.LinesNum(),
				AllSections: entries,
			})
		}
	}
	if lsp.IsPreviewActive() {
		lsp.ScrollPreviewTo(bp.Buf, item.Line, item.Column)
	}
}

func convertOutlineKind(k outline.ItemKind) preview.SectionKind {
	switch k {
	case outline.KindFigure:
		return preview.SectionFigure
	case outline.KindLabel:
		return preview.SectionLabel
	default:
		return preview.SectionHeading
	}
}

func convertLSPSymbols(syms []lsp.DocumentSymbol) []outline.Symbol {
	if len(syms) == 0 {
		return nil
	}
	out := make([]outline.Symbol, len(syms))
	for i, s := range syms {
		pos := s.SelectionRange.Start
		if pos.Line == 0 && pos.Character == 0 && (s.Range.Start.Line != 0 || s.Range.Start.Character != 0) {
			pos = s.Range.Start
		}
		out[i] = outline.Symbol{
			Name:     s.Name,
			Kind:     s.Kind,
			Line:     pos.Line,
			Column:   pos.Character,
			Children: convertLSPSymbols(s.Children),
		}
	}
	return out
}

// NewToggleConcealAction returns the BufKeyAction for toggling math/symbol
// concealment on the active Typst buffer (returns false on non-Typst buffers
// so fallback bindings continue to work).
func NewToggleConcealAction() action.BufKeyAction {
	return func(bp *action.BufPane) bool {
		if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
			return false
		}
		applyConcealToggle(bp, nil)
		return true
	}
}

// NewConcealCmd returns the command handler for `:conceal [on|off|true|false]`.
func NewConcealCmd() func(bp *action.BufPane, args []string) {
	return func(bp *action.BufPane, args []string) {
		if bp == nil || bp.Buf == nil {
			return
		}
		if !bp.Buf.IsTypst() {
			if action.InfoBar != nil {
				action.InfoBar.Error("Command requires a Typst buffer")
			}
			return
		}
		applyConcealToggle(bp, args)
	}
}

func applyConcealToggle(bp *action.BufPane, args []string) {
	staging := make(map[string]any, 1)
	if cur, ok := bp.Buf.Settings["typst.conceal"]; ok {
		staging["typst.conceal"] = cur
	}
	enabled, err := conceal.ToggleSetting(staging, args...)
	if err != nil {
		if action.InfoBar != nil {
			action.InfoBar.Error(err.Error())
		}
		return
	}
	if err := bp.Buf.SetOptionNative("typst.conceal", enabled); err != nil {
		if action.InfoBar != nil {
			action.InfoBar.Error(err.Error())
		}
		return
	}
	if action.InfoBar != nil {
		if enabled {
			action.InfoBar.Message("Concealment enabled")
		} else {
			action.InfoBar.Message("Concealment disabled")
		}
	}
}

var concealArgChoices = []string{"on", "off", "true", "false"}

func concealCompleter(b *buffer.Buffer) ([]string, []string) {
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
	for _, choice := range concealArgChoices {
		if strings.HasPrefix(choice, lower) {
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

func modeStatusInfo(b *buffer.Buffer) string {
	if b == nil || !b.IsTypst() {
		return ""
	}
	if b.GetActiveCursor() == nil {
		return ""
	}
	return detectBufferMode(b).String() + " | "
}

func detectBufferMode(b *buffer.Buffer) typst.Mode {
	if b == nil || !b.IsTypst() {
		return typst.ModeMarkup
	}
	c := b.GetActiveCursor()
	if c == nil {
		return typst.ModeMarkup
	}
	ver := b.Version()
	var states []typst.LineState
	if b.SharedBuffer != nil {
		statusCacheMu.Lock()
		if entry, ok := modeCache[b.SharedBuffer]; ok && entry.version == ver {
			if entry.line == c.Y && entry.col == c.X {
				statusCacheMu.Unlock()
				return entry.mode
			}
			states = entry.states
		}
		statusCacheMu.Unlock()
	}

	var m typst.Mode
	if c.Y >= 0 && c.Y < b.LinesNum() {
		if len(states) == 0 {
			states = append(states, typst.LineState{})
		}
		for len(states) <= c.Y {
			idx := len(states) - 1
			next, _ := typst.ScanLineState(states[idx], b.LineBytes(idx))
			states = append(states, next)
		}
		m = typst.DetectModeAtLine(states[c.Y], b.LineBytes(c.Y), c.X)
	} else {
		m = typst.ModeMarkup
	}

	if b.SharedBuffer != nil {
		statusCacheMu.Lock()
		if len(modeCache) >= 32 {
			modeCache = make(map[*buffer.SharedBuffer]modeCacheEntry)
		}
		modeCache[b.SharedBuffer] = modeCacheEntry{
			version: ver,
			line:    c.Y,
			col:     c.X,
			mode:    m,
			states:  states,
		}
		statusCacheMu.Unlock()
	}
	return m
}

func wordCountStatusInfo(b *buffer.Buffer) string {
	if b == nil || !b.IsTypst() {
		return ""
	}
	ver := b.Version()
	if b.SharedBuffer != nil {
		statusCacheMu.Lock()
		if entry, ok := wordCache[b.SharedBuffer]; ok && entry.version == ver {
			statusCacheMu.Unlock()
			return formatWordCount(entry.words)
		}
		statusCacheMu.Unlock()
	}

	count := countBufferWords(b)

	if b.SharedBuffer != nil {
		statusCacheMu.Lock()
		if len(wordCache) >= 32 {
			wordCache = make(map[*buffer.SharedBuffer]wordCacheEntry)
		}
		wordCache[b.SharedBuffer] = wordCacheEntry{
			version: ver,
			words:   count,
		}
		statusCacheMu.Unlock()
	}
	return formatWordCount(count)
}

func formatWordCount(n int) string {
	if n == 1 {
		return "1 word | "
	}
	return fmt.Sprintf("%d words | ", n)
}

func countBufferWords(b *buffer.Buffer) int {
	total := 0
	lines := b.LinesNum()
	for i := 0; i < lines; i++ {
		line := b.LineBytes(i)
		inWord := false
		for len(line) > 0 {
			r, sz := utf8.DecodeRune(line)
			line = line[sz:]
			if unicode.IsSpace(r) {
				inWord = false
			} else if !inWord {
				inWord = true
				total++
			}
		}
	}
	return total
}

// runeColToCharCol converts a 0-based Go rune offset on line to a 0-based
// character cluster index matching util.DecodeCharacter / buffer.Loc.X.
func runeColToCharCol(line []byte, runeCol int) int {
	if runeCol <= 0 {
		return 0
	}
	charCol := 0
	runes := 0
	off := 0
	for off < len(line) && runes < runeCol {
		_, combc, size := util.DecodeCharacter(line[off:])
		if size <= 0 {
			break
		}
		runes += 1 + len(combc)
		off += size
		if runes <= runeCol {
			charCol++
		}
	}
	return charCol
}
