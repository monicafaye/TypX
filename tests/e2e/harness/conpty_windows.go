//go:build windows

package harness

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const stillActiveCode = 259

// ConPTYProcess represents an interactive subprocess attached to a Windows ConPTY.
type ConPTYProcess struct {
	hPC          windows.Handle
	inW          *os.File
	outR         *os.File
	procHandle   windows.Handle
	threadHandle windows.Handle
	PID          int

	outMu     sync.Mutex
	outBuf    bytes.Buffer
	drainDone chan struct{}

	closeOnce sync.Once
}

// StartConPTY spawns exePath attached to an isolated Windows Pseudo Console (ConPTY).
func StartConPTY(exePath string, args []string, env map[string]string, workDir string, cols, rows int16) (*ConPTYProcess, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create input pipe: %w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return nil, fmt.Errorf("create output pipe: %w", err)
	}

	var hPC windows.Handle
	size := windows.Coord{X: cols, Y: rows}
	if err := windows.CreatePseudoConsole(size, windows.Handle(inR.Fd()), windows.Handle(outW.Fd()), 0, &hPC); err != nil {
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
		return nil, fmt.Errorf("CreatePseudoConsole: %w", err)
	}

	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		windows.ClosePseudoConsole(hPC)
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
		return nil, fmt.Errorf("NewProcThreadAttributeList: %w", err)
	}

	hPCPtr := *(*unsafe.Pointer)(unsafe.Pointer(&hPC))
	if err := attrList.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, hPCPtr, unsafe.Sizeof(hPC)); err != nil {
		attrList.Delete()
		windows.ClosePseudoConsole(hPC)
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
		return nil, fmt.Errorf("UpdateProcThreadAttribute: %w", err)
	}

	cmdLine := buildWindowsCmdLine(exePath, args)
	cmdLineUTF16, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		attrList.Delete()
		windows.ClosePseudoConsole(hPC)
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
		return nil, err
	}

	var workDirUTF16 *uint16
	if workDir != "" {
		workDirUTF16, err = windows.UTF16PtrFromString(workDir)
		if err != nil {
			attrList.Delete()
			windows.ClosePseudoConsole(hPC)
			_ = inR.Close()
			_ = inW.Close()
			_ = outR.Close()
			_ = outW.Close()
			return nil, err
		}
	}

	envBlock := buildUnicodeEnvBlock(MergeEnv(env))

	var siEx windows.StartupInfoEx
	siEx.Cb = uint32(unsafe.Sizeof(siEx))
	siEx.ProcThreadAttributeList = attrList.List()

	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	err = windows.CreateProcess(
		nil,
		cmdLineUTF16,
		nil,
		nil,
		false,
		flags,
		&envBlock[0],
		workDirUTF16,
		&siEx.StartupInfo,
		&pi,
	)

	attrList.Delete()
	_ = inR.Close()
	_ = outW.Close()

	if err != nil {
		windows.ClosePseudoConsole(hPC)
		_ = inW.Close()
		_ = outR.Close()
		return nil, fmt.Errorf("CreateProcess(%s): %w", cmdLine, err)
	}

	p := &ConPTYProcess{
		hPC:          hPC,
		inW:          inW,
		outR:         outR,
		procHandle:   pi.Process,
		threadHandle: pi.Thread,
		PID:          int(pi.ProcessId),
		drainDone:    make(chan struct{}),
	}

	go p.drainOutput()
	return p, nil
}

func (p *ConPTYProcess) drainOutput() {
	defer close(p.drainDone)
	buf := make([]byte, 4096)
	for {
		n, err := p.outR.Read(buf)
		if n > 0 {
			p.outMu.Lock()
			_, _ = p.outBuf.Write(buf[:n])
			p.outMu.Unlock()
		}
		if err != nil {
			if err != io.EOF {
				// pipe closed during teardown
			}
			return
		}
	}
}

// WriteInput sends raw bytes (keystrokes/escape sequences) to the ConPTY input pipe.
func (p *ConPTYProcess) WriteInput(data []byte) error {
	_, err := p.inW.Write(data)
	return err
}

// Output returns all bytes drained from the ConPTY output pipe so far.
func (p *ConPTYProcess) Output() string {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	return p.outBuf.String()
}

// WaitExit waits up to timeout for the attached process to exit and returns its exit code.
func (p *ConPTYProcess) WaitExit(timeout time.Duration) (uint32, error) {
	ms := uint32(timeout.Milliseconds())
	if ms == 0 && timeout > 0 {
		ms = 1
	}
	res, err := windows.WaitForSingleObject(p.procHandle, ms)
	if err != nil {
		return 0, err
	}
	if res != windows.WAIT_OBJECT_0 {
		return 0, fmt.Errorf("process %d did not exit within %v (wait result=%d)", p.PID, timeout, res)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(p.procHandle, &code); err != nil {
		return 0, err
	}
	return code, nil
}

// KillProcess forcefully terminates the attached process without closing ConPTY.
func (p *ConPTYProcess) KillProcess() error {
	return windows.TerminateProcess(p.procHandle, 1)
}

// Close closes the ConPTY, pipes, and process/thread handles.
func (p *ConPTYProcess) Close() error {
	p.closeOnce.Do(func() {
		if p.inW != nil {
			_ = p.inW.Close()
		}
		if p.hPC != 0 {
			windows.ClosePseudoConsole(p.hPC)
			p.hPC = 0
		}
		if p.outR != nil {
			_ = p.outR.Close()
		}
		select {
		case <-p.drainDone:
		case <-time.After(1 * time.Second):
		}
		if p.threadHandle != 0 {
			_ = windows.CloseHandle(p.threadHandle)
			p.threadHandle = 0
		}
		if p.procHandle != 0 {
			_ = windows.CloseHandle(p.procHandle)
			p.procHandle = 0
		}
	})
	return nil
}

// IsPIDAlive reports whether the process with the given PID is currently running.
func IsPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err == nil {
		defer windows.CloseHandle(h)
		var code uint32
		if err := windows.GetExitCodeProcess(h, &code); err == nil && code == stillActiveCode {
			waitRes, werr := windows.WaitForSingleObject(h, 0)
			if werr == nil && waitRes == uint32(windows.WAIT_TIMEOUT) {
				return true
			}
		}
		return false
	}

	snap, snapErr := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if snapErr != nil {
		return false
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return false
	}
	for {
		if int(pe.ProcessID) == pid {
			return true
		}
		if err := windows.Process32Next(snap, &pe); err != nil {
			break
		}
	}
	return false
}

func buildWindowsCmdLine(exe string, args []string) string {
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, syscall.EscapeArg(exe))
	for _, a := range args {
		parts = append(parts, syscall.EscapeArg(a))
	}
	return strings.Join(parts, " ")
}

func buildUnicodeEnvBlock(env []string) []uint16 {
	var b strings.Builder
	for _, kv := range env {
		b.WriteString(kv)
		b.WriteByte(0)
	}
	b.WriteByte(0)
	return utf16.Encode([]rune(b.String()))
}
