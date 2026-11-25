package views

import (
	"fmt"
	"testing"
)

func TestHSplit(t *testing.T) {
	root := NewRoot(0, 0, 80, 80)
	n1 := root.VSplit(true)
	root.GetNode(n1).VSplit(true)
	root.GetNode(root.id).ResizeSplit(7)
	root.Resize(120, 120)

	fmt.Println(root.String())
}

func TestDockLeftLoneLeafAndUnsplit(t *testing.T) {
	root := NewRoot(0, 0, 100, 30)
	origID := root.ID()

	dockID := root.DockLeft(30)
	if dockID == 0 {
		t.Fatal("DockLeft returned 0")
	}

	dock := root.GetNode(dockID)
	main := root.GetNode(origID)
	if dock == nil || main == nil {
		t.Fatalf("expected both dock and main leaves, got dock=%v main=%v", dock, main)
	}
	if dock.X != 0 || dock.W != 30 || dock.H != 30 {
		t.Fatalf("unexpected dock geometry: %+v", dock.View)
	}
	if main.X != 30 || main.W != 70 || main.H != 30 {
		t.Fatalf("unexpected main geometry: %+v", main.View)
	}
	if dock.CanResize() || dock.PropScale() {
		t.Fatalf("dock should have canResize=false and propScale=false")
	}

	// Terminal resize should preserve dock's fixed width.
	root.Resize(140, 40)
	if dock.W != 30 || dock.H != 40 || dock.X != 0 {
		t.Fatalf("after grow resize, dock = %+v, want X=0 W=30 H=40", dock.View)
	}
	if main.X != 30 || main.W != 110 || main.H != 40 {
		t.Fatalf("after grow resize, main = %+v, want X=30 W=110 H=40", main.View)
	}

	root.Resize(70, 24)
	if dock.W != 30 || dock.H != 24 || dock.X != 0 {
		t.Fatalf("after shrink resize, dock = %+v, want X=0 W=30 H=24", dock.View)
	}
	if main.X != 30 || main.W != 40 || main.H != 24 {
		t.Fatalf("after shrink resize, main = %+v, want X=30 W=40 H=24", main.View)
	}

	// Unsplit dock should collapse root back to a single leaf preserving origID.
	if !dock.Unsplit() {
		t.Fatal("dock.Unsplit() returned false")
	}
	if !root.IsLeaf() || root.ID() != origID {
		t.Fatalf("expected root to collapse to lone leaf %d, got leaf=%v id=%d", origID, root.IsLeaf(), root.ID())
	}
	if root.W != 70 || root.H != 24 {
		t.Fatalf("unexpected root size after unsplit: %+v", root.View)
	}
}

func TestDockLeftWithExistingVSplit(t *testing.T) {
	root := NewRoot(0, 0, 100, 30)
	leftID := root.ID()
	rightID := root.VSplit(true)

	dockID := root.DockLeft(20)
	if dockID == 0 {
		t.Fatal("DockLeft failed on STHoriz root")
	}

	dock := root.GetNode(dockID)
	left := root.GetNode(leftID)
	right := root.GetNode(rightID)
	if dock == nil || left == nil || right == nil {
		t.Fatalf("missing leaves after dock: dock=%v left=%v right=%v", dock, left, right)
	}
	if dock.X != 0 || dock.W != 20 {
		t.Fatalf("dock = %+v, want X=0 W=20", dock.View)
	}
	if left.X != 20 || left.W != 40 {
		t.Fatalf("left = %+v, want X=20 W=40", left.View)
	}
	if right.X != 60 || right.W != 40 {
		t.Fatalf("right = %+v, want X=60 W=40", right.View)
	}

	// Add another VSplit while docked; dock stays fixed at 20 cols on the far left.
	thirdID := right.VSplit(true)
	third := root.GetNode(thirdID)
	if dock.X != 0 || dock.W != 20 {
		t.Fatalf("after VSplit while docked, dock = %+v, want X=0 W=20", dock.View)
	}
	if left.W+right.W+third.W != 80 {
		t.Fatalf("editor splits width sum = %d, want 80", left.W+right.W+third.W)
	}

	// Unsplit dock restores full 100-col width across editor splits.
	if !dock.Unsplit() {
		t.Fatal("dock.Unsplit() returned false")
	}
	if left.X != 0 || left.W+right.W+third.W != 100 {
		t.Fatalf("after dock unsplit, left.X=%d sum=%d", left.X, left.W+right.W+third.W)
	}
}

