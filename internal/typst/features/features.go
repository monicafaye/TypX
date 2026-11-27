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
	"github.com/monicafaye/TypX/internal/typst/bib"
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

	offlineCiteMu              sync.Mutex
	offlineCiteSessions        = make(map[*buffer.SharedBuffer]*offlineCiteSession)
	offlineCiteSuppressVersion = make(map[*buffer.SharedBuffer]uint64)
)

type offlineCiteSession struct {
	reqVersion    uint64
	reqLoc        buffer.Loc
	reqLinePrefix string
	completions   []string
	suggestions   []string
}

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
	clearOfflineCiteSession(b.SharedBuffer)
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
		shortcuts.SetRootResolver(lsp.FindProjectRoot)
		shortcuts.SetPicker(func(bp *action.BufPane, title, badge, initialQuery string, items []shortcuts.PickerItem) bool {
			listItems := make([]lsp.ListItem, len(items))
			for i, it := range items {
				listItems[i] = lsp.ListItem{
					Badge:     it.Badge,
					Primary:   it.Primary,
					Secondary: it.Secondary,
					OnSelect:  it.OnSelect,
				}
			}
			return openBottomPicker(bp, title, badge, initialQuery, listItems)
		})
		shortcuts.RegisterAll()

		// 4. Hook Typst-aware smart pairing, list continuation, LSP completion
		// Enter/Autocomplete/Escape, offline '@' citation Enter/Escape, and
		// snippet Escape into editing actions. Completion Escape hooks run
		// before HandleSnippetEscape so canceling completion inside a tabstop
		// does not discard the enclosing snippet session.
		action.RegisterRuneHook(shortcuts.HandleTypstRune)
		action.RegisterPreActionHook("Backspace", shortcuts.HandleTypstBackspace)
		action.RegisterPreActionHook("InsertNewline", shortcuts.HandleTypstNewline)
		lsp.SetSnippetExpander(shortcuts.ExpandSnippetReplacing)
		lsp.RegisterCompletionHooks()
		action.RegisterPreActionHook("InsertNewline", handleOfflineCiteInsertNewline)
		action.RegisterPreActionHook("Escape", handleOfflineCiteEscape)
		action.RegisterPreActionHook("Escape", shortcuts.HandleSnippetEscape)

		// 5. Register Phase 2 statusline providers, bottom bar provider, and observers.
		display.SetStatusInfoFn("typst.mode", modeStatusInfo)
		display.SetStatusInfoFn("typst.words", wordCountStatusInfo)
		display.SetBottomBarProvider(buildBottomBarContext)
		buffer.RegisterObserver(cacheEvictObserver{})
		buffer.RegisterObserver(shortcuts.SnippetEvictObserver())
		buffer.RegisterObserver(offlineCiteObserver{})

		// 6. Wire Phase 4 Document Outline sidebar with multi-file resolvers and optional tinymist enrichment.
		outline.SetMainFileResolver(func(string) string { return lsp.PinnedMain() })
		outline.SetRootResolver(lsp.FindProjectRoot)
		outline.SetFileOpener(lsp.OpenFileAt)
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
		preview.SetAnchorNeedChecker(func(tab *action.Tab) bool {
			return outline.PaneForTab(tab) != nil
		})
		preview.SetSectionsProvider(func(b *buffer.Buffer) []preview.SectionEntry {
			if b == nil {
				return nil
			}
			items := outline.ParseString(string(b.Bytes()), outline.ParseOptions{IncludeLabels: true})
			entries := make([]preview.SectionEntry, len(items))
			for i, it := range items {
				entries[i] = preview.SectionEntry{
					Kind:  convertOutlineKind(it.Kind),
					Level: it.Level,
					Title: it.Title,
					Label: it.Label,
					Line:  it.Line,
				}
			}
			return entries
		})
		preview.Init()
		lsp.SetInlinePreviewHandler(inlinePreviewAdapter{})
		lsp.SetPinChangeHandler(func(string) {
			preview.NotifyMainFileChanged()
			outline.NotifyMainFileChanged()
		})

		// 8. Wire Template Gallery & scaffolding with optional tinymist template RPCs
		// and live in-terminal Sixel visual preview.
		templates.SetExternalBackend(lspTemplateAdapter{})
		templates.SetRootResolver(lsp.FindProjectRoot)
		templates.SetPreviewProvider(previewTemplateAdapter{})
		templates.Init()

		// 9. Wire pure-Go bibliography picker (:cite / Alt-r) and resolvers.
		bib.SetMainFileResolver(func(string) string { return lsp.PinnedMain() })
		bib.SetRootResolver(lsp.FindProjectRoot)
		bib.SetPicker(func(bp *action.BufPane, title, badge, initialQuery string, items []bib.PickerItem) bool {
			if badge == "" {
				badge = "CITE"
			}
			listItems := make([]lsp.ListItem, len(items))
			for i, it := range items {
				listItems[i] = lsp.ListItem{
					Badge:     it.Badge,
					Primary:   it.Primary,
					Secondary: it.Secondary,
					OnSelect:  it.OnSelect,
				}
			}
			return openBottomPicker(bp, title, badge, initialQuery, listItems)
		})
		bib.RegisterAll()
	})
}

