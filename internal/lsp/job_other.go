//go:build !windows

package lsp

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// ProcessJob owns a process group on Unix systems so child processes and
// descendants can be terminated cleanly on shutdown.
type ProcessJob struct {
	mu     sync.Mutex
	pid    int
	proc   *os.Process
	closed bool
}

func hideWindowSysProcAttr() *syscall.SysProcAttr {
	return nil
}

func startManagedProcess(cmd *exec.Cmd) (*ProcessJob, error) {
	var attr syscall.SysProcAttr
	if cmd.SysProcAttr != nil {
		attr = *cmd.SysProcAttr
	}
	attr.Setpgid = true
	cmd.SysProcAttr = &attr

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &ProcessJob{
		pid:  cmd.Process.Pid,
		proc: cmd.Process,
	}, nil
}

// Assigned reports whether process-group isolation is active.
func (j *ProcessJob) Assigned() bool {
	return j != nil && j.pid > 0
}

// Close terminates the managed process group. Safe and idempotent.
func (j *ProcessJob) Close() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		return nil
	}
	j.closed = true
	pid := j.pid
	proc := j.proc
	j.mu.Unlock()

	var firstErr error
	if pid > 0 {
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			firstErr = err
		}
	}
	if proc != nil {
		_ = proc.Kill()
	}
	return firstErr
}
