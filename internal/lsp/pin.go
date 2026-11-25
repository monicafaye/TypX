package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/util"
)

const pinsFilename = "pins.json"

func (m *Manager) getPinnedMain() string {
	v := m.atomicPinnedMain.Load()
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func (m *Manager) setPinChangeHandler(fn func(string)) {
	m.mu.Lock()
	m.pinChangeHandler = fn
	m.mu.Unlock()
}

func (m *Manager) pinCommand(bp *action.BufPane, args []string) {
	var targetPath string
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		arg := strings.TrimSpace(args[0])
		if filepath.IsAbs(arg) {
			targetPath = arg
		} else if bp != nil && bp.Buf != nil && bp.Buf.AbsPath != "" {
			targetPath = filepath.Join(filepath.Dir(bp.Buf.AbsPath), arg)
		} else if cwd, err := os.Getwd(); err == nil {
			targetPath = filepath.Join(cwd, arg)
		} else {
			targetPath = arg
		}
	} else {
		if bp == nil || bp.Buf == nil || !bp.Buf.IsTypst() {
			notifyInfoError("Pin requires a .typ buffer or explicit .typ file path")
			return
		}
		if bp.Buf.AbsPath == "" {
			notifyInfoError("Save file before pinning")
			return
		}
		targetPath = bp.Buf.AbsPath
	}

	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		absPath = filepath.Clean(targetPath)
	}
	absPath = filepath.Clean(absPath)

	info, err := os.Stat(absPath)
	if err != nil || info.IsDir() {
		notifyInfoError("Cannot pin missing file: " + absPath)
		return
	}
	if !strings.EqualFold(filepath.Ext(absPath), ".typ") {
		notifyInfoError("Pin target must be a .typ file")
		return
	}

	rootDir := findProjectRoot(absPath)
	savePersistedPin(rootDir, absPath)

	m.mu.Lock()
	m.pinnedMain = absPath
	m.pinnedRoot = rootDir
	m.pinExplicitlyCleared = false
	m.atomicPinnedMain.Store(absPath)
	c := m.client
	ready := m.State() == StateReady && c != nil
	cb := m.pinChangeHandler
	restartPreview := m.previewActive
	openBrowser := m.previewOpenBrowser
	m.mu.Unlock()

	if ready {
		go func(client *Client, p string) {
			_ = sendPinMain(client, p)
		}(c, absPath)
	}

	if cb != nil {
		cb(absPath)
	}

	if restartPreview {
		go func(target string, open bool) {
			m.stopPreview()
			m.startPreview(target, open)
		}(absPath, openBrowser)
	}

	if ready {
		notifyInfoMessage("Pinned main: " + filepath.Base(absPath))
	} else {
		notifyInfoMessage("Pinned main (offline): " + filepath.Base(absPath))
	}
}

func (m *Manager) unpinCommand(bp *action.BufPane) {
	m.mu.Lock()
	prevRoot := m.pinnedRoot
	m.pinnedMain = ""
	m.pinnedRoot = ""
	m.pinExplicitlyCleared = true
	m.atomicPinnedMain.Store("")
	c := m.client
	ready := m.State() == StateReady && c != nil
	cb := m.pinChangeHandler
	restartPreview := m.previewActive
	openBrowser := m.previewOpenBrowser
	m.mu.Unlock()

	if prevRoot != "" {
		savePersistedPin(prevRoot, "")
	}
	if bp != nil && bp.Buf != nil && bp.Buf.AbsPath != "" {
		if r := findProjectRoot(bp.Buf.AbsPath); r != "" && canonicalDocKey(r) != canonicalDocKey(prevRoot) {
			savePersistedPin(r, "")
		}
	}

	if ready {
		go func(client *Client) {
			_ = sendUnpinMain(client)
		}(c)
	}

	if cb != nil {
		cb("")
	}

	if restartPreview && bp != nil && bp.Buf != nil && bp.Buf.AbsPath != "" {
		go func(target string, open bool) {
			m.stopPreview()
			m.startPreview(target, open)
		}(bp.Buf.AbsPath, openBrowser)
	}

	notifyInfoMessage("Unpinned main file")
}

func sendPinMain(c *Client, absPath string) error {
	if c == nil || absPath == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return c.Call(ctx, "workspace/executeCommand", ExecuteCommandParams{
		Command:   "tinymist.pinMain",
		Arguments: []any{absPath},
	}, nil)
}

func sendUnpinMain(c *Client) error {
	if c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return c.Call(ctx, "workspace/executeCommand", ExecuteCommandParams{
		Command:   "tinymist.pinMain",
		Arguments: []any{nil},
	}, nil)
}

func pinsFilePath() string {
	if config.ConfigDir == "" {
		return ""
	}
	return filepath.Join(config.ConfigDir, pinsFilename)
}

func readPinsMap() (map[string]string, bool) {
	p := pinsFilePath()
	if p == "" {
		return map[string]string{}, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return map[string]string{}, false
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return map[string]string{}, false
	}
	pruned := false
	clean := make(map[string]string, len(raw))
	for k, v := range raw {
		if k == "" || v == "" {
			pruned = true
			continue
		}
		if st, statErr := os.Stat(v); statErr != nil || st.IsDir() {
			pruned = true
			continue
		}
		clean[k] = filepath.Clean(v)
	}
	return clean, pruned
}

func writePinsMap(m map[string]string) {
	p := pinsFilePath()
	if p == "" {
		return
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = util.SafeWrite(p, append(data, '\n'), false)
}

func loadPersistedPin(rootDir string) string {
	if rootDir == "" {
		return ""
	}
	pins, pruned := readPinsMap()
	if pruned {
		writePinsMap(pins)
	}
	return pins[canonicalDocKey(rootDir)]
}

func savePersistedPin(rootDir, mainPath string) {
	if rootDir == "" {
		return
	}
	pins, _ := readPinsMap()
	key := canonicalDocKey(rootDir)
	if mainPath == "" {
		delete(pins, key)
	} else {
		pins[key] = filepath.Clean(mainPath)
	}
	writePinsMap(pins)
}
