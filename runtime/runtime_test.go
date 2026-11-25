package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAssetDir(t *testing.T) {
	t.Parallel()
	// Test AssetDir
	entries, err := AssetDir("syntax")
	assert.NoError(t, err)
	assert.Contains(t, entries, "typst.yaml")
	assert.Contains(t, entries, "bibtex.yaml")
	assert.True(t, len(entries) > 5)

	helpEntries, err := AssetDir("help")
	assert.NoError(t, err)
	assert.Contains(t, helpEntries, "typst.md")
	assert.Contains(t, helpEntries, "help.md")
	assert.Contains(t, helpEntries, "commands.md")
	assert.Contains(t, helpEntries, "defaultkeys.md")
	assert.Contains(t, helpEntries, "options.md")

	colorEntries, err := AssetDir("colorschemes")
	assert.NoError(t, err)
	assert.Contains(t, colorEntries, "catppuccin-mocha.micro")
	assert.Contains(t, colorEntries, "catppuccin-latte.micro")
	assert.Contains(t, colorEntries, "github-dark.micro")
	assert.Contains(t, colorEntries, "github-light.micro")
	assert.Contains(t, colorEntries, "tokyo-night.micro")
	assert.Contains(t, colorEntries, "nord.micro")
	assert.Contains(t, colorEntries, "rose-pine.micro")
	assert.Contains(t, colorEntries, "rose-pine-dawn.micro")
	assert.Contains(t, colorEntries, "kanagawa.micro")
	assert.Contains(t, colorEntries, "everforest-dark.micro")
	assert.Contains(t, colorEntries, "everforest-light.micro")
	assert.Contains(t, colorEntries, "gruvbox-light.micro")
	assert.Contains(t, colorEntries, "solarized-light.micro")
	assert.NotContains(t, colorEntries, "typx-dark.micro")
	assert.NotContains(t, colorEntries, "typx-light.micro")

	_, err = AssetDir("templates")
	assert.Error(t, err)

	data, err := Asset("help/typst.md")
	assert.NoError(t, err)
	assert.Contains(t, string(data), "TypX")

	prefixedData, err := Asset("runtime/help/typst.md")
	assert.NoError(t, err)
	assert.Equal(t, data, prefixedData)
}
