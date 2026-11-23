package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/micro-editor/tcell/v2"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/clipboard"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/display"
	"github.com/monicafaye/TypX/internal/lsp"
	"github.com/monicafaye/TypX/internal/screen"
	"github.com/monicafaye/TypX/internal/typst/features"
)

// RequirePhase23 skips the test ONLY when TYPX_E2E_PHASE1_ONLY=1 is set.
// Plain `go test ./tests/e2e/...` never skips Phase 2/3 tests.
func RequirePhase23(t testing.TB) {
	t.Helper()
	if os.Getenv("TYPX_E2E_PHASE1_ONLY") == "1" {
		t.Skip("skipping Phase 2/3 E2E test because TYPX_E2E_PHASE1_ONLY=1")
	}
}

// SimOptions configures an in-process SimulationScreen TypX editor session.
type SimOptions struct {
	Files       []string
	InitialText string
	InitialName string
	ConfigDir   string
	Settings    map[string]any
	Bindings    map[string]any
	LSPOptions  lsp.Options
	Width       int
	Height      int
}

// SimEditor drives TypX interactively over tcell.SimulationScreen without importing
// internal/typst, internal/typst/conceal, or internal/typst/shortcuts.
// Note: Do not invoke :quit or Ctrl-Q inside SimEditor because action.Quit calls runtime.Goexit().
type SimEditor struct {
	t   testing.TB
	Sim tcell.SimulationScreen
}

var (
	simInitOnce sync.Once
	simScreen   tcell.SimulationScreen
	simInitErr  error
)