func TestDockLeftWithExistingHSplit(t *testing.T) {
	root := NewRoot(0, 0, 100, 30)
	topID := root.ID()
	botID := root.HSplit(true)

	dockID := root.DockLeft(28)
	if dockID == 0 {
		t.Fatal("DockLeft failed on STVert root")
	}

	dock := root.GetNode(dockID)
	top := root.GetNode(topID)
	bot := root.GetNode(botID)
	if dock == nil || top == nil || bot == nil {
		t.Fatalf("missing leaves: dock=%v top=%v bot=%v", dock, top, bot)
	}
	if dock.X != 0 || dock.Y != 0 || dock.W != 28 || dock.H != 30 {
		t.Fatalf("dock = %+v, want (0,0,28,30)", dock.View)
	}
	if top.X != 28 || top.Y != 0 || top.W != 72 || top.H != 15 {
		t.Fatalf("top = %+v, want (28,0,72,15)", top.View)
	}
	if bot.X != 28 || bot.Y != 15 || bot.W != 72 || bot.H != 15 {
		t.Fatalf("bot = %+v, want (28,15,72,15)", bot.View)
	}

	// Resize terminal and verify dock stays 28 cols while top/bot fill remaining width.
	root.Resize(128, 40)
	if dock.W != 28 || dock.H != 40 {
		t.Fatalf("after resize, dock = %+v, want W=28 H=40", dock.View)
	}
	if top.X != 28 || top.W != 100 || top.H != 20 {
		t.Fatalf("after resize, top = %+v, want X=28 W=100 H=20", top.View)
	}
	if bot.X != 28 || bot.W != 100 || bot.H != 20 {
		t.Fatalf("after resize, bot = %+v, want X=28 W=100 H=20", bot.View)
	}

	// Unsplit dock should promote STVert back to root cleanly.
	if !dock.Unsplit() {
		t.Fatal("dock.Unsplit() returned false")
	}
	if root.Kind != STVert || len(root.Children()) != 2 {
		t.Fatalf("expected root promoted back to STVert with 2 children, got Kind=%d children=%d", root.Kind, len(root.Children()))
	}
	if top.X != 0 || top.W != 128 || bot.X != 0 || bot.W != 128 {
		t.Fatalf("after unsplit, top=%+v bot=%+v", top.View, bot.View)
	}
}

func TestDockLeftInvalidAndTinyResize(t *testing.T) {
	root := NewRoot(0, 0, 40, 20)
	if id := root.DockLeft(0); id != 0 {
		t.Fatalf("expected 0 for width=0, got %d", id)
	}
	if id := root.DockLeft(40); id != 0 {
		t.Fatalf("expected 0 for width>=root.W, got %d", id)
	}

	origID := root.ID()
	dockID := root.DockLeft(20)
	if dockID == 0 {
		t.Fatal("DockLeft(20) failed on 40-col root")
	}
	// Resize down below fixed width must clamp without panicking or negative widths.
	root.Resize(10, 5)
	dock := root.GetNode(dockID)
	main := root.GetNode(origID)
	if dock.W < 0 || main.W < 0 || dock.W+main.W != 10 {
		t.Fatalf("unexpected tiny resize widths: dock=%+v main=%+v", dock.View, main.View)
	}
	root.Resize(0, 0)
	if dock.W != 0 || main.W != 0 {
		t.Fatalf("expected zero widths on 0x0 resize, got dock=%+v main=%+v", dock.View, main.View)
	}
}

