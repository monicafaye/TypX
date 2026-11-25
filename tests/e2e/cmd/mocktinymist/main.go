package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

type traceRecord struct {
	TS      string          `json:"ts"`
	Event   string          `json:"event,omitempty"`
	Method  string          `json:"method,omitempty"`
	PID     int             `json:"pid"`
	ID      json.RawMessage `json:"id,omitempty"`
	URI     string          `json:"uri,omitempty"`
	Version int             `json:"version,omitempty"`
	Text    string          `json:"text,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Note    string          `json:"note,omitempty"`
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

var (
	traceMu sync.Mutex
	writeMu sync.Mutex
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "mocktinymist: missing subcommand")
		os.Exit(2)
	}

	switch args[0] {
	case "--version", "-version", "-V", "version":
		if os.Getenv("TYPX_MOCK_VERSION_FAIL") == "1" {
			fmt.Fprintln(os.Stderr, "tinymist: simulated version probe failure")
			os.Exit(1)
		}
		fmt.Println("tinymist 0.13.0-mock")
		os.Exit(0)
	case "lsp":
		runLSP()
	case "preview":
		runPreview(args[1:])
	case "compile":
		runCompile(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "mocktinymist: unknown subcommand %q\n", args[0])
		os.Exit(2)
	}
}

func recordPID() {
	pidPath := os.Getenv("TYPX_MOCK_PID_FILE")
	if pidPath == "" {
		return
	}
	f, err := os.OpenFile(pidPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Sync()
}

func appendTrace(rec traceRecord) {
	tracePath := os.Getenv("TYPX_MOCK_TRACE_FILE")
	if tracePath == "" {
		return
	}
	rec.TS = time.Now().UTC().Format(time.RFC3339Nano)
	rec.PID = os.Getpid()
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	traceMu.Lock()
	defer traceMu.Unlock()
	f, err := os.OpenFile(tracePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
	_ = f.Sync()
}

func shouldCrash(phase string) bool {
	crashOn := os.Getenv("TYPX_MOCK_CRASH_ON")
	if crashOn == "" {
		return false
	}
	stateFile := os.Getenv("TYPX_MOCK_STATE_FILE")
	if stateFile != "" {
		if _, err := os.Stat(stateFile); err == nil {
			// Already crashed once; do not crash on subsequent restart.
			return false
		}
	}
	match := false
	switch crashOn {
	case "didOpen":
		match = phase == "didOpen"
	case "didChange":
		match = phase == "didChange"
	case "once":
		match = phase == "didOpen"
	}
	if !match {
		return false
	}
	if stateFile != "" {
		_ = os.WriteFile(stateFile, []byte("crashed\n"), 0644)
	}
	return true
}

func runPreview(args []string) {
	recordPID()
	appendTrace(traceRecord{
		Event: "preview_start",
		Note:  strings.Join(args, " "),
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mocktinymist preview listen error: %v\n", err)
		os.Exit(1)
	}
	defer ln.Close()

	fmt.Printf("http://%s\n", ln.Addr().String())
	_ = os.Stdout.Sync()

	// Accept connections harmlessly in background until process exits.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	buf := make([]byte, 256)
	for {
		_, err := os.Stdin.Read(buf)
		if err != nil {
			if os.Getenv("TYPX_MOCK_IGNORE_STDIN_EOF") == "1" {
				appendTrace(traceRecord{Event: "preview_ignore_stdin_eof"})
				time.Sleep(60 * time.Second)
			}
			return
		}
	}
}

func runLSP() {
	recordPID()
	appendTrace(traceRecord{Event: "start", Note: "lsp"})

	reader := bufio.NewReader(os.Stdin)
	docs := make(map[string]string)
	pinnedMain := ""
	shutdownSeen := false
	var previewListeners []net.Listener
	defer func() {
		for _, ln := range previewListeners {
			_ = ln.Close()
		}
	}()

	for {
		payload, err := readRPCMessage(reader)
		if err != nil {
			if err == io.EOF && !shutdownSeen && os.Getenv("TYPX_MOCK_IGNORE_STDIN_EOF") == "1" {
				appendTrace(traceRecord{Event: "ignore_stdin_eof"})
				time.Sleep(60 * time.Second)
			}
			if shutdownSeen {
				os.Exit(0)
			}
			os.Exit(0)
		}

		var msg rpcMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			appendTrace(traceRecord{Event: "parse_error", Note: err.Error()})
			continue
		}

		switch msg.Method {
		case "initialize":
			appendTrace(traceRecord{
				Method: msg.Method,
				ID:     msg.ID,
				Params: msg.Params,
			})
			result := map[string]any{
				"capabilities": map[string]any{
					"textDocumentSync": 1,
					"completionProvider": map[string]any{
						"triggerCharacters": []string{"#", ".", "@"},
					},
					"hoverProvider":              true,
					"documentSymbolProvider":     true,
					"definitionProvider":         true,
					"documentFormattingProvider": true,
					"executeCommandProvider": map[string]any{
						"commands": []string{
							"tinymist.startDefaultPreview",
							"tinymist.doInitTemplate",
							"tinymist.doGetTemplateEntry",
							"tinymist.pinMain",
						},
					},
				},
				"serverInfo": map[string]any{
					"name":    "tinymist",
					"version": "0.13.0-mock",
				},
			}
			writeRPCResponse(msg.ID, result, nil)

		case "initialized":
			appendTrace(traceRecord{
				Method: msg.Method,
				Params: msg.Params,
			})

		case "textDocument/didOpen":
			var p struct {
				TextDocument struct {
					URI     string `json:"uri"`
					Version int    `json:"version"`
					Text    string `json:"text"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			docs[p.TextDocument.URI] = p.TextDocument.Text
			appendTrace(traceRecord{
				Method:  msg.Method,
				URI:     p.TextDocument.URI,
				Version: p.TextDocument.Version,
				Text:    p.TextDocument.Text,
				Params:  msg.Params,
			})
			if shouldCrash("didOpen") {
				appendTrace(traceRecord{Event: "crash", Note: "didOpen"})
				fmt.Fprintln(os.Stderr, "mocktinymist panic: simulated crash on textDocument/didOpen")
				os.Exit(42)
			}
			if strings.Contains(p.TextDocument.Text, "ERR_TRIGGER") || os.Getenv("TYPX_MOCK_DIAG_ON_OPEN") == "1" {
				publishDiagsForText(p.TextDocument.URI, p.TextDocument.Version, p.TextDocument.Text, true)
			}

		case "textDocument/didChange":
			var p struct {
				TextDocument struct {
					URI     string `json:"uri"`
					Version int    `json:"version"`
				} `json:"textDocument"`
				ContentChanges []struct {
					Range *struct {
						Start struct {
							Line      int `json:"line"`
							Character int `json:"character"`
						} `json:"start"`
						End struct {
							Line      int `json:"line"`
							Character int `json:"character"`
						} `json:"end"`
					} `json:"range"`
					Text string `json:"text"`
				} `json:"contentChanges"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			text := docs[p.TextDocument.URI]
			for _, ch := range p.ContentChanges {
				if ch.Range == nil {
					text = ch.Text
				} else {
					text = applyIncrementalEdit(text, ch.Range.Start.Line, ch.Range.Start.Character, ch.Range.End.Line, ch.Range.End.Character, ch.Text)
				}
			}
			docs[p.TextDocument.URI] = text
			appendTrace(traceRecord{
				Method:  msg.Method,
				URI:     p.TextDocument.URI,
				Version: p.TextDocument.Version,
				Text:    text,
				Params:  msg.Params,
			})
			if shouldCrash("didChange") {
				appendTrace(traceRecord{Event: "crash", Note: "didChange"})
				fmt.Fprintln(os.Stderr, "mocktinymist panic: simulated crash on textDocument/didChange")
				os.Exit(42)
			}
			publishDiagsForText(p.TextDocument.URI, p.TextDocument.Version, text, false)

		case "textDocument/didSave":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
				Text string `json:"text"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.Text != "" {
				docs[p.TextDocument.URI] = p.Text
			}
			appendTrace(traceRecord{
				Method: msg.Method,
				URI:    p.TextDocument.URI,
				Text:   p.Text,
				Params: msg.Params,
			})

		case "textDocument/didClose":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			delete(docs, p.TextDocument.URI)
			appendTrace(traceRecord{
				Method: msg.Method,
				URI:    p.TextDocument.URI,
				Params: msg.Params,
			})

		case "textDocument/completion":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			appendTrace(traceRecord{
				Method: msg.Method,
				ID:     msg.ID,
				URI:    p.TextDocument.URI,
				Params: msg.Params,
			})
			result := map[string]any{
				"isIncomplete": false,
				"items": []map[string]any{
					{"label": "alpha", "kind": 14, "detail": "Greek letter alpha"},
					{"label": "figure", "kind": 3, "detail": "Typst figure"},
					{"label": "heading", "kind": 14, "detail": "Typst heading"},
					{"label": "beta", "kind": 14, "detail": "Greek letter beta"},
				},
			}
			writeRPCResponse(msg.ID, result, nil)

		case "textDocument/hover":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
				Position struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"position"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			appendTrace(traceRecord{
				Method: msg.Method,
				ID:     msg.ID,
				URI:    p.TextDocument.URI,
				Params: msg.Params,
			})
			token := tokenAtUTF16(docs[p.TextDocument.URI], p.Position.Line, p.Position.Character)
			hoverText := fmt.Sprintf("mock hover docs (line %d, char %d", p.Position.Line, p.Position.Character)
			if token != "" {
				hoverText += ", token: " + token
			}
			hoverText += ")"
			result := map[string]any{
				"contents": map[string]any{
					"kind":  "markdown",
					"value": hoverText,
				},
			}
			writeRPCResponse(msg.ID, result, nil)

		case "textDocument/definition":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
				Position struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"position"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			appendTrace(traceRecord{
				Method: msg.Method,
				ID:     msg.ID,
				URI:    p.TextDocument.URI,
				Params: msg.Params,
			})
			srcPath := uriToLocalPath(p.TextDocument.URI)
			srcDir := filepath.Dir(srcPath)
			lineText := lineAtOffset(docs[p.TextDocument.URI], p.Position.Line)
			switch {
			case strings.Contains(lineText, "knuth"):
				bibPath := filepath.Join(srcDir, "refs.bib")
				if _, statErr := os.Stat(bibPath); statErr == nil {
					writeRPCResponse(msg.ID, []map[string]any{{
						"targetUri": pathToFileURI(bibPath),
						"targetRange": map[string]any{
							"start": map[string]any{"line": 0, "character": 0},
							"end":   map[string]any{"line": 0, "character": 12},
						},
						"targetSelectionRange": map[string]any{
							"start": map[string]any{"line": 0, "character": 0},
							"end":   map[string]any{"line": 0, "character": 12},
						},
					}}, nil)
					break
				}
				writeRPCResponse(msg.ID, nil, nil)
			case strings.Contains(lineText, "helper"):
				helperPath := filepath.Join(srcDir, "helper.typ")
				if _, statErr := os.Stat(helperPath); statErr == nil {
					writeRPCResponse(msg.ID, []map[string]any{{
						"targetUri": pathToFileURI(helperPath),
						"targetRange": map[string]any{
							"start": map[string]any{"line": 0, "character": 0},
							"end":   map[string]any{"line": 0, "character": 10},
						},
						"targetSelectionRange": map[string]any{
							"start": map[string]any{"line": 0, "character": 0},
							"end":   map[string]any{"line": 0, "character": 10},
						},
					}}, nil)
					break
				}
				writeRPCResponse(msg.ID, nil, nil)
			default:
				writeRPCResponse(msg.ID, nil, nil)
			}

		case "textDocument/formatting":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			appendTrace(traceRecord{
				Method: msg.Method,
				ID:     msg.ID,
				URI:    p.TextDocument.URI,
				Params: msg.Params,
			})
			text := docs[p.TextDocument.URI]
			normalized := strings.ReplaceAll(text, "\r\n", "\n")
			lines := strings.Split(normalized, "\n")
			edits := make([]map[string]any, 0)
			for y, line := range lines {
				if strings.HasPrefix(line, "=") {
					trimmed := strings.TrimLeft(line[1:], " ")
					if len(line) > 1 && line[1] == ' ' && strings.HasPrefix(line, "=  ") {
						spacesEnd := len(line) - len(trimmed)
						edits = append(edits, map[string]any{
							"range": map[string]any{
								"start": map[string]any{"line": y, "character": 1},
								"end":   map[string]any{"line": y, "character": spacesEnd},
							},
							"newText": " ",
						})
					}
				}
				if strings.HasPrefix(line, "  let  x=1") {
					edits = append(edits, map[string]any{
						"range": map[string]any{
							"start": map[string]any{"line": y, "character": 0},
							"end":   map[string]any{"line": y, "character": len(line)},
						},
						"newText": "  let x = 1",
					})
				}
			}
			writeRPCResponse(msg.ID, edits, nil)

		case "workspace/executeCommand":
			var p struct {
				Command   string            `json:"command"`
				Arguments []json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			appendTrace(traceRecord{
				Method: msg.Method,
				ID:     msg.ID,
				Params: msg.Params,
			})
			switch {
			case p.Command == "tinymist.startDefaultPreview":
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				addr := "127.0.0.1:0"
				if err == nil {
					addr = ln.Addr().String()
					previewListeners = append(previewListeners, ln)
					go func(l net.Listener) {
						for {
							conn, err := l.Accept()
							if err != nil {
								return
							}
							_ = conn.Close()
						}
					}(ln)
				}
				writeRPCResponse(msg.ID, map[string]any{
					"staticServerAddr": addr,
					"url":              "http://" + addr,
				}, nil)
			case p.Command == "tinymist.doInitTemplate":
				var spec, targetDir string
				if len(p.Arguments) > 0 {
					_ = json.Unmarshal(p.Arguments[0], &spec)
				}
				if len(p.Arguments) > 1 {
					_ = json.Unmarshal(p.Arguments[1], &targetDir)
				}
				if targetDir == "" {
					writeRPCResponse(msg.ID, nil, map[string]any{
						"code":    -32602,
						"message": "missing targetDir",
					})
					break
				}
				if err := os.MkdirAll(targetDir, 0o755); err != nil {
					writeRPCResponse(msg.ID, nil, map[string]any{
						"code":    -32000,
						"message": err.Error(),
					})
					break
				}
				entryBody := fmt.Sprintf("// mock template for %s\n#import %q: *\n= Mock Template\n", spec, spec)
				if err := os.WriteFile(filepath.Join(targetDir, "main.typ"), []byte(entryBody), 0o644); err != nil {
					writeRPCResponse(msg.ID, nil, map[string]any{
						"code":    -32000,
						"message": err.Error(),
					})
					break
				}
				writeRPCResponse(msg.ID, map[string]any{
					"entryPath": "main.typ",
				}, nil)

			case p.Command == "tinymist.doGetTemplateEntry":
				var spec string
				if len(p.Arguments) > 0 {
					_ = json.Unmarshal(p.Arguments[0], &spec)
				}
				entryBody := fmt.Sprintf("// mock entry for %s\n#import %q: *\n", spec, spec)
				writeRPCResponse(msg.ID, entryBody, nil)

			case p.Command == "tinymist.pinMain":
				var nextPin *string
				if len(p.Arguments) > 0 {
					_ = json.Unmarshal(p.Arguments[0], &nextPin)
				}
				if nextPin != nil {
					pinnedMain = *nextPin
				} else {
					pinnedMain = ""
				}
				appendTrace(traceRecord{Event: "pinMain", Note: pinnedMain})
				writeRPCResponse(msg.ID, nil, nil)

			default:
				writeRPCResponse(msg.ID, nil, nil)
			}

		case "shutdown":
			shutdownSeen = true
			appendTrace(traceRecord{
				Method: msg.Method,
				ID:     msg.ID,
			})
			writeRPCResponse(msg.ID, nil, nil)

		case "exit":
			appendTrace(traceRecord{
				Method: msg.Method,
			})
			if shutdownSeen {
				os.Exit(0)
			}
			os.Exit(1)

		default:
			appendTrace(traceRecord{
				Method: msg.Method,
				ID:     msg.ID,
				Params: msg.Params,
			})
			if len(msg.ID) > 0 && string(msg.ID) != "null" {
				writeRPCResponse(msg.ID, nil, map[string]any{
					"code":    -32601,
					"message": "Method not found: " + msg.Method,
				})
			}
		}
	}
}

