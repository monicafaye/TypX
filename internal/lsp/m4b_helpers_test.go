package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	ulua "github.com/monicafaye/TypX/internal/lua"
	"github.com/monicafaye/TypX/internal/screen"
	lua "github.com/yuin/gopher-lua"
)

type m4bFakeServerConfig struct {
	DocumentHighlightProvider any
	SelectionRangeProvider    any
	OnDocumentHighlight       func(params DocumentHighlightParams) (any, *RPCError)
	OnSelectionRange          func(params SelectionRangeParams) (any, *RPCError)
}

type m4bFakeServer struct {
	t   *testing.T
	cfg m4bFakeServerConfig

	mu      sync.Mutex
	writeMu sync.Mutex
	srvW    *io.PipeWriter
	cliR    *io.PipeReader
	closed  bool

	methods []string
	notifs  []string
}

func (s *m4bFakeServer) sendFrame(v any) {
	frame, err := encodeFrame(v)
	if err != nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	closed := s.closed
	w := s.srvW
	s.mu.Unlock()
	if closed || w == nil {
		return
	}
	_, _ = w.Write(frame)
}

func (s *m4bFakeServer) SendNotification(method string, params any) {
	s.sendFrame(rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}

func (s *m4bFakeServer) DropConnection() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		if s.srvW != nil {
			_ = s.srvW.Close()
		}
		if s.cliR != nil {
			_ = s.cliR.Close()
		}
	}
	s.mu.Unlock()
}

func (s *m4bFakeServer) Methods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.methods...)
}

func (s *m4bFakeServer) CountMethod(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.methods {
		if m == method {
			n++
		}
	}
	return n
}

func (s *m4bFakeServer) serve() {
	br := bufio.NewReader(s.cliR)
	for {
		payload, err := readFrame(br)
		if err != nil {
			return
		}
		var req rpcIncoming
		if uerr := json.Unmarshal(payload, &req); uerr != nil {
			continue
		}
		if len(req.ID) == 0 {
			s.mu.Lock()
			s.notifs = append(s.notifs, req.Method)
			s.mu.Unlock()
			if req.Method == "exit" {
				s.DropConnection()
				return
			}
			continue
		}

		s.mu.Lock()
		s.methods = append(s.methods, req.Method)
		s.mu.Unlock()

		switch req.Method {
		case "initialize":
			res := InitializeResult{
				ServerInfo: &ServerInfo{Name: "m4b-fake-tinymist", Version: "0.0.1"},
				Capabilities: ServerCapabilities{
					TextDocumentSync:          TextDocumentSyncKindFull,
					DocumentHighlightProvider: s.cfg.DocumentHighlightProvider,
					SelectionRangeProvider:    s.cfg.SelectionRangeProvider,
				},
			}
			s.sendFrame(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: res})
		case "shutdown":
			s.sendFrame(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: nil})
		case "textDocument/documentHighlight":
			var p DocumentHighlightParams
			_ = json.Unmarshal(req.Params, &p)
			if s.cfg.OnDocumentHighlight != nil {
				idCopy := append(json.RawMessage(nil), req.ID...)
				go func(params DocumentHighlightParams, id json.RawMessage) {
					res, rpcErr := s.cfg.OnDocumentHighlight(params)
					s.sendFrame(rpcResponse{JSONRPC: "2.0", ID: id, Result: res, Error: rpcErr})
				}(p, idCopy)
			} else {
				s.sendFrame(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: nil})
			}
		case "textDocument/selectionRange":
			var p SelectionRangeParams
			_ = json.Unmarshal(req.Params, &p)
			if s.cfg.OnSelectionRange != nil {
				idCopy := append(json.RawMessage(nil), req.ID...)
				go func(params SelectionRangeParams, id json.RawMessage) {
					res, rpcErr := s.cfg.OnSelectionRange(params)
					s.sendFrame(rpcResponse{JSONRPC: "2.0", ID: id, Result: res, Error: rpcErr})
				}(p, idCopy)
			} else {
				s.sendFrame(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: nil})
			}
		default:
			s.sendFrame(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: nil})
		}
	}
}

