package lsp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultClientCapabilitiesIncludesM4bFeatures(t *testing.T) {
	caps := DefaultClientCapabilities()
	require.NotNil(t, caps.TextDocument)
	require.NotNil(t, caps.TextDocument.DocumentHighlight)
	assert.False(t, caps.TextDocument.DocumentHighlight.DynamicRegistration)
	require.NotNil(t, caps.TextDocument.SelectionRange)
	assert.False(t, caps.TextDocument.SelectionRange.DynamicRegistration)

	raw, err := json.Marshal(caps)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"documentHighlight"`)
	assert.Contains(t, string(raw), `"selectionRange"`)
}

func TestSupportsProvider(t *testing.T) {
	bTrue := true
	bFalse := false
	assert.False(t, SupportsProvider(nil))
	assert.False(t, SupportsProvider(false))
	assert.False(t, SupportsProvider(&bFalse))
	assert.False(t, SupportsProvider("true"))
	assert.False(t, SupportsProvider(map[string]any(nil)))

	assert.True(t, SupportsProvider(true))
	assert.True(t, SupportsProvider(&bTrue))
	assert.True(t, SupportsProvider(map[string]any{}))
	assert.True(t, SupportsProvider(map[string]any{"workDoneProgress": true}))
}

func TestServerCapabilitiesUnmarshalsM4bProviders(t *testing.T) {
	raw := `{"documentHighlightProvider":{"workDoneProgress":true},"selectionRangeProvider":true}`
	var caps ServerCapabilities
	require.NoError(t, json.Unmarshal([]byte(raw), &caps))
	assert.True(t, SupportsProvider(caps.DocumentHighlightProvider))
	assert.True(t, SupportsProvider(caps.SelectionRangeProvider))
}

func TestParseDocumentHighlights(t *testing.T) {
	hs, err := ParseDocumentHighlights(nil)
	require.NoError(t, err)
	assert.Nil(t, hs)

	hs, err = ParseDocumentHighlights(json.RawMessage(" null \n"))
	require.NoError(t, err)
	assert.Nil(t, hs)

	hs, err = ParseDocumentHighlights(json.RawMessage("[]"))
	require.NoError(t, err)
	assert.Nil(t, hs)

	raw := `[
		{"range":{"start":{"line":0,"character":5},"end":{"line":0,"character":10}},"kind":3},
		{"range":{"start":{"line":2,"character":1},"end":{"line":2,"character":6}},"kind":2},
		{"range":{"start":{"line":4,"character":0},"end":{"line":4,"character":5}},"kind":1}
	]`
	hs, err = ParseDocumentHighlights(json.RawMessage(raw))
	require.NoError(t, err)
	require.Len(t, hs, 3)
	assert.Equal(t, DocumentHighlightKindWrite, hs[0].Kind)
	assert.Equal(t, DocumentHighlightKindRead, hs[1].Kind)
	assert.Equal(t, DocumentHighlightKindText, hs[2].Kind)
	assert.Equal(t, Position{Line: 2, Character: 1}, hs[1].Range.Start)
	assert.Equal(t, Position{Line: 2, Character: 6}, hs[1].Range.End)

	_, err = ParseDocumentHighlights(json.RawMessage(`{"bad":true}`))
	assert.Error(t, err)
}

func TestParseSelectionRanges(t *testing.T) {
	srs, err := ParseSelectionRanges(nil)
	require.NoError(t, err)
	assert.Nil(t, srs)

	srs, err = ParseSelectionRanges(json.RawMessage("null"))
	require.NoError(t, err)
	assert.Nil(t, srs)

	srs, err = ParseSelectionRanges(json.RawMessage("[]"))
	require.NoError(t, err)
	assert.Nil(t, srs)

	raw := `[{
		"range":{"start":{"line":1,"character":5},"end":{"line":1,"character":9}},
		"parent":{
			"range":{"start":{"line":1,"character":0},"end":{"line":1,"character":12}},
			"parent":{"range":{"start":{"line":0,"character":0},"end":{"line":3,"character":0}}}
		}
	}]`
	srs, err = ParseSelectionRanges(json.RawMessage(raw))
	require.NoError(t, err)
	require.Len(t, srs, 1)
	assert.Equal(t, Position{Line: 1, Character: 5}, srs[0].Range.Start)
	require.NotNil(t, srs[0].Parent)
	assert.Equal(t, Position{Line: 1, Character: 0}, srs[0].Parent.Range.Start)
	require.NotNil(t, srs[0].Parent.Parent)
	assert.Equal(t, Position{Line: 3, Character: 0}, srs[0].Parent.Parent.Range.End)
	assert.Nil(t, srs[0].Parent.Parent.Parent)

	_, err = ParseSelectionRanges(json.RawMessage(`"invalid"`))
	assert.Error(t, err)
}
