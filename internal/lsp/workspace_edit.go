package lsp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"unicode/utf8"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/util"
)

type preparedWorkspaceTarget struct {
	path      string
	openBuf   *buffer.Buffer
	batch     []buffer.BatchEdit
	diskBytes []byte
	editCount int
}

// ApplyWorkspaceEdit validates and applies a multi-file LSP WorkspaceEdit across
// open TypX buffers (each getting a single atomic undo step) and unopened files
// on disk. All targets are validated pre-flight before any mutation occurs.
// Must be called on the main UI goroutine.
func ApplyWorkspaceEdit(we WorkspaceEdit) (filesChanged int, editsApplied int, err error) {
	editsByURI, uris, err := WorkspaceEditFileEdits(we)
	if err != nil {
		return 0, 0, err
	}
	if len(uris) == 0 {
		return 0, 0, nil
	}

	// Canonicalize by local path so equivalent URIs merge cleanly.
	byKey := make(map[string]*preparedWorkspaceTarget, len(uris))
	orderedKeys := make([]string, 0, len(uris))
	editsByKey := make(map[string][]TextEdit, len(uris))
	for _, uri := range uris {
		path := URIToPath(uri)
		key := canonicalDocKey(path)
		if path == "" || key == "" {
			return 0, 0, fmt.Errorf("invalid workspace edit target URI %q", uri)
		}
		if _, exists := byKey[key]; !exists {
			byKey[key] = &preparedWorkspaceTarget{path: path}
			orderedKeys = append(orderedKeys, key)
		}
		editsByKey[key] = append(editsByKey[key], editsByURI[uri]...)
	}

	// Index open buffers by canonical path (deduplicating SharedBuffer instances).
	openByKey := make(map[string]*buffer.Buffer)
	for _, b := range buffer.OpenBuffers {
		if b == nil || b.Type.Scratch || b.AbsPath == "" {
			continue
		}
		k := canonicalDocKey(b.AbsPath)
		if _, exists := openByKey[k]; !exists {
			openByKey[k] = b
		}
	}

	// Pre-flight validation & computation (zero mutations before everything validates).
	for _, key := range orderedKeys {
		target := byKey[key]
		edits := editsByKey[key]
		if isTypstPackageCachePath(target.path) {
			return 0, 0, fmt.Errorf("refusing to edit read-only package cache file: %s", filepath.Base(target.path))
		}
		if ob, ok := openByKey[key]; ok {
			if ob.Type.Readonly {
				return 0, 0, fmt.Errorf("target buffer is read-only: %s", filepath.Base(target.path))
			}
			batch, prepErr := prepareTextEdits(ob, edits)
			if prepErr != nil {
				return 0, 0, fmt.Errorf("%s: %w", filepath.Base(target.path), prepErr)
			}
			target.openBuf = ob
			target.batch = batch
			target.editCount = len(batch)
			continue
		}

		info, statErr := os.Stat(target.path)
		if statErr != nil || info.IsDir() {
			return 0, 0, fmt.Errorf("target file unavailable: %s", target.path)
		}
		raw, readErr := os.ReadFile(target.path)
		if readErr != nil {
			return 0, 0, fmt.Errorf("read %s: %w", filepath.Base(target.path), readErr)
		}
		updated, count, applyErr := applyTextEditsToBytes(raw, edits)
		if applyErr != nil {
			return 0, 0, fmt.Errorf("%s: %w", filepath.Base(target.path), applyErr)
		}
		target.diskBytes = updated
		target.editCount = count
	}

	// Commit phase.
	for _, key := range orderedKeys {
		target := byKey[key]
		if target.editCount == 0 {
			continue
		}
		if target.openBuf != nil {
			target.openBuf.EventHandler.ApplyAtomicEdits(target.batch)
			target.openBuf.RelocateCursors()
		} else {
			if writeErr := util.SafeWrite(target.path, target.diskBytes, false); writeErr != nil {
				return filesChanged, editsApplied, fmt.Errorf("write %s: %w", filepath.Base(target.path), writeErr)
			}
		}
		filesChanged++
		editsApplied += target.editCount
	}
	return filesChanged, editsApplied, nil
}

type byteResolvedEdit struct {
	start int
	end   int
	text  []byte
}

