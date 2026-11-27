package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"sort"
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

// DocumentHighlightKind values defined by LSP 3.17.
const (
	DocumentHighlightKindText  = 1
	DocumentHighlightKindRead  = 2
	DocumentHighlightKindWrite = 3
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
	WorkspaceEdit  *WorkspaceEditClientCapabilities  `json:"workspaceEdit,omitempty"`
	Configuration  bool                              `json:"configuration,omitempty"`
	ExecuteCommand *ExecuteCommandClientCapabilities `json:"executeCommand,omitempty"`
	Symbol         *WorkspaceSymbolClientCapabilities `json:"symbol,omitempty"`
}

// WorkspaceEditClientCapabilities describes workspace/applyEdit document change support.
type WorkspaceEditClientCapabilities struct {
	DocumentChanges bool `json:"documentChanges,omitempty"`
}

// WorkspaceSymbolClientCapabilities describes workspace/symbol support.
type WorkspaceSymbolClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
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
	SignatureHelp      *SignatureHelpClientCapabilities      `json:"signatureHelp,omitempty"`
	PublishDiagnostics *PublishDiagnosticsClientCapabilities `json:"publishDiagnostics,omitempty"`
	DocumentSymbol     *DocumentSymbolClientCapabilities     `json:"documentSymbol,omitempty"`
	DocumentHighlight  *DocumentHighlightClientCapabilities  `json:"documentHighlight,omitempty"`
	SelectionRange     *SelectionRangeClientCapabilities     `json:"selectionRange,omitempty"`
	Definition         *DefinitionClientCapabilities         `json:"definition,omitempty"`
	References         *ReferenceClientCapabilities          `json:"references,omitempty"`
	Rename             *RenameClientCapabilities             `json:"rename,omitempty"`
	CodeAction         *CodeActionClientCapabilities         `json:"codeAction,omitempty"`
	Formatting         *FormattingClientCapabilities         `json:"formatting,omitempty"`
}

// DocumentHighlightClientCapabilities describes textDocument/documentHighlight capabilities.
type DocumentHighlightClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

// SelectionRangeClientCapabilities describes textDocument/selectionRange capabilities.
type SelectionRangeClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

// ReferenceClientCapabilities describes textDocument/references capabilities.
type ReferenceClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

// RenameClientCapabilities describes textDocument/rename capabilities.
type RenameClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
	PrepareSupport      bool `json:"prepareSupport,omitempty"`
}

// CodeActionLiteralKindSupport configures supported codeAction kinds.
type CodeActionLiteralKindSupport struct {
	ValueSet []string `json:"valueSet"`
}

// CodeActionLiteralSupport describes literal CodeAction support.
type CodeActionLiteralSupport struct {
	CodeActionKind CodeActionLiteralKindSupport `json:"codeActionKind"`
}

