package display

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	luar "layeh.com/gopher-luar"

	runewidth "github.com/mattn/go-runewidth"
	"github.com/micro-editor/tcell/v2"
	lua "github.com/yuin/gopher-lua"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/config"
	ulua "github.com/monicafaye/TypX/internal/lua"
	"github.com/monicafaye/TypX/internal/screen"
	"github.com/monicafaye/TypX/internal/util"
)

// StatusLine represents the information line at the bottom
// of each window
// It gives information such as filename, whether the file has been
// modified, filetype, cursor location
type StatusLine struct {
	Info map[string]func(*buffer.Buffer) string

	win *BufWindow
}

var statusInfo = map[string]func(*buffer.Buffer) string{
	"filename": func(b *buffer.Buffer) string {
		return b.GetName()
	},
	"line": func(b *buffer.Buffer) string {
		return strconv.Itoa(b.GetActiveCursor().Y + 1)
	},
	"col": func(b *buffer.Buffer) string {
		return strconv.Itoa(b.GetActiveCursor().X + 1)
	},
	"modified": func(b *buffer.Buffer) string {
		if b.Modified() {
			return "+ "
		}
		if b.Type.Readonly {
			return "[ro] "
		}
		return ""
	},
	"overwrite": func(b *buffer.Buffer) string {
		if b.OverwriteMode && !b.Type.Readonly {
			return "[ovwr] "
		}
		return ""
	},
	"lines": func(b *buffer.Buffer) string {
		return strconv.Itoa(b.LinesNum())
	},
	"percentage": func(b *buffer.Buffer) string {
		return strconv.Itoa((b.GetActiveCursor().Y + 1) * 100 / b.LinesNum())
	},
}

// SetStatusInfoFn registers a Go statusline provider for $(name) in
// statusformatl / statusformatr. Passing nil removes the provider.
// Convention: return "" when not applicable, otherwise "LABEL | ".
// Call during initialization (on the main goroutine).
func SetStatusInfoFn(name string, fn func(*buffer.Buffer) string) {
	if name == "" {
		return
	}
	if fn == nil {
		delete(statusInfo, name)
		return
	}
	statusInfo[name] = fn
}

func SetStatusInfoFnLua(fn string) {
	luaFn := strings.Split(fn, ".")
	if len(luaFn) <= 1 {
		return
	}
	plName, plFn := luaFn[0], luaFn[1]
	pl := config.FindPlugin(plName)
	if pl == nil {
		return
	}
	statusInfo[fn] = func(b *buffer.Buffer) string {
		if pl == nil || !pl.IsLoaded() {
			return ""
		}
		val, err := pl.Call(plFn, luar.New(ulua.L, b))
		if err == nil {
			if v, ok := val.(lua.LString); !ok {
				screen.TermMessage(plFn, "should return a string")
				return ""
			} else {
				return string(v)
			}
		}
		return ""
	}
}

// NewStatusLine returns a statusline bound to a window
func NewStatusLine(win *BufWindow) *StatusLine {
	s := new(StatusLine)
	s.win = win
	return s
}

// FindOpt finds a given option in the current buffer's settings
func (s *StatusLine) FindOpt(opt string) any {
	if val, ok := s.win.Buf.Settings[opt]; ok {
		return val
	}
	return "null"
}

var formatParser = regexp.MustCompile(`\$\(.+?\)`)

