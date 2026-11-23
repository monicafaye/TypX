//go:build windows

package lsp

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestWindowsJobObjectKillsChildrenOnParentKill(t *testing.T) {
	parentCmd := exec.Command(os.Args[0])
	parentCmd.Env = append(os.Environ(), "TYPX_LSP_HELPER=job_parent")
	stdout, err := parentCmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, parentCmd.Start())
	defer func() {
		if parentCmd.Process != nil {
			_ = parentCmd.Process.Kill()
		}
		_ = parentCmd.Wait()
	}()

	lineCh := make(chan string, 1)
	go func() {
		br := bufio.NewReader(stdout)
		l, _ := br.ReadString('\n')
		lineCh <- strings.TrimSpace(l)
	}()

	var pidStr string
	select {
	case pidStr = <-lineCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for job_parent child PID")
	}

	childPID, err := strconv.Atoi(pidStr)
	require.NoError(t, err)
	require.Greater(t, childPID, 0)

	childHandle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(childPID))
	require.NoError(t, err)
	defer windows.CloseHandle(childHandle)
	t.Cleanup(func() {
		_ = windows.TerminateProcess(childHandle, 1)
	})

	// Confirm grandchild is alive while parent holds the Job Object.
	waitBefore, err := windows.WaitForSingleObject(childHandle, 0)
	require.NoError(t, err)
	assert.Equal(t, uint32(windows.WAIT_TIMEOUT), waitBefore, "child should still be running")

	// Force-kill parent abruptly (no graceful shutdown).
	require.NoError(t, parentCmd.Process.Kill())
	_ = parentCmd.Wait()

	// Kernel must terminate child immediately when parent's Job Object handle closes.
	waitAfter, err := windows.WaitForSingleObject(childHandle, 3000)
	require.NoError(t, err)
	assert.Equal(t, uint32(windows.WAIT_OBJECT_0), waitAfter, "child should be terminated by JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE")
}
