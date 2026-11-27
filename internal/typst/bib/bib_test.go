package bib

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/micro-editor/tcell/v2"
	lua "github.com/yuin/gopher-lua"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	ulua "github.com/monicafaye/TypX/internal/lua"
	"github.com/monicafaye/TypX/internal/screen"
)

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "typx_bib_test_*")
	if err != nil {
		panic(err)
	}

	ulua.L = lua.NewState()
	_ = config.InitConfigDir(tmpDir)
	config.InitRuntimeFiles(false)
	_ = config.InitGlobalSettings()
	config.GlobalSettings["backup"] = false
	config.GlobalSettings["fastdirty"] = true

	simScreen, err := screen.InitSimScreen()
	if err != nil {
		panic(err)
	}
	simScreen.SetSize(80, 24)
	simScreen.Show()

	Init()
	action.InitBindings()
	action.InitCommands()
	action.InitGlobals()
	buffer.SetMessager(action.InfoBar)
	_ = config.InitColorscheme()

	code := m.Run()
	SetPicker(nil)
	SetMainFileResolver(nil)
	SetRootResolver(nil)
	ClearCache()
	buffer.CloseOpenBuffers()
	buffer.FlushObservers()
	action.DrainUIQueue()
	_ = os.RemoveAll(tmpDir)
	os.Exit(code)
}

func setupBibPane(t *testing.T, name, content string) *action.BufPane {
	t.Helper()
	SetPicker(nil)
	SetMainFileResolver(nil)
	SetRootResolver(nil)
	ClearCache()
	buffer.CloseOpenBuffers()
	buffer.FlushObservers()
	action.DrainUIQueue()
	if action.InfoBar != nil {
		action.InfoBar.Reset()
	}

	b := buffer.NewBufferFromString(content, name, buffer.BTDefault)
	action.InitTabs([]*buffer.Buffer{b})
	action.Tabs.Resize()
	action.Tabs.SetActive(0)
	t.Cleanup(func() {
		SetPicker(nil)
		SetMainFileResolver(nil)
		SetRootResolver(nil)
		ClearCache()
		buffer.CloseOpenBuffers()
		buffer.FlushObservers()
		action.DrainUIQueue()
		if action.InfoBar != nil {
			action.InfoBar.Reset()
		}
	})
	return action.MainTab().CurPane()
}

func TestCitePickerCommandBindingInsertionAndNilPickerFallback(t *testing.T) {
	dir := t.TempDir()
	bibPath := filepath.Join(dir, "refs.bib")
	ymlPath := filepath.Join(dir, "extra.yml")

	if err := os.WriteFile(bibPath, []byte(`@article{vaswani2017, author={Vaswani, Ashish and Shazeer, Noam}, title={Attention Is All You Need}, year={2017}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ymlPath, []byte("einstein1905:\n  type: article\n  title: Special Relativity\n  author: Albert Einstein\n  date: 1905\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	docPath := filepath.Join(dir, "paper.typ")
	bp := setupBibPane(t, docPath, `#bibliography(("refs.bib", "extra.yml"))`+"\nSee \n")
	SetRootResolver(func(string) string { return dir })

	// 1. pickerFn == nil reports graceful InfoBar message without panicking.
	bp.Cursor.GotoLoc(buffer.Loc{X: 4, Y: 1})
	bp.HandleCommand("cite vas")
	if !strings.Contains(action.InfoBar.Msg, "2 bibliography entries") {
		t.Fatalf("expected graceful nil-picker message, got %q", action.InfoBar.Msg)
	}

	// 2. Wire picker and verify :cite [query] + insertion without leading '@'.
	var gotTitle, gotBadge, gotQuery string
	var gotItems []PickerItem
	SetPicker(func(pane *action.BufPane, title, badge, initialQuery string, items []PickerItem) bool {
		gotTitle = title
		gotBadge = badge
		gotQuery = initialQuery
		gotItems = items
		return true
	})

	bp.HandleCommand("cite einstein")
	if gotTitle != "Cite" || gotBadge != "CITE" || gotQuery != "einstein" || len(gotItems) != 2 {
		t.Fatalf("unexpected picker args: title=%q badge=%q query=%q items=%d", gotTitle, gotBadge, gotQuery, len(gotItems))
	}
	// Primary preserves co-authors so ListPane searches match secondary authors too.
	if !strings.Contains(gotItems[0].Primary, "Shazeer, Noam") || gotItems[0].Badge != "2017" || gotItems[0].Secondary != "@vaswani2017" {
		t.Fatalf("unexpected first PickerItem: %+v", gotItems[0])
	}

	// Selecting item when cursor does NOT follow '@' inserts '@key'.
	gotItems[0].OnSelect()
	if line := string(bp.Buf.LineBytes(1)); line != "See @vaswani2017" {
		t.Fatalf("expected 'See @vaswani2017', got %q", line)
	}

	// 3. Alt-r opens picker and selecting when cursor immediately follows '@' inserts only 'key'.
	bp.Buf.Insert(bp.Buf.End(), "@")
	bp.Cursor.GotoLoc(buffer.Loc{X: len("See @vaswani2017@"), Y: 1})
	gotItems = nil
	action.Tabs.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'r', tcell.ModAlt, ""))
	if len(gotItems) != 2 || gotQuery != "" {
		t.Fatalf("expected Alt-r to open picker with empty query and 2 items, got query=%q items=%d", gotQuery, len(gotItems))
	}
	gotItems[1].OnSelect()
	if line := string(bp.Buf.LineBytes(1)); line != "See @vaswani2017@einstein1905" {
		t.Fatalf("expected no double @@ when '@' precedes cursor, got %q", line)
	}

	// 4. CiteCompleter reads active document buffer rather than InfoBar buffer.
	infoBuf := buffer.NewBufferFromString("cite ein", "", buffer.BTDefault)
	infoBuf.GetActiveCursor().GotoLoc(buffer.Loc{X: len("cite ein"), Y: 0})
	comps, sugs := CiteCompleter(infoBuf)
	if !reflect.DeepEqual(comps, []string{"stein1905"}) || !reflect.DeepEqual(sugs, []string{"einstein1905"}) {
		t.Fatalf("CiteCompleter mismatch: comps=%#v sugs=%#v", comps, sugs)
	}

	// 5. Duplicate registration panics loudly as required by extension registry contract.
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		RegisterAll()
	}()
	if !panicked {
		t.Fatal("expected duplicate RegisterAll() call to panic")
	}
}

