package templates

import (
	"context"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	lua "github.com/yuin/gopher-lua"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	ulua "github.com/monicafaye/TypX/internal/lua"
	"github.com/monicafaye/TypX/internal/screen"
)

type fakeBackend struct {
	mu          sync.Mutex
	initCalls   [][2]string
	entryCalls  []string
	entryResult string
}

func (f *fakeBackend) InitTemplate(ctx context.Context, spec, targetDir string) (string, error) {
	f.mu.Lock()
	f.initCalls = append(f.initCalls, [2]string{spec, targetDir})
	f.mu.Unlock()
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(targetDir, "main.typ"), []byte("#import \""+spec+"\": *\n= Scaffolded\n"), 0o644); err != nil {
		return "", err
	}
	return "main.typ", nil
}

func (f *fakeBackend) GetTemplateEntry(ctx context.Context, spec string) (string, error) {
	f.mu.Lock()
	f.entryCalls = append(f.entryCalls, spec)
	res := f.entryResult
	f.mu.Unlock()
	if res == "" {
		res = "#import \"" + spec + "\": *\n"
	}
	return res, nil
}

func initPaneTestEnv(t *testing.T, width, height int) {
	t.Helper()
	if ulua.L == nil {
		ulua.L = lua.NewState()
	}
	config.ConfigDir = t.TempDir()
	config.InitRuntimeFiles(false)
	_ = config.InitGlobalSettings()
	config.GlobalSettings["backup"] = false
	config.GlobalSettings["fastdirty"] = true
	if screen.Screen == nil {
		sim, err := screen.InitSimScreen()
		require.NoError(t, err)
		sim.SetSize(width, height)
	} else if sim, ok := screen.Screen.(tcell.SimulationScreen); ok {
		sim.SetSize(width, height)
	}
	Init()
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()
	buffer.SetMessager(action.InfoBar)
	t.Cleanup(func() {
		SetExternalBackend(nil)
		SetUniverseClient(nil)
		SetPreviewProvider(nil)
		SetPreviewCacheDir("")
		if tab := action.MainTab(); tab != nil {
			CloseForTab(tab)
		}
		buffer.CloseOpenBuffers()
		action.DrainUIQueue()
	})
}

func TestEffectiveWidth(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 0, EffectiveWidth(49, 36))
	assert.Equal(t, 25, EffectiveWidth(50, 36))
	assert.Equal(t, 36, EffectiveWidth(120, 36))
	assert.Equal(t, MinTemplatesWidth, EffectiveWidth(120, 5))
}