// Display draws the statusline to the screen
func (s *StatusLine) Display() {
	// We'll draw the line at the lowest line in the window
	y := s.win.Height + s.win.Y - 1

	winX := s.win.X

	b := s.win.Buf
	// autocomplete suggestions (for the buffer, not for the infowindow)
	if b.HasSuggestions && (len(b.Suggestions) > 1 || (len(b.Suggestions) == 1 && b.CurSuggestion < 0)) {
		statusLineStyle := config.DefStyle.Reverse(true)
		if style, ok := config.Colorscheme["statusline.suggestions"]; ok {
			statusLineStyle = style
		} else if style, ok := config.Colorscheme["statusline"]; ok {
			statusLineStyle = style
		}
		x := 0
		for j, sug := range b.Suggestions {
			style := statusLineStyle
			if b.CurSuggestion == j {
				style = style.Reverse(true)
			}
			for _, r := range sug {
				screen.SetContent(winX+x, y, r, nil, style)
				x++
				if x >= s.win.Width {
					return
				}
			}
			screen.SetContent(winX+x, y, ' ', nil, statusLineStyle)
			x++
			if x >= s.win.Width {
				return
			}
		}

		for x < s.win.Width {
			screen.SetContent(winX+x, y, ' ', nil, statusLineStyle)
			x++
		}
		return
	}

	formatStatus := func(tmpl, filenameOverride string) []byte {
		return formatParser.ReplaceAllFunc([]byte(tmpl), func(match []byte) []byte {
			name := match[2 : len(match)-1]
			if bytes.HasPrefix(name, []byte("opt")) {
				option := name[4:]
				return fmt.Append(nil, s.FindOpt(string(option)))
			} else if bytes.HasPrefix(name, []byte("bind")) {
				binding := string(name[5:])
				for k, v := range config.Bindings["buffer"] {
					if v == binding {
						return []byte(k)
					}
				}
				return []byte("null")
			} else {
				if filenameOverride != "" && string(name) == "filename" {
					return []byte(filenameOverride)
				}
				if fn, ok := statusInfo[string(name)]; ok {
					return []byte(fn(s.win.Buf))
				}
				return []byte{}
			}
		})
	}

	leftFormat := s.win.Buf.Settings["statusformatl"].(string)
	leftText := formatStatus(leftFormat, "")
	if s.win.Width > 0 && strings.Contains(leftFormat, "$(filename)") {
		leftW := util.StringWidth(leftText, util.CharacterCount(leftText), 1)
		if leftW > s.win.Width {
			fullName := statusInfo["filename"](s.win.Buf)
			fullNameW := runewidth.StringWidth(fullName)
			budget := s.win.Width - (leftW - fullNameW)
			if short := compactStatusFilename(fullName, budget); short != fullName {
				leftText = formatStatus(leftFormat, short)
			}
		}
	}
	rightText := formatStatus(s.win.Buf.Settings["statusformatr"].(string), "")

	statusLineStyle := config.DefStyle.Reverse(true)
	if s.win.IsActive() {
		if style, ok := config.Colorscheme["statusline"]; ok {
			statusLineStyle = style
		}
	} else {
		if style, ok := config.Colorscheme["statusline.inactive"]; ok {
			statusLineStyle = style
		} else if style, ok := config.Colorscheme["statusline"]; ok {
			statusLineStyle = style
		}
	}

	// Fill the full statusline row with the base style first.
	for x := 0; x < s.win.Width; x++ {
		screen.SetContent(winX+x, y, ' ', nil, statusLineStyle)
	}
	if s.win.Width <= 0 {
		return
	}

	leftStr := string(leftText)
	rightStr := string(rightText)
	leftLen := util.StringWidth(leftText, util.CharacterCount(leftText), 1)
	rightLen := util.StringWidth(rightText, util.CharacterCount(rightText), 1)

	var leftSpans, rightSpans []statusSpan
	if s.win.IsActive() {
		palette := resolveStatusLinePalette(statusLineStyle)
		leftSpans = styleStatusSegments(leftStr, true, palette)
		rightSpans = styleStatusSegments(rightStr, false, palette)
	} else {
		leftSpans = []statusSpan{{text: leftStr, style: statusLineStyle}}
		rightSpans = []statusSpan{{text: rightStr, style: statusLineStyle}}
	}

	// Render left spans, cleanly ellipsizing if wider than the pane.
	ellipsis := leftLen > s.win.Width && s.win.Width >= 2
	leftMax := s.win.Width
	if ellipsis {
		leftMax = s.win.Width - 1
	}
	drawStatusSpans(winX, y, leftMax, leftSpans)
	if ellipsis {
		dimStyle := statusLineStyle
		if s.win.IsActive() {
			dimStyle = resolveStatusLinePalette(statusLineStyle).dim
		}
		screen.SetContent(winX+s.win.Width-1, y, '…', nil, dimStyle)
	}

	// Render right spans only when there is clean breathing room (never colliding with left text).
	if rightLen > 0 && leftLen+2+rightLen <= s.win.Width {
		drawStatusSpans(winX+s.win.Width-rightLen, y, s.win.Width-(s.win.Width-rightLen), rightSpans)
	}
}

