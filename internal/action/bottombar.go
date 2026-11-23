package action

import (
	"strings"

	"github.com/micro-editor/tcell/v2"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/display"
)

var bottomBarMouseDown bool

// HandleBottomBarEvent intercepts mouse clicks on the bottom bar and expanded
// key menu (Alt+G) when no prompt is active, triggering the clicked shortcut
// or dismissing a visible message/error. Returns true if the event was consumed.
func HandleBottomBarEvent(event tcell.Event) bool {
	me, ok := event.(*tcell.EventMouse)
	if !ok {
		return false
	}
	if InfoBar == nil || InfoBar.HasPrompt {
		bottomBarMouseDown = false
		return false
	}
	if infobar, ok := config.GetGlobalOption("infobar").(bool); !ok || !infobar {
		bottomBarMouseDown = false
		return false
	}
	if Tabs != nil && len(Tabs.List) > 0 && MainTab().IsResizing() {
		return false
	}

	iw, ok := InfoBar.BWindow.(*display.InfoWindow)
	if !ok || iw == nil {
		return false
	}

	mx, my := me.Position()
	yBottom := iw.Y
	yTop := yBottom
	if km, ok := config.GetGlobalOption("keymenu").(bool); ok && km {
		yTop = yBottom - 2
	}

	btn := me.Buttons()
	if btn == tcell.ButtonNone {
		if bottomBarMouseDown {
			bottomBarMouseDown = false
			return true
		}
		return my >= yTop && my <= yBottom
	}

	if my < yTop || my > yBottom {
		return false
	}

	if btn == tcell.Button1 {
		if bottomBarMouseDown {
			// Ignore drag/repeat while button remains held on the bottom bar.
			return true
		}
		bottomBarMouseDown = true

		if act, hit := iw.HitAt(mx, my); hit {
			executeBottomBarAction(act)
			return true
		}
		// Clicking anywhere on a visible message or error bar dismisses it.
		if InfoBar.HasError || (InfoBar.HasMessage && strings.TrimSpace(InfoBar.Msg) != "") {
			InfoBar.Reset()
		}
	}

	return true
}

func executeBottomBarAction(act string) {
	if act == "" {
		return
	}
	if act == "dismiss" {
		if InfoBar != nil {
			InfoBar.Reset()
		}
		return
	}

	if strings.HasPrefix(act, "pane:") {
		keySpec := strings.TrimPrefix(act, "pane:")
		if Tabs != nil && len(Tabs.List) > 0 {
			if ap := MainTab().ActivePane(); ap != nil {
				if ev := syntheticKeyEvent(keySpec); ev != nil {
					ap.HandleEvent(ev)
				}
			}
		}
		return
	}

	if Tabs == nil || len(Tabs.List) == 0 {
		return
	}
	bp := MainTab().CurPane()
	if bp == nil {
		return
	}

	if strings.HasPrefix(act, "command:") {
		cmd := strings.TrimPrefix(act, "command:")
		bp.HandleCommand(cmd)
		return
	}

	if fn, ok := BufKeyActions[act]; ok {
		fn(bp)
	}
}

func syntheticKeyEvent(spec string) *tcell.EventKey {
	switch spec {
	case "Esc":
		return tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone, "")
	case "Enter":
		return tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone, "")
	case "Up":
		return tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone, "")
	case "Down":
		return tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, "")
	default:
		runes := []rune(spec)
		if len(runes) == 1 {
			return tcell.NewEventKey(tcell.KeyRune, runes[0], tcell.ModNone, "")
		}
		return nil
	}
}
