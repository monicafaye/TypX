//go:build !windows

package harness

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// ConPTYProcess is only supported on Windows.
type ConPTYProcess struct {
	PID int
}

// StartConPTY returns an unsupported error on non-Windows platforms.
func StartConPTY(exePath string, args []string, env map[string]string, workDir string, cols, rows int16) (*ConPTYProcess, error) {
	return nil, errors.New("ConPTY is only supported on Windows")
}

func (p *ConPTYProcess) WriteInput(data []byte) error {
	return errors.New("ConPTY is only supported on Windows")
}

func (p *ConPTYProcess) Output() string {
	return ""
}

func (p *ConPTYProcess) WaitExit(timeout time.Duration) (uint32, error) {
	return 0, errors.New("ConPTY is only supported on Windows")
}

func (p *ConPTYProcess) KillProcess() error {
	return errors.New("ConPTY is only supported on Windows")
}

func (p *ConPTYProcess) Close() error {
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
