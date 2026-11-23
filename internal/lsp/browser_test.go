package lsp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePreviewURL(t *testing.T) {
	u, err := ValidatePreviewURL("  http://127.0.0.1:23625/  ")
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:23625/", u)

	u, err = ValidatePreviewURL("https://localhost:8443/preview?doc=a")
	require.NoError(t, err)
	assert.Equal(t, "https://localhost:8443/preview?doc=a", u)

	invalid := []string{
		"",
		"   ",
		"javascript:alert(1)",
		"file:///C:/Windows/System32/calc.exe",
		"http://127.0.0.1:8080 & calc.exe",
		"http://127.0.0.1:8080\ncalc",
		"http:///nohost",
	}
	for _, raw := range invalid {
		_, err := ValidatePreviewURL(raw)
		assert.Error(t, err, "expected error for %q", raw)
	}
}

func TestExtractPreviewURL(t *testing.T) {
	// Control panel line must be ignored
	_, _, ok := ExtractPreviewURL("Control panel server listening on: 127.0.0.1:23624")
	assert.False(t, ok)

	// Static file server host:port extracted as http://
	u, prio, ok := ExtractPreviewURL("Static file server listening on: 127.0.0.1:23625")
	assert.True(t, ok)
	assert.Equal(t, 1, prio)
	assert.Equal(t, "http://127.0.0.1:23625", u)

	// Explicit http URL gets priority 2
	u, prio, ok = ExtractPreviewURL(`Preview server listening at http://localhost:23630/index.html`)
	assert.True(t, ok)
	assert.Equal(t, 2, prio)
	assert.Equal(t, "http://localhost:23630/index.html", u)
}

func TestOpenURLUsesInjectableOpenerAndRejectsUnsafe(t *testing.T) {
	orig := openURLFunc
	t.Cleanup(func() { openURLFunc = orig })

	var opened []string
	openURLFunc = func(safe string) error {
		opened = append(opened, safe)
		return nil
	}

	require.NoError(t, OpenURL("http://127.0.0.1:23625"))
	assert.Equal(t, []string{"http://127.0.0.1:23625"}, opened)

	err := OpenURL("file:///etc/passwd")
	require.Error(t, err)
	assert.Len(t, opened, 1)
}
