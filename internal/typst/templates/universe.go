package templates

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/monicafaye/TypX/internal/config"
)

const (
	defaultUniverseBaseURL     = "https://packages.typst.org/preview"
	defaultUniverseTTL         = 24 * time.Hour
	universeCacheSchemaVersion = 2

	maxUniverseIndexBytes = 32 << 20 // 32 MiB
	maxThumbnailBytes     = 10 << 20 // 10 MiB
	maxTarballBytes       = 50 << 20 // 50 MiB
	maxExtractedBytes     = 50 << 20 // 50 MiB
	maxExtractedFiles     = 1000
)

// UniverseClient loads and caches Typst Universe (@preview) template metadata
// and provides a pure-Go tarball download fallback into Typst's package cache.
type UniverseClient struct {
	BaseURL    string
	CacheFile  string
	HTTPClient *http.Client
	TTL        time.Duration
}

// NewUniverseClient returns a UniverseClient with standard defaults.
func NewUniverseClient() *UniverseClient {
	return &UniverseClient{
		BaseURL:    defaultUniverseBaseURL,
		CacheFile:  defaultUniverseCacheFile(),
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
		TTL:        defaultUniverseTTL,
	}
}

func defaultUniverseCacheFile() string {
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return filepath.Join(cacheDir, "typx", "universe-templates.json")
	}
	if config.ConfigDir != "" {
		return filepath.Join(config.ConfigDir, "cache", "universe-templates.json")
	}
	return ""
}

func (c *UniverseClient) baseURL() string {
	if c != nil && strings.TrimSpace(c.BaseURL) != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return defaultUniverseBaseURL
}

func (c *UniverseClient) cacheFile() string {
	if c != nil && strings.TrimSpace(c.CacheFile) != "" {
		return c.CacheFile
	}
	return defaultUniverseCacheFile()
}

func (c *UniverseClient) httpClient() *http.Client {
	if c != nil && c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (c *UniverseClient) ttl() time.Duration {
	if c != nil && c.TTL > 0 {
		return c.TTL
	}
	return defaultUniverseTTL
}

type universeCachePayload struct {
	SchemaVersion int        `json:"schemaVersion,omitempty"`
	ETag          string     `json:"etag,omitempty"`
	FetchedAt     time.Time  `json:"fetchedAt"`
	Templates     []Template `json:"templates"`
}

type universeIndexEntry struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Authors     []string `json:"authors"`
	Categories  []string `json:"categories"`
	Disciplines []string `json:"disciplines"`
	Keywords    []string `json:"keywords"`
	Template    *struct {
		Path       string `json:"path"`
		Entrypoint string `json:"entrypoint"`
		Thumbnail  string `json:"thumbnail"`
	} `json:"template"`
}

// LoadCachedUniverse reads the local stripped Universe cache without network calls.
func (c *UniverseClient) LoadCachedUniverse() ([]Template, time.Time, bool) {
	payload, ok := c.readCache()
	if !ok {
		return nil, time.Time{}, false
	}
	return payload.Templates, payload.FetchedAt, true
}

