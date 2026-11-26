package lsp

import (
	"encoding/json"
	"runtime"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUTF16Conversions(t *testing.T) {
	tests := []struct {
		name     string
		line     []byte
		utf16Col int
		wantRune int
	}{
		{name: "empty", line: []byte(""), utf16Col: 0, wantRune: 0},
		{name: "empty_past_eol", line: []byte(""), utf16Col: 5, wantRune: 0},
		{name: "negative", line: []byte("abc"), utf16Col: -3, wantRune: 0},
		{name: "ascii", line: []byte("hello"), utf16Col: 3, wantRune: 3},
		{name: "bmp_2byte_3byte", line: []byte("aéα→∑中b"), utf16Col: 4, wantRune: 4},
		{name: "bmp_end", line: []byte("aéα→∑中b"), utf16Col: 7, wantRune: 7},
		{name: "surrogate_before", line: []byte("a😀b"), utf16Col: 1, wantRune: 1},
		{name: "surrogate_mid_clamp", line: []byte("a😀b"), utf16Col: 2, wantRune: 1},
		{name: "surrogate_after", line: []byte("a😀b"), utf16Col: 3, wantRune: 2},
		{name: "surrogate_end", line: []byte("a😀b"), utf16Col: 4, wantRune: 3},
		{name: "surrogate_past_eol", line: []byte("a😀b"), utf16Col: 99, wantRune: 3},
		{name: "math_non_bmp", line: []byte("$𝕏 + α$"), utf16Col: 3, wantRune: 2},
		{name: "invalid_utf8", line: []byte{0x61, 0xff, 0xe2, 0x82, 0x62}, utf16Col: 4, wantRune: 4},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := UTF16ToRuneCol(tc.line, tc.utf16Col)
			assert.Equal(t, tc.wantRune, got)
		})
	}
}

func TestRuneColToUTF16AndRoundTrip(t *testing.T) {
	assert.Equal(t, 0, RuneColToUTF16([]byte("a😀b"), -1))
	assert.Equal(t, 0, RuneColToUTF16([]byte("a😀b"), 0))
	assert.Equal(t, 1, RuneColToUTF16([]byte("a😀b"), 1))
	assert.Equal(t, 3, RuneColToUTF16([]byte("a😀b"), 2))
	assert.Equal(t, 4, RuneColToUTF16([]byte("a😀b"), 3))
	assert.Equal(t, 4, RuneColToUTF16([]byte("a😀b"), 100))

	lines := [][]byte{
		[]byte(""),
		[]byte("plain ascii"),
		[]byte("αβγ → ∑"),
		[]byte("😀𝕏🎉aé中文b"),
		{0x61, 0xff, 0x62, 0xf0, 0x9f, 0x98, 0x80, 0x63},
	}
	for _, line := range lines {
		runeCount := utf8.RuneCount(line)
		for r := 0; r <= runeCount; r++ {
			u := RuneColToUTF16(line, r)
			back := UTF16ToRuneCol(line, u)
			assert.Equal(t, r, back, "round-trip failed for line %q at rune %d", line, r)
		}
	}
}

func TestPathToURIAndURIToPath(t *testing.T) {
	assert.Equal(t, "", PathToURI(""))
	assert.Equal(t, "", uriToPath("", true))

	assert.Equal(t, "file:///C:/foo%20bar/a%231.typ", PathToURI(`C:\foo bar\a#1.typ`))
	assert.Equal(t, "file:///C:/foo/a.typ", PathToURI(`c:\foo\a.typ`))
	assert.Equal(t, "file://server/share/my%20doc.typ", PathToURI(`\\server\share\my doc.typ`))
	assert.Equal(t, "file:///tmp/my%20doc.typ", PathToURI("/tmp/my doc.typ"))

	// Windows decoding (tested deterministically regardless of host OS)
	assert.Equal(t, `C:\foo bar\a#1.typ`, uriToPath("file:///c%3A/foo%20bar/a%231.typ", true))
	assert.Equal(t, `C:\foo\a.typ`, uriToPath("file://localhost/C:/foo/a.typ", true))
	assert.Equal(t, `\\server\share\doc.typ`, uriToPath("file://server/share/doc.typ", true))

	// POSIX decoding
	assert.Equal(t, "/tmp/my doc.typ", uriToPath("file:///tmp/my%20doc.typ", false))
	assert.Equal(t, "//server/share/doc.typ", uriToPath("file://server/share/doc.typ", false))

	if runtime.GOOS == "windows" {
		assert.Equal(t, `C:\foo\a.typ`, URIToPath("file:///c%3A/foo/a.typ"))
	} else {
		assert.Equal(t, "/tmp/my doc.typ", URIToPath("file:///tmp/my%20doc.typ"))
	}
}

