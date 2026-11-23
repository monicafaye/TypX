package display

import (
	"math"
	"sort"
	"strings"

	runewidth "github.com/mattn/go-runewidth"
	"github.com/micro-editor/tcell/v2"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/screen"
)

// BottomBarItem represents a single shortcut hint rendered in the bottom bar
// or expanded key menu (Alt+G).
type BottomBarItem struct {
	Key        string // Keycap text, e.g. "^S", "Alt+P", "Enter"
	Label      string // Short description, e.g. "Save", "Preview", "Jump"
	Action     string // Click target: BufKeyAction name, "command:<cmd>", "pane:<key>", or "dismiss"
	Priority   int    // Lower value = higher priority (survives longer on narrow terminals)
	Active     bool   // True when the associated feature/toggle is currently active
	Accent     string // Optional emphasis: "", "err", "warn", "ok"
	AlignRight bool   // Pin to the right side of the idle bottom bar when room permits
}

// BottomBarMenuRow describes one row in the expanded key menu (Alt+G).
type BottomBarMenuRow struct {
	Title string          // Optional short left label (e.g. "Editor", "Typst")
	Items []BottomBarItem // Shortcut items rendered on this row
}

// BottomBarContext supplies context-aware content for the idle bottom bar
// and the expanded 2-row key menu.
type BottomBarContext struct {
	Badge       string             // Optional left context pill (e.g. "MATH", "OUTLINE", "PREVIEW")
	BadgeAccent string             // Optional badge style key: "", "math", "code", "raw", "markup", "err", "warn"
	Hints       []BottomBarItem    // Single-line idle bottom bar hints
	MenuRows    []BottomBarMenuRow // Expanded 2-row key menu (Alt+G)
	MenuTip     string             // Tip shown on the bottom row when keymenu is open and idle
}

// BottomBarHit describes a clickable screen region rendered by the bottom bar
// or expanded key menu.
type BottomBarHit struct {
	Y      int
	XStart int // inclusive
	XEnd   int // exclusive
	Action string
}

const keyMenuRowCount = 2

var bottomBarProvider func() BottomBarContext

// SetBottomBarProvider registers a provider supplying context-aware bottom bar
// hints and expanded key menu rows. Passing nil restores the built-in default.
// Call during initialization on the main goroutine.
func SetBottomBarProvider(fn func() BottomBarContext) {
	bottomBarProvider = fn
}

func currentBottomBarContext() BottomBarContext {
	if bottomBarProvider != nil {
		ctx := bottomBarProvider()
		if len(ctx.Hints) > 0 || len(ctx.MenuRows) > 0 {
			return ctx
		}
	}
	return defaultBottomBarContext()
}

func defaultBottomBarContext() BottomBarContext {
	return BottomBarContext{
		Hints: []BottomBarItem{
			{Key: "^S", Label: "Save", Action: "Save", Priority: 0},
			{Key: "^O", Label: "Open", Action: "OpenFile", Priority: 3},
			{Key: "^F", Label: "Find", Action: "Find", Priority: 2},
			{Key: "^E", Label: "Cmd", Action: "CommandMode", Priority: 1},
			{Key: "^Z", Label: "Undo", Action: "Undo", Priority: 4},
			{Key: "Alt+G", Label: "Keys", Action: "ToggleKeyMenu", Priority: 0, AlignRight: true},
			{Key: "^G", Label: "Help", Action: "ToggleHelp", Priority: 3, AlignRight: true},
			{Key: "^Q", Label: "Quit", Action: "Quit", Priority: 1, AlignRight: true},
		},
		MenuRows: []BottomBarMenuRow{
			{
				Title: "File",
				Items: []BottomBarItem{
					{Key: "^S", Label: "Save", Action: "Save", Priority: 0},
					{Key: "^O", Label: "Open", Action: "OpenFile", Priority: 1},
					{Key: "^F", Label: "Find", Action: "Find", Priority: 1},
					{Key: "^E", Label: "Command", Action: "CommandMode", Priority: 0},
					{Key: "^T", Label: "New Tab", Action: "AddTab", Priority: 3},
					{Key: "^G", Label: "Help", Action: "ToggleHelp", Priority: 2},
					{Key: "^Q", Label: "Quit", Action: "Quit", Priority: 0},
				},
			},
			{
				Title: "Edit",
				Items: []BottomBarItem{
					{Key: "^Z", Label: "Undo", Action: "Undo", Priority: 0},
					{Key: "^Y", Label: "Redo", Action: "Redo", Priority: 1},
					{Key: "^C", Label: "Copy", Action: "Copy", Priority: 2},
					{Key: "^V", Label: "Paste", Action: "Paste", Priority: 2},
					{Key: "^K", Label: "Cut Line", Action: "CutLine", Priority: 3},
					{Key: "^D", Label: "Duplicate", Action: "DuplicateLine", Priority: 3},
					{Key: "^A", Label: "Select All", Action: "SelectAll", Priority: 4},
				},
			},
		},
		MenuTip: "Click any shortcut or press Alt+G to hide · ^E for commands",
	}
}