type statusSpan struct {
	text  string
	style tcell.Style
}

type statusLinePalette struct {
	base   tcell.Style
	bold   tcell.Style
	dim    tcell.Style
	ok     tcell.Style
	warn   tcell.Style
	err    tcell.Style
	accent tcell.Style
	markup tcell.Style
	math   tcell.Style
	code   tcell.Style
	raw    tcell.Style
}

func resolveStatusLinePalette(base tcell.Style) statusLinePalette {
	_, baseBg, baseAttr := base.Decompose()
	isReversed := baseAttr&tcell.AttrReverse != 0

	pick := func(explicit string, fgCandidates []string, bold bool) tcell.Style {
		if s, ok := config.Colorscheme[explicit]; ok {
			return s
		}
		st := base
		if bold {
			st = st.Bold(true)
		}
		if isReversed {
			return st
		}
		for _, grp := range fgCandidates {
			if s, ok := config.Colorscheme[grp]; ok {
				fg, _, _ := s.Decompose()
				if isReadableContrast(fg, baseBg, 3.0) {
					res := base.Foreground(fg)
					if bold {
						res = res.Bold(true)
					}
					return res
				}
			}
		}
		return st
	}

	dim := base
	if s, ok := config.Colorscheme["statusline.dim"]; ok {
		dim = s
	} else if !isReversed {
		for _, grp := range []string{"comment", "divider", "statusline.inactive"} {
			if s, ok := config.Colorscheme[grp]; ok {
				fg, _, _ := s.Decompose()
				if isReadableContrast(fg, baseBg, 2.0) {
					dim = base.Foreground(fg)
					break
				}
			}
		}
	}

	errStyle := pick("statusline.err", []string{"error", "gutter-error"}, true)
	if _, ok := config.Colorscheme["statusline.err"]; !ok {
		if s, ok := config.Colorscheme["error-message"]; ok {
			errStyle = s.Bold(true)
		}
	}

	return statusLinePalette{
		base:   base,
		bold:   base.Bold(true),
		dim:    dim,
		ok:     pick("statusline.ok", []string{"constant.bool.true", "diff-added", "special"}, true),
		warn:   pick("statusline.warn", []string{"gutter-warning", "diff-modified", "special"}, true),
		err:    errStyle,
		accent: pick("statusline.accent", []string{"conceal", "symbol", "type"}, true),
		markup: pick("statusline.mode.markup", []string{"identifier", "type"}, true),
		math:   pick("statusline.mode.math", []string{"symbol.math", "conceal", "identifier"}, true),
		code:   pick("statusline.mode.code", []string{"statement", "identifier.macro"}, true),
		raw:    pick("statusline.mode.raw", []string{"constant.string.raw", "constant", "special"}, true),
	}
}

func styleStatusSegments(text string, isLeft bool, p statusLinePalette) []statusSpan {
	if text == "" {
		return nil
	}
	parts := strings.Split(text, "|")
	spans := make([]statusSpan, 0, len(parts)*2)
	for idx, part := range parts {
		if idx > 0 {
			spans = append(spans, statusSpan{text: "|", style: p.dim})
		}
		if part == "" {
			continue
		}
		if isLeft && idx == 0 {
			spans = append(spans, styleLeadingSegment(part, p)...)
			continue
		}
		if !isLeft {
			spans = append(spans, statusSpan{text: part, style: p.dim})
			continue
		}
		spans = append(spans, statusSpan{text: part, style: classifyStatusSegment(part, p)})
	}
	return spans
}

