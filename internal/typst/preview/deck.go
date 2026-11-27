package preview

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const pageGapPx = 14

var pageNumRe = regexp.MustCompile(`page-(\d+)\.png$`)

// CompileRequest describes a document snapshot to compile into per-page PNGs.
type CompileRequest struct {
	FilePath    string // Absolute path on disk (may be empty for unnamed buffers)
	RootDir     string // Typst project root directory
	Content     []byte // In-memory buffer bytes (used when Modified is true or FilePath is empty)
	Modified    bool   // True if buffer has unsaved edits in memory
	PPI         int    // Target render resolution in pixels per inch
	SkipAnchors bool   // Skip the parallel PDF anchor compile when outline and cursor sync are inactive
}

// Compiler compiles a Typst document into a DocumentDeck of PNG pages.
type Compiler interface {
	CompilePages(ctx context.Context, req CompileRequest) (*DocumentDeck, error)
}

// TinymistDiscoveryFunc locates the tinymist binary path.
type TinymistDiscoveryFunc func(configuredPath string) (binPath string, err error)

// RootResolverFunc computes the Typst project root for a given .typ file path.
type RootResolverFunc func(filePath string) string

// TinymistCompiler invokes `tinymist compile --format png` in single-pass all-page mode.
type TinymistCompiler struct {
	DiscoverFn TinymistDiscoveryFunc
	ConfigPath func() string
}

// CompilePages compiles req into a temporary directory of per-page PNGs.
func (c *TinymistCompiler) CompilePages(ctx context.Context, req CompileRequest) (*DocumentDeck, error) {
	cfgPath := ""
	if c.ConfigPath != nil {
		cfgPath = c.ConfigPath()
	}
	var bin string
	var err error
	if c.DiscoverFn != nil {
		bin, err = c.DiscoverFn(cfgPath)
	} else {
		bin, err = exec.LookPath("tinymist")
	}
	if err != nil || bin == "" {
		if err != nil {
			return nil, fmt.Errorf("tinymist not found: %w", err)
		}
		return nil, fmt.Errorf("tinymist executable not found")
	}

	tmpDir, err := os.MkdirTemp("", "typx-preview-*")
	if err != nil {
		return nil, err
	}

	ppi := req.PPI
	if ppi < 72 {
		ppi = 72
	}
	if ppi > 300 {
		ppi = 300
	}

	rootDir := req.RootDir
	inputPath := req.FilePath

	// When the buffer has unsaved edits (or no backing path), compile a transient
	// shadow file placed in the same directory so relative & root imports still resolve.
	var shadowPath string
	if req.Modified || inputPath == "" {
		shadowDir := tmpDir
		if inputPath != "" {
			candidateDir := filepath.Dir(inputPath)
			if st, statErr := os.Stat(candidateDir); statErr == nil && st.IsDir() {
				shadowDir = candidateDir
			}
		}
		f, tmpErr := os.CreateTemp(shadowDir, ".typx-preview-*.typ")
		if tmpErr != nil && shadowDir != tmpDir {
			// Directory may be read-only; fall back to tmpDir.
			f, tmpErr = os.CreateTemp(tmpDir, "preview-*.typ")
		}
		if tmpErr != nil {
			os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("failed to stage unsaved buffer: %w", tmpErr)
		}
		shadowPath = f.Name()
		_, writeErr := f.Write(req.Content)
		closeErr := f.Close()
		defer os.Remove(shadowPath)
		if writeErr != nil {
			os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("failed to write staged buffer: %w", writeErr)
		}
		if closeErr != nil {
			os.RemoveAll(tmpDir)
			return nil, closeErr
		}
		inputPath = shadowPath
		if rootDir == "" || !isSubPath(rootDir, shadowPath) {
			rootDir = filepath.Dir(shadowPath)
		}
	}

	if rootDir == "" && inputPath != "" {
		rootDir = filepath.Dir(inputPath)
	}

	outTemplate := filepath.Join(tmpDir, "page-{p}.png")
	args := []string{
		"compile",
		"--root", rootDir,
		"--format", "png",
		"--ppi", strconv.Itoa(ppi),
		inputPath,
		outTemplate,
	}

	// In parallel, compile a lightweight PDF to extract exact heading/label
	// page + Y coordinates when outline or cursor sync needs anchors. PDF
	// compilation is non-fatal and skipped when SkipAnchors is set.
	pdfPath := filepath.Join(tmpDir, "outline.pdf")
	pdfCtx, cancelPDF := context.WithCancel(ctx)
	defer cancelPDF()
	var anchors *DocumentAnchors
	var pdfWG sync.WaitGroup
	if !req.SkipAnchors {
		pdfWG.Add(1)
		go func() {
			defer pdfWG.Done()
			defer os.Remove(pdfPath)
			pdfArgs := []string{
				"compile",
				"--root", rootDir,
				"--format", "pdf",
				inputPath,
				pdfPath,
			}
			pdfCmd := exec.CommandContext(pdfCtx, bin, pdfArgs...)
			if rootDir != "" {
				pdfCmd.Dir = rootDir
			}
			if err := pdfCmd.Run(); err != nil {
				return
			}
			if pdfBytes, err := os.ReadFile(pdfPath); err == nil {
				anchors = ParsePDFAnchors(pdfBytes)
			}
		}()
	}

	t0 := time.Now()
	cmd := exec.CommandContext(ctx, bin, args...)
	// Set working directory to rootDir to avoid tinymist's unnormalized relative
	// root path check on Windows when cwd is a subdirectory of --root.
	if rootDir != "" {
		cmd.Dir = rootDir
	}
	out, err := cmd.CombinedOutput()
	compileDur := time.Since(t0)
	if err != nil {
		// Cancel and wait for the PDF goroutine before removing tmpDir/shadowPath.
		cancelPDF()
		pdfWG.Wait()
		os.RemoveAll(tmpDir)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}

	pdfWG.Wait()
	deck, err := LoadDocumentDeckFromDir(tmpDir, compileDur)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, err
	}
	deck.anchors = anchors
	deck.anchorsChecked = !req.SkipAnchors
	return deck, nil
}

