package lsp

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/monicafaye/TypX/internal/config"
)

type fakeFileInfo struct {
	name string
	dir  bool
	mode fs.FileMode
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 100 }
func (f fakeFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) Sys() any           { return nil }

func TestDiscoverPrecedenceAndConfiguredMissing(t *testing.T) {
	tmp := t.TempDir()
	customBin := filepath.Join(tmp, "custom-tinymist")
	require.NoError(t, os.WriteFile(customBin, []byte("bin"), 0755))

	// 1. Explicit configured path wins over PATH.
	res := discoverWithDeps(customBin, discoverDeps{
		goos:     "windows",
		lookPath: func(string) (string, error) { return `C:\path\tinymist.exe`, nil },
		stat:     os.Stat,
		userHomeDir: func() (string, error) {
			return tmp, nil
		},
		getenv: func(string) string { return "" },
	})
	assert.Equal(t, customBin, res.Path)
	assert.Equal(t, "config", res.Source)
	assert.False(t, res.ConfiguredMissing)

	// 2. Invalid explicit configured path reports ConfiguredMissing without falling back to PATH.
	missingCfg := filepath.Join(tmp, "nonexistent-tinymist")
	res = discoverWithDeps(missingCfg, discoverDeps{
		goos:     "windows",
		lookPath: func(string) (string, error) { return `C:\path\tinymist.exe`, nil },
		stat:     os.Stat,
		userHomeDir: func() (string, error) {
			return tmp, nil
		},
		getenv: func(string) string { return "" },
	})
	assert.Empty(t, res.Path)
	assert.True(t, res.ConfiguredMissing)
	require.Error(t, res.Err)

	// 3. PATH wins over OS candidate paths when configuredPath is empty.
	res = discoverWithDeps("", discoverDeps{
		goos:     "linux",
		lookPath: func(string) (string, error) { return "/usr/bin/tinymist", nil },
		stat:     os.Stat,
		userHomeDir: func() (string, error) {
			return "/home/user", nil
		},
		getenv: func(string) string { return "" },
	})
	assert.Equal(t, "/usr/bin/tinymist", res.Path)
	assert.Equal(t, "PATH", res.Source)
}

func TestDiscoverOSCandidatesAndVSCodeSorting(t *testing.T) {
	// Windows WinGet + VS Code descending sort (0.12.10 > 0.12.2)
	existing := map[string]bool{
		`C:\Users\Alice\.vscode\extensions\myriad-dreamin.tinymist-0.12.2\out\tinymist.exe`:  true,
		`C:\Users\Alice\.vscode\extensions\myriad-dreamin.tinymist-0.12.10\out\tinymist.exe`: true,
	}
	res := discoverWithDeps("", discoverDeps{
		goos:     "windows",
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
		stat: func(p string) (os.FileInfo, error) {
			if existing[p] {
				return fakeFileInfo{name: "tinymist.exe", mode: 0755}, nil
			}
			return nil, os.ErrNotExist
		},
		userHomeDir: func() (string, error) { return `C:\Users\Alice`, nil },
		getenv: func(k string) string {
			if k == "LOCALAPPDATA" {
				return `C:\Users\Alice\AppData\Local`
			}
			return ""
		},
		glob: func(pattern string) ([]string, error) {
			if pattern == `C:\Users\Alice\.vscode\extensions\myriad-dreamin.tinymist-*\out\tinymist.exe` {
				return []string{
					`C:\Users\Alice\.vscode\extensions\myriad-dreamin.tinymist-0.12.2\out\tinymist.exe`,
					`C:\Users\Alice\.vscode\extensions\myriad-dreamin.tinymist-0.12.10\out\tinymist.exe`,
				}, nil
			}
			return nil, nil
		},
	})
	assert.Equal(t, `C:\Users\Alice\.vscode\extensions\myriad-dreamin.tinymist-0.12.10\out\tinymist.exe`, res.Path)
	assert.Equal(t, "vscode", res.Source)

	// Windows ~/.local/bin (tinymist-installer.ps1)
	res = discoverWithDeps("", discoverDeps{
		goos:     "windows",
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
		stat: func(p string) (os.FileInfo, error) {
			if p == `C:\Users\Alice\.local\bin\tinymist.exe` {
				return fakeFileInfo{name: "tinymist.exe", mode: 0755}, nil
			}
			return nil, os.ErrNotExist
		},
		userHomeDir: func() (string, error) { return `C:\Users\Alice`, nil },
		getenv:      func(string) string { return "" },
	})
	assert.Equal(t, `C:\Users\Alice\.local\bin\tinymist.exe`, res.Path)
	assert.Equal(t, "local-bin", res.Source)

	// macOS Homebrew
	res = discoverWithDeps("", discoverDeps{
		goos:     "darwin",
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
		stat: func(p string) (os.FileInfo, error) {
			if p == "/opt/homebrew/bin/tinymist" {
				return fakeFileInfo{name: "tinymist", mode: 0755}, nil
			}
			return nil, os.ErrNotExist
		},
		userHomeDir: func() (string, error) { return "/Users/alice", nil },
		getenv:      func(string) string { return "" },
	})
	assert.Equal(t, "/opt/homebrew/bin/tinymist", res.Path)
	assert.Equal(t, "homebrew", res.Source)

	// Linux ~/.local/bin (skips non-executable 0644 file)
	res = discoverWithDeps("", discoverDeps{
		goos:     "linux",
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
		stat: func(p string) (os.FileInfo, error) {
			if p == "/home/alice/.cargo/bin/tinymist" {
				return fakeFileInfo{name: "tinymist", mode: 0644}, nil // not executable
			}
			if p == "/home/alice/.local/bin/tinymist" {
				return fakeFileInfo{name: "tinymist", mode: 0755}, nil
			}
			return nil, os.ErrNotExist
		},
		userHomeDir: func() (string, error) { return "/home/alice", nil },
		getenv:      func(string) string { return "" },
	})
	assert.Equal(t, "/home/alice/.local/bin/tinymist", res.Path)
	assert.Equal(t, "local-bin", res.Source)
}