func TestSuggestEntriesAndCycleAutocomplete(t *testing.T) {
	dir := t.TempDir()
	bibPath := filepath.Join(dir, "refs.bib")
	bibContent := strings.Join([]string{
		`@article{vaswani2017, author={Vaswani}, title={Attention}, year={2017}}`,
		`@article{Vasquez-2021:main, author={Vasquez}, title={Case Folded}, year={2021}}`,
		`@article{vas-2020.intro, author={Vas}, title={Hyphenated Key}, year={2020}}`,
		`@article{other1999, author={Other}, title={Other}, year={1999}}`,
	}, "\n")
	if err := os.WriteFile(bibPath, []byte(bibContent), 0o644); err != nil {
		t.Fatal(err)
	}

	docPath := filepath.Join(dir, "notes.typ")
	bp := setupBibPane(t, docPath, "#bibliography(\"refs.bib\")\n@vas")
	SetRootResolver(func(string) string { return dir })

	// Exact-case prefix matches come first, sorted, followed by case-folded matches.
	comps, sugs := SuggestEntries(bp.Buf, "vas")
	wantSugs := []string{"vas-2020.intro", "vaswani2017", "Vasquez-2021:main"}
	wantComps := []string{"-2020.intro", "wani2017", "quez-2021:main"}
	if !reflect.DeepEqual(sugs, wantSugs) || !reflect.DeepEqual(comps, wantComps) {
		t.Fatalf("SuggestEntries mismatch:\nsugs:  %#v (want %#v)\ncomps: %#v (want %#v)", sugs, wantSugs, comps, wantComps)
	}

	// Integration with ShowSuggestions + CycleAutocomplete (bypassing GetWord breakage on '-' / ':').
	bp.Cursor.GotoLoc(buffer.Loc{X: 4, Y: 1}) // right after "@vas"
	if !bp.Buf.ShowSuggestions(func(b *buffer.Buffer) ([]string, []string) {
		return SuggestEntries(b, "vas")
	}) {
		t.Fatal("expected ShowSuggestions to return true")
	}
	bp.Buf.CycleAutocomplete(true)
	if got := string(bp.Buf.LineBytes(1)); got != "@vas-2020.intro" {
		t.Fatalf("after 1st Tab expected '@vas-2020.intro', got %q", got)
	}
	bp.Buf.CycleAutocomplete(true)
	if got := string(bp.Buf.LineBytes(1)); got != "@vaswani2017" {
		t.Fatalf("after 2nd Tab expected '@vaswani2017', got %q", got)
	}

	// Non-Typst buffer yields nil, nil.
	txtBuf := buffer.NewBufferFromString("@vas", "notes.txt", buffer.BTDefault)
	if c, s := SuggestEntries(txtBuf, "vas"); c != nil || s != nil {
		t.Fatalf("expected nil suggestions for non-Typst buffer, got %#v %#v", c, s)
	}
}
