package templates

import (
	"context"
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