func keyHintsEnabled() bool {
	v, ok := config.GetGlobalOption("typst.keyHints").(bool)
	if !ok {
		return true
	}
	return v
}

// bottomBarTheme holds contrast-safe styles derived from the active colorscheme.
type bottomBarTheme struct {
	bar        tcell.Style
	dim        tcell.Style
	key        tcell.Style
	activeKey  tcell.Style
	activeDot  tcell.Style
	errKey     tcell.Style
	warnKey    tcell.Style
	prompt     tcell.Style
	gutterMark tcell.Style
}

func resolveBottomBarTheme() bottomBarTheme {
	bar := config.DefStyle
	if s, ok := config.Colorscheme["bottombar"]; ok {
		bar = s
	} else if s, ok := config.Colorscheme["message"]; ok {
		bar = s
	}
	barFg, barBg, _ := bar.Decompose()

	dim := bar
	if s, ok := config.Colorscheme["bottombar.dim"]; ok {
		dim = s
	} else if s, ok := config.Colorscheme["comment"]; ok {
		fg, _, _ := s.Decompose()
		if isReadableContrast(fg, barBg, 2.2) {
			dim = bar.Foreground(fg)
		}
	}

	// Subtle elevated background for keycaps when colorscheme doesn't specify bottombar.key.
	key := bar.Bold(true).Reverse(true)
	if s, ok := config.Colorscheme["bottombar.key"]; ok {
		key = s
	} else {
		keyBg := barBg
		for _, bgGroup := range []string{"cursor-line", "line-number", "statusline.inactive"} {
			if s, ok := config.Colorscheme[bgGroup]; ok {
				_, bg, _ := s.Decompose()
				if bg != tcell.ColorDefault && bg != barBg {
					keyBg = bg
					break
				}
			}
		}
		picked := false
		for _, fgGroup := range []string{"identifier", "type", "special"} {
			if s, ok := config.Colorscheme[fgGroup]; ok {
				fg, _, _ := s.Decompose()
				if isReadableContrast(fg, keyBg, 3.2) {
					key = config.DefStyle.Foreground(fg).Background(keyBg).Bold(true)
					picked = true
					break
				}
			}
		}
		if !picked && keyBg != barBg && isReadableContrast(barFg, keyBg, 3.0) {
			key = config.DefStyle.Foreground(barFg).Background(keyBg).Bold(true)
		}
	}

	activeKey := key
	if s, ok := config.Colorscheme["bottombar.active"]; ok {
		activeKey = s
	} else {
		_, keyBg, _ := key.Decompose()
		for _, fgGroup := range []string{"conceal", "special", "diff-added", "constant.bool.true"} {
			if s, ok := config.Colorscheme[fgGroup]; ok {
				fg, _, _ := s.Decompose()
				if isReadableContrast(fg, keyBg, 3.2) {
					activeKey = config.DefStyle.Foreground(fg).Background(keyBg).Bold(true)
					break
				}
			}
		}
	}

	activeDot := bar.Bold(true)
	if s, ok := config.Colorscheme["bottombar.active"]; ok {
		fg, bg, _ := s.Decompose()
		// Use active badge bg as the dot foreground on barBg when readable, else fg.
		if isReadableContrast(bg, barBg, 3.0) {
			activeDot = bar.Foreground(bg).Bold(true)
		} else if isReadableContrast(fg, barBg, 3.0) {
			activeDot = bar.Foreground(fg).Bold(true)
		}
	} else {
		for _, fgGroup := range []string{"conceal", "constant.bool.true", "diff-added"} {
			if s, ok := config.Colorscheme[fgGroup]; ok {
				fg, _, _ := s.Decompose()
				if isReadableContrast(fg, barBg, 3.0) {
					activeDot = bar.Foreground(fg).Bold(true)
					break
				}
			}
		}
	}

	errKey := config.DefStyle.Foreground(tcell.ColorBlack).Background(tcell.ColorMaroon).Bold(true)
	if s, ok := config.Colorscheme["bottombar.err"]; ok {
		errKey = s
	} else if s, ok := config.Colorscheme["error-message"]; ok {
		errKey = s.Bold(true)
	}

	warnKey := bar.Bold(true).Reverse(true)
	if s, ok := config.Colorscheme["bottombar.warn"]; ok {
		warnKey = s
	} else if s, ok := config.Colorscheme["hlsearch"]; ok {
		warnKey = s.Bold(true)
	}

	prompt := bar.Bold(true)
	if fg, _, _ := key.Decompose(); isReadableContrast(fg, barBg, 3.0) {
		prompt = bar.Foreground(fg).Bold(true)
	}

	gutterMark := bar.Bold(true)
	for _, grp := range []string{"gutter-warning", "gutter-error", "special"} {
		if s, ok := config.Colorscheme[grp]; ok {
			fg, _, _ := s.Decompose()
			if isReadableContrast(fg, barBg, 3.0) {
				gutterMark = bar.Foreground(fg).Bold(true)
				break
			}
		}
	}

	return bottomBarTheme{
		bar:        bar,
		dim:        dim,
		key:        key,
		activeKey:  activeKey,
		activeDot:  activeDot,
		errKey:     errKey,
		warnKey:    warnKey,
		prompt:     prompt,
		gutterMark: gutterMark,
	}
}