func openBottomPicker(bp *action.BufPane, title, badge, initialQuery string, items []lsp.ListItem) bool {
	lp := lsp.OpenListPaneWithOptions(bp, lsp.ListPaneOptions{
		Title:           title,
		StartFiltering:  true,
		InitialQuery:    initialQuery,
		EnterLabel:      "Insert",
		Badge:           badge,
		DiagnosticsMode: false,
		CloseOnSelect:   true,
	}, items)
	return lp != nil
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
func (inlinePreviewAdapter) ToggleSync(tab *action.Tab) (bool, bool) {
	pp := preview.PaneForTab(tab)
	if pp == nil {
		return false, false
	}
	return pp.ToggleSyncCursor(), true
}
func (inlinePreviewAdapter) JumpToPage(tab *action.Tab, page int) (int, int, bool) {
	pp := preview.PaneForTab(tab)
	if pp == nil {
		return 0, 0, false
	}
	return pp.JumpToPage(page)
}

func syncPreviewToOutlineItem(bp *action.BufPane, item outline.Item, allItems []outline.Item) {
	if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
		return
	}
	if item.Kind == outline.KindFile {
		return
	}
	var pp *preview.PreviewPane
	if tab := bp.Tab(); tab != nil {
		pp = preview.PaneForTab(tab)
	}
	if pp == nil && action.Tabs != nil {
		// When cross-file jumps open a new tab (default typst.gotoOpenMode="tab"),
		// locate the open preview pane accompanying the project outline.
		for _, t := range action.Tabs.List {
			if t == nil {
				continue
			}
			if cand := preview.PaneForTab(t); cand != nil {
				if outline.PaneForTab(t) != nil || lsp.PinnedMain() != "" {
					pp = cand
					break
				}
			}
		}
	}
	if pp != nil {
		entries := make([]preview.SectionEntry, 0, len(allItems))
		for _, it := range allItems {
			if it.Kind == outline.KindFile {
				continue
			}
			entries = append(entries, preview.SectionEntry{
				Kind:     convertOutlineKind(it.Kind),
				Level:    it.Level,
				Title:    it.Title,
				Label:    it.Label,
				FilePath: it.FilePath,
				Line:     it.Line,
			})
		}
		pp.ScrollToSection(preview.SectionTarget{
			Kind:        convertOutlineKind(item.Kind),
			Level:       item.Level,
			Title:       item.Title,
			Label:       item.Label,
			FilePath:    item.FilePath,
			Line:        item.Line,
			TotalLines:  bp.Buf.LinesNum(),
			AllSections: entries,
		})
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
	return detectBufferModeAt(b, c.Loc)
}

func detectBufferModeAt(b *buffer.Buffer, loc buffer.Loc) typst.Mode {
	if b == nil || !b.IsTypst() {
		return typst.ModeMarkup
	}
	ver := b.Version()
	var states []typst.LineState
	if b.SharedBuffer != nil {
		statusCacheMu.Lock()
		if entry, ok := modeCache[b.SharedBuffer]; ok && entry.version == ver {
			if entry.line == loc.Y && entry.col == loc.X {
				statusCacheMu.Unlock()
				return entry.mode
			}
			states = entry.states
		}
		statusCacheMu.Unlock()
	}

	var m typst.Mode
	if loc.Y >= 0 && loc.Y < b.LinesNum() {
		if len(states) == 0 {
			states = append(states, typst.LineState{})
		}
		for len(states) <= loc.Y {
			idx := len(states) - 1
			next, _ := typst.ScanLineState(states[idx], b.LineBytes(idx))
			states = append(states, next)
		}
		m = typst.DetectModeAtLine(states[loc.Y], b.LineBytes(loc.Y), loc.X)
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
			line:    loc.Y,
			col:     loc.X,
			mode:    m,
			states:  states,
		}
		statusCacheMu.Unlock()
	}
	return m
}

type offlineCiteObserver struct {
	buffer.NopObserver
}

