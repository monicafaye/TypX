package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
)

func TestApplyWorkspaceEditMixedOpenSharedAndDiskCRLF(t *testing.T) {
	dir := t.TempDir()
	setupLSPEnv(t, 0)

	Init(Options{InitialFiles: nil})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	openPath := filepath.Join(dir, "main.typ")
	diskPath := filepath.Join(dir, "sub.typ")
	require.NoError(t, os.WriteFile(diskPath, []byte("// α line\r\n#let oldName() = [hi]\r\noldName()\r\n"), 0644))

	bMain1 := openTestBuffer(t, openPath, "#import \"sub.typ\": oldName\n#oldName()\n")
	bMain2, err := buffer.NewBufferFromFile(openPath, buffer.BTDefault)
	require.NoError(t, err)
	assert.True(t, bMain1.Shared())
	_ = bMain2
	action.InitTabs([]*buffer.Buffer{bMain1, bMain2})

	docChangeJSON, err := json.Marshal(TextDocumentEdit{
		TextDocument: OptionalVersionedTextDocumentIdentifier{URI: PathToURI(diskPath)},
		Edits: []TextEdit{
			{Range: Range{Start: Position{1, 5}, End: Position{1, 12}}, NewText: "newName"},
			{Range: Range{Start: Position{2, 0}, End: Position{2, 7}}, NewText: "newName"},
		},
	})
	require.NoError(t, err)

	files, applied, err := ApplyWorkspaceEdit(WorkspaceEdit{
		Changes: map[string][]TextEdit{
			PathToURI(openPath): {
				{Range: Range{Start: Position{0, 19}, End: Position{0, 26}}, NewText: "newName"},
				{Range: Range{Start: Position{1, 1}, End: Position{1, 8}}, NewText: "newName"},
			},
		},
		DocumentChanges: []json.RawMessage{docChangeJSON},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, files)
	assert.Equal(t, 4, applied)

	assert.Equal(t, "#import \"sub.typ\": newName\n#newName()\n", string(bMain1.Bytes()))
	assert.Equal(t, string(bMain1.Bytes()), string(bMain2.Bytes()))

	// Open buffer edits collapse into a single undo step.
	assert.True(t, bMain1.Undo())
	assert.Equal(t, "#import \"sub.typ\": oldName\n#oldName()\n", string(bMain1.Bytes()))

	// Unopened sibling file preserves CRLF line endings on disk.
	diskAfter, err := os.ReadFile(diskPath)
	require.NoError(t, err)
	assert.Equal(t, "// α line\r\n#let newName() = [hi]\r\nnewName()\r\n", string(diskAfter))
}

func TestApplyWorkspaceEditPreflightAtomicityAndOverlaps(t *testing.T) {
	dir := t.TempDir()
	setupLSPEnv(t, 0)

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	goodPath := filepath.Join(dir, "good.typ")
	pkgDir := filepath.Join(dir, "typst", "packages", "preview", "pkg", "0.1.0")
	require.NoError(t, os.MkdirAll(pkgDir, 0755))
	pkgFile := filepath.Join(pkgDir, "lib.typ")
	require.NoError(t, os.WriteFile(pkgFile, []byte("#let x = 1\n"), 0644))

	roPath := filepath.Join(dir, "ro.typ")
	bGood := openTestBuffer(t, goodPath, "old\n")
	bRO := openTestBuffer(t, roPath, "old\n")
	require.NoError(t, bRO.SetOptionNative("readonly", true))
	action.InitTabs([]*buffer.Buffer{bGood, bRO})

	// Package cache file in WorkspaceEdit must fail pre-flight without touching bGood.
	_, _, err := ApplyWorkspaceEdit(WorkspaceEdit{
		Changes: map[string][]TextEdit{
			PathToURI(goodPath): {{Range: Range{Start: Position{0, 0}, End: Position{0, 3}}, NewText: "new"}},
			PathToURI(pkgFile):  {{Range: Range{Start: Position{0, 5}, End: Position{0, 6}}, NewText: "y"}},
		},
	})
	require.Error(t, err)
	assert.Equal(t, "old\n", string(bGood.Bytes()))

	// Read-only open buffer in WorkspaceEdit must fail pre-flight without touching bGood.
	_, _, err = ApplyWorkspaceEdit(WorkspaceEdit{
		Changes: map[string][]TextEdit{
			PathToURI(goodPath): {{Range: Range{Start: Position{0, 0}, End: Position{0, 3}}, NewText: "new"}},
			PathToURI(roPath):   {{Range: Range{Start: Position{0, 0}, End: Position{0, 3}}, NewText: "new"}},
		},
	})
	require.Error(t, err)
	assert.Equal(t, "old\n", string(bGood.Bytes()))

	// Overlapping edits on an unopened disk file fail cleanly.
	diskPath := filepath.Join(dir, "overlap.typ")
	require.NoError(t, os.WriteFile(diskPath, []byte("abcdef\n"), 0644))
	_, _, err = ApplyWorkspaceEdit(WorkspaceEdit{
		Changes: map[string][]TextEdit{
			PathToURI(diskPath): {
				{Range: Range{Start: Position{0, 0}, End: Position{0, 4}}, NewText: "X"},
				{Range: Range{Start: Position{0, 2}, End: Position{0, 5}}, NewText: "Y"},
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overlapping")
	raw, readErr := os.ReadFile(diskPath)
	require.NoError(t, readErr)
	assert.Equal(t, "abcdef\n", string(raw))
}
