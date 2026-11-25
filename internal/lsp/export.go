package lsp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/util"
)

var exportFormats = []string{"pdf", "png", "svg", "html"}

func exportCompleter(b *buffer.Buffer) ([]string, []string) {
	if b == nil {
		return nil, nil
	}
	c := b.GetActiveCursor()
	if c == nil {
		return nil, nil
	}
	input, argstart := b.GetArg()
	lower := strings.ToLower(input)

	var suggestions []string
	for _, choice := range exportFormats {
		if strings.HasPrefix(choice, lower) {
			suggestions = append(suggestions, choice)
		}
	}
	if len(suggestions) == 0 {
		return nil, nil
	}
	prefixChars := util.Max(0, c.X-argstart)
	completions := make([]string, len(suggestions))
	for i, s := range suggestions {
		completions[i] = util.SliceEndStr(s, prefixChars)
	}
	return completions, suggestions
}

func isValidExportFormat(s string) bool {
	switch s {
	case "pdf", "png", "svg", "html":
		return true
	default:
		return false
	}
}

func parseExportArgs(args []string) (format string, outputArg string, err error) {
	format = "pdf"
	if len(args) == 0 {
		return format, "", nil
	}

	first := strings.TrimSpace(args[0])
	lowerFirst := strings.ToLower(first)

	if len(args) == 1 {
		if isValidExportFormat(lowerFirst) {
			return lowerFirst, "", nil
		}
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(first), "."))
		if isValidExportFormat(ext) {
			return ext, first, nil
		}
		return "", "", fmt.Errorf("invalid export format or output %q (use pdf, png, svg, or html)", first)
	}

	if !isValidExportFormat(lowerFirst) {
		return "", "", fmt.Errorf("invalid export format %q (use pdf, png, svg, or html)", first)
	}
	return lowerFirst, strings.TrimSpace(args[1]), nil
}

type exportCancelEntry struct {
	seq    uint64
	cancel context.CancelFunc
}

type exportRequest struct {
	targetPath          string
	rootDir             string
	format              string
	outputPath          string
	displayOutput       string
	shadowContent       []byte
	useShadow           bool
	ppi                 int
	warnUnsavedSubfiles bool
	cancelKey           string
}

func (m *Manager) exportCommand(bp *action.BufPane, args []string) {
	format, outputArg, err := parseExportArgs(args)
	if err != nil {
		notifyInfoError(err.Error())
		return
	}

	var b *buffer.Buffer
	if bp != nil {
		b = bp.Buf
	}
	pinned := m.getPinnedMain()

	var targetPath string
	if pinned != "" {
		targetPath = pinned
	} else if b != nil && b.IsTypst() && b.AbsPath != "" {
		targetPath = b.AbsPath
	}

	if targetPath == "" && (b == nil || !b.IsTypst()) {
		notifyInfoError("Export requires a .typ buffer or a pinned main file")
		return
	}
	if targetPath == "" && outputArg == "" {
		notifyInfoError("Save file or specify an output path before exporting")
		return
	}

	m.startExportForTarget(targetPath, b, format, outputArg, false)
}

func (m *Manager) maybeExportOnSave(b *buffer.Buffer) {
	if !isTrackableTypst(b) {
		return
	}
	format := strings.ToLower(strings.TrimSpace(readGlobalString("typst.exportOnSave", "off")))
	if !isValidExportFormat(format) {
		return
	}

	targetPath := b.AbsPath
	if pinned := m.getPinnedMain(); pinned != "" {
		pinnedRoot := findProjectRoot(pinned)
		if canonicalDocKey(b.AbsPath) == canonicalDocKey(pinned) ||
			(pinnedRoot != "" && isSubPath(pinnedRoot, b.AbsPath)) {
			targetPath = pinned
		}
	}
	m.startExportForTarget(targetPath, b, format, "", true)
}