func (offlineCiteObserver) OnBufferChange(b *buffer.Buffer, ev buffer.ChangeEvent) {
	if b == nil || !b.IsTypst() || b.Type.Readonly || b.Type.Scratch || b.Type.Kind != buffer.BTDefault.Kind {
		return
	}
	if b.SharedBuffer != nil {
		offlineCiteMu.Lock()
		if supVer, ok := offlineCiteSuppressVersion[b.SharedBuffer]; ok {
			delete(offlineCiteSuppressVersion, b.SharedBuffer)
			if supVer == ev.Version {
				offlineCiteMu.Unlock()
				return
			}
		}
		offlineCiteMu.Unlock()
	}
	if !lsp.IsOffline() {
		return
	}
	if autoComp, ok := config.GetGlobalOption("typst.autoComplete").(bool); ok && !autoComp {
		return
	}
	if b.NumCursors() != 1 {
		return
	}
	c := b.GetActiveCursor()
	if c == nil || c.HasSelection() {
		return
	}
	// Do not clobber active multi-candidate Tab cycling.
	if b.HasSuggestions && b.CurSuggestion >= 0 {
		return
	}

	prefix, atCharX, ok := extractOfflineCitePrefix(b.LineBytes(c.Y), c.X)
	if !ok {
		return
	}
	if detectBufferModeAt(b, buffer.Loc{X: atCharX, Y: c.Y}) != typst.ModeMarkup {
		return
	}
	comps, sugs := bib.SuggestEntries(b, prefix)
	if len(comps) == 0 || len(sugs) == 0 || len(comps) != len(sugs) {
		return
	}
	// Do not re-open suggestions immediately after an exact single-match completion.
	if len(comps) == 1 && comps[0] == "" {
		return
	}
	reqVersion := b.Version()
	reqLoc := c.Loc
	reqLinePrefix := ""
	if reqLoc.Y >= 0 && reqLoc.Y < b.LinesNum() {
		reqLinePrefix = string(b.Substr(buffer.Loc{X: 0, Y: reqLoc.Y}, reqLoc))
	}
	b.ShowSuggestions(func(*buffer.Buffer) ([]string, []string) {
		return comps, sugs
	})
	if b.HasSuggestions && b.SharedBuffer != nil {
		setOfflineCiteSession(b.SharedBuffer, &offlineCiteSession{
			reqVersion:    reqVersion,
			reqLoc:        reqLoc,
			reqLinePrefix: reqLinePrefix,
			completions:   append([]string(nil), comps...),
			suggestions:   append([]string(nil), sugs...),
		})
	} else {
		clearOfflineCiteSession(b.SharedBuffer)
	}
}

func setOfflineCiteSession(sb *buffer.SharedBuffer, sess *offlineCiteSession) {
	if sb == nil {
		return
	}
	offlineCiteMu.Lock()
	if sess == nil {
		delete(offlineCiteSessions, sb)
	} else {
		offlineCiteSessions[sb] = sess
	}
	offlineCiteMu.Unlock()
}

func getOfflineCiteSession(sb *buffer.SharedBuffer) *offlineCiteSession {
	if sb == nil {
		return nil
	}
	offlineCiteMu.Lock()
	sess := offlineCiteSessions[sb]
	offlineCiteMu.Unlock()
	return sess
}

func clearOfflineCiteSession(sb *buffer.SharedBuffer) {
	if sb == nil {
		return
	}
	offlineCiteMu.Lock()
	delete(offlineCiteSessions, sb)
	delete(offlineCiteSuppressVersion, sb)
	offlineCiteMu.Unlock()
}

