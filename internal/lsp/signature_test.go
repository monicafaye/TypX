package lsp

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
)

func TestSignatureHelpExplicitAndAutoTrigger(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)
	config.GlobalSettings["typst.signatureHelp"] = true
	config.GlobalSettings["typst.autoComplete"] = false

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected StateReady")

	docPath := filepath.Join(dir, "sig.typ")
	b := openTestBuffer(t, docPath, "#table(")
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()
	bp.Cursor.GotoLoc(buffer.Loc{X: 7, Y: 0})

	// Explicit Alt+H / LspSignatureHelpAction displays active parameter 0
	assert.True(t, LspSignatureHelpAction()(bp))
	waitForCondition(t, 2*time.Second, func() bool {
		return action.InfoBar != nil && strings.Contains(action.InfoBar.Msg, "»columns: auto«")
	}, "expected signature help for first parameter")
	assert.Contains(t, action.InfoBar.Msg, "Column count")

	// Auto-trigger on ',' switches to second parameter
	b.Insert(bp.Cursor.Loc, "2,")
	buffer.FlushObservers()
	waitForCondition(t, 2*time.Second, func() bool {
		return action.InfoBar != nil && strings.Contains(action.InfoBar.Msg, "»body: content«")
	}, "expected auto signature help on ',' for second parameter")

	// Typing ')' clears the auto signature help message
	b.Insert(bp.Cursor.Loc, ")")
	buffer.FlushObservers()
	assert.Equal(t, "", action.InfoBar.Msg)
}

func TestAutoCompletionShowsNonMutatingSuggestions(t *testing.T) {
	dir, _ := setupLSPEnv(t, 0)
	config.GlobalSettings["typst.autoComplete"] = true
	config.GlobalSettings["typst.signatureHelp"] = false

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected StateReady")

	docPath := filepath.Join(dir, "auto.typ")
	b := openTestBuffer(t, docPath, "")
	action.InitTabs([]*buffer.Buffer{b})

	// Typing '#' triggers debounced ShowSuggestions without mutating buffer text
	b.Insert(buffer.Loc{X: 0, Y: 0}, "#")
	buffer.FlushObservers()

	waitForCondition(t, 2*time.Second, func() bool {
		return b.HasSuggestions && b.CurSuggestion == -1 && len(b.Suggestions) == 2
	}, "expected auto-completion to populate non-mutating suggestions")
	assert.Equal(t, "#", string(b.LineBytes(0)), "ShowSuggestions must not mutate buffer bytes before Tab")

	// Pressing Tab (CycleAutocomplete) accepts the first suggestion
	b.CycleAutocomplete(true)
	assert.Equal(t, "#alpha", string(b.LineBytes(0)))
}

func TestSingleSnippetCompletionInvokesSnippetExpander(t *testing.T) {
	t.Setenv("TYPX_MOCK_SNIPPET_COMPLETE", "1")
	dir, _ := setupLSPEnv(t, 0)
	config.GlobalSettings["typst.autoComplete"] = false

	var gotRaw string
	var gotStart, gotEnd buffer.Loc
	SetSnippetExpander(func(bp *action.BufPane, start, end buffer.Loc, rawSnippet string) bool {
		gotStart = start
		gotEnd = end
		gotRaw = rawSnippet
		bp.Buf.Replace(start, end, "table(columns: 2, body)")
		return true
	})
	t.Cleanup(func() { SetSnippetExpander(nil) })

	Init(Options{})
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()

	waitForCondition(t, 5*time.Second, func() bool {
		return defaultManager.State() == StateReady
	}, "expected StateReady")

	docPath := filepath.Join(dir, "snip.typ")
	b := openTestBuffer(t, docPath, "#tab")
	action.InitTabs([]*buffer.Buffer{b})
	bp := action.MainTab().CurPane()
	bp.Cursor.GotoLoc(buffer.Loc{X: 4, Y: 0})

	require.True(t, LspCompleteAction()(bp))
	waitForCondition(t, 2*time.Second, func() bool {
		return gotRaw != ""
	}, "expected single snippet completion to invoke SnippetExpander")
	assert.Equal(t, buffer.Loc{X: 1, Y: 0}, gotStart)
	assert.Equal(t, buffer.Loc{X: 4, Y: 0}, gotEnd)
	assert.Equal(t, "table(columns: ${1:2}, ${2:body})$0", gotRaw)
	assert.Equal(t, "#table(columns: 2, body)", string(b.LineBytes(0)))
}
