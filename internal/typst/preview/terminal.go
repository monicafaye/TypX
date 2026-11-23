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

// TerminalCaps describes detected inline graphics capabilities and cell geometry.
type TerminalCaps struct {
	Sixel  bool
	CellW  int
	CellH  int
	Source string
	Probed bool
}

const (
	defaultCellW = 10
	defaultCellH = 20
)

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
)

// ProbeTerminal queries the attached interactive terminal for Sixel capability
// (via DA1 attribute 4) and exact cell pixel dimensions (via CSI 16 t / CSI 14 t).
// Must be called BEFORE tcell takes over stdin; safe no-op on non-TTY streams.
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

// SupportsSixel reports whether the current terminal supports inline Sixel graphics.
func SupportsSixel() bool {
	return Caps().Sixel
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
	if c.Sixel {
		fmt.Fprintf(w, "Inline preview: [OK] Sixel supported (cell %dx%dpx via %s)\n", c.CellW, c.CellH, c.Source)
	} else {
		fmt.Fprintf(w, "Inline preview: [INFO] Sixel not detected (%s); :preview will use browser mode\n", c.Source)
	}
}

func parseProbeResponse(resp []byte, cols, rows int) (sixel bool, cellW, cellH int, src string) {
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
			return sixel, cw, ch, "CSI16t"
		}
	}

	if m := csi14Re.FindSubmatch(resp); m != nil && cols > 0 && rows > 0 {
		wh, _ := strconv.Atoi(string(m[1]))
		ww, _ := strconv.Atoi(string(m[2]))
		if ww > 0 && wh > 0 {
			cw := ww / cols
			ch := wh / rows
			if cw >= 4 && cw <= 64 && ch >= 8 && ch <= 128 {
				return sixel, cw, ch, "CSI14t"
			}
		}
	}

	return sixel, 0, 0, ""
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
