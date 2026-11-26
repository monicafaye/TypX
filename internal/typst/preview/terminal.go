package preview

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// GraphicsProtocol identifies the detected in-terminal image rendering protocol.
type GraphicsProtocol int

const (
	ProtocolNone GraphicsProtocol = iota
	ProtocolSixel
	ProtocolKitty
)

// String returns a human-readable protocol name.
func (p GraphicsProtocol) String() string {
	switch p {
	case ProtocolKitty:
		return "Kitty"
	case ProtocolSixel:
		return "Sixel"
	default:
		return "none"
	}
}

// TerminalCaps describes detected inline graphics capabilities and cell geometry.
type TerminalCaps struct {
	Sixel  bool
	Kitty  bool
	CellW  int
	CellH  int
	Source string
	Probed bool
}

// Protocol returns the preferred inline graphics protocol supported by the terminal.
// Kitty graphics is preferred over Sixel when both are available for 24-bit color
// and explicit image lifecycle control.
func (c TerminalCaps) Protocol() GraphicsProtocol {
	if c.Kitty {
		return ProtocolKitty
	}
	if c.Sixel {
		return ProtocolSixel
	}
	return ProtocolNone
}

// SupportsInline reports whether the terminal supports any inline graphics protocol.
func (c TerminalCaps) SupportsInline() bool {
	return c.Protocol() != ProtocolNone
}

const (
	defaultCellW = 10
	defaultCellH = 20

	// Query cell size (CSI 16t / 14t) and Primary Device Attributes (DA1, CSI c),
	// optionally preceded by a 1x1 Kitty graphics test action (APC a=q).
	baseProbeQuery     = "\x1b[16t\x1b[14t\x1b[c"
	terminalProbeQuery = "\x1b[16t\x1b[14t\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\\x1b[c"
)

func probeQueryForEnv() string {
	// Skip the Kitty APC query inside tmux/GNU Screen (where raw APC is blocked
	// or mishandled) and macOS Terminal.app (which does not support Kitty graphics).
	if os.Getenv("TMUX") != "" || os.Getenv("STY") != "" || os.Getenv("TERM_PROGRAM") == "Apple_Terminal" {
		return baseProbeQuery
	}
	return terminalProbeQuery
}

func skipRawTerminalProbe() bool {
	t := strings.ToLower(strings.TrimSpace(os.Getenv("TERM")))
	return t == "" || t == "dumb" || t == "linux"
}

var (
	capsMu sync.RWMutex
	caps   = TerminalCaps{
		CellW:  defaultCellW,
		CellH:  defaultCellH,
		Source: "unprobed",
	}

	csi16Re = regexp.MustCompile(`\x1b\[6;(\d+);(\d+)t`)
	csi14Re = regexp.MustCompile(`\x1b\[4;(\d+);(\d+)t`)
	da1Re   = regexp.MustCompile(`\x1b\[\?([0-9;]+)c`)
	kittyRe = regexp.MustCompile("\x1b_G[^\x1b\x07]*i=31[^\x1b\x07]*;OK(?:\x1b\\\\|\x07)")
)

// ProbeTerminal queries the attached interactive terminal for inline graphics
// capabilities (Kitty graphics via APC a=q, Sixel via DA1 attribute 4) and exact
// cell pixel dimensions (via CSI 16 t / CSI 14 t). Must be called BEFORE tcell
// takes over stdin; safe no-op on non-TTY streams.
func ProbeTerminal() TerminalCaps {
	c := probeTerminalPlatform()
	capsMu.Lock()
	caps = c
	capsMu.Unlock()
	return c
}

// Caps returns the most recently detected terminal graphics capabilities.
func Caps() TerminalCaps {
	capsMu.RLock()
	c := caps
	capsMu.RUnlock()
	return c
}

// ParseProtocol parses a user-facing inline graphics protocol name ("kitty", "sixel").
// Returns ProtocolNone for "auto" or unrecognized names.
func ParseProtocol(name string) GraphicsProtocol {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "kitty":
		return ProtocolKitty
	case "sixel":
		return ProtocolSixel
	default:
		return ProtocolNone
	}
}

func configuredProtocol() GraphicsProtocol {
	return ParseProtocol(readGlobalString("typst.previewProtocol", "auto"))
}

// SupportsInline reports whether the current terminal supports any inline graphics
// protocol, or whether the user explicitly configured typst.previewProtocol.
func SupportsInline() bool {
	return Caps().SupportsInline() || configuredProtocol() != ProtocolNone
}

// SupportsSixel reports whether the current terminal supports inline Sixel graphics.
func SupportsSixel() bool {
	return Caps().Sixel
}

