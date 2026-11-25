package templates

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUniverseFetchCachingETagAndOfflineFallback(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	var failServer atomic.Bool

	indexJSON := `[
  {"name": "cetz", "version": "0.3.2", "description": "Drawing library"},
  {"name": "charged-ieee", "version": "0.1.2", "description": "Old IEEE", "categories": ["paper"], "template": {"path": "template", "entrypoint": "main.typ"}},
  {"name": "charged-ieee", "version": "0.1.4", "description": "Latest IEEE", "categories": ["paper"], "template": {"path": "template", "entrypoint": "main.typ"}},
  {"name": "touying", "version": "0.6.1", "description": "Slides framework", "categories": ["presentation"], "template": {"path": "template", "entrypoint": "slides.typ"}}
]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if failServer.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		assert.Equal(t, "/index.json", r.URL.Path)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(indexJSON))
	}))
	defer srv.Close()

	cacheFile := filepath.Join(t.TempDir(), "cache", "universe.json")
	client := &UniverseClient{
		BaseURL:    srv.URL,
		CacheFile:  cacheFile,
		HTTPClient: srv.Client(),
		TTL:        time.Hour,
	}

	// 1. Cold fetch populates cache, filters out non-templates (cetz), keeps latest semver.
	items, err := client.FetchUniverse(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, int32(1), hits.Load())
	require.Len(t, items, 2)
	assert.Equal(t, "charged-ieee", items[0].Name)
	assert.Equal(t, "0.1.4", items[0].Version)
	assert.Equal(t, "@preview/charged-ieee:0.1.4", items[0].PackageSpec)
	assert.Equal(t, SourceUniverse, items[0].Source)
	assert.Equal(t, "touying", items[1].Name)

	// 2. Subsequent call within TTL uses disk cache with zero HTTP calls.
	cachedItems, err := client.FetchUniverse(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, int32(1), hits.Load())
	assert.Equal(t, items, cachedItems)

	// Also verify LoadCachedUniverse without network.
	loaded, _, ok := client.LoadCachedUniverse()
	assert.True(t, ok)
	assert.Len(t, loaded, 2)

	// 3. Force refresh sends ETag and accepts 304 Not Modified.
	refreshed, err := client.FetchUniverse(context.Background(), true)
	require.NoError(t, err)
	assert.Equal(t, int32(2), hits.Load())
	assert.Equal(t, items, refreshed)

	// 4. Unversioned spec resolves against cached catalog without network hit.
	name, ver, err := ResolveUniverseVersion(context.Background(), client, "@preview/touying")
	require.NoError(t, err)
	assert.Equal(t, "touying", name)
	assert.Equal(t, "0.6.1", ver)

	// 5. Server outage returns cached entries alongside error.
	failServer.Store(true)
	fallbackItems, err := client.FetchUniverse(context.Background(), true)
	assert.Error(t, err)
	assert.Equal(t, items, fallbackItems)
}

func TestDownloadAndExtractPreviewPackageAndZipSlipGuard(t *testing.T) {
	t.Parallel()

	goodArchive := buildTarGz(t, []tarSpec{
		{name: "typst.toml", body: "[package]\nname = \"demo\"\nversion = \"1.0.0\"\n[template]\npath = \"tmpl\"\nentrypoint = \"main.typ\"\n"},
		{name: "tmpl/main.typ", body: "= Demo\n"},
	})
	slipArchive := buildTarGz(t, []tarSpec{
		{name: "../evil.typ", body: "bad"},
	})
	symlinkArchive := buildTarGz(t, []tarSpec{
		{name: "link.typ", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/demo-1.0.0.tar.gz":
			_, _ = w.Write(goodArchive)
		case "/slip-1.0.0.tar.gz":
			_, _ = w.Write(slipArchive)
		case "/sym-1.0.0.tar.gz":
			_, _ = w.Write(symlinkArchive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cacheRoot := filepath.Join(t.TempDir(), "pkgs")
	client := &UniverseClient{
		BaseURL:    srv.URL,
		CacheFile:  filepath.Join(t.TempDir(), "universe.json"),
		HTTPClient: srv.Client(),
	}

	pkgDir, err := client.DownloadAndExtractPreviewPackage(context.Background(), "demo", "1.0.0", cacheRoot)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cacheRoot, "preview", "demo", "1.0.0"), pkgDir)
	entryData, err := os.ReadFile(filepath.Join(pkgDir, "tmpl", "main.typ"))
	require.NoError(t, err)
	assert.Equal(t, "= Demo\n", string(entryData))

	// Zip-slip rejection.
	_, err = client.DownloadAndExtractPreviewPackage(context.Background(), "slip", "1.0.0", cacheRoot)
	assert.Error(t, err)
	_, err = os.Stat(filepath.Join(cacheRoot, "preview", "slip"))
	assert.True(t, os.IsNotExist(err))

	// Symlink rejection.
	_, err = client.DownloadAndExtractPreviewPackage(context.Background(), "sym", "1.0.0", cacheRoot)
	assert.Error(t, err)
}

type tarSpec struct {
	name     string
	body     string
	typeflag byte
	linkname string
}

func buildTarGz(t *testing.T, specs []tarSpec) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, s := range specs {
		tf := s.typeflag
		if tf == 0 {
			tf = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     s.name,
			Mode:     0o644,
			Size:     int64(len(s.body)),
			Typeflag: tf,
			Linkname: s.linkname,
		}
		if tf == tar.TypeSymlink || tf == tar.TypeLink {
			hdr.Size = 0
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if len(s.body) > 0 && hdr.Size > 0 {
			_, err := tw.Write([]byte(s.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}