func setupM4bPipeEnv(t *testing.T, debounceMs float64, cfg m4bFakeServerConfig) (string, *m4bFakeServer, func() *m4bFakeServer) {
	t.Helper()

	dir := t.TempDir()
	origConfigDir := config.ConfigDir
	origGlobalSettings := config.GlobalSettings
	origLuaL := ulua.L
	origTabs := action.Tabs

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
	config.GlobalSettings["typst.tinymistPath"] = ""
	config.GlobalSettings["typst.debounceMs"] = debounceMs
	config.GlobalSettings["typst.previewOpenBrowser"] = false
	config.GlobalSettings["typst.documentHighlight"] = true
	_ = config.InitColorscheme()

	drainM4bUI(t)

	origScreen := screen.Screen
	simScreen, err := screen.InitSimScreen()
	require.NoError(t, err)
	simScreen.SetSize(80, 24)
	var finiOnce sync.Once

	var srvMu sync.Mutex
	var servers []*m4bFakeServer
	latestServer := func() *m4bFakeServer {
		srvMu.Lock()
		defer srvMu.Unlock()
		if len(servers) == 0 {
			return nil
		}
		return servers[len(servers)-1]
	}

	defaultManager.mu.Lock()
	origBackoff := defaultManager.restartBackoff
	origDiscover := defaultManager.discoverFn
	origProbe := defaultManager.probeFn
	origStart := defaultManager.startClientFn
	defaultManager.restartBackoff = 20 * time.Millisecond
	defaultManager.discoverFn = func(string) DiscoveryResult {
		return DiscoveryResult{Path: "m4b-pipe", Source: "test-pipe"}
	}
	defaultManager.probeFn = nil
	defaultManager.startClientFn = func(ctx context.Context, _ string, h Handlers) (*Client, error) {
		srvToCliR, srvToCliW := io.Pipe()
		cliToSrvR, cliToSrvW := io.Pipe()
		srv := &m4bFakeServer{
			t:    t,
			cfg:  cfg,
			srvW: srvToCliW,
			cliR: cliToSrvR,
		}
		srvMu.Lock()
		servers = append(servers, srv)
		srvMu.Unlock()
		go srv.serve()
		return NewPipeClient(srvToCliR, cliToSrvW, h), nil
	}
	defaultManager.mu.Unlock()

	// Seed the first server reference placeholder via getter; callers use latestServer() after Init.
	t.Cleanup(func() {
		Shutdown()
		srvMu.Lock()
		all := append([]*m4bFakeServer(nil), servers...)
		srvMu.Unlock()
		for _, s := range all {
			s.DropConnection()
		}
		defaultManager.mu.Lock()
		defaultManager.restartBackoff = origBackoff
		defaultManager.discoverFn = origDiscover
		defaultManager.probeFn = origProbe
		defaultManager.startClientFn = origStart
		defaultManager.mu.Unlock()

		buffer.CloseOpenBuffers()
		action.Tabs = origTabs
		drainM4bUI(t)
		finiOnce.Do(func() {
			simScreen.Fini()
			if screen.Screen == simScreen {
				screen.Screen = origScreen
			}
		})
		if ulua.L != nil && ulua.L != origLuaL {
			ulua.L.Close()
		}
		ulua.L = origLuaL
		config.ConfigDir = origConfigDir
		config.GlobalSettings = origGlobalSettings
	})

	return dir, nil, latestServer
}

func drainM4bUI(t *testing.T) {
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

func waitForM4b(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		drainM4bUI(t)
		buffer.FlushObservers()
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	drainM4bUI(t)
	buffer.FlushObservers()
	if !cond() {
		t.Fatalf("timed out waiting for %s", msg)
	}
}

func openM4bBuffer(t *testing.T, path, content string) *buffer.Buffer {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	b, err := buffer.NewBufferFromFile(path, buffer.BTDefault)
	require.NoError(t, err)
	if strings.HasSuffix(strings.ToLower(path), ".typ") && !b.IsTypst() {
		t.Fatalf("expected %s to be recognized as typst filetype (got %q)", path, b.FileType())
	}
	return b
}
