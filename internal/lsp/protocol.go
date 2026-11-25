package lsp

import (
	"bytes"
	"encoding/json"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

// DiagnosticSeverity values defined by LSP 3.17.
const (
	SeverityError       = 1
	SeverityWarning     = 2
	SeverityInformation = 3
	SeverityHint        = 4
)

// InsertTextFormat values defined by LSP 3.17.
const (
	InsertTextFormatPlainText = 1
	InsertTextFormatSnippet   = 2
)

// TextDocumentSyncKind values defined by LSP 3.17.
const (
	TextDocumentSyncKindNone        = 0
	TextDocumentSyncKindFull        = 1
	TextDocumentSyncKindIncremental = 2
)

// MessageType values for window/showMessage and window/logMessage.
const (
	MessageTypeError   = 1
	MessageTypeWarning = 2
	MessageTypeInfo    = 3
	MessageTypeLog     = 4
)

// SymbolKind values defined by LSP 3.17.
const (
	SymbolKindFile      = 1
	SymbolKindModule    = 2
	SymbolKindNamespace = 3
	SymbolKindPackage   = 4
	SymbolKindClass     = 5
	SymbolKindMethod    = 6
	SymbolKindProperty  = 7
	SymbolKindField     = 8
	SymbolKindFunction  = 12
	SymbolKindVariable  = 13
	SymbolKindConstant  = 14
	SymbolKindString    = 15
	SymbolKindObject    = 19
	SymbolKindKey       = 20
	SymbolKindStruct    = 23
)

// Position represents a zero-based line and UTF-16 character offset in a document.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range represents a text range in a document.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location represents a location inside a resource.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// LocationLink represents a link between a source and a target location.
type LocationLink struct {
	OriginSelectionRange *Range `json:"originSelectionRange,omitempty"`
	TargetURI            string `json:"targetUri"`
	TargetRange          Range  `json:"targetRange"`
	TargetSelectionRange Range  `json:"targetSelectionRange"`
}

// TextDocumentIdentifier identifies a text document using a URI.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// VersionedTextDocumentIdentifier denotes a specific version of a text document.
type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

// TextDocumentItem transfers a text document from client to server.
type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

// TextDocumentPositionParams identifies a document and a position inside it.
type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// MarkupContent represents a formatted string value (plaintext or markdown).
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// WorkspaceFolder describes a workspace root.
type WorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// ClientInfo describes the LSP client.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// InitializeParams is sent as the first request from client to server.
type InitializeParams struct {
	ProcessID             *int               `json:"processId"`
	RootURI               *string            `json:"rootUri"`
	WorkspaceFolders      []WorkspaceFolder  `json:"workspaceFolders,omitempty"`
	ClientInfo            *ClientInfo        `json:"clientInfo,omitempty"`
	Capabilities          ClientCapabilities `json:"capabilities"`
	InitializationOptions any                `json:"initializationOptions,omitempty"`
}

// ClientCapabilities defines the LSP 3.17 capabilities advertised by TypX.
type ClientCapabilities struct {
	Workspace    *WorkspaceClientCapabilities    `json:"workspace,omitempty"`
	TextDocument *TextDocumentClientCapabilities `json:"textDocument,omitempty"`
	General      *GeneralClientCapabilities      `json:"general,omitempty"`
}

// WorkspaceClientCapabilities describes workspace-level capabilities.
type WorkspaceClientCapabilities struct {
	ApplyEdit      bool                              `json:"applyEdit"`
	Configuration  bool                              `json:"configuration,omitempty"`
	ExecuteCommand *ExecuteCommandClientCapabilities `json:"executeCommand,omitempty"`
}

// ExecuteCommandClientCapabilities describes workspace/executeCommand support.
type ExecuteCommandClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

// TextDocumentClientCapabilities describes text document capabilities.
type TextDocumentClientCapabilities struct {
	Synchronization    *TextDocumentSyncClientCapabilities   `json:"synchronization,omitempty"`
	Completion         *CompletionClientCapabilities         `json:"completion,omitempty"`
	Hover              *HoverClientCapabilities              `json:"hover,omitempty"`
	PublishDiagnostics *PublishDiagnosticsClientCapabilities `json:"publishDiagnostics,omitempty"`
	DocumentSymbol     *DocumentSymbolClientCapabilities     `json:"documentSymbol,omitempty"`
	Definition         *DefinitionClientCapabilities         `json:"definition,omitempty"`
	Formatting         *FormattingClientCapabilities         `json:"formatting,omitempty"`
}

// DefinitionClientCapabilities describes textDocument/definition capabilities.
type DefinitionClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
	LinkSupport         bool `json:"linkSupport,omitempty"`
}