func (m *Manager) startExportForTarget(targetPath string, activeBuf *buffer.Buffer, format, outputArg string, quiet bool) {
	req, err := m.buildExportRequest(targetPath, activeBuf, format, outputArg)
	if err != nil {
		notifyInfoError(err.Error())
		return
	}

	m.mu.Lock()
	configuredPath := m.tinymistPath
	discoverFn := m.discoverFn
	m.mu.Unlock()
	if configuredPath == "" {
		configuredPath = readGlobalString("typst.tinymistPath", "")
	}
	if discoverFn == nil {
		discoverFn = Discover
	}
	res := discoverFn(configuredPath)
	if res.Path == "" {
		notifyInfoError("Cannot export: tinymist binary not found")
		return
	}

	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		return
	}
	if m.exportCancels == nil {
		m.exportCancels = make(map[string]exportCancelEntry)
	}
	if prev, ok := m.exportCancels[req.cancelKey]; ok && prev.cancel != nil {
		prev.cancel()
	}
	m.exportSeq++
	seq := m.exportSeq
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	gen := m.gen
	m.exportCancels[req.cancelKey] = exportCancelEntry{seq: seq, cancel: cancel}
	m.mu.Unlock()

	if !quiet {
		notifyInfoMessage(fmt.Sprintf("Exporting %s...", filepath.Base(req.displayOutput)))
	}

	go m.runExport(ctx, cancel, gen, seq, res.Path, req)
}

func (m *Manager) buildExportRequest(targetPath string, activeBuf *buffer.Buffer, format, outputArg string) (exportRequest, error) {
	req := exportRequest{
		targetPath: targetPath,
		format:     format,
		ppi:        readGlobalInt("typst.previewPPI", 144),
	}
	if req.ppi < 72 {
		req.ppi = 144
	}
	if req.ppi > 300 {
		req.ppi = 300
	}

	var outPath string
	if outputArg != "" {
		if filepath.IsAbs(outputArg) {
			outPath = filepath.Clean(outputArg)
		} else if targetPath != "" {
			outPath = filepath.Join(filepath.Dir(targetPath), outputArg)
		} else if cwd, err := os.Getwd(); err == nil {
			outPath = filepath.Join(cwd, outputArg)
		} else {
			outPath = outputArg
		}
		if absOut, err := filepath.Abs(outPath); err == nil {
			outPath = absOut
		}
	} else {
		dir := filepath.Dir(targetPath)
		stem := strings.TrimSuffix(filepath.Base(targetPath), filepath.Ext(targetPath))
		switch format {
		case "png", "svg":
			outPath = filepath.Join(dir, fmt.Sprintf("%s-{0p}.%s", stem, format))
		default:
			outPath = filepath.Join(dir, fmt.Sprintf("%s.%s", stem, format))
		}
	}
	req.outputPath = outPath
	req.displayOutput = outPath

	rootDir := ""
	if targetPath != "" {
		rootDir = findProjectRoot(targetPath)
	} else {
		rootDir = filepath.Dir(outPath)
	}
	req.rootDir = rootDir

	// Stage shadow bytes if the target itself has unsaved in-memory edits (or is unnamed).
	var targetBuf *buffer.Buffer
	if targetPath == "" {
		targetBuf = activeBuf
	} else if activeBuf != nil && canonicalDocKey(activeBuf.AbsPath) == canonicalDocKey(targetPath) {
		targetBuf = activeBuf
	} else {
		targetKey := canonicalDocKey(targetPath)
		for _, ob := range buffer.OpenBuffers {
			if ob != nil && ob.AbsPath != "" && canonicalDocKey(ob.AbsPath) == targetKey {
				targetBuf = ob
				break
			}
		}
	}

	if targetBuf != nil && (targetPath == "" || targetBuf.Modified()) {
		raw := targetBuf.Bytes()
		cp := make([]byte, len(raw))
		copy(cp, raw)
		req.shadowContent = cp
		req.useShadow = true
	}

	// If compiling a pinned main file, check whether any open subfile under rootDir is dirty.
	if targetPath != "" && rootDir != "" {
		targetKey := canonicalDocKey(targetPath)
		for _, ob := range buffer.OpenBuffers {
			if ob == nil || !ob.IsTypst() || !ob.Modified() || ob.AbsPath == "" {
				continue
			}
			if canonicalDocKey(ob.AbsPath) == targetKey {
				continue
			}
			if isSubPath(rootDir, ob.AbsPath) {
				req.warnUnsavedSubfiles = true
				break
			}
		}
	}

	if targetPath != "" {
		req.cancelKey = canonicalDocKey(targetPath)
	} else {
		req.cancelKey = "unnamed:" + outPath
	}

	return req, nil
}

