//go:build !windows

package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// ConPTYProcess represents an interactive subprocess attached to a Unix PTY.
type ConPTYProcess struct {
	cmd  *exec.Cmd
	ptmx *os.File
	PID  int

	outMu     sync.Mutex
	outBuf    bytes.Buffer
	drainDone chan struct{}
	waitDone  chan error

	closeOnce sync.Once
}

// StartConPTY spawns exePath attached to an isolated Unix PTY with the requested window size.
func StartConPTY(exePath string, args []string, env map[string]string, workDir string, cols, rows int16) (*ConPTYProcess, error) {
	merged := make(map[string]string, len(env)+1)
	for k, v := range env {
		merged[k] = v
	}
	if _, ok := merged["TERM"]; !ok {
		if cur := os.Getenv("TERM"); cur == "" || cur == "dumb" {
			merged["TERM"] = "xterm-256color"
		}
	}

	cmd := exec.Command(exePath, args...)
	cmd.Dir = workDir
	cmd.Env = MergeEnv(merged)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
	if err != nil {
		return nil, fmt.Errorf("pty.StartWithSize(%s): %w", exePath, err)
	}

	p := &ConPTYProcess{
		cmd:       cmd,
		ptmx:      ptmx,
		PID:       cmd.Process.Pid,
		drainDone: make(chan struct{}),
		waitDone:  make(chan error, 1),
	}

	go p.drainOutput()
	go func() {
		p.waitDone <- cmd.Wait()
	}()
	return p, nil
}

func (p *ConPTYProcess) drainOutput() {
	defer close(p.drainDone)
	buf := make([]byte, 4096)
	for {
		n, err := p.ptmx.Read(buf)
		if n > 0 {
			p.outMu.Lock()
			_, _ = p.outBuf.Write(buf[:n])
			p.outMu.Unlock()
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				// PTY closed during teardown.
			}
			return
		}
	}
}

// WriteInput writes raw bytes (keystrokes/escape sequences) to the PTY primary fd.
func (p *ConPTYProcess) WriteInput(data []byte) error {
	_, err := p.ptmx.Write(data)
	return err
}

// Output returns all bytes drained from the PTY so far.
func (p *ConPTYProcess) Output() string {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	return p.outBuf.String()
}

// WaitExit waits up to timeout for the process to exit and returns its exit code.
func (p *ConPTYProcess) WaitExit(timeout time.Duration) (uint32, error) {
	select {
	case err := <-p.waitDone:
		if err == nil {
			return 0, nil
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return uint32(ee.ExitCode()), nil
		}
		return 0, err
	case <-time.After(timeout):
		return 0, fmt.Errorf("process %d did not exit within %v", p.PID, timeout)
	}
}

// KillProcess forcefully terminates the attached process and its process group.
func (p *ConPTYProcess) KillProcess() error {
	if p.PID <= 0 {
		return nil
	}
	if err := syscall.Kill(-p.PID, syscall.SIGKILL); err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}

// Close closes the PTY master file descriptor and waits briefly for output draining.
func (p *ConPTYProcess) Close() error {
	p.closeOnce.Do(func() {
		if p.ptmx != nil {
			_ = p.ptmx.Close()
		}
		select {
		case <-p.drainDone:
		case <-time.After(1 * time.Second):
		}
	})
	return nil
}

// IsPIDAlive checks whether pid is alive on POSIX systems.
func IsPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