func applyTextEditsToBytes(raw []byte, edits []TextEdit) ([]byte, int, error) {
	if len(edits) == 0 {
		return raw, 0, nil
	}

	useCRLF := bytes.Contains(raw, []byte("\r\n"))
	norm := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	norm = bytes.ReplaceAll(norm, []byte("\r"), []byte(""))

	lineStarts := []int{0}
	for i, b := range norm {
		if b == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}

	byteOffsetAt := func(pos Position) int {
		if pos.Line < 0 {
			return 0
		}
		if pos.Line >= len(lineStarts) {
			return len(norm)
		}
		lineStart := lineStarts[pos.Line]
		lineEnd := len(norm)
		if pos.Line+1 < len(lineStarts) {
			lineEnd = lineStarts[pos.Line+1] - 1 // exclude '\n'
		}
		lineSlice := norm[lineStart:lineEnd]
		runeCol := UTF16ToRuneCol(lineSlice, pos.Character)
		off := 0
		for r := 0; r < runeCol && off < len(lineSlice); r++ {
			_, size := utf8.DecodeRune(lineSlice[off:])
			if size <= 0 {
				break
			}
			off += size
		}
		return lineStart + off
	}

	resolved := make([]byteResolvedEdit, 0, len(edits))
	for i, ed := range edits {
		start := byteOffsetAt(ed.Range.Start)
		end := byteOffsetAt(ed.Range.End)
		if end < start {
			return nil, 0, fmt.Errorf("invalid text edit %d: end before start", i)
		}
		newText := bytes.ReplaceAll([]byte(ed.NewText), []byte("\r\n"), []byte("\n"))
		newText = bytes.ReplaceAll(newText, []byte("\r"), []byte(""))
		if start == end && len(newText) == 0 {
			continue
		}
		resolved = append(resolved, byteResolvedEdit{
			start: start,
			end:   end,
			text:  newText,
		})
	}
	if len(resolved) == 0 {
		return raw, 0, nil
	}

	sort.SliceStable(resolved, func(i, j int) bool {
		if resolved[i].start != resolved[j].start {
			return resolved[i].start < resolved[j].start
		}
		return resolved[i].end < resolved[j].end
	})

	for i := 0; i+1 < len(resolved); i++ {
		if resolved[i].end > resolved[i+1].start {
			return nil, 0, errors.New("overlapping LSP text edits")
		}
	}

	var out bytes.Buffer
	out.Grow(len(norm))
	cursor := 0
	for _, r := range resolved {
		out.Write(norm[cursor:r.start])
		out.Write(r.text)
		cursor = r.end
	}
	out.Write(norm[cursor:])

	result := out.Bytes()
	if useCRLF {
		result = bytes.ReplaceAll(result, []byte("\n"), []byte("\r\n"))
	}
	return result, len(resolved), nil
}

func (m *Manager) handleApplyWorkspaceEdit(ctx context.Context, gen uint64, edit WorkspaceEdit) ApplyWorkspaceEditResult {
	m.mu.Lock()
	if m.shuttingDown || m.gen != gen {
		m.mu.Unlock()
		return ApplyWorkspaceEditResult{Applied: false, FailureReason: "LSP manager inactive"}
	}
	stopCh := m.stopCh
	m.mu.Unlock()

	type applyRes struct {
		err error
	}
	resCh := make(chan applyRes, 1)
	action.EnqueueUI(func() {
		m.mu.Lock()
		stale := m.shuttingDown || m.gen != gen
		m.mu.Unlock()
		if stale {
			resCh <- applyRes{err: errors.New("LSP session closed")}
			return
		}
		_, _, err := ApplyWorkspaceEdit(edit)
		resCh <- applyRes{err: err}
	})

	select {
	case r := <-resCh:
		if r.err != nil {
			return ApplyWorkspaceEditResult{Applied: false, FailureReason: r.err.Error()}
		}
		return ApplyWorkspaceEditResult{Applied: true}
	case <-ctx.Done():
		return ApplyWorkspaceEditResult{Applied: false, FailureReason: ctx.Err().Error()}
	case <-stopCh:
		return ApplyWorkspaceEditResult{Applied: false, FailureReason: "LSP shutdown"}
	}
}