func TestParseCompletionResultAndExtractHoverText(t *testing.T) {
	items, err := ParseCompletionResult(json.RawMessage(`null`))
	require.NoError(t, err)
	assert.Nil(t, items)

	items, err = ParseCompletionResult(json.RawMessage(`[{"label":"alpha","insertText":"alpha"}]`))
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "alpha", items[0].Label)

	items, err = ParseCompletionResult(json.RawMessage(`{"isIncomplete":true,"items":[{"label":"beta"}]}`))
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "beta", items[0].Label)

	assert.Equal(t, "", ExtractHoverText(json.RawMessage(`null`)))
	assert.Equal(t, "plain doc", ExtractHoverText(json.RawMessage(`"plain doc"`)))
	assert.Equal(t, "let x = 1", ExtractHoverText(json.RawMessage(`{"kind":"markdown","value":"let x = 1"}`)))
	assert.Equal(t, "fn foo()", ExtractHoverText(json.RawMessage(`{"language":"typst","value":"fn foo()"}`)))
	assert.Equal(t, "sig\n\ndoc", ExtractHoverText(json.RawMessage(`[{"language":"typst","value":"sig"},"doc"]`)))
}

func TestDefaultClientCapabilitiesEnablesSnippetsSignatureHelpAndUTF16(t *testing.T) {
	caps := DefaultClientCapabilities()
	require.NotNil(t, caps.Workspace)
	assert.True(t, caps.Workspace.ApplyEdit)
	require.NotNil(t, caps.Workspace.WorkspaceEdit)
	assert.True(t, caps.Workspace.WorkspaceEdit.DocumentChanges)
	require.NotNil(t, caps.TextDocument)
	require.NotNil(t, caps.TextDocument.Completion)
	require.NotNil(t, caps.TextDocument.Completion.CompletionItem)
	assert.True(t, caps.TextDocument.Completion.CompletionItem.SnippetSupport)
	require.NotNil(t, caps.TextDocument.SignatureHelp)
	require.NotNil(t, caps.TextDocument.SignatureHelp.SignatureInformation)
	require.NotNil(t, caps.TextDocument.SignatureHelp.SignatureInformation.ParameterInformation)
	assert.True(t, caps.TextDocument.SignatureHelp.SignatureInformation.ParameterInformation.LabelOffsetSupport)
	assert.True(t, caps.TextDocument.SignatureHelp.SignatureInformation.ActiveParameterSupport)
	require.NotNil(t, caps.TextDocument.References)
	require.NotNil(t, caps.TextDocument.Rename)
	assert.True(t, caps.TextDocument.Rename.PrepareSupport)
	require.NotNil(t, caps.TextDocument.CodeAction)
	require.NotNil(t, caps.TextDocument.CodeAction.CodeActionLiteralSupport)
	assert.Contains(t, caps.TextDocument.CodeAction.CodeActionLiteralSupport.CodeActionKind.ValueSet, "quickfix")
	require.NotNil(t, caps.General)
	assert.Equal(t, []string{"utf-16"}, caps.General.PositionEncodings)
}

func TestParseAndFormatSignatureHelp(t *testing.T) {
	sh, err := ParseSignatureHelp(json.RawMessage(`null`))
	require.NoError(t, err)
	assert.Nil(t, sh)

	sh, err = ParseSignatureHelp(json.RawMessage(`{"signatures":[]}`))
	require.NoError(t, err)
	assert.Nil(t, sh)

	// Substring parameter label + activeParameter + parameter doc
	raw := `{
		"activeSignature": 0,
		"activeParameter": 1,
		"signatures": [{
			"label": "table(columns: auto, rows: auto)",
			"parameters": [
				{"label": "columns: auto", "documentation": "Number of columns"},
				{"label": "rows: auto", "documentation": {"kind": "markdown", "value": "Number of rows"}}
			]
		}]
	}`
	sh, err = ParseSignatureHelp(json.RawMessage(raw))
	require.NoError(t, err)
	require.NotNil(t, sh)
	assert.Equal(t, "table(columns: auto, »rows: auto«) | Number of rows", FormatSignatureHelp(sh))

	// UTF-16 offset span label + multi-overload prefix
	raw2 := `{
		"activeSignature": 1,
		"signatures": [
			{"label": "f(a)"},
			{
				"label": "f(α, beta)",
				"activeParameter": 1,
				"parameters": [
					{"label": [2, 3]},
					{"label": [5, 9]}
				]
			}
		]
	}`
	sh2, err := ParseSignatureHelp(json.RawMessage(raw2))
	require.NoError(t, err)
	assert.Equal(t, "(2/2) f(α, »beta«)", FormatSignatureHelp(sh2))

	// ExtractTriggerChars merges triggerCharacters and retriggerCharacters cleanly
	got := ExtractTriggerChars(map[string]any{
		"triggerCharacters":   []any{"(", ","},
		"retriggerCharacters": []any{",", " "},
	}, true)
	assert.Equal(t, []string{"(", ",", " "}, got)
	assert.Nil(t, ExtractTriggerChars(true, true))
}

