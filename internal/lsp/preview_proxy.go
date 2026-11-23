package lsp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const preferredPreviewProxyAddr = "127.0.0.1:23628"

// previewStatusPayload is returned by /__typx_status for the browser preview
// loading/error overlay.
type previewStatusPayload struct {
	Phase         string   `json:"phase"`
	File          string   `json:"file"`
	Pages         int      `json:"pages"`
	ElapsedMs     int64    `json:"elapsedMs"`
	UpstreamReady bool     `json:"upstreamReady"`
	Errors        []string `json:"errors,omitempty"`
	Detail        string   `json:"detail,omitempty"`
}

// previewProxy is a lightweight localhost HTTP/WebSocket reverse proxy that
// sits in front of tinymist's preview server so the browser opens immediately
// with a live progress/error overlay instead of a blank gray canvas while
// tinymist starts or compiles large Typst documents.
type previewProxy struct {
	ln        net.Listener
	srv       *http.Server
	client    *http.Client
	url       string
	fileName  string
	startedAt time.Time
	statusFn  func() previewStatusPayload

	mu           sync.RWMutex
	upstreamURL  *url.URL
	revProxy     *httputil.ReverseProxy
	cachedRoot   []byte
	cachedHeader http.Header
	cachedCode   int
	cachedETag   string
	closed       bool
}

func startPreviewProxy(filePath string, statusFn func() previewStatusPayload) (*previewProxy, error) {
	// Prefer a stable localhost port so browsers can reuse compiled WASM/V8
	// caches across previews on the same origin; fall back to an ephemeral port
	// when another TypX window already owns it.
	ln, err := net.Listen("tcp", preferredPreviewProxyAddr)
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
	}
	p := &previewProxy{
		ln:        ln,
		client:    &http.Client{Timeout: 10 * time.Second},
		url:       "http://" + ln.Addr().String(),
		fileName:  filepath.Base(filePath),
		startedAt: time.Now(),
		statusFn:  statusFn,
	}
	p.srv = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = p.srv.Serve(ln)
	}()
	return p, nil
}

func (p *previewProxy) URL() string {
	return p.url
}

func (p *previewProxy) SetUpstream(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.FlushInterval = -1
	origDirector := rp.Director
	rp.Director = func(req *http.Request) {
		origDirector(req)
		req.Host = u.Host
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, renderWaitingHTML(p.fileName, "Connecting to tinymist preview server..."))
	}

	p.mu.Lock()
	p.upstreamURL = u
	p.revProxy = rp
	p.cachedRoot = nil
	p.cachedHeader = nil
	p.cachedCode = 0
	p.cachedETag = ""
	p.mu.Unlock()

	// Pre-warm the 2MB root HTML in memory while the OS opens the browser tab.
	go p.prefetchRoot(u)
	return nil
}

func (p *previewProxy) prefetchRoot(upstream *url.URL) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _, _, _ = p.fetchAndCacheRoot(ctx, http.MethodGet, "/", "", upstream)
}

func (p *previewProxy) HasUpstream() bool {
	p.mu.RLock()
	ok := p.upstreamURL != nil
	p.mu.RUnlock()
	return ok
}

func (p *previewProxy) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	_ = p.srv.Shutdown(ctx)
	cancel()
	_ = p.ln.Close()
}

func (p *previewProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/__typx_status" {
		p.serveStatus(w)
		return
	}

	p.mu.RLock()
	upstream := p.upstreamURL
	rp := p.revProxy
	p.mu.RUnlock()

	if upstream == nil || rp == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, renderWaitingHTML(p.fileName, "Starting tinymist preview server..."))
		return
	}

	// WebSocket upgrades and non-root asset paths pass straight through.
	if isWebSocketUpgrade(r) || (r.URL.Path != "/" && r.URL.Path != "/index.html") {
		rp.ServeHTTP(w, r)
		return
	}

	// Root HTML: fetch from tinymist and inject the live progress/error overlay.
	p.serveInjectedRoot(w, r, upstream)
}