func TestInstallHintsPerOS(t *testing.T) {
	assert.Contains(t, InstallHints("windows")[0], "winget")
	assert.Contains(t, InstallHints("darwin")[0], "brew")
	assert.Contains(t, InstallHints("linux")[0], "cargo")
}

func TestProbeVersionSuccessAnd2sTimeout(t *testing.T) {
	t.Setenv("TYPX_LSP_HELPER", "version_ok")
	ver, err := ProbeVersion(context.Background(), os.Args[0])
	require.NoError(t, err)
	assert.Equal(t, "tinymist 0.13.0 (test)", ver)

	t.Setenv("TYPX_LSP_HELPER", "version_hang")
	start := time.Now()
	_, err = ProbeVersion(context.Background(), os.Args[0])
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.True(t, elapsed < 3500*time.Millisecond, "elapsed=%v should be < 3.5s", elapsed)
}

func TestCheckOutputStates(t *testing.T) {
	origSettings := config.GlobalSettings
	t.Cleanup(func() { config.GlobalSettings = origSettings })

	// Disabled
	config.GlobalSettings = map[string]any{
		"typst.lspEnabled":   false,
		"typst.tinymistPath": "",
	}
	var buf bytes.Buffer
	assert.True(t, Check(&buf))
	assert.Contains(t, buf.String(), "Tinymist LSP: [INFO] disabled")

	// Configured missing
	buf.Reset()
	config.GlobalSettings = map[string]any{
		"typst.lspEnabled":   true,
		"typst.tinymistPath": filepath.Join(t.TempDir(), "missing-tinymist"),
	}
	assert.True(t, Check(&buf))
	assert.Contains(t, buf.String(), "Tinymist LSP: [WARN] configured typst.tinymistPath not found")
	assert.Contains(t, buf.String(), "hint:")

	// Configured valid (using helper binary)
	buf.Reset()
	t.Setenv("TYPX_LSP_HELPER", "version_ok")
	config.GlobalSettings = map[string]any{
		"typst.lspEnabled":   true,
		"typst.tinymistPath": os.Args[0],
	}
	assert.True(t, Check(&buf))
	assert.Contains(t, buf.String(), "Tinymist LSP: [OK]")
	assert.Contains(t, buf.String(), "tinymist 0.13.0 (test)")
}
