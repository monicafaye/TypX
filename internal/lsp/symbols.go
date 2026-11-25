package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
)

func (m *Manager) triggerWorkspaceSymbols(bp *action.BufPane, query string, explicitCmd bool) bool {
	if !explicitCmd && (bp == nil || bp.Buf == nil || !bp.Buf.IsTypst()) {
		return false
	}

	m.mu.Lock()
	state := m.State()
	c := m.client
	gen := m.gen
	m.mu.Unlock()

	if state != StateReady || c == nil {
		setInfoMessage(m.notReadyMessage(state, "LSP is not ready"))
		return true
	}

	var absPath string
	var cursorLoc buffer.Loc
	var originTab *action.Tab
	if bp != nil {
		originTab = bp.Tab()
		if bp.Buf != nil {
			absPath = bp.Buf.AbsPath
		}
		if bp.Cursor != nil {
			cursorLoc = bp.Cursor.Loc
		}
	}
	activeKey := canonicalDocKey(absPath)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var raw json.RawMessage
		err := c.Call(ctx, "workspace/symbol", WorkspaceSymbolParams{Query: query}, &raw)
		canceled := ctx.Err() == context.Canceled
		cancel()

		if err != nil {
			if canceled {
				return
			}
			notifyInfoError(fmt.Sprintf("Workspace symbols failed: %v", err))
			return
		}

		syms, parseErr := ParseWorkspaceSymbols(raw)
		if parseErr != nil {
			notifyInfoError(fmt.Sprintf("Workspace symbols parse failed: %v", parseErr))
			return
		}

		action.EnqueueUI(func() {
			m.mu.Lock()
			stale := m.shuttingDown || m.gen != gen
			m.mu.Unlock()
			if stale {
				return
			}

			targetBP := bp
			if (targetBP == nil || targetBP.Tab() == nil) && action.Tabs != nil {
				if mt := action.MainTab(); mt != nil {
					targetBP = mt.CurPane()
				}
			}
			if targetBP == nil {
				return
			}
			if originTab == nil {
				originTab = targetBP.Tab()
			}

			if len(syms) == 0 {
				setInfoMessage("No workspace symbols found")
				return
			}

			sort.SliceStable(syms, func(i, j int) bool {
				pi := URIToPath(syms[i].Location.URI)
				pj := URIToPath(syms[j].Location.URI)
				ki := activeKey != "" && canonicalDocKey(pi) == activeKey
				kj := activeKey != "" && canonicalDocKey(pj) == activeKey
				if ki != kj {
					return ki
				}
				ni := strings.ToLower(syms[i].Name)
				nj := strings.ToLower(syms[j].Name)
				if ni != nj {
					return ni < nj
				}
				if pi != pj {
					return pi < pj
				}
				if syms[i].Location.Range.Start.Line != syms[j].Location.Range.Start.Line {
					return syms[i].Location.Range.Start.Line < syms[j].Location.Range.Start.Line
				}
				return syms[i].Location.Range.Start.Character < syms[j].Location.Range.Start.Character
			})

			items := make([]ListItem, 0, len(syms))
			for _, sym := range syms {
				s := sym
				targetPath := URIToPath(s.Location.URI)
				if targetPath == "" {
					continue
				}
				primary := s.Name
				if s.ContainerName != "" {
					primary = fmt.Sprintf("%s (%s)", s.Name, s.ContainerName)
				}
				items = append(items, ListItem{
					FilePath:  targetPath,
					Line:      s.Location.Range.Start.Line,
					Col:       s.Location.Range.Start.Character,
					Badge:     symbolKindBadge(s.Kind),
					Primary:   primary,
					Secondary: fmt.Sprintf("%s:%d:%d", filepath.Base(targetPath), s.Location.Range.Start.Line+1, s.Location.Range.Start.Character+1),
					OnSelect: func() {
						CloseListPaneForTab(originTab)
						if destBP := m.jumpToLocation(targetBP, s.Location); destBP != nil && absPath != "" {
							m.pushJumpOrigin(destBP.Tab(), absPath, cursorLoc)
						}
					},
				})
			}
			if len(items) == 0 {
				setInfoMessage("No workspace symbols found")
				return
			}
			title := "Workspace Symbols"
			if q := strings.TrimSpace(query); q != "" {
				title = fmt.Sprintf("Workspace Symbols: %s", q)
			}
			OpenListPane(targetBP, title, false, items)
		})
	}()
	return true
}

func symbolKindBadge(kind int) string {
	switch kind {
	case SymbolKindFile, SymbolKindModule, SymbolKindNamespace:
		return "MOD"
	case SymbolKindPackage:
		return "PKG"
	case SymbolKindFunction, SymbolKindMethod:
		return "FN"
	case SymbolKindVariable, SymbolKindConstant, SymbolKindField, SymbolKindProperty:
		return "VAR"
	case SymbolKindString:
		return "HDG"
	case SymbolKindKey:
		return "KEY"
	case SymbolKindClass, SymbolKindStruct:
		return "TYP"
	case SymbolKindObject:
		return "OBJ"
	default:
		return "SYM"
	}
}
