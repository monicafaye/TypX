package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	if len(os.Args) > 1 && os.Args[1] == "compile" {
		return runMockCompile(os.Args[2:])
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

	docs := make(map[string]string)
	nextSrvID := int64(9002)
	pendingApplyCmds := make(map[string]json.RawMessage)
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

		// Handle response to server-initiated request (e.g. workspace/configuration, workspace/applyEdit).
		if msg.Method == "" {
			idKey := strings.TrimSpace(string(msg.ID))
			appendLog("resp:" + idKey)
			if cmdID, ok := pendingApplyCmds[idKey]; ok {
				delete(pendingApplyCmds, idKey)
				writeFrame(rpcResponse{
					JSONRPC: "2.0",
					ID:      cmdID,
					Result:  nil,
				})
			}
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
						PositionEncoding:           "utf-16",
						TextDocumentSync:           1,
						CompletionProvider:         map[string]any{},
						HoverProvider:              true,
						DefinitionProvider:         true,
						ReferencesProvider:         true,
						RenameProvider:             map[string]any{"prepareProvider": true},
						CodeActionProvider:         true,
						WorkspaceSymbolProvider:    true,
						DocumentFormattingProvider: true,
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
				docs[p.TextDocument.URI] = p.TextDocument.Text
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
				docs[p.TextDocument.URI] = text
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

		case "textDocument/formatting":
			var p DocumentFormattingParams
			_ = json.Unmarshal(msg.Params, &p)
			appendLog("formatting:" + p.TextDocument.URI)
			if delay := os.Getenv("TYPX_MOCK_FORMAT_DELAY_MS"); delay != "" {
				var ms int
				_, _ = fmt.Sscanf(delay, "%d", &ms)
				if ms > 0 {
					time.Sleep(time.Duration(ms) * time.Millisecond)
				}
			}
			if os.Getenv("TYPX_MOCK_FORMAT_FAIL") == "1" {
				writeFrame(rpcResponse{
					JSONRPC: "2.0",
					ID:      msg.ID,
					Error:   &RPCError{Code: -32603, Message: "mock format error"},
				})
				continue
			}
			var edits []TextEdit
			text := docs[p.TextDocument.URI]
			lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
			for y, line := range lines {
				if strings.HasPrefix(line, "=   ") {
					edits = append(edits, TextEdit{
						Range:   Range{Start: Position{Line: y, Character: 1}, End: Position{Line: y, Character: 4}},
						NewText: " ",
					})
				}
				if strings.HasPrefix(line, "  let  x=1") {
					edits = append(edits, TextEdit{
						Range:   Range{Start: Position{Line: y, Character: 0}, End: Position{Line: y, Character: len(line)}},
						NewText: "  let x = 1",
					})
				}
			}
			writeFrame(rpcResponse{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Result:  edits,
			})

		case "textDocument/definition":
			var p TextDocumentPositionParams
			_ = json.Unmarshal(msg.Params, &p)
			appendLog(fmt.Sprintf("definition:%s:%d:%d", p.TextDocument.URI, p.Position.Line, p.Position.Character))
			srcPath := URIToPath(p.TextDocument.URI)
			srcDir := filepath.Dir(srcPath)
			text := docs[p.TextDocument.URI]
			lineText := mockLineAt(text, p.Position.Line)
			switch {
			case strings.Contains(text, "NO_DEF"):
				writeFrame(rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: nil})
			case strings.Contains(text, "MULTI_DEF"):
				targetA := PathToURI(filepath.Join(srcDir, "helper.typ"))
				targetB := PathToURI(filepath.Join(srcDir, "refs.bib"))
				locs := []Location{
					{URI: targetA, Range: Range{Start: Position{Line: 1, Character: 0}, End: Position{Line: 1, Character: 6}}},
					{URI: targetB, Range: Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 8}}},
				}
				writeFrame(rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: locs})
			case strings.Contains(lineText, "citekey"):
				bibPath := filepath.Join(srcDir, "refs.bib")
				links := []LocationLink{{
					TargetURI:            PathToURI(bibPath),
					TargetRange:          Range{Start: Position{Line: 1, Character: 0}, End: Position{Line: 1, Character: 12}},
					TargetSelectionRange: Range{Start: Position{Line: 1, Character: 2}, End: Position{Line: 1, Character: 10}},
				}}
				writeFrame(rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: links})
			case os.Getenv("TYPX_MOCK_DEF_PKG_PATH") != "":
				pkgPath := os.Getenv("TYPX_MOCK_DEF_PKG_PATH")
				links := []LocationLink{{
					TargetURI:            PathToURI(pkgPath),
					TargetRange:          Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 5}},
					TargetSelectionRange: Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 5}},
				}}
				writeFrame(rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: links})
			default:
				helperPath := filepath.Join(srcDir, "helper.typ")
				links := []LocationLink{{
					TargetURI:            PathToURI(helperPath),
					TargetRange:          Range{Start: Position{Line: 2, Character: 5}, End: Position{Line: 2, Character: 11}},
					TargetSelectionRange: Range{Start: Position{Line: 2, Character: 5}, End: Position{Line: 2, Character: 11}},
				}}
				writeFrame(rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: links})
			}

		case "textDocument/references":
			var p ReferenceParams
			_ = json.Unmarshal(msg.Params, &p)
			appendLog(fmt.Sprintf("references:%s:%d:%d", p.TextDocument.URI, p.Position.Line, p.Position.Character))
			tok, _, _, ok := mockTokenSpanAtUTF16(mockLineAt(docs[p.TextDocument.URI], p.Position.Line), p.Position.Character)
			if !ok {
				writeFrame(rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: nil})
				continue
			}
			locs := mockFindTokenLocations(p.TextDocument.URI, docs, tok)
			writeFrame(rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: locs})

		case "textDocument/prepareRename":
			var p TextDocumentPositionParams
			_ = json.Unmarshal(msg.Params, &p)
			appendLog(fmt.Sprintf("prepareRename:%s:%d:%d", p.TextDocument.URI, p.Position.Line, p.Position.Character))
			if strings.Contains(docs[p.TextDocument.URI], "PREPARE_UNSUPPORTED") {
				writeFrame(rpcResponse{
					JSONRPC: "2.0",
					ID:      msg.ID,
					Error:   &RPCError{Code: -32601, Message: "Method not found"},
				})
				continue
			}
			line := mockLineAt(docs[p.TextDocument.URI], p.Position.Line)
			if strings.Contains(line, "NO_RENAME") {
				writeFrame(rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: nil})
				continue
			}
			tok, startU16, endU16, ok := mockTokenSpanAtUTF16(line, p.Position.Character)
			if !ok {
				writeFrame(rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: nil})
				continue
			}
			writeFrame(rpcResponse{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Result: map[string]any{
					"range": Range{
						Start: Position{Line: p.Position.Line, Character: startU16},
						End:   Position{Line: p.Position.Line, Character: endU16},
					},
					"placeholder": tok,
				},
			})

		case "textDocument/rename":
			var p RenameParams
			_ = json.Unmarshal(msg.Params, &p)
			appendLog(fmt.Sprintf("rename:%s:%s", p.TextDocument.URI, p.NewName))
			tok, _, _, ok := mockTokenSpanAtUTF16(mockLineAt(docs[p.TextDocument.URI], p.Position.Line), p.Position.Character)
			if !ok {
				writeFrame(rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: WorkspaceEdit{}})
				continue
			}
			changes := make(map[string][]TextEdit)
			for _, loc := range mockFindTokenLocations(p.TextDocument.URI, docs, tok) {
				changes[loc.URI] = append(changes[loc.URI], TextEdit{
					Range:   loc.Range,
					NewText: p.NewName,
				})
			}
			writeFrame(rpcResponse{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Result:  WorkspaceEdit{Changes: changes},
			})

		case "textDocument/codeAction":
			var p CodeActionParams
			_ = json.Unmarshal(msg.Params, &p)
			appendLog("codeAction:" + p.TextDocument.URI)
			if strings.Contains(docs[p.TextDocument.URI], "NO_ACTIONS") {
				writeFrame(rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: []CodeAction{}})
				continue
			}
			fixRange := Range{Start: Position{0, 0}, End: Position{0, 0}}
			fixNew := "// fixed\n"
			fixTitle := "Insert fix header"
			if locs := mockFindTokenLocationsInText(p.TextDocument.URI, docs[p.TextDocument.URI], "FIXME"); len(locs) > 0 {
				fixRange = locs[0].Range
				fixNew = "DONE"
				fixTitle = "Replace FIXME with DONE"
			}
			actions := []CodeAction{
				{
					Title:       fixTitle,
					Kind:        "quickfix",
					IsPreferred: true,
					Edit: &WorkspaceEdit{
						Changes: map[string][]TextEdit{
							p.TextDocument.URI: {{Range: fixRange, NewText: fixNew}},
						},
					},
				},
				{
					Title: "Apply edit via server command",
					Kind:  "refactor",
					Command: &LSPCommand{
						Title:     "Apply edit",
						Command:   "mock.applyCommandEdit",
						Arguments: []any{p.TextDocument.URI},
					},
				},
			}
			writeFrame(rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: actions})

		case "workspace/symbol":
			var p WorkspaceSymbolParams
			_ = json.Unmarshal(msg.Params, &p)
			appendLog("workspaceSymbol:" + p.Query)
			syms := mockCollectWorkspaceSymbols(docs, p.Query)
			writeFrame(rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: syms})

		case "workspace/executeCommand":
			var p ExecuteCommandParams
			_ = json.Unmarshal(msg.Params, &p)
			if p.Command == "tinymist.pinMain" {
				rawArgs, _ := json.Marshal(p.Arguments)
				appendLog("executeCommand:" + p.Command + ":" + string(rawArgs))
			} else {
				appendLog("executeCommand:" + p.Command)
			}
			if p.Command == "mock.applyCommandEdit" {
				targetURI := ""
				if len(p.Arguments) > 0 {
					if s, ok := p.Arguments[0].(string); ok {
						targetURI = s
					}
				}
				editRange := Range{Start: Position{0, 0}, End: Position{0, 0}}
				editNew := "// cmd-applied\n"
				if targetURI != "" {
					if locs := mockFindTokenLocationsInText(targetURI, docs[targetURI], "CMD_TARGET"); len(locs) > 0 {
						editRange = locs[0].Range
						editNew = "CMD_APPLIED"
					}
				}
				srvID := nextSrvID
				nextSrvID++
				pendingApplyCmds[fmt.Sprintf("%d", srvID)] = msg.ID
				writeFrame(rpcRequest{
					JSONRPC: "2.0",
					ID:      &srvID,
					Method:  "workspace/applyEdit",
					Params: ApplyWorkspaceEditParams{
						Label: "Apply mock edit",
						Edit: WorkspaceEdit{
							Changes: map[string][]TextEdit{
								targetURI: {{Range: editRange, NewText: editNew}},
							},
						},
					},
				})
				continue
			}
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