func isSubPath(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// pageSlot represents one compiled page in a continuous multi-page deck.
type pageSlot struct {
	pageNum int
	pngPath string
	srcW    int
	srcH    int
	raw     *image.RGBA

	contentHash [32]byte
	hasHash     bool

	scaled       *image.RGBA
	scaledW      int
	scaledInvert bool
	scaledTrim   bool
	cropTop      int
	cropH        int
}

// DocumentDeck holds a compiled document's per-page PNGs with lazy decoding,
// scaled image caching, and continuous vertical viewport compositing.
type DocumentDeck struct {
	tmpDir         string
	compileDur     time.Duration
	pages          []*pageSlot
	pageTops       []int
	pageHeights    []int
	totalHeight    int
	layoutW        int
	layoutTrim     bool
	layoutInvert   bool
	anchors        *DocumentAnchors
	anchorsChecked bool
}

func hashRGBAImage(img *image.RGBA) [32]byte {
	h := sha256.New()
	b := img.Bounds()
	w, height := b.Dx(), b.Dy()
	var hdr [9]byte
	hdr[0] = 'R'
	binary.LittleEndian.PutUint32(hdr[1:5], uint32(w))
	binary.LittleEndian.PutUint32(hdr[5:9], uint32(height))
	h.Write(hdr[:])
	for y := 0; y < height; y++ {
		off := img.PixOffset(b.Min.X, b.Min.Y+y)
		h.Write(img.Pix[off : off+w*4])
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// NewDocumentDeckFromImages builds an in-memory DocumentDeck directly from RGBA
// images (useful for unit tests without invoking tinymist).
func NewDocumentDeckFromImages(imgs []*image.RGBA, compileDur time.Duration) *DocumentDeck {
	pages := make([]*pageSlot, 0, len(imgs))
	for i, img := range imgs {
		if img == nil {
			continue
		}
		b := img.Bounds()
		pages = append(pages, &pageSlot{
			pageNum:     i + 1,
			srcW:        b.Dx(),
			srcH:        b.Dy(),
			raw:         img,
			contentHash: hashRGBAImage(img),
			hasHash:     true,
		})
	}
	return &DocumentDeck{
		compileDur: compileDur,
		pages:      pages,
	}
}

// DecodePageRGBA returns the decoded RGBA image for 0-based page index idx.
func (d *DocumentDeck) DecodePageRGBA(idx int) (*image.RGBA, error) {
	if d == nil || idx < 0 || idx >= len(d.pages) {
		return nil, fmt.Errorf("page index %d out of range", idx)
	}
	raw, err := d.pages[idx].ensureRaw()
	if err != nil {
		return nil, err
	}
	b := raw.Bounds()
	cp := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(cp, cp.Bounds(), raw, b.Min, draw.Src)
	return cp, nil
}

// CompileTypstToImages compiles a Typst document via the active preview Compiler
// and decodes up to maxPages (defaulting to 1) as RGBA images.
func CompileTypstToImages(ctx context.Context, req CompileRequest, maxPages int) ([]*image.RGBA, error) {
	if maxPages <= 0 {
		maxPages = 1
	}
	compiler := defaultManager.getCompiler()
	if compiler == nil {
		return nil, fmt.Errorf("preview compiler unavailable")
	}
	if req.PPI <= 0 {
		req.PPI = readGlobalInt("typst.previewPPI", 144)
		if req.PPI < 72 {
			req.PPI = 144
		}
	}
	deck, err := compiler.CompilePages(ctx, req)
	if err != nil {
		return nil, err
	}
	if deck == nil {
		return nil, fmt.Errorf("preview compiler returned empty deck")
	}
	defer deck.Close()

	n := deck.PageCount()
	if n > maxPages {
		n = maxPages
	}
	imgs := make([]*image.RGBA, 0, n)
	for i := 0; i < n; i++ {
		img, err := deck.DecodePageRGBA(i)
		if err != nil {
			return nil, err
		}
		imgs = append(imgs, img)
	}
	if len(imgs) == 0 {
		return nil, fmt.Errorf("compiled document produced zero pages")
	}
	return imgs, nil
}

// LoadDocumentDeckFromDir scans tmpDir for `page-<N>.png` files, reading only
// PNG headers up front so layout is instant and decoding stays lazy.
func LoadDocumentDeckFromDir(tmpDir string, compileDur time.Duration) (*DocumentDeck, error) {
	matches, _ := filepath.Glob(filepath.Join(tmpDir, "page-*.png"))
	if len(matches) == 0 {
		return nil, fmt.Errorf("no PNG pages produced by tinymist")
	}

	type entry struct {
		num  int
		path string
	}
	entries := make([]entry, 0, len(matches))
	for _, p := range matches {
		m := pageNumRe.FindStringSubmatch(filepath.Base(p))
		num := 1
		if m != nil {
			num, _ = strconv.Atoi(m[1])
		}
		entries = append(entries, entry{num: num, path: p})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].num < entries[j].num })

	pages := make([]*pageSlot, 0, len(entries))
	for _, e := range entries {
		f, err := os.Open(e.path)
		if err != nil {
			continue
		}
		cfg, _, err := image.DecodeConfig(f)
		var contentHash [32]byte
		var hasHash bool
		if err == nil && cfg.Width > 0 && cfg.Height > 0 {
			if _, seekErr := f.Seek(0, io.SeekStart); seekErr == nil {
				h := sha256.New()
				if _, copyErr := io.Copy(h, f); copyErr == nil {
					copy(contentHash[:], h.Sum(nil))
					hasHash = true
				}
			}
		}
		f.Close()
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
			continue
		}
		pages = append(pages, &pageSlot{
			pageNum:     e.num,
			pngPath:     e.path,
			srcW:        cfg.Width,
			srcH:        cfg.Height,
			contentHash: contentHash,
			hasHash:     hasHash,
		})
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("failed to decode PNG page headers")
	}

	return &DocumentDeck{
		tmpDir:     tmpDir,
		compileDur: compileDur,
		pages:      pages,
	}, nil
}