// FetchUniverse loads Typst Universe template entries, using the local TTL/ETag
// cache unless forceRefresh bypasses the TTL window. Never auto-invoked on startup.
func (c *UniverseClient) FetchUniverse(ctx context.Context, forceRefresh bool) ([]Template, error) {
	cached, hasCache := c.readCache()
	cacheSchemaFresh := cached.SchemaVersion >= universeCacheSchemaVersion
	if !forceRefresh && hasCache && cacheSchemaFresh && len(cached.Templates) > 0 && time.Since(cached.FetchedAt) < c.ttl() {
		return cached.Templates, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+"/index.json", nil)
	if err != nil {
		if hasCache && len(cached.Templates) > 0 {
			return cached.Templates, err
		}
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "TypX")
	if hasCache && cacheSchemaFresh && cached.ETag != "" {
		req.Header.Set("If-None-Match", cached.ETag)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		if hasCache && len(cached.Templates) > 0 {
			return cached.Templates, err
		}
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified && hasCache {
		cached.FetchedAt = time.Now().UTC()
		if etag := strings.TrimSpace(resp.Header.Get("ETag")); etag != "" {
			cached.ETag = etag
		}
		_ = c.writeCache(cached)
		return cached.Templates, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		statusErr := fmt.Errorf("universe index request failed: HTTP %d", resp.StatusCode)
		if hasCache && len(cached.Templates) > 0 {
			return cached.Templates, statusErr
		}
		return nil, statusErr
	}

	var entries []universeIndexEntry
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxUniverseIndexBytes))
	if err := dec.Decode(&entries); err != nil {
		if hasCache && len(cached.Templates) > 0 {
			return cached.Templates, err
		}
		return nil, fmt.Errorf("decode universe index: %w", err)
	}

	templates := extractLatestUniverseTemplates(entries)
	payload := universeCachePayload{
		SchemaVersion: universeCacheSchemaVersion,
		ETag:          strings.TrimSpace(resp.Header.Get("ETag")),
		FetchedAt:     time.Now().UTC(),
		Templates:     templates,
	}
	_ = c.writeCache(payload)
	return templates, nil
}

