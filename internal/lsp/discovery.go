package lsp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const probeTimeout = 2 * time.Second

// DiscoveryResult describes the outcome of locating the tinymist binary.
type DiscoveryResult struct {
	Path              string
	Version           string
	Source            string
	ConfiguredMissing bool
	Err               error
}

type discoverDeps struct {
	goos        string
	lookPath    func(string) (string, error)
	stat        func(string) (os.FileInfo, error)
	userHomeDir func() (string, error)
	getenv      func(string) string
	glob        func(string) ([]string, error)
}

func defaultDiscoverDeps() discoverDeps {
	return discoverDeps{
		goos:        runtime.GOOS,
		lookPath:    exec.LookPath,
		stat:        os.Stat,
		userHomeDir: os.UserHomeDir,
		getenv:      os.Getenv,
		glob:        filepath.Glob,
	}
}

// Discover locates the tinymist binary according to TypX precedence:
// 1. Explicit configuredPath (typst.tinymistPath)
// 2. PATH via exec.LookPath("tinymist")
// 3. OS-specific installation paths (Cargo, WinGet, Scoop, Homebrew, ~/.local/bin, VS Code extensions)
//
// If configuredPath is non-empty and invalid, Discover reports ConfiguredMissing: true
// without silently falling back to PATH or OS candidate paths.
func Discover(configuredPath string) DiscoveryResult {
	return discoverWithDeps(configuredPath, defaultDiscoverDeps())
}

// DiscoverWithProbe locates tinymist and runs a 2-second version probe if found.
func DiscoverWithProbe(ctx context.Context, configuredPath string) DiscoveryResult {
	res := Discover(configuredPath)
	if res.Path != "" {
		ver, err := ProbeVersion(ctx, res.Path)
		if err != nil {
			res.Err = err
		} else {
			res.Version = ver
		}
	}
	return res
}

func discoverWithDeps(configuredPath string, deps discoverDeps) DiscoveryResult {
	home, _ := deps.userHomeDir()
	configuredPath = strings.TrimSpace(configuredPath)

	if configuredPath != "" {
		expanded := expandHomePath(configuredPath, home)
		if isUsableBinary(expanded, deps.goos, deps.stat) {
			return DiscoveryResult{
				Path:   expanded,
				Source: "config",
			}
		}
		return DiscoveryResult{
			ConfiguredMissing: true,
			Err:               fmt.Errorf("configured typst.tinymistPath not found or not executable: %s", expanded),
		}
	}

	if p, err := deps.lookPath("tinymist"); err == nil && p != "" {
		return DiscoveryResult{
			Path:   p,
			Source: "PATH",
		}
	}

	candidates := osCandidatePaths(deps.goos, home, deps.getenv, deps.glob)
	for _, cand := range candidates {
		if isUsableBinary(cand.path, deps.goos, deps.stat) {
			return DiscoveryResult{
				Path:   cand.path,
				Source: cand.source,
			}
		}
	}

	return DiscoveryResult{
		Err: errors.New("tinymist binary not found on PATH or standard install locations"),
	}
}

type candidatePath struct {
	path   string
	source string
}

func osCandidatePaths(goos, home string, getenv func(string) string, glob func(string) ([]string, error)) []candidatePath {
	var out []candidatePath
	join := filepath.Join
	if goos == "windows" {
		join = joinWindows
	} else {
		join = joinPosix
	}

	switch goos {
	case "windows":
		userProfile := home
		if userProfile == "" {
			userProfile = getenv("USERPROFILE")
		}
		localAppData := getenv("LOCALAPPDATA")
		if localAppData == "" && userProfile != "" {
			localAppData = join(userProfile, "AppData", "Local")
		}
		if userProfile != "" {
			out = append(out, candidatePath{
				path:   join(userProfile, ".cargo", "bin", "tinymist.exe"),
				source: "cargo",
			})
		}
		if localAppData != "" {
			out = append(out, candidatePath{
				path:   join(localAppData, "Microsoft", "WinGet", "Links", "tinymist.exe"),
				source: "winget",
			})
		}
		if userProfile != "" {
			out = append(out, candidatePath{
				path:   join(userProfile, "scoop", "shims", "tinymist.exe"),
				source: "scoop",
			})
			out = append(out, vscodeCandidates(userProfile, "tinymist.exe", join, glob)...)
		}

	case "darwin":
		out = append(out,
			candidatePath{path: "/opt/homebrew/bin/tinymist", source: "homebrew"},
			candidatePath{path: "/usr/local/bin/tinymist", source: "homebrew"},
		)
		if home != "" {
			out = append(out,
				candidatePath{path: join(home, ".cargo", "bin", "tinymist"), source: "cargo"},
				candidatePath{path: join(home, ".local", "bin", "tinymist"), source: "local-bin"},
			)
			out = append(out, vscodeCandidates(home, "tinymist", join, glob)...)
		}

	default:
		if home != "" {
			out = append(out,
				candidatePath{path: join(home, ".cargo", "bin", "tinymist"), source: "cargo"},
				candidatePath{path: join(home, ".local", "bin", "tinymist"), source: "local-bin"},
			)
		}
		out = append(out,
			candidatePath{path: "/usr/local/bin/tinymist", source: "system"},
			candidatePath{path: "/usr/bin/tinymist", source: "system"},
		)
		if home != "" {
			out = append(out, vscodeCandidates(home, "tinymist", join, glob)...)
		}
	}

	return out
}

