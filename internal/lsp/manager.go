package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
)

// State represents the lifecycle state of the tinymist LSP manager.
type State int32

const (
	StateOff State = iota
	StateMissing
	StateStarting
	StateReady
	StateRestarting
	StateError
)

func (s State) String() string {
	switch s {
	case StateOff:
		return "off"
	case StateMissing:
		return "no tinymist"
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
	atomicPinnedMain      atomic.Value
	missingHintShown      atomic.Bool

	mu                 sync.Mutex
	gen                uint64
	shuttingDown       bool
	stopCh             chan struct{}
	readyCh            chan struct{}
	sessionCtx         context.Context
	sessionCancel      context.CancelFunc
	lifecycleWG        sync.WaitGroup
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

	docs               map[string]*openDoc
	docErrorCounts     map[string]int
	docErrors          map[string][]string
	docDiagnostics     map[string][]Diagnostic
	docDiagnosticPaths map[string]string
	serverCommands     map[string]bool
	completionTriggers map[string]bool
	sigHelpTriggers    map[string]bool
	jumpStacks         map[*action.Tab][]jumpOrigin

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
	cursorTimer       *time.Timer
	autoCompleteTimer *time.Timer
	sigHelpTimer      *time.Timer
	lastSigMsg        string

	hoverBuf  *buffer.Buffer
	statusBuf *buffer.Buffer

	unregObserver func()

	pinnedMain           string
	pinnedRoot           string
	pinExplicitlyCleared bool
	pinChangeHandler     func(string)
	exportSeq            uint64
	exportCancels        map[string]exportCancelEntry
	formattingOnSave     map[*buffer.SharedBuffer]bool

	discoverFn    func(string) DiscoveryResult
	probeFn       func(context.Context, string) (string, error)
	startClientFn func(context.Context, string, Handlers) (*Client, error)
}

func newManager() *Manager {
	m := &Manager{
		restartBackoff:     defaultRestartBackoff,
		docs:               make(map[string]*openDoc),
		docErrorCounts:     make(map[string]int),
		docErrors:          make(map[string][]string),
		docDiagnostics:     make(map[string][]Diagnostic),
		docDiagnosticPaths: make(map[string]string),
		serverCommands:     make(map[string]bool),
		jumpStacks:         make(map[*action.Tab][]jumpOrigin),
		discoverFn:     Discover,
		probeFn:        ProbeVersion,
		startClientFn: func(ctx context.Context, bin string, h Handlers) (*Client, error) {
			return StartProcessClient(ctx, bin, []string{"lsp"}, "", h)
		},
	}
	m.atomicState.Store(int32(StateOff))
	m.atomicPinnedMain.Store("")
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
	m.sessionCtx, m.sessionCancel = context.WithCancel(context.Background())
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
	m.pinnedMain = ""
	m.pinnedRoot = ""
	m.pinExplicitlyCleared = false
	m.atomicPinnedMain.Store("")
	m.missingHintShown.Store(false)
	if m.rootURI != "" {
		if root := URIToPath(m.rootURI); root != "" {
			if pinned := loadPersistedPin(root); pinned != "" {
				m.pinnedMain = pinned
				m.pinnedRoot = root
				m.atomicPinnedMain.Store(pinned)
			}
		}
	}
	m.docs = make(map[string]*openDoc)
	m.docErrorCounts = make(map[string]int)
	m.docErrors = make(map[string][]string)
	m.docDiagnostics = make(map[string][]Diagnostic)
	m.docDiagnosticPaths = make(map[string]string)
	m.serverCommands = make(map[string]bool)
	m.jumpStacks = make(map[*action.Tab][]jumpOrigin)
	m.resetCompileStatusLocked()
	m.unregObserver = buffer.RegisterObserver(m)

	if !lspEnabled {
		m.setStateLocked(StateOff)
		m.mu.Unlock()
		return
	}

	m.setStateLocked(StateStarting)
	m.lifecycleWG.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.lifecycleWG.Done()
		m.startAsync(gen)
	}()
}

