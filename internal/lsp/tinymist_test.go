package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	ulua "github.com/monicafaye/TypX/internal/lua"
	"github.com/monicafaye/TypX/internal/screen"
	lua "github.com/yuin/gopher-lua"
)

func setupLSPEnv(t *testing.T, debounceMs float64) (string, string) {
	t.Helper()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "mock-rpc.log")
	t.Setenv("TYPX_LSP_HELPER", "mock_tinymist")
	t.Setenv("TYPX_MOCK_LOG", logPath)

	origConfigDir := config.ConfigDir
	origGlobalSettings := config.GlobalSettings
	origLuaL := ulua.L
	cfgDir := filepath.Join(dir, "cfg")
	require.NoError(t, os.MkdirAll(cfgDir, 0755))
	require.NoError(t, config.InitConfigDir(cfgDir))
	config.InitRuntimeFiles(true)
	config.InitPlugins()
	if ulua.L == nil {
		ulua.L = lua.NewState()
	}
	require.NoError(t, config.ReadSettings())
	require.NoError(t, config.InitGlobalSettings())
	config.GlobalSettings["typst.lspEnabled"] = true
	config.GlobalSettings["typst.tinymistPath"] = os.Args[0]
	config.GlobalSettings["typst.debounceMs"] = debounceMs
	config.GlobalSettings["typst.previewOpenBrowser"] = false
	_ = config.InitColorscheme()

	// Reset UI queue before and after
	drainAllUI(t)

	// SimulationScreen for BufPane / Tabs / InfoBar
	simScreen, err := screen.InitSimScreen()
	require.NoError(t, err)
	simScreen.SetSize(80, 24)

	defaultManager.mu.Lock()
	origBackoff := defaultManager.restartBackoff
	defaultManager.restartBackoff = 50 * time.Millisecond
	defaultManager.mu.Unlock()

	t.Cleanup(func() {
		Shutdown()
		defaultManager.mu.Lock()
		defaultManager.restartBackoff = origBackoff
		defaultManager.mu.Unlock()
		buffer.CloseOpenBuffers()
		drainAllUI(t)
		if screen.Screen != nil {
			screen.Screen.Fini()
		}
		if ulua.L != nil && ulua.L != origLuaL {
			ulua.L.Close()
		}
		ulua.L = origLuaL
		config.ConfigDir = origConfigDir
		config.GlobalSettings = origGlobalSettings
	})

	return dir, logPath
}

