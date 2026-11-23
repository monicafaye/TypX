package action

import (
	"github.com/monicafaye/TypX/internal/display"
)

// A Pane is a general interface for a window in the editor.
type Pane interface {
	Handler
	display.Window
	ID() uint64
	SetID(i uint64)
	Name() string
	Close()
	SetTab(t *Tab)
	Tab() *Tab
}

// AuxiliaryPane is optionally implemented by docked sidebar/tool panes (such as
// the Document Outline) that accompany primary editor panes rather than keeping
// a tab or editor session alive on their own.
type AuxiliaryPane interface {
	Pane
	IsAuxiliary() bool
}

// IsAuxiliaryPane reports whether p is an auxiliary/sidebar pane.
func IsAuxiliaryPane(p Pane) bool {
	if aux, ok := p.(AuxiliaryPane); ok {
		return aux.IsAuxiliary()
	}
	return false
}
