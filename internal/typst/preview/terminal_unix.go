//go:build !windows

package preview

import (
	"bytes"
	"os"
	"time"

	isatty "github.com/mattn/go-isatty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// EnableVTOutput is a no-op on Unix terminals (VT output is always active).
func EnableVTOutput() {}

func probeTerminalPlatform() TerminalCaps {
	res := TerminalCaps{
		CellW:  defaultCellW,
		CellH:  defaultCellH,
		Source: "non-tty",
		Probed: true,
	}
	inFd := int(os.Stdin.Fd())
	outFd := int(os.Stdout.Fd())
	if !isatty.IsTerminal(uintptr(inFd)) || !isatty.IsTerminal(uintptr(outFd)) {
		return res
	}

	var cols, rows int
	if ws, err := unix.IoctlGetWinsize(outFd, unix.TIOCGWINSZ); err == nil && ws != nil {
		cols = int(ws.Col)
		rows = int(ws.Row)
		if cols > 0 && rows > 0 && ws.Xpixel > 0 && ws.Ypixel > 0 {
			cw := int(ws.Xpixel) / cols
			ch := int(ws.Ypixel) / rows
			if cw >= 4 && cw <= 64 && ch >= 8 && ch <= 128 {
				res.CellW = cw
				res.CellH = ch
				res.Source = "TIOCGWINSZ"
			}
		}
	}

	oldState, err := term.MakeRaw(inFd)
	if err != nil {
		if ok, name := envIndicatesSixel(); ok {
			res.Sixel = true
			if res.Source == "non-tty" {
				res.Source = name
			}
		}
		return res
	}
	defer func() { _ = term.Restore(inFd, oldState) }()

	_, _ = os.Stdout.WriteString("\x1b[16t\x1b[14t\x1b[c")

	var resp []byte
	buf := make([]byte, 256)
	deadline := time.Now().Add(120 * time.Millisecond)
	for time.Now().Before(deadline) {
		rem := int(time.Until(deadline).Milliseconds())
		if rem <= 0 {
			break
		}
		pfd := []unix.PollFd{{Fd: int32(inFd), Events: unix.POLLIN}}
		nReady, err := unix.Poll(pfd, rem)
		if err != nil || nReady <= 0 {
			break
		}
		n, err := unix.Read(inFd, buf)
		if err != nil || n <= 0 {
			break
		}
		resp = append(resp, buf[:n]...)
		if bytes.IndexByte(resp, 'c') >= 0 {
			break
		}
	}

	sixel, cw, ch, src := parseProbeResponse(resp, cols, rows)
	if cw > 0 && ch > 0 {
		res.CellW = cw
		res.CellH = ch
		res.Source = src
	} else if res.Source == "non-tty" {
		res.Source = "default"
	}
	if sixel {
		res.Sixel = true
	} else if ok, name := envIndicatesSixel(); ok {
		res.Sixel = true
		if res.Source == "default" {
			res.Source = name
		}
	}
	return res
}