// FormattingClientCapabilities describes textDocument/formatting capabilities.
type FormattingClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

// TextDocumentSyncClientCapabilities describes document sync capabilities.
type TextDocumentSyncClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
	WillSave            bool `json:"willSave,omitempty"`
	DidSave             bool `json:"didSave,omitempty"`
}

// CompletionClientCapabilities describes completion capabilities.
type CompletionClientCapabilities struct {
	DynamicRegistration bool                              `json:"dynamicRegistration,omitempty"`
	CompletionItem      *CompletionItemClientCapabilities `json:"completionItem,omitempty"`
}

// CompletionItemClientCapabilities configures accepted completion item features.
type CompletionItemClientCapabilities struct {
	SnippetSupport       bool     `json:"snippetSupport"`
	DocumentationFormat  []string `json:"documentationFormat,omitempty"`
	InsertReplaceSupport bool     `json:"insertReplaceSupport,omitempty"`
	DeprecatedSupport    bool     `json:"deprecatedSupport,omitempty"`
}

// HoverClientCapabilities describes hover capabilities.
type HoverClientCapabilities struct {
	DynamicRegistration bool     `json:"dynamicRegistration,omitempty"`
	ContentFormat       []string `json:"contentFormat,omitempty"`
}

// PublishDiagnosticsClientCapabilities describes diagnostics capabilities.
type PublishDiagnosticsClientCapabilities struct {
	RelatedInformation bool `json:"relatedInformation,omitempty"`
	VersionSupport     bool `json:"versionSupport,omitempty"`
}

// DocumentSymbolClientCapabilities describes documentSymbol capabilities.
type DocumentSymbolClientCapabilities struct {
	HierarchicalDocumentSymbolSupport bool `json:"hierarchicalDocumentSymbolSupport,omitempty"`
}

// GeneralClientCapabilities describes general LSP client capabilities.
type GeneralClientCapabilities struct {
	PositionEncodings []string `json:"positionEncodings,omitempty"`
}

// InitializeResult is returned by the server for initialize.
type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   *ServerInfo        `json:"serverInfo,omitempty"`
}

// ServerInfo describes the connected LSP server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// ExecuteCommandOptions lists supported executeCommand identifiers.
type ExecuteCommandOptions struct {
	Commands []string `json:"commands,omitempty"`
}

// ServerCapabilities describes server-provided capabilities.
type ServerCapabilities struct {
	PositionEncoding       string                 `json:"positionEncoding,omitempty"`
	TextDocumentSync       any                    `json:"textDocumentSync,omitempty"`
	CompletionProvider     any                    `json:"completionProvider,omitempty"`
	HoverProvider          any                    `json:"hoverProvider,omitempty"`
	DocumentSymbolProvider     any                    `json:"documentSymbolProvider,omitempty"`
	DefinitionProvider         any                    `json:"definitionProvider,omitempty"`
	DocumentFormattingProvider any                    `json:"documentFormattingProvider,omitempty"`
	ExecuteCommandProvider     *ExecuteCommandOptions `json:"executeCommandProvider,omitempty"`
}

// InitializedParams is sent with the initialized notification.
type InitializedParams struct{}

// DidOpenTextDocumentParams is sent with textDocument/didOpen.
type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

// TextDocumentContentChangeEvent carries full-document or ranged text updates.
type TextDocumentContentChangeEvent struct {
	Text string `json:"text"`
}

// DidChangeTextDocumentParams is sent with textDocument/didChange.
type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent `json:"contentChanges"`
}

// DidSaveTextDocumentParams is sent with textDocument/didSave.
type DidSaveTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Text         *string                `json:"text,omitempty"`
}

// DidCloseTextDocumentParams is sent with textDocument/didClose.
type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// PublishDiagnosticsParams is sent from server via textDocument/publishDiagnostics.
type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Version     *int         `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// Diagnostic represents a compiler/linter diagnostic.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Code     any    `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// CompletionContext provides extra context for textDocument/completion.
type CompletionContext struct {
	TriggerKind      int    `json:"triggerKind"`
	TriggerCharacter string `json:"triggerCharacter,omitempty"`
}

