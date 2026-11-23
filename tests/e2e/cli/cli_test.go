package cli_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/lsp"
	"github.com/monicafaye/TypX/internal/util"
	"github.com/monicafaye/TypX/tests/e2e/harness"
)

// --- Tier 1: CLI Flags & Subprocess Discovery (>=5 cases) ---

func TestCLI_VersionFlag(t *testing.T) {
	typx := harness.BuildTypxBinary(t)
	cfgDir := harness.IsolatedConfigDir(t, nil)

	for _, flagName := range []string{"--version", "-version"} {
		t.Run(flagName, func(t *testing.T) {
			res := harness.RunCLI(t, typx, map[string]string{
				"TYPX_CONFIG_HOME": cfgDir,
			}, "", flagName)
			require.Equal(t, 0, res.ExitCode, "stderr: %s", res.Stderr)
			assert.Contains(t, res.Stdout, "TypX "+util.TypXVersion)
			assert.Contains(t, res.Stdout, "(based on micro "+util.MicroBaseVersion+")")
			assert.Contains(t, res.Stdout, "Commit hash:")
			assert.Contains(t, res.Stdout, "Compiled on")
		})
	}
}

func TestCLI_OptionsFlag(t *testing.T) {
	typx := harness.BuildTypxBinary(t)
	cfgDir := harness.IsolatedConfigDir(t, nil)

	expectedTypstKeys := []string{
		"-typst.conceal ",
		"-typst.concealMode ",
		"-typst.smartPairs ",
		"-typst.tinymistPath ",
		"-typst.lspEnabled ",
		"-typst.debounceMs ",
		"-typst.previewOpenBrowser ",
		"-typst.outlineWidth ",
		"-typst.outlineShowLabels ",
	}

	for _, flagName := range []string{"--options", "-options"} {
		t.Run(flagName, func(t *testing.T) {
			res := harness.RunCLI(t, typx, map[string]string{
				"TYPX_CONFIG_HOME": cfgDir,
			}, "", flagName)
			require.Equal(t, 0, res.ExitCode, "stderr: %s", res.Stderr)
			for _, key := range expectedTypstKeys {
				assert.Contains(t, res.Stdout, key)
			}
		})
	}
}