func drainAllUI(t *testing.T) {
	t.Helper()
	for {
		select {
		case <-action.UIQueueReady():
			action.DrainUIQueue()
		default:
			action.DrainUIQueue()
			return
		}
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		drainAllUI(t)
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	drainAllUI(t)
	require.True(t, cond(), msg)
}

func readMockLog(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func openTestBuffer(t *testing.T, absPath, content string) *buffer.Buffer {
	t.Helper()
	require.NoError(t, os.WriteFile(absPath, []byte(content), 0644))
	b, err := buffer.NewBufferFromFile(absPath, buffer.BTDefault)
	require.NoError(t, err)
	return b
}

func TestIntegrationDocumentLifecycleDiagnosticsCompletionHover(t *testing.T) {
	dir, logPath := setupLSPEnv(t, 40)

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "LSP manager did not reach StateReady")

	typPath := filepath.Join(dir, "doc.typ")
	txtPath := filepath.Join(dir, "notes.txt")

	bTyp := openTestBuffer(t, typPath, "hello\n")
	bTxt := openTestBuffer(t, txtPath, "plain text\n")
	// Unnamed buffer (Path == "") should also be ignored by LSP tracking.
	bUnnamed := buffer.NewBufferFromString("unnamed", "", buffer.BTDefault)
	_ = bUnnamed

	action.InitTabs([]*buffer.Buffer{bTyp, bTxt})
	bpTyp := action.MainTab().CurPane()
	require.NotNil(t, bpTyp)

	waitForCondition(t, 3*time.Second, func() bool {
		return strings.Contains(readMockLog(logPath), "didOpen:"+PathToURI(typPath))
	}, "expected didOpen for .typ buffer")
	assert.NotContains(t, readMockLog(logPath), PathToURI(txtPath), ".txt buffer must not trigger didOpen")

	// StatusLineInfo check
	slFn := StatusLineInfo()
	assert.Contains(t, slFn(bTyp), "LSP: ready")
	assert.Equal(t, "", slFn(bTxt))

	// Debounced didChange + UTF-16 diagnostic with 2-byte α and 4-byte 😀
	bTyp.Insert(buffer.Loc{X: 0, Y: 0}, "α😀ERR ")
	buffer.FlushObservers()

	waitForCondition(t, 3*time.Second, func() bool {
		for _, m := range bTyp.Messages {
			if m.Owner == diagnosticOwner {
				return true
			}
		}
		return false
	}, "expected publishDiagnostics to populate buffer.Messages")

	require.Len(t, bTyp.Messages, 1)
	msg := bTyp.Messages[0]
	assert.Equal(t, diagnosticOwner, msg.Owner)
	assert.Equal(t, buffer.MsgType(buffer.MTError), msg.Kind)
	assert.Equal(t, buffer.Loc{X: 2, Y: 0}, msg.Start)
	assert.Equal(t, buffer.Loc{X: 5, Y: 0}, msg.End)
	assert.Equal(t, "mock error here", msg.Msg)

	// :diagnostics command opens a bottom-docked interactive ListPane
	DiagnosticsCommand()(bpTyp, nil)
	diagLP := ListPaneForTab(bpTyp.Tab())
	require.NotNil(t, diagLP, "expected :diagnostics to open ListPane drawer")
	diagItems := diagLP.VisibleItems()
	require.NotEmpty(t, diagItems)
	assert.Contains(t, diagItems[0].Primary, "mock error here")
	assert.True(t, CloseListPaneForTab(bpTyp.Tab()))

	// Save flushes pending edit before didSave
	bTyp.Insert(buffer.Loc{X: 0, Y: 0}, "ok ")
	buffer.FlushObservers()
	require.NoError(t, bTyp.Save())
	waitForCondition(t, 3*time.Second, func() bool {
		return strings.Contains(readMockLog(logPath), "didSave:"+PathToURI(typPath))
	}, "expected didSave")

	// LspCompleteAction inserts suffix completion & cycles suggestions
	bTyp.Insert(buffer.Loc{X: 0, Y: 0}, "al")
	bpTyp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})
	assert.True(t, LspCompleteAction()(bpTyp))
	waitForCondition(t, 3*time.Second, func() bool {
		return bTyp.HasSuggestions && len(bTyp.Suggestions) == 2
	}, "expected LspCompleteAction to populate suggestions")
	assert.Equal(t, []string{"alpha", "algorithm"}, bTyp.Suggestions)
	assert.Contains(t, string(bTyp.LineBytes(0)), "alpha")

	// Stale-version completion must be discarded if buffer mutates mid-flight
	t.Setenv("TYPX_MOCK_COMPLETION_DELAY_MS", "150")
	// Restart so mock picks up delay env? Or test with direct delay env on next test;
	// let's test hover first, then stale completion in its own subtest.

	// LspHoverAction displays short hover in InfoBar
	bpTyp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})
	assert.True(t, LspHoverAction()(bpTyp))
	waitForCondition(t, 3*time.Second, func() bool {
		return action.InfoBar != nil && action.InfoBar.Msg == "Mock short hover"
	}, "expected short hover message in InfoBar")

	// Long multi-line hover opens a readonly Hover split
	t.Setenv("TYPX_MOCK_HOVER_LONG", "1")
	TinymistRestartCommand()(bpTyp, nil)
	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected restart ready for long hover")
	bpTyp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})
	assert.True(t, LspHoverAction()(bpTyp))
	waitForCondition(t, 3*time.Second, func() bool {
		for _, p := range bpTyp.Tab().Panes {
			if pane, ok := p.(*action.BufPane); ok && pane.Buf.GetName() == "Hover" {
				return strings.Contains(string(pane.Buf.Bytes()), "Line 1 documentation")
			}
		}
		return false
	}, "expected long hover to open Hover split")

	// :tinymist-status opens status split
	TinymistStatusCommand()(bpTyp, nil)
	foundStatusPane := false
	for _, p := range bpTyp.Tab().Panes {
		if pane, ok := p.(*action.BufPane); ok && pane.Buf.GetName() == "Tinymist Status" {
			foundStatusPane = true
			assert.Contains(t, string(pane.Buf.Bytes()), "State: ready")
		}
	}
	assert.True(t, foundStatusPane, "expected :tinymist-status split")

	// SharedBuffer close guard: opening second Buffer on same file does not didClose on first close
	bShared, err := buffer.NewBufferFromFile(typPath, buffer.BTDefault)
	require.NoError(t, err)
	assert.True(t, bShared.Shared())

	bShared.Close()
	time.Sleep(80 * time.Millisecond)
	assert.NotContains(t, readMockLog(logPath), "didClose:"+PathToURI(typPath), "didClose must not fire while SharedBuffer still open")

	// SaveAs from .typ to .txt closes old .typ URI and clears tinymist diagnostics
	newTxt := filepath.Join(dir, "converted.txt")
	require.NoError(t, bTyp.SaveAs(newTxt))
	waitForCondition(t, 3*time.Second, func() bool {
		return strings.Contains(readMockLog(logPath), "didClose:"+PathToURI(typPath))
	}, "expected SaveAs to .txt to send didClose for old .typ URI")
	assert.Empty(t, bTyp.Messages, "expected tinymist diagnostics cleared after SaveAs away from .typ")

	// Non-Typst fallback check: TogglePreviewAction returns false on .txt, and Alt-p falls through to RemoveMultiCursor
	assert.False(t, TogglePreviewAction()(bpTyp))
	assert.False(t, LspCompleteAction()(bpTyp))
	assert.False(t, LspHoverAction()(bpTyp))
	binding, ok := config.Bindings["buffer"]["Alt-p"]
	require.True(t, ok)
	assert.Equal(t, "TogglePreview|RemoveMultiCursor", binding)
}