func TestParseDocumentSymbols(t *testing.T) {
	syms, err := ParseDocumentSymbols(json.RawMessage(`null`))
	require.NoError(t, err)
	assert.Nil(t, syms)

	syms, err = ParseDocumentSymbols(json.RawMessage(`[]`))
	require.NoError(t, err)
	assert.Nil(t, syms)

	hier := `[{
		"name": "Introduction",
		"kind": 3,
		"range": {"start":{"line":0,"character":0},"end":{"line":10,"character":0}},
		"selectionRange": {"start":{"line":0,"character":2},"end":{"line":0,"character":14}},
		"children": [{
			"name": "Background",
			"kind": 3,
			"range": {"start":{"line":3,"character":0},"end":{"line":6,"character":0}},
			"selectionRange": {"start":{"line":3,"character":3},"end":{"line":3,"character":13}}
		}]
	}]`
	syms, err = ParseDocumentSymbols(json.RawMessage(hier))
	require.NoError(t, err)
	require.Len(t, syms, 1)
	assert.Equal(t, "Introduction", syms[0].Name)
	assert.Equal(t, 0, syms[0].SelectionRange.Start.Line)
	require.Len(t, syms[0].Children, 1)
	assert.Equal(t, "Background", syms[0].Children[0].Name)
	assert.Equal(t, 3, syms[0].Children[0].SelectionRange.Start.Line)

	flat := `[{
		"name": "Methods",
		"kind": 2,
		"location": {
			"uri": "file:///paper.typ",
			"range": {"start":{"line":12,"character":0},"end":{"line":12,"character":10}}
		}
	}]`
	syms, err = ParseDocumentSymbols(json.RawMessage(flat))
	require.NoError(t, err)
	require.Len(t, syms, 1)
	assert.Equal(t, "Methods", syms[0].Name)
	assert.Equal(t, 12, syms[0].SelectionRange.Start.Line)
}

func TestParseLocations(t *testing.T) {
	locs, err := ParseLocations(json.RawMessage(`null`))
	require.NoError(t, err)
	assert.Nil(t, locs)

	locs, err = ParseLocations(json.RawMessage(`[]`))
	require.NoError(t, err)
	assert.Nil(t, locs)

	single := `{"uri":"file:///a.typ","range":{"start":{"line":2,"character":4},"end":{"line":2,"character":9}}}`
	locs, err = ParseLocations(json.RawMessage(single))
	require.NoError(t, err)
	require.Len(t, locs, 1)
	assert.Equal(t, "file:///a.typ", locs[0].URI)
	assert.Equal(t, 2, locs[0].Range.Start.Line)
	assert.Equal(t, 4, locs[0].Range.Start.Character)

	multi := `[{"uri":"file:///a.typ","range":{"start":{"line":1,"character":0},"end":{"line":1,"character":3}}},{"uri":"file:///refs.bib","range":{"start":{"line":10,"character":7},"end":{"line":10,"character":15}}}]`
	locs, err = ParseLocations(json.RawMessage(multi))
	require.NoError(t, err)
	require.Len(t, locs, 2)
	assert.Equal(t, "file:///refs.bib", locs[1].URI)
	assert.Equal(t, 10, locs[1].Range.Start.Line)

	links := `[{"targetUri":"file:///lib.typ","targetRange":{"start":{"line":5,"character":0},"end":{"line":8,"character":1}},"targetSelectionRange":{"start":{"line":5,"character":5},"end":{"line":5,"character":12}}}]`
	locs, err = ParseLocations(json.RawMessage(links))
	require.NoError(t, err)
	require.Len(t, locs, 1)
	assert.Equal(t, "file:///lib.typ", locs[0].URI)
	assert.Equal(t, 5, locs[0].Range.Start.Line)
	assert.Equal(t, 5, locs[0].Range.Start.Character)
}

