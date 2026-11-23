package action

import (
	"testing"

	"github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
)

func TestAltShiftFallbackKeyEvents(t *testing.T) {
	// Windows ConPTY / Win32 Console delivers Alt+Shift+M as KeyRune 'M' with ModAlt|ModShift.
	ke := keyEvent(tcell.NewEventKey(tcell.KeyRune, 'M', tcell.ModAlt|tcell.ModShift, ""))
	fbs := altShiftFallbackKeyEvents(ke)
	assert.Equal(t, []KeyEvent{{code: tcell.KeyRune, mod: tcell.ModAlt, r: 'M'}}, fbs)

	// Shifted symbol with Alt (e.g. Alt-{).
	keBrace := keyEvent(tcell.NewEventKey(tcell.KeyRune, '{', tcell.ModAlt|tcell.ModShift, ""))
	assert.Equal(t, []KeyEvent{{code: tcell.KeyRune, mod: tcell.ModAlt, r: '{'}}, altShiftFallbackKeyEvents(keBrace))

	// Caps Lock + Alt+Shift+M delivers lowercase 'm' with ModAlt|ModShift: prefer Alt-M first.
	keCaps := keyEvent(tcell.NewEventKey(tcell.KeyRune, 'm', tcell.ModAlt|tcell.ModShift, ""))
	assert.Equal(t, []KeyEvent{
		{code: tcell.KeyRune, mod: tcell.ModAlt, r: 'M'},
		{code: tcell.KeyRune, mod: tcell.ModAlt, r: 'm'},
	}, altShiftFallbackKeyEvents(keCaps))

	// Non-Alt or non-Shift or non-Rune events should have no fallbacks.
	assert.Nil(t, altShiftFallbackKeyEvents(keyEvent(tcell.NewEventKey(tcell.KeyRune, 'M', tcell.ModShift, ""))))
	assert.Nil(t, altShiftFallbackKeyEvents(keyEvent(tcell.NewEventKey(tcell.KeyRune, 'M', tcell.ModAlt, ""))))
	assert.Nil(t, altShiftFallbackKeyEvents(keyEvent(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModAlt|tcell.ModShift, ""))))
}