func readRPCMessage(r *bufio.Reader) ([]byte, error) {
	contentLen := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}
		colon := strings.IndexByte(trimmed, ':')
		if colon > 0 {
			name := strings.ToLower(strings.TrimSpace(trimmed[:colon]))
			val := strings.TrimSpace(trimmed[colon+1:])
			if name == "content-length" {
				n, convErr := strconv.Atoi(val)
				if convErr == nil {
					contentLen = n
				}
			}
		}
	}
	if contentLen < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}
	body := make([]byte, contentLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func writeRPCFrame(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	_, _ = fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n", len(data))
	_, _ = os.Stdout.Write(data)
}

func writeRPCResponse(id json.RawMessage, result any, rpcErr any) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
	}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	writeRPCFrame(resp)
}

func writeRPCNotification(method string, params any) {
	notif := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	writeRPCFrame(notif)
}

func publishDiagsForText(uri string, version int, text string, forceDefault bool) {
	ranges := findErrTriggerRanges(text)
	diags := make([]map[string]any, 0)
	lineIdx, startUTF16, endUTF16 := 0, 0, 0
	for i, r := range ranges {
		if i == 0 {
			lineIdx, startUTF16, endUTF16 = r[0], r[1], r[2]
		}
		diags = append(diags, map[string]any{
			"range": map[string]any{
				"start": map[string]any{"line": r[0], "character": r[1]},
				"end":   map[string]any{"line": r[0], "character": r[2]},
			},
			"severity": 1,
			"source":   "tinymist",
			"message":  "mock error: ERR_TRIGGER detected",
		})
	}
	if len(ranges) == 0 && forceDefault {
		diags = append(diags, map[string]any{
			"range": map[string]any{
				"start": map[string]any{"line": 0, "character": 0},
				"end":   map[string]any{"line": 0, "character": 1},
			},
			"severity": 2,
			"source":   "tinymist",
			"message":  "mock warning: diagnostic on open",
		})
	}

	params := map[string]any{
		"uri":         uri,
		"diagnostics": diags,
	}
	if version > 0 {
		params["version"] = version
	}
	appendTrace(traceRecord{
		Event:   "publishDiagnostics",
		URI:     uri,
		Version: version,
		Note:    fmt.Sprintf("count=%d line=%d start=%d end=%d", len(diags), lineIdx, startUTF16, endUTF16),
	})
	writeRPCNotification("textDocument/publishDiagnostics", params)

	if len(ranges) > 0 && os.Getenv("TYPX_MOCK_MULTI_DIAG") == "1" {
		srcPath := uriToLocalPath(uri)
		subPath := filepath.Join(filepath.Dir(srcPath), "sub.typ")
		if _, err := os.Stat(subPath); err == nil {
			subURI := pathToFileURI(subPath)
			writeRPCNotification("textDocument/publishDiagnostics", map[string]any{
				"uri": subURI,
				"diagnostics": []map[string]any{{
					"range": map[string]any{
						"start": map[string]any{"line": 0, "character": 0},
						"end":   map[string]any{"line": 0, "character": 3},
					},
					"severity": 2,
					"source":   "tinymist",
					"message":  "mock warning in sub.typ",
				}},
			})
		}
	}
}

