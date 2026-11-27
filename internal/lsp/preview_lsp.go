package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
)

// OnCursorMove debounces documentHighlight and, when live preview is active,
// preview cursor scroll synchronization.
func (m *Manager) OnCursorMove(b *buffer.Buffer, ev buffer.CursorEvent) {
	if !isTrackableTypst(b) {
		return
	}
	m.scheduleDocumentHighlight(b, ev.New)

	if !m.atomicPreview.Load() {
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

func (m *Manager) togglePreview(bp *action.BufPane) bool {
	if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
		return false
	}
	openBrowser := readGlobalBool("typst.previewOpenBrowser", true)
	absPath := bp.Buf.AbsPath
	if pinned := m.getPinnedMain(); pinned != "" {
		absPath = pinned
	}

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
		notifyInfoError("Cannot start preview: " + m.notReadyMessage(StateMissing, "tinymist binary not found"))
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

type previewScrollTarget struct {
	TaskID   string
	FilePath string
	Pos      Position
}

func parsePreviewScrollSource(raw json.RawMessage) (previewScrollTarget, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return previewScrollTarget{}, false
	}
	var payload struct {
		TaskID       string          `json:"taskId"`
		FilePath     string          `json:"filepath"`
		URI          string          `json:"uri"`
		Path         string          `json:"path"`
		Line         *int            `json:"line"`
		Character    *int            `json:"character"`
		Col          *int            `json:"col"`
		Column       *int            `json:"column"`
		Start        json.RawMessage `json:"start"`
		End          json.RawMessage `json:"end"`
		Position     json.RawMessage `json:"position"`
		Range        json.RawMessage `json:"range"`
		TextDocument *struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Location *struct {
			URI   string          `json:"uri"`
			Range json.RawMessage `json:"range"`
		} `json:"location"`
	}
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return previewScrollTarget{}, false
	}
	if payload.TaskID != "" && payload.TaskID != previewTaskID {
		return previewScrollTarget{}, false
	}

	rawPath := payload.FilePath
	if rawPath == "" {
		rawPath = payload.URI
	}
	if rawPath == "" {
		rawPath = payload.Path
	}
	if rawPath == "" && payload.TextDocument != nil {
		rawPath = payload.TextDocument.URI
	}
	if rawPath == "" && payload.Location != nil {
		rawPath = payload.Location.URI
	}
	filePath := strings.TrimSpace(URIToPath(rawPath))
	if filePath == "" {
		return previewScrollTarget{}, false
	}

	var pos Position
	hasPos := false
	if len(bytes.TrimSpace(payload.Range)) > 0 {
		var rng struct {
			Start json.RawMessage `json:"start"`
			End   json.RawMessage `json:"end"`
		}
		if json.Unmarshal(payload.Range, &rng) == nil {
			if p, ok := parseScrollPositionValue(rng.Start); ok {
				pos = p
				hasPos = true
			} else if p, ok := parseScrollPositionValue(rng.End); ok {
				pos = p
				hasPos = true
			}
		}
	}
	if !hasPos && len(bytes.TrimSpace(payload.Position)) > 0 {
		if p, ok := parseScrollPositionValue(payload.Position); ok {
			pos = p
			hasPos = true
		}
	}
	if !hasPos && len(bytes.TrimSpace(payload.Start)) > 0 {
		if p, ok := parseScrollPositionValue(payload.Start); ok {
			pos = p
			hasPos = true
		}
	}
	if !hasPos && len(bytes.TrimSpace(payload.End)) > 0 {
		if p, ok := parseScrollPositionValue(payload.End); ok {
			pos = p
			hasPos = true
		}
	}
	if !hasPos && payload.Location != nil && len(bytes.TrimSpace(payload.Location.Range)) > 0 {
		var rng struct {
			Start json.RawMessage `json:"start"`
			End   json.RawMessage `json:"end"`
		}
		if json.Unmarshal(payload.Location.Range, &rng) == nil {
			if p, ok := parseScrollPositionValue(rng.Start); ok {
				pos = p
				hasPos = true
			} else if p, ok := parseScrollPositionValue(rng.End); ok {
				pos = p
				hasPos = true
			}
		}
	}
	if !hasPos && payload.Line != nil {
		pos.Line = *payload.Line
		switch {
		case payload.Character != nil:
			pos.Character = *payload.Character
		case payload.Col != nil:
			pos.Character = *payload.Col
		case payload.Column != nil:
			pos.Character = *payload.Column
		}
		hasPos = true
	}
	if !hasPos {
		return previewScrollTarget{}, false
	}

	if pos.Line < 0 {
		pos.Line = 0
	}
	if pos.Character < 0 {
		pos.Character = 0
	}
	return previewScrollTarget{
		TaskID:   payload.TaskID,
		FilePath: filePath,
		Pos:      pos,
	}, true
}

