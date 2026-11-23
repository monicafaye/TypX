package shortcuts

import (
	"strings"
	"testing"

	"github.com/micro-editor/tcell/v2"
	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
)

func TestAllTemplatesExpandAndStripDollarZero(t *testing.T) {
	names := TemplateNames()
	expected := []string{"bar", "callout", "cases", "code", "figure", "frac", "hat", "matrix", "table", "vec"}
	if len(names) != len(expected) {
		t.Fatalf("TemplateNames() len = %d, want %d (%v)", len(names), len(expected), names)
	}
	for i := range expected {
		if names[i] != expected[i] {
			t.Fatalf("TemplateNames()[%d] = %q, want %q", i, names[i], expected[i])
		}
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			bp := newTestBufPane(t, "", "test.typ")
			NewInsertTemplateCmd(name)(bp, nil)
			txt := bufferText(bp.Buf)
			if txt == "" {
				t.Fatalf("template %q produced empty buffer", name)
			}
			if strings.Contains(txt, "$0") {
				t.Fatalf("template %q left literal $0 in output: %q", name, txt)
			}
			// Inserting 'X' at cursor position verifies $0 cursor placement is valid.
			bp.Buf.Insert(bp.Cursor.Loc, "X")
			if !strings.Contains(bufferText(bp.Buf), "X") {
				t.Fatalf("cursor placement invalid after %q", name)
			}
		})
	}
}

func TestTemplateIndentationAndUnicodeOffsets(t *testing.T) {
	// 1. Indented line with spaces (tabsize=4)
	bp := newTestBufPane(t, "    ", "test.typ")
	bp.Buf.Settings["tabsize"] = float64(4)
	bp.Cursor.GotoLoc(buffer.Loc{X: 4, Y: 0})
	NewInsertCmd()(bp, []string{"figure"})
	want := "    #figure(\n        ,\n        caption: [],\n    ) <fig:>"
	if got := bufferText(bp.Buf); got != want {
		t.Fatalf("space-indented figure = %q, want %q", got, want)
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 8, Y: 1}) {
		t.Fatalf("cursor = %v, want {8 1}", bp.Cursor.Loc)
	}

	// 2. Tab indentation (tabstospaces=false)
	bp = newTestBufPane(t, "\t", "test.typ")
	bp.Buf.Settings["tabstospaces"] = false
	bp.Cursor.GotoLoc(buffer.Loc{X: 1, Y: 0})
	NewInsertCmd()(bp, []string{"table"})
	wantTab := "\t#table(\n\t\tcolumns: 2,\n\t\t[], [],\n\t)"
	if got := bufferText(bp.Buf); got != wantTab {
		t.Fatalf("tab-indented table = %q, want %q", got, wantTab)
	}
	if bp.Cursor.Loc != (buffer.Loc{X: 3, Y: 2}) {
		t.Fatalf("tab cursor = %v, want {3 2}", bp.Cursor.Loc)
	}

	// 3. Multi-byte UTF-8 and non-BMP prefix before single-line $0 template
	bp = newTestBufPane(t, "𝕏😀 e\u0301 ", "test.typ")
	// chars: 0:'𝕏', 1:'😀', 2:' ', 3:'e\u0301', 4:' ' -> X=5
	bp.Cursor.GotoLoc(buffer.Loc{X: 5, Y: 0})
	NewInsertCmd()(bp, []string{"frac"})
	if got := bufferText(bp.Buf); got != "𝕏😀 e\u0301 frac(, )" {
		t.Fatalf("unicode frac = %q", got)
	}
	// "frac(" is 5 chars, so cursor is at 5 + 5 = 10
	if bp.Cursor.Loc != (buffer.Loc{X: 10, Y: 0}) {
		t.Fatalf("unicode frac cursor = %v, want {10 0}", bp.Cursor.Loc)
	}

	// 4. Unknown template and non-Typst buffer do not mutate buffer
	bp = newTestBufPane(t, "untouched", "test.typ")
	NewInsertCmd()(bp, []string{"nonexistent"})
	if bufferText(bp.Buf) != "untouched" {
		t.Fatalf("unknown template mutated buffer: %q", bufferText(bp.Buf))
	}
	bpTxt := newTestBufPane(t, "plain", "test.txt")
	NewInsertCmd()(bpTxt, []string{"frac"})
	if bufferText(bpTxt.Buf) != "plain" {
		t.Fatalf("non-Typst insert mutated buffer: %q", bufferText(bpTxt.Buf))
	}
}