func TestTemplatePaneNavigationFilterApplyAndUniverse(t *testing.T) {
	initPaneTestEnv(t, 120, 30)

	persDir := filepath.Join(config.ConfigDir, "templates")
	require.NoError(t, os.MkdirAll(persDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(persDir, "minimal.typ"), []byte("// @title: Minimal Document\n= {{title}}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(persDir, "notes.typ"), []byte("// @title: Study Notes\n= {{title}}\n"), 0o644))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"charged-ieee","version":"0.1.4","description":"IEEE paper","categories":["paper"],"template":{"path":"template","entrypoint":"main.typ"}}]`))
	}))
	defer srv.Close()

	client := &UniverseClient{
		BaseURL:    srv.URL,
		CacheFile:  filepath.Join(t.TempDir(), "universe.json"),
		HTTPClient: srv.Client(),
	}
	SetUniverseClient(client)
	backend := &fakeBackend{}
	SetExternalBackend(backend)

	b := buffer.NewBufferFromString("", "hw1.typ", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{b})
	action.Tabs.Resize()
	tab := action.MainTab()
	bp := tab.CurPane()

	tp, ok := Open(bp, true)
	require.True(t, ok)
	require.NotNil(t, tp)
	assert.Equal(t, tp, tab.ActivePane())

	// First selectable item skips section header row.
	sel, hasSel := tp.SelectedTemplate()
	require.True(t, hasSel)
	assert.Equal(t, "minimal", sel.Name)

	// Moving down skips headers cleanly and never lands on rowHeader.
	rows := tp.Rows()
	require.Greater(t, len(rows), 4)
	for i := 0; i < len(rows); i++ {
		action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
		if tp.selectedIdx >= 0 && tp.selectedIdx < len(tp.rows) {
			assert.NotEqual(t, rowHeader, tp.rows[tp.selectedIdx].Kind)
		}
	}

	// Render should not panic and should include footer.
	tp.Display()

	// Load Universe via 'u' key -> fetches mock index and populates Universe row.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'u', tcell.ModNone, ""))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		action.DrainUIQueue()
		if defaultManager.isUniverseLoaded() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.True(t, defaultManager.isUniverseLoaded())

	// Filter to "charged-ieee", test preamble insertion ('p') via fakeBackend.
	for _, r := range "/charged-ieee" {
		action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone, ""))
	}
	assert.True(t, tp.IsFiltering())
	assert.Equal(t, "charged-ieee", tp.FilterQuery())
	sel, hasSel = tp.SelectedTemplate()
	require.True(t, hasSel)
	assert.Equal(t, "@preview/charged-ieee:0.1.4", sel.PackageSpec)

	// Clear filter with Esc, then filter to "notes" and press Enter to insert starter.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, ""))
	assert.False(t, tp.IsFiltering())
	for _, r := range "/notes" {
		action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone, ""))
	}
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))

	assert.Contains(t, string(bp.Buf.Bytes()), "= Hw1")
	assert.Equal(t, bp, tab.ActivePane(), "applying starter should return focus to editor")

	// Test ScaffoldProjectAsync via backend.
	scaffoldDir := filepath.Join(t.TempDir(), "ieee-proj")
	defaultManager.ScaffoldProjectAsync(tab, bp, Template{
		Name:        "charged-ieee",
		Version:     "0.1.4",
		Source:      SourceUniverse,
		IsMultiFile: true,
		PackageSpec: "@preview/charged-ieee:0.1.4",
	}, scaffoldDir)

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		action.DrainUIQueue()
		backend.mu.Lock()
		n := len(backend.initCalls)
		backend.mu.Unlock()
		if n > 0 && len(action.Tabs.List) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	backend.mu.Lock()
	require.Len(t, backend.initCalls, 1)
	backend.mu.Unlock()
	_, err := os.Stat(filepath.Join(scaffoldDir, "main.typ"))
	assert.NoError(t, err)

	// Test preamble insertion via backend.
	blank := buffer.NewBufferFromString("", "in-place.typ", buffer.BTDefault)
	action.Tabs.AddTab(action.NewTabFromBuffer(0, 0, 120, 23, blank))
	action.Tabs.SetActive(len(action.Tabs.List) - 1)
	inPlaceTab := action.MainTab()
	defaultManager.InsertPreamble(inPlaceTab.CurPane(), inPlaceTab, Template{
		Name:        "charged-ieee",
		Version:     "0.1.4",
		Source:      SourceUniverse,
		IsMultiFile: true,
		PackageSpec: "@preview/charged-ieee:0.1.4",
	})
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		action.DrainUIQueue()
		if len(string(blank.Bytes())) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Contains(t, string(blank.Bytes()), `#import "@preview/charged-ieee:0.1.4": *`)
}

type fakePreviewProvider struct {
	mu           sync.Mutex
	visual       bool
	loadingCalls []string
	shownTitles  []string
	errorCalls   []string
	clearCount   int
}

func (f *fakePreviewProvider) SupportsVisual() bool { return f.visual }

func (f *fakePreviewProvider) OpenLoading(tab *action.Tab, bp *action.BufPane, title string) bool {
	f.mu.Lock()
	f.loadingCalls = append(f.loadingCalls, title)
	f.mu.Unlock()
	return true
}

func (f *fakePreviewProvider) ShowImages(tab *action.Tab, title string, imgs []*image.RGBA) {
	f.mu.Lock()
	f.shownTitles = append(f.shownTitles, title)
	f.mu.Unlock()
}

func (f *fakePreviewProvider) ShowError(tab *action.Tab, title, errMsg string) {
	f.mu.Lock()
	f.errorCalls = append(f.errorCalls, title+": "+errMsg)
	f.mu.Unlock()
}

func (f *fakePreviewProvider) Clear(tab *action.Tab) {
	f.mu.Lock()
	f.clearCount++
	f.mu.Unlock()
}

