package lsp

import (
	"errors"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

var openURLFunc = openURLOS

var (
	reHTTPURL     = regexp.MustCompile(`https?://[^\s"'<>]+`)
	reStaticHost  = regexp.MustCompile(`(?i)(?:static file server|preview server).*?(?:listening on:\s*|at\s+)((?:127\.0\.0\.1|localhost|\[::1\]|0\.0\.0\.0):\d+)`)
	reControlLine = regexp.MustCompile(`(?i)control panel server listening on:`)
)

// ValidatePreviewURL validates that raw is an http or https URL with a non-empty
// host and no whitespace or control characters.
func ValidatePreviewURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("empty preview URL")
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] < 0x20 || trimmed[i] == 0x7f || trimmed[i] == ' ' || trimmed[i] == '\t' {
			return "", errors.New("preview URL contains invalid control or whitespace character")
		}
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("preview URL scheme must be http or https")
	}
	if u.Host == "" {
		return "", errors.New("preview URL host is empty")
	}
	return u.String(), nil
}

// OpenURL validates raw and opens it in the user's default browser without
// invoking a shell (uses rundll32 on Windows, open on macOS, xdg-open on Linux).
func OpenURL(raw string) error {
	safeURL, err := ValidatePreviewURL(raw)
	if err != nil {
		return err
	}
	return openURLFunc(safeURL)
}

func openURLOS(safeURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", safeURL)
		cmd.SysProcAttr = hideWindowSysProcAttr()
	case "darwin":
		cmd = exec.Command("open", safeURL)
	default:
		cmd = exec.Command("xdg-open", safeURL)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}

// ExtractPreviewURL inspects a tinymist preview log line or JSON snippet and
// returns a validated preview URL and priority (2 for explicit http/https URL,
// 1 for static/preview server host:port), ignoring control-plane server lines.
func ExtractPreviewURL(line string) (string, int, bool) {
	if reControlLine.MatchString(line) {
		return "", 0, false
	}
	if m := reHTTPURL.FindString(line); m != "" {
		m = strings.TrimRight(m, ".,;)")
		if valid, err := ValidatePreviewURL(m); err == nil {
			return valid, 2, true
		}
	}
	if m := reStaticHost.FindStringSubmatch(line); len(m) == 2 {
		hostPort := strings.Replace(m[1], "0.0.0.0:", "127.0.0.1:", 1)
		if valid, err := ValidatePreviewURL("http://" + hostPort); err == nil {
			return valid, 1, true
		}
	}
	return "", 0, false
}
