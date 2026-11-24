package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/util"
)

// State represents the lifecycle state of the tinymist LSP manager.
type State int32

const (
	StateOff State = iota
	StateStarting
	StateReady
	StateRestarting
	StateError
)

func (s State) String() string {
	switch s {
	case StateOff:
		return "off"
	case StateStarting:
		return "starting"
	case StateReady:
		return "ready"
	case StateRestarting:
		return "restarting"
	case StateError:
		return "error"
	default:
		return "off"
	}
}

const (
	diagnosticOwner       = "tinymist"
	previewTaskID         = "default_preview"
	defaultRestartBackoff = 500 * time.Millisecond
	cursorSyncDelay       = 100 * time.Millisecond
	maxCompletionItems    = 50
)

var (
	reSnippetNested = regexp.MustCompile(`\$\{\d+:([^}]*)\}`)
	reSnippetChoice = regexp.MustCompile(`\$\{\d+\|([^,|}]*)[^}]*\}`)
	reSnippetStop   = regexp.MustCompile(`\$\{?\d+\}?`)
	reParentImport  = regexp.MustCompile(`(?m)#(?:import|include)\s+"((?:\.\./)+)`)
	reRootRelImport = regexp.MustCompile(`(?m)#(?:import|include)\s+"/([^"\r\n]+)"`)
)

// CompileState tracks live Typst compilation status reported by tinymist.
type CompileState int32

const (
	CompileIdle CompileState = iota
	CompileRunning
	CompileSuccess
	CompileError
)

func (cs CompileState) String() string {
	switch cs {
	case CompileRunning:
		return "compiling"
	case CompileSuccess:
		return "ok"
	case CompileError:
		return "error"
	default:
		return "idle"
	}
}

type openDoc struct {
	key           string
	uri           string
	absPath       string
	shared        *buffer.SharedBuffer
	opened        bool
	lastQueuedVer uint64
	lastSentVer   uint64
	latestText    string
	timer         *time.Timer
}

// Manager coordinates tinymist discovery, LSP lifecycle, document sync,
// diagnostics, completion, hover, live preview, and crash recovery.
type Manager struct {
	buffer.NopObserver

	atomicState           atomic.Int32
	atomicPreview         atomic.Bool
	atomicPreviewStarting atomic.Bool
	atomicCompileState    atomic.Int32
	atomicCompilePages    atomic.Int32
	atomicSpinTick        atomic.Int32
	atomicCompileStartNs  atomic.Int64
	atomicPreviewStartNs  atomic.Int64

	mu                 sync.Mutex
	gen                uint64
	shuttingDown       bool
	stopCh             chan struct{}
	readyCh            chan struct{}
	client             *Client
	autoRestarts       int
	restartBackoff     time.Duration
	binPath            string
	binVersion         string
	binSource          string
	configuredMissing  bool
	lastErr            string
	lastStderr         []string
	lspEnabled         bool
	tinymistPath       string
	debounceMs         int
	previewOpenBrowser bool
	startPreviewPend   bool
	rootURI            string

	docs           map[string]*openDoc
	docErrorCounts map[string]int
	docErrors      map[string][]string
	serverCommands map[string]bool

	previewActive bool
	previewURL    string
	previewOpened bool
	previewMode   string
	previewRoot   string
	previewTarget string
	previewDetail string
	previewProxy  *previewProxy
	previewJob    *ProcessJob
	previewDone   chan struct{}
	cursorTimer   *time.Timer

	diagBuf   *buffer.Buffer
	hoverBuf  *buffer.Buffer
	statusBuf *buffer.Buffer

	unregObserver func()

	discoverFn    func(string) DiscoveryResult
	probeFn       func(context.Context, string) (string, error)
	startClientFn func(context.Context, string, Handlers) (*Client, error)
}

func newManager() *Manager {
	m := &Manager{
		restartBackoff: defaultRestartBackoff,
		docs:           make(map[string]*openDoc),
		docErrorCounts: make(map[string]int),
		docErrors:      make(map[string][]string),
		serverCommands: make(map[string]bool),
		discoverFn:     Discover,
		probeFn:        ProbeVersion,
		startClientFn: func(ctx context.Context, bin string, h Handlers) (*Client, error) {
			return StartProcessClient(ctx, bin, []string{"lsp"}, "", h)
		},
	}
	m.atomicState.Store(int32(StateOff))
	return m
}

func (m *Manager) setStateLocked(s State) {
	m.atomicState.Store(int32(s))
	if s != StateStarting {
		m.signalReadyLocked()
	}
}

func (m *Manager) signalReadyLocked() {
	if m.readyCh != nil {
		select {
		case <-m.readyCh:
		default:
			close(m.readyCh)
		}
	}
}

func (m *Manager) resetCompileStatusLocked() {
	m.atomicCompileState.Store(int32(CompileIdle))
	m.atomicCompilePages.Store(0)
}

// State returns the current LSP manager state.
func (m *Manager) State() State {
	return State(m.atomicState.Load())
}

// Init starts or resets the manager with the given options. Must be called on
// the main goroutine. Does not block on tinymist startup.
func (m *Manager) Init(opts Options) {
	lspEnabled := readGlobalBool("typst.lspEnabled", true)
	tinymistPath := readGlobalString("typst.tinymistPath", "")
	debounceMs := readGlobalInt("typst.debounceMs", 150)
	previewOpenBrowser := readGlobalBool("typst.previewOpenBrowser", true)

	// Tear down any prior session cleanly (important in tests).
	m.Shutdown()

	m.mu.Lock()
	m.shuttingDown = false
	m.stopCh = make(chan struct{})
	m.readyCh = make(chan struct{})
	m.gen++
	gen := m.gen
	m.autoRestarts = 0
	m.lastErr = ""
	m.lastStderr = nil
	m.binPath = ""
	m.binVersion = ""
	m.binSource = ""
	m.configuredMissing = false
	m.lspEnabled = lspEnabled
	m.tinymistPath = tinymistPath
	m.debounceMs = debounceMs
	m.previewOpenBrowser = previewOpenBrowser
	m.startPreviewPend = opts.StartPreview
	m.rootURI = initialRootURI(opts.InitialFiles)
	m.docs = make(map[string]*openDoc)
	m.docErrorCounts = make(map[string]int)
	m.docErrors = make(map[string][]string)
	m.serverCommands = make(map[string]bool)
	m.resetCompileStatusLocked()
	m.unregObserver = buffer.RegisterObserver(m)

	if !lspEnabled {
		m.setStateLocked(StateOff)
		m.mu.Unlock()
		return
	}

	m.setStateLocked(StateStarting)
	m.mu.Unlock()

	go m.startAsync(gen)
}

// Shutdown cleanly terminates any active tinymist LSP or preview subprocesses
// and unregisters the buffer observer. Safe and idempotent.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.shuttingDown = true
	m.gen++
	if m.stopCh != nil {
		select {
		case <-m.stopCh:
		default:
			close(m.stopCh)
		}
	}
	unreg := m.unregObserver
	m.unregObserver = nil

	if m.cursorTimer != nil {
		m.cursorTimer.Stop()
		m.cursorTimer = nil
	}
	for _, doc := range m.docs {
		if doc.timer != nil {
			doc.timer.Stop()
			doc.timer = nil
		}
	}
	m.docs = make(map[string]*openDoc)
	m.docErrorCounts = make(map[string]int)
	m.docErrors = make(map[string][]string)
	m.serverCommands = make(map[string]bool)

	c := m.client
	m.client = nil
	pProxy := m.previewProxy
	pJob := m.previewJob
	pDone := m.previewDone
	pMode := m.previewMode
	m.previewProxy = nil
	m.previewJob = nil
	m.previewDone = nil
	m.previewMode = ""
	m.previewRoot = ""
	m.previewTarget = ""
	m.previewDetail = ""
	m.previewActive = false
	m.previewURL = ""
	m.previewOpened = false
	m.atomicPreview.Store(false)
	m.atomicPreviewStarting.Store(false)
	m.atomicPreviewStartNs.Store(0)
	m.atomicCompileStartNs.Store(0)
	m.resetCompileStatusLocked()
	m.setStateLocked(StateOff)
	m.mu.Unlock()

	if unreg != nil {
		unreg()
	}
	if pProxy != nil {
		pProxy.Close()
	}

	if pMode == "lsp" && c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		_ = c.Call(ctx, "workspace/executeCommand", ExecuteCommandParams{
			Command:   "tinymist.doKillPreview",
			Arguments: []any{previewTaskID},
		}, nil)
		cancel()
	}
	if pJob != nil {
		_ = pJob.Close()
	}
	if pDone != nil {
		select {
		case <-pDone:
		case <-time.After(1500 * time.Millisecond):
		}
	}
	if c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = c.Close(ctx)
		cancel()
	}
}

