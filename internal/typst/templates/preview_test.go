package templates

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func minimalPNGBytes(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// minimalLosslessWebP returns a valid 1x1 VP8L WebP image without external tools.
func minimalLosslessWebP() []byte {
	var bits []byte
	pushBit := func(bitPos *int, val int) {
		if *bitPos/8 >= len(bits) {
			bits = append(bits, 0)
		}
		if val != 0 {
			bits[*bitPos/8] |= 1 << (*bitPos % 8)
		}
		*bitPos++
	}
	pos := 0
	// transform=0, colorCache=0, metaHuffman=0
	pushBit(&pos, 0)
	pushBit(&pos, 0)
	pushBit(&pos, 0)
	// 5 single-symbol simple Huffman trees (green, red, blue, alpha, dist -> symbol 0)
	for i := 0; i < 5; i++ {
		pushBit(&pos, 1) // simple code
		pushBit(&pos, 0) // 1 symbol
		pushBit(&pos, 0) // 1-bit symbol
		pushBit(&pos, 0) // symbol = 0
	}

	vp8l := append([]byte{0x2f, 0x00, 0x00, 0x00, 0x00}, bits...)
	if len(vp8l)%2 == 1 {
		vp8l = append(vp8l, 0)
	}
	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(4+8+len(vp8l)))
	out.WriteString("WEBP")
	out.WriteString("VP8L")
	_ = binary.Write(&out, binary.LittleEndian, uint32(len(vp8l)))
	out.Write(vp8l)
	return out.Bytes()
}

func TestDecodeImageToRGBA_PNGAndWebP(t *testing.T) {
	t.Parallel()

	pngImg, err := DecodeImageToRGBA(minimalPNGBytes(t, 12, 8, color.RGBA{10, 20, 30, 255}))
	require.NoError(t, err)
	assert.Equal(t, image.Rect(0, 0, 12, 8), pngImg.Bounds())

	// Transparent PNGs must composite onto paper white instead of turning black.
	transImg, err := DecodeImageToRGBA(minimalPNGBytes(t, 4, 4, color.RGBA{0, 0, 0, 0}))
	require.NoError(t, err)
	assert.Equal(t, color.RGBA{255, 255, 255, 255}, transImg.RGBAAt(1, 1))

	webpImg, err := DecodeImageToRGBA(minimalLosslessWebP())
	require.NoError(t, err)
	assert.Equal(t, image.Rect(0, 0, 1, 1), webpImg.Bounds())
}