func findErrTriggerRanges(text string) [][3]int {
	const needle = "ERR_TRIGGER"
	lines := strings.Split(text, "\n")
	var out [][3]int
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		idx := strings.Index(line, needle)
		if idx >= 0 {
			prefix := line[:idx]
			start := len(utf16.Encode([]rune(prefix)))
			end := start + len(utf16.Encode([]rune(needle)))
			out = append(out, [3]int{i, start, end})
		}
	}
	return out
}

func runCompile(args []string) {
	appendTrace(traceRecord{
		Event: "compile",
		Note:  strings.Join(args, " "),
	})
	var format, inputPath, outputPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--root", "--ppi":
			i++
		case "--format":
			if i+1 < len(args) {
				format = strings.ToLower(strings.TrimSpace(args[i+1]))
				i++
			}
		default:
			if strings.HasPrefix(args[i], "--") {
				continue
			}
			if inputPath == "" {
				inputPath = args[i]
			} else if outputPath == "" {
				outputPath = args[i]
			}
		}
	}
	if outputPath == "" {
		os.Exit(2)
	}
	var srcContent []byte
	if inputPath != "" {
		var err error
		srcContent, err = os.ReadFile(inputPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if format == "" {
		format = strings.TrimPrefix(strings.ToLower(filepath.Ext(outputPath)), ".")
	}
	payload := compileSyntheticPayload(format)
	pageNums := []string{"1"}
	if strings.Contains(string(srcContent), "#pagebreak()") {
		pageNums = []string{"1", "2"}
	}
	writeOne := func(dest string) {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(dest, payload, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if strings.Contains(outputPath, "{0p}") || strings.Contains(outputPath, "{p}") {
		for _, pNum := range pageNums {
			padded := pNum
			if len(padded) < 2 {
				padded = "0" + padded
			}
			dest := strings.ReplaceAll(outputPath, "{0p}", padded)
			dest = strings.ReplaceAll(dest, "{p}", pNum)
			writeOne(dest)
		}
		return
	}
	writeOne(outputPath)
}

func compileSyntheticPayload(format string) []byte {
	switch format {
	case "pdf":
		return []byte("%PDF-1.4\n% mock tinymist pdf\n")
	case "png":
		return []byte("\x89PNG\r\n\x1a\n")
	case "svg":
		return []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>")
	case "html":
		return []byte("<!doctype html><html><body>mock</body></html>")
	default:
		return []byte("mock")
	}
}

func lineAtOffset(text string, line int) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if line < 0 || line >= len(lines) {
		return ""
	}
	return lines[line]
}

func uriToLocalPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || !strings.EqualFold(u.Scheme, "file") {
		return uri
	}
	p := u.Path
	if runtime.GOOS == "windows" {
		if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
		return strings.ReplaceAll(p, "/", `\`)
	}
	return p
}

func pathToFileURI(p string) string {
	abs, err := filepath.Abs(p)
	if err == nil {
		p = abs
	}
	slashed := strings.ReplaceAll(p, `\`, "/")
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

func tokenAtUTF16(text string, lineIdx, utf16Col int) string {
	lines := strings.Split(text, "\n")
	if lineIdx < 0 || lineIdx >= len(lines) {
		return ""
	}
	line := strings.TrimSuffix(lines[lineIdx], "\r")
	runes := []rune(line)
	runeIdx := utf16OffsetToRune(runes, utf16Col)
	if runeIdx >= len(runes) && len(runes) > 0 {
		runeIdx = len(runes) - 1
	}
	if runeIdx < 0 || runeIdx >= len(runes) {
		return ""
	}
	isIdent := func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
	}
	if !isIdent(runes[runeIdx]) {
		return string(runes[runeIdx])
	}
	start := runeIdx
	for start > 0 && isIdent(runes[start-1]) {
		start--
	}
	end := runeIdx + 1
	for end < len(runes) && isIdent(runes[end]) {
		end++
	}
	return string(runes[start:end])
}

func utf16OffsetToRune(runes []rune, utf16Col int) int {
	if utf16Col <= 0 {
		return 0
	}
	units := 0
	for i, r := range runes {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if units+w > utf16Col {
			return i
		}
		units += w
	}
	return len(runes)
}

func applyIncrementalEdit(text string, startLine, startChar, endLine, endChar int, replacement string) string {
	lines := strings.Split(text, "\n")
	if startLine < 0 {
		startLine = 0
	}
	if endLine < 0 {
		endLine = 0
	}
	for len(lines) <= endLine {
		lines = append(lines, "")
	}
	startRunes := []rune(lines[startLine])
	endRunes := []rune(lines[endLine])
	sCol := utf16OffsetToRune(startRunes, startChar)
	eCol := utf16OffsetToRune(endRunes, endChar)

	var out bytes.Buffer
	for i := 0; i < startLine; i++ {
		out.WriteString(lines[i])
		out.WriteByte('\n')
	}
	out.WriteString(string(startRunes[:sCol]))
	out.WriteString(replacement)
	out.WriteString(string(endRunes[eCol:]))
	for i := endLine + 1; i < len(lines); i++ {
		out.WriteByte('\n')
		out.WriteString(lines[i])
	}
	return out.String()
}
