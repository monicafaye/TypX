package display

import (
	"strings"

	runewidth "github.com/mattn/go-runewidth"
	"github.com/micro-editor/tcell/v2"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/info"
	"github.com/monicafaye/TypX/internal/screen"
	"github.com/monicafaye/TypX/internal/util"
)

type InfoWindow struct {
	*info.InfoBuf
	*View

	hscroll int
	hits    []BottomBarHit
}

func (i *InfoWindow) errStyle() tcell.Style {
	errStyle := config.DefStyle.
		Foreground(tcell.ColorBlack).
		Background(tcell.ColorMaroon)

	if _, ok := config.Colorscheme["error-message"]; ok {
		errStyle = config.Colorscheme["error-message"]
	}

	return errStyle
}

func (i *InfoWindow) defStyle() tcell.Style {
	defStyle := config.DefStyle

	if _, ok := config.Colorscheme["message"]; ok {
		defStyle = config.Colorscheme["message"]
	}

	return defStyle
}

func NewInfoWindow(b *info.InfoBuf) *InfoWindow {
	iw := new(InfoWindow)
	iw.InfoBuf = b
	iw.View = new(View)

	iw.Width, iw.Y = screen.Screen.Size()
	iw.Y--

	return iw
}

func (i *InfoWindow) Resize(w, h int) {
	i.Width = w
	i.Y = h
}

func (i *InfoWindow) SetBuffer(b *buffer.Buffer) {
	i.InfoBuf.Buffer = b
}

func (i *InfoWindow) Relocate() bool   { return false }
func (i *InfoWindow) GetView() *View   { return i.View }
func (i *InfoWindow) SetView(v *View)  {}
func (i *InfoWindow) SetActive(b bool) {}
func (i *InfoWindow) IsActive() bool   { return true }

func (i *InfoWindow) LocFromVisual(vloc buffer.Loc) buffer.Loc {
	c := i.Buffer.GetActiveCursor()
	l := i.Buffer.LineBytes(0)
	n := util.CharacterCountInString(i.Msg)
	return buffer.Loc{c.GetCharPosInLine(l, vloc.X-n), 0}
}

func (i *InfoWindow) BufView() View {
	return View{
		X:         0,
		Y:         i.Y,
		Width:     i.Width,
		Height:    1,
		StartLine: SLoc{0, 0},
		StartCol:  0,
	}
}

func (i *InfoWindow) Scroll(s SLoc, n int) SLoc        { return s }
func (i *InfoWindow) Diff(s1, s2 SLoc) int             { return 0 }
func (i *InfoWindow) SLocFromLoc(loc buffer.Loc) SLoc  { return SLoc{0, 0} }
func (i *InfoWindow) VLocFromLoc(loc buffer.Loc) VLoc  { return VLoc{SLoc{0, 0}, loc.X} }
func (i *InfoWindow) LocFromVLoc(vloc VLoc) buffer.Loc { return buffer.Loc{vloc.VisualX, 0} }

func (i *InfoWindow) Clear() {
	for x := 0; x < i.Width; x++ {
		screen.SetContent(x, i.Y, ' ', nil, i.defStyle())
	}
}

func (i *InfoWindow) displayBuffer() int {
	b := i.Buffer
	line := b.LineBytes(0)
	activeC := b.GetActiveCursor()

	blocX := 0
	vlocX := util.CharacterCountInString(i.Msg)

	tabsize := 4
	line, nColsBeforeStart, bslice := util.SliceVisualEnd(line, blocX, tabsize)
	blocX = bslice

	draw := func(r rune, combc []rune, style tcell.Style) {
		if nColsBeforeStart <= 0 {
			bloc := buffer.Loc{X: blocX, Y: 0}
			if activeC.HasSelection() &&
				(bloc.GreaterEqual(activeC.CurSelection[0]) && bloc.LessThan(activeC.CurSelection[1]) ||
					bloc.LessThan(activeC.CurSelection[0]) && bloc.GreaterEqual(activeC.CurSelection[1])) {
				// The current character is selected
				style = i.defStyle().Reverse(true)

				if s, ok := config.Colorscheme["selection"]; ok {
					style = s
				}

			}

			screen.SetContent(vlocX, i.Y, r, combc, style)
			vlocX += runewidth.RuneWidth(r)
		}
		nColsBeforeStart--
	}

	totalwidth := blocX - nColsBeforeStart
	for len(line) > 0 {
		curVX := vlocX
		curBX := blocX
		r, combc, size := util.DecodeCharacter(line)

		width := 0

		switch r {
		case '\t':
			width = tabsize - (totalwidth % tabsize)
			for j := 0; j < width; j++ {
				draw(' ', nil, i.defStyle())
			}
		default:
			width = runewidth.RuneWidth(r)
			draw(r, combc, i.defStyle())
		}

		blocX++
		line = line[size:]

		if activeC.X == curBX {
			screen.ShowCursor(curVX, i.Y)
		}
		totalwidth += width
		if vlocX >= i.Width {
			break
		}
	}
	if activeC.X == blocX {
		screen.ShowCursor(vlocX, i.Y)
	}
	return vlocX
}