func suppressOfflineCiteAtVersion(sb *buffer.SharedBuffer, ver uint64) {
	if sb == nil {
		return
	}
	offlineCiteMu.Lock()
	offlineCiteSuppressVersion[sb] = ver
	offlineCiteMu.Unlock()
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func resolveOfflineCiteSession(bp *action.BufPane) (*offlineCiteSession, int, buffer.Loc, bool) {
	if bp == nil || bp.Buf == nil || bp.Buf.SharedBuffer == nil || !bp.Buf.HasSuggestions {
		if bp != nil && bp.Buf != nil && bp.Buf.SharedBuffer != nil {
			clearOfflineCiteSession(bp.Buf.SharedBuffer)
		}
		return nil, 0, buffer.Loc{}, false
	}
	sess := getOfflineCiteSession(bp.Buf.SharedBuffer)
	if sess == nil {
		return nil, 0, buffer.Loc{}, false
	}
	b := bp.Buf
	if !slicesEqual(b.Completions, sess.completions) || !slicesEqual(b.Suggestions, sess.suggestions) {
		clearOfflineCiteSession(b.SharedBuffer)
		return nil, 0, buffer.Loc{}, false
	}
	if !b.IsTypst() || b.Type.Readonly || b.NumCursors() != 1 || bp.Cursor == nil || bp.Cursor.HasSelection() {
		clearOfflineCiteSession(b.SharedBuffer)
		b.HasSuggestions = false
		return nil, 0, buffer.Loc{}, false
	}
	if sess.reqLoc.Y < 0 || sess.reqLoc.Y >= b.LinesNum() ||
		util.CharacterCount(b.LineBytes(sess.reqLoc.Y)) < sess.reqLoc.X ||
		string(b.Substr(buffer.Loc{X: 0, Y: sess.reqLoc.Y}, sess.reqLoc)) != sess.reqLinePrefix {
		clearOfflineCiteSession(b.SharedBuffer)
		b.HasSuggestions = false
		return nil, 0, buffer.Loc{}, false
	}
	if b.CurSuggestion == -1 {
		if b.Version() != sess.reqVersion || bp.Cursor.Loc != sess.reqLoc {
			clearOfflineCiteSession(b.SharedBuffer)
			b.HasSuggestions = false
			return nil, 0, buffer.Loc{}, false
		}
		return sess, 0, sess.reqLoc, true
	}
	if b.CurSuggestion >= 0 && b.CurSuggestion < len(sess.completions) {
		suffix := sess.completions[b.CurSuggestion]
		expectedEnd := sess.reqLoc.Move(util.CharacterCountInString(suffix), b)
		if bp.Cursor.Loc != expectedEnd || string(b.Substr(sess.reqLoc, bp.Cursor.Loc)) != suffix {
			clearOfflineCiteSession(b.SharedBuffer)
			b.HasSuggestions = false
			return nil, 0, buffer.Loc{}, false
		}
		return sess, b.CurSuggestion, bp.Cursor.Loc, true
	}
	clearOfflineCiteSession(b.SharedBuffer)
	b.HasSuggestions = false
	return nil, 0, buffer.Loc{}, false
}

func handleOfflineCiteInsertNewline(bp *action.BufPane) bool {
	sess, idx, _, ok := resolveOfflineCiteSession(bp)
	if !ok {
		return false
	}
	curSel := bp.Buf.CurSuggestion
	suffix := sess.completions[idx]
	clearOfflineCiteSession(bp.Buf.SharedBuffer)
	bp.Buf.HasSuggestions = false
	if curSel < 0 && suffix != "" {
		bp.Buf.Insert(sess.reqLoc, suffix)
		suppressOfflineCiteAtVersion(bp.Buf.SharedBuffer, bp.Buf.Version())
		if bp.Cursor != nil {
			bp.Cursor.StoreVisualX()
		}
		if bp.BWindow != nil {
			bp.Relocate()
		}
	}
	bp.Buf.HasSuggestions = false
	return true
}

func handleOfflineCiteEscape(bp *action.BufPane) bool {
	sess, _, curLoc, ok := resolveOfflineCiteSession(bp)
	if !ok {
		return false
	}
	curSel := bp.Buf.CurSuggestion
	clearOfflineCiteSession(bp.Buf.SharedBuffer)
	if curSel >= 0 && sess.reqLoc != curLoc {
		bp.Buf.Remove(sess.reqLoc, curLoc)
		suppressOfflineCiteAtVersion(bp.Buf.SharedBuffer, bp.Buf.Version())
		if bp.Cursor != nil {
			bp.Cursor.GotoLoc(sess.reqLoc)
			bp.Cursor.ResetSelection()
			bp.Cursor.StoreVisualX()
		}
		if bp.BWindow != nil {
			bp.Relocate()
		}
	}
	bp.Buf.HasSuggestions = false
	return true
}

func isCiteKeyRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' || r == ':'
}

func extractOfflineCitePrefix(line []byte, charX int) (prefix string, atCharX int, ok bool) {
	if charX <= 0 || len(line) == 0 {
		return "", 0, false
	}
	type charCluster struct {
		r        rune
		hasCombc bool
		byteEnd  int
	}
	clusters := make([]charCluster, 0, charX)
	off := 0
	for i := 0; i < charX && off < len(line); i++ {
		r, combc, size := util.DecodeCharacter(line[off:])
		if size <= 0 {
			break
		}
		off += size
		clusters = append(clusters, charCluster{
			r:        r,
			hasCombc: len(combc) > 0,
			byteEnd:  off,
		})
	}
	if len(clusters) == 0 {
		return "", 0, false
	}

	idx := len(clusters) - 1
	for idx >= 0 {
		cl := clusters[idx]
		if cl.hasCombc {
			return "", 0, false
		}
		if cl.r == '@' {
			if idx > 0 {
				prev := clusters[idx-1]
				if prev.hasCombc || unicode.IsLetter(prev.r) || unicode.IsDigit(prev.r) || prev.r == '_' || prev.r == '\\' {
					return "", 0, false
				}
			}
			startByte := cl.byteEnd
			endByte := clusters[len(clusters)-1].byteEnd
			return string(line[startByte:endByte]), idx, true
		}
		if !isCiteKeyRune(cl.r) {
			return "", 0, false
		}
		idx--
	}
	return "", 0, false
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