// CodeActionClientCapabilities describes textDocument/codeAction capabilities.
type CodeActionClientCapabilities struct {
	DynamicRegistration      bool                      `json:"dynamicRegistration,omitempty"`
	CodeActionLiteralSupport *CodeActionLiteralSupport `json:"codeActionLiteralSupport,omitempty"`
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

// ParameterInformationClientCapabilities describes signature parameter support.
type ParameterInformationClientCapabilities struct {
	LabelOffsetSupport bool `json:"labelOffsetSupport,omitempty"`
}

// SignatureInformationClientCapabilities describes signature info support.
type SignatureInformationClientCapabilities struct {
	DocumentationFormat  []string                                `json:"documentationFormat,omitempty"`
	ParameterInformation *ParameterInformationClientCapabilities `json:"parameterInformation,omitempty"`
	ActiveParameterSupport bool                                  `json:"activeParameterSupport,omitempty"`
}

// SignatureHelpClientCapabilities describes textDocument/signatureHelp capabilities.
type SignatureHelpClientCapabilities struct {
	DynamicRegistration  bool                                    `json:"dynamicRegistration,omitempty"`
	SignatureInformation *SignatureInformationClientCapabilities `json:"signatureInformation,omitempty"`
	ContextSupport       bool                                    `json:"contextSupport,omitempty"`
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
	PositionEncoding           string                 `json:"positionEncoding,omitempty"`
	TextDocumentSync           any                    `json:"textDocumentSync,omitempty"`
	CompletionProvider         any                    `json:"completionProvider,omitempty"`
	HoverProvider              any                    `json:"hoverProvider,omitempty"`
	SignatureHelpProvider      any                    `json:"signatureHelpProvider,omitempty"`
	DocumentSymbolProvider     any                    `json:"documentSymbolProvider,omitempty"`
	DocumentHighlightProvider  any                    `json:"documentHighlightProvider,omitempty"`
	SelectionRangeProvider     any                    `json:"selectionRangeProvider,omitempty"`
	DefinitionProvider         any                    `json:"definitionProvider,omitempty"`
	ReferencesProvider         any                    `json:"referencesProvider,omitempty"`
	RenameProvider             any                    `json:"renameProvider,omitempty"`
	CodeActionProvider         any                    `json:"codeActionProvider,omitempty"`
	WorkspaceSymbolProvider    any                    `json:"workspaceSymbolProvider,omitempty"`
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

// ReferenceContext configures textDocument/references behavior.
type ReferenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

// ReferenceParams is sent with textDocument/references.
type ReferenceParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      ReferenceContext       `json:"context"`
}

// RenameParams is sent with textDocument/rename.
type RenameParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	NewName      string                 `json:"newName"`
}

// OptionalVersionedTextDocumentIdentifier identifies a versioned or unversioned document.
type OptionalVersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version *int   `json:"version,omitempty"`
}

// TextDocumentEdit describes edits targeting one text document inside WorkspaceEdit.
type TextDocumentEdit struct {
	TextDocument OptionalVersionedTextDocumentIdentifier `json:"textDocument"`
	Edits        []TextEdit                              `json:"edits"`
}

// WorkspaceEdit represents edits across one or more workspace resources.
type WorkspaceEdit struct {
	Changes         map[string][]TextEdit `json:"changes,omitempty"`
	DocumentChanges []json.RawMessage     `json:"documentChanges,omitempty"`
}

// ApplyWorkspaceEditParams is received from server-initiated workspace/applyEdit.
type ApplyWorkspaceEditParams struct {
	Label string        `json:"label,omitempty"`
	Edit  WorkspaceEdit `json:"edit"`
}

// ApplyWorkspaceEditResult is returned for workspace/applyEdit.
type ApplyWorkspaceEditResult struct {
	Applied       bool   `json:"applied"`
	FailureReason string `json:"failureReason,omitempty"`
}

// CodeActionContext carries diagnostics and kind filters for textDocument/codeAction.
type CodeActionContext struct {
	Diagnostics []Diagnostic `json:"diagnostics"`
	Only        []string     `json:"only,omitempty"`
	TriggerKind int          `json:"triggerKind,omitempty"`
}

// CodeActionParams is sent with textDocument/codeAction.
type CodeActionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
	Context      CodeActionContext      `json:"context"`
}

// LSPCommand represents an LSP Command object.
type LSPCommand struct {
	Title     string `json:"title"`
	Command   string `json:"command"`
	Arguments []any  `json:"arguments,omitempty"`
}

// CodeActionDisabled explains why a code action is currently disabled.
type CodeActionDisabled struct {
	Reason string `json:"reason"`
}

// CodeAction represents a quickfix, refactor, or source action returned by LSP.
type CodeAction struct {
	Title       string              `json:"title"`
	Kind        string              `json:"kind,omitempty"`
	Diagnostics []Diagnostic        `json:"diagnostics,omitempty"`
	IsPreferred bool                `json:"isPreferred,omitempty"`
	Disabled    *CodeActionDisabled `json:"disabled,omitempty"`
	Edit        *WorkspaceEdit      `json:"edit,omitempty"`
	Command     *LSPCommand         `json:"command,omitempty"`
}