func TestUniverseFetchThumbnailCacheAndFallback(t *testing.T) {
	t.Parallel()

	webpBytes := minimalLosslessWebP()
	var smallHits atomic.Int32
	var fullHits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/thumbnails/charged-ieee-0.1.4.webp":
			fullHits.Add(1)
			w.Header().Set("Content-Type", "image/webp")
			_, _ = w.Write(webpBytes)
		case "/thumbnails/fallback-pkg-1.0.0.webp":
			fullHits.Add(1)
			w.WriteHeader(http.StatusNotFound)
		case "/thumbnails/fallback-pkg-1.0.0-small.webp":
			smallHits.Add(1)
			w.Header().Set("Content-Type", "image/webp")
			_, _ = w.Write(webpBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cacheDir := filepath.Join(t.TempDir(), "previews")
	client := &UniverseClient{
		BaseURL:    srv.URL,
		CacheFile:  filepath.Join(t.TempDir(), "universe.json"),
		HTTPClient: srv.Client(),
	}

	// 1. Prefers full-res .webp and caches it under thumb-full-*.webp.
	got1, err := client.FetchThumbnail(context.Background(), "charged-ieee", "0.1.4", cacheDir)
	require.NoError(t, err)
	assert.Equal(t, webpBytes, got1)
	assert.Equal(t, int32(1), fullHits.Load())
	assert.Equal(t, int32(0), smallHits.Load())
	_, statErr := os.Stat(filepath.Join(cacheDir, "thumb-full-charged-ieee-0.1.4.webp"))
	assert.NoError(t, statErr)

	// 2. Second request serves from disk with 0 HTTP hits.
	got2, err := client.FetchThumbnail(context.Background(), "charged-ieee", "0.1.4", cacheDir)
	require.NoError(t, err)
	assert.Equal(t, webpBytes, got2)
	assert.Equal(t, int32(1), fullHits.Load())

	// 3. 404 on full .webp falls back cleanly to -small.webp.
	gotFallback, err := client.FetchThumbnail(context.Background(), "fallback-pkg", "1.0.0", cacheDir)
	require.NoError(t, err)
	assert.Equal(t, webpBytes, gotFallback)
	assert.Equal(t, int32(1), smallHits.Load())
}

func TestLoadTemplatePreviewImagesLocalUniverseAndCompiledCache(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cacheDir := filepath.Join(tmp, "preview-cache")

	// 1. Local ThumbnailPath PNG takes precedence with zero compilation.
	thumbFile := filepath.Join(tmp, "local-thumb.png")
	require.NoError(t, os.WriteFile(thumbFile, minimalPNGBytes(t, 16, 24, color.RGBA{200, 100, 50, 255}), 0o644))
	imgs, err := LoadTemplatePreviewImages(context.Background(), Template{
		Name:          "local-demo",
		Source:        SourceLocalPkg,
		ThumbnailPath: thumbFile,
	}, nil, cacheDir, nil)
	require.NoError(t, err)
	require.Len(t, imgs, 1)
	assert.Equal(t, image.Rect(0, 0, 16, 24), imgs[0].Bounds())

	// 2. Personal template compiles once via compileFn, writes SHA-256 PNG cache, then hits cache.
	persDir := filepath.Join(tmp, "cfg", "templates")
	require.NoError(t, os.MkdirAll(persDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(persDir, "notes.typ"), []byte("#let note(title, body) = []\n= {{title}}\n"), 0o644))
	all := DiscoverOffline(DiscoverOptions{ConfigDir: filepath.Join(tmp, "cfg")})
	notes, ok := ResolveByName(all, "notes")
	require.True(t, ok)

	var compileCount atomic.Int32
	stubCompile := func(ctx context.Context, filePath, rootDir string, content []byte) ([]*image.RGBA, error) {
		compileCount.Add(1)
		assert.Contains(t, string(content), "#let note(")
		img := image.NewRGBA(image.Rect(0, 0, 40, 60))
		return []*image.RGBA{img}, nil
	}

	firstImgs, err := LoadTemplatePreviewImages(context.Background(), notes, nil, cacheDir, stubCompile)
	require.NoError(t, err)
	require.Len(t, firstImgs, 1)
	assert.Equal(t, int32(1), compileCount.Load())

	secondImgs, err := LoadTemplatePreviewImages(context.Background(), notes, nil, cacheDir, stubCompile)
	require.NoError(t, err)
	require.Len(t, secondImgs, 1)
	assert.Equal(t, int32(1), compileCount.Load(), "second call must hit SHA-256 PNG cache on disk")
	assert.Equal(t, image.Rect(0, 0, 40, 60), secondImgs[0].Bounds())
}

func TestLoadTemplatePreviewImagesRespectsCanceledContext(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	persDir := filepath.Join(tmp, "cfg", "templates")
	require.NoError(t, os.MkdirAll(persDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(persDir, "notes.typ"), []byte("= {{title}}\n"), 0o644))
	all := DiscoverOffline(DiscoverOptions{ConfigDir: filepath.Join(tmp, "cfg")})
	notes, ok := ResolveByName(all, "notes")
	require.True(t, ok)

	// 1. Pre-canceled context must not compile or touch cacheDir.
	preCanceled, cancelPre := context.WithCancel(context.Background())
	cancelPre()
	preCacheDir := filepath.Join(tmp, "pre-canceled-cache")
	var compileCalls atomic.Int32
	_, err := LoadTemplatePreviewImages(preCanceled, notes, nil, preCacheDir, func(ctx context.Context, filePath, rootDir string, content []byte) ([]*image.RGBA, error) {
		compileCalls.Add(1)
		return []*image.RGBA{image.NewRGBA(image.Rect(0, 0, 10, 10))}, nil
	})
	require.True(t, errors.Is(err, context.Canceled), "expected context.Canceled, got %v", err)
	assert.Equal(t, int32(0), compileCalls.Load())
	_, statErr := os.Stat(preCacheDir)
	assert.True(t, os.IsNotExist(statErr), "canceled load must not create cache directory")

	// 2. Context canceled during compilation must not write a cache file afterward.
	midCtx, cancelMid := context.WithCancel(context.Background())
	midCacheDir := filepath.Join(tmp, "mid-canceled-cache")
	_, err = LoadTemplatePreviewImages(midCtx, notes, nil, midCacheDir, func(ctx context.Context, filePath, rootDir string, content []byte) ([]*image.RGBA, error) {
		cancelMid()
		return []*image.RGBA{image.NewRGBA(image.Rect(0, 0, 10, 10))}, nil
	})
	require.True(t, errors.Is(err, context.Canceled), "expected context.Canceled, got %v", err)
	_, statErr = os.Stat(midCacheDir)
	assert.True(t, os.IsNotExist(statErr), "context canceled during compile must not write to cacheDir")
}
