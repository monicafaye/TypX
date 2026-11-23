package action

var termdefaults = map[string]string{
	"<Ctrl-q><Ctrl-q>": "Exit",
	"<Ctrl-e><Ctrl-e>": "CommandMode",
	"<Ctrl-w><Ctrl-w>": "NextSplit|FirstSplit",
}

// DefaultBindings returns a merged copy of micro's default keybindings
// overlaid with any TypX extension defaults registered via RegisterDefaultBinding.
func DefaultBindings(pane string) map[string]string {
	var base map[string]string
	switch pane {
	case "command":
		base = infodefaults
	case "buffer":
		base = bufdefaults
	case "terminal":
		base = termdefaults
	default:
		return map[string]string{}
	}

	merged := make(map[string]string, len(base)+len(extDefaultBindings[pane]))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extDefaultBindings[pane] {
		merged[k] = v
	}
	return merged
}