func (p *previewProxy) serveStatus(w http.ResponseWriter) {
	var payload previewStatusPayload
	if p.statusFn != nil {
		payload = p.statusFn()
	}
	if payload.File == "" {
		payload.File = p.fileName
	}
	payload.UpstreamReady = p.HasUpstream()
	if payload.ElapsedMs == 0 {
		payload.ElapsedMs = time.Since(p.startedAt).Milliseconds()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(payload)
}

func (p *previewProxy) serveInjectedRoot(w http.ResponseWriter, r *http.Request, upstream *url.URL) {
	if r.Method == http.MethodGet && r.URL.RawQuery == "" {
		p.mu.RLock()
		cached := p.cachedRoot
		hdr := p.cachedHeader
		code := p.cachedCode
		etag := p.cachedETag
		p.mu.RUnlock()
		if len(cached) > 0 {
			writeRootResponse(w, r, cached, hdr, code, etag)
			return
		}
	}

	body, hdr, code, etag, err := p.fetchAndCacheRoot(r.Context(), r.Method, r.URL.Path, r.URL.RawQuery, upstream)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, renderWaitingHTML(p.fileName, "Waiting for tinymist preview server..."))
		return
	}
	writeRootResponse(w, r, body, hdr, code, etag)
}

func writeRootResponse(w http.ResponseWriter, r *http.Request, body []byte, hdr http.Header, code int, etag string) {
	for k, vals := range hdr {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	if etag != "" {
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

func (p *previewProxy) fetchAndCacheRoot(ctx context.Context, method, path, rawQuery string, upstream *url.URL) ([]byte, http.Header, int, string, error) {
	target := *upstream
	target.Path = path
	target.RawQuery = rawQuery

	req, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return nil, nil, 0, "", err
	}
	req.Header.Set("Accept", "text/html,*/*")
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, nil, 0, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, 0, "", err
	}

	hdr := make(http.Header, len(resp.Header))
	for k, vals := range resp.Header {
		if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Content-Encoding") || strings.EqualFold(k, "Cache-Control") || strings.EqualFold(k, "ETag") {
			continue
		}
		for _, v := range vals {
			hdr.Add(k, v)
		}
	}

	var etag string
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		body = injectOverlayIntoHTML(body, p.fileName)
		if method == http.MethodGet && rawQuery == "" && resp.StatusCode == http.StatusOK {
			sum := sha256.Sum256(body)
			etag = `"` + hex.EncodeToString(sum[:12]) + `"`
			p.mu.Lock()
			if p.upstreamURL == upstream {
				p.cachedRoot = body
				p.cachedHeader = hdr
				p.cachedCode = resp.StatusCode
				p.cachedETag = etag
			}
			p.mu.Unlock()
		}
	}
	return body, hdr, resp.StatusCode, etag, nil
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func injectOverlayIntoHTML(raw []byte, fileName string) []byte {
	snippet := []byte(renderOverlaySnippet(fileName))
	// Only scan the tail of the HTML for </body> to avoid lowercasing 2MB+ bundles.
	tailStart := len(raw) - 512
	if tailStart < 0 {
		tailStart = 0
	}
	tailLower := bytes.ToLower(raw[tailStart:])
	if idx := bytes.LastIndex(tailLower, []byte("</body>")); idx >= 0 {
		pos := tailStart + idx
		out := make([]byte, 0, len(raw)+len(snippet))
		out = append(out, raw[:pos]...)
		out = append(out, snippet...)
		out = append(out, raw[pos:]...)
		return out
	}
	return append(raw, snippet...)
}

