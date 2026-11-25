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
	assert.Contains(t, colorEntries, "typx-dark.micro")
	assert.Contains(t, colorEntries, "typx-light.micro")

	templateEntries, err := AssetDir("templates")
	assert.NoError(t, err)
	assert.Contains(t, templateEntries, "article.typ")
	assert.Contains(t, templateEntries, "notes.typ")
	assert.Contains(t, templateEntries, "slides.typ")

	data, err := Asset("templates/article.typ")
	assert.NoError(t, err)
	assert.Contains(t, string(data), "{{title}}")

	prefixedData, err := Asset("runtime/templates/article.typ")
	assert.NoError(t, err)
	assert.Equal(t, data, prefixedData)
}