func (t bottomBarTheme) badgeStyle(accent string) tcell.Style {
	if accent != "" {
		if s, ok := config.Colorscheme["bottombar.badge."+accent]; ok {
			return s
		}
		switch accent {
		case "err":
			return t.errKey
		case "warn":
			return t.warnKey
		}
	}
	if s, ok := config.Colorscheme["bottombar.badge"]; ok {
		return s
	}
	if s, ok := config.Colorscheme["tabbar.active"]; ok {
		return s.Bold(true)
	}
	if s, ok := config.Colorscheme["statusline"]; ok {
		return s.Bold(true)
	}
	return t.bar.Bold(true).Reverse(true)
}

func (t bottomBarTheme) itemKeyStyle(it BottomBarItem) tcell.Style {
	switch it.Accent {
	case "err":
		return t.errKey
	case "warn":
		return t.warnKey
	}
	if it.Active {
		return t.activeKey
	}
	return t.key
}

// isReadableContrast returns true when two RGB colors meet at least minRatio
// WCAG relative-luminance contrast. Returns false if either color has no RGB
// decomposition (e.g. ColorDefault) so callers fall back safely.
func isReadableContrast(fg, bg tcell.Color, minRatio float64) bool {
	r1, g1, b1 := fg.RGB()
	r2, g2, b2 := bg.RGB()
	if r1 < 0 || r2 < 0 {
		return false
	}
	l1 := relLuminance(r1, g1, b1)
	l2 := relLuminance(r2, g2, b2)
	hi, lo := l1, l2
	if lo > hi {
		hi, lo = lo, hi
	}
	ratio := (hi + 0.05) / (lo + 0.05)
	return ratio >= minRatio
}

func relLuminance(r, g, b int32) float64 {
	return 0.2126*srgbToLinear(r) + 0.7152*srgbToLinear(g) + 0.0722*srgbToLinear(b)
}

