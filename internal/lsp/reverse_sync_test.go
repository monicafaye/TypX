package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
)

func TestParsePreviewScrollSource(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		wantOK bool
		want   previewScrollTarget
	}{
		{
			name:   "flat filepath with line and character",
			raw:    `{"taskId":"default_preview","filepath":"/tmp/doc.typ","line":3,"character":7}`,
			wantOK: true,
			want:   previewScrollTarget{TaskID: "default_preview", FilePath: "/tmp/doc.typ", Pos: Position{Line: 3, Character: 7}},
		},
		{
			name:   "uri with percent encoding and omitted taskId",
			raw:    `{"uri":"file:///tmp/my%20doc.typ","line":1,"character":4}`,
			wantOK: true,
			want:   previewScrollTarget{FilePath: filepath.FromSlash("/tmp/my doc.typ"), Pos: Position{Line: 1, Character: 4}},
		},
		{
			name:   "relative path with col and column aliases",
			raw:    `{"path":"chapters/ch1.typ","line":5,"col":2}`,
			wantOK: true,
			want:   previewScrollTarget{FilePath: "chapters/ch1.typ", Pos: Position{Line: 5, Character: 2}},
		},
		{
			name:   "flat column alias",
			raw:    `{"filepath":"/a.typ","line":2,"column":9}`,
			wantOK: true,
			want:   previewScrollTarget{FilePath: "/a.typ", Pos: Position{Line: 2, Character: 9}},
		},
		{
			name:   "nested start object",
			raw:    `{"filepath":"/a.typ","start":{"line":4,"character":6},"end":{"line":4,"character":10}}`,
			wantOK: true,
			want:   previewScrollTarget{FilePath: "/a.typ", Pos: Position{Line: 4, Character: 6}},
		},
		{
			name:   "nested start array",
			raw:    `{"filepath":"/a.typ","start":[7,3]}`,
			wantOK: true,
			want:   previewScrollTarget{FilePath: "/a.typ", Pos: Position{Line: 7, Character: 3}},
		},
		{
			name:   "nested position object and array",
			raw:    `{"filepath":"/a.typ","position":[8,1]}`,
			wantOK: true,
			want:   previewScrollTarget{FilePath: "/a.typ", Pos: Position{Line: 8, Character: 1}},
		},
		{
			name:   "nested range start",
			raw:    `{"filepath":"/a.typ","range":{"start":{"line":2,"character":5},"end":{"line":2,"character":9}}}`,
			wantOK: true,
			want:   previewScrollTarget{FilePath: "/a.typ", Pos: Position{Line: 2, Character: 5}},
		},
		{
			name:   "nested location",
			raw:    `{"location":{"uri":"file:///tmp/nested.typ","range":{"start":[6,4],"end":[6,8]}}}`,
			wantOK: true,
			want:   previewScrollTarget{FilePath: filepath.FromSlash("/tmp/nested.typ"), Pos: Position{Line: 6, Character: 4}},
		},
		{
			name:   "negative coordinates clamped to zero",
			raw:    `{"filepath":"/a.typ","line":-3,"character":-5}`,
			wantOK: true,
			want:   previewScrollTarget{FilePath: "/a.typ", Pos: Position{Line: 0, Character: 0}},
		},
		{
			name:   "mismatched taskId ignored",
			raw:    `{"taskId":"other_task","filepath":"/a.typ","line":1,"character":1}`,
			wantOK: false,
		},
		{
			name:   "missing file ignored",
			raw:    `{"line":1,"character":1}`,
			wantOK: false,
		},
		{
			name:   "missing position ignored",
			raw:    `{"filepath":"/a.typ"}`,
			wantOK: false,
		},
		{
			name:   "null and invalid json ignored",
			raw:    `null`,
			wantOK: false,
		},
		{
			name:   "malformed json ignored",
			raw:    `{not-json`,
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parsePreviewScrollSource(json.RawMessage(tc.raw))
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestReverseSyncNotificationUTF16ModesAndGuards(t *testing.T) {
	dir, _, latestSrv := setupM4bPipeEnv(t, 0, m4bFakeServerConfig{})
	mainPath := filepath.Join(dir, "main.typ")
	subDir := filepath.Join(dir, "sections")
	require.NoError(t, os.MkdirAll(subDir, 0755))
	subPath := filepath.Join(subDir, "part.typ")
	require.NoError(t, os.WriteFile(subPath, []byte("// part line 0\n= Part Heading\na😀b target\n"), 0644))
	otherPath := filepath.Join(dir, "other.typ")
	require.NoError(t, os.WriteFile(otherPath, []byte("// other 0\n// other 1\n"), 0644))

	Init(Options{InitialFiles: []string{mainPath}})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForM4b(t, 3*time.Second, func() bool {
		return defaultManager.State() == StateReady && latestSrv() != nil
	}, "pipe manager StateReady")

	srv := latestSrv()
	bMain := openM4bBuffer(t, mainPath, "line 0\nline 1\na😀b rest\n")
	action.InitTabs([]*buffer.Buffer{bMain})
	bpMain := action.MainTab().CurPane()
	// Give main buffer an active selection and extra cursor to confirm cleanup on reverse sync.
	bpMain.Cursor.SetSelectionStart(buffer.Loc{X: 0, Y: 0})
	bpMain.Cursor.SetSelectionEnd(buffer.Loc{X: 4, Y: 0})
	extra := buffer.NewCursor(bMain, buffer.Loc{X: 1, Y: 1})
	bMain.AddCursor(extra)
	require.Equal(t, 2, bMain.NumCursors())

	// Open a ListPane as auxiliary pane focus to ensure mt.CurPane() still resolves bpMain.
	OpenListPane(bpMain, "Aux", false, []ListItem{{Primary: "item"}})
	require.NotNil(t, ListPaneForTab(action.MainTab()))

	// 1. Same-file tinymist/preview/scrollSource with non-BMP UTF-16 col=3 -> Loc.X=2.
	srv.SendNotification("tinymist/preview/scrollSource", map[string]any{
		"taskId":    previewTaskID,
		"filepath":  mainPath,
		"line":      2,
		"character": 3,
	})
	waitForM4b(t, 3*time.Second, func() bool {
		return bpMain.Cursor.Loc == (buffer.Loc{X: 2, Y: 2})
	}, "same-file UTF-16 reverse sync cursor jump")
	assert.False(t, bpMain.Cursor.HasSelection())
	assert.Equal(t, 1, bMain.NumCursors())

	// 2. Cross-file editorScrollTo with relative path resolved against rootURI and gotoOpenMode="tab".
	config.GlobalSettings["typst.gotoOpenMode"] = "tab"
	srv.SendNotification("editorScrollTo", map[string]any{
		"filepath": "sections/part.typ",
		"range": map[string]any{
			"start": map[string]any{"line": 2, "character": 3},
		},
	})
	waitForM4b(t, 3*time.Second, func() bool {
		return len(action.Tabs.List) == 2 && canonicalDocKey(action.MainTab().CurPane().Buf.AbsPath) == canonicalDocKey(subPath)
	}, "cross-file editorScrollTo opening tab")
	assert.Equal(t, buffer.Loc{X: 2, Y: 2}, action.MainTab().CurPane().Cursor.Loc)

	// 3. Stale generation notification must be ignored.
	defaultManager.mu.Lock()
	curGen := defaultManager.gen
	defaultManager.mu.Unlock()
	defaultManager.handleNotification(curGen-1, "editorScrollTo", []byte(`{"filepath":`+jsonQuote(otherPath)+`,"line":1,"character":0}`))
	drainM4bUI(t)
	assert.Equal(t, 2, len(action.Tabs.List))

	// 4. vsplit mode opens in split pane.
	config.GlobalSettings["typst.gotoOpenMode"] = "vsplit"
	srv.SendNotification("tinymist/preview/scrollSource", map[string]any{
		"uri":      PathToURI(otherPath),
		"position": []int{1, 2},
	})
	waitForM4b(t, 3*time.Second, func() bool {
		return canonicalDocKey(action.MainTab().CurPane().Buf.AbsPath) == canonicalDocKey(otherPath)
	}, "cross-file vsplit reverse sync")
	assert.Equal(t, buffer.Loc{X: 2, Y: 1}, action.MainTab().CurPane().Cursor.Loc)
	assert.GreaterOrEqual(t, len(action.MainTab().Panes), 2)

	// 5. Empty Tabs.List / Tabs == nil safety does not panic.
	savedTabs := action.Tabs
	action.Tabs = nil
	defaultManager.handleNotification(curGen, "editorScrollTo", []byte(`{"filepath":`+jsonQuote(mainPath)+`,"line":0,"character":0}`))
	drainM4bUI(t)
	action.Tabs = savedTabs
}

func TestOpenFileAtRuneColsGotoModesAndNoLeak(t *testing.T) {
	dir, _, _ := setupM4bPipeEnv(t, 0, m4bFakeServerConfig{})
	mainPath := filepath.Join(dir, "main.typ")
	subPath := filepath.Join(dir, "sub.typ")
	combPath := filepath.Join(dir, "comb.typ")
	require.NoError(t, os.WriteFile(subPath, []byte("line 0\na😀b end\n"), 0644))
	require.NoError(t, os.WriteFile(combPath, []byte("e\u0301x\n"), 0644))

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	bMain := openM4bBuffer(t, mainPath, "// main\n")
	action.InitTabs([]*buffer.Buffer{bMain})
	bpMain := action.MainTab().CurPane()

	// Empty / nonexistent paths return nil cleanly.
	assert.Nil(t, OpenFileAt(bpMain, "", 0, 0))
	assert.Nil(t, OpenFileAt(bpMain, filepath.Join(dir, "missing.typ"), 0, 0))

	// Rune column 2 on "a😀b end" lands on 'b' (Loc.X == 2), distinct from UTF-16 col 3.
	config.GlobalSettings["typst.gotoOpenMode"] = "tab"
	OpenListPane(bpMain, "Aux", false, []ListItem{{Primary: "x"}})
	dest := OpenFileAt(nil, subPath, 1, 2)
	require.NotNil(t, dest)
	assert.Equal(t, canonicalDocKey(subPath), canonicalDocKey(dest.Buf.AbsPath))
	assert.Equal(t, buffer.Loc{X: 2, Y: 1}, dest.Cursor.Loc)

	// Current mode replaces unmodified buffer.
	config.GlobalSettings["typst.gotoOpenMode"] = "current"
	destComb := OpenFileAt(dest, combPath, 0, 2)
	require.NotNil(t, destComb)
	assert.Equal(t, dest, destComb)
	assert.Equal(t, canonicalDocKey(combPath), canonicalDocKey(destComb.Buf.AbsPath))
	assert.Equal(t, buffer.Loc{X: 1, Y: 0}, destComb.Cursor.Loc)

	// Guard: action.Tabs == nil with bp == nil returns nil without leaking an orphan buffer.
	savedTabs := action.Tabs
	action.Tabs = nil
	beforeCount := len(buffer.OpenBuffers)
	unopenedPath := filepath.Join(dir, "unopened.typ")
	require.NoError(t, os.WriteFile(unopenedPath, []byte("hello\n"), 0644))
	assert.Nil(t, OpenFileAt(nil, unopenedPath, 0, 0))
	assert.Equal(t, beforeCount, len(buffer.OpenBuffers))
	action.Tabs = savedTabs
}

func TestIsOffline(t *testing.T) {
	var nilMgr *Manager
	assert.True(t, nilMgr.IsOffline())

	m := newManager()
	assert.True(t, m.IsOffline()) // StateOff
	m.atomicState.Store(int32(StateMissing))
	assert.True(t, m.IsOffline())
	for _, st := range []State{StateStarting, StateReady, StateRestarting, StateError} {
		m.atomicState.Store(int32(st))
		assert.False(t, m.IsOffline())
	}

	// Package-level IsOffline with typst.lspEnabled=false and with active pipe server.
	_, _, latestSrv := setupM4bPipeEnv(t, 0, m4bFakeServerConfig{})
	config.GlobalSettings["typst.lspEnabled"] = false
	Init(Options{})
	assert.True(t, IsOffline())

	config.GlobalSettings["typst.lspEnabled"] = true
	Init(Options{})
	waitForM4b(t, 3*time.Second, func() bool {
		return defaultManager.State() == StateReady && latestSrv() != nil
	}, "StateReady")
	assert.False(t, IsOffline())
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
