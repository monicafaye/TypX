package templates

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/config"
)

// PreviewProvider bridges TemplatePane live visual preview into preview.PreviewPane
// without introducing an import cycle between internal/typst/templates and preview.
type PreviewProvider interface {
	SupportsVisual() bool
	OpenLoading(tab *action.Tab, bp *action.BufPane, title string) bool
	ShowImages(tab *action.Tab, title string, imgs []*image.RGBA)
	ShowError(tab *action.Tab, title, errMsg string)
	Clear(tab *action.Tab)
	CompileTypst(ctx context.Context, filePath, rootDir string, content []byte) ([]*image.RGBA, error)
}

// DefaultPreviewCacheDir returns the on-disk cache folder for template thumbnails
// and compiled starter previews.
func DefaultPreviewCacheDir() string {
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return filepath.Join(cacheDir, "typx", "template-previews")
	}
	if config.ConfigDir != "" {
		return filepath.Join(config.ConfigDir, "cache", "template-previews")
	}
	return ""
}

// DecodeImageToRGBA decodes PNG, JPEG, or WebP bytes into an opaque *image.RGBA
// composited over paper white so transparent areas render cleanly in dark or light themes.
func DecodeImageToRGBA(data []byte) (*image.RGBA, error) {
	if len(data) == 0 {
		return nil, errors.New("empty image data")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Over)
	return rgba, nil
}

// LoadTemplatePreviewImages resolves page preview images for t using:
//  1. Local package ThumbnailPath (if present on disk),
//  2. Cached/fetched Typst Universe WebP thumbnail for @preview templates
//     (falling back to compiling FilePath if thumbnail fetch fails),
//  3. Single-page compilation via compileFn with SHA-256 PNG disk caching.
func LoadTemplatePreviewImages(
	ctx context.Context,
	t Template,
	client *UniverseClient,
	cacheDir string,
	compileFn func(ctx context.Context, filePath, rootDir string, content []byte) ([]*image.RGBA, error),
) ([]*image.RGBA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.ThumbnailPath != "" {
		raw, err := os.ReadFile(t.ThumbnailPath)
		if err == nil {
			img, decErr := DecodeImageToRGBA(raw)
			if decErr == nil {
				return []*image.RGBA{img}, nil
			}
		}
	}

	if t.Source == SourceUniverse || (t.Source == SourceCachedPkg && t.Name != "" && t.Version != "") {
		if client == nil {
			client = NewUniverseClient()
		}
		raw, err := client.FetchThumbnail(ctx, t.Name, t.Version, cacheDir)
		if err == nil {
			img, decErr := DecodeImageToRGBA(raw)
			if decErr == nil {
				return []*image.RGBA{img}, nil
			}
			err = decErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if t.FilePath != "" && compileFn != nil {
			root := t.DirPath
			if root == "" {
				root = filepath.Dir(t.FilePath)
			}
			if imgs, cErr := compileFn(ctx, t.FilePath, root, nil); cErr == nil && len(imgs) > 0 {
				return imgs, nil
			}
		}
		return nil, err
	}

	rendered, err := RenderEntry(t, TemplateVars{
		Title:  t.DisplayTitle(),
		Author: "Author",
		Date:   "2026-01-01",
	})
	if err != nil {
		return nil, err
	}

	var cachePath string
	if strings.TrimSpace(cacheDir) != "" {
		sum := sha256.Sum256([]byte(t.ID + "\x00" + rendered))
		slug := sanitizePreviewSlug(t.Name)
		cachePath = filepath.Join(cacheDir, fmt.Sprintf("compiled-%s-%s.png", slug, hex.EncodeToString(sum[:8])))
		if cachedBytes, readErr := os.ReadFile(cachePath); readErr == nil {
			if img, decErr := DecodeImageToRGBA(cachedBytes); decErr == nil {
				return []*image.RGBA{img}, nil
			}
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if compileFn == nil {
		return nil, errors.New("preview compiler unavailable")
	}

	entryPath := t.FilePath
	if entryPath == "" {
		name := sanitizePreviewSlug(t.Name)
		if name == "" {
			name = "template"
		}
		entryPath = name + ".typ"
	}
	rootDir := t.DirPath
	if rootDir == "" && t.FilePath != "" {
		rootDir = filepath.Dir(t.FilePath)
	}

	imgs, err := compileFn(ctx, entryPath, rootDir, []byte(rendered))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(imgs) == 0 {
		return nil, errors.New("template preview produced no pages")
	}

	if cachePath != "" && imgs[0] != nil {
		var buf bytes.Buffer
		if encErr := png.Encode(&buf, imgs[0]); encErr == nil && ctx.Err() == nil {
			_ = writeBytesAtomic(cachePath, buf.Bytes())
		}
	}
	return imgs, nil
}

func sanitizePreviewSlug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "template"
	}
	return b.String()
}
