package preview

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/png"
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
	FilePath string // Absolute path on disk (may be empty for unnamed buffers)
	RootDir  string // Typst project root directory
	Content  []byte // In-memory buffer bytes (used when Modified is true or FilePath is empty)
	Modified bool   // True if buffer has unsaved edits in memory
	PPI      int    // Target render resolution in pixels per inch
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
	// page + Y coordinates for outline-to-preview jump synchronization. PDF
	// compilation is non-fatal and typically finishes faster than PNG rasterization.
	pdfPath := filepath.Join(tmpDir, "outline.pdf")
	pdfCtx, cancelPDF := context.WithCancel(ctx)
	defer cancelPDF()
	var anchors *DocumentAnchors
	var pdfWG sync.WaitGroup
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
	tmpDir      string
	compileDur  time.Duration
	pages       []*pageSlot
	pageTops    []int
	pageHeights []int
	totalHeight int
	layoutW     int
	layoutTrim  bool
	anchors     *DocumentAnchors
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
			pageNum: i + 1,
			srcW:    b.Dx(),
			srcH:    b.Dy(),
			raw:     img,
		})
	}
	return &DocumentDeck{
		compileDur: compileDur,
		pages:      pages,
	}
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
		f.Close()
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
			continue
		}
		pages = append(pages, &pageSlot{
			pageNum: e.num,
			pngPath: e.path,
			srcW:    cfg.Width,
			srcH:    cfg.Height,
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

// ViewportRender holds a single Sixel-encoded continuous viewport frame.
type ViewportRender struct {
	Sixel      []byte
	DstW       int
	DstH       int
	ComposeDur time.Duration
	EncodeDur  time.Duration
	FirstPage  int
	LastPage   int
	PageCount  int
	ScrollY    int
	MaxScrollY int
}

// RenderViewport composites the visible slice [scrollY, scrollY+viewH) across
// all intersecting pages, strictly clamping scrollY to [0, MaxScrollY] so there
// is never any overscroll debt.
func (d *DocumentDeck) RenderViewport(dstW, viewH, scrollY int, invert, trim bool) (*ViewportRender, error) {
	if d == nil || len(d.pages) == 0 {
		return nil, fmt.Errorf("empty document deck")
	}
	if dstW < 16 {
		dstW = 16
	}
	if viewH < 16 {
		viewH = 16
	}

	t0 := time.Now()
	d.ComputeLayout(dstW, trim)

	maxScrollY := d.totalHeight - viewH
	if maxScrollY < 0 {
		maxScrollY = 0
	}
	if scrollY < 0 {
		scrollY = 0
	}
	if scrollY > maxScrollY {
		scrollY = maxScrollY
	}

	layoutChanged := false
	for i, p := range d.pages {
		top := d.pageTops[i]
		bot := top + d.pageHeights[i]
		if bot <= scrollY || top >= scrollY+viewH {
			continue
		}
		sc, err := p.ensureScaled(dstW, invert, trim)
		if err != nil {
			return nil, err
		}
		if sc.Bounds().Dy() != d.pageHeights[i] {
			layoutChanged = true
		}
	}
	if layoutChanged {
		d.ComputeLayout(dstW, trim)
		maxScrollY = d.totalHeight - viewH
		if maxScrollY < 0 {
			maxScrollY = 0
		}
		if scrollY > maxScrollY {
			scrollY = maxScrollY
		}
		for i, p := range d.pages {
			top := d.pageTops[i]
			bot := top + d.pageHeights[i]
			if bot <= scrollY || top >= scrollY+viewH {
				continue
			}
			if _, err := p.ensureScaled(dstW, invert, trim); err != nil {
				return nil, err
			}
		}
	}

	outH := viewH
	if d.totalHeight < outH {
		outH = d.totalHeight
	}
	if outH < 1 {
		outH = 1
	}

	canvas := image.NewRGBA(image.Rect(0, 0, dstW, outH))
	gapCol := color.RGBA{R: 22, G: 25, B: 34, A: 255}
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: gapCol}, image.Point{}, draw.Src)

	for i, p := range d.pages {
		top := d.pageTops[i]
		bot := top + d.pageHeights[i]
		if bot <= scrollY || top >= scrollY+outH {
			continue
		}
		sc, err := p.ensureScaled(dstW, invert, trim)
		if err != nil {
			return nil, err
		}
		visTop := top
		if visTop < scrollY {
			visTop = scrollY
		}
		visBot := bot
		if visBot > scrollY+outH {
			visBot = scrollY + outH
		}
		dstRect := image.Rect(0, visTop-scrollY, dstW, visBot-scrollY)
		srcPt := image.Pt(0, visTop-top)
		draw.Draw(canvas, dstRect, sc, srcPt, draw.Src)
	}
	composeDur := time.Since(t0)

	tEnc := time.Now()
	sixel := EncodeSixel(canvas)
	encodeDur := time.Since(tEnc)

	fp, lp := d.visiblePageRange(scrollY, outH)
	return &ViewportRender{
		Sixel:      sixel,
		DstW:       dstW,
		DstH:       outH,
		ComposeDur: composeDur,
		EncodeDur:  encodeDur,
		FirstPage:  fp,
		LastPage:   lp,
		PageCount:  len(d.pages),
		ScrollY:    scrollY,
		MaxScrollY: maxScrollY,
	}, nil
}