func TestStaleCompletionIsDiscarded(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)
	t.Setenv("TYPX_MOCK_COMPLETION_DELAY_MS", "150")

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected ready state")

	typPath := filepath.Join(dir, "stale.typ")
	b := openTestBuffer(t, typPath, "al")
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()
	bp.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})

	assert.True(t, LspCompleteAction()(bp))
	// Mutate buffer while completion request is in flight!
	b.Insert(buffer.Loc{X: 2, Y: 0}, "x")
	time.Sleep(250 * time.Millisecond)
	drainAllUI(t)

	assert.False(t, b.HasSuggestions, "stale completion must be discarded when buffer version changes")
	assert.Equal(t, "alx", string(b.Bytes()))
}

func TestPreviewStartStopAndFallbackSubprocess(t *testing.T) {
	dir, logPath := setupLSPEnv(t, 0)
	t.Setenv("TYPX_MOCK_PREVIEW_FALLBACK", "1")

	origOpen := openURLFunc
	t.Cleanup(func() { openURLFunc = origOpen })
	var mu sync.Mutex
	var openedURLs []string
	openURLFunc = func(u string) error {
		mu.Lock()
		openedURLs = append(openedURLs, u)
		mu.Unlock()
		return nil
	}

	config.GlobalSettings["typst.previewOpenBrowser"] = true

	typPath := filepath.Join(dir, "prev.typ")
	require.NoError(t, os.WriteFile(typPath, []byte("= Preview\n"), 0644))

	// --preview CLI flag (StartPreview: true) deferred to first .typ OnBufferOpen
	Init(Options{StartPreview: true})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	b, err := buffer.NewBufferFromFile(typPath, buffer.BTDefault)
	require.NoError(t, err)
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()

	waitForCondition(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(openedURLs) == 1
	}, "expected --preview fallback subprocess to emit URL and invoke OpenURL")

	mu.Lock()
	assert.Equal(t, "http://127.0.0.1:23625", openedURLs[0])
	mu.Unlock()
	assert.Contains(t, StatusLineInfo()(b), "PREVIEW: ON")
	assert.Contains(t, readMockLog(logPath), "executeCommand:tinymist.startDefaultPreview")

	// Toggle off via TogglePreviewAction
	assert.True(t, TogglePreviewAction()(bp))
	assert.NotContains(t, StatusLineInfo()(b), "PREVIEW: ON")
}