func extractLatestUniverseTemplates(entries []universeIndexEntry) []Template {
	bestByName := make(map[string]Template)
	for _, e := range entries {
		if e.Name == "" || e.Version == "" || e.Template == nil {
			continue
		}
		if strings.TrimSpace(e.Template.Path) == "" || strings.TrimSpace(e.Template.Entrypoint) == "" {
			continue
		}
		key := strings.ToLower(e.Name)
		if cur, exists := bestByName[key]; exists && compareSemver(e.Version, cur.Version) <= 0 {
			continue
		}
		cats := cleanStringSlice(e.Categories)
		cat := "Universe"
		if len(cats) > 0 {
			cat = cats[0]
		}
		spec := "@preview/" + e.Name + ":" + e.Version
		bestByName[key] = Template{
			ID:            spec,
			Name:          e.Name,
			Title:         e.Name + " (" + e.Version + ")",
			Description:   e.Description,
			Category:      cat,
			Categories:    cats,
			Keywords:      cleanStringSlice(e.Keywords),
			Disciplines:   cleanStringSlice(e.Disciplines),
			Authors:       cleanStringSlice(e.Authors),
			Version:       e.Version,
			Source:        SourceUniverse,
			IsMultiFile:   true,
			Entrypoint:    filepath.ToSlash(e.Template.Entrypoint),
			PackageSpec:   spec,
			ThumbnailName: filepath.ToSlash(e.Template.Thumbnail),
		}
	}

	out := make([]Template, 0, len(bestByName))
	for _, t := range bestByName {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func cleanStringSlice(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s := strings.TrimSpace(item); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c *UniverseClient) readCache() (universeCachePayload, bool) {
	cf := c.cacheFile()
	if cf == "" {
		return universeCachePayload{}, false
	}
	data, err := os.ReadFile(cf)
	if err != nil {
		return universeCachePayload{}, false
	}
	var payload universeCachePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return universeCachePayload{}, false
	}
	for i := range payload.Templates {
		payload.Templates[i].Source = SourceUniverse
	}
	return payload, true
}

func (c *UniverseClient) writeCache(payload universeCachePayload) error {
	cf := c.cacheFile()
	if cf == "" {
		return nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	dir := filepath.Dir(cf)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".universe-cache-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, cf); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// FetchThumbnail loads a cached Typst Universe template thumbnail or fetches
// `<name>-<version>.webp` (falling back to `<name>-<version>-small.webp` on 404).
func (c *UniverseClient) FetchThumbnail(ctx context.Context, name, version, previewCacheDir string) ([]byte, error) {
	if !isSafePkgToken(name) || !isSafePkgToken(version) {
		return nil, fmt.Errorf("invalid package coordinates: %q %q", name, version)
	}

	var cachePath string
	if strings.TrimSpace(previewCacheDir) != "" {
		cachePath = filepath.Join(previewCacheDir, fmt.Sprintf("thumb-full-%s-%s.webp", name, version))
		if raw, err := os.ReadFile(cachePath); err == nil && len(raw) > 0 {
			return raw, nil
		}
	}

	urls := []string{
		fmt.Sprintf("%s/thumbnails/%s-%s.webp", c.baseURL(), name, version),
		fmt.Sprintf("%s/thumbnails/%s-%s-small.webp", c.baseURL(), name, version),
	}

	var lastErr error
	for idx, u := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "TypX")

		resp, err := c.httpClient().Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusNotFound && idx == 0 {
			resp.Body.Close()
			lastErr = fmt.Errorf("thumbnail not found: HTTP 404")
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return nil, fmt.Errorf("thumbnail request failed: HTTP %d", resp.StatusCode)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxThumbnailBytes+1))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(raw) > maxThumbnailBytes {
			return nil, fmt.Errorf("thumbnail exceeds %d byte limit", maxThumbnailBytes)
		}
		if len(raw) == 0 {
			return nil, errors.New("empty thumbnail response")
		}
		if cachePath != "" {
			_ = writeBytesAtomic(cachePath, raw)
		}
		return raw, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("thumbnail unavailable")
}

func writeBytesAtomic(target string, data []byte) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cache-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// ParsePackageSpec parses `@preview/<name>[:<version>]` or `@local/<name>[:<version>]`.
func ParsePackageSpec(spec string) (namespace, name, version string, ok bool) {
	s := strings.TrimSpace(spec)
	switch {
	case strings.HasPrefix(s, "@preview/"):
		namespace = "preview"
		s = strings.TrimPrefix(s, "@preview/")
	case strings.HasPrefix(s, "@local/"):
		namespace = "local"
		s = strings.TrimPrefix(s, "@local/")
	default:
		return "", "", "", false
	}
	if colon := strings.IndexByte(s, ':'); colon >= 0 {
		name = strings.TrimSpace(s[:colon])
		version = strings.TrimSpace(s[colon+1:])
	} else {
		name = strings.TrimSpace(s)
	}
	if !isSafePkgToken(name) || (version != "" && !isSafePkgToken(version)) {
		return "", "", "", false
	}
	return namespace, name, version, true
}

func isSafePkgToken(tok string) bool {
	if tok == "" || tok == "." || tok == ".." {
		return false
	}
	return !strings.ContainsAny(tok, "/\\ \t\r\n")
}

// ResolveUniverseVersion resolves a `@preview/<name>[:<version>]` spec into
// concrete package name and version, querying the Universe index when unversioned.
func ResolveUniverseVersion(ctx context.Context, client *UniverseClient, spec string) (string, string, error) {
	ns, name, version, ok := ParsePackageSpec(spec)
	if !ok || ns != "preview" {
		return "", "", fmt.Errorf("invalid @preview package spec: %q", spec)
	}
	if version != "" {
		return name, version, nil
	}
	if client == nil {
		client = NewUniverseClient()
	}
	items, err := client.FetchUniverse(ctx, false)
	if len(items) == 0 && err != nil {
		return "", "", err
	}
	for _, item := range items {
		if strings.EqualFold(item.Name, name) && item.Version != "" {
			return item.Name, item.Version, nil
		}
	}
	if err != nil {
		return "", "", err
	}
	return "", "", fmt.Errorf("template %q not found in Typst Universe", spec)
}

// DownloadAndExtractPreviewPackage downloads `<name>-<version>.tar.gz` into
// `<cachePackagesDir>/preview/<name>/<version>/` using the default Universe client.
func DownloadAndExtractPreviewPackage(ctx context.Context, name, version, cachePackagesDir string) (string, error) {
	return NewUniverseClient().DownloadAndExtractPreviewPackage(ctx, name, version, cachePackagesDir)
}

// DownloadAndExtractPreviewPackage downloads and safely unpacks a @preview package
// archive into `<cachePackagesDir>/preview/<name>/<version>/`.
func (c *UniverseClient) DownloadAndExtractPreviewPackage(ctx context.Context, name, version, cachePackagesDir string) (string, error) {
	if !isSafePkgToken(name) || !isSafePkgToken(version) {
		return "", fmt.Errorf("invalid package coordinates: %q %q", name, version)
	}
	if strings.TrimSpace(cachePackagesDir) == "" {
		_, cachePackagesDir = PackageDirs()
	}
	if strings.TrimSpace(cachePackagesDir) == "" {
		return "", errors.New("Typst package cache directory unavailable")
	}

	pkgDir := filepath.Join(cachePackagesDir, "preview", name, version)
	if _, err := os.Stat(filepath.Join(pkgDir, "typst.toml")); err == nil {
		return pkgDir, nil
	}

	archiveURL := fmt.Sprintf("%s/%s-%s.tar.gz", c.baseURL(), name, version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "TypX")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download %s failed: HTTP %d", archiveURL, resp.StatusCode)
	}

	parentDir := filepath.Dir(pkgDir)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return "", err
	}
	tmpDir, err := os.MkdirTemp(parentDir, ".tmp-"+version+"-*")
	if err != nil {
		return "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmpDir)
			_ = os.Remove(parentDir) // remove empty <name> dir if created by this call
		}
	}()

	gz, err := gzip.NewReader(io.LimitReader(resp.Body, maxTarballBytes))
	if err != nil {
		return "", fmt.Errorf("open gzip stream: %w", err)
	}
	defer gz.Close()

	if err := extractTarSafe(tar.NewReader(gz), tmpDir); err != nil {
		return "", err
	}

	if err := os.Rename(tmpDir, pkgDir); err != nil {
		if _, statErr := os.Stat(filepath.Join(pkgDir, "typst.toml")); statErr == nil {
			return pkgDir, nil
		}
		return "", err
	}
	cleanup = false
	return pkgDir, nil
}