// CompletionParams is sent with textDocument/completion.
type CompletionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      *CompletionContext     `json:"context,omitempty"`
}

// CompletionList is an LSP CompletionList response.
type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

// TextEdit represents a textual edit at a document range.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// CompletionItem represents a single completion candidate.
type CompletionItem struct {
	Label            string          `json:"label"`
	Kind             int             `json:"kind,omitempty"`
	Detail           string          `json:"detail,omitempty"`
	Documentation    json.RawMessage `json:"documentation,omitempty"`
	SortText         string          `json:"sortText,omitempty"`
	FilterText       string          `json:"filterText,omitempty"`
	InsertText       string          `json:"insertText,omitempty"`
	InsertTextFormat int             `json:"insertTextFormat,omitempty"`
	TextEdit         *TextEdit       `json:"textEdit,omitempty"`
}

// HoverParams is sent with textDocument/hover.
type HoverParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// Hover is the response from textDocument/hover.
type Hover struct {
	Contents json.RawMessage `json:"contents"`
	Range    *Range          `json:"range,omitempty"`
}

// MarkedString is a legacy LSP hover object.
type MarkedString struct {
	Language string `json:"language"`
	Value    string `json:"value"`
}

// DocumentSymbolParams is sent with textDocument/documentSymbol.
type DocumentSymbolParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// FormattingOptions configures LSP document formatting.
type FormattingOptions struct {
	TabSize                int  `json:"tabSize"`
	InsertSpaces           bool `json:"insertSpaces"`
	TrimTrailingWhitespace bool `json:"trimTrailingWhitespace,omitempty"`
	InsertFinalNewline     bool `json:"insertFinalNewline,omitempty"`
}

// DocumentFormattingParams is sent with textDocument/formatting.
type DocumentFormattingParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Options      FormattingOptions      `json:"options"`
}

// DocumentSymbol represents a hierarchical symbol in a document.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// SymbolInformation represents a flat LSP symbol entry with a Location.
type SymbolInformation struct {
	Name          string   `json:"name"`
	Kind          int      `json:"kind"`
	Location      Location `json:"location"`
	ContainerName string   `json:"containerName,omitempty"`
}

// ExecuteCommandParams is sent with workspace/executeCommand.
type ExecuteCommandParams struct {
	Command   string `json:"command"`
	Arguments []any  `json:"arguments,omitempty"`
}

// ShowMessageParams is received from window/showMessage.
type ShowMessageParams struct {
	Type    int    `json:"type"`
	Message string `json:"message"`
}

// LogMessageParams is received from window/logMessage.
type LogMessageParams struct {
	Type    int    `json:"type"`
	Message string `json:"message"`
}

// CancelParams is sent with $/cancelRequest.
type CancelParams struct {
	ID any `json:"id"`
}

// ConfigurationItem is one item of workspace/configuration params.
type ConfigurationItem struct {
	ScopeURI string `json:"scopeUri,omitempty"`
	Section  string `json:"section,omitempty"`
}

// ConfigurationParams is received from workspace/configuration.
type ConfigurationParams struct {
	Items []ConfigurationItem `json:"items"`
}

// DefaultClientCapabilities returns TypX's LSP 3.17 client capabilities.
func DefaultClientCapabilities() ClientCapabilities {
	return ClientCapabilities{
		Workspace: &WorkspaceClientCapabilities{
			ApplyEdit:     false,
			Configuration: true,
			ExecuteCommand: &ExecuteCommandClientCapabilities{
				DynamicRegistration: false,
			},
		},
		TextDocument: &TextDocumentClientCapabilities{
			Synchronization: &TextDocumentSyncClientCapabilities{
				DynamicRegistration: false,
				WillSave:            false,
				DidSave:             true,
			},
			Completion: &CompletionClientCapabilities{
				DynamicRegistration: false,
				CompletionItem: &CompletionItemClientCapabilities{
					SnippetSupport:       false,
					DocumentationFormat:  []string{"markdown", "plaintext"},
					InsertReplaceSupport: false,
					DeprecatedSupport:    true,
				},
			},
			Hover: &HoverClientCapabilities{
				DynamicRegistration: false,
				ContentFormat:       []string{"markdown", "plaintext"},
			},
			PublishDiagnostics: &PublishDiagnosticsClientCapabilities{
				RelatedInformation: true,
				VersionSupport:     true,
			},
			DocumentSymbol: &DocumentSymbolClientCapabilities{
				HierarchicalDocumentSymbolSupport: true,
			},
			Definition: &DefinitionClientCapabilities{
				DynamicRegistration: false,
				LinkSupport:         true,
			},
			Formatting: &FormattingClientCapabilities{
				DynamicRegistration: false,
			},
		},
		General: &GeneralClientCapabilities{
			PositionEncodings: []string{"utf-16"},
		},
	}
}