// WorkspaceSymbolParams is sent with workspace/symbol.
type WorkspaceSymbolParams struct {
	Query string `json:"query"`
}

// WorkspaceSymbol represents a symbol entry returned by workspace/symbol.
type WorkspaceSymbol struct {
	Name          string   `json:"name"`
	Kind          int      `json:"kind"`
	Tags          []int    `json:"tags,omitempty"`
	ContainerName string   `json:"containerName,omitempty"`
	Location      Location `json:"location"`
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

// DocumentHighlightParams is sent with textDocument/documentHighlight.
type DocumentHighlightParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// DocumentHighlight represents a document highlight range and optional kind.
type DocumentHighlight struct {
	Range Range `json:"range"`
	Kind  int   `json:"kind,omitempty"`
}

// SelectionRangeParams is sent with textDocument/selectionRange.
type SelectionRangeParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Positions    []Position             `json:"positions"`
}

// SelectionRange represents a nested selection range returned by textDocument/selectionRange.
type SelectionRange struct {
	Range  Range           `json:"range"`
	Parent *SelectionRange `json:"parent,omitempty"`
}

// DefaultClientCapabilities returns TypX's LSP 3.17 client capabilities.
func DefaultClientCapabilities() ClientCapabilities {
	return ClientCapabilities{
		Workspace: &WorkspaceClientCapabilities{
			ApplyEdit: true,
			WorkspaceEdit: &WorkspaceEditClientCapabilities{
				DocumentChanges: true,
			},
			Configuration: true,
			ExecuteCommand: &ExecuteCommandClientCapabilities{
				DynamicRegistration: false,
			},
			Symbol: &WorkspaceSymbolClientCapabilities{
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
					SnippetSupport:       true,
					DocumentationFormat:  []string{"markdown", "plaintext"},
					InsertReplaceSupport: false,
					DeprecatedSupport:    true,
				},
			},
			Hover: &HoverClientCapabilities{
				DynamicRegistration: false,
				ContentFormat:       []string{"markdown", "plaintext"},
			},
			SignatureHelp: &SignatureHelpClientCapabilities{
				DynamicRegistration: false,
				SignatureInformation: &SignatureInformationClientCapabilities{
					DocumentationFormat: []string{"plaintext", "markdown"},
					ParameterInformation: &ParameterInformationClientCapabilities{
						LabelOffsetSupport: true,
					},
					ActiveParameterSupport: true,
				},
				ContextSupport: true,
			},
			PublishDiagnostics: &PublishDiagnosticsClientCapabilities{
				RelatedInformation: true,
				VersionSupport:     true,
			},
			DocumentSymbol: &DocumentSymbolClientCapabilities{
				HierarchicalDocumentSymbolSupport: true,
			},
			DocumentHighlight: &DocumentHighlightClientCapabilities{
				DynamicRegistration: false,
			},
			SelectionRange: &SelectionRangeClientCapabilities{
				DynamicRegistration: false,
			},
			Definition: &DefinitionClientCapabilities{
				DynamicRegistration: false,
				LinkSupport:         true,
			},
			References: &ReferenceClientCapabilities{
				DynamicRegistration: false,
			},
			Rename: &RenameClientCapabilities{
				DynamicRegistration: false,
				PrepareSupport:      true,
			},
			CodeAction: &CodeActionClientCapabilities{
				DynamicRegistration: false,
				CodeActionLiteralSupport: &CodeActionLiteralSupport{
					CodeActionKind: CodeActionLiteralKindSupport{
						ValueSet: []string{
							"quickfix",
							"refactor",
							"refactor.extract",
							"refactor.inline",
							"refactor.rewrite",
							"source",
						},
					},
				},
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

// SignatureHelpTriggerKind values defined by LSP 3.17.
const (
	SignatureHelpTriggerInvoked       = 1
	SignatureHelpTriggerCharacter     = 2
	SignatureHelpTriggerContentChange = 3
)

// SignatureHelpContext carries trigger details for textDocument/signatureHelp.
type SignatureHelpContext struct {
	TriggerKind      int    `json:"triggerKind"`
	TriggerCharacter string `json:"triggerCharacter,omitempty"`
	IsRetrigger      bool   `json:"isRetrigger"`
}

// SignatureHelpParams is sent with textDocument/signatureHelp.
type SignatureHelpParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      *SignatureHelpContext  `json:"context,omitempty"`
}

// ParameterInformation describes one parameter of a callable signature.
type ParameterInformation struct {
	Label         json.RawMessage `json:"label"`
	Documentation json.RawMessage `json:"documentation,omitempty"`
}

// SignatureInformation describes one callable signature overload.
type SignatureInformation struct {
	Label           string                 `json:"label"`
	Documentation   json.RawMessage        `json:"documentation,omitempty"`
	Parameters      []ParameterInformation `json:"parameters,omitempty"`
	ActiveParameter *int                   `json:"activeParameter,omitempty"`
}

// SignatureHelp is returned by textDocument/signatureHelp.
type SignatureHelp struct {
	Signatures      []SignatureInformation `json:"signatures"`
	ActiveSignature *int                   `json:"activeSignature,omitempty"`
	ActiveParameter *int                   `json:"activeParameter,omitempty"`
}

// ParseSignatureHelp parses a textDocument/signatureHelp response (supporting null).
func ParseSignatureHelp(raw json.RawMessage) (*SignatureHelp, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var sh SignatureHelp
	if err := json.Unmarshal(trimmed, &sh); err != nil {
		return nil, err
	}
	if len(sh.Signatures) == 0 {
		return nil, nil
	}
	return &sh, nil
}

// FormatSignatureHelp renders the active signature and highlighted active
// parameter as a single-line infobar hint.
func FormatSignatureHelp(sh *SignatureHelp) string {
	if sh == nil || len(sh.Signatures) == 0 {
		return ""
	}
	sigIdx := 0
	if sh.ActiveSignature != nil && *sh.ActiveSignature >= 0 && *sh.ActiveSignature < len(sh.Signatures) {
		sigIdx = *sh.ActiveSignature
	}
	sig := sh.Signatures[sigIdx]
	label := collapseWhitespace(sig.Label)
	if label == "" {
		return ""
	}

	paramIdx := -1
	if sig.ActiveParameter != nil {
		paramIdx = *sig.ActiveParameter
	} else if sh.ActiveParameter != nil {
		paramIdx = *sh.ActiveParameter
	}

	if paramIdx >= 0 && paramIdx < len(sig.Parameters) {
		label = highlightSignatureParameter(sig.Label, sig.Parameters[paramIdx].Label)
		label = collapseWhitespace(label)
	}

	prefix := ""
	if len(sh.Signatures) > 1 {
		prefix = fmt.Sprintf("(%d/%d) ", sigIdx+1, len(sh.Signatures))
	}

	doc := ""
	if paramIdx >= 0 && paramIdx < len(sig.Parameters) {
		doc = collapseWhitespace(ExtractHoverText(sig.Parameters[paramIdx].Documentation))
	}
	if doc == "" {
		doc = collapseWhitespace(ExtractHoverText(sig.Documentation))
	}
	if doc != "" {
		const maxDocRunes = 80
		dr := []rune(doc)
		if len(dr) > maxDocRunes {
			doc = string(dr[:maxDocRunes-1]) + "…"
		}
		return prefix + label + " | " + doc
	}
	return prefix + label
}

func highlightSignatureParameter(sigLabel string, rawParamLabel json.RawMessage) string {
	trimmed := bytes.TrimSpace(rawParamLabel)
	if len(trimmed) == 0 {
		return sigLabel
	}
	// Offset tuple [startUTF16, endUTF16]
	if trimmed[0] == '[' {
		var span [2]int
		if err := json.Unmarshal(trimmed, &span); err == nil {
			lineBytes := []byte(sigLabel)
			startRune := UTF16ToRuneCol(lineBytes, span[0])
			endRune := UTF16ToRuneCol(lineBytes, span[1])
			runes := []rune(sigLabel)
			if startRune >= 0 && endRune > startRune && endRune <= len(runes) {
				return string(runes[:startRune]) + "»" + string(runes[startRune:endRune]) + "«" + string(runes[endRune:])
			}
		}
		return sigLabel
	}
	// Plain substring parameter label
	var paramStr string
	if err := json.Unmarshal(trimmed, &paramStr); err == nil && paramStr != "" {
		if idx := strings.Index(sigLabel, paramStr); idx >= 0 {
			return sigLabel[:idx] + "»" + paramStr + "«" + sigLabel[idx+len(paramStr):]
		}
	}
	return sigLabel
}

func collapseWhitespace(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.Join(strings.Fields(s), " ")
}

// ExtractTriggerChars extracts triggerCharacters (and optional
// retriggerCharacters) from a ServerCapabilities provider field (completion or
// signatureHelp). Returns nil when none are advertised.
func ExtractTriggerChars(provider any, includeRetrigger bool) []string {
	m, ok := provider.(map[string]any)
	if !ok || m == nil {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	appendFrom := func(key string) {
		raw, ok := m[key].([]any)
		if !ok {
			return
		}
		for _, v := range raw {
			s, ok := v.(string)
			if !ok || s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	appendFrom("triggerCharacters")
	if includeRetrigger {
		appendFrom("retriggerCharacters")
	}
	return out
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

// ParsePrepareRename parses textDocument/prepareRename supporting null, Range,
// {range, placeholder}, and {defaultBehavior: true}.
func ParsePrepareRename(raw json.RawMessage) (placeholder string, rng *Range, ok bool, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil, false, nil
	}
	var obj struct {
		DefaultBehavior bool      `json:"defaultBehavior"`
		Placeholder     string    `json:"placeholder"`
		Range           *Range    `json:"range"`
		Start           *Position `json:"start"`
		End             *Position `json:"end"`
	}
	if uerr := json.Unmarshal(trimmed, &obj); uerr != nil {
		return "", nil, false, uerr
	}
	if obj.Range != nil {
		return obj.Placeholder, obj.Range, true, nil
	}
	if obj.DefaultBehavior {
		return "", nil, true, nil
	}
	if obj.Start != nil && obj.End != nil {
		r := Range{Start: *obj.Start, End: *obj.End}
		return "", &r, true, nil
	}
	return "", nil, false, nil
}

// WorkspaceEditFileEdits merges WorkspaceEdit.Changes and DocumentChanges
// (TextDocumentEdit) into a per-URI edit map plus sorted deterministic URI list.
// Returns an error if an unsupported resource operation (create/rename/delete)
// appears in DocumentChanges.
func WorkspaceEditFileEdits(we WorkspaceEdit) (map[string][]TextEdit, []string, error) {
	out := make(map[string][]TextEdit)
	seenInDocChanges := make(map[string]bool)

	for i, raw := range we.DocumentChanges {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			continue
		}
		var kindProbe struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(trimmed, &kindProbe); err != nil {
			return nil, nil, fmt.Errorf("invalid documentChanges[%d]: %w", i, err)
		}
		if kindProbe.Kind != "" {
			return nil, nil, fmt.Errorf("unsupported resource operation %q in workspace edit", kindProbe.Kind)
		}
		var tde TextDocumentEdit
		if err := json.Unmarshal(trimmed, &tde); err != nil {
			return nil, nil, fmt.Errorf("invalid TextDocumentEdit[%d]: %w", i, err)
		}
		if tde.TextDocument.URI == "" {
			return nil, nil, errors.New("workspace edit documentChanges entry missing URI")
		}
		seenInDocChanges[tde.TextDocument.URI] = true
		out[tde.TextDocument.URI] = append(out[tde.TextDocument.URI], tde.Edits...)
	}

	for uri, edits := range we.Changes {
		if uri == "" {
			return nil, nil, errors.New("workspace edit changes entry missing URI")
		}
		if seenInDocChanges[uri] {
			continue
		}
		out[uri] = append(out[uri], edits...)
	}

	uris := make([]string, 0, len(out))
	for uri, edits := range out {
		if len(edits) == 0 {
			delete(out, uri)
			continue
		}
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	return out, uris, nil
}

// ParseCodeActions parses textDocument/codeAction responses supporting null,
// []CodeAction, legacy []Command, and mixed arrays, filtering out disabled items.
func ParseCodeActions(raw json.RawMessage) ([]CodeAction, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(trimmed, &elems); err != nil {
		return nil, err
	}
	if len(elems) == 0 {
		return nil, nil
	}

	out := make([]CodeAction, 0, len(elems))
	for _, elem := range elems {
		et := bytes.TrimSpace(elem)
		if len(et) == 0 || bytes.Equal(et, []byte("null")) {
			continue
		}
		var probe struct {
			Command json.RawMessage `json:"command,omitempty"`
		}
		if err := json.Unmarshal(et, &probe); err != nil {
			return nil, err
		}
		cmdTrim := bytes.TrimSpace(probe.Command)
		if len(cmdTrim) > 0 && cmdTrim[0] == '"' {
			var cmd LSPCommand
			if err := json.Unmarshal(et, &cmd); err != nil {
				return nil, err
			}
			out = append(out, CodeAction{
				Title:   cmd.Title,
				Command: &cmd,
			})
			continue
		}
		var ca CodeAction
		if err := json.Unmarshal(et, &ca); err != nil {
			return nil, err
		}
		if ca.Disabled != nil {
			continue
		}
		out = append(out, ca)
	}
	return out, nil
}

// ParseWorkspaceSymbols parses workspace/symbol responses supporting null and []WorkspaceSymbol.
func ParseWorkspaceSymbols(raw json.RawMessage) ([]WorkspaceSymbol, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var syms []WorkspaceSymbol
	if err := json.Unmarshal(trimmed, &syms); err != nil {
		return nil, err
	}
	if len(syms) == 0 {
		return nil, nil
	}
	out := make([]WorkspaceSymbol, 0, len(syms))
	for _, s := range syms {
		if s.Location.URI == "" {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// SupportsProvider reports whether an LSP ServerCapabilities provider field
// (boolean or options object) enables the capability.
func SupportsProvider(provider any) bool {
	switch v := provider.(type) {
	case bool:
		return v
	case *bool:
		return v != nil && *v
	case map[string]any:
		return v != nil
	default:
		return false
	}
}

// ParseDocumentHighlights parses a textDocument/documentHighlight response (supporting null).
func ParseDocumentHighlights(raw json.RawMessage) ([]DocumentHighlight, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var highlights []DocumentHighlight
	if err := json.Unmarshal(trimmed, &highlights); err != nil {
		return nil, err
	}
	if len(highlights) == 0 {
		return nil, nil
	}
	return highlights, nil
}

// ParseSelectionRanges parses a textDocument/selectionRange response (supporting null).
func ParseSelectionRanges(raw json.RawMessage) ([]SelectionRange, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var ranges []SelectionRange
	if err := json.Unmarshal(trimmed, &ranges); err != nil {
		return nil, err
	}
	if len(ranges) == 0 {
		return nil, nil
	}
	return ranges, nil
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