// Close deletes any temporary directory backing this deck. Safe to call multiple times.
func (d *DocumentDeck) Close() {
	if d != nil && d.tmpDir != "" {
		os.RemoveAll(d.tmpDir)
		d.tmpDir = ""
	}
}

// PageCount returns the number of pages in this deck.
func (d *DocumentDeck) PageCount() int {
	if d == nil {
		return 0
	}
	return len(d.pages)
}

// PageHash returns the SHA-256 content hash for 0-based page index idx.
func (d *DocumentDeck) PageHash(idx int) ([32]byte, bool) {
	if d == nil || idx < 0 || idx >= len(d.pages) || d.pages[idx] == nil {
		return [32]byte{}, false
	}
	return d.pages[idx].contentHash, d.pages[idx].hasHash
}

// CarryForwardFrom reuses decoded raw and scaled RGBA images from old for any
// page whose content hash and source dimensions match. Must be called before
// old.Close() (and on the main UI loop). Returns the number of carried pages.
func (d *DocumentDeck) CarryForwardFrom(old *DocumentDeck) int {
	if d == nil || old == nil || d == old || len(d.pages) == 0 || len(old.pages) == 0 {
		return 0
	}
	byHash := make(map[[32]byte]*pageSlot, len(old.pages))
	for _, op := range old.pages {
		if op == nil || !op.hasHash || (op.raw == nil && op.scaled == nil) {
			continue
		}
		if existing, exists := byHash[op.contentHash]; !exists || (existing.scaled == nil && op.scaled != nil) {
			byHash[op.contentHash] = op
		}
	}
	if len(byHash) == 0 {
		return 0
	}
	carried := 0
	for _, np := range d.pages {
		if np == nil || !np.hasHash {
			continue
		}
		op, ok := byHash[np.contentHash]
		if !ok || op.srcW != np.srcW || op.srcH != np.srcH {
			continue
		}
		np.raw = op.raw
		if op.scaled != nil {
			np.scaled = op.scaled
			np.scaledW = op.scaledW
			np.scaledInvert = op.scaledInvert
			np.scaledTrim = op.scaledTrim
			np.cropTop = op.cropTop
			np.cropH = op.cropH
		}
		carried++
	}
	return carried
}