func TestInsertCompleterAndAutocomplete(t *testing.T) {
	b := buffer.NewBufferFromString("insert f", "", buffer.BTDefault)
	t.Cleanup(buffer.CloseOpenBuffers)
	b.GetActiveCursor().GotoLoc(buffer.Loc{X: 8, Y: 0})

	completions, suggestions := InsertCompleter(b)
	if len(suggestions) != 2 || suggestions[0] != "figure" || suggestions[1] != "frac" {
		t.Fatalf("suggestions for 'f' = %v, want [figure frac]", suggestions)
	}
	if completions[0] != "igure" || completions[1] != "rac" {
		t.Fatalf("completions for 'f' = %v, want [igure rac]", completions)
	}

	if !b.Autocomplete(InsertCompleter) {
		t.Fatal("Buffer.Autocomplete(InsertCompleter) returned false")
	}
	if got := string(b.LineBytes(0)); got != "insert figure" {
		t.Fatalf("after 1st completion = %q, want %q", got, "insert figure")
	}
	b.CycleAutocomplete(true)
	if got := string(b.LineBytes(0)); got != "insert frac" {
		t.Fatalf("after 2nd cycle = %q, want %q", got, "insert frac")
	}

	// Empty prefix lists all 10 sorted templates
	b2 := buffer.NewBufferFromString("insert ", "", buffer.BTDefault)
	b2.GetActiveCursor().GotoLoc(buffer.Loc{X: 7, Y: 0})
	_, all := InsertCompleter(b2)
	if len(all) != 10 {
		t.Fatalf("empty prefix suggestions len = %d, want 10", len(all))
	}
}

func TestInsertPaletteActionInfoBar(t *testing.T) {
	bp := newTestBufPane(t, "", "test.typ")

	// Non-Typst returns false
	bpTxt := newTestBufPane(t, "", "test.txt")
	if NewInsertPaletteAction()(bpTxt) {
		t.Fatal("InsertPaletteAction should return false on non-Typst buffer")
	}

	bp = newTestBufPane(t, "", "test.typ")
	if !NewInsertPaletteAction()(bp) {
		t.Fatal("InsertPaletteAction returned false on Typst buffer")
	}
	if !action.InfoBar.HasPrompt {
		t.Fatal("InfoBar should have active prompt")
	}
	if action.InfoBar.PromptType != "Command" {
		t.Fatalf("PromptType = %q, want Command", action.InfoBar.PromptType)
	}
	if got := string(action.InfoBar.LineBytes(0)); got != "insert " {
		t.Fatalf("prefill = %q, want %q", got, "insert ")
	}

	// Cancel prompt leaves buffer empty
	action.InfoBar.AbortCommand()
	if action.InfoBar.HasPrompt {
		t.Fatal("InfoBar prompt should close after AbortCommand")
	}
	if bufferText(bp.Buf) != "" {
		t.Fatalf("canceled palette mutated buffer: %q", bufferText(bp.Buf))
	}

	// Re-open, type "frac" via tcell.EventKey runes, and execute
	if !NewInsertPaletteAction()(bp) {
		t.Fatal("InsertPaletteAction reopen failed")
	}
	for _, r := range "frac" {
		action.InfoBar.HandleEvent(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone, ""))
	}
	// Replace prompt line to bare "frac" or execute "insert frac":
	// Note: if RegisterAll hasn't run yet in this test, bare "frac" tests direct template lookup;
	// let's test direct template lookup by clearing "insert " and entering "frac"
	action.InfoBar.Buf.Replace(buffer.Loc{X: 0, Y: 0}, buffer.Loc{X: len("insert frac"), Y: 0}, "frac")
	action.InfoBar.ExecuteCommand()
	if got := bufferText(bp.Buf); got != "frac(, )" {
		t.Fatalf("palette submit 'frac' = %q, want %q", got, "frac(, )")
	}
}
