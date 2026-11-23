package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTypstSettingsDefaultsAndValidation(t *testing.T) {
	all := DefaultAllSettings()
	common := DefaultCommonSettings()

	// Common (per-buffer) Typst settings
	assert.Equal(t, true, common["typst.conceal"])
	assert.Equal(t, "line", common["typst.concealMode"])
	assert.Equal(t, true, common["typst.smartPairs"])

	// Global-only Typst settings
	assert.Equal(t, "", DefaultGlobalOnlySettings["typst.tinymistPath"])
	assert.Equal(t, true, DefaultGlobalOnlySettings["typst.lspEnabled"])
	assert.Equal(t, float64(150), DefaultGlobalOnlySettings["typst.debounceMs"])
	assert.Equal(t, true, DefaultGlobalOnlySettings["typst.previewOpenBrowser"])
	assert.Equal(t, float64(30), DefaultGlobalOnlySettings["typst.outlineWidth"])
	assert.Equal(t, false, DefaultGlobalOnlySettings["typst.outlineShowLabels"])

	// All present in DefaultAllSettings
	for _, key := range []string{
		"typst.conceal",
		"typst.concealMode",
		"typst.smartPairs",
		"typst.tinymistPath",
		"typst.lspEnabled",
		"typst.debounceMs",
		"typst.previewOpenBrowser",
		"typst.outlineWidth",
		"typst.outlineShowLabels",
	} {
		_, ok := all[key]
		assert.True(t, ok, "missing setting %s", key)
	}

	// Validators
	require.NoError(t, OptionIsValid("typst.concealMode", "line"))
	require.NoError(t, OptionIsValid("typst.concealMode", "token"))
	assert.Error(t, OptionIsValid("typst.concealMode", "always"))

	require.NoError(t, OptionIsValid("typst.debounceMs", float64(0)))
	require.NoError(t, OptionIsValid("typst.debounceMs", float64(150)))
	assert.Error(t, OptionIsValid("typst.debounceMs", float64(-1)))

	require.NoError(t, OptionIsValid("typst.outlineWidth", float64(30)))
	assert.Error(t, OptionIsValid("typst.outlineWidth", float64(0)))
	assert.Error(t, OptionIsValid("typst.outlineWidth", float64(-5)))
}

func TestTypstFileTypeDefaults(t *testing.T) {
	origParsed := parsedSettings
	origGlobal := GlobalSettings
	origVolatile := VolatileSettings
	origModified := ModifiedSettings
	t.Cleanup(func() {
		parsedSettings = origParsed
		GlobalSettings = origGlobal
		VolatileSettings = origVolatile
		ModifiedSettings = origModified
	})

	t.Run("applies typst defaults when user has not overridden", func(t *testing.T) {
		parsedSettings = map[string]any{}
		VolatileSettings = map[string]bool{}
		ModifiedSettings = map[string]bool{}
		GlobalSettings = DefaultAllSettings()

		s := DefaultCommonSettings()
		UpdateFileTypeLocals(s, "typst")

		assert.Equal(t, true, s["softwrap"])
		assert.Equal(t, true, s["wordwrap"])
		assert.Equal(t, true, s["tabstospaces"])
		assert.Equal(t, float64(2), s["tabsize"])
		assert.Equal(t, true, s["autoindent"])
		assert.Equal(t, true, s["ruler"])
		assert.Contains(t, s["statusformatl"].(string), "$(typst.mode)")
	})

	t.Run("does not apply typst defaults to non-typst filetypes", func(t *testing.T) {
		parsedSettings = map[string]any{}
		VolatileSettings = map[string]bool{}
		ModifiedSettings = map[string]bool{}
		GlobalSettings = DefaultAllSettings()

		s := DefaultCommonSettings()
		UpdateFileTypeLocals(s, "markdown")

		assert.Equal(t, false, s["softwrap"])
		assert.Equal(t, float64(4), s["tabsize"])
	})

	t.Run("explicit user global, volatile, and ft:typst settings win", func(t *testing.T) {
		parsedSettings = map[string]any{
			"tabsize": float64(4),
			"ft:typst": map[string]any{
				"wordwrap": false,
			},
		}
		VolatileSettings = map[string]bool{
			"softwrap": true,
		}
		ModifiedSettings = map[string]bool{}
		GlobalSettings = DefaultAllSettings()
		GlobalSettings["tabsize"] = float64(4)
		GlobalSettings["softwrap"] = false

		s := DefaultCommonSettings()
		s["tabsize"] = float64(4)
		s["softwrap"] = false
		UpdateFileTypeLocals(s, "typst")

		assert.Equal(t, float64(4), s["tabsize"], "user global setting must win over built-in ft default")
		assert.Equal(t, false, s["softwrap"], "CLI volatile setting must win over built-in ft default")
		assert.Equal(t, false, s["wordwrap"], "user ft:typst setting must win over built-in ft default")
		assert.Equal(t, true, s["tabstospaces"], "unset key still gets built-in typst default")
	})
}