// SupportsKitty reports whether the current terminal supports the Kitty graphics protocol.
func SupportsKitty() bool {
	return Caps().Kitty
}

// ActiveProtocol returns the active inline graphics protocol, honoring an explicit
// typst.previewProtocol override ("kitty" or "sixel") before auto-detected caps.
func ActiveProtocol() GraphicsProtocol {
	if p := configuredProtocol(); p != ProtocolNone {
		return p
	}
	return Caps().Protocol()
}

// SetCapsForTest overrides detected terminal capabilities (intended for tests).
func SetCapsForTest(c TerminalCaps) {
	if c.CellW <= 0 {
		c.CellW = defaultCellW
	}
	if c.CellH <= 0 {
		c.CellH = defaultCellH
	}
	capsMu.Lock()
	caps = c
	capsMu.Unlock()
}

// Check writes an inline-preview terminal graphics diagnostic line for `typx --check`.
func Check(w io.Writer) {
	c := Caps()
	if !c.Probed {
		c = ProbeTerminal()
	}
	switch {
	case c.Kitty && c.Sixel:
		fmt.Fprintf(w, "Inline preview: [OK] Kitty graphics supported (Sixel also available; cell %dx%dpx via %s)\n", c.CellW, c.CellH, c.Source)
	case c.Kitty:
		fmt.Fprintf(w, "Inline preview: [OK] Kitty graphics supported (cell %dx%dpx via %s)\n", c.CellW, c.CellH, c.Source)
	case c.Sixel:
		fmt.Fprintf(w, "Inline preview: [OK] Sixel supported (cell %dx%dpx via %s)\n", c.CellW, c.CellH, c.Source)
	default:
		fmt.Fprintf(w, "Inline preview: [INFO] No inline graphics protocol detected (%s); :preview will use browser mode\n", c.Source)
	}
}

func parseProbeResponse(resp []byte, cols, rows int) (sixel, kitty bool, cellW, cellH int, src string) {
	if kittyRe.Match(resp) {
		kitty = true
	}

	if m := da1Re.FindSubmatch(resp); m != nil {
		parts := strings.Split(string(m[1]), ";")
		for _, p := range parts {
			if p == "4" {
				sixel = true
				break
			}
		}
	}

	if m := csi16Re.FindSubmatch(resp); m != nil {
		ch, _ := strconv.Atoi(string(m[1]))
		cw, _ := strconv.Atoi(string(m[2]))
		if cw >= 4 && cw <= 64 && ch >= 8 && ch <= 128 {
			return sixel, kitty, cw, ch, "CSI16t"
		}
	}

	if m := csi14Re.FindSubmatch(resp); m != nil && cols > 0 && rows > 0 {
		wh, _ := strconv.Atoi(string(m[1]))
		ww, _ := strconv.Atoi(string(m[2]))
		if ww > 0 && wh > 0 {
			cw := ww / cols
			ch := wh / rows
			if cw >= 4 && cw <= 64 && ch >= 8 && ch <= 128 {
				return sixel, kitty, cw, ch, "CSI14t"
			}
		}
	}

	return sixel, kitty, 0, 0, ""
}

func envIndicatesKitty() (bool, string) {
	// Never trust inherited outer terminal variables inside tmux or GNU Screen,
	// where APC graphics sequences are blocked unless passthrough is enabled.
	if os.Getenv("TMUX") != "" || os.Getenv("STY") != "" {
		return false, ""
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" {
		return true, "Kitty"
	}
	tp := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	if strings.Contains(tp, "ghostty") {
		return true, "Ghostty"
	}
	term := strings.ToLower(os.Getenv("TERM"))
	switch {
	case strings.Contains(term, "kitty"):
		return true, "Kitty"
	case strings.Contains(term, "ghostty"):
		return true, "Ghostty"
	}
	return false, ""
}

func envIndicatesSixel() (bool, string) {
	if os.Getenv("WT_SESSION") != "" {
		return true, "Windows Terminal"
	}
	tp := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	switch {
	case strings.Contains(tp, "wezterm"):
		return true, "WezTerm"
	case strings.Contains(tp, "mintty"):
		return true, "mintty"
	case strings.Contains(tp, "contour"):
		return true, "Contour"
	case strings.Contains(tp, "blackbox"):
		return true, "BlackBox"
	}
	term := strings.ToLower(os.Getenv("TERM"))
	switch {
	case strings.HasPrefix(term, "foot"):
		return true, "foot"
	case strings.HasPrefix(term, "mlterm"):
		return true, "mlterm"
	case strings.Contains(term, "sixel"):
		return true, term
	}
	if os.Getenv("KONSOLE_VERSION") != "" {
		return true, "Konsole"
	}
	return false, ""
}
