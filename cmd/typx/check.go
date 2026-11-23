package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/lsp"
	"github.com/monicafaye/TypX/internal/typst/preview"
	"github.com/monicafaye/TypX/internal/util"
)

func checkUnicodeCapability() (bool, string) {
	if runtime.GOOS == "windows" {
		if os.Getenv("WT_SESSION") != "" {
			return true, "Windows Terminal"
		}
		if tp := os.Getenv("TERM_PROGRAM"); tp != "" {
			return true, tp
		}
		if os.Getenv("ConEmuPID") != "" {
			return true, "ConEmu"
		}
		return false, "legacy Windows console (Windows Terminal recommended for full math Unicode)"
	}

	for _, env := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		val := os.Getenv(env)
		if val == "" {
			continue
		}
		lower := strings.ToLower(val)
		if strings.Contains(lower, "utf-8") || strings.Contains(lower, "utf8") {
			return true, fmt.Sprintf("%s=%s", env, val)
		}
		return false, fmt.Sprintf("%s=%s (UTF-8 locale recommended)", env, val)
	}
	return false, "no UTF-8 locale detected (set LANG=en_US.UTF-8)"
}

// runCheck verifies the TypX runtime environment and writes a diagnostic report to w.
// It returns 0 on success (including advisory warnings such as missing tinymist)
// and 1 on hard environment failures.
func runCheck(w io.Writer, configDirErr, settingsErr error) int {
	failed := false

	fmt.Fprintf(w, "TypX %s (based on micro %s)\n", util.TypXVersion, util.MicroBaseVersion)

	if configDirErr != nil {
		fmt.Fprintf(w, "Config dir:   [FAIL] %s (%v)\n", config.ConfigDir, configDirErr)
		failed = true
	} else {
		tmp, err := os.CreateTemp(config.ConfigDir, ".typx-check-*")
		if err != nil {
			fmt.Fprintf(w, "Config dir:   [FAIL] %s (not writable: %v)\n", config.ConfigDir, err)
			failed = true
		} else {
			name := tmp.Name()
			tmp.Close()
			os.Remove(name)
			fmt.Fprintf(w, "Config dir:   [OK] %s (writable)\n", config.ConfigDir)
		}
	}

	if settingsErr != nil {
		fmt.Fprintf(w, "Settings:     [FAIL] %v\n", settingsErr)
		failed = true
	} else {
		fmt.Fprintln(w, "Settings:     [OK]")
	}

	if config.FindRuntimeFile(config.RTSyntax, "typst") != nil {
		fmt.Fprintln(w, "Typst syntax: [OK] embedded")
	} else {
		fmt.Fprintln(w, "Typst syntax: [FAIL] embedded typst.yaml not found")
		failed = true
	}

	if ok, detail := checkUnicodeCapability(); ok {
		fmt.Fprintf(w, "Unicode:      [OK] %s\n", detail)
	} else {
		fmt.Fprintf(w, "Unicode:      [WARN] %s\n", detail)
	}

	if !lsp.Check(w) {
		failed = true
	}
	preview.Check(w)

	if failed {
		return 1
	}
	return 0
}