// CompileDuration returns how long tinymist took to compile this deck.
func (d *DocumentDeck) CompileDuration() time.Duration {
	if d == nil {
		return 0
	}
	return d.compileDur
}

// Anchors returns the PDF outline/destination anchors attached to this deck, if any.
func (d *DocumentDeck) Anchors() *DocumentAnchors {
	if d == nil {
		return nil
	}
	return d.anchors
}

// SetAnchors attaches PDF outline/destination anchors to this deck.
func (d *DocumentDeck) SetAnchors(a *DocumentAnchors) {
	if d != nil {
		d.anchors = a
	}
}

func (p *pageSlot) ensureRaw() (*image.RGBA, error) {
	if p.raw != nil {
		return p.raw, nil
	}
	f, err := os.Open(p.pngPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	p.raw = rgba
	return p.raw, nil
}

func (p *pageSlot) ensureScaled(dstW int, invert, trim bool) (*image.RGBA, error) {
	if p.scaled != nil && p.scaledW == dstW && p.scaledInvert == invert && p.scaledTrim == trim {
		return p.scaled, nil
	}
	raw, err := p.ensureRaw()
	if err != nil {
		return nil, err
	}
	src := raw
	cropTop, cropH := 0, raw.Bounds().Dy()
	if trim {
		src, cropTop, cropH = cropWhitespaceWithBounds(raw, 24)
	}
	scaled := scaleArea(src, dstW, invert)
	p.scaled = scaled
	p.scaledW = dstW
	p.scaledInvert = invert
	p.scaledTrim = trim
	p.cropTop = cropTop
	p.cropH = cropH
	return scaled, nil
}

// ComputeLayout computes virtual Y tops and scaled heights for all pages.
func (d *DocumentDeck) ComputeLayout(dstW int, trim bool) {
	if d == nil || len(d.pages) == 0 || dstW <= 0 {
		return
	}
	n := len(d.pages)
	d.pageTops = make([]int, n)
	d.pageHeights = make([]int, n)
	y := 0
	for i, p := range d.pages {
		d.pageTops[i] = y
		var h int
		if p.scaled != nil && p.scaledW == dstW && p.scaledTrim == trim {
			h = p.scaled.Bounds().Dy()
		} else {
			h = (p.srcH * dstW) / p.srcW
			if h < 1 {
				h = 1
			}
		}
		d.pageHeights[i] = h
		y += h
		if i < n-1 {
			y += pageGapPx
		}
	}
	d.totalHeight = y
	d.layoutW = dstW
	d.layoutTrim = trim
}

// PageTop returns the virtual Y coordinate of the 0-based page index.
func (d *DocumentDeck) PageTop(idx int) int {
	if d == nil || idx < 0 || idx >= len(d.pageTops) {
		return 0
	}
	return d.pageTops[idx]
}

// TotalHeight returns the full stacked virtual height in pixels.
func (d *DocumentDeck) TotalHeight() int {
	if d == nil {
		return 0
	}
	return d.totalHeight
}

func (d *DocumentDeck) visiblePageRange(scrollY, viewH int) (firstPage, lastPage int) {
	if len(d.pages) == 0 {
		return 0, 0
	}
	y0 := scrollY
	y1 := scrollY + viewH
	first, last := d.pages[0].pageNum, d.pages[0].pageNum
	found := false
	for i := range d.pages {
		top := d.pageTops[i]
		bot := top + d.pageHeights[i]
		if bot > y0 && top < y1 {
			if !found {
				first = d.pages[i].pageNum
				found = true
			}
			last = d.pages[i].pageNum
		}
	}
	return first, last
}

// ViewportComposite holds an uncompressed composited viewport RGBA image and layout metrics.
type ViewportComposite struct {
	Image        *image.RGBA
	ScaledW      int
	DstW         int
	DstH         int
	ComposeDur   time.Duration
	FirstPage    int
	LastPage     int
	PageCount    int
	ScrollY      int
	MaxScrollY   int
	ScrollX      int
	MaxScrollX   int
	ViewportHash [32]byte
	HasHash      bool
}

// ViewportRender holds a single encoded continuous viewport frame (Sixel or Kitty).
type ViewportRender struct {
	Protocol     GraphicsProtocol
	Payload      []byte
	Sixel        []byte
	ScaledW      int
	DstW         int
	DstH         int
	ComposeDur   time.Duration
	EncodeDur    time.Duration
	FirstPage    int
	LastPage     int
	PageCount    int
	ScrollY      int
	MaxScrollY   int
	ScrollX      int
	MaxScrollX   int
	ViewportHash [32]byte
	HasHash      bool
	Reused       bool
}

type preparedViewport struct {
	t0            time.Time
	scaledW       int
	viewW         int
	outW          int
	outH          int
	scrollY       int
	maxScrollY    int
	scrollX       int
	maxScrollX    int
	invert        bool
	trim          bool
	firstPage     int
	lastPage      int
	compositeHash [32]byte
	hasHash       bool
}

func (d *DocumentDeck) prepareViewport(scaledW, viewW, viewH, scrollY, scrollX int, invert, trim bool) (*preparedViewport, error) {
	if d == nil || len(d.pages) == 0 {
		return nil, fmt.Errorf("empty document deck")
	}
	if scaledW < 16 {
		scaledW = 16
	}
	if viewW < 16 {
		viewW = 16
	}
	if viewH < 16 {
		viewH = 16
	}

	t0 := time.Now()
	d.ComputeLayout(scaledW, trim)
	d.layoutInvert = invert

	clampY := func(y int) (int, int) {
		maxY := d.totalHeight - viewH
		if maxY < 0 {
			maxY = 0
		}
		if y < 0 {
			y = 0
		}
		if y > maxY {
			y = maxY
		}
		return y, maxY
	}

	var maxScrollY int
	scrollY, maxScrollY = clampY(scrollY)

	for pass := 0; pass < 3; pass++ {
		layoutChanged := false
		for i, p := range d.pages {
			top := d.pageTops[i]
			bot := top + d.pageHeights[i]
			if bot <= scrollY || top >= scrollY+viewH {
				continue
			}
			sc, err := p.ensureScaled(scaledW, invert, trim)
			if err != nil {
				return nil, err
			}
			if sc.Bounds().Dy() != d.pageHeights[i] {
				layoutChanged = true
			}
		}
		if !layoutChanged {
			break
		}
		d.ComputeLayout(scaledW, trim)
		scrollY, maxScrollY = clampY(scrollY)
	}

	outH := viewH
	if d.totalHeight < outH {
		outH = d.totalHeight
	}
	if outH < 1 {
		outH = 1
	}

	outW := scaledW
	if viewW < outW {
		outW = viewW
	}
	if outW < 1 {
		outW = 1
	}

	maxScrollX := scaledW - outW
	if maxScrollX < 0 {
		maxScrollX = 0
	}
	if scrollX < 0 {
		scrollX = 0
	}
	if scrollX > maxScrollX {
		scrollX = maxScrollX
	}

	fp, lp := d.visiblePageRange(scrollY, outH)

	h := sha256.New()
	var buf [64]byte
	binary.LittleEndian.PutUint64(buf[0:8], uint64(scaledW))
	binary.LittleEndian.PutUint64(buf[8:16], uint64(viewW))
	binary.LittleEndian.PutUint64(buf[16:24], uint64(outW))
	binary.LittleEndian.PutUint64(buf[24:32], uint64(outH))
	binary.LittleEndian.PutUint64(buf[32:40], uint64(scrollY))
	binary.LittleEndian.PutUint64(buf[40:48], uint64(scrollX))
	if invert {
		buf[48] = 1
	}
	if trim {
		buf[49] = 1
	}
	h.Write(buf[:50])

	hasHash := true
	visCount := 0
	for i, p := range d.pages {
		top := d.pageTops[i]
		bot := top + d.pageHeights[i]
		if bot <= scrollY || top >= scrollY+outH {
			continue
		}
		if !p.hasHash {
			hasHash = false
			break
		}
		visCount++
		binary.LittleEndian.PutUint64(buf[0:8], uint64(i))
		binary.LittleEndian.PutUint64(buf[8:16], uint64(top))
		binary.LittleEndian.PutUint64(buf[16:24], uint64(d.pageHeights[i]))
		binary.LittleEndian.PutUint64(buf[24:32], uint64(p.srcW))
		binary.LittleEndian.PutUint64(buf[32:40], uint64(p.srcH))
		h.Write(buf[:40])
		h.Write(p.contentHash[:])
	}
	binary.LittleEndian.PutUint64(buf[0:8], uint64(visCount))
	h.Write(buf[:8])

	var compositeHash [32]byte
	if hasHash {
		copy(compositeHash[:], h.Sum(nil))
	}

	return &preparedViewport{
		t0:            t0,
		scaledW:       scaledW,
		viewW:         viewW,
		outW:          outW,
		outH:          outH,
		scrollY:       scrollY,
		maxScrollY:    maxScrollY,
		scrollX:       scrollX,
		maxScrollX:    maxScrollX,
		invert:        invert,
		trim:          trim,
		firstPage:     fp,
		lastPage:      lp,
		compositeHash: compositeHash,
		hasHash:       hasHash,
	}, nil
}

func (d *DocumentDeck) compositePrepared(prep *preparedViewport) (*image.RGBA, error) {
	canvas := image.NewRGBA(image.Rect(0, 0, prep.outW, prep.outH))
	gapCol := color.RGBA{R: 22, G: 25, B: 34, A: 255}
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: gapCol}, image.Point{}, draw.Src)

	for i, p := range d.pages {
		top := d.pageTops[i]
		bot := top + d.pageHeights[i]
		if bot <= prep.scrollY || top >= prep.scrollY+prep.outH {
			continue
		}
		sc, err := p.ensureScaled(prep.scaledW, prep.invert, prep.trim)
		if err != nil {
			return nil, err
		}
		visTop := top
		if visTop < prep.scrollY {
			visTop = prep.scrollY
		}
		visBot := bot
		if visBot > prep.scrollY+prep.outH {
			visBot = prep.scrollY + prep.outH
		}
		dstRect := image.Rect(0, visTop-prep.scrollY, prep.outW, visBot-prep.scrollY)
		srcPt := image.Pt(prep.scrollX, visTop-top)
		draw.Draw(canvas, dstRect, sc, srcPt, draw.Src)
	}
	return canvas, nil
}

