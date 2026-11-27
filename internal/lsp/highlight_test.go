package lsp

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
)

func TestDocumentHighlightDebouncedUTF16AndClear(t *testing.T) {
	var reqCount atomic.Int32
	dir, _, latestSrv := setupM4bPipeEnv(t, 0, m4bFakeServerConfig{
		DocumentHighlightProvider: true,
		OnDocumentHighlight: func(params DocumentHighlightParams) (any, *RPCError) {
			reqCount.Add(1)
			// Line 0: "a😀b foo = foo" -> first "foo" at UTF-16 [5, 8), second at [11, 14)
			if params.Position.Line == 0 && params.Position.Character >= 5 && params.Position.Character <= 8 {
				return []DocumentHighlight{
					{
						Range: Range{Start: Position{Line: 0, Character: 5}, End: Position{Line: 0, Character: 8}},
						Kind:  DocumentHighlightKindWrite,
					},
					{
						Range: Range{Start: Position{Line: 0, Character: 11}, End: Position{Line: 0, Character: 14}},
						Kind:  DocumentHighlightKindRead,
					},
				}, nil
			}
			return nil, nil
		},
	})

	mainPath := filepath.Join(dir, "main.typ")
	Init(Options{InitialFiles: []string{mainPath}})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForM4b(t, 3*time.Second, func() bool {
		return defaultManager.State() == StateReady && latestSrv() != nil
	}, "StateReady")
	require.False(t, IsPreviewActive())

	b := openM4bBuffer(t, mainPath, "a😀b foo = foo\n")
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()

	// Move cursor to "foo" (rune col 4 = Loc.X 4, UTF-16 col 5) while preview is OFF.
	bp.Cursor.GotoLoc(buffer.Loc{X: 4, Y: 0})
	buffer.FlushObservers()

	waitForM4b(t, 2*time.Second, func() bool {
		return len(b.SymbolHighlights) == 2
	}, "symbol highlights populated")
	assert.Equal(t, []buffer.HighlightRange{
		{Start: buffer.Loc{X: 4, Y: 0}, End: buffer.Loc{X: 7, Y: 0}},
		{Start: buffer.Loc{X: 10, Y: 0}, End: buffer.Loc{X: 13, Y: 0}},
	}, b.SymbolHighlights)

	// Move cursor off symbol -> nil result clears highlights.
	bp.Cursor.GotoLoc(buffer.Loc{X: 0, Y: 0})
	buffer.FlushObservers()
	waitForM4b(t, 2*time.Second, func() bool {
		return reqCount.Load() >= 2 && len(b.SymbolHighlights) == 0
	}, "highlights cleared off symbol")
}

func TestDocumentHighlightStaleVersionAndSeqDiscarded(t *testing.T) {
	inFlight := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	dir, _, latestSrv := setupM4bPipeEnv(t, 0, m4bFakeServerConfig{
		DocumentHighlightProvider: true,
		OnDocumentHighlight: func(params DocumentHighlightParams) (any, *RPCError) {
			select {
			case inFlight <- struct{}{}:
			default:
			}
			<-release
			return []DocumentHighlight{
				{Range: Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 3}}},
			}, nil
		},
	})

	mainPath := filepath.Join(dir, "main.typ")
	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()
	waitForM4b(t, 3*time.Second, func() bool {
		return defaultManager.State() == StateReady && latestSrv() != nil
	}, "StateReady")

	b := openM4bBuffer(t, mainPath, "foo bar\n")
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()

	bp.Cursor.GotoLoc(buffer.Loc{X: 1, Y: 0})
	buffer.FlushObservers()

	waitForM4b(t, 2*time.Second, func() bool {
		select {
		case <-inFlight:
			return true
		default:
			return false
		}
	}, "in-flight highlight request")

	// Edit buffer while request is in flight -> version bumps and highlights must not apply.
	b.Insert(buffer.Loc{X: 0, Y: 0}, "z")
	buffer.FlushObservers()
	releaseOnce.Do(func() { close(release) })

	time.Sleep(80 * time.Millisecond)
	drainM4bUI(t)
	assert.Empty(t, b.SymbolHighlights)
}

