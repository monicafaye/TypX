package shortcuts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
)

func setupCustomSnippetDirs(t *testing.T) (userDir, projRoot string) {
	t.Helper()
	origCfg := config.ConfigDir
	userDir = t.TempDir()
	projRoot = t.TempDir()
	config.ConfigDir = userDir
	SetRootResolver(func(p string) string {
		if p == "" {
			return ""
		}
		return projRoot
	})
	SetPicker(nil)
	if action.InfoBar != nil {
		if action.InfoBar.HasPrompt {
			action.InfoBar.AbortCommand()
		}
		action.InfoBar.Msg = ""
		action.InfoBar.HasError = false
		action.InfoBar.HasMessage = false
	}
	t.Cleanup(func() {
		config.ConfigDir = origCfg
		SetRootResolver(nil)
		SetPicker(nil)
		if action.InfoBar != nil {
			if action.InfoBar.HasPrompt {
				action.InfoBar.AbortCommand()
			}
			action.InfoBar.Msg = ""
			action.InfoBar.HasError = false
			action.InfoBar.HasMessage = false
		}
	})
	return userDir, projRoot
}

func TestCustomSnippetsThreeLayerPrecedenceAndFormats(t *testing.T) {
	userDir, projRoot := setupCustomSnippetDirs(t)

	userJSON := `{
  "thm": {"body": "#theorem[$1]$0", "description": "Theorem block"},
  "frac": "($1)/($2)$0",
  "useronly": "#note[$1]$0"
}`
	if err := os.WriteFile(filepath.Join(userDir, "snippets.json"), []byte(userJSON), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(projRoot, ".typx"), 0755); err != nil {
		t.Fatal(err)
	}
	// Project layer overrides "thm" with a plain string (clearing user's Description)
	// and adds "proj" as an object with description.
	projJSON := `{
  "thm": "#theorem(title: [$1])[$2]$0",
  "proj": {"body": "#proj(\n  $1,\n)$0", "description": "Project macro"}
}`
	if err := os.WriteFile(filepath.Join(projRoot, ".typx", "snippets.json"), []byte(projJSON), 0644); err != nil {
		t.Fatal(err)
	}

	docPath := filepath.Join(projRoot, "main.typ")
	bp := newTestBufPane(t, "", docPath)

	entries := loadSnippetsForBuffer(bp.Buf, true)
	byName := make(map[string]SnippetEntry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}

	if got := byName["table"]; got.Source != "built-in" {
		t.Fatalf("table source = %q, want built-in", got.Source)
	}
	if got := byName["frac"]; got.Source != "user" || got.Body != "($1)/($2)$0" {
		t.Fatalf("frac override = %+v", got)
	}
	if got := byName["useronly"]; got.Source != "user" || got.Body != "#note[$1]$0" {
		t.Fatalf("useronly = %+v", got)
	}
	if got := byName["thm"]; got.Source != "project" || got.Body != "#theorem(title: [$1])[$2]$0" || got.Description != "" {
		t.Fatalf("thm project string override = %+v (stale description should be cleared)", got)
	}
	if got := byName["proj"]; got.Source != "project" || got.Description != "Project macro" {
		t.Fatalf("proj = %+v", got)
	}

	// :insert and :insert-<name> expand merged overrides
	NewInsertCmd()(bp, []string{"thm"})
	if got := bufferText(bp.Buf); got != "#theorem(title: [])[]" {
		t.Fatalf(":insert thm = %q", got)
	}
	bp2 := newTestBufPane(t, "", docPath)
	NewInsertTemplateCmd("frac")(bp2, nil)
	if got := bufferText(bp2.Buf); got != "()/()" {
		t.Fatalf(":insert-frac user override = %q", got)
	}

	// InsertCompleter works on direct buffer and on InfoBar.Buf (Path == "") via MainTab().CurPane().Buf
	cmdBuf := buffer.NewBufferFromString("insert p", "", buffer.BTDefault)
	cmdBuf.GetActiveCursor().GotoLoc(buffer.Loc{X: 8, Y: 0})
	comp, sugg := InsertCompleter(cmdBuf)
	if len(sugg) != 1 || sugg[0] != "proj" || comp[0] != "roj" {
		t.Fatalf("InsertCompleter via MainTab fallback got comp=%v sugg=%v", comp, sugg)
	}

	infoBuf := buffer.NewBufferFromString("insert th", "", buffer.BTInfo)
	infoBuf.GetActiveCursor().GotoLoc(buffer.Loc{X: 9, Y: 0})
	comp, sugg = InsertCompleter(infoBuf)
	if len(sugg) != 1 || sugg[0] != "thm" || comp[0] != "m" {
		t.Fatalf("InsertCompleter with BTInfo buffer got comp=%v sugg=%v", comp, sugg)
	}

	// TemplateNames / TemplateBody stay backward-compatible with built-ins only
	if len(TemplateNames()) != 10 {
		t.Fatalf("TemplateNames() len = %d, want 10", len(TemplateNames()))
	}
	if body, ok := TemplateBody("frac"); !ok || body != "frac($1, $2)$0" {
		t.Fatalf("TemplateBody(frac) = (%q, %v)", body, ok)
	}
}