func runMockCompile(args []string) int {
	var format, inputPath, outputPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--root", "--ppi":
			i++
		case "--format":
			if i+1 < len(args) {
				format = args[i+1]
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
	if logPath := os.Getenv("TYPX_MOCK_LOG"); logPath != "" {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			_, _ = fmt.Fprintf(f, "compile:%s:%s:%s\n", format, inputPath, outputPath)
			_ = f.Close()
		}
	}
	if os.Getenv("TYPX_MOCK_COMPILE_FAIL") == "1" {
		fmt.Fprintln(os.Stderr, "error: mock compile failure")
		return 1
	}
	if outputPath == "" {
		return 2
	}
	outFile := strings.ReplaceAll(outputPath, "{0p}", "01")
	outFile = strings.ReplaceAll(outFile, "{p}", "1")
	content, err := os.ReadFile(inputPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(outFile, append([]byte("MOCK_"+strings.ToUpper(format)+":"), content...), 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if format == "html" {
		fmt.Fprintln(os.Stderr, "warning: html export is experimental")
	}
	return 0
}

func mockLineAt(text string, line int) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if line < 0 || line >= len(lines) {
		return ""
	}
	return lines[line]
}

func isMockIdentRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
}

func mockTokenSpanAtUTF16(line string, utf16Col int) (token string, startU16, endU16 int, ok bool) {
	runes := []rune(line)
	if len(runes) == 0 {
		return "", 0, 0, false
	}
	idx := UTF16ToRuneCol([]byte(line), utf16Col)
	if idx >= len(runes) {
		idx = len(runes) - 1
	}
	if !isMockIdentRune(runes[idx]) {
		if idx > 0 && isMockIdentRune(runes[idx-1]) {
			idx--
		} else {
			return "", 0, 0, false
		}
	}
	start := idx
	for start > 0 && isMockIdentRune(runes[start-1]) {
		start--
	}
	end := idx + 1
	for end < len(runes) && isMockIdentRune(runes[end]) {
		end++
	}
	return string(runes[start:end]), RuneColToUTF16([]byte(line), start), RuneColToUTF16([]byte(line), end), true
}