func vscodeCandidates(home, binName string, join func(...string) string, glob func(string) ([]string, error)) []candidatePath {
	if glob == nil || home == "" {
		return nil
	}
	extRoots := []string{
		join(home, ".vscode", "extensions"),
		join(home, ".vscode-insiders", "extensions"),
	}
	var matches []string
	for _, root := range extRoots {
		for _, sub := range []string{"out", "bin"} {
			pattern := join(root, "myriad-dreamin.tinymist-*", sub, binName)
			found, err := glob(pattern)
			if err == nil && len(found) > 0 {
				matches = append(matches, found...)
			}
		}
	}
	if len(matches) == 0 {
		return nil
	}
	sort.Slice(matches, func(i, j int) bool {
		return compareVSCodeTinymistDesc(matches[i], matches[j])
	})
	out := make([]candidatePath, 0, len(matches))
	for _, m := range matches {
		out = append(out, candidatePath{path: m, source: "vscode"})
	}
	return out
}

func compareVSCodeTinymistDesc(a, b string) bool {
	va := extractTinymistExtVersion(a)
	vb := extractTinymistExtVersion(b)
	if len(va) > 0 && len(vb) > 0 {
		n := len(va)
		if len(vb) > n {
			n = len(vb)
		}
		for i := 0; i < n; i++ {
			var ai, bi int
			if i < len(va) {
				ai = va[i]
			}
			if i < len(vb) {
				bi = vb[i]
			}
			if ai != bi {
				return ai > bi
			}
		}
	}
	return a > b
}

func extractTinymistExtVersion(p string) []int {
	const prefix = "myriad-dreamin.tinymist-"
	slashed := strings.ReplaceAll(p, "\\", "/")
	idx := strings.LastIndex(slashed, prefix)
	if idx < 0 {
		return nil
	}
	rest := slashed[idx+len(prefix):]
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		rest = rest[:slash]
	}
	// Strip optional platform suffix like -win32-x64 if present after version.
	if dash := strings.IndexByte(rest, '-'); dash >= 0 {
		rest = rest[:dash]
	}
	parts := strings.Split(rest, ".")
	nums := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil
		}
		nums = append(nums, n)
	}
	return nums
}

func expandHomePath(p, home string) string {
	if p == "" || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func isUsableBinary(p, goos string, stat func(string) (os.FileInfo, error)) bool {
	if p == "" {
		return false
	}
	info, err := stat(p)
	if err != nil || info.IsDir() {
		return false
	}
	if goos != "windows" && info.Mode().IsRegular() {
		if info.Mode()&0111 == 0 {
			return false
		}
	}
	return true
}

func joinWindows(elem ...string) string {
	cleaned := make([]string, 0, len(elem))
	for _, e := range elem {
		e = strings.TrimRight(e, "\\/")
		if e != "" {
			cleaned = append(cleaned, e)
		}
	}
	return strings.Join(cleaned, "\\")
}

func joinPosix(elem ...string) string {
	var parts []string
	leadingSlash := len(elem) > 0 && strings.HasPrefix(elem[0], "/")
	for _, e := range elem {
		e = strings.Trim(e, "/")
		if e != "" {
			parts = append(parts, e)
		}
	}
	res := strings.Join(parts, "/")
	if leadingSlash {
		return "/" + res
	}
	return res
}

// ProbeVersion runs `<binPath> --version` with a strict 2-second timeout.
func ProbeVersion(ctx context.Context, binPath string) (string, error) {
	if binPath == "" {
		return "", errors.New("empty tinymist path")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	cmd := exec.Command(binPath, "--version")
	cmd.SysProcAttr = hideWindowSysProcAttr()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Start(); err != nil {
		return "", err
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		out := strings.TrimSpace(buf.String())
		if idx := strings.IndexAny(out, "\r\n"); idx >= 0 {
			out = strings.TrimSpace(out[:idx])
		}
		if err != nil {
			if out != "" {
				return "", fmt.Errorf("%w: %s", err, out)
			}
			return "", err
		}
		return out, nil
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return "", fmt.Errorf("version probe timed out: %w", ctx.Err())
	}
}

// InstallHints returns OS-specific installation instructions for tinymist.
func InstallHints(goos string) []string {
	switch goos {
	case "windows":
		return []string{
			"winget install Myriad-Dreamin.Tinymist",
			"scoop install tinymist",
			"cargo install --locked tinymist",
			"Or set \"typst.tinymistPath\" in TypX settings.json",
		}
	case "darwin":
		return []string{
			"brew install tinymist",
			"cargo install --locked tinymist",
			"Or set \"typst.tinymistPath\" in TypX settings.json",
		}
	default:
		return []string{
			"cargo install --locked tinymist",
			"Download prebuilt binary from https://github.com/Myriad-Dreamin/tinymist/releases",
			"Or set \"typst.tinymistPath\" in TypX settings.json",
		}
	}
}
