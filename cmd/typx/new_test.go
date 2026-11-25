package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/monicafaye/TypX/internal/config"
)

func TestHandleNewFlagList(t *testing.T) {
	origCfg := config.ConfigDir
	cfgDir := t.TempDir()
	config.ConfigDir = cfgDir
	t.Cleanup(func() { config.ConfigDir = origCfg })
	t.Setenv("TYPST_PACKAGE_PATH", t.TempDir())
	t.Setenv("TYPST_PACKAGE_CACHE_PATH", t.TempDir())
	config.InitRuntimeFiles(false)

	var emptyOut bytes.Buffer
	args, exitNow, err := handleNewFlag("list", nil, &emptyOut, nil)
	require.NoError(t, err)
	assert.True(t, exitNow)
	assert.Nil(t, args)
	assert.Contains(t, emptyOut.String(), "No offline templates found.")
	assert.NotContains(t, emptyOut.String(), "Built-in Starters:")

	persDir := filepath.Join(cfgDir, "templates")
	require.NoError(t, os.MkdirAll(persDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(persDir, "article.typ"), []byte("// @title: Article\n= {{title}}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(persDir, "notes.typ"), []byte("// @title: Notes\n= {{title}}\n"), 0o644))

	var out bytes.Buffer
	args, exitNow, err = handleNewFlag("list", nil, &out, nil)
	require.NoError(t, err)
	assert.True(t, exitNow)
	assert.Nil(t, args)
	assert.Contains(t, out.String(), "Project & Personal:")
	assert.Contains(t, out.String(), "article")
	assert.Contains(t, out.String(), "notes")
}

func TestHandleNewFlagSingleMultiPersonalAndNoClobber(t *testing.T) {
	origCfg := config.ConfigDir
	cfgDir := t.TempDir()
	config.ConfigDir = cfgDir
	t.Cleanup(func() { config.ConfigDir = origCfg })
	config.InitRuntimeFiles(true)

	persDir := filepath.Join(cfgDir, "templates")
	require.NoError(t, os.MkdirAll(persDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(persDir, "notes.typ"), []byte("#let note(title, body) = []\n= {{title}}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(persDir, "article.typ"), []byte("= {{title}}\n"), 0o644))

	tmp := t.TempDir()
	// 1. Single-file personal template with explicit target path & cursor line flag preserved.
	targetFile := filepath.Join(tmp, "hw-1.typ")
	openArgs, exitNow, err := handleNewFlag("notes", []string{targetFile, "+10"}, nil, nil)
	require.NoError(t, err)
	assert.False(t, exitNow)
	assert.Equal(t, []string{targetFile, "+10"}, openArgs)

	data, err := os.ReadFile(targetFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "= Hw 1")
	assert.Contains(t, string(data), "#let note(title, body)")

	// 2. Non-clobbering guard refuses overwriting existing non-empty file.
	_, _, err = handleNewFlag("article", []string{targetFile}, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	// 3. Multi-file personal template folder scaffolds to directory.
	persMulti := filepath.Join(persDir, "lab")
	require.NoError(t, os.MkdirAll(persMulti, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(persMulti, "main.typ"), []byte("= {{title}}\n#import \"defs.typ\": *\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(persMulti, "defs.typ"), []byte("#let x = 42\n"), 0o644))

	projDir := filepath.Join(tmp, "lab-report")
	openArgs, exitNow, err = handleNewFlag("lab", []string{projDir}, nil, nil)
	require.NoError(t, err)
	assert.False(t, exitNow)
	assert.Equal(t, []string{filepath.Join(projDir, "main.typ")}, openArgs)

	mainBytes, err := os.ReadFile(filepath.Join(projDir, "main.typ"))
	require.NoError(t, err)
	assert.Contains(t, string(mainBytes), "= Lab Report")
	_, err = os.Stat(filepath.Join(projDir, "defs.typ"))
	assert.NoError(t, err)
}