func TestCustomSnippetsEmptyRootAndUnnamedBufferSilent(t *testing.T) {
	_, _ = setupCustomSnippetDirs(t)

	// Create a .typx/snippets.json in a temp cwd to verify unnamed buffer does NOT read cwd.
	cwdDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdDir, ".typx"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwdDir, ".typx", "snippets.json"), []byte(`{"cwdleak": "leak"}`), 0644); err != nil {
		t.Fatal(err)
	}
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	SetRootResolver(func(p string) string {
		if p == "" {
			return cwdDir
		}
		return filepath.Dir(p)
	})

	bpUnnamed := newTestBufPane(t, "", "")
	if _, ok := lookupSnippetForBuffer(bpUnnamed.Buf, "cwdleak", true); ok {
		t.Fatal("unnamed buffer unexpectedly loaded .typx/snippets.json from cwd")
	}

	// Empty root resolver & nil root resolver stay silent
	SetRootResolver(func(string) string { return "" })
	bp := newTestBufPane(t, "", "main.typ")
	entries := loadSnippetsForBuffer(bp.Buf, true)
	if len(entries) != 10 {
		t.Fatalf("expected 10 built-ins with empty root, got %d", len(entries))
	}
	if action.InfoBar.HasError {
		t.Fatalf("unexpected InfoBar error: %q", action.InfoBar.Msg)
	}

	SetRootResolver(nil)
	entries = loadSnippetsForBuffer(bp.Buf, true)
	if len(entries) != 10 || action.InfoBar.HasError {
		t.Fatalf("expected 10 built-ins and no error with nil root resolver, got %d err=%v", len(entries), action.InfoBar.Msg)
	}
}