func TestCrashRecoveryMaxOneAutoRestartAndManualRestart(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected initial StateReady")

	typPath := filepath.Join(dir, "crash.typ")
	b := openTestBuffer(t, typPath, "ERR line\n")
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()

	waitForCondition(t, 3*time.Second, func() bool {
		return len(b.Messages) > 0
	}, "expected initial diagnostic before crash")

	// Crash #1 -> clears stale diagnostics, auto-restarts once back to StateReady
	b.Insert(buffer.Loc{X: 0, Y: 0}, "CRASH_NOW ")
	buffer.FlushObservers()

	waitForCondition(t, 5*time.Second, func() bool {
		return len(b.Messages) == 0
	}, "expected stale diagnostics cleared on crash")

	waitForCondition(t, 5*time.Second, func() bool {
		defaultManager.mu.Lock()
		restarts := defaultManager.autoRestarts
		defaultManager.mu.Unlock()
		return restarts == 1 && defaultManager.State() == StateReady
	}, "expected 1 auto-restart back to StateReady")

	// Crash #2 -> hits max 1 auto-restart and transitions to StateError (no crash loop)
	b.Insert(buffer.Loc{X: 0, Y: 0}, "CRASH_NOW ")
	buffer.FlushObservers()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateError
	}, "expected StateError after second crash")

	// Fix buffer content and run :tinymist-restart -> resets counter and recovers to StateReady
	b.EventHandler.Remove(b.Start(), b.End())
	b.EventHandler.Insert(b.Start(), "= Healthy\n")
	buffer.FlushObservers()

	TinymistRestartCommand()(bp, nil)
	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected :tinymist-restart to recover to StateReady")
}

func TestNoLSPGracefulDegradation(t *testing.T) {
	dir, logPath := setupLSPEnv(t, 0)
	config.GlobalSettings["typst.lspEnabled"] = false

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	typPath := filepath.Join(dir, "offline.typ")
	b := openTestBuffer(t, typPath, "= Offline\n")
	action.InitTabs([]*buffer.Buffer{b})

	time.Sleep(80 * time.Millisecond)
	assert.Equal(t, StateOff, defaultManager.State())
	assert.Contains(t, StatusLineInfo()(b), "LSP: off")
	assert.Empty(t, readMockLog(logPath), "no LSP process should spawn when typst.lspEnabled=false")
}