func TestCLI_CheckFlag_MissingTinymist(t *testing.T) {
	typx := harness.BuildTypxBinary(t)
	cfgDir := harness.IsolatedConfigDir(t, nil)

	for _, flagName := range []string{"--check", "-check"} {
		t.Run(flagName, func(t *testing.T) {
			res := harness.RunCLI(t, typx, map[string]string{
				"TYPX_CONFIG_HOME": cfgDir,
				"PATH":             filepath.Dir(typx),
			}, "", flagName)
			require.Equal(t, 0, res.ExitCode, "stdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
			assert.Contains(t, res.Stdout, "Config dir:   [OK]")
			assert.Contains(t, res.Stdout, "Settings:     [OK]")
			assert.Contains(t, res.Stdout, "Typst syntax: [OK] embedded")
			assert.Contains(t, res.Stdout, "Tinymist LSP:")
		})
	}
}

func TestCLI_CheckFlag_NoLSP(t *testing.T) {
	typx := harness.BuildTypxBinary(t)
	cfgDir := harness.IsolatedConfigDir(t, nil)

	for _, noLspFlag := range []string{"--no-lsp", "-no-lsp"} {
		t.Run(noLspFlag, func(t *testing.T) {
			res := harness.RunCLI(t, typx, map[string]string{
				"TYPX_CONFIG_HOME": cfgDir,
			}, "", "--check", noLspFlag)
			require.Equal(t, 0, res.ExitCode, "stdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
			assert.Contains(t, res.Stdout, "Tinymist LSP: [INFO] disabled")
		})
	}
}

func TestCLI_CheckFlag_WithMockTinymist(t *testing.T) {
	typx := harness.BuildTypxBinary(t)
	mockBin := harness.BuildMockTinymist(t)

	t.Run("settings.json typst.tinymistPath", func(t *testing.T) {
		cfgDir := harness.IsolatedConfigDir(t, map[string]any{
			"typst.tinymistPath": mockBin,
		})
		res := harness.RunCLI(t, typx, map[string]string{
			"TYPX_CONFIG_HOME": cfgDir,
		}, "", "--check")
		require.Equal(t, 0, res.ExitCode, "stdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
		assert.Contains(t, res.Stdout, "Tinymist LSP: [OK]")
		assert.Contains(t, res.Stdout, mockBin)
	})

	t.Run("CLI -typst.tinymistPath flag", func(t *testing.T) {
		cfgDir := harness.IsolatedConfigDir(t, nil)
		res := harness.RunCLI(t, typx, map[string]string{
			"TYPX_CONFIG_HOME": cfgDir,
		}, "", "-typst.tinymistPath", mockBin, "--check")
		require.Equal(t, 0, res.ExitCode, "stdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
		assert.Contains(t, res.Stdout, "Tinymist LSP: [OK]")
		assert.Contains(t, res.Stdout, mockBin)
	})
}

func TestCLI_OptionOverrideFlags(t *testing.T) {
	typx := harness.BuildTypxBinary(t)
	cfgDir := harness.IsolatedConfigDir(t, map[string]any{
		"typst.lspEnabled": true,
	})

	res := harness.RunCLI(t, typx, map[string]string{
		"TYPX_CONFIG_HOME": cfgDir,
	}, "", "-typst.lspEnabled", "false", "-typst.debounceMs", "75", "--check")
	require.Equal(t, 0, res.ExitCode, "stdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
	assert.Contains(t, res.Stdout, "Tinymist LSP: [INFO] disabled")

	// Session CLI flag must remain volatile (not persisted to settings.json).
	raw, err := os.ReadFile(filepath.Join(cfgDir, "settings.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "75")
}

func TestCLI_MockTinymist_VersionAndJSONRPCProtocol(t *testing.T) {
	mockBin := harness.BuildMockTinymist(t)

	verRes := harness.RunCLI(t, mockBin, nil, "", "--version")
	require.Equal(t, 0, verRes.ExitCode)
	assert.Contains(t, verRes.Stdout, "tinymist 0.13.0-mock")

	failRes := harness.RunCLI(t, mockBin, map[string]string{"TYPX_MOCK_VERSION_FAIL": "1"}, "", "-V")
	assert.NotEqual(t, 0, failRes.ExitCode)

	mockEnv, pidFile, traceFile, _ := harness.MockEnv(t, harness.MockOptions{})
	cmd := exec.Command(mockBin, "lsp")
	cmd.Env = harness.MergeEnv(mockEnv)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	writeFrame := func(msg any) {
		b, err := json.Marshal(msg)
		require.NoError(t, err)
		_, err = fmt.Fprintf(stdin, "Content-Length: %d\r\n\r\n%s", len(b), string(b))
		require.NoError(t, err)
	}
	reader := bufio.NewReader(stdout)
	readFrame := func() map[string]any {
		contentLen := -1
		for {
			line, err := reader.ReadString('\n')
			require.NoError(t, err)
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			if strings.HasPrefix(strings.ToLower(line), "content-length:") {
				n, err := strconv.Atoi(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]))
				require.NoError(t, err)
				contentLen = n
			}
		}
		require.Greater(t, contentLen, 0)
		buf := make([]byte, contentLen)
		_, err := io.ReadFull(reader, buf)
		require.NoError(t, err)
		var out map[string]any
		require.NoError(t, json.Unmarshal(buf, &out))
		return out
	}

	writeFrame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	initResp := readFrame()
	assert.Equal(t, float64(1), initResp["id"])

	writeFrame(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})

	// Verify UTF-16 offset accounting with surrogate pair U+1D54F ("𝕏").
	writeFrame(map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":     "file:///test.typ",
				"version": 1,
				"text":    "𝕏 + ERR_TRIGGER\n",
			},
		},
	})
	diagNotif := readFrame()
	assert.Equal(t, "textDocument/publishDiagnostics", diagNotif["method"])
	params := diagNotif["params"].(map[string]any)
	diags := params["diagnostics"].([]any)
	require.Len(t, diags, 1)
	rng := diags[0].(map[string]any)["range"].(map[string]any)
	start := rng["start"].(map[string]any)
	// "𝕏" (2 UTF-16 code units) + " + " (3 UTF-16 code units) = 5
	assert.Equal(t, float64(5), start["character"])

	// Clearing ERR_TRIGGER publishes empty diagnostics.
	writeFrame(map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didChange",
		"params": map[string]any{
			"textDocument":   map[string]any{"uri": "file:///test.typ", "version": 2},
			"contentChanges": []map[string]any{{"text": "clean text\n"}},
		},
	})
	clearNotif := readFrame()
	assert.Equal(t, "textDocument/publishDiagnostics", clearNotif["method"])
	assert.Empty(t, clearNotif["params"].(map[string]any)["diagnostics"])

	writeFrame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
	shutResp := readFrame()
	assert.Equal(t, float64(2), shutResp["id"])
	writeFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"})
	require.NoError(t, stdin.Close())
	require.NoError(t, cmd.Wait())

	pids, err := harness.ReadMockPIDs(pidFile)
	require.NoError(t, err)
	assert.NotEmpty(t, pids)

	recs, err := harness.ReadMockTrace(traceFile)
	require.NoError(t, err)
	assert.True(t, harness.HasTraceMethod(recs, "initialize"))
	assert.True(t, harness.HasTraceMethod(recs, "shutdown"))
	assert.True(t, harness.HasTraceMethod(recs, "exit"))
}