func renderWaitingHTML(fileName, subtitle string) string {
	safeFile := html.EscapeString(fileName)
	safeSub := html.EscapeString(subtitle)
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=device-width, initial-scale=1.0" />
<title>%s — Loading Preview</title>
<style>
  html, body { margin: 0; height: 100%%; background: #1e222a; color: #e5e9f0; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; display: flex; align-items: center; justify-content: center; }
  .card { background: #262b36; border: 1px solid #3b4252; border-radius: 12px; padding: 28px 36px; min-width: 340px; max-width: 560px; box-shadow: 0 12px 32px rgba(0,0,0,0.35); text-align: center; }
  .spinner { width: 34px; height: 34px; border: 3px solid #3b4252; border-top-color: #69d4e4; border-radius: 50%%; margin: 0 auto 16px; animation: spin 0.8s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  .title { font-size: 16px; font-weight: 600; margin-bottom: 6px; }
  .sub { font-size: 13px; color: #9aa5b8; margin-bottom: 10px; }
  .timer { font-size: 12px; color: #69d4e4; font-variant-numeric: tabular-nums; }
  .errs { text-align: left; margin-top: 14px; padding: 10px 12px; background: rgba(224,108,117,0.12); border: 1px solid rgba(224,108,117,0.45); border-radius: 8px; color: #f4a4ab; font-family: ui-monospace, Consolas, monospace; font-size: 12px; white-space: pre-wrap; max-height: 220px; overflow: auto; display: none; }
</style>
</head>
<body>
<div class="card">
  <div id="sp" class="spinner"></div>
  <div id="title" class="title">Preparing %s</div>
  <div id="sub" class="sub">%s</div>
  <div id="timer" class="timer">0.0s</div>
  <div id="errs" class="errs"></div>
</div>
<script>
(function() {
  var start = Date.now();
  var timerEl = document.getElementById('timer');
  var subEl = document.getElementById('sub');
  var titleEl = document.getElementById('title');
  var errsEl = document.getElementById('errs');
  var spEl = document.getElementById('sp');
  setInterval(function() {
    timerEl.textContent = ((Date.now() - start) / 1000).toFixed(1) + 's';
  }, 100);
  function poll() {
    fetch('/__typx_status', { cache: 'no-store' }).then(function(r) { return r.json(); }).then(function(st) {
      if (st.upstreamReady) {
        subEl.textContent = 'Preview server ready — loading document...';
        window.location.reload();
        return;
      }
      if (st.detail) subEl.textContent = st.detail;
      if (st.phase === 'error' && st.errors && st.errors.length) {
        spEl.style.display = 'none';
        titleEl.textContent = 'Preview Failed';
        errsEl.style.display = 'block';
        errsEl.textContent = st.errors.join('\n');
      }
    }).catch(function() {});
  }
  poll();
  setInterval(poll, 120);
})();
</script>
</body>
</html>`, safeFile, safeFile, safeSub)
}

func renderOverlaySnippet(fileName string) string {
	safeFile := html.EscapeString(fileName)
	return fmt.Sprintf(`
<!-- TypX live loading/error overlay -->
<style id="typx-overlay-style">
  #typx-overlay { position: fixed; inset: 0; z-index: 99999; display: flex; align-items: center; justify-content: center; background: rgba(30, 34, 42, 0.88); backdrop-filter: blur(2px); font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; color: #e5e9f0; transition: opacity 0.2s ease; }
  #typx-overlay.typx-hidden { opacity: 0; pointer-events: none; }
  #typx-overlay .typx-card { background: #262b36; border: 1px solid #3b4252; border-radius: 12px; padding: 24px 32px; min-width: 320px; max-width: 580px; box-shadow: 0 12px 32px rgba(0,0,0,0.4); text-align: center; }
  #typx-overlay .typx-spinner { width: 32px; height: 32px; border: 3px solid #3b4252; border-top-color: #69d4e4; border-radius: 50%%; margin: 0 auto 14px; animation: typx-spin 0.8s linear infinite; }
  @keyframes typx-spin { to { transform: rotate(360deg); } }
  #typx-overlay .typx-title { font-size: 15px; font-weight: 600; margin-bottom: 6px; }
  #typx-overlay .typx-sub { font-size: 13px; color: #9aa5b8; margin-bottom: 8px; }
  #typx-overlay .typx-timer { font-size: 12px; color: #69d4e4; font-variant-numeric: tabular-nums; }
  #typx-overlay .typx-errs { text-align: left; margin-top: 12px; padding: 10px 12px; background: rgba(224,108,117,0.12); border: 1px solid rgba(224,108,117,0.45); border-radius: 8px; color: #f4a4ab; font-family: ui-monospace, Consolas, monospace; font-size: 12px; white-space: pre-wrap; max-height: 240px; overflow: auto; display: none; }
  #typx-pill { position: fixed; top: 12px; right: 16px; z-index: 99998; display: none; align-items: center; gap: 8px; padding: 6px 12px; border-radius: 999px; background: rgba(38, 43, 54, 0.92); border: 1px solid #3b4252; color: #e5e9f0; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; font-size: 12px; box-shadow: 0 4px 12px rgba(0,0,0,0.25); }
  #typx-pill.typx-show { display: inline-flex; }
  #typx-pill.typx-err { border-color: rgba(224,108,117,0.7); color: #f4a4ab; }
  #typx-pill .typx-dot { width: 10px; height: 10px; border: 2px solid #3b4252; border-top-color: #69d4e4; border-radius: 50%%; animation: typx-spin 0.8s linear infinite; }
</style>
<div id="typx-overlay">
  <div class="typx-card">
    <div id="typx-ov-spin" class="typx-spinner"></div>
    <div id="typx-ov-title" class="typx-title">Compiling %s</div>
    <div id="typx-ov-sub" class="typx-sub">Rendering Typst document...</div>
    <div id="typx-ov-timer" class="typx-timer">0.0s</div>
    <div id="typx-ov-errs" class="typx-errs"></div>
  </div>
</div>
<div id="typx-pill"><span id="typx-pill-dot" class="typx-dot"></span><span id="typx-pill-text">Compiling...</span></div>
<script>
(function() {
  var overlay = document.getElementById('typx-overlay');
  var ovSpin = document.getElementById('typx-ov-spin');
  var ovTitle = document.getElementById('typx-ov-title');
  var ovSub = document.getElementById('typx-ov-sub');
  var ovTimer = document.getElementById('typx-ov-timer');
  var ovErrs = document.getElementById('typx-ov-errs');
  var pill = document.getElementById('typx-pill');
  var pillDot = document.getElementById('typx-pill-dot');
  var pillText = document.getElementById('typx-pill-text');
  var start = Date.now();
  var hasRendered = false;
  var mo = null;
  var timerId = null;
  var rafPending = false;

  function markRendered() {
    hasRendered = true;
    overlay.classList.add('typx-hidden');
    if (mo) {
      mo.disconnect();
      mo = null;
    }
    if (timerId) {
      clearInterval(timerId);
      timerId = null;
    }
  }

  function checkRendered() {
    if (hasRendered) return true;
    var app = document.getElementById('typst-app');
    if (!app || app.childElementCount === 0) return false;
    var el = app.querySelector('svg, canvas, .typst-page, [data-page-number]');
    if (el) {
      var rect = el.getBoundingClientRect();
      if (rect.width > 10 && rect.height > 10) {
        markRendered();
        return true;
      }
    }
    return false;
  }

  var appEl = document.getElementById('typst-app') || document.body;
  mo = new MutationObserver(function() {
    if (hasRendered || rafPending) return;
    rafPending = true;
    requestAnimationFrame(function() {
      rafPending = false;
      checkRendered();
    });
  });
  mo.observe(appEl, { childList: true });

  timerId = setInterval(function() {
    if (!checkRendered()) {
      ovTimer.textContent = ((Date.now() - start) / 1000).toFixed(1) + 's';
    }
  }, 200);

  function pollStatus() {
    fetch('/__typx_status', { cache: 'no-store' }).then(function(r) { return r.json(); }).then(function(st) {
      if (!hasRendered) checkRendered();
      var hasErr = st.phase === 'error' || (st.errors && st.errors.length > 0);
      if (!hasRendered) {
        if (hasErr) {
          ovSpin.style.display = 'none';
          ovTitle.textContent = 'Compile Error — ' + (st.file || '%s');
          ovSub.textContent = 'Fix the Typst error in TypX to render the preview:';
          ovErrs.style.display = 'block';
          ovErrs.textContent = (st.errors && st.errors.length) ? st.errors.join('\n') : 'Compilation failed (run :diagnostics in TypX)';
        } else {
          ovSpin.style.display = 'block';
          ovErrs.style.display = 'none';
          ovTitle.textContent = (st.phase === 'compiling' ? 'Compiling ' : 'Rendering ') + (st.file || '%s');
          ovSub.textContent = st.detail || (st.pages > 0 ? ('Rendered ' + st.pages + ' page' + (st.pages === 1 ? '' : 's') + ' — transferring to browser...') : 'Compiling Typst document...');
        }
        pill.classList.remove('typx-show');
      } else {
        if (hasErr) {
          pill.classList.add('typx-show', 'typx-err');
          pillDot.style.display = 'none';
          pillText.textContent = 'Compile error (' + (st.errors ? st.errors.length : 1) + ')';
        } else if (st.phase === 'compiling') {
          pill.classList.add('typx-show');
          pill.classList.remove('typx-err');
          pillDot.style.display = 'inline-block';
          pillText.textContent = 'Compiling...';
        } else {
          pill.classList.remove('typx-show', 'typx-err');
        }
      }
      var nextDelay = (hasRendered && !hasErr && st.phase !== 'compiling') ? 1500 : 350;
      setTimeout(pollStatus, nextDelay);
    }).catch(function() {
      setTimeout(pollStatus, 1500);
    });
  }
  pollStatus();
})();
</script>
`, safeFile, safeFile, safeFile)
}
