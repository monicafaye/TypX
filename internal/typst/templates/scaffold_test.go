package templates

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCommentMetadataAndApplyVars(t *testing.T) {
	t.Parallel()

	src := "// @title: Study Notes\r\n// @desc: Clean study notes\r\n// @category: Notes\r\n\r\n= {{title}}\r\nBy {{author}} on {{date}}\r\n"
	title, desc, cat, body := ParseCommentMetadata(src)
	assert.Equal(t, "Study Notes", title)
	assert.Equal(t, "Clean study notes", desc)
	assert.Equal(t, "Notes", cat)
	assert.Equal(t, "= {{title}}\nBy {{author}} on {{date}}\n", body)

	rendered := ApplyVars(body, TemplateVars{
		Title:  "Linear Algebra",
		Author: "Ada",
		Date:   "2026-11-25",
	})
	assert.Equal(t, "= Linear Algebra\nBy Ada on 2026-11-25\n", rendered)
}

func TestExtractPreamble(t *testing.T) {
	t.Parallel()

	src := `// @title: Article
#set page(paper: "a4")
/*
= Heading inside comment should be ignored
*/
` + "```typ\n= Heading inside raw block should be ignored\n```\n\n" + `#let helper = 1

= Real First Heading

Body text here.
`
	preamble := ExtractPreamble(src)
	assert.Contains(t, preamble, `#set page(paper: "a4")`)
	assert.Contains(t, preamble, `#let helper = 1`)
	assert.NotContains(t, preamble, "= Real First Heading")
	assert.NotContains(t, preamble, "Body text here.")
}

func TestScaffoldAndSavePersonalTemplate(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "cfg")

	// Save preamble-only personal template.
	sample := "#set page(margin: 2cm)\n#set text(size: 11pt)\n\n= Heading\n\nBody\n"
	saved, err := SavePersonalTemplate(cfgDir, "my-preamble", sample, true, false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cfgDir, "templates", "my-preamble.typ"), saved)

	raw, err := os.ReadFile(saved)
	require.NoError(t, err)
	assert.Equal(t, "#set page(margin: 2cm)\n#set text(size: 11pt)\n", string(raw))

	// Refuses overwrite without force.
	_, err = SavePersonalTemplate(cfgDir, "my-preamble", sample, false, false)
	assert.True(t, errors.Is(err, ErrTargetExists))

	// Allows overwrite with force.
	_, err = SavePersonalTemplate(cfgDir, "my-preamble", sample, false, true)
	assert.NoError(t, err)

	// Invalid names rejected.
	_, err = SavePersonalTemplate(cfgDir, "../evil", sample, false, false)
	assert.Error(t, err)

	// Scaffold built-in single-file template to file and check non-clobbering.
	all := DiscoverOffline(DiscoverOptions{ConfigDir: cfgDir})
	notes, ok := ResolveByName(all, "notes")
	require.True(t, ok)

	outFile := filepath.Join(tmp, "out", "hw-3.typ")
	require.NoError(t, ScaffoldToFile(notes, outFile, TemplateVars{Author: "Ada", Date: "2026-11-25"}, false))
	written, err := os.ReadFile(outFile)
	require.NoError(t, err)
	assert.Contains(t, string(written), "= Hw 3")
	assert.Contains(t, string(written), "Ada")
	assert.NotContains(t, string(written), "// @title:")

	// Refuses overwriting existing non-empty file.
	err = ScaffoldToFile(notes, outFile, TemplateVars{}, false)
	assert.True(t, errors.Is(err, ErrTargetExists))

	// Multi-file scaffold into directory.
	srcMulti := filepath.Join(tmp, "multi-src")
	require.NoError(t, os.MkdirAll(filepath.Join(srcMulti, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcMulti, "main.typ"), []byte("= {{title}}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(srcMulti, "sub", "defs.typ"), []byte("#let x = 1\n"), 0o644))

	targetDir := filepath.Join(tmp, "project-one")
	entry, err := ScaffoldToDir(Template{
		Name:        "multi",
		Source:      SourcePersonal,
		IsMultiFile: true,
		DirPath:     srcMulti,
		FilePath:    filepath.Join(srcMulti, "main.typ"),
		Entrypoint:  "main.typ",
	}, targetDir, TemplateVars{}, false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(targetDir, "main.typ"), entry)
	entryBytes, err := os.ReadFile(entry)
	require.NoError(t, err)
	assert.Equal(t, "= Project One\n", string(entryBytes))
	_, err = os.Stat(filepath.Join(targetDir, "sub", "defs.typ"))
	assert.NoError(t, err)

	// Refuses non-empty target directory without force.
	_, err = ScaffoldToDir(notes, targetDir, TemplateVars{}, false)
	assert.True(t, errors.Is(err, ErrTargetExists))
}
