package lsp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreviewProxyWaitingAndOverlayInjection(t *testing.T) {
	status := previewStatusPayload{
		Phase:  "starting",
		File:   "01-sets.typ",
		Detail: "Starting tinymist preview server...",
	}

	proxy, err := startPreviewProxy("/book/01-sets.typ", func() previewStatusPayload {
		return status
	})
	require.NoError(t, err)
	defer proxy.Close()

	// 1. Before upstream is ready, GET / returns the waiting progress HTML and
	// /__typx_status reports upstreamReady=false.
	resp, err := http.Get(proxy.URL() + "/")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "Preparing 01-sets.typ")
	assert.Contains(t, string(body), "/__typx_status")

	sResp, err := http.Get(proxy.URL() + "/__typx_status")
	require.NoError(t, err)
	var gotStatus previewStatusPayload
	require.NoError(t, json.NewDecoder(sResp.Body).Decode(&gotStatus))
	sResp.Body.Close()
	assert.False(t, gotStatus.UpstreamReady)
	assert.Equal(t, "starting", gotStatus.Phase)
	assert.Equal(t, "01-sets.typ", gotStatus.File)

	// 2. Attach a mock upstream tinymist server -> GET / injects #typx-overlay
	// while preserving the upstream <title> and DOM, and /__typx_status reports upstreamReady=true.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset.js" {
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = io.WriteString(w, "console.log('ok');")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><head><title>01-sets.typ</title></head><body><div id="typst-app"></div></body></html>`)
	}))
	defer upstream.Close()

	require.NoError(t, proxy.SetUpstream(upstream.URL))
	status.Phase = "compiling"

	resp2, err := http.Get(proxy.URL() + "/")
	require.NoError(t, err)
	body2, err := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
	assert.Contains(t, string(body2), "<title>01-sets.typ</title>")
	assert.Contains(t, string(body2), `id="typst-app"`)
	assert.Contains(t, string(body2), `id="typx-overlay"`)
	assert.Contains(t, string(body2), `id="typx-pill"`)

	// Non-root asset passes straight through without HTML injection.
	aResp, err := http.Get(proxy.URL() + "/asset.js")
	require.NoError(t, err)
	aBody, err := io.ReadAll(aResp.Body)
	aResp.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, "console.log('ok');", string(aBody))

	// Status endpoint now reports upstreamReady=true + live phase.
	sResp2, err := http.Get(proxy.URL() + "/__typx_status")
	require.NoError(t, err)
	var gotStatus2 previewStatusPayload
	require.NoError(t, json.NewDecoder(sResp2.Body).Decode(&gotStatus2))
	sResp2.Body.Close()
	assert.True(t, gotStatus2.UpstreamReady)
	assert.Equal(t, "compiling", gotStatus2.Phase)
}