// Shutdown cleanly terminates any active tinymist LSP or preview subprocesses
// and unregisters the buffer observer. Safe and idempotent.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.shuttingDown = true
	m.gen++
	cancelSession := m.sessionCancel
	m.sessionCancel = nil
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
	if m.autoCompleteTimer != nil {
		m.autoCompleteTimer.Stop()
		m.autoCompleteTimer = nil
	}
	if m.sigHelpTimer != nil {
		m.sigHelpTimer.Stop()
		m.sigHelpTimer = nil
	}
	m.lastSigMsg = ""
	m.completionTriggers = nil
	m.sigHelpTriggers = nil
	for _, doc := range m.docs {
		if doc.timer != nil {
			doc.timer.Stop()
			doc.timer = nil
		}
	}
	for k, entry := range m.exportCancels {
		if entry.cancel != nil {
			entry.cancel()
		}
		delete(m.exportCancels, k)
	}
	m.docs = make(map[string]*openDoc)
	m.docErrorCounts = make(map[string]int)
	m.docErrors = make(map[string][]string)
	m.docDiagnostics = make(map[string][]Diagnostic)
	m.docDiagnosticPaths = make(map[string]string)
	m.serverCommands = make(map[string]bool)
	m.jumpStacks = make(map[*action.Tab][]jumpOrigin)

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
	m.pinnedMain = ""
	m.pinnedRoot = ""
	m.pinExplicitlyCleared = false
	m.atomicPinnedMain.Store("")
	m.missingHintShown.Store(false)
	m.resetCompileStatusLocked()
	m.setStateLocked(StateOff)
	m.mu.Unlock()

	if cancelSession != nil {
		cancelSession()
	}
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
	m.lifecycleWG.Wait()
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
	sessionCtx := m.sessionCtx
	if sessionCtx == nil {
		sessionCtx = context.Background()
	}
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
		m.setStateLocked(StateMissing)
		m.mu.Unlock()

		if res.ConfiguredMissing {
			m.missingHintShown.Store(true)
			notifyInfoError("Tinymist not found at configured path (run :tinymist-status): " + configuredPath)
		} else {
			action.EnqueueUI(func() {})
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
			if method == "workspace/applyEdit" {
				var p ApplyWorkspaceEditParams
				if err := json.Unmarshal(params, &p); err != nil {
					return ApplyWorkspaceEditResult{Applied: false, FailureReason: err.Error()}, nil
				}
				return m.handleApplyWorkspaceEdit(ctx, gen, p.Edit), nil
			}
			return defaultServerRequestResult(method, params), nil
		},
	}

	startCtx, cancelStart := context.WithTimeout(sessionCtx, 5*time.Second)
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

	if err := m.handshake(gen, sessionCtx, client); err != nil {
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

	m.mu.Lock()
	pinned := m.pinnedMain
	m.mu.Unlock()
	if pinned != "" {
		_ = sendPinMain(client, pinned)
		if previewTarget != "" {
			previewTarget = pinned
		}
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
		if needProbe {
			m.lifecycleWG.Add(1)
		}
		m.mu.Unlock()
		if needProbe {
			go func(path string) {
				defer m.lifecycleWG.Done()
				probeCtx, cancel := context.WithTimeout(sessionCtx, probeTimeout)
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

	m.mu.Lock()
	if !m.shuttingDown && m.gen == gen && m.client == client {
		m.lifecycleWG.Add(1)
		go func() {
			defer m.lifecycleWG.Done()
			m.watchClient(gen, client)
		}()
	}
	m.mu.Unlock()
}

func (m *Manager) handshake(gen uint64, sessionCtx context.Context, client *Client) error {
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
			"formatterMode": readGlobalString("typst.formatter", "typstyle"),
		},
	}

	if sessionCtx == nil {
		sessionCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(sessionCtx, 5*time.Second)
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
	m.completionTriggers = make(map[string]bool)
	for _, ch := range ExtractTriggerChars(res.Capabilities.CompletionProvider, false) {
		m.completionTriggers[ch] = true
	}
	if len(m.completionTriggers) == 0 {
		for _, ch := range []string{"#", ".", "@", "<"} {
			m.completionTriggers[ch] = true
		}
	}
	m.sigHelpTriggers = make(map[string]bool)
	for _, ch := range ExtractTriggerChars(res.Capabilities.SignatureHelpProvider, true) {
		m.sigHelpTriggers[ch] = true
	}
	if len(m.sigHelpTriggers) == 0 {
		m.sigHelpTriggers["("] = true
		m.sigHelpTriggers[","] = true
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
	m.docErrorCounts = make(map[string]int)
	m.docErrors = make(map[string][]string)
	m.docDiagnostics = make(map[string][]Diagnostic)
	m.docDiagnosticPaths = make(map[string]string)

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
	if pinned := m.getPinnedMain(); pinned != "" {
		fmt.Fprintf(&b, "Pinned main: %s\n", pinned)
	} else {
		fmt.Fprintf(&b, "Pinned main: (none)\n")
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
	oldCancel := m.sessionCancel
	m.sessionCtx, m.sessionCancel = context.WithCancel(context.Background())
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
	m.missingHintShown.Store(false)
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
	for k, entry := range m.exportCancels {
		if entry.cancel != nil {
			entry.cancel()
		}
		delete(m.exportCancels, k)
	}
	m.docs = make(map[string]*openDoc)
	m.docErrorCounts = make(map[string]int)
	m.docErrors = make(map[string][]string)
	m.docDiagnostics = make(map[string][]Diagnostic)
	m.docDiagnosticPaths = make(map[string]string)
	m.serverCommands = make(map[string]bool)
	m.rootURI = ""
	if !lspEnabled {
		if oldClient != nil {
			m.lifecycleWG.Add(1)
		}
		m.setStateLocked(StateOff)
		m.mu.Unlock()
		if oldCancel != nil {
			oldCancel()
		}
		clearAllTinymistDiagnostics()
		if oldClient != nil {
			go func() {
				defer m.lifecycleWG.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = oldClient.Close(ctx)
				cancel()
			}()
		}
		notifyInfoMessage("Tinymist LSP is disabled")
		return
	}
	m.setStateLocked(StateStarting)
	m.lifecycleWG.Add(1)
	m.mu.Unlock()
	if oldCancel != nil {
		oldCancel()
	}

	clearAllTinymistDiagnostics()

	// Snapshot currently open Typst buffers so they are re-opened cleanly.
	for _, b := range buffer.OpenBuffers {
		if isTrackableTypst(b) {
			m.OnBufferOpen(b)
		}
	}

	go func() {
		defer m.lifecycleWG.Done()
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
	if pinned := m.getPinnedMain(); pinned != "" {
		fmt.Fprintf(&sb, "PIN: %s | ", filepath.Base(pinned))
	}
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

func lastNonEmptyLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t != "" {
			return t
		}
	}
	return ""
}

func (m *Manager) notReadyMessage(state State, fallback string) string {
	switch state {
	case StateMissing:
		if !m.missingHintShown.Swap(true) {
			return fmt.Sprintf("tinymist not found — install with: %s (or :tinymist-status)", PrimaryInstallHint(runtime.GOOS))
		}
		return "LSP: no tinymist (run :tinymist-status)"
	case StateStarting:
		return "LSP: starting..."
	case StateError:
		return "LSP: error (run :tinymist-status)"
	default:
		return fallback
	}
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