// CompositeViewport composites the visible slice [scrollY, scrollY+viewH) and
// horizontal slice [scrollX, scrollX+outW) from the scaledW page canvas.
func (d *DocumentDeck) CompositeViewport(scaledW, viewW, viewH, scrollY, scrollX int, invert, trim bool) (*ViewportComposite, error) {
	prep, err := d.prepareViewport(scaledW, viewW, viewH, scrollY, scrollX, invert, trim)
	if err != nil {
		return nil, err
	}
	canvas, err := d.compositePrepared(prep)
	if err != nil {
		return nil, err
	}
	return &ViewportComposite{
		Image:        canvas,
		ScaledW:      prep.scaledW,
		DstW:         prep.outW,
		DstH:         prep.outH,
		ComposeDur:   time.Since(prep.t0),
		FirstPage:    prep.firstPage,
		LastPage:     prep.lastPage,
		PageCount:    len(d.pages),
		ScrollY:      prep.scrollY,
		MaxScrollY:   prep.maxScrollY,
		ScrollX:      prep.scrollX,
		MaxScrollX:   prep.maxScrollX,
		ViewportHash: prep.compositeHash,
		HasHash:      prep.hasHash,
	}, nil
}

// RenderViewport composites the visible slice [scrollY, scrollY+viewH) across
// all intersecting pages, strictly clamping scrollY to [0, MaxScrollY] so there
// is never any overscroll debt, and encodes it using the active terminal protocol.
func (d *DocumentDeck) RenderViewport(dstW, viewH, scrollY int, invert, trim bool) (*ViewportRender, error) {
	return d.RenderViewportWithProtocol(dstW, viewH, scrollY, invert, trim, ActiveProtocol())
}