func parseScrollPositionValue(raw json.RawMessage) (Position, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return Position{}, false
	}
	if trimmed[0] == '[' {
		var arr []int
		if err := json.Unmarshal(trimmed, &arr); err != nil || len(arr) == 0 {
			return Position{}, false
		}
		pos := Position{Line: arr[0]}
		if len(arr) >= 2 {
			pos.Character = arr[1]
		}
		return pos, true
	}
	var obj struct {
		Line      *int `json:"line"`
		Character *int `json:"character"`
		Col       *int `json:"col"`
		Column    *int `json:"column"`
	}
	if err := json.Unmarshal(trimmed, &obj); err != nil || obj.Line == nil {
		return Position{}, false
	}
	pos := Position{Line: *obj.Line}
	switch {
	case obj.Character != nil:
		pos.Character = *obj.Character
	case obj.Col != nil:
		pos.Character = *obj.Col
	case obj.Column != nil:
		pos.Character = *obj.Column
	}
	return pos, true
}

func (m *Manager) handlePreviewScrollSource(gen uint64, params json.RawMessage) {
	m.mu.Lock()
	if m.shuttingDown || m.gen != gen {
		m.mu.Unlock()
		return
	}
	previewRoot := m.previewRoot
	rootURI := m.rootURI
	m.mu.Unlock()

	target, ok := parsePreviewScrollSource(params)
	if !ok {
		return
	}

	if !filepath.IsAbs(target.FilePath) {
		switch {
		case previewRoot != "":
			target.FilePath = filepath.Join(previewRoot, target.FilePath)
		case URIToPath(rootURI) != "":
			target.FilePath = filepath.Join(URIToPath(rootURI), target.FilePath)
		default:
			if abs, err := filepath.Abs(target.FilePath); err == nil {
				target.FilePath = abs
			}
		}
	}

	action.EnqueueUI(func() {
		m.mu.Lock()
		stale := m.shuttingDown || m.gen != gen
		m.mu.Unlock()
		if stale {
			return
		}
		var bp *action.BufPane
		if action.Tabs != nil && len(action.Tabs.List) > 0 {
			if idx := action.Tabs.Active(); idx >= 0 && idx < len(action.Tabs.List) {
				if mt := action.Tabs.List[idx]; mt != nil {
					bp = mt.CurPane()
				}
			}
		}
		m.jumpToFilePosition(bp, target.FilePath, target.Pos)
	})
}

func (m *Manager) cancelHighlightForShared(shared *buffer.SharedBuffer) {
	m.mu.Lock()
	if shared == nil || m.highlightShared == shared {
		if m.highlightTimer != nil {
			m.highlightTimer.Stop()
			m.highlightTimer = nil
		}
		m.highlightSeq++
		m.highlightShared = nil
	}
	m.mu.Unlock()
}

func (m *Manager) scheduleDocumentHighlight(b *buffer.Buffer, loc buffer.Loc) {
	if b == nil {
		return
	}
	if !readGlobalBool("typst.documentHighlight", true) {
		m.mu.Lock()
		prevShared := m.highlightShared
		if m.highlightTimer != nil {
			m.highlightTimer.Stop()
			m.highlightTimer = nil
		}
		m.highlightSeq++
		m.highlightShared = nil
		m.mu.Unlock()
		if prevShared != nil && prevShared != b.SharedBuffer {
			prevShared.ClearSymbolHighlights()
		}
		b.ClearSymbolHighlights()
		return
	}

	m.mu.Lock()
	prevShared := m.highlightShared
	if m.shuttingDown || m.State() != StateReady || m.client == nil || !m.docHighlightSupported {
		if m.highlightTimer != nil {
			m.highlightTimer.Stop()
			m.highlightTimer = nil
		}
		m.highlightSeq++
		m.highlightShared = nil
		m.mu.Unlock()
		if prevShared != nil && prevShared != b.SharedBuffer {
			prevShared.ClearSymbolHighlights()
		}
		b.ClearSymbolHighlights()
		return
	}
	if m.highlightTimer != nil {
		m.highlightTimer.Stop()
	}
	m.highlightSeq++
	seq := m.highlightSeq
	gen := m.gen
	m.highlightShared = b.SharedBuffer
	reqVer := b.Version()
	targetBuf := b
	m.highlightTimer = time.AfterFunc(documentHighlightDelay, func() {
		action.EnqueueUI(func() {
			m.dispatchDocumentHighlight(targetBuf, gen, seq, reqVer, loc)
		})
	})
	m.mu.Unlock()
	if prevShared != nil && prevShared != b.SharedBuffer {
		prevShared.ClearSymbolHighlights()
	}
}