// ParseCompletionResult parses a textDocument/completion result that may be
// null, []CompletionItem, or CompletionList.
func ParseCompletionResult(raw json.RawMessage) ([]CompletionItem, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var items []CompletionItem
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, err
		}
		return items, nil
	}
	var list CompletionList
	if err := json.Unmarshal(trimmed, &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// ExtractHoverText extracts readable text from an LSP Hover.Contents JSON payload,
// supporting MarkupContent, plain string, MarkedString, and []MarkedString.
func ExtractHoverText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return strings.TrimSpace(s)
		}
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err == nil {
			var parts []string
			for _, elem := range arr {
				if t := ExtractHoverText(elem); t != "" {
					parts = append(parts, t)
				}
			}
			return strings.Join(parts, "\n\n")
		}
	case '{':
		var mc MarkupContent
		if err := json.Unmarshal(trimmed, &mc); err == nil && (mc.Kind != "" || mc.Value != "") {
			return strings.TrimSpace(mc.Value)
		}
		var ms MarkedString
		if err := json.Unmarshal(trimmed, &ms); err == nil && ms.Value != "" {
			return strings.TrimSpace(ms.Value)
		}
	}
	return ""
}

// ParseDocumentSymbols parses a textDocument/documentSymbol response that may
// be null, hierarchical []DocumentSymbol, or flat []SymbolInformation.
func ParseDocumentSymbols(raw json.RawMessage) ([]DocumentSymbol, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] != '[' {
		return nil, json.Unmarshal(trimmed, &[]DocumentSymbol{})
	}

	var probe []struct {
		Location *Location `json:"location,omitempty"`
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return nil, err
	}
	if len(probe) == 0 {
		return nil, nil
	}
	if probe[0].Location != nil {
		var flat []SymbolInformation
		if err := json.Unmarshal(trimmed, &flat); err != nil {
			return nil, err
		}
		out := make([]DocumentSymbol, 0, len(flat))
		for _, si := range flat {
			out = append(out, DocumentSymbol{
				Name:           si.Name,
				Kind:           si.Kind,
				Range:          si.Location.Range,
				SelectionRange: si.Location.Range,
			})
		}
		return out, nil
	}

	var syms []DocumentSymbol
	if err := json.Unmarshal(trimmed, &syms); err != nil {
		return nil, err
	}
	return syms, nil
}

// ParseLocations parses a textDocument/definition or textDocument/references
// response supporting null, Location, []Location, and []LocationLink.
func ParseLocations(raw json.RawMessage) ([]Location, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '{' {
		var link LocationLink
		if err := json.Unmarshal(trimmed, &link); err == nil && link.TargetURI != "" {
			r := link.TargetSelectionRange
			if r == (Range{}) {
				r = link.TargetRange
			}
			return []Location{{URI: link.TargetURI, Range: r}}, nil
		}
		var loc Location
		if err := json.Unmarshal(trimmed, &loc); err != nil {
			return nil, err
		}
		if loc.URI == "" {
			return nil, nil
		}
		return []Location{loc}, nil
	}
	if trimmed[0] != '[' {
		return nil, json.Unmarshal(trimmed, &[]Location{})
	}

	var elems []json.RawMessage
	if err := json.Unmarshal(trimmed, &elems); err != nil {
		return nil, err
	}
	if len(elems) == 0 {
		return nil, nil
	}
	out := make([]Location, 0, len(elems))
	for _, elem := range elems {
		parsed, err := ParseLocations(elem)
		if err != nil {
			return nil, err
		}
		out = append(out, parsed...)
	}
	return out, nil
}

