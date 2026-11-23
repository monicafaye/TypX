//go:build windows

package harness

import (
	"syscall"

	"golang.org/x/sys/windows"
)

func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS,
	}
}
