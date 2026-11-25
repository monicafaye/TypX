package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagerTemplateCommandsUnsupportedWhenOffline(t *testing.T) {
	t.Parallel()

	m := newManager()
	_, err := m.InitTemplate(context.Background(), "@preview/charged-ieee", t.TempDir())
	assert.True(t, errors.Is(err, ErrTemplateCommandUnsupported))

	_, err = m.GetTemplateEntry(context.Background(), "@preview/charged-ieee")
	assert.True(t, errors.Is(err, ErrTemplateCommandUnsupported))
}

func TestManagerTemplateCommandsRoundTrip(t *testing.T) {
	t.Parallel()

	srvToCliR, srvToCliW := io.Pipe()
	cliToSrvR, cliToSrvW := io.Pipe()
	client := NewPipeClient(srvToCliR, cliToSrvW, Handlers{})
	defer func() {
		_ = srvToCliW.Close()
		_ = cliToSrvR.Close()
	}()

	var gotInitArgs []string
	var gotEntryArgs []string
	go func() {
		br := bufio.NewReader(cliToSrvR)
		for {
			payload, err := readFrame(br)
			if err != nil {
				return
			}
			var req rpcIncoming
			if err := json.Unmarshal(payload, &req); err != nil {
				continue
			}
			switch req.Method {
			case "workspace/executeCommand":
				var p struct {
					Command   string   `json:"command"`
					Arguments []string `json:"arguments"`
				}
				_ = json.Unmarshal(req.Params, &p)
				switch p.Command {
				case "tinymist.doInitTemplate":
					gotInitArgs = p.Arguments
					frame, _ := encodeFrame(rpcResponse{
						JSONRPC: "2.0",
						ID:      req.ID,
						Result:  map[string]string{"entryPath": "template/main.typ"},
					})
					_, _ = srvToCliW.Write(frame)
				case "tinymist.doGetTemplateEntry":
					gotEntryArgs = p.Arguments
					frame, _ := encodeFrame(rpcResponse{
						JSONRPC: "2.0",
						ID:      req.ID,
						Result:  "#import \"@preview/charged-ieee:0.1.4\": *\n",
					})
					_, _ = srvToCliW.Write(frame)
				}
			case "shutdown":
				frame, _ := encodeFrame(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: nil})
				_, _ = srvToCliW.Write(frame)
			case "exit":
				return
			}
		}
	}()

	m := newManager()
	m.atomicState.Store(int32(StateReady))
	m.mu.Lock()
	m.client = client
	m.serverCommands["tinymist.doInitTemplate"] = true
	m.serverCommands["tinymist.doGetTemplateEntry"] = true
	m.mu.Unlock()

	target := filepath.Join(t.TempDir(), "paper")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	entryPath, err := m.InitTemplate(ctx, "@preview/charged-ieee", target)
	require.NoError(t, err)
	assert.Equal(t, "template/main.typ", entryPath)
	assert.Equal(t, []string{"@preview/charged-ieee", target}, gotInitArgs)

	entryContent, err := m.GetTemplateEntry(ctx, "@preview/charged-ieee")
	require.NoError(t, err)
	assert.Contains(t, entryContent, `#import "@preview/charged-ieee:0.1.4": *`)
	assert.Equal(t, []string{"@preview/charged-ieee"}, gotEntryArgs)

	require.NoError(t, client.Close(ctx))
}