// UTF16ToRuneCol converts a zero-based UTF-16 code-unit column offset into a
// zero-based Go rune column offset on the given UTF-8 line slice.
// Negative offsets clamp to 0; past-EOL offsets clamp to rune count;
// offsets inside a surrogate pair clamp to the start of that rune.
func UTF16ToRuneCol(line []byte, utf16Col int) int {
	if utf16Col <= 0 {
		return 0
	}
	utf16Pos := 0
	runeCol := 0
	off := 0
	for off < len(line) {
		if utf16Pos == utf16Col {
			return runeCol
		}
		r, size := utf8.DecodeRune(line[off:])
		units := 1
		if r > 0xFFFF {
			units = 2
		}
		if utf16Pos+units > utf16Col {
			return runeCol
		}
		utf16Pos += units
		runeCol++
		off += size
	}
	return runeCol
}

// RuneColToUTF16 converts a zero-based Go rune column offset into a zero-based
// UTF-16 code-unit column offset on the given UTF-8 line slice.
// Negative offsets clamp to 0; past-EOL offsets clamp to total UTF-16 units.
func RuneColToUTF16(line []byte, runeCol int) int {
	if runeCol <= 0 {
		return 0
	}
	utf16Pos := 0
	curRune := 0
	off := 0
	for off < len(line) && curRune < runeCol {
		r, size := utf8.DecodeRune(line[off:])
		if r > 0xFFFF {
			utf16Pos += 2
		} else {
			utf16Pos++
		}
		curRune++
		off += size
	}
	return utf16Pos
}

// PathToURI converts a filesystem path (Windows drive, UNC, or POSIX) into a
// canonical file:// URI with Windows drive letters uppercased and special
// characters percent-encoded.
func PathToURI(path string) string {
	if path == "" {
		return ""
	}
	if len(path) >= 7 && strings.EqualFold(path[:7], "file://") {
		return path
	}

	isUNC := strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//")
	isWinDrive := len(path) >= 2 && isASCIIAlpha(path[0]) && path[1] == ':'
	isPOSIXAbs := strings.HasPrefix(path, "/")

	if !isUNC && !isWinDrive && !isPOSIXAbs {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		isUNC = strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//")
		isWinDrive = len(path) >= 2 && isASCIIAlpha(path[0]) && path[1] == ':'
	}

	slashed := strings.ReplaceAll(path, `\`, "/")

	if isUNC || strings.HasPrefix(slashed, "//") {
		rest := strings.TrimLeft(slashed, "/")
		host := rest
		sharePath := "/"
		if idx := strings.IndexByte(rest, '/'); idx >= 0 {
			host = rest[:idx]
			sharePath = rest[idx:]
		}
		u := url.URL{
			Scheme: "file",
			Host:   host,
			Path:   sharePath,
		}
		return u.String()
	}

	if isWinDrive || (len(slashed) >= 2 && isASCIIAlpha(slashed[0]) && slashed[1] == ':') {
		slashed = strings.ToUpper(slashed[:1]) + slashed[1:]
		if len(slashed) == 2 {
			slashed += "/"
		} else if slashed[2] != '/' {
			slashed = slashed[:2] + "/" + slashed[2:]
		}
		u := url.URL{
			Scheme: "file",
			Path:   "/" + slashed,
		}
		return u.String()
	}

	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	u := url.URL{
		Scheme: "file",
		Path:   slashed,
	}
	return u.String()
}

// URIToPath converts a file:// URI into a filesystem path suitable for the
// current OS, decoding percent escapes (%3A, %20, %23, etc.) and handling
// Windows drive letters, file://localhost/, and UNC hosts.
func URIToPath(uri string) string {
	return uriToPath(uri, runtime.GOOS == "windows")
}

func uriToPath(uri string, windowsOS bool) string {
	if uri == "" {
		return ""
	}
	u, err := url.Parse(uri)
	if err != nil || !strings.EqualFold(u.Scheme, "file") {
		return uri
	}

	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		if windowsOS {
			return `\\` + u.Host + strings.ReplaceAll(u.Path, "/", `\`)
		}
		return "//" + u.Host + u.Path
	}

	p := u.Path
	if len(p) >= 3 && p[0] == '/' && isASCIIAlpha(p[1]) && p[2] == ':' {
		p = strings.ToUpper(p[1:2]) + p[2:]
		if windowsOS {
			return strings.ReplaceAll(p, "/", `\`)
		}
		return p
	}
	if windowsOS {
		return strings.ReplaceAll(p, "/", `\`)
	}
	return p
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