func mockFindTokenLocations(originURI string, docs map[string]string, token string) []Location {
	sources := make(map[string]string, len(docs)+2)
	for uri, content := range docs {
		sources[uri] = content
	}
	srcDir := filepath.Dir(URIToPath(originURI))
	for _, sib := range []string{"sub.typ", "helper.typ"} {
		p := filepath.Join(srcDir, sib)
		u := PathToURI(p)
		if _, exists := sources[u]; !exists {
			if raw, err := os.ReadFile(p); err == nil {
				sources[u] = string(raw)
			}
		}
	}
	var out []Location
	for uri, content := range sources {
		out = append(out, mockFindTokenLocationsInText(uri, content, token)...)
	}
	return out
}

func mockFindTokenLocationsInText(uri, content, token string) []Location {
	if token == "" {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var out []Location
	for y, line := range lines {
		off := 0
		for off < len(line) {
			idx := strings.Index(line[off:], token)
			if idx < 0 {
				break
			}
			absStart := off + idx
			absEnd := absStart + len(token)
			leftOK := absStart == 0 || !isMockIdentRune(rune(line[absStart-1]))
			rightOK := absEnd == len(line) || !isMockIdentRune(rune(line[absEnd]))
			if leftOK && rightOK {
				startRune := len([]rune(line[:absStart]))
				endRune := len([]rune(line[:absEnd]))
				out = append(out, Location{
					URI: uri,
					Range: Range{
						Start: Position{Line: y, Character: RuneColToUTF16([]byte(line), startRune)},
						End:   Position{Line: y, Character: RuneColToUTF16([]byte(line), endRune)},
					},
				})
			}
			off = absEnd
		}
	}
	return out
}

func mockCollectWorkspaceSymbols(docs map[string]string, query string) []WorkspaceSymbol {
	q := strings.ToLower(strings.TrimSpace(query))
	sources := make(map[string]string, len(docs)+2)
	var firstDir string
	for uri, content := range docs {
		sources[uri] = content
		if firstDir == "" {
			firstDir = filepath.Dir(URIToPath(uri))
		}
	}
	if firstDir != "" {
		for _, sib := range []string{"sub.typ", "helper.typ"} {
			p := filepath.Join(firstDir, sib)
			u := PathToURI(p)
			if _, exists := sources[u]; !exists {
				if raw, err := os.ReadFile(p); err == nil {
					sources[u] = string(raw)
				}
			}
		}
	}
	var out []WorkspaceSymbol
	for uri, content := range sources {
		lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
		base := filepath.Base(URIToPath(uri))
		for y, line := range lines {
			trim := strings.TrimSpace(line)
			var symName string
			kind := SymbolKindVariable
			switch {
			case strings.HasPrefix(trim, "= "):
				symName = strings.TrimSpace(strings.TrimLeft(trim, "= "))
				kind = SymbolKindString
			case strings.HasPrefix(trim, "#let "):
				rest := strings.TrimSpace(strings.TrimPrefix(trim, "#let "))
				symName = rest
				if i := strings.IndexAny(rest, "=("); i >= 0 {
					symName = strings.TrimSpace(rest[:i])
				}
				kind = SymbolKindFunction
			}
			if symName == "" {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(symName), q) {
				continue
			}
			out = append(out, WorkspaceSymbol{
				Name:          symName,
				Kind:          kind,
				ContainerName: base,
				Location: Location{
					URI: uri,
					Range: Range{
						Start: Position{Line: y, Character: 0},
						End:   Position{Line: y, Character: len([]rune(line))},
					},
				},
			})
		}
	}
	return out
}