// styleLeadingSegment highlights the filename in bold and flags (+, [ro],
// [ovwr], [PASTE]) in warning accent while leaving cursor coordinates calm.
func styleLeadingSegment(seg string, p statusLinePalette) []statusSpan {
	// Split before the cursor "(line,col)" suffix if present.
	coordIdx := strings.Index(seg, "(")
	head := seg
	tail := ""
	if coordIdx > 0 {
		head = seg[:coordIdx]
		tail = seg[coordIdx:]
	}

	var spans []statusSpan
	// Separate trailing status flags ("+ ", "[ro] ", "[ovwr] ") from filename.
	trimmedHead := strings.TrimRight(head, " ")
	trailSpaces := head[len(trimmedHead):]

	flagStart := -1
	for _, flag := range []string{" +", " [ro]", " [ovwr]"} {
		if idx := strings.Index(trimmedHead, flag); idx >= 0 && (flagStart < 0 || idx < flagStart) {
			flagStart = idx
		}
	}
	if flagStart >= 0 {
		spans = append(spans, statusSpan{text: trimmedHead[:flagStart], style: p.bold})
		spans = append(spans, statusSpan{text: trimmedHead[flagStart:] + trailSpaces, style: p.warn})
	} else {
		spans = append(spans, statusSpan{text: head, style: p.bold})
	}

	if tail != "" {
		if pasteIdx := strings.Index(tail, "PASTE"); pasteIdx >= 0 {
			spans = append(spans, statusSpan{text: tail[:pasteIdx], style: p.base})
			spans = append(spans, statusSpan{text: tail[pasteIdx:], style: p.warn})
		} else {
			spans = append(spans, statusSpan{text: tail, style: p.base})
		}
	}
	return spans
}

func classifyStatusSegment(seg string, p statusLinePalette) tcell.Style {
	trimmed := strings.TrimSpace(seg)
	switch trimmed {
	case "MARKUP":
		return p.markup
	case "MATH":
		return p.math
	case "CODE":
		return p.code
	case "RAW":
		return p.raw
	case "PREVIEW: ON":
		return p.accent
	case "LSP: ready":
		return p.ok
	case "LSP: off", "LSP: missing", "LSP: no tinymist":
		return p.dim
	case "LSP: error":
		return p.err
	case "LSP: starting":
		return p.warn
	case "ERR":
		return p.err
	}
	switch {
	case strings.HasPrefix(trimmed, "PIN:"):
		return p.accent
	case strings.HasPrefix(trimmed, "ERR:"):
		return p.err
	case strings.Contains(trimmed, "compiling") || strings.Contains(trimmed, "starting"):
		return p.warn
	case strings.HasPrefix(trimmed, "ft:"):
		return p.dim
	case strings.HasSuffix(trimmed, "p") && len(trimmed) >= 2 && trimmed[0] >= '0' && trimmed[0] <= '9':
		return p.accent
	default:
		return p.base
	}
}

func compactStatusFilename(name string, budget int) string {
	if !strings.ContainsAny(name, `/\`) {
		return name
	}
	sep := "/"
	if strings.Contains(name, `\`) {
		sep = `\`
	}
	rawParts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	if len(rawParts) <= 1 {
		return name
	}
	nameW := runewidth.StringWidth(name)
	base := rawParts[len(rawParts)-1]
	if len(rawParts) >= 2 {
		parentBase := "…" + sep + rawParts[len(rawParts)-2] + sep + base
		pbW := runewidth.StringWidth(parentBase)
		if pbW <= budget && pbW < nameW {
			return parentBase
		}
	}
	if runewidth.StringWidth(base) < nameW {
		return base
	}
	return name
}

func drawStatusSpans(startX, y, maxWidth int, spans []statusSpan) {
	x := 0
	for _, sp := range spans {
		b := []byte(sp.text)
		for len(b) > 0 && x < maxWidth {
			r, combc, size := util.DecodeCharacter(b)
			if size <= 0 {
				break
			}
			b = b[size:]
			rw := runewidth.RuneWidth(r)
			if rw < 1 {
				rw = 1
			}
			if x+rw > maxWidth {
				return
			}
			for j := 0; j < rw; j++ {
				c := r
				cb := combc
				if j > 0 {
					c = ' '
					cb = nil
				}
				screen.SetContent(startX+x+j, y, c, cb, sp.style)
			}
			x += rw
		}
	}
}