func (m *Manager) dispatchDocumentHighlight(targetBuf *buffer.Buffer, gen, seq, reqVer uint64, reqLoc buffer.Loc) {
	m.mu.Lock()
	if m.shuttingDown || m.gen != gen || m.highlightSeq != seq || m.State() != StateReady || m.client == nil || !m.docHighlightSupported {
		m.mu.Unlock()
		return
	}
	c := m.client
	sessionCtx := m.sessionCtx
	m.mu.Unlock()

	if !readGlobalBool("typst.documentHighlight", true) {
		if targetBuf != nil {
			targetBuf.ClearSymbolHighlights()
		}
		return
	}
	if !isBufferOpenAndAt(targetBuf, reqVer, reqLoc, false) {
		return
	}

	m.flushDocChangeNow(targetBuf)

	if targetBuf.AbsPath == "" {
		return
	}
	uri := PathToURI(targetBuf.AbsPath)
	rawLine := targetBuf.LineBytes(reqLoc.Y)
	lineCopy := append([]byte(nil), rawLine...)
	utf16Col := RuneColToUTF16(lineCopy, locXToRuneCol(lineCopy, reqLoc.X))
	shared := targetBuf.SharedBuffer

	go func() {
		if sessionCtx == nil {
			sessionCtx = context.Background()
		}
		ctx, cancel := context.WithTimeout(sessionCtx, 1500*time.Millisecond)
		var raw json.RawMessage
		err := c.Call(ctx, "textDocument/documentHighlight", DocumentHighlightParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: reqLoc.Y, Character: utf16Col},
		}, &raw)
		canceled := ctx.Err() == context.Canceled
		cancel()

		if err != nil {
			var rpcErr *RPCError
			if errors.As(err, &rpcErr) && rpcErr.Code == -32601 {
				m.mu.Lock()
				if !m.shuttingDown && m.gen == gen {
					m.docHighlightSupported = false
				}
				m.mu.Unlock()
			}
			if !canceled {
				action.EnqueueUI(func() {
					m.mu.Lock()
					stale := m.shuttingDown || m.gen != gen || m.highlightSeq != seq
					m.mu.Unlock()
					if stale || targetBuf.SharedBuffer != shared || !isBufferOpenAndAt(targetBuf, reqVer, reqLoc, false) {
						return
					}
					targetBuf.ClearSymbolHighlights()
				})
			}
			return
		}

		highlights, perr := ParseDocumentHighlights(raw)
		action.EnqueueUI(func() {
			m.mu.Lock()
			stale := m.shuttingDown || m.gen != gen || m.highlightSeq != seq
			m.mu.Unlock()
			if stale || targetBuf.SharedBuffer != shared || !isBufferOpenAndAt(targetBuf, reqVer, reqLoc, false) {
				return
			}
			if !readGlobalBool("typst.documentHighlight", true) || perr != nil || len(highlights) == 0 {
				targetBuf.ClearSymbolHighlights()
				return
			}
			numLines := targetBuf.LinesNum()
			ranges := make([]buffer.HighlightRange, 0, len(highlights))
			for _, h := range highlights {
				if numLines <= 0 || h.Range.End.Line < 0 || h.Range.Start.Line >= numLines {
					continue
				}
				start := lspPositionToLoc(targetBuf, h.Range.Start)
				end := lspPositionToLoc(targetBuf, h.Range.End)
				if end.LessThan(start) {
					start, end = end, start
				}
				if start == end {
					continue
				}
				ranges = append(ranges, buffer.HighlightRange{Start: start, End: end})
			}
			if len(ranges) == 0 {
				targetBuf.ClearSymbolHighlights()
				return
			}
			targetBuf.SetSymbolHighlights(ranges)
		})
	}()
}

func clearAllSymbolHighlights() {
	seen := make(map[*buffer.SharedBuffer]bool)
	for _, b := range buffer.OpenBuffers {
		if b == nil || b.SharedBuffer == nil || seen[b.SharedBuffer] {
			continue
		}
		seen[b.SharedBuffer] = true
		b.ClearSymbolHighlights()
	}
}