func TestDockRightWithDockLeftAndResize(t *testing.T) {
	root := NewRoot(0, 0, 160, 40)
	editorID := root.ID()

	// Dock fixed Outline on left (30 cols) and proportional Preview on right (76 cols).
	leftID := root.DockLeft(30)
	if leftID == 0 {
		t.Fatal("DockLeft(30) failed")
	}
	rightID := root.DockRight(76)
	if rightID == 0 {
		t.Fatal("DockRight(76) failed")
	}

	left := root.GetNode(leftID)
	editor := root.GetNode(editorID)
	right := root.GetNode(rightID)
	if left.W != 30 || editor.W != 54 || right.W != 76 {
		t.Fatalf("unexpected initial widths: left=%d editor=%d right=%d (want 30, 54, 76)", left.W, editor.W, right.W)
	}
	if left.X != 0 || editor.X != 30 || right.X != 84 {
		t.Fatalf("unexpected initial X offsets: left=%d editor=%d right=%d", left.X, editor.X, right.X)
	}

	// Resizing terminal wider should keep left fixed at 30 and scale editor+right proportionally.
	root.Resize(200, 50)
	if left.W != 30 {
		t.Fatalf("expected fixed left dock width 30 after resize, got %d", left.W)
	}
	if left.W+editor.W+right.W != 200 {
		t.Fatalf("total width mismatch after resize: %d+%d+%d != 200", left.W, editor.W, right.W)
	}
	if right.W <= 76 || editor.W <= 54 {
		t.Fatalf("expected editor and right dock to grow proportionally, got editor=%d right=%d", editor.W, right.W)
	}

	// Unsplit right dock cleanly.
	if !right.Unsplit() {
		t.Fatal("right.Unsplit() returned false")
	}
	if left.W != 30 || editor.W != 170 {
		t.Fatalf("after right unsplit, want left=30 editor=170, got left=%d editor=%d", left.W, editor.W)
	}
}

func TestDockRightOnVerticalSplit(t *testing.T) {
	root := NewRoot(0, 0, 120, 40)
	topID := root.ID()
	botID := root.HSplit(true)

	rightID := root.DockRight(50)
	if rightID == 0 {
		t.Fatal("DockRight(50) on STVert root failed")
	}
	top := root.GetNode(topID)
	bot := root.GetNode(botID)
	right := root.GetNode(rightID)
	if top.W != 70 || bot.W != 70 || right.W != 50 || right.X != 70 || right.H != 40 {
		t.Fatalf("unexpected STVert+DockRight layout: top=%+v bot=%+v right=%+v", top.View, bot.View, right.View)
	}
	if !right.Unsplit() {
		t.Fatal("right.Unsplit() returned false")
	}
	if root.Kind != STVert || top.W != 120 || bot.W != 120 {
		t.Fatalf("expected clean restore to STVert: kind=%d top=%+v bot=%+v", root.Kind, top.View, bot.View)
	}
}

func TestDockBottomLoneLeafAndUnsplit(t *testing.T) {
	root := NewRoot(0, 0, 120, 40)
	origID := root.ID()

	if id := root.DockBottom(0); id != 0 {
		t.Fatalf("expected 0 for height=0, got %d", id)
	}
	if id := root.DockBottom(40); id != 0 {
		t.Fatalf("expected 0 for height>=root.H, got %d", id)
	}

	dockID := root.DockBottom(10)
	if dockID == 0 {
		t.Fatal("DockBottom(10) returned 0")
	}

	dock := root.GetNode(dockID)
	main := root.GetNode(origID)
	if dock == nil || main == nil {
		t.Fatalf("missing leaves: dock=%v main=%v", dock, main)
	}
	if main.X != 0 || main.Y != 0 || main.W != 120 || main.H != 30 {
		t.Fatalf("unexpected main geometry: %+v", main.View)
	}
	if dock.X != 0 || dock.Y != 30 || dock.W != 120 || dock.H != 10 {
		t.Fatalf("unexpected dock geometry: %+v", dock.View)
	}
	if dock.CanResize() || dock.PropScale() {
		t.Fatal("bottom dock should have canResize=false and propScale=false")
	}

	// Terminal resize preserves fixed bottom height.
	root.Resize(140, 50)
	if dock.Y != 40 || dock.W != 140 || dock.H != 10 {
		t.Fatalf("after grow resize, dock = %+v, want X=0 Y=40 W=140 H=10", dock.View)
	}
	if main.Y != 0 || main.W != 140 || main.H != 40 {
		t.Fatalf("after grow resize, main = %+v, want X=0 Y=0 W=140 H=40", main.View)
	}

	// Tiny resize clamps cleanly without negative heights.
	root.Resize(60, 5)
	if dock.H < 0 || main.H < 0 || dock.H+main.H != 5 {
		t.Fatalf("unexpected tiny resize heights: main=%+v dock=%+v", main.View, dock.View)
	}

	if !dock.Unsplit() {
		t.Fatal("dock.Unsplit() returned false")
	}
	if !root.IsLeaf() || root.ID() != origID || root.W != 60 || root.H != 5 {
		t.Fatalf("unexpected root after unsplit: leaf=%v id=%d view=%+v", root.IsLeaf(), root.ID(), root.View)
	}
}