func writeJSONIfNotNil(t testing.TB, path string, v map[string]any) {
	t.Helper()
	if v == nil {
		return
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func openInitialBuffers(t testing.TB, opts SimOptions) []*buffer.Buffer {
	t.Helper()
	var bufs []*buffer.Buffer
	if len(opts.Files) > 0 {
		for _, path := range opts.Files {
			b, err := buffer.NewBufferFromFile(path, buffer.BTDefault)
			if err != nil {
				t.Fatalf("NewBufferFromFile(%q): %v", path, err)
			}
			bufs = append(bufs, b)
		}
		return bufs
	}
	b := buffer.NewBufferFromString(opts.InitialText, opts.InitialName, buffer.BTDefault)
	bufs = append(bufs, b)
	return bufs
}

func applyGlobalSettingsOverrides(settings map[string]any) {
	for k, v := range settings {
		if _, ok := config.GlobalSettings[k]; ok {
			config.GlobalSettings[k] = v
		} else {
			config.GlobalSettings[k] = v
		}
	}
}

// NewSimEditor boots or resets the in-process TypX SimulationScreen harness.
// Global registrations (Lua plugins, features.Init, lsp.Init, InitBindings, InitCommands)
// run once per OS test process; per-test buffers, settings, InfoBar, and screen dimensions
// are reset cleanly for each test and cleaned up via t.Cleanup.
func NewSimEditor(t testing.TB, opts SimOptions) *SimEditor {
	t.Helper()

	w, h := opts.Width, opts.Height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	cfgDir := opts.ConfigDir
	if cfgDir == "" {
		cfgDir = t.TempDir()
	}
	writeJSONIfNotNil(t, filepath.Join(cfgDir, "settings.json"), opts.Settings)
	writeJSONIfNotNil(t, filepath.Join(cfgDir, "bindings.json"), opts.Bindings)
	t.Setenv("TYPX_CONFIG_HOME", cfgDir)

	var didFirstInit bool
	simInitOnce.Do(func() {
		didFirstInit = true
		InitLuaBridge()

		if err := config.InitConfigDir(cfgDir); err != nil {
			simInitErr = err
			return
		}
		config.InitRuntimeFiles(true)
		config.InitPlugins()

		if err := config.ReadSettings(); err != nil {
			simInitErr = err
			return
		}
		if err := config.InitGlobalSettings(); err != nil {
			simInitErr = err
			return
		}
		applyGlobalSettingsOverrides(opts.Settings)

		s, err := screen.InitSimScreen()
		if err != nil {
			simInitErr = err
			return
		}
		simScreen = s
		simScreen.SetSize(w, h)
		simScreen.Show()

		m := clipboard.SetMethod("internal")
		_ = clipboard.Initialize(m)

		if err := config.LoadAllPlugins(); err != nil {
			simInitErr = err
			return
		}

		features.Init()
		lsp.Init(opts.LSPOptions)

		action.InitBindings()
		action.InitCommands()

		if err := config.RunPluginFn("preinit"); err != nil {
			simInitErr = err
			return
		}

		action.InitGlobals()
		buffer.SetMessager(action.InfoBar)
		screen.Events = make(chan tcell.Event, 256)

		bufs := openInitialBuffers(t, opts)
		action.InitTabs(bufs)

		if err := config.RunPluginFn("init"); err != nil {
			simInitErr = err
			return
		}
		if err := config.RunPluginFn("postinit"); err != nil {
			simInitErr = err
			return
		}
		if err := config.InitColorscheme(); err != nil {
			simInitErr = err
			return
		}

		go func() {
			for {
				ev := simScreen.PollEvent()
				if ev == nil {
					return
				}
				screen.Events <- ev
			}
		}()
	})

	if simInitErr != nil {
		t.Fatalf("NewSimEditor init failed: %v", simInitErr)
	}

	ed := &SimEditor{t: t, Sim: simScreen}
	if !didFirstInit {
		ed.Reset(opts)
	} else {
		ed.Sim.SetSize(w, h)
		screen.Events <- tcell.NewEventResize(w, h)
		ed.Pump(32)
		ed.Render()
	}

	t.Cleanup(func() {
		ed.cleanup()
	})
	return ed
}

func (e *SimEditor) cleanup() {
	drainChannels()
	buffer.CloseOpenBuffers()
	buffer.FlushObservers()
	action.DrainUIQueue()
	if action.InfoBar != nil {
		action.InfoBar.Reset()
	}
	if e.Sim != nil {
		e.Sim.SetSize(80, 24)
		e.Sim.Clear()
	}
}

// Reset resets open buffers, config settings, colorscheme, InfoBar, and screen size
// without re-running one-time global registrations.
func (e *SimEditor) Reset(opts SimOptions) {
	e.t.Helper()

	w, h := opts.Width, opts.Height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	drainChannels()
	buffer.CloseOpenBuffers()
	buffer.FlushObservers()
	action.DrainUIQueue()

	cfgDir := opts.ConfigDir
	if cfgDir == "" {
		cfgDir = e.t.TempDir()
	}
	writeJSONIfNotNil(e.t, filepath.Join(cfgDir, "settings.json"), opts.Settings)
	config.ConfigDir = cfgDir
	e.t.Setenv("TYPX_CONFIG_HOME", cfgDir)

	if err := config.ReadSettings(); err != nil {
		e.t.Fatalf("Reset ReadSettings: %v", err)
	}
	if err := config.InitGlobalSettings(); err != nil {
		e.t.Fatalf("Reset InitGlobalSettings: %v", err)
	}
	applyGlobalSettingsOverrides(opts.Settings)

	if err := config.InitColorscheme(); err != nil {
		e.t.Fatalf("Reset InitColorscheme: %v", err)
	}

	action.InitGlobals()
	buffer.SetMessager(action.InfoBar)

	e.Sim.SetSize(w, h)
	e.Sim.Show()
	e.Sim.Clear()

	bufs := openInitialBuffers(e.t, opts)
	action.InitTabs(bufs)

	screen.Events <- tcell.NewEventResize(w, h)
	e.Pump(32)
	e.Render()
}

func drainChannels() {
	for {
		select {
		case <-screen.Events:
		default:
			goto drainedEvents
		}
	}
drainedEvents:
	for len(screen.DrawChan()) > 0 {
		<-screen.DrawChan()
	}
}

// Render flushes buffer observers and draws the full TypX UI onto SimulationScreen.
func (e *SimEditor) Render() {
	buffer.FlushObservers()
	screen.Screen.Fill(' ', config.DefStyle)
	screen.Screen.HideCursor()
	action.Tabs.Display()
	for _, ep := range action.MainTab().Panes {
		ep.Display()
	}
	action.MainTab().Display()
	action.InfoBar.Display()
	screen.Screen.Show()
}

// Step processes one batch of pending UIQueue callbacks, DrawChan signals, or screen.Events
// and renders if any state changed. Returns true if work was performed.
func (e *SimEditor) Step() bool {
	didWork := false

	if n := action.DrainUIQueue(); n > 0 {
		didWork = true
	}

	for len(screen.DrawChan()) > 0 {
		<-screen.DrawChan()
		didWork = true
	}

	select {
	case ev := <-screen.Events:
		didWork = true
		if ev != nil {
			if _, resize := ev.(*tcell.EventResize); resize {
				action.InfoBar.HandleEvent(ev)
				action.Tabs.HandleEvent(ev)
			} else if action.InfoBar.HasPrompt {
				action.InfoBar.HandleEvent(ev)
			} else if action.HandleBottomBarEvent(ev) {
				// Consumed by the bottom bar or expanded key menu.
			} else {
				action.Tabs.HandleEvent(ev)
			}
		}
		_ = config.RunPluginFn("onAnyEvent")
	default:
	}

	if didWork {
		e.Render()
	}
	return didWork
}

// Pump runs Step() up to maxEvents times until idle, then performs a final Render.
func (e *SimEditor) Pump(maxEvents int) {
	if maxEvents <= 0 {
		maxEvents = 64
	}
	for i := 0; i < maxEvents; i++ {
		if !e.Step() {
			break
		}
	}
	e.Render()
}

// PumpUntil pumps events and waits until pred() returns true or timeout expires.
func (e *SimEditor) PumpUntil(timeout time.Duration, pred func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		e.Pump(32)
		if pred() {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		wait := 10 * time.Millisecond
		if remaining < wait {
			wait = remaining
		}
		select {
		case <-action.UIQueueReady():
		case <-screen.DrawChan():
		case <-time.After(wait):
		}
	}
}

// InjectKey sends a key event and pumps pending editor events.
func (e *SimEditor) InjectKey(key tcell.Key, r rune, mod tcell.ModMask) {
	screen.Events <- tcell.NewEventKey(key, r, mod, "")
	e.Pump(32)
}

// InjectRune sends a single rune key event and pumps pending editor events.
func (e *SimEditor) InjectRune(r rune) {
	e.InjectKey(tcell.KeyRune, r, tcell.ModNone)
}

// InjectString sends each rune in s sequentially (mapping '\n' -> KeyEnter, '\t' -> KeyTab).
func (e *SimEditor) InjectString(s string) {
	for _, r := range s {
		switch r {
		case '\n':
			e.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
		case '\t':
			e.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
		default:
			e.InjectRune(r)
		}
	}
}

// InjectMouse sends a mouse event and pumps pending editor events.
func (e *SimEditor) InjectMouse(x, y int, buttons tcell.ButtonMask, mod tcell.ModMask) {
	screen.Events <- tcell.NewEventMouse(x, y, buttons, mod, "")
	e.Pump(32)
}

// Resize resizes the simulated terminal and pumps the resulting resize event.
func (e *SimEditor) Resize(w, h int) {
	e.Sim.SetSize(w, h)
	e.Sim.Show()
	screen.Events <- tcell.NewEventResize(w, h)
	e.Pump(32)
}

// RunCommand executes a TypX command string (without leading ':') on the current BufPane.
func (e *SimEditor) RunCommand(cmd string) {
	e.Pane().HandleCommand(cmd)
	e.Pump(32)
}

// Pane returns the currently active BufPane.
func (e *SimEditor) Pane() *action.BufPane {
	return action.MainTab().CurPane()
}

// Buf returns the buffer of the currently active BufPane.
func (e *SimEditor) Buf() *buffer.Buffer {
	return e.Pane().Buf
}

// BufView returns the display.View of the currently active BufPane.
func (e *SimEditor) BufView() display.View {
	return e.Pane().BufView()
}

// Cursor returns the active cursor location in the current buffer.
func (e *SimEditor) Cursor() buffer.Loc {
	return e.Buf().GetActiveCursor().Loc
}

// ScreenCell returns the cell content and style at screen coordinates (x, y).
func (e *SimEditor) ScreenCell(x, y int) (mainc rune, combc []rune, style tcell.Style, width int) {
	return e.Sim.GetContent(x, y)
}

// ScreenLine returns the rendered text of row y with trailing spaces trimmed.
func (e *SimEditor) ScreenLine(y int) string {
	w, h := e.Sim.Size()
	if y < 0 || y >= h {
		return ""
	}
	var b strings.Builder
	for x := 0; x < w; {
		mainc, combc, _, width := e.Sim.GetContent(x, y)
		if mainc == 0 {
			mainc = ' '
		}
		b.WriteRune(mainc)
		for _, cr := range combc {
			b.WriteRune(cr)
		}
		if width < 1 {
			width = 1
		}
		x += width
	}
	return strings.TrimRight(b.String(), " ")
}

// AllScreenLines returns all rendered rows on the SimulationScreen.
func (e *SimEditor) AllScreenLines() []string {
	_, h := e.Sim.Size()
	lines := make([]string, h)
	for y := 0; y < h; y++ {
		lines[y] = e.ScreenLine(y)
	}
	return lines
}

// StatusLineText returns the rendered statusline text for the active BufPane.
func (e *SimEditor) StatusLineText() string {
	v := e.Pane().GetView()
	return e.ScreenLine(v.Y + v.Height - 1)
}

// InfoBarText returns the rendered InfoBar line at the bottom of the screen.
func (e *SimEditor) InfoBarText() string {
	_, h := e.Sim.Size()
	return e.ScreenLine(h - 1)
}

// InfoBarState returns the current InfoBar message and flags.
func (e *SimEditor) InfoBarState() (msg string, hasPrompt, hasMessage, hasError bool) {
	if action.InfoBar == nil {
		return "", false, false, false
	}
	return action.InfoBar.Msg, action.InfoBar.HasPrompt, action.InfoBar.HasMessage, action.InfoBar.HasError
}

// WaitDiagnostics pumps events until at least minCount buffer messages exist or timeout expires.
func (e *SimEditor) WaitDiagnostics(timeout time.Duration, minCount int) []*buffer.Message {
	e.t.Helper()
	ok := e.PumpUntil(timeout, func() bool {
		return len(e.Buf().Messages) >= minCount
	})
	if !ok {
		e.t.Fatalf("timed out waiting for >= %d diagnostics (got %d)", minCount, len(e.Buf().Messages))
	}
	return e.Buf().Messages
}

// WaitNoDiagnostics pumps events until all buffer messages are cleared or timeout expires.
func (e *SimEditor) WaitNoDiagnostics(timeout time.Duration) {
	e.t.Helper()
	ok := e.PumpUntil(timeout, func() bool {
		return len(e.Buf().Messages) == 0
	})
	if !ok {
		e.t.Fatalf("timed out waiting for diagnostics to clear (still have %d)", len(e.Buf().Messages))
	}
}
