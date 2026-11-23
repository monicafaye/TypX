package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	switch os.Getenv("TYPX_LSP_HELPER") {
	case "":
		os.Exit(m.Run())
	case "version_ok":
		fmt.Println("tinymist 0.13.0 (test)")
		os.Exit(0)
	case "version_hang":
		time.Sleep(10 * time.Second)
		os.Exit(0)
	case "stderr_spam":
		for i := 0; i < 250; i++ {
			fmt.Fprintf(os.Stderr, "stderr line %d\n", i)
		}
		os.Exit(1)
	case "job_child":
		time.Sleep(60 * time.Second)
		os.Exit(0)
	case "job_parent":
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "TYPX_LSP_HELPER=job_child")
		job, err := startManagedProcess(cmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "startManagedProcess error: %v\n", err)
			os.Exit(2)
		}
		fmt.Printf("%d\n", cmd.Process.Pid)
		_ = os.Stdout.Sync()
		time.Sleep(60 * time.Second)
		_ = job.Close()
		os.Exit(0)
	case "mock_tinymist":
		os.Exit(runMockTinymist())
	default:
		fmt.Fprintf(os.Stderr, "unknown helper: %s\n", os.Getenv("TYPX_LSP_HELPER"))
		os.Exit(2)
	}
}

func runMockTinymist() int {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("tinymist 0.13.0 (mock)")
		return 0
	}
	if len(os.Args) > 1 && os.Args[1] == "preview" {
		fmt.Println("Static file server listening on: 127.0.0.1:23625")
		_ = os.Stdout.Sync()
		time.Sleep(30 * time.Second)
		return 0
	}

	logPath := os.Getenv("TYPX_MOCK_LOG")
	var logMu sync.Mutex
	appendLog := func(line string) {
		if logPath == "" {
			return
		}
		logMu.Lock()
		defer logMu.Unlock()
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			_, _ = fmt.Fprintln(f, line)
			_ = f.Close()
		}
	}

	var writeMu sync.Mutex
	writeFrame := func(v any) {
		frame, err := encodeFrame(v)
		if err != nil {
			return
		}
		writeMu.Lock()
		_, _ = os.Stdout.Write(frame)
		writeMu.Unlock()
	}

	crashMarkerFile := os.Getenv("TYPX_MOCK_CRASH_ONCE_FILE")
	maybeCrashOnce := func(reason string) {
		if crashMarkerFile == "" {
			return
		}
		if _, err := os.Stat(crashMarkerFile); os.IsNotExist(err) {
			_ = os.WriteFile(crashMarkerFile, []byte(reason), 0644)
			fmt.Fprintln(os.Stderr, "mock panic: simulated crash on "+reason)
			os.Exit(2)
		}
	}

	br := bufio.NewReaderSize(os.Stdin, 64*1024)
	for {
		payload, err := readFrame(br)
		if err != nil {
			if err == io.EOF {
				return 0
			}
			return 1
		}
		var msg rpcIncoming
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue
		}

		// Handle response to server-initiated request (e.g. workspace/configuration).
		if msg.Method == "" {
			appendLog("resp:" + string(msg.ID))
			continue
		}

		appendLog("recv:" + msg.Method)

		switch msg.Method {
		case "initialize":
			// Trigger a server-initiated workspace/configuration request mid-session.
			var srvID int64 = 9001
			writeFrame(rpcRequest{
				JSONRPC: "2.0",
				ID:      &srvID,
				Method:  "workspace/configuration",
				Params: ConfigurationParams{
					Items: []ConfigurationItem{{Section: "tinymist"}},
				},
			})
			writeFrame(rpcResponse{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Result: InitializeResult{
					Capabilities: ServerCapabilities{
						PositionEncoding:   "utf-16",
						TextDocumentSync:   1,
						CompletionProvider: map[string]any{},
						HoverProvider:      true,
					},
					ServerInfo: &ServerInfo{Name: "tinymist", Version: "0.13.0-mock"},
				},
			})

		case "initialized":
			if os.Getenv("TYPX_MOCK_CRASH_AFTER_INIT") == "1" {
				maybeCrashOnce("after_init")
			}

		case "textDocument/didOpen":
			var p DidOpenTextDocumentParams
			if err := json.Unmarshal(msg.Params, &p); err == nil {
				appendLog("didOpen:" + p.TextDocument.URI + ":" + p.TextDocument.Text)
				publishMockDiagnostics(writeFrame, p.TextDocument.URI, p.TextDocument.Version, p.TextDocument.Text)
			}

		case "textDocument/didChange":
			var p DidChangeTextDocumentParams
			if err := json.Unmarshal(msg.Params, &p); err == nil {
				text := ""
				if len(p.ContentChanges) > 0 {
					text = p.ContentChanges[0].Text
				}
				appendLog(fmt.Sprintf("didChange:%s:v%d:%s", p.TextDocument.URI, p.TextDocument.Version, text))
				if strings.Contains(text, "CRASH_NOW") {
					fmt.Fprintln(os.Stderr, "mock panic: CRASH_NOW requested")
					os.Exit(2)
				}
				if strings.Contains(text, "CRASH_ONCE") {
					maybeCrashOnce("didChange")
				}
				v := p.TextDocument.Version
				publishMockDiagnostics(writeFrame, p.TextDocument.URI, v, text)
			}

		case "textDocument/didSave":
			var p DidSaveTextDocumentParams
			if err := json.Unmarshal(msg.Params, &p); err == nil {
				appendLog("didSave:" + p.TextDocument.URI)
			}

		case "textDocument/didClose":
			var p DidCloseTextDocumentParams
			if err := json.Unmarshal(msg.Params, &p); err == nil {
				appendLog("didClose:" + p.TextDocument.URI)
			}

		case "textDocument/completion":
			var p CompletionParams
			_ = json.Unmarshal(msg.Params, &p)
			appendLog(fmt.Sprintf("completion:%d:%d", p.Position.Line, p.Position.Character))
			if delay := os.Getenv("TYPX_MOCK_COMPLETION_DELAY_MS"); delay != "" {
				var ms int
				_, _ = fmt.Sscanf(delay, "%d", &ms)
				if ms > 0 {
					time.Sleep(time.Duration(ms) * time.Millisecond)
				}
			}
			writeFrame(rpcResponse{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Result: CompletionList{
					IsIncomplete: false,
					Items: []CompletionItem{
						{Label: "alpha", InsertText: "alpha"},
						{Label: "algorithm", InsertText: "algorithm"},
					},
				},
			})

		case "textDocument/hover":
			var p HoverParams
			_ = json.Unmarshal(msg.Params, &p)
			appendLog(fmt.Sprintf("hover:%d:%d", p.Position.Line, p.Position.Character))
			val := "Mock short hover"
			if os.Getenv("TYPX_MOCK_HOVER_LONG") == "1" {
				val = "```typst\nlet alpha: symbol\n```\nLine 1 documentation\nLine 2 documentation"
			}
			writeFrame(rpcResponse{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Result: Hover{
					Contents: mustRawJSON(MarkupContent{Kind: "markdown", Value: val}),
				},
			})

		case "workspace/executeCommand":
			var p ExecuteCommandParams
			_ = json.Unmarshal(msg.Params, &p)
			appendLog("executeCommand:" + p.Command)
			if p.Command == "tinymist.startDefaultPreview" && os.Getenv("TYPX_MOCK_PREVIEW_FALLBACK") == "1" {
				writeFrame(rpcResponse{
					JSONRPC: "2.0",
					ID:      msg.ID,
					Error:   &RPCError{Code: -32601, Message: "method not found"},
				})
				continue
			}
			if p.Command == "tinymist.startDefaultPreview" {
				writeFrame(rpcRequest{
					JSONRPC: "2.0",
					Method:  "window/logMessage",
					Params: LogMessageParams{
						Type:    MessageTypeInfo,
						Message: "Static file server listening on: 127.0.0.1:23626",
					},
				})
				writeFrame(rpcResponse{
					JSONRPC: "2.0",
					ID:      msg.ID,
					Result:  map[string]any{"url": "http://127.0.0.1:23626"},
				})
				continue
			}
			writeFrame(rpcResponse{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Result:  nil,
			})

		case "shutdown":
			writeFrame(rpcResponse{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Result:  nil,
			})

		case "exit":
			return 0

		default:
			if len(msg.ID) > 0 {
				writeFrame(rpcResponse{
					JSONRPC: "2.0",
					ID:      msg.ID,
					Result:  nil,
				})
			}
		}
	}
}

func publishMockDiagnostics(writeFrame func(any), uri string, ver int, text string) {
	var diags []Diagnostic
	if strings.Contains(text, "ERR") {
		lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
		for y, line := range lines {
			if idx := strings.Index(line, "ERR"); idx >= 0 {
				utf16Start := RuneColToUTF16([]byte(line[:idx]), len([]rune(line[:idx])))
				diags = append(diags, Diagnostic{
					Range: Range{
						Start: Position{Line: y, Character: utf16Start},
						End:   Position{Line: y, Character: utf16Start + 3},
					},
					Severity: SeverityError,
					Source:   "tinymist",
					Message:  "mock error here",
				})
			}
		}
	}
	v := ver
	writeFrame(rpcRequest{
		JSONRPC: "2.0",
		Method:  "textDocument/publishDiagnostics",
		Params: PublishDiagnosticsParams{
			URI:         uri,
			Version:     &v,
			Diagnostics: diags,
		},
	})
}

func mustRawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
