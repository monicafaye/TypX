package templates

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTypstTOML(t *testing.T) {
	t.Parallel()

	raw := "\xef\xbb\xbf" + `
# Package manifest
[package]
name = "charged-ieee"
version = "0.1.4"
entrypoint = "lib.typ"
description = "An IEEE template for Typst."
categories = [
  "paper",
  "report", # inline comment
]

[template]
path = "template"
entrypoint = "main.typ"
thumbnail = 'thumbnail.png'

[tool.mytool]
name = "should-not-overwrite-package-name"
`
	man, err := ParseTypstTOML([]byte(raw))
	require.NoError(t, err)
	assert.True(t, man.HasTemplate())
	assert.Equal(t, "charged-ieee", man.Package.Name)
	assert.Equal(t, "0.1.4", man.Package.Version)
	assert.Equal(t, "lib.typ", man.Package.Entrypoint)
	assert.Equal(t, "An IEEE template for Typst.", man.Package.Description)
	assert.Equal(t, []string{"paper", "report"}, man.Package.Categories)
	assert.Equal(t, "template", man.Template.Path)
	assert.Equal(t, "main.typ", man.Template.Entrypoint)
	assert.Equal(t, "thumbnail.png", man.Template.Thumbnail)
}

func TestParseTypstTOMLMissingTemplateSection(t *testing.T) {
	t.Parallel()

	raw := `
[package]
name = "cetz"
version = "0.3.2"
entrypoint = "src/lib.typ"
`
	man, err := ParseTypstTOML([]byte(raw))
	require.NoError(t, err)
	assert.False(t, man.HasTemplate())
	assert.Equal(t, "cetz", man.Package.Name)
}
