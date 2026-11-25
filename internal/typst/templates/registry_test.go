package templates

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPackageDirsForOS(t *testing.T) {
	t.Parallel()

	winLocal, winCache := packageDirsForOS("windows", func(k string) string {
		switch k {
		case "APPDATA":
			return `C:\Users\alice\AppData\Roaming`
		case "LOCALAPPDATA":
			return `C:\Users\alice\AppData\Local`
		}
		return ""
	}, `C:\Users\alice`)
	assert.Equal(t, filepath.Join(`C:\Users\alice\AppData\Roaming`, "typst", "packages"), winLocal)
	assert.Equal(t, filepath.Join(`C:\Users\alice\AppData\Local`, "typst", "packages"), winCache)

	linLocal, linCache := packageDirsForOS("linux", func(string) string { return "" }, "/home/alice")
	assert.Equal(t, filepath.Join("/home/alice", ".local", "share", "typst", "packages"), linLocal)
	assert.Equal(t, filepath.Join("/home/alice", ".cache", "typst", "packages"), linCache)

	macLocal, macCache := packageDirsForOS("darwin", func(string) string { return "" }, "/Users/alice")
	assert.Equal(t, filepath.Join("/Users/alice", "Library", "Application Support", "typst", "packages"), macLocal)
	assert.Equal(t, filepath.Join("/Users/alice", "Library", "Caches", "typst", "packages"), macCache)

	ovLocal, ovCache := packageDirsForOS("linux", func(k string) string {
		if k == "TYPST_PACKAGE_PATH" {
			return "/custom/local"
		}
		if k == "TYPST_PACKAGE_CACHE_PATH" {
			return "/custom/cache"
		}
		return ""
	}, "/home/alice")
	assert.Equal(t, "/custom/local", ovLocal)
	assert.Equal(t, "/custom/cache", ovCache)
}

func TestDiscoverOfflineAndPrecedence(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	projRoot := filepath.Join(tmp, "proj")
	cfgDir := filepath.Join(tmp, "cfg")
	localPkgDir := filepath.Join(tmp, "local-pkgs")
	cachePkgDir := filepath.Join(tmp, "cache-pkgs")

	// Project-local single-file template overriding "article"
	projTmplDir := filepath.Join(projRoot, ".typx", "templates")
	require.NoError(t, os.MkdirAll(projTmplDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projTmplDir, "article.typ"), []byte("// @title: Project Article\n= {{title}}\n"), 0o644))

	// Personal single-file & multi-file template
	persTmplDir := filepath.Join(cfgDir, "templates")
	require.NoError(t, os.MkdirAll(persTmplDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(persTmplDir, "article.typ"), []byte("// @title: Personal Article\n= {{title}}\n"), 0o644))
	multiDir := filepath.Join(persTmplDir, "thesis")
	require.NoError(t, os.MkdirAll(multiDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(multiDir, "main.typ"), []byte("// @title: My Thesis\n#import \"macros.typ\": *\n= {{title}}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(multiDir, "macros.typ"), []byte("#let thm = []\n"), 0o644))

	// @local template package
	localPkg := filepath.Join(localPkgDir, "local", "lab-template", "0.2.0")
	require.NoError(t, os.MkdirAll(filepath.Join(localPkg, "tmpl"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(localPkg, "typst.toml"), []byte("[package]\nname = \"lab-template\"\nversion = \"0.2.0\"\n[template]\npath = \"tmpl\"\nentrypoint = \"main.typ\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(localPkg, "tmpl", "main.typ"), []byte("= Lab\n"), 0o644))

	// Cached @preview package with two versions (latest should win) + non-template package (should be skipped)
	for _, ver := range []string{"0.1.2", "0.1.10"} {
		verDir := filepath.Join(cachePkgDir, "preview", "charged-ieee", ver)
		require.NoError(t, os.MkdirAll(filepath.Join(verDir, "template"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(verDir, "typst.toml"), []byte("[package]\nname = \"charged-ieee\"\nversion = \""+ver+"\"\ndescription = \"IEEE paper\"\n[template]\npath = \"template\"\nentrypoint = \"main.typ\"\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(verDir, "template", "main.typ"), []byte("= IEEE "+ver+"\n"), 0o644))
	}
	nonTmplDir := filepath.Join(cachePkgDir, "preview", "cetz", "0.3.0")
	require.NoError(t, os.MkdirAll(nonTmplDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nonTmplDir, "typst.toml"), []byte("[package]\nname = \"cetz\"\nversion = \"0.3.0\"\n"), 0o644))

	found := DiscoverOffline(DiscoverOptions{
		ProjectRoot:      projRoot,
		ConfigDir:        cfgDir,
		LocalPackagesDir: localPkgDir,
		CachePackagesDir: cachePkgDir,
	})

	// Check project template overrides personal & builtin when resolving by name.
	resolved, ok := ResolveByName(found, "article")
	require.True(t, ok)
	assert.Equal(t, SourceProject, resolved.Source)
	assert.Equal(t, "Project Article", resolved.Title)

	// Built-in templates can still be selected explicitly or via default names.
	notes, ok := ResolveByName(found, "notes")
	require.True(t, ok)
	assert.Equal(t, SourceBuiltin, notes.Source)

	// Multi-file personal template discovered.
	thesis, ok := ResolveByName(found, "thesis")
	require.True(t, ok)
	assert.True(t, thesis.IsMultiFile)
	assert.Equal(t, SourcePersonal, thesis.Source)

	// @local package resolved with and without version.
	loc, ok := ResolveByName(found, "@local/lab-template")
	require.True(t, ok)
	assert.Equal(t, SourceLocalPkg, loc.Source)
	assert.Equal(t, "0.2.0", loc.Version)

	// Cached @preview picks highest semver (0.1.10 > 0.1.2) and skips cetz.
	ieee, ok := ResolveByName(found, "@preview/charged-ieee")
	require.True(t, ok)
	assert.Equal(t, "0.1.10", ieee.Version)
	_, hasCetz := ResolveByName(found, "@preview/cetz")
	assert.False(t, hasCetz)
}
