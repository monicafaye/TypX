//go:build windows

package lsp

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProcessJob owns a Windows Job Object configured with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE so child processes and descendants are
// terminated automatically when the handle closes or TypX crashes.
type ProcessJob struct {
	mu       sync.Mutex
	job      windows.Handle
	proc     *os.Process
	assigned bool
	closed   bool
}

func hideWindowSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
}

func startManagedProcess(cmd *exec.Cmd) (*ProcessJob, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}

	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}

	var attr syscall.SysProcAttr
	if cmd.SysProcAttr != nil {
		attr = *cmd.SysProcAttr
	}
	attr.HideWindow = true
	attr.CreationFlags |= windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED
	cmd.SysProcAttr = &attr

	if err := cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}

	pid := uint32(cmd.Process.Pid)
	assigned := false
	if procHandle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid); err == nil {
		if assignErr := windows.AssignProcessToJobObject(job, procHandle); assignErr == nil {
			assigned = true
		}
		_ = windows.CloseHandle(procHandle)
	}

	if err := resumeProcessThreads(pid); err != nil {
		_ = cmd.Process.Kill()
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("resumeProcessThreads: %w", err)
	}

	if !assigned {
		_ = windows.CloseHandle(job)
		job = 0
	}

	return &ProcessJob{
		job:      job,
		proc:     cmd.Process,
		assigned: assigned,
	}, nil
}

func resumeProcessThreads(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)

	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	if err := windows.Thread32First(snap, &te); err != nil {
		return err
	}

	resumed := 0
	for {
		if te.OwnerProcessID == pid {
			th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
			if err == nil {
				_, rerr := windows.ResumeThread(th)
				_ = windows.CloseHandle(th)
				if rerr == nil {
					resumed++
				}
			}
		}
		te.Size = uint32(unsafe.Sizeof(te))
		if err := windows.Thread32Next(snap, &te); err != nil {
			break
		}
	}
	if resumed == 0 {
		return fmt.Errorf("no threads resumed for pid %d", pid)
	}
	return nil
}

// Assigned reports whether the process was attached to a Windows Job Object.
func (j *ProcessJob) Assigned() bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.assigned
}

// Close terminates the Job Object (or process fallback) and closes handles.
// Safe and idempotent.
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
	job := j.job
	proc := j.proc
	j.job = 0
	j.mu.Unlock()

	var firstErr error
	if job != 0 {
		if err := windows.TerminateJobObject(job, 1); err != nil {
			firstErr = err
		}
		if err := windows.CloseHandle(job); err != nil && firstErr == nil {
			firstErr = err
		}
		return firstErr
	}
	if proc != nil {
		return proc.Kill()
	}
	return nil
}
