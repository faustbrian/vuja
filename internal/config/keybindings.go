package config

import (
	"bytes"
	"fmt"
	"strings"
)

var reservedLineEditingKeys = map[byte]bool{
	0x01: true, // ctrl+a
	0x03: true, // ctrl+c
	0x05: true, // ctrl+e
	0x08: true, // ctrl+h / backspace
	0x0a: true, // ctrl+j / enter
	0x0c: true, // ctrl+l
	0x0d: true, // ctrl+m / enter
	0x15: true, // ctrl+u
	0x17: true, // ctrl+w
	0x1b: true, // ctrl+[ / escape
	0x7f: true, // backspace
}

func KeySequences(binding string) ([][]byte, error) {
	switch normalized := strings.ToLower(strings.TrimSpace(binding)); normalized {
	case "none":
		return nil, nil
	case "tab":
		return [][]byte{{0x09}}, nil
	case "shift+tab":
		return [][]byte{{0x1b, '[', 'Z'}}, nil
	case "up":
		return [][]byte{{0x1b, '[', 'A'}, {0x1b, 'O', 'A'}}, nil
	case "down":
		return [][]byte{{0x1b, '[', 'B'}, {0x1b, 'O', 'B'}}, nil
	case "page-up":
		return [][]byte{{0x1b, '[', '5', '~'}}, nil
	case "page-down":
		return [][]byte{{0x1b, '[', '6', '~'}}, nil
	case "alt+right":
		return [][]byte{{0x1b, '[', '1', ';', '3', 'C'}}, nil
	case "ctrl+right":
		return [][]byte{{0x1b, '[', '1', ';', '5', 'C'}}, nil
	case "meta+right":
		return [][]byte{{0x1b, '[', '1', ';', '9', 'C'}}, nil
	case "home":
		return [][]byte{{0x1b, '[', 'H'}, {0x1b, 'O', 'H'}, {0x1b, '[', '1', '~'}}, nil
	case "end":
		return [][]byte{{0x1b, '[', 'F'}, {0x1b, 'O', 'F'}, {0x1b, '[', '4', '~'}}, nil
	default:
		if strings.HasPrefix(normalized, "ctrl+") && len(normalized) == len("ctrl+a") {
			letter := normalized[len(normalized)-1]
			if letter >= 'a' && letter <= 'z' {
				return [][]byte{{letter - 'a' + 1}}, nil
			}
		}
		return nil, fmt.Errorf("unsupported key %q", binding)
	}
}

func validateKeybindings(bindings KeybindingsConfig) error {
	if bindings.Keymap != "emacs" && bindings.Keymap != "vi" {
		return fmt.Errorf("keybindings.keymap: invalid value %q (want: emacs|vi)", bindings.Keymap)
	}
	actions := []struct {
		name     string
		bindings []string
		lineEdit bool
	}{
		{name: "history-search", bindings: []string{bindings.HistorySearch}},
		{name: "history-success-only", bindings: []string{bindings.HistorySuccessOnly}},
		{name: "accept", bindings: []string{bindings.Accept}},
		{name: "toggle-overlay", bindings: []string{bindings.ToggleOverlay}},
		{name: "accept-token", bindings: bindings.AcceptToken},
		{name: "navigation-up", bindings: []string{bindings.NavigationUp}},
		{name: "navigation-down", bindings: []string{bindings.NavigationDown}},
		{name: "navigation-page-up", bindings: []string{bindings.NavigationPageUp}},
		{name: "navigation-page-down", bindings: []string{bindings.NavigationPageDown}},
		{name: "navigation-first", bindings: []string{bindings.NavigationFirst}},
		{name: "navigation-last", bindings: []string{bindings.NavigationLast}},
	}
	for name, binding := range bindings.ResolvedLineEditingBindings() {
		actions = append(actions, struct {
			name     string
			bindings []string
			lineEdit bool
		}{name: name, bindings: []string{binding}, lineEdit: true})
	}

	owners := make(map[string]string)
	for _, action := range actions {
		disabled := false
		for _, binding := range action.bindings {
			sequences, err := KeySequences(binding)
			if err != nil {
				return fmt.Errorf("keybindings.%s: %w", action.name, err)
			}
			if len(sequences) == 0 {
				disabled = true
				continue
			}
			if disabled {
				return fmt.Errorf("keybindings.%s: none cannot be combined with another key", action.name)
			}
			for _, sequence := range sequences {
				if action.name == "accept" && len(sequence) != 1 {
					return fmt.Errorf("keybindings.accept: %q must be a tab or control key", binding)
				}
				if !action.lineEdit && len(sequence) == 1 && reservedLineEditingKeys[sequence[0]] {
					return fmt.Errorf("keybindings.%s: %q is reserved for line editing", action.name, binding)
				}
				key := string(sequence)
				if owner, exists := owners[key]; exists {
					return fmt.Errorf("keybindings.%s: %q conflicts with keybindings.%s", action.name, binding, owner)
				}
				owners[key] = action.name
			}
		}
		if disabled && len(action.bindings) > 1 {
			return fmt.Errorf("keybindings.%s: none cannot be combined with another key", action.name)
		}
	}

	return nil
}

func KeySequenceMatches(data []byte, index int, sequences [][]byte) int {
	if index < 0 || index >= len(data) {
		return 0
	}
	for _, sequence := range sequences {
		if len(sequence) > 0 && index+len(sequence) <= len(data) &&
			bytes.Equal(data[index:index+len(sequence)], sequence) {
			return len(sequence)
		}
	}
	return 0
}