func TestParsePrepareRename(t *testing.T) {
	ph, r, ok, err := ParsePrepareRename(json.RawMessage(`null`))
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, r)
	assert.Equal(t, "", ph)

	ph, r, ok, err = ParsePrepareRename(json.RawMessage(`{"defaultBehavior":true}`))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Nil(t, r)
	assert.Equal(t, "", ph)

	ph, r, ok, err = ParsePrepareRename(json.RawMessage(`{"range":{"start":{"line":1,"character":2},"end":{"line":1,"character":7}},"placeholder":"greet"}`))
	require.NoError(t, err)
	assert.True(t, ok)
	require.NotNil(t, r)
	assert.Equal(t, "greet", ph)
	assert.Equal(t, 2, r.Start.Character)

	ph, r, ok, err = ParsePrepareRename(json.RawMessage(`{"start":{"line":0,"character":1},"end":{"line":0,"character":4}}`))
	require.NoError(t, err)
	assert.True(t, ok)
	require.NotNil(t, r)
	assert.Equal(t, "", ph)
	assert.Equal(t, 4, r.End.Character)
}

func TestWorkspaceEditFileEdits(t *testing.T) {
	// Resource operation in documentChanges must return explicit error.
	_, _, err := WorkspaceEditFileEdits(WorkspaceEdit{
		DocumentChanges: []json.RawMessage{
			json.RawMessage(`{"kind":"rename","oldUri":"file:///a.typ","newUri":"file:///b.typ"}`),
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported resource operation")

	// Merging Changes and DocumentChanges produces sorted deterministic URI list.
	we := WorkspaceEdit{
		Changes: map[string][]TextEdit{
			"file:///z.typ": {{Range: Range{Start: Position{0, 0}, End: Position{0, 1}}, NewText: "Z"}},
			"file:///a.typ": {{Range: Range{Start: Position{0, 0}, End: Position{0, 1}}, NewText: "ignored-dup"}},
		},
		DocumentChanges: []json.RawMessage{
			json.RawMessage(`{"textDocument":{"uri":"file:///a.typ","version":1},"edits":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":2}},"newText":"A"}]}`),
		},
	}
	byURI, uris, err := WorkspaceEditFileEdits(we)
	require.NoError(t, err)
	assert.Equal(t, []string{"file:///a.typ", "file:///z.typ"}, uris)
	require.Len(t, byURI["file:///a.typ"], 1)
	assert.Equal(t, "A", byURI["file:///a.typ"][0].NewText)
	require.Len(t, byURI["file:///z.typ"], 1)
}

func TestParseCodeActionsAndWorkspaceSymbols(t *testing.T) {
	actions, err := ParseCodeActions(json.RawMessage(`null`))
	require.NoError(t, err)
	assert.Nil(t, actions)

	mixed := `[
		{"title":"Fix import","kind":"quickfix","isPreferred":true,"edit":{"changes":{"file:///a.typ":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}},"newText":"X"}]}}},
		{"title":"Disabled fix","kind":"quickfix","disabled":{"reason":"stale"}},
		{"title":"Run command","command":"tinymist.doSomething","arguments":["arg1"]}
	]`
	actions, err = ParseCodeActions(json.RawMessage(mixed))
	require.NoError(t, err)
	require.Len(t, actions, 2)
	assert.Equal(t, "Fix import", actions[0].Title)
	assert.True(t, actions[0].IsPreferred)
	require.NotNil(t, actions[0].Edit)
	assert.Equal(t, "Run command", actions[1].Title)
	require.NotNil(t, actions[1].Command)
	assert.Equal(t, "tinymist.doSomething", actions[1].Command.Command)

	syms, err := ParseWorkspaceSymbols(json.RawMessage(`null`))
	require.NoError(t, err)
	assert.Nil(t, syms)

	syms, err = ParseWorkspaceSymbols(json.RawMessage(`[
		{"name":"Introduction","kind":3,"containerName":"main.typ","location":{"uri":"file:///main.typ","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":14}}}},
		{"name":"Helper","kind":12,"location":{"uri":"file:///lib.typ"}}
	]`))
	require.NoError(t, err)
	require.Len(t, syms, 2)
	assert.Equal(t, "Introduction", syms[0].Name)
	assert.Equal(t, "Helper", syms[1].Name)
	assert.Equal(t, 0, syms[1].Location.Range.Start.Line)
}
