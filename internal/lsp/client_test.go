package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPipeClientFramingFragmentedLargeAndCaseInsensitive(t *testing.T) {
	srvToCliR, srvToCliW := io.Pipe()
	cliToSrvR, cliToSrvW := io.Pipe()

	var notifOrder []string
	var notifMu sync.Mutex
	notifDone := make(chan struct{})

	client := NewPipeClient(srvToCliR, cliToSrvW, Handlers{
		OnNotification: func(method string, params json.RawMessage) {
			notifMu.Lock()
			notifOrder = append(notifOrder, method+":"+string(params))
			if len(notifOrder) == 2 {
				close(notifDone)
			}
			notifMu.Unlock()
		},
	})
	defer func() {
		_ = srvToCliW.Close()
		_ = cliToSrvR.Close()
	}()

	largeText := strings.Repeat("x", 1<<20+1024) // > 1 MiB
	go func() {
		br := bufio.NewReader(cliToSrvR)
		for {
			payload, err := readFrame(br)
			if err != nil {
				return
			}
			var req rpcIncoming
			require.NoError(t, json.Unmarshal(payload, &req))
			switch req.Method {
			case "test/echo":
				resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"echo":%q}}`, string(req.ID), largeText)
				// Case-insensitive header + extra Content-Type header + byte-by-byte fragmented writes
				header := fmt.Sprintf("content-length: %d\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n", len(resp))
				_, _ = srvToCliW.Write([]byte(header[:10]))
				_, _ = srvToCliW.Write([]byte(header[10:]))
				for off := 0; off < len(resp); off += 4096 {
					end := off + 4096
					if end > len(resp) {
						end = len(resp)
					}
					_, _ = srvToCliW.Write([]byte(resp[off:end]))
				}

				// Send two ordered notifications
				f1, _ := encodeFrame(rpcRequest{JSONRPC: "2.0", Method: "n/one", Params: 1})
				f2, _ := encodeFrame(rpcRequest{JSONRPC: "2.0", Method: "n/two", Params: 2})
				_, _ = srvToCliW.Write(f1)
				_, _ = srvToCliW.Write(f2)
			case "shutdown":
				f, _ := encodeFrame(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: nil})
				_, _ = srvToCliW.Write(f)
			case "exit":
				return
			}
		}
	}()

	var out struct {
		Echo string `json:"echo"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Call(ctx, "test/echo", map[string]any{"n": 1}, &out))
	assert.Equal(t, largeText, out.Echo)

	select {
	case <-notifDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notifications")
	}
	notifMu.Lock()
	assert.Equal(t, []string{"n/one:1", "n/two:2"}, notifOrder)
	notifMu.Unlock()

	require.NoError(t, client.Close(context.Background()))
}

func TestPipeClientConcurrentCallsCancellationAndServerRequests(t *testing.T) {
	srvToCliR, srvToCliW := io.Pipe()
	cliToSrvR, cliToSrvW := io.Pipe()

	var cancelReceived sync.WaitGroup
	cancelReceived.Add(1)
	var gotConfigResp sync.WaitGroup
	gotConfigResp.Add(1)

	client := NewPipeClient(srvToCliR, cliToSrvW, Handlers{})
	defer func() {
		_ = srvToCliW.Close()
		_ = cliToSrvR.Close()
	}()

	var writeMu sync.Mutex
	sendToClient := func(v any) {
		f, _ := encodeFrame(v)
		writeMu.Lock()
		_, _ = srvToCliW.Write(f)
		writeMu.Unlock()
	}

	go func() {
		// Server-initiated workspace/configuration and unknown request
		cfgID := int64(500)
		sendToClient(rpcRequest{
			JSONRPC: "2.0",
			ID:      &cfgID,
			Method:  "workspace/configuration",
			Params:  ConfigurationParams{Items: []ConfigurationItem{{Section: "a"}, {Section: "b"}}},
		})
		unkID := int64(501)
		sendToClient(rpcRequest{
			JSONRPC: "2.0",
			ID:      &unkID,
			Method:  "custom/unknownRequest",
		})

		br := bufio.NewReader(cliToSrvR)
		for {
			payload, err := readFrame(br)
			if err != nil {
				return
			}
			var msg rpcIncoming
			if err := json.Unmarshal(payload, &msg); err != nil {
				continue
			}
			if msg.Method == "" && string(msg.ID) == "500" {
				var arr []any
				_ = json.Unmarshal(msg.Result, &arr)
				if len(arr) == 2 {
					gotConfigResp.Done()
				}
				continue
			}
			if msg.Method == "$/cancelRequest" {
				cancelReceived.Done()
				continue
			}
			if msg.Method == "concurrent/echo" {
				idStr := string(msg.ID)
				var params map[string]int
				_ = json.Unmarshal(msg.Params, &params)
				go func(id json.RawMessage, n int) {
					sendToClient(rpcResponse{JSONRPC: "2.0", ID: id, Result: n})
				}(json.RawMessage(idStr), params["i"])
			}
			if msg.Method == "shutdown" {
				sendToClient(rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: nil})
			}
		}
	}()

	gotConfigResp.Wait()

	// 32 concurrent calls
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var res int
			err := client.Call(ctx, "concurrent/echo", map[string]int{"i": idx}, &res)
			assert.NoError(t, err)
			assert.Equal(t, idx, res)
		}(i)
	}
	wg.Wait()

	// Cancelled call cleans up pending map and emits $/cancelRequest
	cctx, ccancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer ccancel()
	err := client.Call(cctx, "never/replies", nil, nil)
	require.Error(t, err)
	assert.Equal(t, 0, client.PendingCount())
	cancelReceived.Wait()

	require.NoError(t, client.Close(context.Background()))
}

func TestStartProcessClientStderrRingBuffer(t *testing.T) {
	t.Setenv("TYPX_LSP_HELPER", "stderr_spam")
	c, err := StartProcessClient(context.Background(), os.Args[0], nil, "", Handlers{})
	require.NoError(t, err)

	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stderr_spam helper exit")
	}

	require.Error(t, c.ExitErr())
	tail := c.StderrTail()
	require.Len(t, tail, 200)
	assert.Equal(t, "stderr line 50", tail[0])
	assert.Equal(t, "stderr line 249", tail[199])
}