func TestCustomSnippetsMalformedJSONWarningsNonFatal(t *testing.T) {
	userDir, projRoot := setupCustomSnippetDirs(t)

	// 1. Malformed syntax in user snippets.json warns without blocking built-ins or valid project snippets
	if err := os.WriteFile(filepath.Join(userDir, "snippets.json"), []byte(`{not valid json`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projRoot, ".typx"), 0755); err != nil {
		t.Fatal(err)
	}
	projValid := `{"ok": "hello $1$0"}`
	if err := os.WriteFile(filepath.Join(projRoot, ".typx", "snippets.json"), []byte(projValid), 0644); err != nil {
		t.Fatal(err)
	}

	bp := newTestBufPane(t, "", filepath.Join(projRoot, "doc.typ"))
	entries := loadSnippetsForBuffer(bp.Buf, true)
	if !action.InfoBar.HasError || !strings.Contains(action.InfoBar.Msg, "invalid snippets JSON") {
		t.Fatalf("expected non-fatal InfoBar warning on malformed JSON, got HasError=%v Msg=%q", action.InfoBar.HasError, action.InfoBar.Msg)
	}
	if len(entries) != 11 {
		t.Fatalf("expected 10 built-ins + 1 project snippet despite malformed user JSON, got %d", len(entries))
	}

	// 2. Top-level JSON null warns and preserves built-ins
	action.InfoBar.HasError = false
	action.InfoBar.Msg = ""
	if err := os.WriteFile(filepath.Join(userDir, "snippets.json"), []byte(`null`), 0644); err != nil {
		t.Fatal(err)
	}
	entries = loadSnippetsForBuffer(bp.Buf, true)
	if !action.InfoBar.HasError {
		t.Fatal("expected warning for top-level JSON null")
	}
	if len(entries) != 11 {
		t.Fatalf("expected 11 snippets on null user file, got %d", len(entries))
	}

	// 3. Invalid per-entry values (null, missing body, body: null, bad description, empty key)
	// warn while preserving lower-layer entries and valid sibling entries.
	action.InfoBar.HasError = false
	action.InfoBar.Msg = ""
	mixedUser := `{
  "frac": null,
  "missing_body": {"description": "oops"},
  "null_body": {"body": null, "description": "oops"},
  "bad_desc": {"body": "x", "description": 123},
  "   ": "empty key",
  "good": {"body": "good($1)$0", "description": "Valid entry"}
}`
	if err := os.WriteFile(filepath.Join(userDir, "snippets.json"), []byte(mixedUser), 0644); err != nil {
		t.Fatal(err)
	}
	entries = loadSnippetsForBuffer(bp.Buf, true)
	if !action.InfoBar.HasError {
		t.Fatal("expected warning for invalid entries in mixedUser")
	}
	byName := make(map[string]SnippetEntry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}
	if frac, ok := byName["frac"]; !ok || frac.Source != "built-in" || frac.Body != "frac($1, $2)$0" {
		t.Fatalf("invalid user 'frac: null' should not clobber built-in frac, got %+v", frac)
	}
	if good, ok := byName["good"]; !ok || good.Body != "good($1)$0" {
		t.Fatalf("valid sibling 'good' missing: %+v", good)
	}
	for _, bad := range []string{"missing_body", "null_body", "bad_desc", "   "} {
		if _, ok := byName[bad]; ok {
			t.Fatalf("invalid entry %q should not be present", bad)
		}
	}
}