func (m *Manager) runExport(ctx context.Context, cancel context.CancelFunc, gen uint64, seq uint64, binPath string, req exportRequest) {
	defer func() {
		cancel()
		m.mu.Lock()
		if cur, ok := m.exportCancels[req.cancelKey]; ok && cur.seq == seq {
			delete(m.exportCancels, req.cancelKey)
		}
		m.mu.Unlock()
	}()

	inputPath := req.targetPath
	if req.useShadow {
		shadowDir := os.TempDir()
		if req.targetPath != "" {
			candidate := filepath.Dir(req.targetPath)
			if st, err := os.Stat(candidate); err == nil && st.IsDir() {
				shadowDir = candidate
			}
		} else if st, err := os.Stat(filepath.Dir(req.outputPath)); err == nil && st.IsDir() {
			shadowDir = filepath.Dir(req.outputPath)
		}
		f, err := os.CreateTemp(shadowDir, ".typx-export-*.typ")
		if err != nil && shadowDir != os.TempDir() {
			f, err = os.CreateTemp("", "typx-export-*.typ")
		}
		if err != nil {
			notifyInfoError("Export failed to stage unsaved buffer: " + err.Error())
			return
		}
		shadowPath := f.Name()
		_, werr := f.Write(req.shadowContent)
		cerr := f.Close()
		defer os.Remove(shadowPath)
		if werr != nil || cerr != nil {
			if werr != nil {
				notifyInfoError("Export write error: " + werr.Error())
			} else {
				notifyInfoError("Export close error: " + cerr.Error())
			}
			return
		}
		inputPath = shadowPath
	}

	if outDir := filepath.Dir(req.outputPath); outDir != "" {
		if err := os.MkdirAll(outDir, 0755); err != nil {
			notifyInfoError("Export failed to create output directory: " + err.Error())
			return
		}
	}

	rootDir := req.rootDir
	if rootDir == "" || (req.useShadow && !isSubPath(rootDir, inputPath)) {
		rootDir = filepath.Dir(inputPath)
	}

	args := []string{
		"compile",
		"--root", rootDir,
		"--format", req.format,
	}
	if req.format == "png" {
		args = append(args, "--ppi", strconv.Itoa(req.ppi))
	}
	args = append(args, inputPath, req.outputPath)

	cmd := exec.Command(binPath, args...)
	if rootDir != "" {
		cmd.Dir = rootDir
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	job, err := startManagedProcess(cmd)
	if err != nil {
		notifyInfoError("Export failed to launch tinymist: " + err.Error())
		return
	}

	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = job.Close()
		case <-stopWatch:
		}
	}()

	waitErr := cmd.Wait()
	close(stopWatch)
	canceled := ctx.Err() == context.Canceled
	_ = job.Close()

	m.mu.Lock()
	stale := m.shuttingDown || m.gen != gen
	m.mu.Unlock()
	if stale || canceled {
		return
	}

	if waitErr != nil {
		msg := collapseDiagnosticMessage(stderr.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		notifyInfoError("Export failed: " + msg)
		return
	}

	statusMsg := fmt.Sprintf("Exported %s", filepath.Base(req.displayOutput))
	var notes []string
	if warn := lastNonEmptyLine(strings.Split(strings.ReplaceAll(stderr.String(), "\r\n", "\n"), "\n")); warn != "" {
		notes = append(notes, warn)
	}
	if req.warnUnsavedSubfiles {
		notes = append(notes, "unsaved subfiles not included")
	}
	if len(notes) > 0 {
		statusMsg += " (" + strings.Join(notes, "; ") + ")"
	}
	notifyInfoMessage(statusMsg)
}

func isSubPath(root, target string) bool {
	if root == "" || target == "" {
		return false
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