func extractTarSafe(tr *tar.Reader, dstRoot string) error {
	var totalBytes int64
	var fileCount int

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		fileCount++
		if fileCount > maxExtractedFiles {
			return fmt.Errorf("tar archive exceeds %d entries", maxExtractedFiles)
		}

		switch hdr.Typeflag {
		case tar.TypeXHeader, tar.TypeXGlobalHeader, tar.TypeGNULongName, tar.TypeGNULongLink:
			continue
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("unsafe tar link entry %q", hdr.Name)
		}

		rel, err := sanitizeTarPath(hdr.Name)
		if err != nil {
			return err
		}
		if rel == "." {
			continue
		}

		target := filepath.Clean(filepath.Join(dstRoot, filepath.FromSlash(rel)))
		if !isSubpath(dstRoot, target) {
			return fmt.Errorf("tar entry escapes root: %q", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size < 0 || totalBytes+hdr.Size > maxExtractedBytes {
				return fmt.Errorf("tar archive exceeds %d byte extraction limit", maxExtractedBytes)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(out, io.LimitReader(tr, maxExtractedBytes-totalBytes+1))
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			totalBytes += n
			if totalBytes > maxExtractedBytes {
				return fmt.Errorf("tar archive exceeds %d byte extraction limit", maxExtractedBytes)
			}
		default:
			return fmt.Errorf("unsupported tar entry type %q for %q", hdr.Typeflag, hdr.Name)
		}
	}
	return nil
}

func sanitizeTarPath(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("empty tar entry path")
	}
	slashed := strings.ReplaceAll(raw, "\\", "/")
	if strings.HasPrefix(slashed, "/") {
		return "", fmt.Errorf("absolute tar entry path: %q", raw)
	}
	// Reject Windows drive-letter prefixes like C:foo.
	if len(slashed) >= 2 && slashed[1] == ':' {
		return "", fmt.Errorf("drive-prefixed tar entry path: %q", raw)
	}
	for _, seg := range strings.Split(slashed, "/") {
		if seg == ".." {
			return "", fmt.Errorf("tar entry escapes root via '..': %q", raw)
		}
	}
	cleaned := path.Clean(slashed)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("tar entry escapes root: %q", raw)
	}
	return cleaned, nil
}
