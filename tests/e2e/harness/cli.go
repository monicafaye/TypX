package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	typxBinOnce sync.Once
	typxBinPath string
	typxBinErr  error

	mockBinOnce sync.Once
	mockBinPath string
	mockBinErr  error
)

// RepoRoot returns the TypX repository root directory by locating go.mod.
func RepoRoot() (string, error) {
	if _, file, _, ok := runtime.Caller(0); ok {
		dir := filepath.Dir(file)
		for {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not locate go.mod from working directory")
		}
		dir = parent
	}
}

// BuildTypxBinary builds ./cmd/typx once per test binary process into a temp dir.
func BuildTypxBinary(t testing.TB) string {
	t.Helper()
	typxBinOnce.Do(func() {
		root, err := RepoRoot()
		if err != nil {
			typxBinErr = err
			return
		}
		dir, err := os.MkdirTemp("", "typx-e2e-bin-*")
		if err != nil {
			typxBinErr = err
			return
		}
		name := "typx"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		out := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", out, "./cmd/typx")
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			typxBinErr = errors.New("go build ./cmd/typx failed: " + err.Error() + ": " + stderr.String())
			return
		}
		typxBinPath = out
	})
	if typxBinErr != nil {
		t.Fatalf("BuildTypxBinary: %v", typxBinErr)
	}
	return typxBinPath
}

// BuildMockTinymist builds ./tests/e2e/cmd/mocktinymist once per test binary process.
func BuildMockTinymist(t testing.TB) string {
	t.Helper()
	mockBinOnce.Do(func() {
		root, err := RepoRoot()
		if err != nil {
			mockBinErr = err
			return
		}
		dir, err := os.MkdirTemp("", "typx-e2e-mock-*")
		if err != nil {
			mockBinErr = err
			return
		}
		name := "mock-tinymist"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		out := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", out, "./tests/e2e/cmd/mocktinymist")
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			mockBinErr = errors.New("go build ./tests/e2e/cmd/mocktinymist failed: " + err.Error() + ": " + stderr.String())
			return
		}
		mockBinPath = out
	})
	if mockBinErr != nil {
		t.Fatalf("BuildMockTinymist: %v", mockBinErr)
	}
	return mockBinPath
}

// IsolatedConfigDir creates a fresh temporary config directory and optionally writes settings.json.
func IsolatedConfigDir(t testing.TB, settings map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	if settings != nil {
		data, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			t.Fatalf("marshal settings.json: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0644); err != nil {
			t.Fatalf("write settings.json: %v", err)
		}
	}
	return dir
}

// CLIResult captures the result of executing a CLI subprocess.
type CLIResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
	Err      error
}

// MergeEnv builds a clean environment slice from os.Environ() overlaid with overrides.
// Keys mapped to "" in overrides remove any inherited variable with that key.
func MergeEnv(overrides map[string]string) []string {
	envMap := make(map[string]string)
	var order []string
	for _, entry := range os.Environ() {
		eq := strings.IndexByte(entry, '=')
		if eq <= 0 {
			continue
		}
		k := entry[:eq]
		v := entry[eq+1:]
		if _, seen := envMap[k]; !seen {
			order = append(order, k)
		}
		envMap[k] = v
	}
	for k, v := range overrides {
		if v == "" {
			delete(envMap, k)
			continue
		}
		if _, seen := envMap[k]; !seen {
			order = append(order, k)
		}
		envMap[k] = v
	}
	out := make([]string, 0, len(envMap))
	for _, k := range order {
		if v, ok := envMap[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// RunCLI executes bin with the provided env overrides, stdin, and args.
func RunCLI(t testing.TB, bin string, env map[string]string, stdin string, args ...string) CLIResult {
	t.Helper()
	return runCLIWithDetached(t, bin, env, stdin, false, args...)
}

// RunCLIDetached executes bin detached from any parent console/TTY.
func RunCLIDetached(t testing.TB, bin string, env map[string]string, stdin string, args ...string) CLIResult {
	t.Helper()
	return runCLIWithDetached(t, bin, env, stdin, true, args...)
}

func runCLIWithDetached(t testing.TB, bin string, env map[string]string, stdin string, detached bool, args ...string) CLIResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = MergeEnv(env)
	if detached {
		cmd.SysProcAttr = detachedSysProcAttr()
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return CLIResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded),
		Err:      err,
	}
}

// Eventually polls cond every interval until it returns (true, _) or timeout expires.
func Eventually(t testing.TB, timeout, interval time.Duration, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastReason string
	for {
		ok, reason := cond()
		if ok {
			return
		}
		lastReason = reason
		if time.Now().After(deadline) {
			t.Fatalf("Eventually timed out after %v: %s", timeout, lastReason)
		}
		time.Sleep(interval)
	}
}