func TestFindProjectRoot(t *testing.T) {
	t.Run("typst.toml beats parent .git", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0755))
		sub := filepath.Join(repo, "book")
		chapters := filepath.Join(sub, "chapters")
		require.NoError(t, os.MkdirAll(chapters, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(sub, "typst.toml"), []byte("[package]\n"), 0644))
		f := filepath.Join(chapters, "01-sets.typ")
		require.NoError(t, os.WriteFile(f, []byte("= Sets\n"), 0644))

		assert.Equal(t, sub, findProjectRoot(f))
	})

	t.Run("parent .git root", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0755))
		chapters := filepath.Join(repo, "chapters")
		require.NoError(t, os.MkdirAll(chapters, 0755))
		f := filepath.Join(chapters, "01-sets.typ")
		require.NoError(t, os.WriteFile(f, []byte("#import \"../template.typ\": *\n"), 0644))

		assert.Equal(t, repo, findProjectRoot(f))
	})

	t.Run("root-relative import resolves nearest ancestor containing target", func(t *testing.T) {
		outer := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(outer, ".git"), 0755))
		notes := filepath.Join(outer, "course-notes")
		require.NoError(t, os.MkdirAll(filepath.Join(notes, "templates"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(notes, "templates", "course-note.typ"), []byte("#let ok = 1\n"), 0644))
		lectures := filepath.Join(notes, "courses", "AIF231105", "lectures")
		require.NoError(t, os.MkdirAll(lectures, 0755))
		f := filepath.Join(lectures, "01-sets.typ")
		require.NoError(t, os.WriteFile(f, []byte("#import \"/templates/course-note.typ\": *\n= Sets\n"), 0644))

		assert.Equal(t, notes, findProjectRoot(f))
		assert.Equal(t, PathToURI(notes), initialRootURI([]string{"+10:1", f}))
	})

	t.Run("relative parent imports without markers", func(t *testing.T) {
		root := t.TempDir()
		nested := filepath.Join(root, "a", "b")
		require.NoError(t, os.MkdirAll(nested, 0755))
		f := filepath.Join(nested, "01-sets.typ")
		require.NoError(t, os.WriteFile(f, []byte("#import \"../../template.typ\": *\n= Sets\n"), 0644))

		assert.Equal(t, root, findProjectRoot(f))
	})
}

func TestCompileStatusAndStatusLineIndicators(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)
	typPath := filepath.Join(dir, "status.typ")
	b := openTestBuffer(t, typPath, "= Status\n")

	m := newManager()
	m.gen = 1
	m.setStateLocked(StateReady)
	m.atomicPreview.Store(true)
	m.previewActive = true

	m.handleCompileStatus(1, "compiling", 0)
	assert.Contains(t, m.statusLine(b), "LSP: ready |")
	assert.Contains(t, m.statusLine(b), "compiling")
	assert.Contains(t, m.statusLine(b), "PREVIEW: ON |")

	m.handleCompileStatus(1, "compileSuccess", 12)
	assert.Equal(t, "LSP: ready | 12p | PREVIEW: ON | ", m.statusLine(b))

	m.handleCompileStatus(1, "compileError", 0)
	assert.Equal(t, "LSP: ready | ERR | PREVIEW: ON | ", m.statusLine(b))

	// Buffer diagnostic error count takes precedence with exact count.
	b.AddMessage(buffer.NewMessage(diagnosticOwner, "err 1", buffer.Loc{X: 0, Y: 0}, buffer.Loc{X: 1, Y: 0}, buffer.MTError))
	b.AddMessage(buffer.NewMessage(diagnosticOwner, "err 2", buffer.Loc{X: 0, Y: 0}, buffer.Loc{X: 1, Y: 0}, buffer.MTError))
	assert.Equal(t, "LSP: ready | ERR: 2 | PREVIEW: ON | ", m.statusLine(b))
}

func TestScrollPreviewToSendsImmediateScrollCommand(t *testing.T) {
	dir, logPath := setupLSPEnv(t, 0)

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	typPath := filepath.Join(dir, "outline_sync.typ")
	b := openTestBuffer(t, typPath, "= Intro\n\n= Methods\n\n= Conclusion\n")
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected tinymist LSP ready")

	// Start browser preview via LSP executeCommand.
	require.True(t, defaultManager.togglePreview(bp))
	waitForCondition(t, 5*time.Second, func() bool {
		defaultManager.mu.Lock()
		mode := defaultManager.previewMode
		defaultManager.mu.Unlock()
		return mode == "lsp"
	}, "expected browser preview started in lsp mode")

	// Trigger immediate outline-style scroll to line 4 ("= Conclusion") without moving cursor.
	require.True(t, ScrollPreviewTo(b, 4, 0))
	waitForCondition(t, 5*time.Second, func() bool {
		return strings.Contains(readMockLog(logPath), "tinymist.scrollPreview")
	}, "expected ScrollPreviewTo to dispatch tinymist.scrollPreview immediately")
}