func TestDockBottomWithDockLeftAndDockRight(t *testing.T) {
	root := NewRoot(0, 0, 160, 40)
	editorID := root.ID()

	leftID := root.DockLeft(30)
	rightID := root.DockRight(60)
	botID := root.DockBottom(10)
	if leftID == 0 || rightID == 0 || botID == 0 {
		t.Fatalf("docking failed: left=%d right=%d bot=%d", leftID, rightID, botID)
	}

	left := root.GetNode(leftID)
	editor := root.GetNode(editorID)
	right := root.GetNode(rightID)
	bot := root.GetNode(botID)

	if left.W != 30 || left.H != 30 || left.Y != 0 {
		t.Fatalf("unexpected left geometry: %+v", left.View)
	}
	if editor.X != 30 || editor.W != 70 || editor.H != 30 || editor.Y != 0 {
		t.Fatalf("unexpected editor geometry: %+v", editor.View)
	}
	if right.X != 100 || right.W != 60 || right.H != 30 || right.Y != 0 {
		t.Fatalf("unexpected right geometry: %+v", right.View)
	}
	if bot.X != 0 || bot.Y != 30 || bot.W != 160 || bot.H != 10 {
		t.Fatalf("unexpected bottom geometry: %+v", bot.View)
	}

	// Resize keeps left=30, bot.H=10, and scales editor+right proportionally.
	root.Resize(200, 50)
	if left.W != 30 || left.H != 40 || bot.H != 10 || bot.Y != 40 || bot.W != 200 {
		t.Fatalf("unexpected geometry after resize: left=%+v editor=%+v right=%+v bot=%+v",
			left.View, editor.View, right.View, bot.View)
	}
	if left.W+editor.W+right.W != 200 {
		t.Fatalf("top row width sum mismatch: %d", left.W+editor.W+right.W)
	}

	// Unsplit bottom dock promotes STHoriz [left, editor, right] cleanly to root.
	if !bot.Unsplit() {
		t.Fatal("bot.Unsplit() returned false")
	}
	if root.Kind != STHoriz || len(root.Children()) != 3 {
		t.Fatalf("expected root restored to STHoriz with 3 children, got Kind=%d children=%d", root.Kind, len(root.Children()))
	}
	if left.H != 50 || editor.H != 50 || right.H != 50 {
		t.Fatalf("expected restored height 50 across top panes, got left=%d editor=%d right=%d", left.H, editor.H, right.H)
	}
}

func TestDockBottomWithExistingHSplit(t *testing.T) {
	root := NewRoot(0, 0, 100, 40)
	topID := root.ID()
	midID := root.HSplit(true)

	botID := root.DockBottom(8)
	if botID == 0 {
		t.Fatal("DockBottom(8) on STVert root failed")
	}
	top := root.GetNode(topID)
	mid := root.GetNode(midID)
	bot := root.GetNode(botID)

	if bot.Y != 32 || bot.H != 8 || bot.W != 100 {
		t.Fatalf("unexpected bot geometry: %+v", bot.View)
	}
	if top.H+mid.H != 32 || top.W != 100 || mid.W != 100 {
		t.Fatalf("unexpected stacked split geometry: top=%+v mid=%+v", top.View, mid.View)
	}

	if !bot.Unsplit() {
		t.Fatal("bot.Unsplit() returned false")
	}
	if top.H+mid.H != 40 {
		t.Fatalf("expected top+mid height 40 after unsplit, got %d+%d", top.H, mid.H)
	}
}