func (m *Manager) startAsync(gen uint64) {
	m.mu.Lock()
	if m.shuttingDown || m.gen != gen || !m.lspEnabled {
		m.mu.Unlock()
		return
	}
	configuredPath := m.tinymistPath
	discoverFn := m.discoverFn
	probeFn := m.probeFn
	startClientFn := m.startClientFn
	stopCh := m.stopCh
	m.mu.Unlock()

	res := discoverFn(configuredPath)
	if res.Path == "" {
		m.mu.Lock()
		if m.shuttingDown || m.gen != gen {
			m.mu.Unlock()
			return
		}
		m.binPath = ""
		m.binVersion = ""
		m.binSource = ""
		m.configuredMissing = res.ConfiguredMissing
		if res.Err != nil {
			m.lastErr = res.Err.Error()
		} else {
			m.lastErr = "tinymist not found"
		}
		m.setStateLocked(StateOff)
		m.mu.Unlock()

		if res.ConfiguredMissing {
			notifyInfoError("Tinymist not found at configured path (run :tinymist-status): " + configuredPath)
		}
		return
	}

	m.mu.Lock()
	if m.shuttingDown || m.gen != gen {
		m.mu.Unlock()
		return
	}
	m.binPath = res.Path
	m.binSource = res.Source
	m.configuredMissing = false
	m.mu.Unlock()

	handlers := Handlers{
		OnNotification: func(method string, params json.RawMessage) {
			m.handleNotification(gen, method, params)
		},
		OnRequest: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			return defaultServerRequestResult(method, params), nil
		},
	}

	startCtx, cancelStart := context.WithTimeout(context.Background(), 5*time.Second)
	client, err := startClientFn(startCtx, res.Path, handlers)
	cancelStart()
	if err != nil {
		m.mu.Lock()
		if m.shuttingDown || m.gen != gen {
			m.mu.Unlock()
			return
		}
		m.lastErr = err.Error()
		m.setStateLocked(StateError)
		m.mu.Unlock()
		notifyInfoError("Failed to start tinymist: " + err.Error())
		return
	}

	// Check if Shutdown or Restart happened while spawning.
	select {
	case <-stopCh:
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = client.Close(ctx)
		cancel()
		return
	default:
	}

	if err := m.handshake(gen, client); err != nil {
		tail := client.StderrTail()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = client.Close(ctx)
		cancel()

		m.mu.Lock()
		if m.shuttingDown || m.gen != gen {
			m.mu.Unlock()
			return
		}
		m.lastStderr = tail
		m.lastErr = "initialize failed: " + err.Error()
		m.setStateLocked(StateError)
		m.mu.Unlock()
		notifyInfoError("Tinymist initialize failed (run :tinymist-status): " + err.Error())
		return
	}

	type pendingOpen struct {
		uri  string
		path string
		ver  uint64
		text string
	}
	var toOpen []pendingOpen
	var previewTarget string
	var openBrowser bool

	m.mu.Lock()
	if m.shuttingDown || m.gen != gen {
		m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = client.Close(ctx)
		cancel()
		return
	}
	m.client = client
	m.lastErr = ""
	m.setStateLocked(StateReady)

	for _, doc := range m.docs {
		if !doc.opened {
			doc.opened = true
			doc.lastSentVer = doc.lastQueuedVer
			toOpen = append(toOpen, pendingOpen{
				uri:  doc.uri,
				path: doc.absPath,
				ver:  doc.lastQueuedVer,
				text: doc.latestText,
			})
		}
		if m.startPreviewPend && previewTarget == "" {
			previewTarget = doc.absPath
		}
	}
	if previewTarget != "" {
		m.startPreviewPend = false
		openBrowser = m.previewOpenBrowser
	}
	m.mu.Unlock()

	for _, d := range toOpen {
		_ = client.Notify("textDocument/didOpen", DidOpenTextDocumentParams{
			TextDocument: TextDocumentItem{
				URI:        d.uri,
				LanguageID: "typst",
				Version:    int(d.ver),
				Text:       d.text,
			},
		})
	}

	if previewTarget != "" {
		go m.startPreview(previewTarget, openBrowser)
	}

	// Only probe `--version` asynchronously if the LSP server didn't report its
	// version in InitializeResult.ServerInfo (avoids an extra cold process spawn
	// on the startup critical path).
	if probeFn != nil {
		m.mu.Lock()
		needProbe := m.binVersion == "" && !m.shuttingDown && m.gen == gen
		m.mu.Unlock()
		if needProbe {
			go func(path string) {
				probeCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
				v, perr := probeFn(probeCtx, path)
				cancel()
				if perr != nil || v == "" {
					return
				}
				m.mu.Lock()
				if !m.shuttingDown && m.gen == gen && m.binVersion == "" {
					m.binVersion = v
				}
				m.mu.Unlock()
			}(res.Path)
		}
	}

	go m.watchClient(gen, client)
}