func TestInsertPalettePickerAndFallback(t *testing.T) {
	userDir, projRoot := setupCustomSnippetDirs(t)

	if err := os.MkdirAll(filepath.Join(projRoot, ".typx"), 0755); err != nil {
		t.Fatal(err)
	}
	userJSON := `{"multi": "first\n\tsecond  $1 $2$0"}`
	if err := os.WriteFile(filepath.Join(userDir, "snippets.json"), []byte(userJSON), 0644); err != nil {
		t.Fatal(err)
	}
	projJSON := `{"longdesc": {"body": "x($1, $2)$0", "description": "This is a very long snippet description exceeding forty four runes"}}`
	if err := os.WriteFile(filepath.Join(projRoot, ".typx", "snippets.json"), []byte(projJSON), 0644); err != nil {
		t.Fatal(err)
	}

	bp := newTestBufPane(t, "", filepath.Join(projRoot, "main.typ"))

	// 1. With picker installed, Alt+I and bare :insert open bottom picker with badges & single-line previews
	var gotTitle, gotBadge, gotQuery string
	var gotItems []PickerItem
	SetPicker(func(p *action.BufPane, title, badge, initialQuery string, items []PickerItem) bool {
		gotTitle = title
		gotBadge = badge
		gotQuery = initialQuery
		gotItems = items
		return true
	})

	if !NewInsertPaletteAction()(bp) {
		t.Fatal("NewInsertPaletteAction returned false")
	}
	if gotTitle != "Snippets" || gotBadge != "SNIPPETS" || gotQuery != "" {
		t.Fatalf("picker metadata = (%q, %q, %q)", gotTitle, gotBadge, gotQuery)
	}
	itemByName := make(map[string]PickerItem, len(gotItems))
	for _, it := range gotItems {
		itemByName[it.Primary] = it
		if strings.ContainsAny(it.Secondary, "\n\r\t") {
			t.Fatalf("item %q Secondary contains control whitespace: %q", it.Primary, it.Secondary)
		}
	}
	if itemByName["multi"].Badge != "user" || itemByName["multi"].Secondary != "first second $1 $2$0" {
		t.Fatalf("multi picker item = %+v", itemByName["multi"])
	}
	if itemByName["longdesc"].Badge != "project" || len([]rune(itemByName["longdesc"].Secondary)) > 44 {
		t.Fatalf("longdesc picker item = %+v", itemByName["longdesc"])
	}

	// Selecting a multi-stop snippet expands and activates Tab navigation
	itemByName["longdesc"].OnSelect()
	if got := bufferText(bp.Buf); got != "x(, )" {
		t.Fatalf("after OnSelect buffer = %q, want %q", got, "x(, )")
	}
	if !HasActiveSnippetSession(bp.Buf) {
		t.Fatal("expected active snippet tabstop session after OnSelect")
	}
	if !NewSnippetNextAction()(bp) {
		t.Fatal("SnippetNext returned false")
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 4, Y: 0}) {
		t.Fatalf("cursor after Tab stop = %v, want {4 0}", bp.Cursor.Loc)
	}

	// Bare :insert also opens picker and supports multi-cursor expansion
	gotItems = nil
	bpMC := newTestBufPane(t, "a \nb ", filepath.Join(projRoot, "main.typ"))
	bpMC.Buf.GetActiveCursor().GotoLoc(buffer.Loc{X: 2, Y: 0})
	c1 := buffer.NewCursor(bpMC.Buf, buffer.Loc{X: 2, Y: 1})
	c1.Num = 1
	bpMC.Buf.AddCursor(c1)
	NewInsertCmd()(bpMC, nil)
	if len(gotItems) == 0 {
		t.Fatal("bare :insert did not invoke picker")
	}
	for _, it := range gotItems {
		if it.Primary == "longdesc" {
			it.OnSelect()
			break
		}
	}
	if got := bufferText(bpMC.Buf); got != "a x(, )\nb x(, )" {
		t.Fatalf("multi-cursor OnSelect = %q, want %q", got, "a x(, )\nb x(, )")
	}

	// 2. Non-Typst / readonly returns false
	bpReadonly := newTestBufPane(t, "", filepath.Join(projRoot, "main.typ"))
	bpReadonly.Buf.Type.Readonly = true
	if NewInsertPaletteAction()(bpReadonly) {
		t.Fatal("NewInsertPaletteAction should return false on readonly buffer")
	}

	// 3. With picker == nil, InfoBar fallback works for custom snippets too
	SetPicker(nil)
	bpFallback := newTestBufPane(t, "", filepath.Join(projRoot, "main.typ"))
	if !NewInsertPaletteAction()(bpFallback) || !action.InfoBar.HasPrompt {
		t.Fatal("expected InfoBar prompt fallback when picker == nil")
	}
	action.InfoBar.Buf.Replace(buffer.Loc{X: 0, Y: 0}, buffer.Loc{X: len("insert "), Y: 0}, "longdesc")
	action.InfoBar.ExecuteCommand()
	if got := bufferText(bpFallback.Buf); got != "x(, )" {
		t.Fatalf("InfoBar fallback custom snippet = %q, want %q", got, "x(, )")
	}
	// Avoid unused tcell import if not needed; verify cancel via KeyEscape
	if !NewInsertPaletteAction()(bpFallback) {
		t.Fatal("reopen fallback failed")
	}
	action.InfoBar.AbortCommand()
	if action.InfoBar.HasPrompt {
		t.Fatal("InfoBar prompt should close on AbortCommand")
	}
}