func (i *InfoWindow) displayKeyMenu() {
	i.drawExpandedKeyMenu(currentBottomBarContext())
}

func (i *InfoWindow) totalSize() int {
	sum := 0
	for _, n := range i.Suggestions {
		sum += runewidth.StringWidth(n) + 1
	}
	return sum
}

func (i *InfoWindow) scrollToSuggestion() {
	x := 0
	s := i.totalSize()

	for j, n := range i.Suggestions {
		c := util.CharacterCountInString(n)
		if j == i.CurSuggestion {
			if x+c >= i.hscroll+i.Width {
				i.hscroll = util.Clamp(x+c+1-i.Width, 0, s-i.Width)
			} else if x < i.hscroll {
				i.hscroll = util.Clamp(x-1, 0, s-i.Width)
			}
			break
		}
		x += c + 1
	}

	if s-i.Width <= 0 {
		i.hscroll = 0
	}
}

func (i *InfoWindow) Display() {
	i.hits = i.hits[:0]

	if i.HasPrompt || config.GlobalSettings["infobar"].(bool) {
		keyMenuOpen := config.GetGlobalOption("keymenu").(bool)
		var ctx BottomBarContext
		ctxLoaded := false
		getCtx := func() BottomBarContext {
			if !ctxLoaded {
				ctx = currentBottomBarContext()
				ctxLoaded = true
			}
			return ctx
		}

		if keyMenuOpen {
			i.drawExpandedKeyMenu(getCtx())
		}

		hasVisibleMsg := i.HasMessage && strings.TrimSpace(i.Msg) != ""
		if !i.HasPrompt && !hasVisibleMsg && !i.HasError {
			if keyHintsEnabled() {
				i.drawIdleBottomBar(getCtx(), keyMenuOpen)
			} else {
				i.Clear()
			}
			return
		}

		theme := resolveBottomBarTheme()
		if i.HasError {
			style := i.errStyle()
			i.fillRow(i.Y, style)
			x := drawStyledText(0, i.Y, i.Width, i.Msg, style)
			if keyHintsEnabled() {
				i.drawRightHint(x+3, "Esc", "dismiss", style)
			}
		} else if i.HasPrompt {
			i.Clear()
			drawStyledText(0, i.Y, i.Width, i.Msg, theme.prompt)
			endX := i.displayBuffer()
			i.drawPromptHelperHints(endX)
		} else {
			i.Clear()
			x := 0
			if i.HasGutter {
				x = drawStyledText(x, i.Y, i.Width, "● ", theme.gutterMark)
			}
			x = drawStyledText(x, i.Y, i.Width, i.Msg, i.defStyle())
			if keyHintsEnabled() {
				i.drawRightHint(x+3, "Esc dismiss", "dismiss", theme.dim)
			}
		}
	}

	if i.HasSuggestions && len(i.Suggestions) > 1 {
		i.scrollToSuggestion()

		x := -i.hscroll
		done := false

		statusLineStyle := config.DefStyle.Reverse(true)
		if style, ok := config.Colorscheme["statusline.suggestions"]; ok {
			statusLineStyle = style
		} else if style, ok := config.Colorscheme["statusline"]; ok {
			statusLineStyle = style
		}
		keymenuOffset := 0
		if config.GetGlobalOption("keymenu").(bool) {
			keymenuOffset = keyMenuRowCount
		}

		draw := func(r rune, s tcell.Style) {
			y := i.Y - keymenuOffset - 1
			rw := runewidth.RuneWidth(r)
			for j := 0; j < rw; j++ {
				c := r
				if j > 0 {
					c = ' '
				}

				if x == i.Width-1 && !done {
					screen.SetContent(i.Width-1, y, '>', nil, s)
					x++
					break
				} else if x == 0 && i.hscroll > 0 {
					screen.SetContent(0, y, '<', nil, s)
				} else if x >= 0 && x < i.Width {
					screen.SetContent(x, y, c, nil, s)
				}
				x++
			}
		}

		for j, s := range i.Suggestions {
			style := statusLineStyle
			if i.CurSuggestion == j {
				style = style.Reverse(true)
			}
			for _, r := range s {
				draw(r, style)
				// screen.SetContent(x, i.Y-keymenuOffset-1, r, nil, style)
			}
			draw(' ', statusLineStyle)
		}

		for x < i.Width {
			draw(' ', statusLineStyle)
		}
	}
}