func srgbToLinear(c int32) float64 {
	v := float64(c) / 255.0
	if v <= 0.03928 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func measureBottomBarItem(it BottomBarItem) int {
	w := runewidth.StringWidth(it.Key)
	if it.Accent == "err" || it.Accent == "warn" {
		w += 2 // 1-space padding on each side of alert pill
	}
	if it.Label != "" {
		w += 1 + runewidth.StringWidth(it.Label)
	}
	if it.Active {
		w += 1 // trailing '●' indicator
	}
	return w
}

// selectItemsByPriority picks the highest-priority subset of items that fits
// within maxWidth (with gap spaces between items) and returns them in their
// original slice order so visual positions stay stable across resizes.
func selectItemsByPriority(items []BottomBarItem, maxWidth, gap int) []BottomBarItem {
	if len(items) == 0 || maxWidth <= 0 {
		return nil
	}
	type indexed struct {
		idx   int
		width int
		prio  int
	}
	order := make([]indexed, len(items))
	for i, it := range items {
		order[i] = indexed{idx: i, width: measureBottomBarItem(it), prio: it.Priority}
	}
	sort.SliceStable(order, func(a, b int) bool {
		if order[a].prio != order[b].prio {
			return order[a].prio < order[b].prio
		}
		return order[a].idx < order[b].idx
	})

	chosen := make([]bool, len(items))
	used := 0
	count := 0
	for _, o := range order {
		add := o.width
		if count > 0 {
			add += gap
		}
		if used+add <= maxWidth {
			chosen[o.idx] = true
			used += add
			count++
		}
	}
	if count == 0 {
		return nil
	}
	out := make([]BottomBarItem, 0, count)
	for i, it := range items {
		if chosen[i] {
			out = append(out, it)
		}
	}
	return out
}

func totalItemsWidth(items []BottomBarItem, gap int) int {
	if len(items) == 0 {
		return 0
	}
	w := 0
	for i, it := range items {
		if i > 0 {
			w += gap
		}
		w += measureBottomBarItem(it)
	}
	return w
}

func drawStyledText(x, y, maxX int, text string, style tcell.Style) int {
	for _, r := range text {
		rw := runewidth.RuneWidth(r)
		if rw < 1 {
			rw = 1
		}
		if x+rw > maxX {
			break
		}
		screen.SetContent(x, y, r, nil, style)
		for j := 1; j < rw; j++ {
			screen.SetContent(x+j, y, ' ', nil, style)
		}
		x += rw
	}
	return x
}

func (i *InfoWindow) fillRow(y int, style tcell.Style) {
	if y < 0 {
		return
	}
	for x := 0; x < i.Width; x++ {
		screen.SetContent(x, y, ' ', nil, style)
	}
}

func (i *InfoWindow) drawBottomBarItem(x, y, maxX int, it BottomBarItem, theme bottomBarTheme) int {
	startX := x
	keyStyle := theme.itemKeyStyle(it)
	keyText := it.Key
	if it.Accent == "err" || it.Accent == "warn" {
		keyText = " " + keyText + " "
	}
	x = drawStyledText(x, y, maxX, keyText, keyStyle)
	if it.Label != "" && x+1 < maxX {
		screen.SetContent(x, y, ' ', nil, theme.bar)
		x++
		labelStyle := theme.bar
		if it.Active {
			labelStyle = theme.bar.Bold(true)
		}
		x = drawStyledText(x, y, maxX, it.Label, labelStyle)
	}
	if it.Active && x < maxX {
		screen.SetContent(x, y, '●', nil, theme.activeDot)
		x++
	}
	if it.Action != "" && x > startX {
		i.hits = append(i.hits, BottomBarHit{
			Y:      y,
			XStart: startX,
			XEnd:   x,
			Action: it.Action,
		})
	}
	return x
}

func (i *InfoWindow) drawIdleBottomBar(ctx BottomBarContext, keyMenuOpen bool) {
	theme := resolveBottomBarTheme()
	y := i.Y
	i.fillRow(y, theme.bar)
	if i.Width <= 0 {
		return
	}

	if keyMenuOpen {
		tip := ctx.MenuTip
		if tip == "" {
			tip = "Click any shortcut or press Alt+G to hide"
		}
		x := 1
		if i.Width >= 24 {
			x = drawStyledText(x, y, i.Width-1, "KEYS", theme.badgeStyle(""))
			x++
		}
		// Render compact right-aligned Hide button when there's room.
		hideItem := BottomBarItem{Key: "Alt+G", Label: "Hide", Action: "ToggleKeyMenu"}
		hideW := measureBottomBarItem(hideItem)
		tipLimit := i.Width - 1
		if i.Width-x-hideW-3 > 8 {
			tipLimit = i.Width - hideW - 2
			i.drawBottomBarItem(i.Width-hideW-1, y, i.Width, hideItem, theme)
		}
		drawStyledText(x, y, tipLimit, tip, theme.dim)
		return
	}

	const gap = 2
	x := 1
	if ctx.Badge != "" && i.Width >= 24 {
		pill := " " + ctx.Badge + " "
		x = drawStyledText(x, y, i.Width-1, pill, theme.badgeStyle(ctx.BadgeAccent))
		x++ // 1 space after badge
	}

	var leftAll, rightAll []BottomBarItem
	for _, it := range ctx.Hints {
		if it.AlignRight {
			rightAll = append(rightAll, it)
		} else {
			leftAll = append(leftAll, it)
		}
	}

	// Reserve space for right-aligned hints (at most 38% of bar width so left hints always dominate).
	maxRightW := (i.Width - x - 2) * 38 / 100
	if maxRightW < 0 {
		maxRightW = 0
	}
	rightItems := selectItemsByPriority(rightAll, maxRightW, gap)
	rightW := totalItemsWidth(rightItems, gap)

	availLeft := i.Width - x - 1
	if rightW > 0 {
		availLeft = i.Width - x - rightW - gap - 1
	}
	leftItems := selectItemsByPriority(leftAll, availLeft, gap)

	// Draw left items.
	curX := x
	leftLimit := i.Width - 1
	if rightW > 0 {
		leftLimit = i.Width - rightW - 2
	}
	for idx, it := range leftItems {
		if idx > 0 {
			curX += gap
		}
		curX = i.drawBottomBarItem(curX, y, leftLimit, it, theme)
	}

	// Draw right-aligned items.
	if rightW > 0 {
		rx := i.Width - rightW - 1
		if rx > curX+1 {
			for idx, it := range rightItems {
				if idx > 0 {
					rx += gap
				}
				rx = i.drawBottomBarItem(rx, y, i.Width, it, theme)
			}
		}
	}
}

func (i *InfoWindow) drawExpandedKeyMenu(ctx BottomBarContext) {
	theme := resolveBottomBarTheme()
	rows := ctx.MenuRows
	if len(rows) == 0 {
		rows = defaultBottomBarContext().MenuRows
	}

	for r := 0; r < keyMenuRowCount; r++ {
		y := i.Y - keyMenuRowCount + r
		if y < 0 {
			continue
		}
		i.fillRow(y, theme.bar)
		if r >= len(rows) {
			continue
		}
		row := rows[r]
		x := 1
		if row.Title != "" && i.Width >= 72 {
			title := row.Title
			if runewidth.StringWidth(title) < 6 {
				title += strings.Repeat(" ", 6-runewidth.StringWidth(title))
			}
			x = drawStyledText(x, y, i.Width-1, title, theme.dim.Bold(true))
			x += 2
		}
		const gap = 2
		avail := i.Width - x - 1
		items := selectItemsByPriority(row.Items, avail, gap)
		for idx, it := range items {
			if idx > 0 {
				x += gap
			}
			x = i.drawBottomBarItem(x, y, i.Width-1, it, theme)
		}
	}
}

func (i *InfoWindow) drawRightHint(xMin int, text, action string, style tcell.Style) {
	w := runewidth.StringWidth(text)
	startX := i.Width - w - 1
	if startX < xMin || startX < 0 || w <= 0 {
		return
	}
	endX := drawStyledText(startX, i.Y, i.Width, text, style)
	if action != "" && endX > startX {
		i.hits = append(i.hits, BottomBarHit{
			Y:      i.Y,
			XStart: startX,
			XEnd:   endX,
			Action: action,
		})
	}
}

func (i *InfoWindow) drawPromptHelperHints(afterX int) {
	if i.HasYN || !keyHintsEnabled() {
		return
	}
	theme := resolveBottomBarTheme()
	var full, compact string
	switch i.PromptType {
	case "Command", "Open":
		full = "Tab complete · ↑↓ history · Esc cancel"
		compact = "Tab · Esc"
	case "Find", "FindLiteral":
		full = "Enter find · ↑↓ history · Esc cancel"
		compact = "Enter · Esc"
	default:
		full = "Enter confirm · Esc cancel"
		compact = "Esc cancel"
	}
	if afterX+4+runewidth.StringWidth(full) <= i.Width {
		i.drawRightHint(afterX+4, full, "", theme.dim)
	} else if afterX+4+runewidth.StringWidth(compact) <= i.Width {
		i.drawRightHint(afterX+4, compact, "", theme.dim)
	}
}

// HitAt returns the clickable action at screen coordinates (x, y) if any.
func (i *InfoWindow) HitAt(x, y int) (string, bool) {
	for _, h := range i.hits {
		if h.Y == y && x >= h.XStart && x < h.XEnd {
			return h.Action, true
		}
	}
	return "", false
}