// RenderViewportWithProtocol renders the visible viewport slice encoded with proto.
// ProtocolNone falls back to Sixel so forced `:preview inline` calls remain usable.
func (d *DocumentDeck) RenderViewportWithProtocol(dstW, viewH, scrollY int, invert, trim bool, proto GraphicsProtocol) (*ViewportRender, error) {
	return d.RenderViewportCached(dstW, dstW, viewH, scrollY, 0, invert, trim, proto, nil)
}

// RenderViewportCached composites and encodes the visible viewport slice, reusing
// prev's encoded Payload/Sixel and skipping compositing + encoding when the visible
// viewport parameters and visible page content hashes match prev.
func (d *DocumentDeck) RenderViewportCached(scaledW, viewW, viewH, scrollY, scrollX int, invert, trim bool, proto GraphicsProtocol, prev *ViewportRender) (*ViewportRender, error) {
	prep, err := d.prepareViewport(scaledW, viewW, viewH, scrollY, scrollX, invert, trim)
	if err != nil {
		return nil, err
	}

	emitProto := proto
	if emitProto != ProtocolKitty {
		emitProto = ProtocolSixel
	}
	tmux := emitProto == ProtocolKitty && UseTmuxPassthrough()

	var fullHash [32]byte
	if prep.hasHash {
		h := sha256.New()
		h.Write(prep.compositeHash[:])
		var tag [2]byte
		tag[0] = byte(emitProto)
		if tmux {
			tag[1] = 1
		}
		h.Write(tag[:])
		copy(fullHash[:], h.Sum(nil))
	}

	if prev != nil && prep.hasHash && prev.HasHash &&
		prev.ViewportHash == fullHash && prev.Protocol == emitProto &&
		prev.DstW == prep.outW && prev.DstH == prep.outH &&
		len(prev.Payload) > 0 {
		return &ViewportRender{
			Protocol:     emitProto,
			Payload:      prev.Payload,
			Sixel:        prev.Sixel,
			ScaledW:      prep.scaledW,
			DstW:         prep.outW,
			DstH:         prep.outH,
			ComposeDur:   time.Since(prep.t0),
			EncodeDur:    0,
			FirstPage:    prep.firstPage,
			LastPage:     prep.lastPage,
			PageCount:    len(d.pages),
			ScrollY:      prep.scrollY,
			MaxScrollY:   prep.maxScrollY,
			ScrollX:      prep.scrollX,
			MaxScrollX:   prep.maxScrollX,
			ViewportHash: fullHash,
			HasHash:      true,
			Reused:       true,
		}, nil
	}

	canvas, err := d.compositePrepared(prep)
	if err != nil {
		return nil, err
	}
	composeDur := time.Since(prep.t0)

	tEnc := time.Now()
	var payload []byte
	var sixel []byte
	if emitProto == ProtocolKitty {
		payload = EncodeKittyWithPassthrough(canvas, tmux)
	} else {
		sixel = EncodeSixel(canvas)
		payload = sixel
	}
	encodeDur := time.Since(tEnc)

	return &ViewportRender{
		Protocol:     emitProto,
		Payload:      payload,
		Sixel:        sixel,
		ScaledW:      prep.scaledW,
		DstW:         prep.outW,
		DstH:         prep.outH,
		ComposeDur:   composeDur,
		EncodeDur:    encodeDur,
		FirstPage:    prep.firstPage,
		LastPage:     prep.lastPage,
		PageCount:    len(d.pages),
		ScrollY:      prep.scrollY,
		MaxScrollY:   prep.maxScrollY,
		ScrollX:      prep.scrollX,
		MaxScrollX:   prep.maxScrollX,
		ViewportHash: fullHash,
		HasHash:      prep.hasHash,
		Reused:       false,
	}, nil
}