func (f *fakePreviewProvider) CompileTypst(ctx context.Context, filePath, rootDir string, content []byte) ([]*image.RGBA, error) {
	return []*image.RGBA{image.NewRGBA(image.Rect(0, 0, 50, 70))}, nil
}

func TestTemplatePaneLivePreviewDefaultOnToggleAndCleanup(t *testing.T) {
	initPaneTestEnv(t, 120, 30)

	persDir := filepath.Join(config.ConfigDir, "templates")
	require.NoError(t, os.MkdirAll(persDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(persDir, "minimal.typ"), []byte("// @title: Minimal Document\n= {{title}}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(persDir, "notes.typ"), []byte("// @title: Study Notes\n= {{title}}\n"), 0o644))

	SetPreviewCacheDir(filepath.Join(t.TempDir(), "preview-cache"))
	fp := &fakePreviewProvider{visual: true}
	SetPreviewProvider(fp)

	b := buffer.NewBufferFromString("", "live.typ", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{b})
	action.Tabs.Resize()
	tab := action.MainTab()
	bp := tab.CurPane()

	// 1. Live preview is on by default and triggers automatically when gallery opens.
	tp, ok := Open(bp, true)
	require.True(t, ok)
	require.True(t, tp.IsLivePreview())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		action.DrainUIQueue()
		fp.mu.Lock()
		n := len(fp.shownTitles)
		fp.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	fp.mu.Lock()
	require.GreaterOrEqual(t, len(fp.shownTitles), 1)
	firstShown := fp.shownTitles[0]
	fp.mu.Unlock()
	assert.Equal(t, "Minimal Document", firstShown)

	// 2. Moving selection updates preview after debounce.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, ""))
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		action.DrainUIQueue()
		fp.mu.Lock()
		n := len(fp.shownTitles)
		fp.mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	fp.mu.Lock()
	require.GreaterOrEqual(t, len(fp.shownTitles), 2)
	secondShown := fp.shownTitles[len(fp.shownTitles)-1]
	fp.mu.Unlock()
	assert.NotEqual(t, firstShown, secondShown)

	// 3. Pressing 'v' toggles live preview off and clears; pressing 'v' again re-enables.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'v', tcell.ModNone, ""))
	assert.False(t, tp.IsLivePreview())
	fp.mu.Lock()
	clearsAfterOff := fp.clearCount
	fp.mu.Unlock()
	assert.GreaterOrEqual(t, clearsAfterOff, 1)

	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'v', tcell.ModNone, ""))
	assert.True(t, tp.IsLivePreview())

	// 4. Returning focus to editor clears live gallery preview cleanly.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, ""))
	assert.Equal(t, bp, tab.ActivePane())
	fp.mu.Lock()
	clearsAfterEsc := fp.clearCount
	fp.mu.Unlock()
	assert.Greater(t, clearsAfterEsc, clearsAfterOff)
}

func TestTemplateGalleryWithoutOpenTypFile(t *testing.T) {
	initPaneTestEnv(t, 120, 30)

	persDir := filepath.Join(config.ConfigDir, "templates")
	require.NoError(t, os.MkdirAll(persDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(persDir, "notes.typ"),
		[]byte("// @title: Study Notes\n#let note(x) = [#x]\n\n= {{title}}\n\nBody\n"),
		0o644,
	))

	backend := &fakeBackend{}
	SetExternalBackend(backend)

	// 1. Blank unnamed buffer (like launching `typx` with no args):
	// Alt+Shift+N opens gallery, applying a single-file template inserts in-place
	// and switches the buffer filetype to "typst".
	unnamed := buffer.NewBufferFromString("", "", buffer.BTDefault)
	require.False(t, unnamed.IsTypst())
	action.InitTabs([]*buffer.Buffer{unnamed})
	action.Tabs.Resize()
	tab := action.MainTab()
	bp := tab.CurPane()

	toggle := NewToggleTemplatesAction()
	require.True(t, toggle(bp), "ToggleTemplates should open gallery on blank unnamed buffer")
	tp := PaneForTab(tab)
	require.NotNil(t, tp)
	assert.Equal(t, tp, tab.ActivePane())

	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	assert.Equal(t, bp, tab.ActivePane())
	assert.True(t, bp.Buf.IsTypst(), "applying template into unnamed buffer must activate typst filetype")
	assert.Contains(t, string(bp.Buf.Bytes()), "#let note(x)")
	assert.Contains(t, string(bp.Buf.Bytes()), "= Untitled Document")
	CloseForTab(tab)

	// 2. Non-Typst named file (e.g. README.md):
	// Alt+Shift+N opens gallery; applying a template or inserting a preamble
	// opens a new .typ tab without modifying README.md.
	mdBuf := buffer.NewBufferFromString("# Readme\n", "README.md", buffer.BTDefault)
	require.False(t, mdBuf.IsTypst())
	action.InitTabs([]*buffer.Buffer{mdBuf})
	action.Tabs.Resize()
	mdTab := action.MainTab()
	mdBP := mdTab.CurPane()

	require.True(t, toggle(mdBP), "ToggleTemplates should open gallery on non-Typst file")
	require.NotNil(t, PaneForTab(mdTab))

	// Apply single-file template -> opens new .typ tab, leaves README.md untouched.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	require.Len(t, action.Tabs.List, 2)
	assert.Equal(t, "# Readme\n", string(mdBuf.Bytes()))
	newBuf := action.MainTab().CurPane().Buf
	assert.True(t, newBuf.IsTypst())
	assert.Contains(t, string(newBuf.Bytes()), "#let note(x)")
	assert.Equal(t, mdBP, mdTab.ActivePane(), "previous tab should return focus from gallery")

	// Switch back to README.md and insert preamble (single-file) -> opens new .typ tab with preamble only.
	action.Tabs.SetActive(0)
	defaultManager.InsertPreamble(mdBP, mdTab, Template{
		Name:     "notes",
		Source:   SourcePersonal,
		FilePath: filepath.Join(persDir, "notes.typ"),
	})
	require.Len(t, action.Tabs.List, 3)
	preBuf := action.MainTab().CurPane().Buf
	assert.True(t, preBuf.IsTypst())
	assert.Contains(t, string(preBuf.Bytes()), "#let note(x)")
	assert.NotContains(t, string(preBuf.Bytes()), "Body")
	assert.Equal(t, "# Readme\n", string(mdBuf.Bytes()))

	// Package preamble on README.md -> fetches via backend and opens in a new .typ tab.
	action.Tabs.SetActive(0)
	defaultManager.InsertPreamble(mdBP, mdTab, Template{
		Name:        "charged-ieee",
		Version:     "0.1.4",
		Source:      SourceUniverse,
		IsMultiFile: true,
		PackageSpec: "@preview/charged-ieee:0.1.4",
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		action.DrainUIQueue()
		if len(action.Tabs.List) >= 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Len(t, action.Tabs.List, 4)
	pkgPreBuf := action.MainTab().CurPane().Buf
	assert.True(t, pkgPreBuf.IsTypst())
	assert.Contains(t, string(pkgPreBuf.Bytes()), `#import "@preview/charged-ieee:0.1.4": *`)
	assert.Equal(t, "# Readme\n", string(mdBuf.Bytes()))
}

func TestUnnamedBufferScaffoldDirAndProjectRootUseCwd(t *testing.T) {
	initPaneTestEnv(t, 120, 30)

	wd, err := os.Getwd()
	require.NoError(t, err)

	unnamed := buffer.NewBufferFromString("", "", buffer.BTDefault)
	require.Equal(t, "", unnamed.Path)
	require.Equal(t, wd, unnamed.AbsPath)
	action.InitTabs([]*buffer.Buffer{unnamed})
	action.Tabs.Resize()
	bp := action.MainTab().CurPane()

	gotDir := defaultScaffoldDir(bp, Template{Name: "charged-ieee"})
	assert.Equal(t, filepath.Join(wd, "charged-ieee"), gotDir)
	assert.Equal(t, wd, defaultManager.resolveProjectRoot(bp))
}

func TestTemplatePaneSearchBarCtrlFTabBrowseAndUniverseLoad(t *testing.T) {
	t.Setenv("TYPST_PACKAGE_PATH", filepath.Join(t.TempDir(), "local-pkgs"))
	t.Setenv("TYPST_PACKAGE_CACHE_PATH", filepath.Join(t.TempDir(), "cache-pkgs"))
	initPaneTestEnv(t, 120, 30)

	persDir := filepath.Join(config.ConfigDir, "templates")
	require.NoError(t, os.MkdirAll(persDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(persDir, "notes.typ"),
		[]byte("// @title: Study Notes\n// @tags: lecture, math\n= {{title}}\n"),
		0o644,
	))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"name":"charged-ieee","version":"0.1.4","description":"IEEE conference paper","categories":["paper"],"keywords":["two-column"],"template":{"path":"template","entrypoint":"main.typ"}},
			{"name":"touying","version":"0.6.1","description":"Slides framework","categories":["presentation"],"template":{"path":"template","entrypoint":"slides.typ"}}
		]`))
	}))
	defer srv.Close()

	client := &UniverseClient{
		BaseURL:    srv.URL,
		CacheFile:  filepath.Join(t.TempDir(), "universe.json"),
		HTTPClient: srv.Client(),
	}
	SetUniverseClient(client)
	backend := &fakeBackend{}
	SetExternalBackend(backend)

	b := buffer.NewBufferFromString("", "search.typ", buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{b})
	action.Tabs.Resize()
	tab := action.MainTab()
	bp := tab.CurPane()

	tp, ok := Open(bp, true)
	require.True(t, ok)

	// 1. Clicking row 1 (search bar) activates search typing mode.
	tp.HandleEvent(tcell.NewEventMouse(tp.view.X+2, tp.view.Y+1, tcell.Button1, tcell.ModNone, ""))
	tp.HandleEvent(tcell.NewEventMouse(tp.view.X+2, tp.view.Y+1, tcell.ButtonNone, tcell.ModNone, ""))
	assert.True(t, tp.IsFiltering())

	// 2. Typing a query with no local match before Universe loads selects the Universe action row.
	for _, r := range "two-column" {
		action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone, ""))
	}
	assert.Equal(t, "two-column", tp.FilterQuery())
	rows := tp.Rows()
	require.GreaterOrEqual(t, len(rows), 2)
	assert.Equal(t, rowUniverseAction, rows[tp.selectedIdx].Kind)

	// 3. Pressing Enter on the Universe action row loads Universe while preserving the search query.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, ""))
	assert.Equal(t, "two-column", tp.FilterQuery())

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		action.DrainUIQueue()
		if defaultManager.isUniverseLoaded() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.True(t, defaultManager.isUniverseLoaded())
	sel, hasSel := tp.SelectedTemplate()
	require.True(t, hasSel)
	assert.Equal(t, "charged-ieee", sel.Name)

	// 4. Ctrl-F re-focuses search keeping the current query, Ctrl-U clears it, and Tab commits to browse.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyCtrlF, 0, tcell.ModCtrl, ""))
	assert.True(t, tp.IsFiltering())
	assert.Equal(t, "two-column", tp.FilterQuery())

	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyCtrlU, 0, tcell.ModCtrl, ""))
	assert.True(t, tp.IsFiltering())
	assert.Equal(t, "", tp.FilterQuery())

	for _, r := range "lecture math" {
		action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone, ""))
	}
	// Alt+Backspace removes the last word ("math").
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyBackspace2, 0, tcell.ModAlt, ""))
	assert.Equal(t, "lecture ", tp.FilterQuery())

	// Tab exits search typing while preserving filtered results for normal-mode actions.
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone, ""))
	assert.False(t, tp.IsFiltering())
	assert.Equal(t, "lecture ", tp.FilterQuery())
	sel, hasSel = tp.SelectedTemplate()
	require.True(t, hasSel)
	assert.Equal(t, "notes", sel.Name)

	// 5. `:templates search` command opens and filters directly.
	cmd := NewTemplatesCmd()
	cmd(bp, []string{"search", "slides"})
	assert.Equal(t, "slides", tp.FilterQuery())
	assert.False(t, tp.IsFiltering())
	sel, hasSel = tp.SelectedTemplate()
	require.True(t, hasSel)
	assert.Equal(t, "touying", sel.Name)

	// `:templates search` with no query focuses the search bar for typing.
	cmd(bp, []string{"search"})
	assert.Equal(t, "", tp.FilterQuery())
	assert.True(t, tp.IsFiltering())
}
