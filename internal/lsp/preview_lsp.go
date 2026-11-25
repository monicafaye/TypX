package lsp

import (
	"bufio"
	"context"
	"encoding/json"
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