func (m *Manager) handshake(gen uint64, client *Client) error {
	m.mu.Lock()
	rootURI := m.rootURI
	if rootURI == "" {
		for _, d := range m.docs {
			if d.absPath != "" {
				rootURI = PathToURI(findProjectRoot(d.absPath))
				break
			}
		}
	}
	m.mu.Unlock()
	if rootURI == "" {
		if cwd, err := os.Getwd(); err == nil && cwd != "" {
			rootURI = PathToURI(findProjectRootDir(cwd))
		}
	}

	var rootPtr *string
	var folders []WorkspaceFolder
	if rootURI != "" {
		rootPtr = &rootURI
		folders = []WorkspaceFolder{{URI: rootURI, Name: filepath.Base(URIToPath(rootURI))}}
	}

	pid := osGetpid()
	params := InitializeParams{
		ProcessID:        &pid,
		RootURI:          rootPtr,
		WorkspaceFolders: folders,
		ClientInfo: &ClientInfo{
			Name:    "TypX",
			Version: "0.1",
		},
		Capabilities: DefaultClientCapabilities(),
		InitializationOptions: map[string]any{
			"compileStatus": "enable",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var res InitializeResult
	if err := client.Call(ctx, "initialize", params, &res); err != nil {
		return err
	}

	m.mu.Lock()
	if m.shuttingDown || m.gen != gen {
		m.mu.Unlock()
		return errors.New("aborted during initialize")
	}
	if res.ServerInfo != nil && res.ServerInfo.Version != "" && m.binVersion == "" {
		m.binVersion = res.ServerInfo.Version
	}
	m.serverCommands = make(map[string]bool)
	if res.Capabilities.ExecuteCommandProvider != nil {
		for _, cmdName := range res.Capabilities.ExecuteCommandProvider.Commands {
			m.serverCommands[cmdName] = true
		}
	}
	m.mu.Unlock()

	return client.Notify("initialized", InitializedParams{})
}

func (m *Manager) watchClient(gen uint64, client *Client) {
	<-client.Done()

	m.mu.Lock()
	if m.shuttingDown || m.gen != gen || m.client != client {
		m.mu.Unlock()
		return
	}
	m.client = nil
	exitErr := client.ExitErr()
	tail := client.StderrTail()
	m.lastStderr = tail

	for _, doc := range m.docs {
		doc.opened = false
		if doc.timer != nil {
			doc.timer.Stop()
			doc.timer = nil
		}
	}

	restarts := m.autoRestarts
	backoff := m.restartBackoff
	stopCh := m.stopCh

	action.EnqueueUI(clearAllTinymistDiagnostics)

	if restarts < 1 {
		m.autoRestarts++
		m.setStateLocked(StateRestarting)
		m.mu.Unlock()

		notifyInfoMessage("Tinymist exited unexpectedly; restarting...")

		if backoff > 0 {
			timer := time.NewTimer(backoff)
			select {
			case <-timer.C:
			case <-stopCh:
				timer.Stop()
				return
			}
		}

		m.mu.Lock()
		if m.shuttingDown || m.gen != gen {
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
		m.startAsync(gen)
		return
	}

	hint := lastNonEmptyLine(tail)
	if hint != "" && exitErr != nil {
		m.lastErr = fmt.Sprintf("%v (%s)", exitErr, hint)
	} else if hint != "" {
		m.lastErr = hint
	} else if exitErr != nil {
		m.lastErr = exitErr.Error()
	} else {
		m.lastErr = "tinymist exited unexpectedly"
	}
	errMsg := m.lastErr
	m.setStateLocked(StateError)
	m.mu.Unlock()

	notifyInfoError("Tinymist crashed (run :tinymist-restart or :tinymist-status): " + errMsg)
}

// OnBufferOpen tracks newly opened .typ buffers and sends textDocument/didOpen.
func (m *Manager) OnBufferOpen(b *buffer.Buffer) {
	if !isTrackableTypst(b) {
		return
	}
	m.refreshMainSettings()

	text := string(b.Bytes())
	ver := b.Version()
	absPath := b.AbsPath
	key := canonicalDocKey(absPath)
	uri := PathToURI(absPath)
	shared := b.SharedBuffer

	m.mu.Lock()
	if m.shuttingDown || !m.lspEnabled {
		m.mu.Unlock()
		return
	}
	if existing, ok := m.docs[key]; ok {
		if existing.shared == shared {
			m.mu.Unlock()
			return
		}
		if existing.timer != nil {
			existing.timer.Stop()
		}
	}
	doc := &openDoc{
		key:           key,
		uri:           uri,
		absPath:       absPath,
		shared:        shared,
		lastQueuedVer: ver,
		latestText:    text,
	}
	m.docs[key] = doc

	var c *Client
	startPreviewNow := false
	openBrowser := m.previewOpenBrowser
	if m.State() == StateReady && m.client != nil {
		doc.opened = true
		doc.lastSentVer = ver
		c = m.client
		if m.startPreviewPend {
			m.startPreviewPend = false
			startPreviewNow = true
		}
	}
	m.mu.Unlock()

	if c != nil {
		_ = c.Notify("textDocument/didOpen", DidOpenTextDocumentParams{
			TextDocument: TextDocumentItem{
				URI:        uri,
				LanguageID: "typst",
				Version:    int(ver),
				Text:       text,
			},
		})
	}
	if startPreviewNow {
		go m.startPreview(absPath, openBrowser)
	}
}

// OnBufferChange snapshots buffer text on the main goroutine and schedules a
// debounced textDocument/didChange notification.
func (m *Manager) OnBufferChange(b *buffer.Buffer, ev buffer.ChangeEvent) {
	if !isTrackableTypst(b) {
		return
	}
	debounceMs := readGlobalInt("typst.debounceMs", 150)
	text := string(b.Bytes())
	ver := ev.Version
	if ver == 0 {
		ver = b.Version()
	}
	absPath := b.AbsPath
	key := canonicalDocKey(absPath)
	uri := PathToURI(absPath)

	m.mu.Lock()
	m.debounceMs = debounceMs
	if m.shuttingDown || !m.lspEnabled {
		m.mu.Unlock()
		return
	}
	doc, ok := m.docs[key]
	if !ok {
		doc = &openDoc{
			key:           key,
			uri:           uri,
			absPath:       absPath,
			shared:        b.SharedBuffer,
			lastQueuedVer: ver,
			latestText:    text,
		}
		m.docs[key] = doc
		if m.State() == StateReady && m.client != nil {
			doc.opened = true
			doc.lastSentVer = ver
			c := m.client
			m.mu.Unlock()
			_ = c.Notify("textDocument/didOpen", DidOpenTextDocumentParams{
				TextDocument: TextDocumentItem{
					URI:        uri,
					LanguageID: "typst",
					Version:    int(ver),
					Text:       text,
				},
			})
			return
		}
		m.mu.Unlock()
		return
	}

	if ver <= doc.lastQueuedVer {
		m.mu.Unlock()
		return
	}
	doc.lastQueuedVer = ver
	doc.latestText = text
	if doc.timer != nil {
		doc.timer.Stop()
		doc.timer = nil
	}

	if !doc.opened || m.State() != StateReady || m.client == nil {
		m.mu.Unlock()
		return
	}

	if debounceMs <= 0 {
		doc.lastSentVer = ver
		c := m.client
		m.mu.Unlock()
		_ = c.Notify("textDocument/didChange", DidChangeTextDocumentParams{
			TextDocument: VersionedTextDocumentIdentifier{
				URI:     uri,
				Version: int(ver),
			},
			ContentChanges: []TextDocumentContentChangeEvent{{Text: text}},
		})
		return
	}

	gen := m.gen
	doc.timer = time.AfterFunc(time.Duration(debounceMs)*time.Millisecond, func() {
		m.flushDocChange(key, gen, ver)
	})
	m.mu.Unlock()
}

func (m *Manager) flushDocChange(key string, gen uint64, targetVer uint64) {
	m.mu.Lock()
	if m.shuttingDown || m.gen != gen || m.State() != StateReady || m.client == nil {
		m.mu.Unlock()
		return
	}
	doc, ok := m.docs[key]
	if !ok || !doc.opened {
		m.mu.Unlock()
		return
	}
	if doc.lastSentVer >= doc.lastQueuedVer || doc.lastQueuedVer < targetVer {
		m.mu.Unlock()
		return
	}
	ver := doc.lastQueuedVer
	text := doc.latestText
	uri := doc.uri
	doc.lastSentVer = ver
	doc.timer = nil
	c := m.client
	m.mu.Unlock()

	_ = c.Notify("textDocument/didChange", DidChangeTextDocumentParams{
		TextDocument: VersionedTextDocumentIdentifier{
			URI:     uri,
			Version: int(ver),
		},
		ContentChanges: []TextDocumentContentChangeEvent{{Text: text}},
	})
}

// OnBufferSave handles Save and SaveAs transitions, flushing pending edits
// before sending textDocument/didSave.
func (m *Manager) OnBufferSave(b *buffer.Buffer, ev buffer.SaveEvent) {
	if b == nil {
		return
	}
	m.refreshMainSettings()

	newKey := canonicalDocKey(b.AbsPath)
	if ev.PrevAbsPath != "" {
		oldKey := canonicalDocKey(ev.PrevAbsPath)
		if oldKey != newKey {
			var oldClient *Client
			var oldURI string
			var oldOpened bool
			m.mu.Lock()
			if oldDoc, ok := m.docs[oldKey]; ok {
				if oldDoc.timer != nil {
					oldDoc.timer.Stop()
					oldDoc.timer = nil
				}
				delete(m.docs, oldKey)
				oldOpened = oldDoc.opened
				oldURI = oldDoc.uri
				if m.State() == StateReady {
					oldClient = m.client
				}
			}
			m.mu.Unlock()
			if oldClient != nil && oldOpened {
				_ = oldClient.Notify("textDocument/didClose", DidCloseTextDocumentParams{
					TextDocument: TextDocumentIdentifier{URI: oldURI},
				})
			}
			if !isTrackableTypst(b) {
				b.ClearMessages(diagnosticOwner)
				return
			}
		}
	}

	if !isTrackableTypst(b) {
		return
	}

	text := string(b.Bytes())
	ver := b.Version()
	absPath := b.AbsPath
	uri := PathToURI(absPath)

	m.mu.Lock()
	if m.shuttingDown || !m.lspEnabled {
		m.mu.Unlock()
		return
	}
	doc, ok := m.docs[newKey]
	if !ok {
		doc = &openDoc{
			key:           newKey,
			uri:           uri,
			absPath:       absPath,
			shared:        b.SharedBuffer,
			lastQueuedVer: ver,
			latestText:    text,
		}
		m.docs[newKey] = doc
	} else {
		if doc.timer != nil {
			doc.timer.Stop()
			doc.timer = nil
		}
		doc.lastQueuedVer = ver
		doc.latestText = text
	}

	if m.State() != StateReady || m.client == nil {
		m.mu.Unlock()
		return
	}

	c := m.client
	if !doc.opened {
		doc.opened = true
		doc.lastSentVer = ver
		m.mu.Unlock()
		_ = c.Notify("textDocument/didOpen", DidOpenTextDocumentParams{
			TextDocument: TextDocumentItem{
				URI:        uri,
				LanguageID: "typst",
				Version:    int(ver),
				Text:       text,
			},
		})
		_ = c.Notify("textDocument/didSave", DidSaveTextDocumentParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Text:         &text,
		})
		return
	}

	needChange := doc.lastSentVer < ver
	if needChange {
		doc.lastSentVer = ver
	}
	m.mu.Unlock()

	if needChange {
		_ = c.Notify("textDocument/didChange", DidChangeTextDocumentParams{
			TextDocument: VersionedTextDocumentIdentifier{
				URI:     uri,
				Version: int(ver),
			},
			ContentChanges: []TextDocumentContentChangeEvent{{Text: text}},
		})
	}
	_ = c.Notify("textDocument/didSave", DidSaveTextDocumentParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Text:         &text,
	})
}

// OnBufferClose sends textDocument/didClose only when the last buffer sharing
// SharedBuffer closes (!b.Shared()).
func (m *Manager) OnBufferClose(b *buffer.Buffer) {
	if b == nil || b.Shared() {
		return
	}
	if b.Path == "" || b.Type.Scratch {
		return
	}
	key := canonicalDocKey(b.AbsPath)

	m.mu.Lock()
	doc, ok := m.docs[key]
	if !ok {
		m.mu.Unlock()
		return
	}
	if doc.timer != nil {
		doc.timer.Stop()
		doc.timer = nil
	}
	delete(m.docs, key)
	delete(m.docErrorCounts, key)
	delete(m.docErrors, key)

	var c *Client
	opened := doc.opened
	uri := doc.uri
	if m.State() == StateReady {
		c = m.client
	}
	m.mu.Unlock()

	if c != nil && opened {
		_ = c.Notify("textDocument/didClose", DidCloseTextDocumentParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
		})
	}
}

// OnCursorMove debounces preview cursor scroll synchronization when live preview is active.
func (m *Manager) OnCursorMove(b *buffer.Buffer, ev buffer.CursorEvent) {
	if !isTrackableTypst(b) || !m.atomicPreview.Load() {
		return
	}
	absPath := b.AbsPath
	line := ev.New.Y
	lineBytes := b.LineBytes(line)
	utf16Col := RuneColToUTF16(lineBytes, locXToRuneCol(lineBytes, ev.New.X))

	m.mu.Lock()
	if !m.previewActive || m.shuttingDown || m.State() != StateReady || m.client == nil {
		m.mu.Unlock()
		return
	}
	if m.cursorTimer != nil {
		m.cursorTimer.Stop()
	}
	gen := m.gen
	m.cursorTimer = time.AfterFunc(cursorSyncDelay, func() {
		m.sendCursorScrollSync(gen, absPath, line, utf16Col)
	})
	m.mu.Unlock()
}

// scrollPreviewTo immediately sends tinymist.scrollPreview for an explicit
// navigation event (such as jumping via the Document Outline), canceling any
// pending debounced cursor timer so no duplicate scroll is dispatched.
func (m *Manager) scrollPreviewTo(b *buffer.Buffer, line, runeCol int) bool {
	if !isTrackableTypst(b) || !m.atomicPreview.Load() {
		return false
	}
	if line < 0 {
		line = 0
	}
	if n := b.LinesNum(); n > 0 && line >= n {
		line = n - 1
	}
	if runeCol < 0 {
		runeCol = 0
	}
	absPath := b.AbsPath
	lineBytes := b.LineBytes(line)
	utf16Col := RuneColToUTF16(lineBytes, runeCol)

	m.mu.Lock()
	if !m.previewActive || m.shuttingDown || m.State() != StateReady || m.client == nil {
		m.mu.Unlock()
		return false
	}
	if m.cursorTimer != nil {
		m.cursorTimer.Stop()
		m.cursorTimer = nil
	}
	gen := m.gen
	m.mu.Unlock()

	go m.sendCursorScrollSync(gen, absPath, line, utf16Col)
	return true
}