func TestDocumentHighlightLifecycleClearsAndSettingDisable(t *testing.T) {
	var reqCount atomic.Int32
	dir, _, latestSrv := setupM4bPipeEnv(t, 0, m4bFakeServerConfig{
		DocumentHighlightProvider: true,
		OnDocumentHighlight: func(params DocumentHighlightParams) (any, *RPCError) {
			reqCount.Add(1)
			return []DocumentHighlight{
				{Range: Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 3}}},
			}, nil
		},
	})

	mainPath := filepath.Join(dir, "main.typ")
	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()
	waitForM4b(t, 3*time.Second, func() bool {
		return defaultManager.State() == StateReady && latestSrv() != nil
	}, "StateReady")

	b := openM4bBuffer(t, mainPath, "foo bar\n")
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()

	applyHighlights := func() {
		nextX := 2
		if bp.Cursor.Loc.X == 2 {
			nextX = 1
		}
		bp.Cursor.GotoLoc(buffer.Loc{X: nextX, Y: 0})
		buffer.FlushObservers()
		waitForM4b(t, 2*time.Second, func() bool {
			return len(b.SymbolHighlights) == 1
		}, "highlights applied")
	}

	// 1. Setting typst.documentHighlight = false clears immediately on cursor move and skips RPC.
	applyHighlights()
	before := reqCount.Load()
	config.GlobalSettings["typst.documentHighlight"] = false
	bp.Cursor.GotoLoc(buffer.Loc{X: 1, Y: 0})
	buffer.FlushObservers()
	assert.Empty(t, b.SymbolHighlights)
	time.Sleep(220 * time.Millisecond)
	drainM4bUI(t)
	assert.Equal(t, before, reqCount.Load())
	config.GlobalSettings["typst.documentHighlight"] = true

	// 2. SaveAs away from .typ clears highlights.
	applyHighlights()
	txtPath := filepath.Join(dir, "notes.txt")
	require.NoError(t, b.SaveAs(txtPath))
	buffer.FlushObservers()
	assert.Empty(t, b.SymbolHighlights)

	// Re-open typst buffer.
	b2 := openM4bBuffer(t, mainPath, "foo bar\n")
	bp.OpenBuffer(b2)
	b = b2
	applyHighlights()

	// 3. OnBufferClose when Shared() is true does NOT clear; when !Shared() DOES clear.
	bShared, err := buffer.NewBufferFromFile(mainPath, buffer.BTDefault)
	require.NoError(t, err)
	_ = bShared
	require.True(t, b.Shared())
	defaultManager.OnBufferClose(b)
	assert.Len(t, b.SymbolHighlights, 1)

	b.SetSymbolHighlights([]buffer.HighlightRange{{Start: buffer.Loc{X: 0, Y: 0}, End: buffer.Loc{X: 3, Y: 0}}})
	// Closing bShared makes b unshared; now closing b clears highlights.
	for _, ob := range append([]*buffer.Buffer(nil), buffer.OpenBuffers...) {
		if ob != b && ob.AbsPath == b.AbsPath {
			ob.Close()
		}
	}
	require.False(t, b.Shared())
	defaultManager.OnBufferClose(b)
	assert.Empty(t, b.SymbolHighlights)

	// 4. watchClient crash clears highlights via EnqueueUI.
	applyHighlights()
	latestSrv().DropConnection()
	waitForM4b(t, 3*time.Second, func() bool {
		return len(b.SymbolHighlights) == 0 && defaultManager.State() == StateReady
	}, "highlights cleared on crash and recovered")

	// 5. restart() clears highlights.
	applyHighlights()
	defaultManager.restart()
	assert.Empty(t, b.SymbolHighlights)
	waitForM4b(t, 3*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "ready after restart")

	// 6. Shutdown() from a non-main goroutine clears highlights via EnqueueUI without panic.
	applyHighlights()
	done := make(chan struct{})
	go func() {
		defer close(done)
		Shutdown()
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown timed out")
	}
	drainM4bUI(t)
	assert.Empty(t, b.SymbolHighlights)
}

func TestDocumentHighlightCapabilityAnd32601AndPreviewIndependence(t *testing.T) {
	// Case A: Server advertises DocumentHighlightProvider: false -> no requests sent.
	var countA atomic.Int32
	dirA, _, srvA := setupM4bPipeEnv(t, 0, m4bFakeServerConfig{
		DocumentHighlightProvider: false,
		OnDocumentHighlight: func(params DocumentHighlightParams) (any, *RPCError) {
			countA.Add(1)
			return nil, nil
		},
	})
	pathA := filepath.Join(dirA, "a.typ")
	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()
	waitForM4b(t, 3*time.Second, func() bool {
		return defaultManager.State() == StateReady && srvA() != nil
	}, "StateReady A")
	bA := openM4bBuffer(t, pathA, "foo\n")
	action.InitTabs([]*buffer.Buffer{bA})
	action.MainTab().CurPane().Cursor.GotoLoc(buffer.Loc{X: 1, Y: 0})
	buffer.FlushObservers()
	time.Sleep(220 * time.Millisecond)
	drainM4bUI(t)
	assert.Equal(t, int32(0), countA.Load())
	Shutdown()

	// Case B: Server returns -32601 -> disables further requests; preview cursorTimer stays independent.
	var countB atomic.Int32
	dirB, _, srvB := setupM4bPipeEnv(t, 0, m4bFakeServerConfig{
		DocumentHighlightProvider: true,
		OnDocumentHighlight: func(params DocumentHighlightParams) (any, *RPCError) {
			countB.Add(1)
			return nil, &RPCError{Code: -32601, Message: "method not found"}
		},
	})
	pathB := filepath.Join(dirB, "b.typ")
	Init(Options{})
	waitForM4b(t, 3*time.Second, func() bool {
		return defaultManager.State() == StateReady && srvB() != nil
	}, "StateReady B")
	bB := openM4bBuffer(t, pathB, "foo bar\n")
	action.InitTabs([]*buffer.Buffer{bB})
	bpB := action.MainTab().CurPane()
	bB.SetSymbolHighlights([]buffer.HighlightRange{{Start: buffer.Loc{X: 0, Y: 0}, End: buffer.Loc{X: 3, Y: 0}}})

	// Enable preview flag and move cursor: cursorTimer + highlightTimer both fire independently.
	defaultManager.mu.Lock()
	defaultManager.previewActive = true
	defaultManager.mu.Unlock()
	defaultManager.atomicPreview.Store(true)

	bpB.Cursor.GotoLoc(buffer.Loc{X: 1, Y: 0})
	buffer.FlushObservers()

	waitForM4b(t, 2*time.Second, func() bool {
		return countB.Load() == 1 && len(bB.SymbolHighlights) == 0 && srvB().CountMethod("workspace/executeCommand") >= 1
	}, "32601 clears highlights and preview scroll runs independently")

	// Next cursor move should NOT issue another documentHighlight request.
	bpB.Cursor.GotoLoc(buffer.Loc{X: 2, Y: 0})
	buffer.FlushObservers()
	time.Sleep(220 * time.Millisecond)
	drainM4bUI(t)
	assert.Equal(t, int32(1), countB.Load())
	assert.False(t, strings.Contains(action.InfoBar.Msg, "32601"))
}
