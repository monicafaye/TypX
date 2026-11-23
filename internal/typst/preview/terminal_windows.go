//go:build windows

package preview

import (
	"bytes"
	"os"
	"syscall"
	"time"
	"unsafe"

	isatty "github.com/mattn/go-isatty"
)

var (
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode    = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode    = kernel32.NewProc("SetConsoleMode")
	procWaitForSingleObj  = kernel32.NewProc("WaitForSingleObject")
	procFlushConsoleInput = kernel32.NewProc("FlushConsoleInputBuffer")
	procGetConsoleBufInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

const (
	enableVirtualTerminalProcessing = 0x0004
	enableVirtualTerminalInput      = 0x0200
)

type winCoord struct{ X, Y int16 }
type winSmallRect struct{ Left, Top, Right, Bottom int16 }
type winConsoleBufInfo struct {
	Size              winCoord
	CursorPos         winCoord
	Attrs             uint16
	Window            winSmallRect
	MaximumWindowSize winCoord
}

// EnableVTOutput ensures ENABLE_VIRTUAL_TERMINAL_PROCESSING is active on STDOUT.
func EnableVTOutput() {
	h, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if err != nil || h == 0 || h == syscall.InvalidHandle {
		return
	}
	var mode uint32
	r1, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode)))
	if r1 == 0 {
		return
	}
	if mode&enableVirtualTerminalProcessing == 0 {
		procSetConsoleMode.Call(uintptr(h), uintptr(mode|enableVirtualTerminalProcessing))
	}
}

func probeTerminalPlatform() TerminalCaps {
	res := TerminalCaps{
		CellW:  defaultCellW,
		CellH:  defaultCellH,
		Source: "non-tty",
		Probed: true,
	}
	if !isatty.IsTerminal(os.Stdin.Fd()) || !isatty.IsTerminal(os.Stdout.Fd()) {
		return res
	}

	inH, err := syscall.Open("CONIN$", syscall.O_RDWR, 0)
	if err != nil {
		if ok, name := envIndicatesSixel(); ok {
			res.Sixel = true
			res.Source = name
		}
		return res
	}
	defer syscall.Close(inH)

	outH, err := syscall.Open("CONOUT$", syscall.O_RDWR, 0)
	if err != nil {
		if ok, name := envIndicatesSixel(); ok {
			res.Sixel = true
			res.Source = name
		}
		return res
	}
	defer syscall.Close(outH)

	var origIn, origOut uint32
	procGetConsoleMode.Call(uintptr(inH), uintptr(unsafe.Pointer(&origIn)))
	procGetConsoleMode.Call(uintptr(outH), uintptr(unsafe.Pointer(&origOut)))
	defer func() {
		procSetConsoleMode.Call(uintptr(inH), uintptr(origIn))
		procSetConsoleMode.Call(uintptr(outH), uintptr(origOut))
	}()

	procSetConsoleMode.Call(uintptr(outH), uintptr(origOut|enableVirtualTerminalProcessing))
	procSetConsoleMode.Call(uintptr(inH), uintptr(enableVirtualTerminalInput))
	procFlushConsoleInput.Call(uintptr(inH))

	query := []byte("\x1b[16t\x1b[14t\x1b[c")
	var written uint32
	_ = syscall.WriteFile(outH, query, &written, nil)

	var resp []byte
	buf := make([]byte, 256)
	deadline := time.Now().Add(120 * time.Millisecond)
	for time.Now().Before(deadline) {
		rem := time.Until(deadline).Milliseconds()
		if rem <= 0 {
			break
		}
		r, _, _ := procWaitForSingleObj.Call(uintptr(inH), uintptr(rem))
		if r != 0 {
			break
		}
		var n uint32
		if err := syscall.ReadFile(inH, buf, &n, nil); err != nil || n == 0 {
			break
		}
		resp = append(resp, buf[:n]...)
		if bytes.IndexByte(resp, 'c') >= 0 {
			break
		}
	}

	var cols, rows int
	var info winConsoleBufInfo
	if r, _, _ := procGetConsoleBufInfo.Call(uintptr(outH), uintptr(unsafe.Pointer(&info))); r != 0 {
		cols = int(info.Window.Right-info.Window.Left) + 1
		rows = int(info.Window.Bottom-info.Window.Top) + 1
	}

	sixel, cw, ch, src := parseProbeResponse(resp, cols, rows)
	if cw > 0 && ch > 0 {
		res.CellW = cw
		res.CellH = ch
		res.Source = src
	} else {
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