// --- Tier 2: CLI & Config Boundary Cases (>=5 cases) ---

func TestCLI_CheckFlag_BrokenSettingsJSON(t *testing.T) {
	typx := harness.BuildTypxBinary(t)
	cfgDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "settings.json"), []byte("{not valid json"), 0644))

	res := harness.RunCLI(t, typx, map[string]string{
		"TYPX_CONFIG_HOME": cfgDir,
	}, "", "--check")
	assert.Equal(t, 1, res.ExitCode, "stdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
	assert.Contains(t, res.Stdout, "Settings:     [FAIL]")
}

func TestCLI_ConfigDirPrecedence_AndMissingFallback(t *testing.T) {
	typx := harness.BuildTypxBinary(t)
	envDir := harness.IsolatedConfigDir(t, nil)
	flagDir := harness.IsolatedConfigDir(t, nil)
	missingDir := filepath.Join(t.TempDir(), "does-not-exist")

	t.Run("-config-dir overrides TYPX_CONFIG_HOME", func(t *testing.T) {
		res := harness.RunCLI(t, typx, map[string]string{
			"TYPX_CONFIG_HOME": envDir,
		}, "", "-config-dir", flagDir, "--check")
		require.Equal(t, 0, res.ExitCode, "stdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
		assert.Contains(t, res.Stdout, "Config dir:   [OK] "+flagDir)
	})

	t.Run("missing -config-dir falls back with FAIL in --check", func(t *testing.T) {
		res := harness.RunCLI(t, typx, map[string]string{
			"TYPX_CONFIG_HOME": envDir,
		}, "", "-config-dir", missingDir, "--check")
		assert.Equal(t, 1, res.ExitCode, "stdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
		assert.Contains(t, res.Stdout, "Config dir:   [FAIL]")
		assert.Contains(t, res.Stdout, "does not exist")
	})
}

func TestCLI_InvalidFlag_ExitCode2(t *testing.T) {
	typx := harness.BuildTypxBinary(t)
	cfgDir := harness.IsolatedConfigDir(t, nil)

	res := harness.RunCLI(t, typx, map[string]string{
		"TYPX_CONFIG_HOME": cfgDir,
	}, "", "--definitely-invalid-typx-flag")
	assert.Equal(t, 2, res.ExitCode)
	assert.Contains(t, res.Stderr, "flag provided but not defined")
}

func TestCLI_CheckFlag_NonExistentCustomTinymistPath(t *testing.T) {
	typx := harness.BuildTypxBinary(t)
	missingMock := filepath.Join(t.TempDir(), "missing-tinymist.exe")
	cfgDir := harness.IsolatedConfigDir(t, map[string]any{
		"typst.tinymistPath": missingMock,
	})

	res := harness.RunCLI(t, typx, map[string]string{
		"TYPX_CONFIG_HOME": cfgDir,
		"PATH":             filepath.Dir(typx),
	}, "", "--check")
	require.Equal(t, 0, res.ExitCode, "stdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
	assert.Contains(t, res.Stdout, "Tinymist LSP: [WARN]")
}

func TestCLI_CheckFlag_MockVersionFail_GracefulExit0(t *testing.T) {
	typx := harness.BuildTypxBinary(t)
	mockBin := harness.BuildMockTinymist(t)
	cfgDir := harness.IsolatedConfigDir(t, map[string]any{
		"typst.tinymistPath": mockBin,
	})

	res := harness.RunCLI(t, typx, map[string]string{
		"TYPX_CONFIG_HOME":       cfgDir,
		"TYPX_MOCK_VERSION_FAIL": "1",
	}, "", "--check")
	require.Equal(t, 0, res.ExitCode, "stdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
	assert.Contains(t, res.Stdout, "Tinymist LSP:")
}

func TestCLI_CheckFlag_ConfigDirIsFile(t *testing.T) {
	typx := harness.BuildTypxBinary(t)
	filePath := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(filePath, []byte("file"), 0644))

	res := harness.RunCLI(t, typx, map[string]string{
		"TYPX_CONFIG_HOME": filePath,
	}, "", "--check")
	assert.False(t, res.TimedOut)
	assert.Equal(t, 1, res.ExitCode, "stdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
	assert.Contains(t, res.Stdout, "Config dir:   [FAIL]")
}

func TestCLI_CleanFlag_IsolatedConfig(t *testing.T) {
	typx := harness.BuildTypxBinary(t)
	cfgDir := harness.IsolatedConfigDir(t, map[string]any{
		"tabsize": 2,
	})

	res := harness.RunCLI(t, typx, map[string]string{
		"TYPX_CONFIG_HOME": cfgDir,
	}, "", "-clean")
	assert.False(t, res.TimedOut)
	require.Equal(t, 0, res.ExitCode, "stdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
}

// --- ConPTY Sanity & Tier 4 Black-Box Subprocess Scenarios ---

func TestCLI_ConPTY_Phase1Sanity_CtrlQ(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("ConPTY test is Windows-only")
	}

	typx := harness.BuildTypxBinary(t)
	cfgDir := harness.IsolatedConfigDir(t, nil)
	workDir := t.TempDir()
	txtFile := filepath.Join(workDir, "notes.txt")
	require.NoError(t, os.WriteFile(txtFile, []byte("hello typx\n"), 0644))

	proc, err := harness.StartConPTY(typx, []string{"--no-lsp", txtFile}, map[string]string{
		"TYPX_CONFIG_HOME": cfgDir,
	}, workDir, 100, 30)
	require.NoError(t, err)
	defer proc.Close()

	// Give typx.exe a moment to complete screen.Init + initial draw, then send Ctrl-Q (0x11).
	harness.Eventually(t, 5*time.Second, 50*time.Millisecond, func() (bool, string) {
		if len(proc.Output()) > 0 {
			return true, ""
		}
		return false, "waiting for initial ConPTY screen output"
	})

	require.NoError(t, proc.WriteInput([]byte{0x11}))
	code, err := proc.WaitExit(5 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, uint32(0), code)
}

// Scenario S1: Black-box ConPTY clean exit sends LSP shutdown+exit and leaves 0 tinymist processes.
func TestE2E_ConPTY_CleanExit_LSPShutdown(t *testing.T) {
	harness.RequirePhase23(t)
	if runtime.GOOS != "windows" {
		t.Skip("ConPTY test is Windows-only")
	}

	typx := harness.BuildTypxBinary(t)
	mockBin := harness.BuildMockTinymist(t)
	cfgDir := harness.IsolatedConfigDir(t, map[string]any{
		"typst.tinymistPath":       mockBin,
		"typst.lspEnabled":         true,
		"typst.debounceMs":         50,
		"typst.previewOpenBrowser": false,
	})
	workDir := t.TempDir()
	typFile := filepath.Join(workDir, "paper.typ")
	require.NoError(t, os.WriteFile(typFile, []byte("= Title\n\n$alpha + beta$\n"), 0644))

	mockEnv, pidFile, traceFile, _ := harness.MockEnv(t, harness.MockOptions{})
	mockEnv["TYPX_CONFIG_HOME"] = cfgDir

	proc, err := harness.StartConPTY(typx, []string{typFile}, mockEnv, workDir, 100, 30)
	require.NoError(t, err)
	defer proc.Close()

	var mockPID int
	harness.Eventually(t, 10*time.Second, 50*time.Millisecond, func() (bool, string) {
		pids, err := harness.ReadMockPIDs(pidFile)
		if err != nil || len(pids) == 0 {
			return false, "mock-tinymist PID not yet written"
		}
		mockPID = pids[0]
		recs, err := harness.ReadMockTrace(traceFile)
		if err != nil {
			return false, "trace file not readable yet"
		}
		if !harness.HasTraceMethod(recs, "initialize") {
			return false, "initialize not yet in trace"
		}
		if !harness.HasTraceMethod(recs, "textDocument/didOpen") {
			return false, "textDocument/didOpen not yet in trace"
		}
		return true, ""
	})

	// Send Ctrl-Q (0x11) to cleanly exit typx.exe.
	require.NoError(t, proc.WriteInput([]byte{0x11}))
	code, err := proc.WaitExit(5 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, uint32(0), code)

	recs, err := harness.ReadMockTrace(traceFile)
	require.NoError(t, err)
	assert.True(t, harness.HasTraceMethod(recs, "shutdown"), "expected LSP shutdown in trace")
	assert.True(t, harness.HasTraceMethod(recs, "exit"), "expected LSP exit in trace")

	harness.Eventually(t, 3*time.Second, 50*time.Millisecond, func() (bool, string) {
		if !harness.IsPIDAlive(mockPID) {
			return true, ""
		}
		return false, fmt.Sprintf("mock-tinymist PID %d still alive after clean exit", mockPID)
	})
}

// Scenario S2: Black-box ConPTY force-kill proves Windows Job Object kills child tinymist
// BEFORE ConPTY is closed and even when mock-tinymist ignores stdin EOF.
func TestE2E_ConPTY_ForceKill_JobObjectCleanup(t *testing.T) {
	harness.RequirePhase23(t)
	if runtime.GOOS != "windows" {
		t.Skip("ConPTY Job Object test is Windows-only")
	}

	typx := harness.BuildTypxBinary(t)
	mockBin := harness.BuildMockTinymist(t)
	cfgDir := harness.IsolatedConfigDir(t, map[string]any{
		"typst.tinymistPath":       mockBin,
		"typst.lspEnabled":         true,
		"typst.debounceMs":         50,
		"typst.previewOpenBrowser": false,
	})
	workDir := t.TempDir()
	typFile := filepath.Join(workDir, "forcekill.typ")
	require.NoError(t, os.WriteFile(typFile, []byte("= Force Kill\n\n$gamma$\n"), 0644))

	mockEnv, pidFile, traceFile, _ := harness.MockEnv(t, harness.MockOptions{
		IgnoreStdinEOF: true,
	})
	mockEnv["TYPX_CONFIG_HOME"] = cfgDir

	proc, err := harness.StartConPTY(typx, []string{typFile}, mockEnv, workDir, 100, 30)
	require.NoError(t, err)
	defer proc.Close()

	var mockPID int
	harness.Eventually(t, 10*time.Second, 50*time.Millisecond, func() (bool, string) {
		pids, err := harness.ReadMockPIDs(pidFile)
		if err != nil || len(pids) == 0 {
			return false, "mock-tinymist PID not yet written"
		}
		mockPID = pids[0]
		recs, err := harness.ReadMockTrace(traceFile)
		if err != nil {
			return false, "trace file not readable yet"
		}
		if !harness.HasTraceMethod(recs, "textDocument/didOpen") {
			return false, "textDocument/didOpen not yet in trace"
		}
		return true, ""
	})

	// Terminate typx.exe BEFORE closing ConPTY so CTRL_CLOSE_EVENT cannot mask Job Object failure.
	require.NoError(t, proc.KillProcess())
	_, _ = proc.WaitExit(5 * time.Second)

	// Assert mock-tinymist.exe dies promptly WHILE ConPTY is still open.
	harness.Eventually(t, 3*time.Second, 50*time.Millisecond, func() (bool, string) {
		if !harness.IsPIDAlive(mockPID) {
			return true, ""
		}
		return false, fmt.Sprintf("mock-tinymist PID %d survived typx TerminateProcess (Job Object kill-on-close failed)", mockPID)
	})

	recs, err := harness.ReadMockTrace(traceFile)
	require.NoError(t, err)
	assert.False(t, harness.HasTraceMethod(recs, "shutdown"), "force-kill must not have sent graceful shutdown")
}

// Optional gated live-tinymist smoke test (discovers tinymist via PATH or VS Code/Cargo/WinGet/Scoop).
func TestE2E_LiveTinymist_Smoke(t *testing.T) {
	harness.RequirePhase23(t)
	disc := lsp.Discover("")
	if disc.Path == "" {
		t.Skip("tinymist not found on PATH or standard install locations")
	}
	tinymistPath := disc.Path

	typx := harness.BuildTypxBinary(t)
	cfgDir := harness.IsolatedConfigDir(t, nil)

	res := harness.RunCLI(t, typx, map[string]string{
		"TYPX_CONFIG_HOME": cfgDir,
	}, "", "--check")
	require.Equal(t, 0, res.ExitCode, "stdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
	assert.Contains(t, res.Stdout, "Tinymist LSP: [OK]")
	assert.Contains(t, res.Stdout, tinymistPath)

	if runtime.GOOS == "windows" {
		listTinymistPIDs := func() map[int]struct{} {
			out, err := exec.Command("powershell", "-NoProfile", "-Command",
				"Get-Process tinymist -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Id").Output()
			pids := make(map[int]struct{})
			if err != nil {
				return pids
			}
			for _, field := range strings.Fields(string(out)) {
				if id, perr := strconv.Atoi(field); perr == nil && id > 0 {
					pids[id] = struct{}{}
				}
			}
			return pids
		}

		// 1. Clean exit via Ctrl+Q leaves zero newly spawned tinymist processes.
		t.Run("clean_exit", func(t *testing.T) {
			baseline := listTinymistPIDs()
			workDir := t.TempDir()
			typFile := filepath.Join(workDir, "live_clean.typ")
			require.NoError(t, os.WriteFile(typFile, []byte("= Live Clean\n\n$alpha + beta$\n"), 0644))

			proc, err := harness.StartConPTY(typx, []string{typFile}, map[string]string{
				"TYPX_CONFIG_HOME": cfgDir,
			}, workDir, 100, 30)
			require.NoError(t, err)
			defer proc.Close()

			var spawnedPID int
			harness.Eventually(t, 10*time.Second, 100*time.Millisecond, func() (bool, string) {
				for pid := range listTinymistPIDs() {
					if _, existed := baseline[pid]; !existed {
						spawnedPID = pid
						return true, ""
					}
				}
				return false, "waiting for live tinymist.exe process to spawn"
			})

			var buf bytes.Buffer
			harness.Eventually(t, 5*time.Second, 50*time.Millisecond, func() (bool, string) {
				buf.Reset()
				buf.WriteString(proc.Output())
				return buf.Len() > 0, "waiting for live ConPTY output"
			})

			require.NoError(t, proc.WriteInput([]byte{0x11}))
			code, err := proc.WaitExit(5 * time.Second)
			require.NoError(t, err)
			assert.Equal(t, uint32(0), code)

			harness.Eventually(t, 5*time.Second, 100*time.Millisecond, func() (bool, string) {
				if !harness.IsPIDAlive(spawnedPID) {
					return true, ""
				}
				return false, fmt.Sprintf("live tinymist PID %d still alive after clean exit", spawnedPID)
			})
		})

		// 2. Force-kill leaves zero newly spawned tinymist processes (Windows Job Object).
		t.Run("force_kill", func(t *testing.T) {
			baseline := listTinymistPIDs()
			workDir := t.TempDir()
			typFile := filepath.Join(workDir, "live_kill.typ")
			require.NoError(t, os.WriteFile(typFile, []byte("= Live Kill\n\n$gamma$\n"), 0644))

			proc, err := harness.StartConPTY(typx, []string{typFile}, map[string]string{
				"TYPX_CONFIG_HOME": cfgDir,
			}, workDir, 100, 30)
			require.NoError(t, err)
			defer proc.Close()

			var spawnedPID int
			harness.Eventually(t, 10*time.Second, 100*time.Millisecond, func() (bool, string) {
				for pid := range listTinymistPIDs() {
					if _, existed := baseline[pid]; !existed {
						spawnedPID = pid
						return true, ""
					}
				}
				return false, "waiting for live tinymist.exe process to spawn"
			})

			require.NoError(t, proc.KillProcess())
			_, _ = proc.WaitExit(5 * time.Second)

			harness.Eventually(t, 5*time.Second, 100*time.Millisecond, func() (bool, string) {
				if !harness.IsPIDAlive(spawnedPID) {
					return true, ""
				}
				return false, fmt.Sprintf("live tinymist PID %d survived force-kill", spawnedPID)
			})
		})
	}
}