func (m *Manager) sendCursorScrollSync(gen uint64, absPath string, line, utf16Col int) {
	m.mu.Lock()
	if m.shuttingDown || m.gen != gen || !m.previewActive || m.State() != StateReady || m.client == nil {
		m.mu.Unlock()
		return
	}
	c := m.client
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.Call(ctx, "workspace/executeCommand", ExecuteCommandParams{
		Command: "tinymist.scrollPreview",
		Arguments: []any{
			previewTaskID,
			map[string]any{
				"event":     "panelScrollTo",
				"filepath":  absPath,
				"line":      line,
				"character": utf16Col,
			},
		},
	}, nil)
}

func (m *Manager) handleNotification(gen uint64, method string, params json.RawMessage) {
	switch method {
	case "textDocument/publishDiagnostics":
		var p PublishDiagnosticsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return
		}
		m.applyDiagnostics(gen, p)

	case "window/showMessage":
		var p ShowMessageParams
		if err := json.Unmarshal(params, &p); err != nil {
			return
		}
		m.inspectPreviewURL(p.Message)
		if p.Type == MessageTypeError {
			notifyInfoError("tinymist: " + collapseDiagnosticMessage(p.Message))
		}

	case "window/logMessage":
		var p LogMessageParams
		if err := json.Unmarshal(params, &p); err != nil {
			return
		}
		m.inspectPreviewURL(p.Message)

	case "tinymist/compileStatus":
		var p struct {
			Status    string `json:"status"`
			PageCount int    `json:"pageCount"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return
		}
		m.handleCompileStatus(gen, p.Status, p.PageCount)

	case "tinymist/preview/dispose":
		var p struct {
			TaskID string `json:"taskId"`
		}
		if err := json.Unmarshal(params, &p); err == nil && p.TaskID != "" && p.TaskID != previewTaskID {
			return
		}
		m.mu.Lock()
		var pProxy *previewProxy
		if !m.shuttingDown && m.gen == gen && m.previewMode == "lsp" {
			pProxy = m.previewProxy
			m.previewProxy = nil
			m.previewActive = false
			m.previewMode = ""
			m.previewRoot = ""
			m.previewTarget = ""
			m.previewDetail = ""
			m.previewURL = ""
			m.previewOpened = false
			m.atomicPreview.Store(false)
			m.atomicPreviewStarting.Store(false)
			m.atomicPreviewStartNs.Store(0)
		}
		m.mu.Unlock()
		if pProxy != nil {
			pProxy.Close()
		}
		action.EnqueueUI(func() {})
	}
}

func (m *Manager) handleCompileStatus(gen uint64, status string, pageCount int) {
	var next CompileState
	switch status {
	case "compiling":
		next = CompileRunning
	case "compileSuccess":
		next = CompileSuccess
	case "compileError":
		next = CompileError
	default:
		return
	}

	m.mu.Lock()
	if m.shuttingDown || m.gen != gen {
		m.mu.Unlock()
		return
	}
	prev := CompileState(m.atomicCompileState.Swap(int32(next)))
	switch next {
	case CompileRunning:
		if prev != CompileRunning {
			m.atomicCompileStartNs.Store(time.Now().UnixNano())
		}
	case CompileSuccess:
		m.atomicCompilePages.Store(int32(pageCount))
		m.atomicCompileStartNs.Store(0)
	case CompileError:
		m.atomicCompilePages.Store(0)
		m.atomicCompileStartNs.Store(0)
	}
	previewOn := m.previewActive
	startedNs := m.atomicPreviewStartNs.Load()
	m.mu.Unlock()

	if previewOn && next == CompileRunning && prev != CompileRunning {
		go m.runPreviewProgressTicker(gen)
	}

	if previewOn && prev != next {
		switch next {
		case CompileSuccess:
			elapsedSuffix := ""
			if startedNs > 0 {
				elapsed := time.Since(time.Unix(0, startedNs))
				if elapsed >= 200*time.Millisecond && elapsed < 2*time.Minute {
					elapsedSuffix = fmt.Sprintf(", %.1fs", elapsed.Seconds())
				}
			}
			if pageCount == 1 {
				notifyInfoMessageIfIdle(fmt.Sprintf("Preview ready (1 page%s)", elapsedSuffix))
			} else if pageCount > 1 {
				notifyInfoMessageIfIdle(fmt.Sprintf("Preview ready (%d pages%s)", pageCount, elapsedSuffix))
			}
		case CompileError:
			notifyInfoMessageIfIdle("Preview compile error — fix errors to render (:diagnostics)")
		}
	}
	action.EnqueueUI(func() {})
}

func (m *Manager) inspectPreviewURL(text string) {
	if urlStr, _, ok := ExtractPreviewURL(text); ok {
		m.recordPreviewURL(urlStr)
	}
}

func (m *Manager) recordPreviewURL(urlStr string) {
	valid, err := ValidatePreviewURL(urlStr)
	if err != nil {
		return
	}
	m.mu.Lock()
	if !m.previewActive {
		m.mu.Unlock()
		return
	}
	proxy := m.previewProxy
	if proxy != nil {
		m.previewURL = proxy.URL()
		shouldOpen := m.previewOpenBrowser && !m.previewOpened
		if shouldOpen {
			m.previewOpened = true
		}
		m.mu.Unlock()
		_ = proxy.SetUpstream(valid)
		if shouldOpen {
			_ = OpenURL(proxy.URL())
		}
		return
	}
	m.previewURL = valid
	shouldOpen := m.previewOpenBrowser && !m.previewOpened
	if shouldOpen {
		m.previewOpened = true
	}
	m.mu.Unlock()

	if shouldOpen {
		_ = OpenURL(valid)
	}
}

func (m *Manager) applyDiagnostics(gen uint64, p PublishDiagnosticsParams) {
	targetKey := canonicalDocKey(URIToPath(p.URI))
	errCount := 0
	var errMsgs []string
	for _, d := range p.Diagnostics {
		if d.Severity == SeverityError || d.Severity == 0 {
			errCount++
			if len(errMsgs) < 10 {
				errMsgs = append(errMsgs, fmt.Sprintf("line %d: %s", d.Range.Start.Line+1, collapseDiagnosticMessage(d.Message)))
			}
		}
	}
	m.mu.Lock()
	if !m.shuttingDown && m.gen == gen && targetKey != "" {
		if m.docErrorCounts == nil {
			m.docErrorCounts = make(map[string]int)
		}
		if m.docErrors == nil {
			m.docErrors = make(map[string][]string)
		}
		if errCount > 0 {
			m.docErrorCounts[targetKey] = errCount
			m.docErrors[targetKey] = errMsgs
		} else {
			delete(m.docErrorCounts, targetKey)
			delete(m.docErrors, targetKey)
		}
	}
	m.mu.Unlock()

	action.EnqueueUI(func() {
		m.mu.Lock()
		activeGen := m.gen
		shutting := m.shuttingDown
		m.mu.Unlock()
		if shutting || activeGen != gen {
			return
		}

		seen := make(map[*buffer.SharedBuffer]bool)
		var updatedShared *buffer.SharedBuffer
		for _, b := range buffer.OpenBuffers {
			if b == nil || b.Type.Scratch || b.Path == "" {
				continue
			}
			if canonicalDocKey(b.AbsPath) != targetKey {
				continue
			}
			if seen[b.SharedBuffer] {
				continue
			}
			seen[b.SharedBuffer] = true

			if p.Version != nil && *p.Version < int(b.Version()) {
				continue
			}

			b.ClearMessages(diagnosticOwner)
			numLines := b.LinesNum()
			if numLines > 0 {
				for _, d := range p.Diagnostics {
					kind := severityToMsgType(d.Severity)
					startY := util.Clamp(d.Range.Start.Line, 0, numLines-1)
					endY := util.Clamp(d.Range.End.Line, 0, numLines-1)

					startLine := b.LineBytes(startY)
					endLine := b.LineBytes(endY)

					startX := runeColToLocX(startLine, UTF16ToRuneCol(startLine, d.Range.Start.Character))
					endX := runeColToLocX(endLine, UTF16ToRuneCol(endLine, d.Range.End.Character))

					start := buffer.Loc{X: startX, Y: startY}
					end := buffer.Loc{X: endX, Y: endY}
					if end.LessThan(start) {
						end = start
					}
					if start == end {
						if start.X < util.CharacterCount(startLine) {
							end.X = start.X + 1
						}
					}

					msgText := collapseDiagnosticMessage(d.Message)
					b.AddMessage(buffer.NewMessage(diagnosticOwner, msgText, start, end, kind))
				}
			}
			updatedShared = b.SharedBuffer
		}

		if updatedShared != nil {
			syncActivePaneGutter(updatedShared)
		}
	})
}

func clearAllTinymistDiagnostics() {
	seen := make(map[*buffer.SharedBuffer]bool)
	for _, b := range buffer.OpenBuffers {
		if b == nil || seen[b.SharedBuffer] {
			continue
		}
		seen[b.SharedBuffer] = true
		b.ClearMessages(diagnosticOwner)
	}
	if action.InfoBar != nil && !action.InfoBar.HasPrompt && action.InfoBar.HasGutter {
		action.InfoBar.ClearGutter()
	}
}

func syncActivePaneGutter(sb *buffer.SharedBuffer) {
	if action.Tabs == nil || len(action.Tabs.List) == 0 || action.InfoBar == nil || action.InfoBar.HasPrompt {
		return
	}
	tab := action.MainTab()
	if tab == nil {
		return
	}
	bp := tab.CurPane()
	if bp == nil || bp.Buf == nil || bp.Buf.SharedBuffer != sb {
		return
	}
	c := bp.Buf.GetActiveCursor()
	if c == nil {
		return
	}
	for _, msg := range bp.Buf.Messages {
		if c.Y == msg.Start.Y || c.Y == msg.End.Y {
			action.InfoBar.GutterMessage(msg.Msg)
			return
		}
	}
	if action.InfoBar.HasGutter {
		action.InfoBar.ClearGutter()
	}
}

func (m *Manager) triggerCompletion(bp *action.BufPane) bool {
	if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
		return false
	}
	if bp.Cursor == nil || bp.Cursor.HasSelection() {
		return false
	}

	m.mu.Lock()
	state := m.State()
	c := m.client
	m.mu.Unlock()

	if state != StateReady || c == nil {
		notifyInfoMessage("LSP: offline")
		return true
	}

	targetBuf := bp.Buf
	reqVersion := targetBuf.Version()
	reqLoc := bp.Cursor.Loc
	rawLine := targetBuf.LineBytes(reqLoc.Y)
	lineCopy := append([]byte(nil), rawLine...)
	utf16Col := RuneColToUTF16(lineCopy, locXToRuneCol(lineCopy, reqLoc.X))
	uri := PathToURI(targetBuf.AbsPath)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		var raw json.RawMessage
		err := c.Call(ctx, "textDocument/completion", CompletionParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: reqLoc.Y, Character: utf16Col},
			Context:      &CompletionContext{TriggerKind: 1},
		}, &raw)
		if err != nil {
			notifyInfoError("LSP completion error: " + err.Error())
			return
		}
		items, perr := ParseCompletionResult(raw)
		if perr != nil {
			notifyInfoError("LSP completion parse error: " + perr.Error())
			return
		}

		action.EnqueueUI(func() {
			if !isBufferOpenAndAt(targetBuf, reqVersion, reqLoc, true) {
				return
			}
			completions, suggestions := buildCompletions(targetBuf, lineCopy, reqLoc, items)
			if len(suggestions) == 0 {
				notifyInfoMessage("No completions")
				return
			}
			targetBuf.Autocomplete(func(*buffer.Buffer) ([]string, []string) {
				return completions, suggestions
			})
		})
	}()
	return true
}

func buildCompletions(b *buffer.Buffer, lineBytes []byte, reqLoc buffer.Loc, items []CompletionItem) ([]string, []string) {
	if len(items) == 0 {
		return nil, nil
	}

	_, wordStart := b.GetWord()
	defaultPrefixStart := reqLoc.X
	if wordStart >= 0 && wordStart <= reqLoc.X {
		defaultPrefixStart = wordStart
	}

	var completions []string
	var suggestions []string
	seen := make(map[string]bool)

	for _, item := range items {
		if len(suggestions) >= maxCompletionItems {
			break
		}
		label := strings.TrimSpace(item.Label)
		if label == "" {
			continue
		}

		prefixStartX := defaultPrefixStart
		rawInsert := item.InsertText
		if item.TextEdit != nil && item.TextEdit.Range.Start.Line == reqLoc.Y && item.TextEdit.Range.End.Line == reqLoc.Y {
			endX := runeColToLocX(lineBytes, UTF16ToRuneCol(lineBytes, item.TextEdit.Range.End.Character))
			startX := runeColToLocX(lineBytes, UTF16ToRuneCol(lineBytes, item.TextEdit.Range.Start.Character))
			if endX == reqLoc.X && startX >= 0 && startX <= reqLoc.X {
				prefixStartX = startX
				rawInsert = item.TextEdit.NewText
			}
		}
		if rawInsert == "" {
			rawInsert = item.Label
		}
		if item.InsertTextFormat == InsertTextFormatSnippet || strings.Contains(rawInsert, "$") {
			rawInsert = stripSnippetPlaceholders(rawInsert)
		}

		typed := ""
		if prefixStartX < reqLoc.X {
			typed = string(b.Substr(buffer.Loc{X: prefixStartX, Y: reqLoc.Y}, reqLoc))
		}

		var suffix string
		if typed == "" {
			suffix = rawInsert
		} else if strings.HasPrefix(rawInsert, typed) {
			suffix = rawInsert[len(typed):]
		} else if foldSuffix, ok := trimFoldPrefix(rawInsert, typed); ok {
			suffix = foldSuffix
		} else {
			continue
		}

		dedupKey := label + "\x00" + suffix
		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true
		completions = append(completions, suffix)
		suggestions = append(suggestions, label)
	}

	return completions, suggestions
}

func (m *Manager) triggerHover(bp *action.BufPane) bool {
	if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
		return false
	}
	if bp.Cursor == nil {
		return false
	}

	m.mu.Lock()
	state := m.State()
	c := m.client
	m.mu.Unlock()

	if state != StateReady || c == nil {
		notifyInfoMessage("LSP: offline")
		return true
	}

	targetBuf := bp.Buf
	reqVersion := targetBuf.Version()
	reqLoc := bp.Cursor.Loc
	rawLine := targetBuf.LineBytes(reqLoc.Y)
	lineCopy := append([]byte(nil), rawLine...)
	utf16Col := RuneColToUTF16(lineCopy, locXToRuneCol(lineCopy, reqLoc.X))
	uri := PathToURI(targetBuf.AbsPath)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		var hov Hover
		err := c.Call(ctx, "textDocument/hover", HoverParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: reqLoc.Y, Character: utf16Col},
		}, &hov)
		if err != nil {
			notifyInfoError("LSP hover error: " + err.Error())
			return
		}
		text := cleanHoverText(ExtractHoverText(hov.Contents))

		action.EnqueueUI(func() {
			if !isBufferOpenAndAt(targetBuf, reqVersion, reqLoc, false) {
				return
			}
			if text == "" {
				notifyInfoMessage("No hover info")
				return
			}
			if !strings.Contains(text, "\n") && len(text) <= 120 {
				notifyInfoMessage(text)
				return
			}
			m.showSplitText(bp, &m.hoverBuf, "Hover", text)
		})
	}()
	return true
}

// requestDocumentSymbols asynchronously queries textDocument/documentSymbol for b
// when the LSP client is connected and ready. Calls cb on the main UI goroutine
// via action.EnqueueUI only if b is still open and at the same Version.
func (m *Manager) requestDocumentSymbols(b *buffer.Buffer, cb func(version uint64, symbols []DocumentSymbol)) bool {
	if b == nil || cb == nil || !isTrackableTypst(b) {
		return false
	}

	m.mu.Lock()
	state := m.State()
	c := m.client
	gen := m.gen
	m.mu.Unlock()

	if state != StateReady || c == nil {
		return false
	}

	targetBuf := b
	reqVersion := b.Version()
	uri := PathToURI(b.AbsPath)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()

		var raw json.RawMessage
		err := c.Call(ctx, "textDocument/documentSymbol", DocumentSymbolParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
		}, &raw)
		if err != nil {
			return
		}
		syms, perr := ParseDocumentSymbols(raw)
		if perr != nil || len(syms) == 0 {
			return
		}

		action.EnqueueUI(func() {
			m.mu.Lock()
			stale := m.shuttingDown || m.gen != gen
			m.mu.Unlock()
			if stale {
				return
			}
			if !isBufferStillOpenAtVersion(targetBuf, reqVersion) {
				return
			}
			cb(reqVersion, syms)
		})
	}()
	return true
}

func isBufferStillOpenAtVersion(target *buffer.Buffer, reqVersion uint64) bool {
	if target == nil || target.Version() != reqVersion {
		return false
	}
	for _, b := range buffer.OpenBuffers {
		if b == target {
			return true
		}
	}
	return false
}

func (m *Manager) togglePreview(bp *action.BufPane) bool {
	if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
		return false
	}
	openBrowser := readGlobalBool("typst.previewOpenBrowser", true)
	absPath := bp.Buf.AbsPath

	m.mu.Lock()
	m.previewOpenBrowser = openBrowser
	active := m.previewActive
	m.mu.Unlock()

	if active {
		m.stopPreview()
		notifyInfoMessage("Tinymist preview stopped")
		return true
	}

	if absPath == "" {
		notifyInfoError("Save file before starting preview")
		return true
	}
	go m.startPreview(absPath, openBrowser)
	return true
}

func (m *Manager) startPreview(absPath string, openBrowser bool) {
	projectRoot := findProjectRoot(absPath)
	now := time.Now()

	m.mu.Lock()
	if m.shuttingDown || m.previewActive {
		m.mu.Unlock()
		return
	}
	m.previewActive = true
	m.previewOpened = false
	m.previewURL = ""
	m.previewRoot = projectRoot
	m.previewTarget = absPath
	m.previewDetail = "Starting tinymist preview..."
	m.previewOpenBrowser = openBrowser
	m.atomicPreview.Store(true)
	m.atomicPreviewStarting.Store(true)
	m.atomicPreviewStartNs.Store(now.UnixNano())
	state := m.State()
	readyCh := m.readyCh
	stopCh := m.stopCh
	gen := m.gen
	m.mu.Unlock()

	go m.runPreviewProgressTicker(gen)

	if state == StateStarting && readyCh != nil {
		m.setPreviewDetail("Waiting for tinymist LSP...")
		notifyInfoMessageIfIdle("Waiting for tinymist LSP to start preview...")
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-readyCh:
			timer.Stop()
		case <-stopCh:
			timer.Stop()
			m.clearPreviewState()
			return
		case <-timer.C:
		}
	} else {
		notifyInfoMessageIfIdle("Starting tinymist preview...")
	}

	m.mu.Lock()
	if m.shuttingDown || m.gen != gen || !m.previewActive {
		m.mu.Unlock()
		return
	}
	c := m.client
	state = m.State()
	binPath := m.binPath
	configuredPath := m.tinymistPath
	discoverFn := m.discoverFn
	hasDoStart := m.serverCommands["tinymist.doStartPreview"]
	m.mu.Unlock()

	if state == StateReady && c != nil {
		// Launch the localhost progress-overlay proxy when real tinymist
		// (advertising tinymist.doStartPreview) is connected. Wait up to 200ms
		// before opening the browser so fast compiles land directly on the
		// preview page without a reload, while slow compiles still show the
		// live progress card right away.
		var proxy *previewProxy
		var delayedOpen *time.Timer
		if hasDoStart {
			if p, perr := startPreviewProxy(absPath, m.previewStatusSnapshot); perr == nil {
				proxy = p
				m.mu.Lock()
				if m.shuttingDown || m.gen != gen || !m.previewActive {
					m.mu.Unlock()
					p.Close()
					return
				}
				m.previewProxy = p
				m.previewURL = p.URL()
				m.previewDetail = "Starting tinymist preview server..."
				wantOpen := m.previewOpenBrowser && !m.previewOpened
				m.mu.Unlock()
				if wantOpen {
					delayedOpen = time.AfterFunc(200*time.Millisecond, func() {
						m.mu.Lock()
						if m.shuttingDown || m.gen != gen || !m.previewActive || m.previewProxy != p || m.previewOpened {
							m.mu.Unlock()
							return
						}
						m.previewOpened = true
						m.mu.Unlock()
						_ = OpenURL(p.URL())
					})
				}
			}
		}

		var res json.RawMessage
		var err error
		tried := false
		if hasDoStart {
			tried = true
			args := []string{
				"--no-open",
				"--partial-rendering", "true",
				"--task-id", previewTaskID,
				"--data-plane-host", "127.0.0.1:0",
				"--control-plane-host", "127.0.0.1:0",
			}
			if projectRoot != "" {
				args = append(args, "--root", projectRoot)
			}
			args = append(args, absPath)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err = c.Call(ctx, "workspace/executeCommand", ExecuteCommandParams{
				Command:   "tinymist.doStartPreview",
				Arguments: []any{args},
			}, &res)
			cancel()
		}
		if delayedOpen != nil {
			delayedOpen.Stop()
		}
		if !tried || err != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			res = nil
			err = c.Call(ctx, "workspace/executeCommand", ExecuteCommandParams{
				Command:   "tinymist.startDefaultPreview",
				Arguments: []any{},
			}, &res)
			cancel()
		}

		if err == nil {
			m.mu.Lock()
			if m.shuttingDown || m.gen != gen {
				m.mu.Unlock()
				if proxy != nil {
					proxy.Close()
				}
				return
			}
			m.previewMode = "lsp"
			m.previewDetail = ""
			m.atomicPreviewStarting.Store(false)
			hasCompileErrs := m.docErrorCounts[canonicalDocKey(absPath)] > 0
			m.mu.Unlock()

			if u := extractURLFromJSON(res); u != "" {
				m.recordPreviewURL(u)
			}
			action.EnqueueUI(func() {})
			cs := CompileState(m.atomicCompileState.Load())
			pages := int(m.atomicCompilePages.Load())
			switch {
			case hasCompileErrs || cs == CompileError:
				notifyInfoMessage("Tinymist preview started — fix compile errors to render pages (:diagnostics)")
			case cs == CompileRunning:
				notifyInfoMessage("Tinymist preview started (compiling...)")
			case cs == CompileSuccess && pages == 1:
				notifyInfoMessage("Tinymist preview started (1 page)")
			case cs == CompileSuccess && pages > 1:
				notifyInfoMessage(fmt.Sprintf("Tinymist preview started (%d pages)", pages))
			default:
				notifyInfoMessage("Tinymist preview started")
			}
			return
		}
		if proxy != nil {
			m.mu.Lock()
			if m.previewProxy == proxy {
				m.previewProxy = nil
			}
			m.mu.Unlock()
			proxy.Close()
		}
	}

	if binPath == "" && discoverFn != nil {
		binPath = discoverFn(configuredPath).Path
	}
	if binPath == "" {
		m.clearPreviewState()
		notifyInfoError("Cannot start preview: tinymist binary not found")
		return
	}

	previewArgs := []string{"preview", "--no-open", "--partial-rendering", "true"}
	if projectRoot != "" {
		previewArgs = append(previewArgs, "--root", projectRoot)
	}
	previewArgs = append(previewArgs, absPath)
	cmd := exec.Command(binPath, previewArgs...)
	if projectRoot != "" {
		cmd.Dir = projectRoot
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.clearPreviewState()
		notifyInfoError("Preview pipe failed: " + err.Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		m.clearPreviewState()
		notifyInfoError("Preview pipe failed: " + err.Error())
		return
	}

	job, err := startManagedProcess(cmd)
	if err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		m.clearPreviewState()
		notifyInfoError("Failed to start tinymist preview: " + err.Error())
		return
	}

	doneCh := make(chan struct{})

	m.mu.Lock()
	if m.shuttingDown || m.gen != gen || !m.previewActive {
		m.mu.Unlock()
		_ = job.Close()
		_ = cmd.Wait()
		close(doneCh)
		return
	}
	m.previewMode = "subprocess"
	m.previewDetail = ""
	m.previewJob = job
	m.previewDone = doneCh
	m.atomicPreviewStarting.Store(false)
	m.mu.Unlock()
	action.EnqueueUI(func() {})

	var wg sync.WaitGroup
	wg.Add(2)
	scanPipe := func(r io.Reader) {
		defer wg.Done()
		br := bufio.NewReaderSize(r, 16*1024)
		for {
			line, rerr := br.ReadString('\n')
			if len(line) > 0 {
				m.inspectPreviewURL(line)
			}
			if rerr != nil {
				return
			}
		}
	}
	go scanPipe(stdout)
	go scanPipe(stderr)

	go func() {
		wg.Wait()
		_ = cmd.Wait()
		_ = job.Close()
		m.mu.Lock()
		if m.previewDone == doneCh {
			m.previewActive = false
			m.previewMode = ""
			m.previewJob = nil
			m.previewDone = nil
			m.atomicPreview.Store(false)
		}
		m.mu.Unlock()
		close(doneCh)
	}()

	notifyInfoMessage("Tinymist preview started")
}

func (m *Manager) setPreviewDetail(detail string) {
	m.mu.Lock()
	m.previewDetail = detail
	m.mu.Unlock()
}

func (m *Manager) previewStatusSnapshot() previewStatusPayload {
	m.mu.Lock()
	target := m.previewTarget
	detail := m.previewDetail
	errs := append([]string(nil), m.docErrors[canonicalDocKey(target)]...)
	starting := m.atomicPreviewStarting.Load()
	startedNs := m.atomicPreviewStartNs.Load()
	m.mu.Unlock()

	cs := CompileState(m.atomicCompileState.Load())
	pages := int(m.atomicCompilePages.Load())
	phase := "ready"
	switch {
	case len(errs) > 0 || cs == CompileError:
		phase = "error"
	case starting:
		phase = "starting"
	case cs == CompileRunning:
		phase = "compiling"
	case cs == CompileSuccess:
		phase = "ready"
	}
	var elapsedMs int64
	if startedNs > 0 {
		elapsedMs = time.Since(time.Unix(0, startedNs)).Milliseconds()
	}
	return previewStatusPayload{
		Phase:     phase,
		File:      filepath.Base(target),
		Pages:     pages,
		ElapsedMs: elapsedMs,
		Errors:    errs,
		Detail:    detail,
	}
}

func (m *Manager) runPreviewProgressTicker(gen uint64) {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	for {
		m.mu.Lock()
		stopCh := m.stopCh
		active := !m.shuttingDown && m.gen == gen && m.previewActive
		starting := m.atomicPreviewStarting.Load()
		compiling := CompileState(m.atomicCompileState.Load()) == CompileRunning
		target := m.previewTarget
		m.mu.Unlock()

		if !active || (!starting && !compiling) {
			return
		}

		select {
		case <-stopCh:
			return
		case <-ticker.C:
		}

		tick := m.atomicSpinTick.Add(1)
		if tick%4 == 0 {
			name := filepath.Base(target)
			if starting {
				if ns := m.atomicPreviewStartNs.Load(); ns > 0 {
					notifyInfoMessageIfIdle(fmt.Sprintf("Starting preview for %s (%.1fs)...", name, time.Since(time.Unix(0, ns)).Seconds()))
				}
			} else if compiling {
				ns := m.atomicCompileStartNs.Load()
				if ns == 0 {
					ns = m.atomicPreviewStartNs.Load()
				}
				if ns > 0 {
					notifyInfoMessageIfIdle(fmt.Sprintf("Compiling %s (%.1fs)...", name, time.Since(time.Unix(0, ns)).Seconds()))
				}
			}
		}
		action.EnqueueUI(func() {})
	}
}

func (m *Manager) clearPreviewState() {
	m.mu.Lock()
	pProxy := m.previewProxy
	m.previewProxy = nil
	m.previewActive = false
	m.previewMode = ""
	m.previewRoot = ""
	m.previewTarget = ""
	m.previewDetail = ""
	m.previewURL = ""
	m.previewOpened = false
	m.atomicPreview.Store(false)
	m.atomicPreviewStarting.Store(false)
	m.atomicPreviewStartNs.Store(0)
	m.mu.Unlock()
	if pProxy != nil {
		pProxy.Close()
	}
}

func (m *Manager) stopPreview() {
	m.mu.Lock()
	mode := m.previewMode
	c := m.client
	pProxy := m.previewProxy
	pJob := m.previewJob
	pDone := m.previewDone
	m.previewProxy = nil
	m.previewActive = false
	m.previewMode = ""
	m.previewRoot = ""
	m.previewTarget = ""
	m.previewDetail = ""
	m.previewURL = ""
	m.previewOpened = false
	m.previewJob = nil
	m.previewDone = nil
	m.atomicPreview.Store(false)
	m.atomicPreviewStarting.Store(false)
	m.atomicPreviewStartNs.Store(0)
	m.mu.Unlock()

	if pProxy != nil {
		pProxy.Close()
	}

	if mode == "lsp" && c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = c.Call(ctx, "workspace/executeCommand", ExecuteCommandParams{
			Command:   "tinymist.doKillPreview",
			Arguments: []any{previewTaskID},
		}, nil)
		cancel()
	}
	if pJob != nil {
		_ = pJob.Close()
	}
	if pDone != nil {
		select {
		case <-pDone:
		case <-time.After(1500 * time.Millisecond):
		}
	}
}

func (m *Manager) showDiagnostics(bp *action.BufPane) {
	if bp == nil || bp.Buf == nil {
		return
	}
	var lines []string
	name := bp.Buf.GetName()
	for _, msg := range bp.Buf.Messages {
		if msg.Owner != diagnosticOwner {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s:%d:%d [%s] %s",
			name, msg.Start.Y+1, msg.Start.X+1, msgKindLabel(msg.Kind), msg.Msg))
	}
	if len(lines) == 0 {
		notifyInfoMessage("No diagnostics")
		return
	}
	m.showSplitText(bp, &m.diagBuf, "Diagnostics", strings.Join(lines, "\n"))
}

func (m *Manager) showStatus(bp *action.BufPane) {
	m.mu.Lock()
	state := m.State()
	binPath := m.binPath
	binVer := m.binVersion
	binSrc := m.binSource
	cfgMissing := m.configuredMissing
	cfgPath := m.tinymistPath
	lastErr := m.lastErr
	previewActive := m.previewActive
	previewURL := m.previewURL
	previewMode := m.previewMode
	previewRoot := m.previewRoot
	compileState := CompileState(m.atomicCompileState.Load())
	compilePages := int(m.atomicCompilePages.Load())
	c := m.client
	tail := append([]string(nil), m.lastStderr...)
	m.mu.Unlock()

	if c != nil {
		if liveTail := c.StderrTail(); len(liveTail) > 0 {
			tail = liveTail
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Tinymist LSP Status\n")
	fmt.Fprintf(&b, "===================\n")
	fmt.Fprintf(&b, "State: %s\n", state.String())
	if binPath != "" {
		fmt.Fprintf(&b, "Binary: %s (source: %s)\n", binPath, binSrc)
		if binVer != "" {
			fmt.Fprintf(&b, "Version: %s\n", binVer)
		}
	} else {
		fmt.Fprintf(&b, "Binary: not found\n")
		if cfgMissing {
			fmt.Fprintf(&b, "Configured typst.tinymistPath missing: %s\n", cfgPath)
		}
		b.WriteString("\nInstall Hints:\n")
		for _, hint := range InstallHints(runtime.GOOS) {
			fmt.Fprintf(&b, "  - %s\n", hint)
		}
	}

	if previewActive {
		if previewURL != "" {
			fmt.Fprintf(&b, "Preview: active (%s) %s\n", previewMode, previewURL)
		} else {
			fmt.Fprintf(&b, "Preview: active (%s)\n", previewMode)
		}
		if previewRoot != "" {
			fmt.Fprintf(&b, "Preview Root: %s\n", previewRoot)
		}
	} else {
		fmt.Fprintf(&b, "Preview: inactive\n")
	}
	if compileState != CompileIdle {
		if compileState == CompileSuccess && compilePages > 0 {
			fmt.Fprintf(&b, "Compile Status: ok (%d pages)\n", compilePages)
		} else {
			fmt.Fprintf(&b, "Compile Status: %s\n", compileState.String())
		}
	}

	if lastErr != "" {
		fmt.Fprintf(&b, "Last Error: %s\n", lastErr)
	}

	if len(tail) > 0 {
		b.WriteString("\nStderr Tail (last 200 lines):\n")
		for _, l := range tail {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}

	if bp == nil || bp.Tab() == nil {
		notifyInfoMessage("Tinymist: " + state.String())
		return
	}
	m.showSplitText(bp, &m.statusBuf, "Tinymist Status", strings.TrimRight(b.String(), "\n"))
}

func (m *Manager) restart() {
	lspEnabled := readGlobalBool("typst.lspEnabled", true)
	tinymistPath := readGlobalString("typst.tinymistPath", "")
	debounceMs := readGlobalInt("typst.debounceMs", 150)
	previewOpenBrowser := readGlobalBool("typst.previewOpenBrowser", true)

	m.stopPreview()

	m.mu.Lock()
	m.gen++
	gen := m.gen
	m.shuttingDown = false
	if m.stopCh != nil {
		select {
		case <-m.stopCh:
		default:
			close(m.stopCh)
		}
	}
	m.stopCh = make(chan struct{})
	m.signalReadyLocked()
	m.readyCh = make(chan struct{})
	m.autoRestarts = 0
	m.lastErr = ""
	m.lastStderr = nil
	m.resetCompileStatusLocked()
	m.lspEnabled = lspEnabled
	m.tinymistPath = tinymistPath
	m.debounceMs = debounceMs
	m.previewOpenBrowser = previewOpenBrowser

	oldClient := m.client
	m.client = nil
	for _, doc := range m.docs {
		if doc.timer != nil {
			doc.timer.Stop()
			doc.timer = nil
		}
	}
	m.docs = make(map[string]*openDoc)
	m.docErrorCounts = make(map[string]int)
	m.docErrors = make(map[string][]string)
	m.serverCommands = make(map[string]bool)
	m.rootURI = ""
	if !lspEnabled {
		m.setStateLocked(StateOff)
		m.mu.Unlock()
		clearAllTinymistDiagnostics()
		if oldClient != nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = oldClient.Close(ctx)
				cancel()
			}()
		}
		notifyInfoMessage("Tinymist LSP is disabled")
		return
	}
	m.setStateLocked(StateStarting)
	m.mu.Unlock()

	clearAllTinymistDiagnostics()

	// Snapshot currently open Typst buffers so they are re-opened cleanly.
	for _, b := range buffer.OpenBuffers {
		if isTrackableTypst(b) {
			m.OnBufferOpen(b)
		}
	}

	go func() {
		if oldClient != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = oldClient.Close(ctx)
			cancel()
		}
		m.startAsync(gen)
	}()
	notifyInfoMessage("Restarting tinymist LSP...")
}

var previewSpinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m *Manager) statusLine(b *buffer.Buffer) string {
	if b == nil || !b.IsTypst() {
		return ""
	}
	state := State(m.atomicState.Load())
	preview := m.atomicPreview.Load()
	previewStarting := m.atomicPreviewStarting.Load()
	spin := previewSpinnerFrames[int(m.atomicSpinTick.Load())%len(previewSpinnerFrames)]

	var sb strings.Builder
	fmt.Fprintf(&sb, "LSP: %s | ", state.String())
	if previewStarting {
		elapsed := ""
		if ns := m.atomicPreviewStartNs.Load(); ns > 0 {
			d := time.Since(time.Unix(0, ns))
			if d >= 200*time.Millisecond {
				elapsed = fmt.Sprintf(" %.1fs", d.Seconds())
			}
		}
		fmt.Fprintf(&sb, "%s starting%s | ", spin, elapsed)
	} else if state == StateReady {
		cstate := CompileState(m.atomicCompileState.Load())
		pages := int(m.atomicCompilePages.Load())
		errCount := 0
		for _, msg := range b.Messages {
			if msg.Owner == diagnosticOwner && msg.Kind == buffer.MTError {
				errCount++
			}
		}
		switch {
		case cstate == CompileRunning:
			if preview {
				elapsed := ""
				ns := m.atomicCompileStartNs.Load()
				if ns == 0 {
					ns = m.atomicPreviewStartNs.Load()
				}
				if ns > 0 {
					d := time.Since(time.Unix(0, ns))
					if d >= 200*time.Millisecond {
						elapsed = fmt.Sprintf(" %.1fs", d.Seconds())
					}
				}
				fmt.Fprintf(&sb, "%s compiling%s | ", spin, elapsed)
			} else {
				sb.WriteString("compiling... | ")
			}
		case errCount > 0:
			fmt.Fprintf(&sb, "ERR: %d | ", errCount)
		case cstate == CompileError:
			sb.WriteString("ERR | ")
		case cstate == CompileSuccess && pages > 0:
			fmt.Fprintf(&sb, "%dp | ", pages)
		}
	}
	if preview {
		sb.WriteString("PREVIEW: ON | ")
	}
	return sb.String()
}

func (m *Manager) showSplitText(bp *action.BufPane, slot **buffer.Buffer, title, text string) {
	if bp == nil {
		return
	}
	target := *slot
	reused := false
	if target != nil {
		for _, ob := range buffer.OpenBuffers {
			if ob == target {
				reused = true
				break
			}
		}
	}

	if !reused {
		target = buffer.NewBufferFromString(text, "", buffer.BTLog)
		target.SetName(title)
		*slot = target
	} else {
		target.EventHandler.Remove(target.Start(), target.End())
		target.EventHandler.Insert(target.Start(), text)
	}

	if bp.Tab() != nil {
		for _, p := range bp.Tab().Panes {
			if pane, ok := p.(*action.BufPane); ok && pane.Buf == target {
				pane.GotoLoc(target.Start())
				return
			}
		}
		bp.HSplitIndex(target, true)
	}
}

func (m *Manager) refreshMainSettings() {
	debounceMs := readGlobalInt("typst.debounceMs", 150)
	previewOpenBrowser := readGlobalBool("typst.previewOpenBrowser", true)
	m.mu.Lock()
	m.debounceMs = debounceMs
	m.previewOpenBrowser = previewOpenBrowser
	m.mu.Unlock()
}

func isTrackableTypst(b *buffer.Buffer) bool {
	return b != nil && b.IsTypst() && b.Path != "" && !b.Type.Scratch
}

func canonicalDocKey(p string) string {
	if p == "" {
		return ""
	}
	cleaned := filepath.Clean(p)
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}

func isBufferOpenAndAt(target *buffer.Buffer, reqVersion uint64, reqLoc buffer.Loc, checkNoSel bool) bool {
	if target == nil || !target.IsTypst() || target.Version() != reqVersion {
		return false
	}
	found := false
	for _, b := range buffer.OpenBuffers {
		if b == target {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	c := target.GetActiveCursor()
	if c == nil || c.Loc != reqLoc {
		return false
	}
	if checkNoSel && c.HasSelection() {
		return false
	}
	return true
}

func severityToMsgType(sev int) buffer.MsgType {
	switch sev {
	case SeverityError:
		return buffer.MTError
	case SeverityWarning:
		return buffer.MTWarning
	default:
		return buffer.MTInfo
	}
}

func msgKindLabel(k buffer.MsgType) string {
	switch k {
	case buffer.MTError:
		return "ERROR"
	case buffer.MTWarning:
		return "WARN"
	default:
		return "INFO"
	}
}

func collapseDiagnosticMessage(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

func cleanHoverText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func stripSnippetPlaceholders(s string) string {
	s = reSnippetChoice.ReplaceAllString(s, "$1")
	s = reSnippetNested.ReplaceAllString(s, "$1")
	s = reSnippetStop.ReplaceAllString(s, "")
	return s
}

func trimFoldPrefix(s, prefix string) (string, bool) {
	pr := []rune(prefix)
	sr := []rune(s)
	if len(sr) < len(pr) {
		return "", false
	}
	if strings.EqualFold(string(sr[:len(pr)]), prefix) {
		return string(sr[len(pr):]), true
	}
	return "", false
}

func extractURLFromJSON(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var direct string
	if err := json.Unmarshal(raw, &direct); err == nil {
		if u, _, ok := ExtractPreviewURL(direct); ok {
			return u
		}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		for _, k := range []string{"url", "previewUrl", "staticServerAddr"} {
			if v, ok := m[k].(string); ok {
				if u, _, ok := ExtractPreviewURL(v); ok {
					return u
				}
				if valid, verr := ValidatePreviewURL("http://" + v); verr == nil {
					return valid
				}
			}
		}
	}
	if u, _, ok := ExtractPreviewURL(trimmed); ok {
		return u
	}
	return ""
}

func lastNonEmptyLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t != "" {
			return t
		}
	}
	return ""
}

func runeColToLocX(line []byte, runeCol int) int {
	if runeCol <= 0 {
		return 0
	}
	locX := 0
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
			locX++
		}
	}
	return locX
}

func locXToRuneCol(line []byte, locX int) int {
	if locX <= 0 {
		return 0
	}
	runes := 0
	off := 0
	for i := 0; i < locX && off < len(line); i++ {
		_, combc, size := util.DecodeCharacter(line[off:])
		if size <= 0 {
			break
		}
		runes += 1 + len(combc)
		off += size
	}
	return runes
}

func readGlobalBool(key string, def bool) bool {
	v, ok := config.GetGlobalOption(key).(bool)
	if !ok {
		return def
	}
	return v
}

func readGlobalString(key string, def string) string {
	v, ok := config.GetGlobalOption(key).(string)
	if !ok {
		return def
	}
	return v
}

func readGlobalInt(key string, def int) int {
	raw := config.GetGlobalOption(key)
	switch v := raw.(type) {
	case float64:
		if v < 0 {
			return 0
		}
		return int(v)
	case int:
		if v < 0 {
			return 0
		}
		return v
	default:
		return def
	}
}

// initialRootURI derives the Typst workspace root URI from CLI file arguments
// before LoadInput runs so tinymist's initialize handshake gets the right root
// even when opening nested chapter files (e.g. courses/X/lectures/01-sets.typ).
func initialRootURI(args []string) string {
	var fallback string
	for _, a := range args {
		if a == "" || strings.HasPrefix(a, "+") {
			continue
		}
		if strings.EqualFold(filepath.Ext(a), ".typ") {
			if root := findProjectRoot(a); root != "" {
				return PathToURI(root)
			}
		}
		if fallback == "" {
			fallback = a
		}
	}
	if fallback != "" {
		if root := findProjectRoot(fallback); root != "" {
			return PathToURI(root)
		}
	}
	return ""
}

// findProjectRoot resolves the Typst project root for filePath so multi-file
// projects (e.g. chapters/01-sets.typ importing ../template.typ or
// /templates/course-note.typ) can compile and preview without root errors.
func findProjectRoot(filePath string) string {
	if filePath == "" {
		return ""
	}
	abs, err := filepath.Abs(filePath)
	if err != nil {
		abs = filepath.Clean(filePath)
	}
	dir := filepath.Dir(abs)

	// 1. Explicit Typst project manifest (typst.toml) walking upward.
	if root := findMarkerDir(dir, "typst.toml"); root != "" {
		return root
	}

	// 2. Root-relative imports (#import "/templates/course-note.typ") resolved
	// against the nearest ancestor directory that contains the target file.
	head := readFileHead(abs, 64*1024)
	if len(head) > 0 {
		if root := findRootFromRootRelativeImports(dir, head); root != "" {
			return root
		}
	}

	// 3. Nearest Git repository root (.git).
	if root := findMarkerDir(dir, ".git"); root != "" {
		return root
	}

	// 4. Relative parent imports (#import "../../template.typ") when no marker exists.
	if len(head) > 0 {
		if up := maxParentImportDepthBytes(head); up > 0 {
			cur := dir
			for i := 0; i < up; i++ {
				parent := filepath.Dir(cur)
				if parent == cur {
					break
				}
				cur = parent
			}
			return cur
		}
	}
	return dir
}

// findProjectRootDir walks upward from dir looking for a typst.toml or .git
// marker, returning dir if none is found.
func findProjectRootDir(dir string) string {
	if dir == "" {
		return ""
	}
	cleaned := filepath.Clean(dir)
	if root := findMarkerDir(cleaned, "typst.toml"); root != "" {
		return root
	}
	if root := findMarkerDir(cleaned, ".git"); root != "" {
		return root
	}
	return cleaned
}

func findMarkerDir(dir, marker string) string {
	cur := filepath.Clean(dir)
	for {
		if _, err := os.Stat(filepath.Join(cur, marker)); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return ""
}

func readFileHead(filePath string, limit int) []byte {
	f, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, _ := f.Read(buf)
	if n <= 0 {
		return nil
	}
	return buf[:n]
}

func findRootFromRootRelativeImports(dir string, content []byte) string {
	matches := reRootRelImport.FindAllSubmatch(content, -1)
	if len(matches) == 0 {
		return ""
	}
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		rel := filepath.FromSlash(string(m[1]))
		cur := filepath.Clean(dir)
		for {
			if _, err := os.Stat(filepath.Join(cur, rel)); err == nil {
				return cur
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
	}
	return ""
}

func maxParentImportDepthBytes(content []byte) int {
	matches := reParentImport.FindAllSubmatch(content, -1)
	maxUp := 0
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		up := strings.Count(string(m[1]), "../")
		if up > maxUp {
			maxUp = up
		}
	}
	return maxUp
}

func notifyInfoMessage(msg string) {
	action.EnqueueUI(func() {
		if action.InfoBar != nil {
			action.InfoBar.Message(msg)
		}
	})
}

func notifyInfoMessageIfIdle(msg string) {
	action.EnqueueUI(func() {
		if action.InfoBar != nil && !action.InfoBar.HasPrompt {
			action.InfoBar.Message(msg)
		}
	})
}

func notifyInfoError(msg string) {
	action.EnqueueUI(func() {
		if action.InfoBar != nil {
			action.InfoBar.Error(msg)
		}
	})
}

var osGetpid = func() int {
	return getpid()
}
