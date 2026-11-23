package conceal

import (
	"errors"
	"fmt"
	"strings"

	"github.com/monicafaye/TypX/internal/util"
)

const concealSettingKey = "typst.conceal"

// ToggleSetting toggles or explicitly sets "typst.conceal" in settings.
// When args is empty and "typst.conceal" is unset, the implicit default is
// treated as true (matching internal/config/settings.go), so toggling sets it
// to false. When one argument is provided ("on"/"off"/"true"/"false"), it sets
// the parsed boolean value without mutating settings on error.
func ToggleSetting(settings map[string]any, args ...string) (enabled bool, err error) {
	if settings == nil {
		return false, errors.New("settings map is nil")
	}

	curBool := true
	if cur, ok := settings[concealSettingKey]; ok && cur != nil {
		b, isBool := cur.(bool)
		if !isBool {
			return false, fmt.Errorf("invalid type for %s: %T", concealSettingKey, cur)
		}
		curBool = b
	}

	switch len(args) {
	case 0:
		next := !curBool
		settings[concealSettingKey] = next
		return next, nil
	case 1:
		raw := strings.ToLower(strings.TrimSpace(args[0]))
		b, parseErr := util.ParseBool(raw)
		if parseErr != nil {
			return curBool, fmt.Errorf("invalid conceal value %q: expected on/off/true/false", args[0])
		}
		settings[concealSettingKey] = b
		return b, nil
	default:
		return curBool, fmt.Errorf("too many arguments: expected 0 or 1, got %d", len(args))
	}
}
